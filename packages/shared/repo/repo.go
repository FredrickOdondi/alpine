// Package repo defines Alpine's tenancy & identity domain types and the repository interfaces the
// API's auth/org layer depends on. packages/shared/repo/pgrepo implements them on Postgres (sqlc +
// RLS); tests and other services can supply fakes.
//
// Conventions:
//   - Every org-scoped method runs in a transaction scoped to that org (RLS), so a caller can never
//     read or write another tenant's rows even if it passes a foreign id.
//   - Mutations that matter for security write their audit_log entry in the SAME transaction
//     (Actor describes who did it), so there is no change without an audit row.
package repo

import (
	"context"
	"errors"
	"net/netip"
	"time"

	"github.com/google/uuid"
)

// Errors returned by repositories.
var (
	ErrNotFound           = errors.New("repo: not found")
	ErrConflict           = errors.New("repo: conflict")
	ErrLastOwner          = errors.New("repo: an org must keep at least one owner")
	ErrUserDisabled       = errors.New("repo: user is disabled")
	ErrIdentityTaken      = errors.New("repo: identity is linked to another user")
	ErrInvitationInvalid  = errors.New("repo: invitation is invalid, revoked or already used")
	ErrInvitationExpired  = errors.New("repo: invitation has expired")
	ErrInvitationMismatch = errors.New("repo: invitation was sent to a different email")
)

// Role is an org role. The canonical list and permissions live in packages/shared/auth.
type Role string

// User is a global identity (may belong to many orgs).
type User struct {
	ID            uuid.UUID
	Email         string // normalized lower-case; "" if unknown
	EmailVerified bool
	Name          string
	AvatarURL     string
	GitHubLogin   string
	CreatedAt     time.Time
	LastLoginAt   *time.Time
}

// Identity is one login method attached to a user.
type Identity struct {
	ID            uuid.UUID
	UserID        uuid.UUID
	Provider      string // google | github | email | workos
	Subject       string
	Email         string
	EmailVerified bool
	HostedDomain  string
	Login         string
	CreatedAt     time.Time
	LastLoginAt   *time.Time
}

// ExternalIdentity is what a login provider asserted after verification.
type ExternalIdentity struct {
	Provider      string
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
	AvatarURL     string
	HostedDomain  string
	Login         string // GitHub login
	GitHubID      int64  // GitHub numeric id (also the Subject)
}

// LoginOutcome says how a login resolved to a user.
type LoginOutcome struct {
	Created bool // a new user was created
	Linked  bool // the identity was newly attached to an existing user (by verified email or explicit link)
}

// Org is a tenant ("workspace" in the UI).
type Org struct {
	ID        uuid.UUID
	Slug      string
	Name      string
	Plan      string
	CreatedAt time.Time
}

// Membership links a user to an org with a role.
type Membership struct {
	OrgID     uuid.UUID
	UserID    uuid.UUID
	Role      Role
	CreatedAt time.Time
}

// OrgMembership is an org plus the caller's role in it.
type OrgMembership struct {
	Org  Org
	Role Role
}

// Member is a membership with user profile data, for member lists.
type Member struct {
	Membership
	Email       string
	Name        string
	AvatarURL   string
	GitHubLogin string
	LastLoginAt *time.Time
}

// Invitation to join an org.
type Invitation struct {
	ID         uuid.UUID
	OrgID      uuid.UUID
	Email      string
	Role       Role
	InvitedBy  *uuid.UUID
	ExpiresAt  time.Time
	AcceptedAt *time.Time
	AcceptedBy *uuid.UUID
	RevokedAt  *time.Time
	CreatedAt  time.Time
}

// Status of an invitation at time now.
func (i Invitation) Status(now time.Time) string {
	switch {
	case i.AcceptedAt != nil:
		return "accepted"
	case i.RevokedAt != nil:
		return "revoked"
	case !now.Before(i.ExpiresAt):
		return "expired"
	default:
		return "pending"
	}
}

// APIKey is a per-org key. SecretHash is HMAC-SHA256(pepper, secret); the plaintext is shown once.
type APIKey struct {
	ID         uuid.UUID
	OrgID      uuid.UUID
	Name       string
	Prefix     string
	SecretHash []byte
	Scopes     []string
	Role       Role
	CreatedBy  *uuid.UUID
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
	CreatedAt  time.Time
}

// Active reports whether the key can authenticate at time now.
func (k APIKey) Active(now time.Time) bool {
	return k.RevokedAt == nil && (k.ExpiresAt == nil || now.Before(*k.ExpiresAt))
}

// NewAPIKey is the input to CreateAPIKey.
type NewAPIKey struct {
	OrgID      uuid.UUID
	Name       string
	Prefix     string
	SecretHash []byte
	Scopes     []string
	Role       Role
	ExpiresAt  *time.Time
}

// Actor identifies who performed a mutation, for the audit log.
type Actor struct {
	Type      string // user | api_key | system | agent
	ID        string
	IP        *netip.Addr
	RequestID string
}

// AuditEntry is one org-scoped audit_log row.
type AuditEntry struct {
	ID         int64
	OrgID      uuid.UUID
	Actor      Actor
	Action     string // dotted, e.g. member.role_changed
	TargetType string
	TargetID   string
	Metadata   map[string]any
	At         time.Time
}

// AuditFilter pages through the audit log (newest first).
type AuditFilter struct {
	Action   string
	BeforeAt *time.Time
	BeforeID int64
	Limit    int
}

// AuthEvent is a user-level security event (auth_events).
type AuthEvent struct {
	UserID   *uuid.UUID
	OrgID    *uuid.UUID
	Event    string // e.g. login.succeeded
	Provider string
	Success  bool
	Reason   string
	IP       *netip.Addr
	Metadata map[string]any
	At       time.Time
}

// Users manages global user identities and login resolution.
type Users interface {
	GetUser(ctx context.Context, id uuid.UUID) (User, error)
	// GetUserByVerifiedEmail finds an active user by verified (normalized) email.
	GetUserByVerifiedEmail(ctx context.Context, email string) (User, error)
	// ResolveLogin maps a verified external identity to a user: existing identity -> its user;
	// else an existing user with the same VERIFIED email (account linking); else (if allowCreate)
	// a new user. Runs atomically.
	ResolveLogin(ctx context.Context, ext ExternalIdentity, allowCreate bool) (User, LoginOutcome, error)
	// LinkIdentity explicitly attaches ext to userID (user is logged in and asked to link).
	LinkIdentity(ctx context.Context, userID uuid.UUID, ext ExternalIdentity) (Identity, error)
	ListIdentities(ctx context.Context, userID uuid.UUID) ([]Identity, error)
}

// Orgs manages orgs and memberships.
type Orgs interface {
	CreateOrg(ctx context.Context, actor Actor, owner uuid.UUID, slug, name string) (Org, error)
	GetOrg(ctx context.Context, orgID uuid.UUID) (Org, error)
	UpdateOrg(ctx context.Context, actor Actor, orgID uuid.UUID, name string) (Org, error)
	DeleteOrg(ctx context.Context, actor Actor, orgID uuid.UUID) error
	ListOrgsForUser(ctx context.Context, userID uuid.UUID) ([]OrgMembership, error)
	GetMembership(ctx context.Context, orgID, userID uuid.UUID) (Membership, error)
	ListMembers(ctx context.Context, orgID uuid.UUID) ([]Member, error)
	UpdateMemberRole(ctx context.Context, actor Actor, orgID, userID uuid.UUID, role Role) (Membership, error)
	RemoveMember(ctx context.Context, actor Actor, orgID, userID uuid.UUID) error
}

// Invitations manages email invitations.
type Invitations interface {
	// CreateInvitation supersedes any pending invitation for the same email in the org.
	CreateInvitation(ctx context.Context, actor Actor, orgID uuid.UUID, email string, role Role, tokenHash []byte, expiresAt time.Time) (Invitation, error)
	ListInvitations(ctx context.Context, orgID uuid.UUID, pendingOnly bool) ([]Invitation, error)
	RevokeInvitation(ctx context.Context, actor Actor, orgID, invitationID uuid.UUID) error
	// AcceptInvitation validates the token (pending, unexpired, email matches the user's VERIFIED
	// email) and creates the membership.
	AcceptInvitation(ctx context.Context, actor Actor, tokenHash []byte, user User) (Invitation, Membership, error)
}

// APIKeys manages per-org API keys.
type APIKeys interface {
	CreateAPIKey(ctx context.Context, actor Actor, k NewAPIKey) (APIKey, error)
	ListAPIKeys(ctx context.Context, orgID uuid.UUID) ([]APIKey, error)
	RevokeAPIKey(ctx context.Context, actor Actor, orgID, keyID uuid.UUID) error
	// LookupAPIKey resolves a live key by its public prefix (pre-tenant) and loads it under its
	// org's RLS scope. The caller verifies the secret hash, expiry and scopes.
	LookupAPIKey(ctx context.Context, prefix string) (APIKey, error)
	TouchAPIKey(ctx context.Context, orgID, keyID uuid.UUID) error
}

// Audit reads and appends audit records.
type Audit interface {
	Record(ctx context.Context, e AuditEntry) error
	List(ctx context.Context, orgID uuid.UUID, f AuditFilter) ([]AuditEntry, error)
	RecordAuthEvent(ctx context.Context, e AuthEvent) error
}

// Store bundles every repository (what the API wires up).
type Store interface {
	Users
	Orgs
	Invitations
	APIKeys
	Audit
}
