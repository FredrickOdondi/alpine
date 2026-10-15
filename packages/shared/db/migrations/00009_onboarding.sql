-- +goose Up
-- Customer onboarding: the platform GitHub App registration (created once by an operator through
-- the manifest flow, secrets envelope-encrypted), installation lifecycle (linked by whom,
-- uninstalled), and per-repo enablement (only enabled repos turn issues into tickets).

-- 1. Platform GitHub App (single row). Secrets are envelope-encrypted with the platform KEK
--    (keys.Seal with org "platform"); plaintext never touches the DB.
CREATE TABLE github_app_registrations (
  id                        smallint PRIMARY KEY DEFAULT 1 CHECK (id = 1),
  app_id                    bigint NOT NULL CHECK (app_id > 0),
  slug                      text NOT NULL CHECK (length(slug) BETWEEN 1 AND 128),
  name                      text NOT NULL CHECK (length(name) BETWEEN 1 AND 128),
  html_url                  text NOT NULL CHECK (length(html_url) <= 512),
  owner_login               text NOT NULL CHECK (length(owner_login) <= 128),
  client_id                 text NOT NULL CHECK (length(client_id) <= 128),
  secrets_ciphertext        bytea NOT NULL, -- JSON {private_key, webhook_secret, client_secret}
  secrets_data_key          bytea NOT NULL,
  kek_id                    text NOT NULL,
  created_by                uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at                timestamptz NOT NULL DEFAULT now(),
  updated_at                timestamptz NOT NULL DEFAULT now()
);
CREATE TRIGGER github_app_registrations_touch BEFORE UPDATE ON github_app_registrations
  FOR EACH ROW EXECUTE FUNCTION alpine_touch_updated_at();

-- 2. Installation lifecycle.
ALTER TABLE scm_installations
  ADD COLUMN uninstalled_at timestamptz,
  ADD COLUMN repository_selection text CHECK (repository_selection IN ('all','selected')),
  ADD COLUMN linked_by uuid REFERENCES users(id) ON DELETE SET NULL;

-- 3. Repo enablement. Existing rows (seeded / created by intake before onboarding) stay enabled.
ALTER TABLE repos ADD COLUMN enabled boolean NOT NULL DEFAULT true;

-- 4. Webhook ingress resolves any installation that is still installed (suspended ones too, so
--    unsuspend events arrive); ticket creation separately requires an active installation.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION alpine_resolve_installation(p_provider text, p_installation_id bigint)
RETURNS TABLE (org_id uuid, installation_uuid uuid)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
  SELECT i.org_id, i.id FROM scm_installations i
  WHERE i.provider = p_provider AND i.installation_id = p_installation_id AND i.uninstalled_at IS NULL
$$;
-- +goose StatementEnd

-- 5. Linking check: which org (if any) already owns an installation id, including uninstalled
--    rows, so one org can never claim another org's installation. Ids only.
-- +goose StatementBegin
CREATE FUNCTION alpine_installation_owner(p_provider text, p_installation_id bigint)
RETURNS TABLE (org_id uuid, uninstalled boolean)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
  SELECT i.org_id, i.uninstalled_at IS NOT NULL FROM scm_installations i
  WHERE i.provider = p_provider AND i.installation_id = p_installation_id
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION alpine_installation_owner(text, bigint) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION alpine_installation_owner(text, bigint) TO alpine_app;

-- +goose Down
DROP FUNCTION IF EXISTS alpine_installation_owner(text, bigint);
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION alpine_resolve_installation(p_provider text, p_installation_id bigint)
RETURNS TABLE (org_id uuid, installation_uuid uuid)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
  SELECT i.org_id, i.id FROM scm_installations i
  WHERE i.provider = p_provider AND i.installation_id = p_installation_id AND i.suspended_at IS NULL
$$;
-- +goose StatementEnd
ALTER TABLE repos DROP COLUMN enabled;
ALTER TABLE scm_installations DROP COLUMN linked_by, DROP COLUMN repository_selection, DROP COLUMN uninstalled_at;
DROP TABLE IF EXISTS github_app_registrations;
