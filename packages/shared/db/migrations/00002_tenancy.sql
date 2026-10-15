-- +goose Up
-- Orgs, users, memberships, SCM installations, repos, factory configs, BYO model keys, limits.

CREATE TABLE orgs (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  slug         text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9][a-z0-9-]{1,62}$'),
  name         text NOT NULL,
  plan         text NOT NULL DEFAULT 'starter' CHECK (plan IN ('starter','team','business','enterprise')),
  data_region  text NOT NULL DEFAULT 'us-east-1',
  kms_key_arn  text,
  created_at   timestamptz NOT NULL DEFAULT now()
);

-- Users are global identities (one GitHub account can belong to many orgs); no org_id, no RLS.
-- Only non-sensitive profile data lives here; access is by id/github_id lookups.
CREATE TABLE users (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  github_id     bigint NOT NULL UNIQUE,
  github_login  text NOT NULL,
  name          text,
  email         text,
  avatar_url    text,
  created_at    timestamptz NOT NULL DEFAULT now(),
  last_login_at timestamptz
);

CREATE TABLE memberships (
  org_id     uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role       text NOT NULL CHECK (role IN ('owner','admin','factory_owner','member','viewer')),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (org_id, user_id)
);
CREATE INDEX memberships_user_idx ON memberships(user_id);

-- GitHub App installations ("scm_installations" in the spec).
CREATE TABLE scm_installations (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id           uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  provider         text NOT NULL DEFAULT 'github' CHECK (provider IN ('github','gitlab')),
  installation_id  bigint NOT NULL,
  account_login    text NOT NULL,
  account_type     text NOT NULL DEFAULT 'Organization' CHECK (account_type IN ('Organization','User')),
  permissions      jsonb NOT NULL DEFAULT '{}'::jsonb, -- opaque snapshot of granted permissions
  suspended_at     timestamptz,
  created_at       timestamptz NOT NULL DEFAULT now(),
  UNIQUE (provider, installation_id)
);
CREATE INDEX scm_installations_org_idx ON scm_installations(org_id);

CREATE TABLE repos (
  id                       uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id                   uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  installation_id          uuid NOT NULL REFERENCES scm_installations(id) ON DELETE CASCADE,
  provider_repo_id         bigint NOT NULL,
  full_name                text NOT NULL,
  default_branch           text NOT NULL DEFAULT 'main',
  config_sha               text,
  env_snapshot_ref         text,
  -- Fairness knobs (NULL = org default). See architecture "AI reliability & multi-tenancy".
  max_parallel_tickets     int CHECK (max_parallel_tickets > 0),
  llm_requests_per_minute  int CHECK (llm_requests_per_minute > 0),
  created_at               timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, full_name),
  UNIQUE (installation_id, provider_repo_id)
);

CREATE TABLE factory_configs (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id          uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  repo_id         uuid NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  version         int NOT NULL,
  source          text NOT NULL CHECK (source IN ('repo','ui')),
  commit_sha      text,
  schema_version  int NOT NULL DEFAULT 1,
  parsed          jsonb NOT NULL, -- validated, schema-versioned parse of .alpine/factory.yaml
  created_by      uuid REFERENCES users(id),
  created_at      timestamptz NOT NULL DEFAULT now(),
  UNIQUE (repo_id, version)
);

-- BYO model keys (customer's own OpenAI key). Envelope encryption: the key is encrypted with a
-- per-org data key; the data key is encrypted by the KMS key (or the local KEK in dev).
-- Plaintext never touches the DB or logs.
CREATE TABLE model_keys (
  id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id               uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  provider             text NOT NULL DEFAULT 'openai' CHECK (provider IN ('openai')),
  ciphertext           bytea NOT NULL,
  data_key_ciphertext  bytea NOT NULL,
  kek_id               text NOT NULL,
  last4                text NOT NULL CHECK (length(last4) = 4),
  created_by           uuid REFERENCES users(id),
  created_at           timestamptz NOT NULL DEFAULT now(),
  revoked_at           timestamptz
);
CREATE UNIQUE INDEX model_keys_active_uq ON model_keys(org_id, provider) WHERE revoked_at IS NULL;

-- Per-org limits and budgets (NULL = platform default from config).
CREATE TABLE org_limits (
  org_id                    uuid PRIMARY KEY REFERENCES orgs(id) ON DELETE CASCADE,
  max_parallel_tickets      int CHECK (max_parallel_tickets > 0),
  max_concurrent_llm_calls  int CHECK (max_concurrent_llm_calls > 0),
  llm_requests_per_minute   int CHECK (llm_requests_per_minute > 0),
  llm_tokens_per_minute     int CHECK (llm_tokens_per_minute > 0),
  daily_cost_usd_cap        numeric(12,4) CHECK (daily_cost_usd_cap >= 0),
  monthly_cost_usd_cap      numeric(12,4) CHECK (monthly_cost_usd_cap >= 0),
  daily_token_cap           bigint CHECK (daily_token_cap >= 0),
  monthly_token_cap         bigint CHECK (monthly_token_cap >= 0),
  updated_at                timestamptz NOT NULL DEFAULT now()
);

-- RLS -----------------------------------------------------------------------------------------
ALTER TABLE orgs ENABLE ROW LEVEL SECURITY;
CREATE POLICY orgs_visible ON orgs
  USING (id = alpine_current_org()
         OR id IN (SELECT m.org_id FROM memberships m WHERE m.user_id = alpine_current_user()))
  WITH CHECK (id = alpine_current_org());

ALTER TABLE memberships ENABLE ROW LEVEL SECURITY;
CREATE POLICY memberships_visible ON memberships
  USING (org_id = alpine_current_org() OR user_id = alpine_current_user())
  WITH CHECK (org_id = alpine_current_org());

ALTER TABLE scm_installations ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON scm_installations
  USING (org_id = alpine_current_org()) WITH CHECK (org_id = alpine_current_org());
ALTER TABLE repos ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON repos
  USING (org_id = alpine_current_org()) WITH CHECK (org_id = alpine_current_org());
ALTER TABLE factory_configs ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON factory_configs
  USING (org_id = alpine_current_org()) WITH CHECK (org_id = alpine_current_org());
ALTER TABLE model_keys ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON model_keys
  USING (org_id = alpine_current_org()) WITH CHECK (org_id = alpine_current_org());
ALTER TABLE org_limits ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON org_limits
  USING (org_id = alpine_current_org()) WITH CHECK (org_id = alpine_current_org());

-- Narrow cross-tenant lookup used by webhook ingress before a tenant is known: maps a GitHub App
-- installation id to its org. Returns only ids.
-- +goose StatementBegin
CREATE FUNCTION alpine_resolve_installation(p_provider text, p_installation_id bigint)
RETURNS TABLE (org_id uuid, installation_uuid uuid)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
  SELECT i.org_id, i.id FROM scm_installations i
  WHERE i.provider = p_provider AND i.installation_id = p_installation_id AND i.suspended_at IS NULL
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION alpine_resolve_installation(text, bigint) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION alpine_resolve_installation(text, bigint) TO alpine_app;

-- +goose Down
DROP POLICY IF EXISTS orgs_visible ON orgs;
DROP FUNCTION IF EXISTS alpine_resolve_installation(text, bigint);
DROP TABLE IF EXISTS org_limits;
DROP TABLE IF EXISTS model_keys;
DROP TABLE IF EXISTS factory_configs;
DROP TABLE IF EXISTS repos;
DROP TABLE IF EXISTS scm_installations;
DROP TABLE IF EXISTS memberships;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS orgs;
