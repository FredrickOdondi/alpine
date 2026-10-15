package auth

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config configures the auth service. Secrets come ONLY from the environment (see .env.example);
// nothing here has a real default value.
type Config struct {
	// PublicAPIURL is the externally visible API origin, e.g. https://api.alpine.dev
	// (dev: http://localhost:8080). OAuth redirect URIs are derived from it.
	PublicAPIURL string
	// AppURL is the SPA origin users land on after login, e.g. https://app.alpine.dev
	// (dev: http://localhost:5173).
	AppURL string
	// AllowedOrigins may make credentialed, state-changing requests (CORS + CSRF Origin check).
	// Defaults to [AppURL].
	AllowedOrigins []string

	Google GoogleConfig
	GitHub GitHubConfig

	SessionSecret   []byte // >= 32 bytes; keys Redis session ids, rate-limit keys, state
	APIKeyPepper    []byte // >= 32 bytes; HMAC pepper for API key secrets
	Cookie          CookieConfig
	SessionIdleTTL  time.Duration
	SessionAbsTTL   time.Duration
	InvitationTTL   time.Duration // default 7 days
	MagicLinkTTL    time.Duration // default 15 minutes
	AllowSignup     bool          // OAuth logins may create new users (default true)
	MagicLinkEnable bool          // email magic link login
	MagicLinkSignup bool          // magic link may create new users (default false: existing users only)
	DevLogin        bool          // POST /v1/auth/dev-login (never in production)
	DevLoginEmail   string        // default dev@alpine.local
	TrustProxy      bool          // use the right-most X-Forwarded-For entry (behind ALB/CloudFront)
	MailFrom        string
}

// CookieConfig controls the session cookie.
type CookieConfig struct {
	Name     string        // default "alpine_session"; use "__Host-alpine_session" in production
	Domain   string        // empty = host-only (required for __Host-)
	Secure   bool          // true outside local http dev
	SameSite http.SameSite // Lax by default (SPA and API must be same-site, e.g. app./api.alpine.dev)
}

// GoogleConfig configures Sign in with Google (OIDC authorization code + PKCE).
type GoogleConfig struct {
	ClientID      string
	ClientSecret  string
	RedirectURL   string   // default PublicAPIURL + /v1/auth/google/callback
	HostedDomains []string // optional Google Workspace domain allowlist (checked against the `hd` claim)
	Issuer        string   // default https://accounts.google.com (tests point it at httptest)
	HTTPClient    *http.Client
}

// Enabled reports whether Google login is configured.
func (g GoogleConfig) Enabled() bool { return g.ClientID != "" && g.ClientSecret != "" }

// GitHubConfig configures GitHub OAuth login (separate from the GitHub App used for repo access).
type GitHubConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string // default PublicAPIURL + /v1/auth/github/callback
	BaseURL      string // default https://github.com
	APIURL       string // default https://api.github.com
	HTTPClient   *http.Client
}

// Enabled reports whether GitHub login is configured.
func (g GitHubConfig) Enabled() bool { return g.ClientID != "" && g.ClientSecret != "" }

// ConfigFromEnv reads Config from the environment.
func ConfigFromEnv() (Config, error) {
	c := Config{
		PublicAPIURL: env("PUBLIC_API_URL", "http://localhost:8080"),
		AppURL:       env("APP_URL", "http://localhost:5173"),
		Google: GoogleConfig{
			ClientID:      os.Getenv("GOOGLE_CLIENT_ID"),
			ClientSecret:  os.Getenv("GOOGLE_CLIENT_SECRET"),
			RedirectURL:   os.Getenv("GOOGLE_REDIRECT_URL"),
			HostedDomains: list(os.Getenv("GOOGLE_HOSTED_DOMAINS")),
			Issuer:        os.Getenv("GOOGLE_ISSUER"),
		},
		GitHub: GitHubConfig{
			ClientID:     os.Getenv("GITHUB_OAUTH_CLIENT_ID"),
			ClientSecret: os.Getenv("GITHUB_OAUTH_CLIENT_SECRET"),
			RedirectURL:  os.Getenv("GITHUB_OAUTH_REDIRECT_URL"),
		},
		SessionSecret: []byte(os.Getenv("SESSION_SECRET")),
		APIKeyPepper:  []byte(os.Getenv("API_KEY_PEPPER")),
		Cookie: CookieConfig{
			Name:   env("SESSION_COOKIE_NAME", "alpine_session"),
			Domain: os.Getenv("SESSION_COOKIE_DOMAIN"),
			Secure: boolEnv("SESSION_COOKIE_SECURE", true),
		},
		SessionIdleTTL:  durEnv("SESSION_IDLE_TTL", 7*24*time.Hour),
		SessionAbsTTL:   durEnv("SESSION_ABSOLUTE_TTL", 30*24*time.Hour),
		InvitationTTL:   durEnv("INVITATION_TTL", 7*24*time.Hour),
		MagicLinkTTL:    durEnv("MAGIC_LINK_TTL", 15*time.Minute),
		AllowSignup:     boolEnv("AUTH_ALLOW_SIGNUP", true),
		MagicLinkEnable: boolEnv("AUTH_MAGIC_LINK_ENABLED", false),
		MagicLinkSignup: boolEnv("AUTH_MAGIC_LINK_SIGNUP", false),
		DevLogin:        boolEnv("AUTH_DEV_LOGIN", false),
		DevLoginEmail:   env("AUTH_DEV_LOGIN_EMAIL", "dev@alpine.local"),
		TrustProxy:      boolEnv("TRUST_PROXY_HEADERS", false),
		MailFrom:        env("MAIL_FROM", "Alpine <no-reply@alpine.local>"),
		AllowedOrigins:  list(os.Getenv("CORS_ALLOWED_ORIGINS")),
	}
	switch strings.ToLower(env("SESSION_COOKIE_SAMESITE", "lax")) {
	case "strict":
		c.Cookie.SameSite = http.SameSiteStrictMode
	case "none":
		c.Cookie.SameSite = http.SameSiteNoneMode
	default:
		c.Cookie.SameSite = http.SameSiteLaxMode
	}
	c.applyDefaults()
	return c, c.Validate()
}

func (c *Config) applyDefaults() {
	c.PublicAPIURL = strings.TrimRight(c.PublicAPIURL, "/")
	c.AppURL = strings.TrimRight(c.AppURL, "/")
	if c.Google.RedirectURL == "" {
		c.Google.RedirectURL = c.PublicAPIURL + "/v1/auth/google/callback"
	}
	if c.GitHub.RedirectURL == "" {
		c.GitHub.RedirectURL = c.PublicAPIURL + "/v1/auth/github/callback"
	}
	if len(c.AllowedOrigins) == 0 && c.AppURL != "" {
		c.AllowedOrigins = []string{c.AppURL}
	}
	if c.Cookie.Name == "" {
		c.Cookie.Name = "alpine_session"
	}
	if c.Cookie.SameSite == 0 {
		c.Cookie.SameSite = http.SameSiteLaxMode
	}
	if c.InvitationTTL <= 0 {
		c.InvitationTTL = 7 * 24 * time.Hour
	}
	if c.MagicLinkTTL <= 0 {
		c.MagicLinkTTL = 15 * time.Minute
	}
	if c.DevLoginEmail == "" {
		c.DevLoginEmail = "dev@alpine.local"
	}
}

// Validate checks secrets and URLs.
func (c Config) Validate() error {
	var errs []error
	if len(c.SessionSecret) < 32 {
		errs = append(errs, errors.New("SESSION_SECRET must be at least 32 bytes"))
	}
	if len(c.APIKeyPepper) < 32 {
		errs = append(errs, errors.New("API_KEY_PEPPER must be at least 32 bytes"))
	}
	for name, u := range map[string]string{"PUBLIC_API_URL": c.PublicAPIURL, "APP_URL": c.AppURL} {
		if pu, err := url.Parse(u); err != nil || pu.Scheme == "" || pu.Host == "" {
			errs = append(errs, fmt.Errorf("%s must be an absolute URL", name))
		}
	}
	if strings.HasPrefix(c.Cookie.Name, "__Host-") && (!c.Cookie.Secure || c.Cookie.Domain != "") {
		errs = append(errs, errors.New("__Host- cookies require SESSION_COOKIE_SECURE=true and no SESSION_COOKIE_DOMAIN"))
	}
	if c.Cookie.SameSite == http.SameSiteNoneMode && !c.Cookie.Secure {
		errs = append(errs, errors.New("SameSite=None requires Secure cookies"))
	}
	if c.DevLogin && c.Cookie.Secure && strings.HasPrefix(c.PublicAPIURL, "https://") && !strings.Contains(c.PublicAPIURL, "localhost") {
		errs = append(errs, errors.New("AUTH_DEV_LOGIN must not be enabled on a public https deployment"))
	}
	return errors.Join(errs...)
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func list(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func boolEnv(k string, def bool) bool {
	if b, err := strconv.ParseBool(os.Getenv(k)); err == nil {
		return b
	}
	return def
}

func durEnv(k string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(os.Getenv(k)); err == nil && d > 0 {
		return d
	}
	return def
}
