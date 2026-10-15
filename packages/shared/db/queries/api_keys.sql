-- name: CreateAPIKey :one
INSERT INTO api_keys (org_id, name, prefix, secret_hash, scopes, role, created_by, expires_at)
VALUES (@org_id, @name, @prefix, @secret_hash, @scopes, @role, @created_by, @expires_at)
RETURNING *;

-- name: GetAPIKey :one
SELECT * FROM api_keys WHERE id = $1;

-- name: ListAPIKeys :many
SELECT * FROM api_keys WHERE org_id = $1 ORDER BY created_at DESC;

-- name: RevokeAPIKey :execrows
UPDATE api_keys SET revoked_at = now(), revoked_by = @revoked_by
WHERE id = @id AND org_id = @org_id AND revoked_at IS NULL;

-- name: TouchAPIKey :exec
-- Throttled to one write per minute per key to avoid write amplification on hot CI keys.
UPDATE api_keys SET last_used_at = now()
WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute');

-- name: ResolveAPIKey :one
-- Pre-tenant lookup (SECURITY DEFINER): prefix -> (key id, org id) for live keys only.
SELECT r.id::uuid AS id, r.org_id::uuid AS org_id FROM alpine_resolve_api_key(@prefix::text) AS r;
