// Package github is Alpine's outbound GitHub App client: App JWT auth,
// repository-scoped installation tokens with caching, and the REST operations
// the orchestrator needs (branches, API-only pushes, pull requests, labels,
// check runs, comments). It never shells out to git.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	// DefaultBaseURL is the public GitHub REST API. GHES uses https://<host>/api/v3.
	DefaultBaseURL = "https://api.github.com"
	// APIVersion is sent as X-GitHub-Api-Version.
	APIVersion       = "2022-11-28"
	defaultUserAgent = "alpine-integrations"
	maxResponseBytes = 10 << 20
)

// DefaultBranchPrefixes are the only branches a Client will create or move
// unless configured otherwise (architecture §9.1: push only to alpine/*).
var DefaultBranchPrefixes = []string{"alpine/"}

// TokenSource yields a bearer token for each request.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// TokenFunc adapts a function to TokenSource.
type TokenFunc func(ctx context.Context) (string, error)

func (f TokenFunc) Token(ctx context.Context) (string, error) { return f(ctx) }

// StaticToken is a TokenSource for a fixed token (tests, local tools).
type StaticToken string

func (s StaticToken) Token(context.Context) (string, error) { return string(s), nil }

// ClientConfig configures a repository Client.
type ClientConfig struct {
	BaseURL    string // default DefaultBaseURL
	HTTPClient *http.Client
	UserAgent  string
	Tokens     TokenSource
	Owner      string
	Repo       string
	// WritableBranchPrefixes restricts CreateBranch/PushFiles/UpdateRef and
	// the head of CreatePullRequest. nil means DefaultBranchPrefixes; an empty
	// non-nil slice allows any branch.
	WritableBranchPrefixes []string

	// AppID and BotLogin identify Alpine's own GitHub App ("<app-slug>[bot]").
	// Both are required for keyed (idempotent) comments, which only match
	// comments authored by BotLogin and, when GitHub reports it, performed
	// via AppID. App.RepoClient fills them from AppConfig.
	AppID    int64
	BotLogin string

	Retry RetryPolicy
	// Now, Sleep and Rand are injectable for tests; defaults are time.Now,
	// a context-aware timer and math/rand/v2.
	Now   func() time.Time
	Sleep func(context.Context, time.Duration) error
	Rand  func() float64
}

// Client performs repository-scoped GitHub REST calls.
type Client struct {
	t        *transport
	tokens   TokenSource
	owner    string
	repo     string
	prefixes []string
	appID    int64
	botLogin string
}

// NewClient builds a Client for one repository.
func NewClient(cfg ClientConfig) (*Client, error) {
	if cfg.Tokens == nil {
		return nil, errors.New("github: ClientConfig.Tokens is required")
	}
	if cfg.Owner == "" || cfg.Repo == "" || strings.Contains(cfg.Owner, "/") || strings.Contains(cfg.Repo, "/") {
		return nil, fmt.Errorf("github: invalid repository %q/%q", cfg.Owner, cfg.Repo)
	}
	if err := validateIdentity(cfg.AppID, cfg.BotLogin); err != nil {
		return nil, err
	}
	base, err := normalizeBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	prefixes := cfg.WritableBranchPrefixes
	if prefixes == nil {
		prefixes = DefaultBranchPrefixes
	}
	return &Client{
		t:        newTransport(base, cfg.HTTPClient, cfg.UserAgent, cfg.Retry, cfg.Now, cfg.Sleep, cfg.Rand),
		tokens:   cfg.Tokens,
		owner:    cfg.Owner,
		repo:     cfg.Repo,
		prefixes: prefixes,
		appID:    cfg.AppID,
		botLogin: cfg.BotLogin,
	}, nil
}

// botLoginRe matches a GitHub App bot login: the app slug (letters, digits,
// hyphens; no leading/trailing hyphen) followed by "[bot]".
var botLoginRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,98}[A-Za-z0-9])?\[bot\]$`)

// validateIdentity checks the optional App identity; zero values are allowed
// here and rejected only by operations that need them (keyed comments).
func validateIdentity(appID int64, botLogin string) error {
	if appID < 0 {
		return fmt.Errorf("github: invalid AppID %d", appID)
	}
	if botLogin != "" && !botLoginRe.MatchString(botLogin) {
		return fmt.Errorf("github: invalid BotLogin %q (want \"<app-slug>[bot]\")", botLogin)
	}
	return nil
}

// Owner returns the repository owner.
func (c *Client) Owner() string { return c.owner }

// Repo returns the repository name.
func (c *Client) Repo() string { return c.repo }

// APIError is a non-2xx GitHub response.
type APIError struct {
	StatusCode       int
	Method           string
	Path             string
	Message          string          `json:"message"`
	DocumentationURL string          `json:"documentation_url"`
	Errors           json.RawMessage `json:"errors"`
	header           http.Header
}

// rateLimited: 429, or 403 with Retry-After, X-RateLimit-Remaining: 0 or a
// rate-limit message (GitHub's primary and secondary limits).
func (e *APIError) rateLimited() bool {
	switch e.StatusCode {
	case http.StatusTooManyRequests:
		return true
	case http.StatusForbidden:
		return e.header.Get("Retry-After") != "" || e.header.Get("X-RateLimit-Remaining") == "0" ||
			strings.Contains(strings.ToLower(e.Message), "rate limit")
	}
	return false
}

func (e *APIError) Error() string {
	msg := e.Message
	if len(e.Errors) > 0 && string(e.Errors) != "null" {
		msg += " " + string(e.Errors)
	}
	return fmt.Sprintf("github: %s %s: %d %s", e.Method, e.Path, e.StatusCode, msg)
}

// StatusCode returns the HTTP status of an *APIError in err's chain, or 0.
func StatusCode(err error) int {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.StatusCode
	}
	return 0
}

// repoPath builds /repos/{owner}/{repo}/<segments...>, escaping each segment.
func (c *Client) repoPath(segments ...string) string {
	parts := append([]string{"repos", c.owner, c.repo}, segments...)
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return "/" + strings.Join(parts, "/")
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	_, err := c.call(ctx, call{method: method, path: path, in: in, out: out, mode: retrySafe})
	return err
}

// doCreate is for non-idempotent POSTs: only rate limits are retried here;
// ambiguous failures go back to the caller's re-check logic.
func (c *Client) doCreate(ctx context.Context, path string, in, out any) error {
	_, err := c.call(ctx, call{method: http.MethodPost, path: path, in: in, out: out, mode: retryRateLimitOnly})
	return err
}

func (c *Client) call(ctx context.Context, cl call) (http.Header, error) {
	cl.auth = func(ctx context.Context) (string, error) {
		tok, err := c.tokens.Token(ctx)
		if err != nil {
			return "", fmt.Errorf("token: %w", err)
		}
		return tok, nil
	}
	return c.t.do(ctx, cl)
}

func normalizeBaseURL(s string) (string, error) {
	if s == "" {
		return DefaultBaseURL, nil
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", fmt.Errorf("github: invalid base URL %q", s)
	}
	return strings.TrimRight(s, "/"), nil
}

func orDefault(c *http.Client) *http.Client {
	if c != nil {
		return c
	}
	return defaultHTTPClient
}

var defaultHTTPClient = &http.Client{Timeout: 30 * time.Second}

func orString(s, def string) string {
	if s != "" {
		return s
	}
	return def
}
