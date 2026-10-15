package auth

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/google/uuid"
)

// Errors.
var (
	ErrUnauthenticated  = errors.New("auth: unauthenticated")
	ErrForbidden        = errors.New("auth: forbidden")
	ErrNoActiveOrg      = errors.New("auth: no active org")
	ErrRateLimited      = errors.New("auth: too many attempts")
	ErrLocked           = errors.New("auth: temporarily locked after repeated failures")
	ErrNotConfigured    = errors.New("auth: provider not configured")
	ErrInvalidState     = errors.New("auth: invalid or expired login state")
	ErrDomainNotAllowed = errors.New("auth: email domain not allowed")
	ErrEmailUnverified  = errors.New("auth: provider did not return a verified email")
	ErrCSRF             = errors.New("auth: CSRF check failed")
)

// Kind of credential behind a principal.
type Kind string

// Principal kinds.
const (
	KindSession Kind = "session"
	KindAPIKey  Kind = "api_key"
)

// Principal is the authenticated caller of a request.
type Principal struct {
	Kind   Kind
	UserID uuid.UUID // Nil for API keys
	OrgID  uuid.UUID // active org (session) or the key's org; Nil if the user has no org yet
	Role   Role      // role in OrgID; "" when OrgID is Nil
	// Session-only.
	SessionKey string // hashed session key (never the raw cookie value)
	CSRFToken  string
	AuthMethod string // google | github | email | dev | workos
	// API-key-only.
	APIKeyID uuid.UUID
	Scopes   []string
}

// Can reports whether the principal may perform p in its org.
func (p *Principal) Can(perm Permission) bool {
	if p == nil || p.OrgID == uuid.Nil || !Can(p.Role, perm) {
		return false
	}
	if p.Kind != KindAPIKey {
		return true
	}
	for _, s := range p.Scopes {
		if slices.Contains(ScopePermissions[s], perm) {
			return true
		}
	}
	return false
}

// ActorType for audit entries.
func (p *Principal) ActorType() string {
	if p != nil && p.Kind == KindAPIKey {
		return "api_key"
	}
	return "user"
}

// ActorID for audit entries.
func (p *Principal) ActorID() string {
	if p == nil {
		return "anonymous"
	}
	if p.Kind == KindAPIKey {
		return p.APIKeyID.String()
	}
	return p.UserID.String()
}

// Authenticator resolves the caller of a request to (userID, orgID, role). It is the contract
// the rest of the API (jeff/core) depends on; *Service implements it. userID is Nil for API-key
// callers; orgID/role are the active org (session) or the key's org.
type Authenticator interface {
	Authenticate(r *http.Request) (userID, orgID uuid.UUID, role Role, err error)
}

// PrincipalAuthenticator is the richer form used by middleware.
type PrincipalAuthenticator interface {
	AuthenticatePrincipal(r *http.Request) (*Principal, error)
}

type ctxKey struct{}

// WithPrincipal stores p in ctx.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// PrincipalFrom returns the principal stored by the middleware, or nil.
func PrincipalFrom(ctx context.Context) *Principal {
	p, _ := ctx.Value(ctxKey{}).(*Principal)
	return p
}
