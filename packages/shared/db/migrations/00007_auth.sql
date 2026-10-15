-- +goose Up
-- Authentication & access: login identities (Google OIDC primary, GitHub OAuth, email magic link,
-- WorkOS SSO later), invitations, per-org API keys, the RBAC policy table, SSO connections and a
-- user-level security event log. Sessions live in Redis, not here.

-- users: Google-first login means a user may have no GitHub account. github_id/github_login are
-- kept (nullable) for compatibility; user_identities is the source of truth for logins.
ALTER TABLE users ALTER COLUMN github_id DROP NOT NULL, ALTER COLUMN github_login DROP NOT NULL;
ALTER TABLE users ADD COLUMN email_verified boolean NOT NULL DEFAULT false;
UPDATE users SET email = lower(btrim(email)) WHERE email IS NOT NULL AND email <> lower(btrim(email));
ALTER TABLE users ADD CONSTRAINT users_email_normalized_chk CHECK (email = lower(btrim(email)));
-- Account linking is by VERIFIED email, so a verified email maps to at most one live user.
CREATE UNIQUE INDEX users_verified_email_uq ON users(email) WHERE email_verified AND deleted_at IS NULL;
CREATE INDEX users_email_idx ON users(email) WHERE email IS NOT NULL;

CREATE TABLE user_identities (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id         uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  provider        text NOT NULL CHECK (provider IN ('google','github','email','workos')),
  -- Stable provider subject: Google `sub`, GitHub numeric user id, the email for magic links,
  -- WorkOS profile id. Never the (mutable) email for OAuth providers.
  subject         text NOT NULL CHECK (length(subject) BETWEEN 1 AND 255),
  email           text CHECK (email = lower(btrim(email))),
  email_verified  boolean NOT NULL DEFAULT false,
  hosted_domain   text,          -- Google Workspace `hd` claim
  login           text,          -- GitHub login (display only)
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  last_login_at   timestamptz,
  UNIQUE (provider, subject),
  UNIQUE (user_id, provider)
);
CREATE TRIGGER user_identities_touch BEFORE UPDATE ON user_identities
  FOR EACH ROW EXECUTE FUNCTION alpine_touch_updated_at();
INSERT INTO user_identities (user_id, provider, subject, email, email_verified, login)
  SELECT id, 'github', github_id::text, email, false, github_login FROM users WHERE github_id IS NOT NULL;

-- Memberships: who invited whom; at least one owner per org (trigger below).
ALTER TABLE memberships ADD COLUMN invited_by uuid REFERENCES users(id) ON DELETE SET NULL;
CREATE INDEX memberships_org_role_idx ON memberships(org_id, role);

-- +goose StatementBegin
CREATE FUNCTION alpine_memberships_keep_owner() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public, pg_temp AS $$
BEGIN
  IF OLD.role = 'owner' AND (TG_OP = 'DELETE' OR NEW.role <> 'owner' OR NEW.org_id <> OLD.org_id) THEN
    IF EXISTS (SELECT 1 FROM orgs WHERE id = OLD.org_id)
       AND NOT EXISTS (SELECT 1 FROM memberships
                       WHERE org_id = OLD.org_id AND role = 'owner' AND user_id <> OLD.user_id) THEN
      RAISE EXCEPTION 'org % must keep at least one owner', OLD.org_id USING ERRCODE = '23514';
    END IF;
  END IF;
  RETURN NULL;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER memberships_keep_owner AFTER UPDATE OR DELETE ON memberships
  FOR EACH ROW EXECUTE FUNCTION alpine_memberships_keep_owner();

-- RBAC policy table. Mirrors packages/shared/auth.Policy (a test asserts they are identical).
-- Global and read-only to the app.
CREATE TABLE role_permissions (
  role        text NOT NULL CHECK (role IN ('owner','admin','factory_owner','member','viewer')),
  permission  text NOT NULL CHECK (permission ~ '^[a-z_]+:[a-z_]+$'),
  PRIMARY KEY (role, permission)
);
INSERT INTO role_permissions (role, permission)
SELECT r, p FROM (VALUES
  ('org:read'), ('org:update'), ('org:delete'), ('org:transfer'),
  ('members:read'), ('members:invite'), ('members:update'), ('members:remove'),
  ('api_keys:read'), ('api_keys:manage'), ('audit:read'), ('sso:manage'),
  ('billing:read'), ('billing:manage'),
  ('repos:read'), ('repos:manage'), ('factory_config:read'), ('factory_config:manage'),
  ('tickets:read'), ('tickets:write'), ('tickets:approve'),
  ('model_keys:manage'), ('budgets:manage'), ('metrics:read'), ('webhooks:ingest')
) AS perms(p)
CROSS JOIN LATERAL (VALUES ('owner'),('admin'),('factory_owner'),('member'),('viewer')) AS roles(r)
WHERE CASE r
  WHEN 'owner' THEN true
  WHEN 'admin' THEN p NOT IN ('org:delete','org:transfer','billing:manage')
  WHEN 'factory_owner' THEN p IN ('org:read','members:read','repos:read','repos:manage','factory_config:read',
                                  'factory_config:manage','tickets:read','tickets:write','tickets:approve',
                                  'budgets:manage','api_keys:read','audit:read','metrics:read','webhooks:ingest')
  WHEN 'member' THEN p IN ('org:read','members:read','repos:read','factory_config:read','tickets:read',
                           'tickets:write','tickets:approve','metrics:read')
  WHEN 'viewer' THEN p IN ('org:read','members:read','repos:read','factory_config:read','tickets:read',
                           'metrics:read')
END;
REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON role_permissions FROM alpine_app;

-- Invitations: the raw token is only ever in the emailed link; we store SHA-256(token).
CREATE TABLE invitations (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id       uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  email        text NOT NULL CHECK (email = lower(btrim(email)) AND email ~ '^[^@\s]+@[^@\s]+\.[^@\s]+$'),
  role         text NOT NULL CHECK (role IN ('owner','admin','factory_owner','member','viewer')),
  token_hash   bytea NOT NULL UNIQUE CHECK (length(token_hash) = 32),
  invited_by   uuid REFERENCES users(id) ON DELETE SET NULL,
  expires_at   timestamptz NOT NULL,
  accepted_at  timestamptz,
  accepted_by  uuid REFERENCES users(id) ON DELETE SET NULL,
  revoked_at   timestamptz,
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  CHECK (expires_at > created_at),
  CHECK (accepted_at IS NULL OR revoked_at IS NULL),
  CHECK ((accepted_at IS NULL) = (accepted_by IS NULL))
);
CREATE UNIQUE INDEX invitations_pending_uq ON invitations(org_id, email) WHERE accepted_at IS NULL AND revoked_at IS NULL;
CREATE INDEX invitations_org_created_idx ON invitations(org_id, created_at DESC);
CREATE INDEX invitations_expiry_idx ON invitations(expires_at) WHERE accepted_at IS NULL AND revoked_at IS NULL;
CREATE TRIGGER invitations_touch BEFORE UPDATE ON invitations FOR EACH ROW EXECUTE FUNCTION alpine_touch_updated_at();

-- Per-org API keys for CI and webhooks. Presented as `alp_<prefix>_<secret>`; we store the public
-- prefix (lookup) and HMAC-SHA256(pepper, secret). Scoped, optionally expiring, revocable.
CREATE TABLE api_keys (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id        uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  name          text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 100),
  prefix        text NOT NULL UNIQUE CHECK (prefix ~ '^[a-z0-9]{12}$'),
  secret_hash   bytea NOT NULL CHECK (length(secret_hash) = 32),
  scopes        text[] NOT NULL CHECK (cardinality(scopes) > 0 AND scopes <@ ARRAY[
                  'tickets:read','tickets:write','repos:read','pull_requests:read',
                  'webhooks:write','metrics:read','audit:read','members:read']::text[]),
  role          text NOT NULL DEFAULT 'member' CHECK (role IN ('admin','member','viewer')),
  created_by    uuid REFERENCES users(id) ON DELETE SET NULL,
  expires_at    timestamptz,
  last_used_at  timestamptz,
  revoked_at    timestamptz,
  revoked_by    uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  CHECK (expires_at IS NULL OR expires_at > created_at)
);
CREATE INDEX api_keys_org_idx ON api_keys(org_id, created_at DESC);
CREATE TRIGGER api_keys_touch BEFORE UPDATE ON api_keys FOR EACH ROW EXECUTE FUNCTION alpine_touch_updated_at();

-- SSO connections (WorkOS SAML/OIDC, P3). Present now so the auth layer has a stable seam.
CREATE TABLE org_sso_connections (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id       uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  provider     text NOT NULL DEFAULT 'workos' CHECK (provider IN ('workos')),
  external_id  text NOT NULL,
  domains      text[] NOT NULL DEFAULT '{}',
  enforced     boolean NOT NULL DEFAULT false, -- when true, members must log in via SSO
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  UNIQUE (provider, external_id)
);
CREATE INDEX org_sso_connections_org_idx ON org_sso_connections(org_id);
CREATE INDEX org_sso_connections_domains_idx ON org_sso_connections USING gin (domains);
CREATE TRIGGER org_sso_connections_touch BEFORE UPDATE ON org_sso_connections
  FOR EACH ROW EXECUTE FUNCTION alpine_touch_updated_at();

-- User-level security log (login, logout, linking, lockouts) — events that are about a person,
-- not an org. Org-scoped events (membership, invites, keys) go to audit_log. Partitioned monthly;
-- append-only; a user can read their own events.
CREATE SEQUENCE auth_events_seq;
CREATE TABLE auth_events (
  id          bigint NOT NULL DEFAULT nextval('auth_events_seq'),
  user_id     uuid,
  org_id      uuid,
  event       text NOT NULL CHECK (event ~ '^[a-z_]+(\.[a-z_]+)+$'),
  provider    text CHECK (provider IN ('google','github','email','workos','api_key','dev')),
  outcome     text NOT NULL DEFAULT 'success' CHECK (outcome IN ('success','failure')),
  reason      text,
  ip          inet,
  metadata    jsonb NOT NULL DEFAULT '{}'::jsonb,
  at          timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (id, at)
) PARTITION BY RANGE (at);
ALTER SEQUENCE auth_events_seq OWNED BY auth_events.id;
CREATE INDEX auth_events_user_at_idx ON auth_events(user_id, at DESC) WHERE user_id IS NOT NULL;
SELECT alpine_ensure_monthly_partitions('auth_events', 1, 3);
CREATE TRIGGER auth_events_no_update BEFORE UPDATE OR DELETE ON auth_events
  FOR EACH ROW EXECUTE FUNCTION alpine_audit_log_immutable();
REVOKE UPDATE, DELETE, TRUNCATE ON auth_events FROM alpine_app;
ALTER TABLE auth_events ENABLE ROW LEVEL SECURITY;
CREATE POLICY auth_events_read_own ON auth_events FOR SELECT USING (user_id = alpine_current_user());
CREATE POLICY auth_events_append ON auth_events FOR INSERT WITH CHECK (true);

-- Tenant RLS for the new org tables.
-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['invitations','api_keys','org_sso_connections']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (org_id = alpine_current_org()) WITH CHECK (org_id = alpine_current_org())', t);
  END LOOP;
END
$$;
-- +goose StatementEnd

-- Pre-tenant lookups (SECURITY DEFINER, ids only). The caller then re-reads the full row inside
-- a transaction scoped to the returned org, so RLS still governs everything else.
-- +goose StatementBegin
CREATE FUNCTION alpine_resolve_api_key(p_prefix text)
RETURNS TABLE (id uuid, org_id uuid)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
  SELECT k.id, k.org_id FROM api_keys k JOIN orgs o ON o.id = k.org_id
  WHERE k.prefix = p_prefix AND k.revoked_at IS NULL AND o.deleted_at IS NULL
$$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE FUNCTION alpine_resolve_invitation(p_token_hash bytea)
RETURNS TABLE (id uuid, org_id uuid)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
  SELECT i.id, i.org_id FROM invitations i JOIN orgs o ON o.id = i.org_id
  WHERE i.token_hash = p_token_hash AND o.deleted_at IS NULL
$$;
-- +goose StatementEnd
-- SSO routing by email domain (P3): which org/connection should handle alice@acme.com.
-- +goose StatementBegin
CREATE FUNCTION alpine_resolve_sso_domain(p_domain text)
RETURNS TABLE (org_id uuid, connection_id uuid, enforced boolean)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
  SELECT c.org_id, c.id, c.enforced FROM org_sso_connections c JOIN orgs o ON o.id = c.org_id
  WHERE lower(p_domain) = ANY (c.domains) AND o.deleted_at IS NULL
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION alpine_resolve_api_key(text) FROM PUBLIC;
REVOKE ALL ON FUNCTION alpine_resolve_invitation(bytea) FROM PUBLIC;
REVOKE ALL ON FUNCTION alpine_resolve_sso_domain(text) FROM PUBLIC;
REVOKE ALL ON FUNCTION alpine_memberships_keep_owner() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION alpine_resolve_api_key(text) TO alpine_app;
GRANT EXECUTE ON FUNCTION alpine_resolve_invitation(bytea) TO alpine_app;
GRANT EXECUTE ON FUNCTION alpine_resolve_sso_domain(text) TO alpine_app;

-- +goose Down
DROP FUNCTION IF EXISTS alpine_resolve_sso_domain(text);
DROP FUNCTION IF EXISTS alpine_resolve_invitation(bytea);
DROP FUNCTION IF EXISTS alpine_resolve_api_key(text);
DROP TABLE IF EXISTS auth_events;
DROP TABLE IF EXISTS org_sso_connections;
DROP TABLE IF EXISTS api_keys;
DROP TABLE IF EXISTS invitations;
DROP TABLE IF EXISTS role_permissions;
DROP TRIGGER IF EXISTS memberships_keep_owner ON memberships;
DROP FUNCTION IF EXISTS alpine_memberships_keep_owner();
DROP INDEX IF EXISTS memberships_org_role_idx;
ALTER TABLE memberships DROP COLUMN invited_by;
DROP TABLE IF EXISTS user_identities;
DROP INDEX IF EXISTS users_email_idx;
DROP INDEX IF EXISTS users_verified_email_uq;
ALTER TABLE users DROP CONSTRAINT users_email_normalized_chk;
ALTER TABLE users DROP COLUMN email_verified;
-- Users without a GitHub identity cannot satisfy the old NOT NULL columns; remove them first.
DELETE FROM users WHERE github_id IS NULL;
ALTER TABLE users ALTER COLUMN github_id SET NOT NULL, ALTER COLUMN github_login SET NOT NULL;
