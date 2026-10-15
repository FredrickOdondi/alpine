package auth

import (
	"context"

	"github.com/google/uuid"

	"github.com/alpine-dev/alpine/packages/shared/repo"
)

// SSOProvider is the seam for enterprise SSO (SAML/OIDC through WorkOS in P3). A WorkOS adapter
// implements it with the WorkOS SSO API: AuthorizationURL -> /sso/authorize with the org's
// connection, Profile -> /sso/token. Profiles map to repo.ExternalIdentity{Provider: "workos"}
// and flow through the same ResolveLogin/session code as Google and GitHub. Domain routing uses
// the alpine_resolve_sso_domain() SQL function over org_sso_connections.
type SSOProvider interface {
	AuthorizationURL(ctx context.Context, connectionID, state, redirectURI string) (string, error)
	// Profile exchanges the callback code; orgID is the org the connection belongs to.
	Profile(ctx context.Context, code string) (ext repo.ExternalIdentity, orgID uuid.UUID, err error)
}

// NoSSO is the default SSOProvider: SSO is not configured.
type NoSSO struct{}

// AuthorizationURL implements SSOProvider.
func (NoSSO) AuthorizationURL(context.Context, string, string, string) (string, error) {
	return "", ErrNotConfigured
}

// Profile implements SSOProvider.
func (NoSSO) Profile(context.Context, string) (repo.ExternalIdentity, uuid.UUID, error) {
	return repo.ExternalIdentity{}, uuid.Nil, ErrNotConfigured
}
