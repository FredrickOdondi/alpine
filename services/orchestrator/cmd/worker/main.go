// Command worker runs Alpine's Temporal workers: the ticket workflow plus activity workers per
// task queue (control, gates, and agents by priority class).
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"go.temporal.io/sdk/client"
	temporalotel "go.temporal.io/sdk/contrib/opentelemetry"
	"go.temporal.io/sdk/interceptor"
	tlog "go.temporal.io/sdk/log"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/alpine-dev/alpine/packages/agents"
	"github.com/alpine-dev/alpine/packages/gates"
	"github.com/alpine-dev/alpine/packages/shared/budget"
	"github.com/alpine-dev/alpine/packages/shared/config"
	"github.com/alpine-dev/alpine/packages/shared/db"
	"github.com/alpine-dev/alpine/packages/shared/events"
	"github.com/alpine-dev/alpine/packages/shared/gate"
	"github.com/alpine-dev/alpine/packages/shared/keys"
	"github.com/alpine-dev/alpine/packages/shared/llm"
	"github.com/alpine-dev/alpine/packages/shared/ratelimit"
	"github.com/alpine-dev/alpine/packages/shared/repo/pgrepo"
	"github.com/alpine-dev/alpine/packages/shared/telemetry"
	"github.com/alpine-dev/alpine/packages/shared/ticketflow"
	"github.com/alpine-dev/alpine/packages/shared/ticketrepo"
	"github.com/alpine-dev/alpine/services/integrations/github"
	"github.com/alpine-dev/alpine/services/orchestrator/internal/activities"
	"github.com/alpine-dev/alpine/services/orchestrator/internal/adapters"
	"github.com/alpine-dev/alpine/services/orchestrator/internal/workflows"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level(config.String("LOG_LEVEL", "info"))}))
	slog.SetDefault(logger)
	if err := run(logger); err != nil {
		logger.Error("worker exited", "err", err)
		os.Exit(1)
	}
}

func level(s string) slog.Level {
	var l slog.Level
	_ = l.UnmarshalText([]byte(s))
	return l
}

func run(logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdown, err := telemetry.Setup(ctx, "alpine-orchestrator", "0.1.0")
	if err != nil {
		return err
	}
	defer func() { _ = shutdown(context.Background()) }()

	common := config.LoadCommon()
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
	if err := rdb.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis: %w", err)
	}
	store := &ticketrepo.Store{DB: database}
	metering := &pgrepo.Metering{DB: database, Defaults: llmCfg.Limits}

	ag, err := buildAgents(logger, metering, rdb, llmCfg)
	if err != nil {
		return err
	}

	tracing, err := temporalotel.NewTracingInterceptor(temporalotel.TracerOptions{})
	if err != nil {
		return err
	}
	tc, err := client.DialContext(ctx, client.Options{
		HostPort:     common.TemporalHostPort,
		Namespace:    common.TemporalNamespace,
		Logger:       tlog.NewStructuredLogger(logger),
		Interceptors: []interceptor.ClientInterceptor{tracing},
	})
	if err != nil {
		return fmt.Errorf("temporal: %w", err)
	}
	defer tc.Close()

	scm, err := buildSCM(logger)
	if err != nil {
		return err
	}
	acts := &activities.Activities{
		Repo:   store,
		Agents: ag,
		Gates:  buildGates(logger),
		SCM:    scm,
		Slots:  ratelimit.Semaphore{R: rdb},
		Events: events.Redis{Client: rdb},
		Logger: logger,
	}

	base := config.Int("WORKER_AGENT_CONCURRENCY", 8)
	queues := []struct {
		name      string
		workflows bool
		conc      int
	}{
		{ticketflow.TaskQueueWorkflows, true, 0},
		{ticketflow.TaskQueueControl, false, 50},
		{ticketflow.TaskQueueGates, false, config.Int("WORKER_GATES_CONCURRENCY", 4)},
		// Priority classes: high gets twice the slots of normal, low gets half.
		{ticketflow.TaskQueueAgentsHi, false, base * 2},
		{ticketflow.TaskQueueAgentsMid, false, base},
		{ticketflow.TaskQueueAgentsLow, false, max(1, base/2)},
	}
	var workers []worker.Worker
	for _, q := range queues {
		w := worker.New(tc, q.name, worker.Options{
			MaxConcurrentActivityExecutionSize: q.conc,
			EnableSessionWorker:                false,
		})
		if q.workflows {
			w.RegisterWorkflowWithOptions(workflows.TicketWorkflow, workflow.RegisterOptions{Name: ticketflow.WorkflowName})
		} else {
			w.RegisterActivity(acts)
		}
		if err := w.Start(); err != nil {
			return fmt.Errorf("start worker %s: %w", q.name, err)
		}
		workers = append(workers, w)
		logger.Info("worker started", "task_queue", q.name)
	}
	logger.Info("orchestrator ready", "agent_mode", config.String("ALPINE_AGENT_MODE", "stub"))
	<-ctx.Done()
	for _, w := range workers {
		w.Stop()
	}
	return nil
}

// buildSCM returns the GitHub App client adapter (SCM=github, or legacy GITHUB_LIVE=true; needs
// GITHUB_APP_ID, GITHUB_APP_BOT_LOGIN and GITHUB_APP_PRIVATE_KEY_FILE) or StubSCM (default: no
// GitHub calls, deterministic fake branches/PRs/comments).
func buildSCM(logger *slog.Logger) (activities.SCM, error) {
	mode := config.String("SCM", "")
	if mode == "" && config.Bool("GITHUB_LIVE", false) {
		mode = "github"
	}
	if mode != "github" {
		logger.Info("SCM: stub (no GitHub calls); set SCM=github to use the GitHub App")
		return activities.StubSCM{}, nil
	}
	gh, err := config.LoadGitHub()
	if err != nil {
		return nil, err
	}
	keyFile := config.String("GITHUB_APP_PRIVATE_KEY_FILE", "")
	if gh.AppID == 0 || gh.BotLogin == "" || keyFile == "" {
		return nil, fmt.Errorf("SCM=github needs GITHUB_APP_ID, GITHUB_APP_BOT_LOGIN and GITHUB_APP_PRIVATE_KEY_FILE")
	}
	pem, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, fmt.Errorf("GITHUB_APP_PRIVATE_KEY_FILE: %w", err)
	}
	key, err := github.ParsePrivateKeyPEM(pem)
	if err != nil {
		return nil, err
	}
	app, err := github.NewApp(github.AppConfig{AppID: gh.AppID, PrivateKey: key, BotLogin: gh.BotLogin, UserAgent: "alpine-orchestrator"})
	if err != nil {
		return nil, err
	}
	logger.Info("SCM: GitHub App", "app_id", gh.AppID, "bot", gh.BotLogin)
	return &adapters.GitHubSCM{Clients: adapters.AppRepoClients(app)}, nil
}

// buildGates returns lena/gates' runner (GATES=real; runs the repo's configured gates in the
// ticket checkout, so it needs a sandbox/checkout at RunRequest.RepoDir) or StubGates (default:
// fabricated results, failing typecheck for the first STUB_GATE_FAILURES iterations).
func buildGates(logger *slog.Logger) gate.Runner {
	if config.String("GATES", "stub") != "real" {
		logger.Info("gates: stub; set GATES=real to run packages/gates in the ticket checkout")
		return activities.StubGates{FailIterations: config.Int("STUB_GATE_FAILURES", 1)}
	}
	r := gates.NewRunner(nil)
	r.ArtifactsRoot = config.String("GATES_ARTIFACTS_DIR", "")
	r.ExtraEnv = gatesExtraEnv(os.Environ())
	logger.Info("gates: packages/gates SharedRunner", "extra_env", len(r.ExtraEnv))
	return adapters.RepoGates{Runner: r}
}

// orgCapsDefaults applies platform default org caps where the org has no explicit cap.
type orgCapsDefaults struct {
	budget.Source
	Defaults budget.OrgCaps
}

func (o orgCapsDefaults) OrgCaps(ctx context.Context, orgID string) (budget.OrgCaps, error) {
	c, err := o.Source.OrgCaps(ctx, orgID)
	if err != nil {
		return o.Defaults, err
	}
	if c.DailyCostUSD == 0 {
		c.DailyCostUSD = o.Defaults.DailyCostUSD
	}
	if c.MonthlyCostUSD == 0 {
		c.MonthlyCostUSD = o.Defaults.MonthlyCostUSD
	}
	if c.DailyTokens == 0 {
		c.DailyTokens = o.Defaults.DailyTokens
	}
	if c.MonthlyTokens == 0 {
		c.MonthlyTokens = o.Defaults.MonthlyTokens
	}
	return c, nil
}

// buildAgents returns the stub engine (default for local dev) or the LLM engine wired to the
// gateway with all reliability layers. Keys, limits, budgets and usage go through jeff/data's
// repo/pgrepo.Metering.
func buildAgents(logger *slog.Logger, metering *pgrepo.Metering, rdb *redis.Client, cfg config.LLM) (agents.Agents, error) {
	if config.String("ALPINE_AGENT_MODE", "stub") != "llm" {
		return agents.Stub{TurnsPerAttempt: config.Int("STUB_TURNS_PER_ATTEMPT", 3)}, nil
	}
	pricing, err := llm.LoadPricing(cfg.PricingFile)
	if err != nil {
		return nil, err
	}
	var resolver llm.KeyResolver = llm.StaticKeys{}
	if cfg.LocalKEK != "" {
		kek, err := keys.NewLocalKEK("local", cfg.LocalKEK.Reveal())
		if err != nil {
			return nil, err
		}
		resolver = &keys.Resolver{Store: metering, KEK: kek, Provider: "openai", AllowPlatformKey: cfg.AllowPlatformKey}
	}
	gw, err := llm.NewGateway(llm.Options{
		Provider:       &llm.OpenAICompatible{BaseURL: cfg.BaseURL, APIKey: cfg.APIKey, BYOKInBody: cfg.BYOKInBody, HTTP: nil},
		Routing:        cfg.Routing,
		Keys:           resolver,
		Limiter:        &ratelimit.Limiter{Redis: rdb, Provider: metering},
		Budget:         &budget.Checker{Source: orgCapsDefaults{Source: metering, Defaults: cfg.OrgCaps}},
		Recorder:       metering,
		Idempotency:    &llm.RedisIdempotency{Redis: rdb, TTL: 7 * 24 * time.Hour},
		Pricing:        pricing,
		Retry:          cfg.Retry,
		Breaker:        cfg.Breaker,
		MaxRepairs:     cfg.MaxRepairs,
		RedactOutbound: cfg.RedactOutbound,
		LogPrompts:     cfg.LogPrompts,
		Logger:         logger,
	})
	if err != nil {
		return nil, err
	}
	return &agents.LLM{Gateway: gw}, nil
}

// gatesExtraEnv collects gates-only values from GATES_ENV_<NAME>=value variables into
// Runner.ExtraEnv as NAME=value. These are the values for the names a repo declares in
// gate_runner.extra_env; they reach gate commands only, never the implementer sandbox, and the
// worker's other variables (DATABASE_URL, keys) are never passed through. Phase 0 reads them from
// the worker env; a per-org secret store replaces this later.
func gatesExtraEnv(environ []string) []string {
	const prefix = "GATES_ENV_"
	var out []string
	for _, kv := range environ {
		if !strings.HasPrefix(kv, prefix) {
			continue
		}
		if k, v, ok := strings.Cut(kv[len(prefix):], "="); ok && k != "" {
			out = append(out, k+"="+v)
		}
	}
	return out
}
