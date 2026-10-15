// Command api serves the Alpine HTTP API: REST + SSE, GitHub/Sentry webhook receivers, and the
// inbox processor that turns webhooks into tickets and Temporal workflows.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.temporal.io/sdk/client"
	tlog "go.temporal.io/sdk/log"

	sharedauth "github.com/alpine-dev/alpine/packages/shared/auth"
	"github.com/alpine-dev/alpine/packages/shared/budget"
	"github.com/alpine-dev/alpine/packages/shared/config"
	"github.com/alpine-dev/alpine/packages/shared/db"
	"github.com/alpine-dev/alpine/packages/shared/events"
	"github.com/alpine-dev/alpine/packages/shared/githubapp"
	"github.com/alpine-dev/alpine/packages/shared/keys"
	"github.com/alpine-dev/alpine/packages/shared/repo/pgrepo"
	"github.com/alpine-dev/alpine/packages/shared/telemetry"
	"github.com/alpine-dev/alpine/packages/shared/ticketrepo"
	"github.com/alpine-dev/alpine/services/api/internal/auth"
	"github.com/alpine-dev/alpine/services/api/internal/authapi"
	"github.com/alpine-dev/alpine/services/api/internal/inbox"
	"github.com/alpine-dev/alpine/services/api/internal/onboarding"
	"github.com/alpine-dev/alpine/services/api/internal/server"
	"github.com/alpine-dev/alpine/services/api/internal/webhook"
	"github.com/alpine-dev/alpine/services/api/internal/workflows"
	"github.com/alpine-dev/alpine/services/integrations/sentry"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)
	if err := run(logger); err != nil {
		logger.Error("api exited", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	shutdown, err := telemetry.Setup(ctx, "alpine-api", "0.1.0")
	if err != nil {
		return err
	}
	defer func() { _ = shutdown(context.Background()) }()

	common := config.LoadCommon()
	gh, err := config.LoadGitHub()
	if err != nil {
		return err
	}
	llmCfg, err := config.LoadLLM()
	if err != nil {
		return err
	}
	database, err := db.Open(ctx, common.DatabaseURL)
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	defer database.Close()
	ropts, err := redis.ParseURL(common.RedisURL)
	if err != nil {
		return err
	}
	rdb := redis.NewClient(ropts)
	defer rdb.Close()
	tc, err := client.DialContext(ctx, client.Options{HostPort: common.TemporalHostPort, Namespace: common.TemporalNamespace,
		Logger: tlog.NewStructuredLogger(logger)})
	if err != nil {
		return fmt.Errorf("temporal: %w", err)
	}
	defer tc.Close()

	// Pipeline tables (tickets, inbox, runs, PRs, Sentry routing): ticketrepo. Identity, orgs,
	// sessions, API keys, audit: jeff/data's sqlc repos behind the shared auth service.
	st := &ticketrepo.Store{DB: database}
	authCfg, err := sharedauth.ConfigFromEnv()
	if err != nil {
		return fmt.Errorf("auth config: %w", err)
	}
	var mailer sharedauth.Mailer = sharedauth.LogMailer{Logger: logger}
	if addr := config.String("SMTP_ADDR", ""); addr != "" {
		mailer = &sharedauth.SMTPMailer{Addr: addr, From: authCfg.MailFrom, Username: config.String("SMTP_USERNAME", ""),
			Password: config.String("SMTP_PASSWORD", "")}
	}
	authSvc := sharedauth.New(authCfg, pgrepo.New(database), rdb, mailer, logger)

	engine := workflows.Temporal{Client: tc}
	bus := events.Redis{Client: rdb}

	proc := &inbox.Processor{
		Store: st, Sentry: st, Engine: engine, Events: bus, Logger: logger,
		Normalizer: inbox.Normalizers(gh.BotLogin),
		// Per-ticket hard caps (0 = platform default: 100 turns, 15 gate runs, $20).
		Caps: budget.TicketCaps{MaxTurns: config.Int("TICKET_MAX_TURNS", 0), MaxGateIterations: config.Int("TICKET_MAX_GATE_ITERATIONS", 0),
			MaxCostUSD: config.Float("TICKET_MAX_COST_USD", 0), MaxTokens: int64(config.Int("TICKET_MAX_TOKENS", 0))},
		MaxParallelTicketsOrg:  llmCfg.MaxParallelTicketsOrg,
		MaxParallelTicketsRepo: llmCfg.MaxParallelTicketsRepo,
	}
	// Platform GitHub App: GITHUB_APP_* env, else the registration created through the manifest
	// flow (encrypted in Postgres with ALPINE_LOCAL_KEK, or a 0600 dev file without a KEK).
	var kek keys.KEK
	if k := config.String("ALPINE_LOCAL_KEK", ""); k != "" {
		lk, err := keys.NewLocalKEK("local", k)
		if err != nil {
			return fmt.Errorf("ALPINE_LOCAL_KEK: %w", err)
		}
		kek = lk
	}
	apps, err := githubapp.Open(os.Getenv, database, kek, config.String("GITHUB_APP_CREDENTIALS_FILE", ".alpine-local/github-app.json"))
	if err != nil {
		return err
	}
	proc.Installations = st
	onb := &onboarding.Service{Apps: apps, Store: st, States: onboarding.RedisStates{Redis: rdb},
		Emails: func(ctx context.Context, id uuid.UUID) (string, error) {
			u, err := authSvc.Store.GetUser(ctx, id)
			return u.Email, err
		},
		HTTP:         &http.Client{Timeout: 20 * time.Second},
		GitHubAPIURL: gh.APIBaseURL, GitHubWebURL: config.String("GITHUB_WEB_URL", "https://github.com"),
		PublicAPIURL: authCfg.PublicAPIURL, AppURL: authCfg.AppURL, WebhookProxyURL: config.String("WEBHOOK_PROXY_URL", ""),
		Operators: splitList(config.String("PLATFORM_OPERATOR_EMAILS", "")), CookieSecure: authCfg.Cookie.Secure, Logger: logger}
	switch {
	case apps.Env != nil:
		logger.Info("github app: from GITHUB_APP_* env", "app_id", apps.Env.AppID)
	case kek != nil:
		logger.Info("github app: registration stored encrypted in Postgres (manifest flow at /setup/github-app)")
	default:
		logger.Warn("github app: no ALPINE_LOCAL_KEK, so a registration is kept in a 0600 local file (dev only)",
			"file", config.String("GITHUB_APP_CREDENTIALS_FILE", ".alpine-local/github-app.json"))
	}
	rc := &webhook.Receiver{Inbox: st, GitHubVerifier: onboarding.WebhookVerifier{Apps: apps, Static: []byte(gh.WebhookSecret.Reveal())},
		SentryVerifier: sentry.Verifier{ClientSecret: []byte(config.String("SENTRY_CLIENT_SECRET", ""))},
		SentryOrg:      st, Logger: logger, Notify: proc.Notify}

	srv := &server.Server{Server: authapi.New(authSvc), Store: st, Authenticator: auth.Principals{},
		Engine: engine, Events: bus, Webhooks: rc, Logger: logger, Onboarding: onb,
		Artifacts: server.StaticArtifactURLs{Base: strings.TrimRight(config.String("S3_PUBLIC_URL", "http://localhost:9000/"+config.String("S3_BUCKET", "alpine-artifacts")), "/")},
		Ready: map[string]server.Checker{
			"postgres": func(ctx context.Context) error { return database.Pool.Ping(ctx) },
			"redis":    func(ctx context.Context) error { return rdb.Ping(ctx).Err() },
			"temporal": engine.Ping,
		}}
	handler := srv.Handler()
	if config.Bool("AUTH_DEV_STUB", false) {
		stub, err := auth.LookupDevStub(ctx, authSvc.Store, authCfg.DevLoginEmail, authCfg.Cookie.Name)
		if err != nil {
			return err
		}
		handler = stub.Middleware(handler)
		logger.Warn("AUTH_DEV_STUB=true: requests without credentials act as the dev user; never use in production",
			"user", authCfg.DevLoginEmail, "org", stub.OrgID, "csrf_token", auth.DevStubCSRFToken)
	}
	go proc.Run(ctx)

	addr := config.String("API_ADDR", ":8080")
	hs := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = hs.Shutdown(sctx)
	}()
	logger.Info("api listening", "addr", addr)
	if err := hs.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
