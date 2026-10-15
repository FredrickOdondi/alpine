-- name: CreateOrg :one
-- Must run in a transaction scoped to the new org id (RLS WITH CHECK id = current org).
INSERT INTO orgs (id, slug, name) VALUES ($1, $2, $3) RETURNING *;

-- name: GetOrg :one
SELECT * FROM orgs WHERE id = $1 AND deleted_at IS NULL;

-- name: SlugTaken :one
-- Runs without tenant scope; uses the narrow SECURITY DEFINER-free path: slugs are not secret.
SELECT EXISTS (SELECT 1 FROM orgs WHERE slug = $1)::boolean;

-- name: UpdateOrgName :one
UPDATE orgs SET name = @name WHERE id = @id AND deleted_at IS NULL RETURNING *;

-- name: SoftDeleteOrg :execrows
UPDATE orgs SET deleted_at = now() WHERE id = $1 AND deleted_at IS NULL;

-- name: ListOrgsForUser :many
-- Runs in a user-scoped transaction (app.user_id): memberships/orgs policies expose the caller's own rows.
SELECT o.*, m.role
FROM memberships m JOIN orgs o ON o.id = m.org_id
WHERE m.user_id = $1 AND o.deleted_at IS NULL
ORDER BY o.name;

-- name: GetMembership :one
SELECT m.* FROM memberships m JOIN orgs o ON o.id = m.org_id
WHERE m.org_id = $1 AND m.user_id = $2 AND o.deleted_at IS NULL;

-- name: AddMembership :one
INSERT INTO memberships (org_id, user_id, role, invited_by) VALUES ($1, $2, $3, $4)
ON CONFLICT (org_id, user_id) DO UPDATE SET role = memberships.role -- no-op; returns the existing row
RETURNING *;

-- name: ListMembers :many
SELECT m.org_id, m.user_id, m.role, m.created_at, u.email, u.name, u.avatar_url, u.github_login, u.last_login_at
FROM memberships m JOIN users u ON u.id = m.user_id
WHERE m.org_id = $1 AND u.deleted_at IS NULL
ORDER BY m.created_at;

-- name: UpdateMemberRole :one
UPDATE memberships SET role = @role WHERE org_id = @org_id AND user_id = @user_id RETURNING *;

-- name: RemoveMember :execrows
DELETE FROM memberships WHERE org_id = $1 AND user_id = $2;

-- name: CountOwners :one
SELECT count(*) FROM memberships WHERE org_id = $1 AND role = 'owner';

-- name: ListRolePermissions :many
SELECT role, permission FROM role_permissions ORDER BY role, permission;
