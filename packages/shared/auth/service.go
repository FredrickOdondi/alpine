package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"golang.org/x/oauth2"

	"github.com/alpine-dev/alpine/packages/shared/repo"
)

// Service is the authentication & authorization service. It implements Authenticator.
type Service struct {
	Cfg       Config
	Store     repo.Store
	Redis     redis.UniversalClient
	Sessions  *SessionStore
	Providers map[string]LoginProvider
	Mailer    Mailer
	SSO       SSOProvider
	Limiter   *RateLimiter
	Lockout   *Lockout
	Logger    *slog.Logger
	Now       func() time.Time
}

var (
	_ Authenticator          = (*Service)(nil)
	_ PrincipalAuthenticator = (*Service)(nil)
)

// New wires a Service from config. Google/GitHub providers are registered when configured.
func New(cfg Config, store repo.Store, rdb redis.UniversalClient, mailer Mailer, logger *slog.Logger) *Service {
	cfg.applyDefaults()
	if logger == nil {
		logger = slog.Default()
	}
	if mailer == nil {
		mailer = LogMailer{Logger: logger}
	}
	prefix := "alpine:auth:"
	s := &Service{
		Cfg: cfg, Store: store, Redis: rdb, Mailer: mailer, SSO: NoSSO{}, Logger: logger,
		Sessions:  &SessionStore{Redis: rdb, Secret: cfg.SessionSecret, IdleTTL: cfg.SessionIdleTTL, AbsoluteTTL: cfg.SessionAbsTTL, Prefix: prefix},
		Limiter:   &RateLimiter{Redis: rdb, Secret: cfg.SessionSecret, Prefix: prefix},
		Lockout:   &Lockout{Redis: rdb, Secret: cfg.SessionSecret, Prefix: prefix},
		Providers: map[string]LoginProvider{},
	}
	if cfg.Google.Enabled() {
		s.Providers["google"] = NewGoogle(cfg.Google)
	}
	if cfg.GitHub.Enabled() {
		s.Providers["github"] = NewGitHub(cfg.GitHub)
	}
	return s
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// EnabledProviders reports which login methods are on.
func (s *Service) EnabledProviders() (google, github, magicLink, devLogin bool) {
	_, google = s.Providers["google"]
	_, github = s.Providers["github"]
	return google, github, s.Cfg.MagicLinkEnable, s.Cfg.DevLogin
}

// ---------------------------------------------------------------------------------------------
// Request helpers

// ClientIP returns the caller IP. Behind a trusted proxy (ALB/CloudFront) it is the right-most
// X-Forwarded-For entry, i.e. the address the proxy itself saw; client-supplied entries to the
// left are ignored.
func (s *Service) ClientIP(r *http.Request) netip.Addr {
	if s.Cfg.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if a, err := netip.ParseAddr(strings.TrimSpace(parts[len(parts)-1])); err == nil {
				return a.Unmap()
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.IPv4Unspecified()
	}
	return a.Unmap()
}

func (s *Service) ipPtr(r *http.Request) *netip.Addr {
	a := s.ClientIP(r)
	return &a
}

// SafeRedirectPath accepts only same-app relative paths ("/x", not "//evil" or "/\evil").
func SafeRedirectPath(p string) string {
	if p == "" || !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.HasPrefix(p, "/\\") ||
		strings.ContainsAny(p, "\r\n") || len(p) > 512 {
		return "/"
	}
	if u, err := url.Parse(p); err != nil || u.Host != "" || u.Scheme != "" {
		return "/"
	}
	return p
}

func (s *Service) appURL(path string) string { return s.Cfg.AppURL + SafeRedirectPath(path) }

// LoginErrorURL is where browser flows land after a failed login.
func (s *Service) LoginErrorURL(err error) string {
	return s.Cfg.AppURL + "/login?error=" + url.QueryEscape(ErrorCode(err))
}

// ErrorCode maps errors to stable API error codes (never includes PII).
func ErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrRateLimited):
		return "rate_limited"
	case errors.Is(err, ErrLocked):
		return "locked"
	case errors.Is(err, ErrNotConfigured):
		return "not_configured"
	case errors.Is(err, ErrInvalidState):
		return "invalid_state"
	case errors.Is(err, ErrDomainNotAllowed):
		return "domain_not_allowed"
	case errors.Is(err, ErrEmailUnverified):
		return "email_unverified"
	case errors.Is(err, ErrUnauthenticated):
		return "unauthenticated"
	case errors.Is(err, ErrForbidden):
		return "forbidden"
	case errors.Is(err, ErrNoActiveOrg):
		return "no_active_org"
	case errors.Is(err, ErrCSRF):
		return "csrf_failed"
	case errors.Is(err, repo.ErrIdentityTaken):
		return "identity_taken"
	case errors.Is(err, repo.ErrUserDisabled):
		return "user_disabled"
	case errors.Is(err, repo.ErrNotFound):
		return "not_found"
	case errors.Is(err, repo.ErrConflict):
		return "conflict"
	case errors.Is(err, repo.ErrLastOwner):
		return "last_owner"
	case errors.Is(err, repo.ErrInvitationExpired):
		return "invitation_expired"
	case errors.Is(err, repo.ErrInvitationMismatch):
		return "invitation_email_mismatch"
	case errors.Is(err, repo.ErrInvitationInvalid):
		return "invitation_invalid"
	case errors.Is(err, ErrSignupDisabled):
		return "signup_disabled"
	case errors.Is(err, ErrInvalidInput):
		return "invalid_input"
	}
	return "login_failed"
}

// ErrSignupDisabled is returned when a new identity may not create an account.
var ErrSignupDisabled = errors.New("auth: sign-up is disabled for this login method")

// ErrInvalidInput wraps validation failures.
var ErrInvalidInput = errors.New("auth: invalid input")

// ---------------------------------------------------------------------------------------------
// Cookies

func (s *Service) setSessionCookie(w http.ResponseWriter, raw string) {
	http.SetCookie(w, &http.Cookie{
		Name: s.Cfg.Cookie.Name, Value: raw, Path: "/", Domain: s.Cfg.Cookie.Domain,
		HttpOnly: true, Secure: s.Cfg.Cookie.Secure, SameSite: s.Cfg.Cookie.SameSite,
		MaxAge: int(s.Sessions.absolute().Seconds()),
	})
}

func (s *Service) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: s.Cfg.Cookie.Name, Value: "", Path: "/", Domain: s.Cfg.Cookie.Domain,
		HttpOnly: true, Secure: s.Cfg.Cookie.Secure, SameSite: s.Cfg.Cookie.SameSite, MaxAge: -1,
	})
}

// StateCookieName binds an OAuth state to the browser that started the flow (login CSRF).
const StateCookieName = "alpine_oauth_state"

func (s *Service) setStateCookie(w http.ResponseWriter, state string, maxAge int) {
	// SameSite=Lax: sent on the top-level GET redirect back from Google/GitHub.
	http.SetCookie(w, &http.Cookie{
		Name: StateCookieName, Value: state, Path: "/v1/auth/", HttpOnly: true, Secure: s.Cfg.Cookie.Secure,
		SameSite: http.SameSiteLaxMode, MaxAge: maxAge,
	})
}

func (s *Service) rawSession(r *http.Request) string {
	if c, err := r.Cookie(s.Cfg.Cookie.Name); err == nil {
		return c.Value
	}
	return ""
}

// ---------------------------------------------------------------------------------------------
// Authentication

// Authenticate implements Authenticator.
func (s *Service) Authenticate(r *http.Request) (uuid.UUID, uuid.UUID, Role, error) {
	p, err := s.AuthenticatePrincipal(r)
	if err != nil {
		return uuid.Nil, uuid.Nil, "", err
	}
	return p.UserID, p.OrgID, p.Role, nil
}

// AuthenticatePrincipal implements PrincipalAuthenticator: `Authorization: Bearer alp_...` API keys
// or the session cookie.
func (s *Service) AuthenticatePrincipal(r *http.Request) (*Principal, error) {
	ctx := r.Context()
	if authz := r.Header.Get("Authorization"); authz != "" {
		tok, ok := strings.CutPrefix(authz, "Bearer ")
		if !ok {
			return nil, ErrUnauthenticated
		}
		return s.authenticateAPIKey(r, strings.TrimSpace(tok))
	}
	raw := s.rawSession(r)
	if raw == "" {
		return nil, ErrUnauthenticated
	}
	sess, err := s.Sessions.Get(ctx, raw)
	if err != nil {
		if errors.Is(err, ErrNoSession) {
			return nil, ErrUnauthenticated
		}
		return nil, err
	}
	p := &Principal{Kind: KindSession, UserID: sess.UserID, SessionKey: sess.Key, CSRFToken: sess.CSRFToken, AuthMethod: sess.AuthMethod}
	if sess.ActiveOrgID != uuid.Nil {
		m, err := s.Store.GetMembership(ctx, sess.ActiveOrgID, sess.UserID)
		switch {
		case err == nil:
			p.OrgID, p.Role = m.OrgID, Role(m.Role)
		case errors.Is(err, repo.ErrNotFound):
			// Removed from the org (or org deleted): drop the stale active org.
			_ = s.Sessions.SetActiveOrg(ctx, sess, uuid.Nil)
		default:
			return nil, err
		}
	}
	return p, nil
}

func (s *Service) authenticateAPIKey(r *http.Request, token string) (*Principal, error) {
	ctx := r.Context()
	ip := s.ClientIP(r).String()
	if d, err := s.Lockout.Check(ctx, "api_key", ip); err != nil {
		return nil, err
	} else if d > 0 {
		return nil, ErrLocked
	}
	fail := func(reason string) (*Principal, error) {
		if _, err := s.Lockout.Fail(ctx, "api_key", ip); err != nil {
			s.Logger.Warn("auth: lockout write failed", "err", err)
		}
		s.authEvent(ctx, r, nil, nil, "api_key.auth_failed", "api_key", false, reason, nil)
		return nil, ErrUnauthenticated
	}
	prefix, secret, err := ParseAPIKey(token)
	if err != nil {
		return fail("malformed")
	}
	k, err := s.Store.LookupAPIKey(ctx, prefix)
	if errors.Is(err, repo.ErrNotFound) {
		return fail("unknown_or_revoked")
	}
	if err != nil {
		return nil, err
	}
	if !VerifyAPIKeySecret(s.Cfg.APIKeyPepper, secret, k.SecretHash) {
		return fail("bad_secret")
	}
	if !k.Active(s.now()) {
		return fail("expired")
	}
	if err := s.Store.TouchAPIKey(ctx, k.OrgID, k.ID); err != nil {
		s.Logger.Warn("auth: touch api key failed", "err", err)
	}
	return &Principal{Kind: KindAPIKey, OrgID: k.OrgID, Role: Role(k.Role), APIKeyID: k.ID, Scopes: k.Scopes, AuthMethod: "api_key"}, nil
}

// OrgAccess returns a principal scoped to orgID (the caller's role there), or ErrForbidden if
// the caller is not a member / the API key belongs to another org.
func (s *Service) OrgAccess(ctx context.Context, p *Principal, orgID uuid.UUID) (*Principal, error) {
	if p == nil {
		return nil, ErrUnauthenticated
	}
	cp := *p
	if p.Kind == KindAPIKey {
		if p.OrgID != orgID {
			return nil, ErrForbidden
		}
		return &cp, nil
	}
	if p.OrgID == orgID && p.Role != "" {
		return &cp, nil
	}
	m, err := s.Store.GetMembership(ctx, orgID, p.UserID)
	if errors.Is(err, repo.ErrNotFound) {
		return nil, ErrForbidden
	}
	if err != nil {
		return nil, err
	}
	cp.OrgID, cp.Role = orgID, Role(m.Role)
	return &cp, nil
}

// Authorize is OrgAccess plus a permission check.
func (s *Service) Authorize(ctx context.Context, p *Principal, orgID uuid.UUID, perm Permission) (*Principal, error) {
	sp, err := s.OrgAccess(ctx, p, orgID)
	if err != nil {
		return nil, err
	}
	if !sp.Can(perm) {
		return nil, ErrForbidden
	}
	return sp, nil
}

// Actor converts a principal into a repo.Actor for audit entries.
func (s *Service) Actor(r *http.Request, p *Principal) repo.Actor {
	return repo.Actor{Type: p.ActorType(), ID: p.ActorID(), IP: s.ipPtr(r), RequestID: r.Header.Get("X-Request-Id")}
}

// ---------------------------------------------------------------------------------------------
// OAuth / OIDC login

type flowState struct {
	Provider   string    `json:"p"`
	Verifier   string    `json:"v"`
	Nonce      string    `json:"n"`
	RedirectTo string    `json:"r"`
	LinkUserID uuid.UUID `json:"l"`
}

const flowTTL = 10 * time.Minute

func (s *Service) flowKey(kind, token string) string {
	return s.Sessions.prefix() + kind + ":" + keyed(s.Cfg.SessionSecret, kind, token)
}

// StartLogin begins an OAuth/OIDC login (or, with link=true and a session, an account-linking
// flow) and returns the provider authorize URL. It sets the state-binding cookie.
func (s *Service) StartLogin(w http.ResponseWriter, r *http.Request, provider, redirectTo string, link bool) (string, error) {
	ctx := r.Context()
	if err := s.limitIP(r, "login_start", 30, time.Minute); err != nil {
		return "", err
	}
	p, ok := s.Providers[provider]
	if !ok {
		return "", ErrNotConfigured
	}
	fs := flowState{Provider: provider, Verifier: oauth2.GenerateVerifier(), Nonce: RandomToken(32), RedirectTo: SafeRedirectPath(redirectTo)}
	if link {
		pr, err := s.AuthenticatePrincipal(r)
		if err != nil || pr.Kind != KindSession {
			return "", ErrUnauthenticated
		}
		fs.LinkUserID = pr.UserID
	}
	state := RandomToken(32)
	b, _ := json.Marshal(fs)
	if err := s.Redis.Set(ctx, s.flowKey("flow", state), b, flowTTL).Err(); err != nil {
		return "", fmt.Errorf("auth: store login state: %w", err)
	}
	u, err := p.AuthCodeURL(ctx, state, fs.Nonce, fs.Verifier)
	if err != nil {
		return "", err
	}
	s.setStateCookie(w, state, int(flowTTL.Seconds()))
	return u, nil
}

// FinishLogin handles the provider callback and returns the SPA URL to redirect to.
func (s *Service) FinishLogin(w http.ResponseWriter, r *http.Request, provider string) (string, error) {
	ctx := r.Context()
	ip := s.ClientIP(r).String()
	if d, err := s.Lockout.Check(ctx, "login", ip); err != nil {
		return "", err
	} else if d > 0 {
		return "", ErrLocked
	}
	q := r.URL.Query()
	state := q.Get("state")
	c, cerr := r.Cookie(StateCookieName)
	s.setStateCookie(w, "", -1)
	if state == "" || cerr != nil || !ConstantTimeEqual(c.Value, state) {
		return "", s.loginFailed(r, provider, ErrInvalidState, "state_mismatch")
	}
	b, err := s.Redis.GetDel(ctx, s.flowKey("flow", state)).Bytes()
	if err != nil {
		return "", s.loginFailed(r, provider, ErrInvalidState, "state_unknown")
	}
	var fs flowState
	if json.Unmarshal(b, &fs) != nil || fs.Provider != provider {
		return "", s.loginFailed(r, provider, ErrInvalidState, "provider_mismatch")
	}
	if e := q.Get("error"); e != "" { // user cancelled at the provider
		return "", s.loginFailed(r, provider, ErrInvalidState, "provider_error")
	}
	p, ok := s.Providers[provider]
	if !ok {
		return "", ErrNotConfigured
	}
	ext, err := p.Exchange(ctx, q.Get("code"), fs.Verifier, fs.Nonce)
	if err != nil {
		reason := "exchange_failed"
		if errors.Is(err, ErrDomainNotAllowed) {
			reason = "domain_not_allowed"
		} else if errors.Is(err, ErrEmailUnverified) {
			reason = "email_unverified"
		} else {
			s.Logger.Warn("auth: provider exchange failed", "provider", provider, "err", err)
		}
		return "", s.loginFailed(r, provider, err, reason)
	}
	if fs.LinkUserID != uuid.Nil {
		if _, err := s.Store.LinkIdentity(ctx, fs.LinkUserID, ext); err != nil {
			return "", s.loginFailed(r, provider, err, "link_failed")
		}
		uid := fs.LinkUserID
		s.authEvent(ctx, r, &uid, nil, "identity.linked", provider, true, "", nil)
		return s.appURL(fs.RedirectTo), nil
	}
	if err := s.completeLogin(w, r, ext, provider, s.Cfg.AllowSignup); err != nil {
		return "", s.loginFailed(r, provider, err, ErrorCode(err))
	}
	return s.appURL(fs.RedirectTo), nil
}

func (s *Service) loginFailed(r *http.Request, provider string, err error, reason string) error {
	ctx := r.Context()
	if _, lerr := s.Lockout.Fail(ctx, "login", s.ClientIP(r).String()); lerr != nil {
		s.Logger.Warn("auth: lockout write failed", "err", lerr)
	}
	s.authEvent(ctx, r, nil, nil, "login.failed", provider, false, reason, nil)
	return err
}

// completeLogin resolves the user, rotates the session and records the login.
func (s *Service) completeLogin(w http.ResponseWriter, r *http.Request, ext repo.ExternalIdentity, method string, allowCreate bool) error {
	ctx := r.Context()
	user, outcome, err := s.Store.ResolveLogin(ctx, ext, allowCreate)
	if errors.Is(err, repo.ErrConflict) { // concurrent first login: retry once
		user, outcome, err = s.Store.ResolveLogin(ctx, ext, allowCreate)
	}
	if errors.Is(err, repo.ErrNotFound) && !allowCreate {
		return ErrSignupDisabled
	}
	if err != nil {
		return err
	}
	orgs, err := s.Store.ListOrgsForUser(ctx, user.ID)
	if err != nil {
		return err
	}
	active := uuid.Nil
	if len(orgs) > 0 {
		active = orgs[0].Org.ID
	}
	// Rotation: never reuse a pre-login session id (session fixation).
	if old := s.rawSession(r); old != "" {
		_ = s.Sessions.Delete(ctx, old)
	}
	_, raw, err := s.Sessions.Create(ctx, user.ID, active, method)
	if err != nil {
		return err
	}
	s.setSessionCookie(w, raw)
	_ = s.Lockout.Reset(ctx, "login", s.ClientIP(r).String())
	var orgPtr *uuid.UUID
	if active != uuid.Nil {
		orgPtr = &active
	}
	s.authEvent(ctx, r, &user.ID, orgPtr, "login.succeeded", method, true, "",
		map[string]any{"new_user": outcome.Created, "linked": outcome.Linked})
	if active != uuid.Nil {
		s.orgAudit(r, active, repo.Actor{Type: "user", ID: user.ID.String()}, "auth.login", "user", user.ID.String(),
			map[string]any{"method": method})
	}
	return nil
}

// ---------------------------------------------------------------------------------------------
// Magic link

var emailRE = regexp.MustCompile(`^[^@\s]{1,64}@[^@\s]+\.[^@\s]{2,}$`)

// NormalizeEmail lower-cases and validates an address.
func NormalizeEmail(e string) (string, error) {
	e = strings.ToLower(strings.TrimSpace(e))
	if len(e) > 254 || !emailRE.MatchString(e) {
		return "", fmt.Errorf("%w: email", ErrInvalidInput)
	}
	return e, nil
}

type magicState struct {
	Email      string `json:"e"`
	RedirectTo string `json:"r"`
}

// RequestMagicLink emails a one-time login link. To avoid account enumeration it succeeds
// silently for unknown addresses when sign-up is disabled; only the per-IP limit surfaces.
func (s *Service) RequestMagicLink(r *http.Request, email, redirectTo string) error {
	ctx := r.Context()
	if !s.Cfg.MagicLinkEnable {
		return ErrNotConfigured
	}
	if err := s.limitIP(r, "magic_link_ip", 20, time.Hour); err != nil {
		return err
	}
	email, err := NormalizeEmail(email)
	if err != nil {
		return err
	}
	if ok, _, err := s.Limiter.Allow(ctx, "magic_link_email", email, 5, time.Hour); err != nil {
		return err
	} else if !ok {
		return nil // silently drop: do not reveal throttling per address
	}
	if !s.Cfg.MagicLinkSignup {
		// Unknown address and sign-up disabled: answer exactly like success, but send nothing, so
		// the endpoint can be used neither to enumerate accounts nor to spam arbitrary inboxes.
		if _, err := s.Store.GetUserByVerifiedEmail(ctx, email); err != nil {
			if errors.Is(err, repo.ErrNotFound) {
				return nil
			}
			return err
		}
	}
	token := RandomToken(32)
	b, _ := json.Marshal(magicState{Email: email, RedirectTo: SafeRedirectPath(redirectTo)})
	if err := s.Redis.Set(ctx, s.flowKey("ml", token), b, s.Cfg.MagicLinkTTL).Err(); err != nil {
		return fmt.Errorf("auth: store magic link: %w", err)
	}
	// The token travels in the URL fragment: never sent to any server by the browser, so it does
	// not land in access logs, Referer headers, or link-scanner prefetches. The SPA POSTs it.
	link := s.Cfg.AppURL + "/auth/magic-link#token=" + token
	msg := Message{
		To: email, Subject: "Your Alpine sign-in link", Link: link,
		Text: "Click to sign in to Alpine (valid for " + s.Cfg.MagicLinkTTL.String() + "):\n\n" + link +
			"\n\nIf you did not request this, ignore this email.",
	}
	if err := s.Mailer.Send(ctx, msg); err != nil {
		return fmt.Errorf("auth: send magic link: %w", err)
	}
	s.authEvent(ctx, r, nil, nil, "magic_link.requested", "email", true, "", nil)
	return nil
}

// VerifyMagicLink consumes a magic-link token (single use) and logs the user in. Returns the SPA
// path to continue to.
func (s *Service) VerifyMagicLink(w http.ResponseWriter, r *http.Request, token string) (string, error) {
	ctx := r.Context()
	if !s.Cfg.MagicLinkEnable {
		return "", ErrNotConfigured
	}
	ip := s.ClientIP(r).String()
	if d, err := s.Lockout.Check(ctx, "magic_link", ip); err != nil {
		return "", err
	} else if d > 0 {
		return "", ErrLocked
	}
	var ms magicState
	b, err := s.Redis.GetDel(ctx, s.flowKey("ml", token)).Bytes()
	if err != nil || json.Unmarshal(b, &ms) != nil {
		_, _ = s.Lockout.Fail(ctx, "magic_link", ip)
		s.authEvent(ctx, r, nil, nil, "login.failed", "email", false, "invalid_magic_link", nil)
		return "", ErrInvalidState
	}
	ext := repo.ExternalIdentity{Provider: "email", Subject: ms.Email, Email: ms.Email, EmailVerified: true}
	if err := s.completeLogin(w, r, ext, "email", s.Cfg.MagicLinkSignup); err != nil {
		s.authEvent(ctx, r, nil, nil, "login.failed", "email", false, ErrorCode(err), nil)
		return "", err
	}
	_ = s.Lockout.Reset(ctx, "magic_link", ip)
	return SafeRedirectPath(ms.RedirectTo), nil
}

// DevLogin logs in as the seeded dev user (AUTH_DEV_LOGIN=true only).
func (s *Service) DevLogin(w http.ResponseWriter, r *http.Request) error {
	if !s.Cfg.DevLogin {
		return ErrNotConfigured
	}
	ext := repo.ExternalIdentity{Provider: "email", Subject: s.Cfg.DevLoginEmail, Email: s.Cfg.DevLoginEmail, EmailVerified: true}
	if err := s.completeLogin(w, r, ext, "dev", false); err != nil {
		if errors.Is(err, ErrSignupDisabled) {
			return repo.ErrNotFound
		}
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------------------------
// Session management

// Logout revokes the current session and clears the cookie.
func (s *Service) Logout(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	raw := s.rawSession(r)
	s.clearSessionCookie(w)
	if raw == "" {
		return nil
	}
	sess, err := s.Sessions.Get(ctx, raw)
	if err != nil {
		return nil
	}
	if err := s.Sessions.Delete(ctx, raw); err != nil {
		return err
	}
	var org *uuid.UUID
	if sess.ActiveOrgID != uuid.Nil {
		org = &sess.ActiveOrgID
	}
	s.authEvent(ctx, r, &sess.UserID, org, "logout", sess.AuthMethod, true, "", nil)
	return nil
}

// LogoutEverywhere revokes every session of the caller (all devices).
func (s *Service) LogoutEverywhere(w http.ResponseWriter, r *http.Request, p *Principal) (int, error) {
	if p == nil || p.Kind != KindSession {
		return 0, ErrUnauthenticated
	}
	n, err := s.Sessions.DeleteAllForUser(r.Context(), p.UserID)
	if err != nil {
		return 0, err
	}
	s.clearSessionCookie(w)
	s.authEvent(r.Context(), r, &p.UserID, nil, "logout.all", p.AuthMethod, true, "", map[string]any{"sessions": n})
	return n, nil
}

// SetActiveOrg switches the session's active org after checking membership.
func (s *Service) SetActiveOrg(r *http.Request, p *Principal, orgID uuid.UUID) error {
	ctx := r.Context()
	if p == nil || p.Kind != KindSession {
		return ErrUnauthenticated
	}
	if _, err := s.OrgAccess(ctx, p, orgID); err != nil {
		return err
	}
	sess, err := s.Sessions.Get(ctx, s.rawSession(r))
	if err != nil {
		return ErrUnauthenticated
	}
	return s.Sessions.SetActiveOrg(ctx, sess, orgID)
}

// ---------------------------------------------------------------------------------------------
// Rate limiting & events

func (s *Service) limitIP(r *http.Request, bucket string, limit int, window time.Duration) error {
	ok, _, err := s.Limiter.Allow(r.Context(), bucket, s.ClientIP(r).String(), limit, window)
	if err != nil {
		return err
	}
	if !ok {
		return ErrRateLimited
	}
	return nil
}

// authEvent records a user-level security event. Failures are logged, never surfaced: auth must
// not break because the event log is unavailable.
func (s *Service) authEvent(ctx context.Context, r *http.Request, user, org *uuid.UUID, event, provider string, ok bool, reason string, meta map[string]any) {
	err := s.Store.RecordAuthEvent(context.WithoutCancel(ctx), repo.AuthEvent{
		UserID: user, OrgID: org, Event: event, Provider: provider, Success: ok, Reason: reason,
		IP: s.ipPtr(r), Metadata: meta,
	})
	if err != nil {
		s.Logger.Warn("auth: record auth event failed", "event", event, "err", err)
	}
}

func (s *Service) orgAudit(r *http.Request, org uuid.UUID, a repo.Actor, action, targetType, targetID string, meta map[string]any) {
	a.IP = s.ipPtr(r)
	a.RequestID = r.Header.Get("X-Request-Id")
	if err := s.Store.Record(context.WithoutCancel(r.Context()), repo.AuditEntry{
		OrgID: org, Actor: a, Action: action, TargetType: targetType, TargetID: targetID, Metadata: meta,
	}); err != nil {
		s.Logger.Warn("auth: audit write failed", "action", action, "err", err)
	}
}
