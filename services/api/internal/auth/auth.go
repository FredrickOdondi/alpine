// Package auth adapts jeff/data's shared auth service (packages/shared/auth: OAuth, sessions,
// API keys, RBAC) to the core API's small Authenticator/Identity seam, and provides the
// AUTH_DEV_STUB dev principal for local runs without a login.
//
// Request flow: authapi.Router runs sharedauth.Service.Middleware (resolves the session cookie or
// API key into a *sharedauth.Principal) and CSRF; core handlers then call FromRequest with the
// Principals authenticator to get an Identity (user, active org, role) and check permissions.
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"

	sharedauth "github.com/alpine-dev/alpine/packages/shared/auth"
	"github.com/alpine-dev/alpine/packages/shared/repo"
)

// Roles, matching memberships.role (sharedauth.Role values).
const (
	RoleOwner  = string(sharedauth.RoleOwner)
	RoleAdmin  = string(sharedauth.RoleAdmin)
	RoleMember = string(sharedauth.RoleMember)
	RoleViewer = string(sharedauth.RoleViewer)
)

// ErrUnauthenticated means no valid session/credential was presented (or the caller has no
// active org yet).
var ErrUnauthenticated = errors.New("auth: unauthenticated")

// Authenticator resolves the caller of a request to (userID, orgID, role). orgID is the caller's
// active org; every tenant-scoped query runs under it (Postgres RLS).
type Authenticator interface {
	Authenticate(r *http.Request) (userID, orgID uuid.UUID, role string, err error)
}

// Identity is the authenticated caller.
type Identity struct {
	UserID uuid.UUID
	OrgID  uuid.UUID
	Role   string
	// Principal is the shared-auth principal behind this identity (nil for custom
	// Authenticators); it carries API-key scopes for permission checks.
	Principal *sharedauth.Principal
}

// Can reports whether the caller holds perm in its active org (role + API-key scopes).
func (i Identity) Can(perm sharedauth.Permission) bool {
	if i.Principal != nil {
		return i.Principal.Can(perm)
	}
	return i.OrgID != uuid.Nil && sharedauth.Can(sharedauth.Role(i.Role), perm)
}

// CanWrite reports whether the caller may act on tickets (approve, close, …).
func (i Identity) CanWrite() bool { return i.Can(sharedauth.PermTicketsWrite) }

// Actor is the audit/workflow actor string ("user:<id>" or "api_key:<id>").
func (i Identity) Actor() string {
	if i.Principal != nil && i.Principal.Kind == sharedauth.KindAPIKey {
		return "api_key:" + i.Principal.APIKeyID.String()
	}
	return "user:" + i.UserID.String()
}

// Principals is the production Authenticator: it reads the Principal that the shared auth
// middleware resolved for this request.
type Principals struct{}

// Authenticate implements Authenticator.
func (Principals) Authenticate(r *http.Request) (uuid.UUID, uuid.UUID, string, error) {
	p := sharedauth.PrincipalFrom(r.Context())
	if p == nil || p.OrgID == uuid.Nil {
		return uuid.Nil, uuid.Nil, "", ErrUnauthenticated
	}
	return p.UserID, p.OrgID, string(p.Role), nil
}

// FromRequest authenticates r with a and returns the caller identity.
func FromRequest(a Authenticator, r *http.Request) (Identity, error) {
	user, org, role, err := a.Authenticate(r)
	if err != nil {
		return Identity{}, err
	}
	return Identity{UserID: user, OrgID: org, Role: role, Principal: sharedauth.PrincipalFrom(r.Context())}, nil
}

// DevStubCSRFToken is the fixed CSRF token of the dev stub principal (returned by GET /v1/me, so
// the SPA works unchanged; curl must send it as X-CSRF-Token on unsafe requests).
const DevStubCSRFToken = "dev-stub-csrf"

// DevStub makes every credential-less request the seeded dev user (AUTH_DEV_STUB=true). It is a
// session-kind principal with AuthMethod "dev", so RBAC and CSRF behave as for a real session.
// Requests that carry a session cookie or an Authorization header go through real auth. Never
// enable it outside local development.
type DevStub struct {
	UserID     uuid.UUID
	OrgID      uuid.UUID
	Role       sharedauth.Role
	CookieName string // session cookie name (requests with it use real auth)
}

// LookupDevStub resolves the dev user by verified email (seeded dev@alpine.local) and its first
// org membership (jeff/data's repo.Store).
func LookupDevStub(ctx context.Context, users repo.Store, email, cookieName string) (DevStub, error) {
	u, err := users.GetUserByVerifiedEmail(ctx, email)
	if err != nil {
		return DevStub{}, fmt.Errorf("dev stub user %s: %w (run `make seed`)", email, err)
	}
	orgs, err := users.ListOrgsForUser(ctx, u.ID)
	if err != nil {
		return DevStub{}, err
	}
	if len(orgs) == 0 {
		return DevStub{}, fmt.Errorf("dev stub user %s has no org (run `make seed`)", email)
	}
	return DevStub{UserID: u.ID, OrgID: orgs[0].Org.ID, Role: sharedauth.Role(orgs[0].Role), CookieName: cookieName}, nil
}

// Principal is the injected dev principal.
func (d DevStub) Principal() *sharedauth.Principal {
	return &sharedauth.Principal{Kind: sharedauth.KindSession, UserID: d.UserID, OrgID: d.OrgID, Role: d.Role,
		CSRFToken: DevStubCSRFToken, AuthMethod: "dev"}
}

// Middleware injects the dev principal into credential-less requests. Wrap it OUTSIDE
// authapi.Router so the shared middleware still authenticates real credentials.
func (d DevStub) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hasCookie := false
		if d.CookieName != "" {
			if c, err := r.Cookie(d.CookieName); err == nil && c.Value != "" {
				hasCookie = true
			}
		}
		// Webhooks are HMAC-authenticated machine traffic and /v1/auth/* is the real login flow:
		// neither gets the dev identity (it would also trip the session CSRF check).
		if strings.HasPrefix(r.URL.Path, "/v1/webhooks/") || strings.HasPrefix(r.URL.Path, "/v1/auth/") ||
			hasCookie || r.Header.Get("Authorization") != "" || sharedauth.PrincipalFrom(r.Context()) != nil {
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(sharedauth.WithPrincipal(r.Context(), d.Principal())))
	})
}
