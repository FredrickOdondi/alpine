-- +goose Up
-- Integration wiring (integration branch): Sentry tenancy/routing tables used by the Sentry
-- webhook receiver and inbox processor, issue-level dedupe + outcome detail on inbox_events,
-- persisted reviewer verdicts, the PR inbox's human decision + diff summary, and the raw
-- factory.yaml. Follows docs/database.md "Schema conventions": tenant tables carry org_id with RLS
-- tenant_isolation, same-tenant composite FKs (org_id, parent_id), updated_at +
-- alpine_touch_updated_at(), enumerations as text + CHECK, and narrow SECURITY DEFINER lookups
-- (ids only) for pre-tenant webhook ingress.

-- 1. Sentry installations: one row per Sentry integration install (Sentry's installation uuid).
CREATE TABLE sentry_installations (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id             uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  installation_uuid  text NOT NULL UNIQUE CHECK (length(installation_uuid) BETWEEN 1 AND 128),
  sentry_org_slug    text CHECK (sentry_org_slug IS NULL OR length(sentry_org_slug) <= 128),
  -- Reference into the secret manager for the integration client secret; never the secret itself.
  client_secret_ref  text CHECK (client_secret_ref IS NULL OR length(client_secret_ref) <= 512),
  suspended_at       timestamptz,
  created_at         timestamptz NOT NULL DEFAULT now(),
  updated_at         timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT sentry_installations_org_id_id_key UNIQUE (org_id, id)
);
CREATE INDEX sentry_installations_org_idx ON sentry_installations(org_id);
CREATE TRIGGER sentry_installations_touch BEFORE UPDATE ON sentry_installations
  FOR EACH ROW EXECUTE FUNCTION alpine_touch_updated_at();

-- 2. Sentry project -> repo routing (a project maps to exactly one repo per org).
CREATE TABLE sentry_project_repos (
  id                      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id                  uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  sentry_installation_id  uuid NOT NULL,
  sentry_project          text NOT NULL CHECK (length(sentry_project) BETWEEN 1 AND 128),
  repo_id                 uuid NOT NULL,
  created_at              timestamptz NOT NULL DEFAULT now(),
  updated_at              timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT sentry_project_repos_project_key UNIQUE (org_id, sentry_project),
  CONSTRAINT sentry_project_repos_installation_fk FOREIGN KEY (org_id, sentry_installation_id)
    REFERENCES sentry_installations(org_id, id) ON DELETE CASCADE,
  CONSTRAINT sentry_project_repos_repo_fk FOREIGN KEY (org_id, repo_id)
    REFERENCES repos(org_id, id) ON DELETE CASCADE
);
CREATE INDEX sentry_project_repos_installation_idx ON sentry_project_repos(org_id, sentry_installation_id);
CREATE INDEX sentry_project_repos_repo_idx ON sentry_project_repos(org_id, repo_id);
CREATE TRIGGER sentry_project_repos_touch BEFORE UPDATE ON sentry_project_repos
  FOR EACH ROW EXECUTE FUNCTION alpine_touch_updated_at();

-- 3. inbox_events: issue-level dedupe key (intake.Event.DedupeKey, e.g. a Sentry issue id) and a
--    free-text outcome detail ("ticket_created:<id>", "ignored:unmapped_sentry_project", ...).
--    Delivery-level dedupe stays UNIQUE (source, delivery_id) (GitHub delivery id, Sentry Request-ID).
ALTER TABLE inbox_events
  ADD COLUMN dedupe_key text CHECK (dedupe_key IS NULL OR length(dedupe_key) BETWEEN 1 AND 256),
  ADD COLUMN outcome_detail text CHECK (outcome_detail IS NULL OR length(outcome_detail) <= 1024);
CREATE INDEX inbox_events_dedupe_idx ON inbox_events(org_id, source, dedupe_key, received_at DESC)
  WHERE dedupe_key IS NOT NULL;

-- 4. Reviewer verdicts (independent reviewer model) with structured findings.
CREATE TABLE reviews (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id      uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  ticket_id   uuid NOT NULL,
  run_id      uuid,
  verdict     text NOT NULL CHECK (verdict IN ('approve','request_changes','escalate')),
  model       text NOT NULL,
  summary     text,
  findings    jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(findings) = 'array'),
  idempotency_key text NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT reviews_idem_key UNIQUE (org_id, idempotency_key),
  CONSTRAINT reviews_ticket_fk FOREIGN KEY (org_id, ticket_id) REFERENCES tickets(org_id, id) ON DELETE CASCADE,
  CONSTRAINT reviews_run_fk FOREIGN KEY (org_id, run_id) REFERENCES runs(org_id, id) ON DELETE SET NULL (run_id)
);
CREATE INDEX reviews_ticket_idx ON reviews(org_id, ticket_id, created_at DESC);
CREATE INDEX reviews_run_idx ON reviews(org_id, run_id) WHERE run_id IS NOT NULL;

-- 5. PR inbox: human decision (mirrored to a GitHub review) and diff summary.
ALTER TABLE pull_requests
  ADD COLUMN human_decision text CHECK (human_decision IN ('approved','changes_requested')),
  ADD COLUMN human_decision_by text,
  ADD COLUMN human_decision_at timestamptz,
  ADD COLUMN additions int CHECK (additions >= 0),
  ADD COLUMN deletions int CHECK (deletions >= 0),
  ADD COLUMN files_changed int CHECK (files_changed >= 0);

-- 6. Repo config page: keep the raw .alpine/factory.yaml next to its parse.
ALTER TABLE factory_configs
  ADD COLUMN raw_yaml text CHECK (raw_yaml IS NULL OR length(raw_yaml) <= 262144);

-- RLS for the new tenant tables.
-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['sentry_installations','sentry_project_repos','reviews']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (org_id = alpine_current_org()) WITH CHECK (org_id = alpine_current_org())', t);
  END LOOP;
END
$$;
-- +goose StatementEnd

-- Pre-tenant lookup for Sentry ingress: installation uuid -> org (ids only).
-- +goose StatementBegin
CREATE FUNCTION alpine_resolve_sentry_installation(p_installation_uuid text)
RETURNS TABLE (org_id uuid, installation_id uuid)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
  SELECT i.org_id, i.id FROM sentry_installations i JOIN orgs o ON o.id = i.org_id
  WHERE i.installation_uuid = p_installation_uuid AND i.suspended_at IS NULL AND o.deleted_at IS NULL
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION alpine_resolve_sentry_installation(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION alpine_resolve_sentry_installation(text) TO alpine_app;

-- +goose Down
DROP FUNCTION IF EXISTS alpine_resolve_sentry_installation(text);
ALTER TABLE factory_configs DROP COLUMN raw_yaml;
ALTER TABLE pull_requests
  DROP COLUMN files_changed, DROP COLUMN deletions, DROP COLUMN additions,
  DROP COLUMN human_decision_at, DROP COLUMN human_decision_by, DROP COLUMN human_decision;
DROP TABLE IF EXISTS reviews;
DROP INDEX IF EXISTS inbox_events_dedupe_idx;
ALTER TABLE inbox_events DROP COLUMN outcome_detail, DROP COLUMN dedupe_key;
DROP TABLE IF EXISTS sentry_project_repos;
DROP TABLE IF EXISTS sentry_installations;
