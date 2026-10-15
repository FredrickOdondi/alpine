-- name: InsertAuditLog :exec
INSERT INTO audit_log (org_id, actor_type, actor_id, action, target_type, target_id, metadata, ip, request_id)
VALUES (@org_id, @actor_type, @actor_id, @action, @target_type, @target_id, @metadata, sqlc.narg(ip), sqlc.narg(request_id));

-- name: ListAuditLog :many
-- Keyset pagination on (at, id) descending.
SELECT * FROM audit_log
WHERE org_id = @org_id
  AND (sqlc.narg(before_at)::timestamptz IS NULL OR (at, id) < (sqlc.narg(before_at)::timestamptz, @before_id::bigint))
  AND (sqlc.narg(action)::text IS NULL OR action = sqlc.narg(action)::text)
ORDER BY at DESC, id DESC
LIMIT @lim;

-- name: InsertAuthEvent :exec
INSERT INTO auth_events (user_id, org_id, event, provider, outcome, reason, ip, metadata)
VALUES (sqlc.narg(user_id), sqlc.narg(org_id), @event, sqlc.narg(provider), @outcome, sqlc.narg(reason), sqlc.narg(ip), @metadata);

-- name: ListAuthEventsForUser :many
SELECT * FROM auth_events WHERE user_id = $1 ORDER BY at DESC LIMIT $2;
