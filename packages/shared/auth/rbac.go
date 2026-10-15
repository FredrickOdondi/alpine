// Package auth implements Alpine's authentication and authorization core: login providers
// (Google OIDC primary, GitHub OAuth, email magic link, WorkOS SSO seam), Redis-backed server-side
// sessions, CSRF protection for the SPA, per-org API keys, RBAC, rate limiting and lockout.
//
// HTTP handlers live in services/api (generated from api/openapi.yaml); this package is
// transport-light so the orchestrator/integrations services can reuse the Authenticator.
package auth

import "slices"

// Role is an org role, ordered owner > admin > factory_owner > member > viewer.
type Role string

// Roles.
const (
	RoleOwner        Role = "owner"
	RoleAdmin        Role = "admin"
	RoleFactoryOwner Role = "factory_owner" // owns repo factory configs, budgets and approvals
	RoleMember       Role = "member"
	RoleViewer       Role = "viewer"
)

// AllRoles in descending privilege.
var AllRoles = []Role{RoleOwner, RoleAdmin, RoleFactoryOwner, RoleMember, RoleViewer}

// Valid reports whether r is a known role.
func (r Role) Valid() bool { return slices.Contains(AllRoles, r) }

// Rank is higher for more privileged roles (0 for unknown).
func (r Role) Rank() int {
	switch r {
	case RoleOwner:
		return 5
	case RoleAdmin:
		return 4
	case RoleFactoryOwner:
		return 3
	case RoleMember:
		return 2
	case RoleViewer:
		return 1
	}
	return 0
}

// Permission is an action on a resource ("resource:verb").
type Permission string

// Permissions. Keep in sync with the role_permissions table (00007_auth.sql); TestPolicyMatchesDB
// fails if they drift.
const (
	PermOrgRead             Permission = "org:read"
	PermOrgUpdate           Permission = "org:update"
	PermOrgDelete           Permission = "org:delete"
	PermOrgTransfer         Permission = "org:transfer"
	PermMembersRead         Permission = "members:read"
	PermMembersInvite       Permission = "members:invite"
	PermMembersUpdate       Permission = "members:update"
	PermMembersRemove       Permission = "members:remove"
	PermAPIKeysRead         Permission = "api_keys:read"
	PermAPIKeysManage       Permission = "api_keys:manage"
	PermAuditRead           Permission = "audit:read"
	PermSSOManage           Permission = "sso:manage"
	PermBillingRead         Permission = "billing:read"
	PermBillingManage       Permission = "billing:manage"
	PermReposRead           Permission = "repos:read"
	PermReposManage         Permission = "repos:manage"
	PermFactoryConfigRead   Permission = "factory_config:read"
	PermFactoryConfigManage Permission = "factory_config:manage"
	PermTicketsRead         Permission = "tickets:read"
	PermTicketsWrite        Permission = "tickets:write"
	PermTicketsApprove      Permission = "tickets:approve"
	PermModelKeysManage     Permission = "model_keys:manage"
	PermBudgetsManage       Permission = "budgets:manage"
	PermMetricsRead         Permission = "metrics:read"
	PermWebhooksIngest      Permission = "webhooks:ingest"
)

// AllPermissions lists every permission.
var AllPermissions = []Permission{
	PermOrgRead, PermOrgUpdate, PermOrgDelete, PermOrgTransfer,
	PermMembersRead, PermMembersInvite, PermMembersUpdate, PermMembersRemove,
	PermAPIKeysRead, PermAPIKeysManage, PermAuditRead, PermSSOManage,
	PermBillingRead, PermBillingManage,
	PermReposRead, PermReposManage, PermFactoryConfigRead, PermFactoryConfigManage,
	PermTicketsRead, PermTicketsWrite, PermTicketsApprove,
	PermModelKeysManage, PermBudgetsManage, PermMetricsRead, PermWebhooksIngest,
}

// Policy is the RBAC table: role -> granted permissions.
var Policy = buildPolicy()

func buildPolicy() map[Role]map[Permission]bool {
	grant := func(ps ...Permission) map[Permission]bool {
		m := map[Permission]bool{}
		for _, p := range ps {
			m[p] = true
		}
		return m
	}
	all := grant(AllPermissions...)
	admin := grant(AllPermissions...)
	delete(admin, PermOrgDelete)
	delete(admin, PermOrgTransfer)
	delete(admin, PermBillingManage)
	return map[Role]map[Permission]bool{
		RoleOwner: all,
		RoleAdmin: admin,
		RoleFactoryOwner: grant(PermOrgRead, PermMembersRead, PermReposRead, PermReposManage, PermFactoryConfigRead,
			PermFactoryConfigManage, PermTicketsRead, PermTicketsWrite, PermTicketsApprove, PermBudgetsManage,
			PermAPIKeysRead, PermAuditRead, PermMetricsRead, PermWebhooksIngest),
		RoleMember: grant(PermOrgRead, PermMembersRead, PermReposRead, PermFactoryConfigRead, PermTicketsRead,
			PermTicketsWrite, PermTicketsApprove, PermMetricsRead),
		RoleViewer: grant(PermOrgRead, PermMembersRead, PermReposRead, PermFactoryConfigRead, PermTicketsRead,
			PermMetricsRead),
	}
}

// Can reports whether role r has permission p.
func Can(r Role, p Permission) bool { return Policy[r][p] }

// CanAssignRole reports whether an actor with role actor may grant (or change someone to/from)
// role target. Owners can assign anything; admins can manage roles below owner; nobody else can.
func CanAssignRole(actor, target Role) bool {
	switch actor {
	case RoleOwner:
		return target.Valid()
	case RoleAdmin:
		return target.Valid() && target != RoleOwner
	}
	return false
}

// API key scopes and the permissions each one unlocks. A key's effective permissions are the
// intersection of its role's permissions and the union of its scopes'.
var ScopePermissions = map[string][]Permission{
	"tickets:read":       {PermOrgRead, PermTicketsRead},
	"tickets:write":      {PermOrgRead, PermTicketsRead, PermTicketsWrite},
	"repos:read":         {PermOrgRead, PermReposRead, PermFactoryConfigRead},
	"pull_requests:read": {PermOrgRead, PermTicketsRead},
	"webhooks:write":     {PermWebhooksIngest},
	"metrics:read":       {PermOrgRead, PermMetricsRead},
	"audit:read":         {PermOrgRead, PermAuditRead},
	"members:read":       {PermOrgRead, PermMembersRead},
}

// ValidScope reports whether s is a known API key scope.
func ValidScope(s string) bool { _, ok := ScopePermissions[s]; return ok }

// APIKeyRoles are the roles an API key may carry (keys never act as owner).
var APIKeyRoles = []Role{RoleAdmin, RoleMember, RoleViewer}
