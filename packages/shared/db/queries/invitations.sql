-- name: CreateInvitation :one
INSERT INTO invitations (org_id, email, role, token_hash, invited_by, expires_at)
VALUES (@org_id, lower(btrim(@email::text)), @role, @token_hash, @invited_by, @expires_at)
RETURNING *;

-- name: RevokePendingInvitationsForEmail :execrows
UPDATE invitations SET revoked_at = now()
WHERE org_id = @org_id AND email = lower(btrim(@email::text)) AND accepted_at IS NULL AND revoked_at IS NULL;

-- name: GetInvitation :one
SELECT * FROM invitations WHERE id = $1;

-- name: ListInvitations :many
SELECT * FROM invitations
WHERE org_id = @org_id AND (NOT @pending_only::boolean OR (accepted_at IS NULL AND revoked_at IS NULL AND expires_at > now()))
ORDER BY created_at DESC
LIMIT 200;

-- name: RevokeInvitation :execrows
UPDATE invitations SET revoked_at = now()
WHERE id = @id AND org_id = @org_id AND accepted_at IS NULL AND revoked_at IS NULL;

-- name: MarkInvitationAccepted :execrows
UPDATE invitations SET accepted_at = now(), accepted_by = @user_id
WHERE id = @id AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > now();

-- name: ResolveInvitation :one
-- Pre-tenant lookup (SECURITY DEFINER): token hash -> (invitation id, org id).
SELECT r.id::uuid AS id, r.org_id::uuid AS org_id FROM alpine_resolve_invitation(@token_hash::bytea) AS r;
