-- Users and login identities are global (no org_id, no RLS); they hold no secrets.

-- name: GetUser :one
SELECT * FROM users WHERE id = $1 AND deleted_at IS NULL;

-- name: GetUserByVerifiedEmail :one
SELECT * FROM users WHERE email = lower(btrim(@email::text)) AND email_verified AND deleted_at IS NULL;

-- name: CreateUser :one
INSERT INTO users (email, email_verified, name, avatar_url, github_id, github_login, last_login_at)
VALUES (lower(btrim(sqlc.narg(email)::text)), @email_verified, sqlc.narg(name), sqlc.narg(avatar_url),
        sqlc.narg(github_id), sqlc.narg(github_login), now())
RETURNING *;

-- name: UpdateUserOnLogin :one
-- Fills profile gaps from the provider without overwriting what the user already has, upgrades the
-- email to verified when the provider asserts it, and stamps last_login_at.
UPDATE users SET
  name           = COALESCE(name, sqlc.narg(name)),
  avatar_url     = COALESCE(avatar_url, sqlc.narg(avatar_url)),
  email          = CASE WHEN email IS NULL OR (NOT email_verified AND @email_verified::boolean)
                        THEN COALESCE(lower(btrim(sqlc.narg(email)::text)), email) ELSE email END,
  email_verified = email_verified OR (@email_verified::boolean AND sqlc.narg(email)::text IS NOT NULL),
  github_id      = COALESCE(github_id, sqlc.narg(github_id)),
  github_login   = COALESCE(sqlc.narg(github_login), github_login),
  last_login_at  = now()
WHERE id = @id AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteUser :execrows
UPDATE users SET deleted_at = now() WHERE id = $1 AND deleted_at IS NULL;

-- name: GetIdentity :one
SELECT * FROM user_identities WHERE provider = $1 AND subject = $2;

-- name: ListIdentitiesForUser :many
SELECT * FROM user_identities WHERE user_id = $1 ORDER BY created_at;

-- name: CreateIdentity :one
INSERT INTO user_identities (user_id, provider, subject, email, email_verified, hosted_domain, login, last_login_at)
VALUES (@user_id, @provider, @subject, lower(btrim(sqlc.narg(email)::text)), @email_verified,
        sqlc.narg(hosted_domain), sqlc.narg(login), now())
RETURNING *;

-- name: TouchIdentity :exec
UPDATE user_identities SET
  email = lower(btrim(sqlc.narg(email)::text)), email_verified = @email_verified,
  hosted_domain = sqlc.narg(hosted_domain), login = sqlc.narg(login), last_login_at = now()
WHERE id = @id;
