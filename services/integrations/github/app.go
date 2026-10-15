package github

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	// jwtBackdate absorbs clock drift between us and GitHub (GitHub recommends 60s).
	jwtBackdate = 60 * time.Second
	// jwtLifetime must be <= 10 minutes; stay under to tolerate drift.
	jwtLifetime = 9 * time.Minute
	// tokenRefreshMargin: a cached installation token is replaced once it has
	// less than this left, so callers never get a token that expires mid-activity.
	tokenRefreshMargin = 5 * time.Minute
)

// AppConfig configures a GitHub App.
type AppConfig struct {
	AppID      int64
	PrivateKey *rsa.PrivateKey
	BaseURL    string // default DefaultBaseURL
	HTTPClient *http.Client
	UserAgent  string
	// BotLogin is the App's bot account, "<app-slug>[bot]". Optional, but
	// RepoClients need it (with AppID) for keyed comments.
	BotLogin string
	Retry    RetryPolicy
	// Now, Sleep and Rand are injectable for tests (JWT times, token expiry,
	// retry waits, jitter); they are passed on to RepoClient.
	Now   func() time.Time
	Sleep func(context.Context, time.Duration) error
	Rand  func() float64
}

// App authenticates as a GitHub App and mints repository-scoped installation tokens.
type App struct {
	id  int64
	key *rsa.PrivateKey
	cfg AppConfig
	t   *transport
	now func() time.Time

	mu    sync.Mutex
	cache map[string]*cacheEntry
}

type cacheEntry struct {
	mu  sync.Mutex // serializes refreshes per key (no thundering herd)
	tok *InstallationToken
}

// NewApp validates cfg and returns an App.
func NewApp(cfg AppConfig) (*App, error) {
	if cfg.AppID <= 0 {
		return nil, errors.New("github: AppConfig.AppID is required")
	}
	if cfg.PrivateKey == nil {
		return nil, errors.New("github: AppConfig.PrivateKey is required")
	}
	if err := validateIdentity(cfg.AppID, cfg.BotLogin); err != nil {
		return nil, err
	}
	base, err := normalizeBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	cfg.BaseURL = base
	t := newTransport(base, cfg.HTTPClient, cfg.UserAgent, cfg.Retry, cfg.Now, cfg.Sleep, cfg.Rand)
	return &App{id: cfg.AppID, key: cfg.PrivateKey, cfg: cfg, t: t, now: t.now, cache: map[string]*cacheEntry{}}, nil
}

// ParsePrivateKeyPEM parses a GitHub App private key (PKCS#1 "RSA PRIVATE
// KEY", as downloaded from GitHub, or PKCS#8 "PRIVATE KEY").
func ParsePrivateKeyPEM(b []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, errors.New("github: private key: no PEM block")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("github: private key: %w", err)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("github: private key is not RSA")
	}
	return rk, nil
}

// JWT returns a fresh RS256 App JWT: iss = app id, iat backdated 60s,
// exp = now + 9m (GitHub's max is 10m).
func (a *App) JWT() (string, error) {
	now := a.now()
	claims := jwt.RegisteredClaims{
		Issuer:    strconv.FormatInt(a.id, 10),
		IssuedAt:  jwt.NewNumericDate(now.Add(-jwtBackdate)),
		ExpiresAt: jwt.NewNumericDate(now.Add(jwtLifetime)),
	}
	s, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(a.key)
	if err != nil {
		return "", fmt.Errorf("github: sign app jwt: %w", err)
	}
	return s, nil
}

// doAsApp authenticates with a fresh App JWT per attempt. Both App calls
// (repo installation lookup, token minting) are safe to retry.
func (a *App) doAsApp(ctx context.Context, method, path string, in, out any) error {
	_, err := a.t.do(ctx, call{method: method, path: path, in: in, out: out, mode: retrySafe,
		auth: func(context.Context) (string, error) { return a.JWT() }})
	return err
}

// Installation is the subset of GET /repos/{owner}/{repo}/installation we use.
type Installation struct {
	ID      int64 `json:"id"`
	Account struct {
		Login string `json:"login"`
	} `json:"account"`
	Permissions map[string]string `json:"permissions"`
}

// RepoInstallation looks up the App installation for a repository.
func (a *App) RepoInstallation(ctx context.Context, owner, repo string) (*Installation, error) {
	c := &Client{owner: owner, repo: repo}
	var inst Installation
	if err := a.doAsApp(ctx, http.MethodGet, c.repoPath("installation"), nil, &inst); err != nil {
		return nil, err
	}
	return &inst, nil
}

// TokenRequest asks for an installation token scoped to exactly one
// repository (by name or id) and an explicit permission subset.
type TokenRequest struct {
	InstallationID int64
	Repository     string // repository name without owner; or set RepositoryID
	RepositoryID   int64
	Permissions    Permissions
}

func (r TokenRequest) validate() error {
	if r.InstallationID <= 0 {
		return errors.New("github: TokenRequest.InstallationID is required")
	}
	if (r.Repository == "") == (r.RepositoryID == 0) {
		return errors.New("github: TokenRequest must name exactly one of Repository or RepositoryID")
	}
	if strings.Contains(r.Repository, "/") {
		return fmt.Errorf("github: TokenRequest.Repository must be a bare name, got %q", r.Repository)
	}
	return r.Permissions.validate()
}

func (r TokenRequest) cacheKey() string {
	return fmt.Sprintf("%d|%s|%d|%s", r.InstallationID, r.Repository, r.RepositoryID, r.Permissions.key())
}

// InstallationToken is a short-lived (≤1h) installation access token.
// String/GoString redact the secret so it is never logged by accident.
type InstallationToken struct {
	Token               string            `json:"token"`
	ExpiresAt           time.Time         `json:"expires_at"`
	Permissions         map[string]string `json:"permissions"`
	RepositorySelection string            `json:"repository_selection"`
}

func (t InstallationToken) String() string {
	return fmt.Sprintf("InstallationToken{expires_at=%s, token=REDACTED}", t.ExpiresAt.Format(time.RFC3339))
}

func (t InstallationToken) GoString() string { return t.String() }

// InstallationToken returns a cached token for req, minting a new one via
// POST /app/installations/{id}/access_tokens when none is cached or the cached
// one expires within tokenRefreshMargin.
func (a *App) InstallationToken(ctx context.Context, req TokenRequest) (InstallationToken, error) {
	if err := req.validate(); err != nil {
		return InstallationToken{}, err
	}
	key := req.cacheKey()
	a.mu.Lock()
	e := a.cache[key]
	if e == nil {
		e = &cacheEntry{}
		a.cache[key] = e
	}
	a.mu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.tok != nil && a.now().Add(tokenRefreshMargin).Before(e.tok.ExpiresAt) {
		return *e.tok, nil
	}
	body := map[string]any{"permissions": req.Permissions}
	if req.Repository != "" {
		body["repositories"] = []string{req.Repository}
	} else {
		body["repository_ids"] = []int64{req.RepositoryID}
	}
	var tok InstallationToken
	path := "/app/installations/" + strconv.FormatInt(req.InstallationID, 10) + "/access_tokens"
	if err := a.doAsApp(ctx, http.MethodPost, path, body, &tok); err != nil {
		return InstallationToken{}, err
	}
	if tok.Token == "" || tok.ExpiresAt.IsZero() {
		return InstallationToken{}, errors.New("github: access_tokens response missing token or expires_at")
	}
	e.tok = &tok
	return tok, nil
}

// InvalidateToken drops the cached token for req (e.g. after a 401).
func (a *App) InvalidateToken(req TokenRequest) {
	a.mu.Lock()
	delete(a.cache, req.cacheKey())
	a.mu.Unlock()
}

// TokenSource returns a TokenSource backed by the App's token cache.
func (a *App) TokenSource(req TokenRequest) TokenSource {
	return TokenFunc(func(ctx context.Context) (string, error) {
		t, err := a.InstallationToken(ctx, req)
		return t.Token, err
	})
}

// RepoClient returns a Client for owner/repo whose requests use an
// installation token scoped to that single repository and perms.
// Additional Client options (branch prefixes) can be set via opts.
func (a *App) RepoClient(installationID int64, owner, repo string, perms Permissions, opts ...func(*ClientConfig)) (*Client, error) {
	req := TokenRequest{InstallationID: installationID, Repository: repo, Permissions: perms}
	if err := req.validate(); err != nil {
		return nil, err
	}
	cfg := ClientConfig{
		BaseURL: a.cfg.BaseURL, HTTPClient: a.cfg.HTTPClient, UserAgent: a.cfg.UserAgent, Owner: owner, Repo: repo,
		Tokens: a.TokenSource(req), AppID: a.id, BotLogin: a.cfg.BotLogin, Retry: a.cfg.Retry, Now: a.cfg.Now, Sleep: a.cfg.Sleep, Rand: a.cfg.Rand,
	}
	for _, o := range opts {
		o(&cfg)
	}
	return NewClient(cfg)
}
