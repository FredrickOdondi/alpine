// Package onboarding implements customer GitHub onboarding without seed scripts:
//
//  1. Operator setup: build a GitHub App manifest, let the operator click "Create GitHub App" on
//     github.com, and exchange the returned code for the App's id, slug, private key, webhook
//     secret and OAuth client (stored via githubapp: encrypted in the DB, or a 0600 dev file).
//  2. Connect GitHub: send a workspace admin to the App's install page with a state tied to their
//     org; on the way back, prove the user can access the installation (user-to-server OAuth ->
//     GET /user/installations), check it is an installation of this App, and link it to the org.
//  3. Repo picker: list the installation's repositories with an installation token, enable or
//     disable them, read .alpine/factory.yaml from the default branch, and open a PR with a
//     starter config when there is none.
//
// Every GitHub call goes to configurable base URLs (GITHUB_API_URL / GITHUB_WEB_URL), so tests
// and the Playwright run use a fake GitHub at the HTTP layer.
package onboarding

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	sharedauth "github.com/alpine-dev/alpine/packages/shared/auth"
	"github.com/alpine-dev/alpine/packages/shared/githubapp"
	"github.com/alpine-dev/alpine/packages/shared/intake"
	"github.com/alpine-dev/alpine/packages/shared/ticketrepo"
	"github.com/alpine-dev/alpine/services/integrations/github"
)

// Store is the persistence the onboarding flows need (ticketrepo.Store implements it).
type Store interface {
	LinkInstallation(ctx context.Context, org uuid.UUID, in ticketrepo.LinkInstallationInput) (ticketrepo.Installation, error)
	ListInstallations(ctx context.Context, org uuid.UUID) ([]ticketrepo.Installation, error)
	GetInstallation(ctx context.Context, org uuid.UUID, installationID int64) (ticketrepo.Installation, error)
	MarkInstallationUninstalled(ctx context.Context, org uuid.UUID, installationID int64) (bool, error)
	RepoStates(ctx context.Context, org uuid.UUID, installationID int64) (map[int64]ticketrepo.RepoState, error)
	SetRepoEnabled(ctx context.Context, org uuid.UUID, ref intake.RepoRef, enabled bool) (ticketrepo.RepoState, error)
	SaveFactoryConfig(ctx context.Context, org, repoID uuid.UUID, commitSHA, raw string, parsed any) error
	GetRepo(ctx context.Context, org, id uuid.UUID) (ticketrepo.Repo, error)
	Audit(ctx context.Context, org uuid.UUID, actor, action, targetType, targetID string, meta map[string]any) error
}

// StateStore keeps single-use flow states (Redis in production).
type StateStore interface {
	Put(ctx context.Context, key string, val []byte, ttl time.Duration) error
	Take(ctx context.Context, key string) ([]byte, error) // get + delete; ErrStateUnknown if missing
}

// ErrStateUnknown means the state is missing, expired or already used.
var ErrStateUnknown = errors.New("onboarding: unknown or expired state")

// RedisStates implements StateStore.
type RedisStates struct{ Redis *redis.Client }

// Put implements StateStore.
func (r RedisStates) Put(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	return r.Redis.Set(ctx, key, val, ttl).Err()
}

// Take implements StateStore.
func (r RedisStates) Take(ctx context.Context, key string) ([]byte, error) {
	b, err := r.Redis.GetDel(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrStateUnknown
	}
	return b, err
}

// UserEmails resolves a user's email for the operator check.
type UserEmails func(ctx context.Context, userID uuid.UUID) (string, error)

// Service implements the onboarding endpoints' logic.
type Service struct {
	Apps   *githubapp.Cached
	Store  Store
	States StateStore
	Emails UserEmails

	HTTP         *http.Client
	GitHubAPIURL string // default https://api.github.com
	GitHubWebURL string // default https://github.com

	PublicAPIURL    string // e.g. http://localhost:8080
	AppURL          string // e.g. http://localhost:5173
	WebhookProxyURL string // smee.io channel for local dev (optional)
	Operators       []string
	CookieSecure    bool
	Logger          *slog.Logger
	Now             func() time.Time

	mu      sync.Mutex
	app     *github.App
	appKey  string
	stateMu sync.Mutex
}

// StateCookie binds a GitHub flow to the browser that started it.
const StateCookie = "alpine_gh_state"

const (
	flowManifest = "manifest"
	flowInstall  = "install"
	stateTTL     = 15 * time.Minute
)

type flow struct {
	Kind           string    `json:"kind"`
	UserID         uuid.UUID `json:"userId"`
	OrgID          uuid.UUID `json:"orgId,omitempty"`
	InstallationID int64     `json:"installationId,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
	Reauthorized   int       `json:"reauthorized,omitempty"`
}

func (s *Service) log() *slog.Logger {
	if s.Logger != nil {
		return s.Logger
	}
	return slog.Default()
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func stateKey(token string) string {
	h := sha256.Sum256([]byte("alpine:gh-flow:" + token))
	return "alpine:ghflow:" + hex.EncodeToString(h[:])
}

func (s *Service) setCookie(w http.ResponseWriter, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: StateCookie, Value: token, Path: "/v1/", HttpOnly: true, Secure: s.CookieSecure,
		SameSite: http.SameSiteLaxMode, MaxAge: maxAge})
}

// newState stores f under a fresh random token and binds it to this browser with a cookie.
func (s *Service) newState(ctx context.Context, w http.ResponseWriter, f flow) (string, error) {
	f.CreatedAt = s.now().UTC()
	token := sharedauth.RandomToken(32)
	b, _ := json.Marshal(f)
	if err := s.States.Put(ctx, stateKey(token), b, stateTTL); err != nil {
		return "", fmt.Errorf("onboarding: store state: %w", err)
	}
	s.setCookie(w, token, int(stateTTL.Seconds()))
	return token, nil
}

// Flow errors (mapped to ?error= codes on the SPA redirect).
var (
	ErrStateMismatch = errors.New("state_mismatch")
	ErrWrongUser     = errors.New("state_user_mismatch")
)

// takeState consumes the flow named by the cookie. queryState, when GitHub returned one, must
// equal the cookie. The session user must be the user who started the flow.
func (s *Service) takeState(ctx context.Context, w http.ResponseWriter, r *http.Request, kind, queryState string, user uuid.UUID) (flow, string, error) {
	c, err := r.Cookie(StateCookie)
	if err != nil || c.Value == "" {
		return flow{}, "", ErrStateMismatch
	}
	if queryState != "" && !sharedauth.ConstantTimeEqual(c.Value, queryState) {
		return flow{}, "", ErrStateMismatch
	}
	s.setCookie(w, "", -1)
	b, err := s.States.Take(ctx, stateKey(c.Value))
	if err != nil {
		return flow{}, "", ErrStateUnknown
	}
	var f flow
	if json.Unmarshal(b, &f) != nil || f.Kind != kind {
		return flow{}, "", ErrStateMismatch
	}
	if user == uuid.Nil || f.UserID != user {
		return flow{}, "", ErrWrongUser
	}
	return f, c.Value, nil
}

// ---- GitHub App access ----------------------------------------------------------------------

// ErrAppNotConfigured is returned when no App is registered.
var ErrAppNotConfigured = githubapp.ErrNotConfigured

func (s *Service) creds(ctx context.Context) (githubapp.Credentials, error) {
	if s.Apps == nil {
		return githubapp.Credentials{}, ErrAppNotConfigured
	}
	return s.Apps.Load(ctx)
}

// githubApp returns an App client for the current credentials (rebuilt when they change).
func (s *Service) githubApp(ctx context.Context) (*github.App, githubapp.Credentials, error) {
	c, err := s.creds(ctx)
	if err != nil {
		return nil, c, err
	}
	key := fmt.Sprintf("%d|%s|%x", c.AppID, c.Slug, sha256.Sum256([]byte(c.PrivateKeyPEM)))
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.app != nil && s.appKey == key {
		return s.app, c, nil
	}
	pk, err := github.ParsePrivateKeyPEM([]byte(c.PrivateKeyPEM))
	if err != nil {
		return nil, c, err
	}
	a, err := github.NewApp(github.AppConfig{AppID: c.AppID, PrivateKey: pk, BotLogin: c.BotLogin(), BaseURL: s.GitHubAPIURL,
		HTTPClient: s.HTTP, UserAgent: "alpine-api"})
	if err != nil {
		return nil, c, err
	}
	s.app, s.appKey = a, key
	return a, c, nil
}

func (s *Service) webURL() string {
	return strings.TrimRight(orDefault(s.GitHubWebURL, github.DefaultWebURL), "/")
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

// WebhookURL is where GitHub delivers App webhooks: the smee relay in local dev, else the API.
func (s *Service) WebhookURL() (string, bool) {
	if s.WebhookProxyURL != "" {
		return s.WebhookProxyURL, true
	}
	return strings.TrimRight(s.PublicAPIURL, "/") + "/v1/webhooks/github", false
}

func (s *Service) manifestCallbackURL() string {
	return strings.TrimRight(s.PublicAPIURL, "/") + "/v1/setup/github-app/callback"
}

func (s *Service) setupURL() string {
	return strings.TrimRight(s.PublicAPIURL, "/") + "/v1/github/setup"
}

func (s *Service) appRedirect(path string, q url.Values) string {
	u := strings.TrimRight(s.AppURL, "/") + sharedauth.SafeRedirectPath(path)
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

// ---- operator setup -------------------------------------------------------------------------

// SetupStatus is the operator page model.
type SetupStatus struct {
	Credentials  *githubapp.Credentials
	CanConfigure bool
	WebhookURL   string
	ViaProxy     bool
}

// CanConfigure reports whether user may create/replace the App: an operator email, or (no
// operators configured and no App yet) anyone signed in. Env-sourced credentials are fixed.
func (s *Service) CanConfigure(ctx context.Context, user uuid.UUID) (bool, *githubapp.Credentials, error) {
	c, err := s.creds(ctx)
	var cur *githubapp.Credentials
	switch {
	case err == nil:
		cur = &c
	case !errors.Is(err, githubapp.ErrNotConfigured):
		return false, nil, err
	}
	if user == uuid.Nil || (cur != nil && cur.Source == "env") {
		return false, cur, nil
	}
	if len(s.Operators) == 0 {
		return cur == nil, cur, nil
	}
	email := ""
	if s.Emails != nil {
		email, _ = s.Emails(ctx, user)
	}
	return email != "" && slices.ContainsFunc(s.Operators, func(o string) bool { return strings.EqualFold(strings.TrimSpace(o), email) }), cur, nil
}

// Status returns the operator page model.
func (s *Service) Status(ctx context.Context, user uuid.UUID) (SetupStatus, error) {
	can, cur, err := s.CanConfigure(ctx, user)
	if err != nil {
		return SetupStatus{}, err
	}
	hook, proxy := s.WebhookURL()
	return SetupStatus{Credentials: cur, CanConfigure: can, WebhookURL: hook, ViaProxy: proxy}, nil
}

// ErrForbidden is returned when the caller may not perform an operator action.
var ErrForbidden = errors.New("onboarding: forbidden")

// StartManifest builds the manifest and state; the browser POSTs manifest to postURL.
func (s *Service) StartManifest(ctx context.Context, w http.ResponseWriter, user uuid.UUID, org, name string) (postURL, manifest string, err error) {
	can, _, err := s.CanConfigure(ctx, user)
	if err != nil {
		return "", "", err
	}
	if !can {
		return "", "", ErrForbidden
	}
	if org != "" && !validLogin(org) {
		return "", "", fmt.Errorf("%w: organization must be a GitHub login", ErrBadInput)
	}
	if name == "" {
		name = "Alpine Factory " + sharedauth.RandomToken(3)[:4]
	}
	if len(name) > 34 {
		return "", "", fmt.Errorf("%w: name is limited to 34 characters", ErrBadInput)
	}
	hook, _ := s.WebhookURL()
	m := github.NewManifest(github.ManifestOptions{Name: name, HomepageURL: s.AppURL, WebhookURL: hook,
		RedirectURL: s.manifestCallbackURL(), SetupURL: s.setupURL()})
	if err := m.Validate(); err != nil {
		return "", "", fmt.Errorf("%w: %v", ErrBadInput, err)
	}
	b, _ := json.Marshal(m)
	state, err := s.newState(ctx, w, flow{Kind: flowManifest, UserID: user})
	if err != nil {
		return "", "", err
	}
	return github.ManifestPostURL(s.webURL(), org, state), string(b), nil
}

// ErrBadInput wraps validation errors.
var ErrBadInput = errors.New("invalid input")

func validLogin(s string) bool {
	if s == "" || len(s) > 39 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

// FinishManifest handles GitHub's redirect: verify state, convert the code, store credentials.
// It returns the SPA URL to redirect to.
func (s *Service) FinishManifest(ctx context.Context, w http.ResponseWriter, r *http.Request, user, org uuid.UUID, code, state string) string {
	fail := func(reason string, err error) string {
		s.log().Warn("github app manifest callback failed", "reason", reason, "err", err)
		return s.appRedirect("/setup/github-app", url.Values{"error": {reason}})
	}
	if state == "" {
		return fail("state_missing", nil)
	}
	f, _, err := s.takeState(ctx, w, r, flowManifest, state, user)
	if err != nil {
		return fail(errCode(err), err)
	}
	if can, _, _ := s.CanConfigure(ctx, f.UserID); !can {
		return fail("forbidden", nil)
	}
	conv, err := github.ConvertManifest(ctx, s.HTTP, s.GitHubAPIURL, code)
	if err != nil {
		return fail("conversion_failed", err)
	}
	c := githubapp.Credentials{AppID: conv.ID, Slug: conv.Slug, Name: conv.Name, HTMLURL: conv.HTMLURL, OwnerLogin: conv.Owner.Login,
		ClientID: conv.ClientID, ClientSecret: conv.ClientSecret, WebhookSecret: conv.WebhookSecret, PrivateKeyPEM: conv.PEM,
		CreatedBy: f.UserID, CreatedAt: s.now().UTC()}
	if err := s.Apps.Save(ctx, c); err != nil {
		return fail("store_failed", err)
	}
	s.log().Info("github app registered", "app_id", c.AppID, "slug", c.Slug, "owner", c.OwnerLogin)
	if org != uuid.Nil && s.Store != nil {
		_ = s.Store.Audit(ctx, org, "user:"+f.UserID.String(), "github_app.registered", "github_app", fmt.Sprint(c.AppID),
			map[string]any{"slug": c.Slug, "owner": c.OwnerLogin})
	}
	return s.appRedirect("/setup/github-app", url.Values{"created": {"1"}})
}

func errCode(err error) string {
	switch {
	case errors.Is(err, ErrStateMismatch):
		return "state_mismatch"
	case errors.Is(err, ErrStateUnknown):
		return "state_expired"
	case errors.Is(err, ErrWrongUser):
		return "state_user_mismatch"
	default:
		return "failed"
	}
}

// AppRedirect is the SPA URL for path with step/error query values.
func (s *Service) AppRedirect(path, step, errCode string) string {
	q := url.Values{}
	if step != "" {
		q.Set("step", step)
	}
	if errCode != "" {
		q.Set("error", errCode)
	}
	return s.appRedirect(path, q)
}
