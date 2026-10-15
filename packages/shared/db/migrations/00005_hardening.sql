-- +goose Up
-- Hardening pass over 00001–00004 (see docs/database.md "Schema conventions"):
--   1. Tenant context: alpine_current_org() reads `app.org_id` (set per transaction with
--      set_config(..., true) == SET LOCAL). `app.tenant_id` stays as a fallback so older callers
--      keep working.
--   2. Same-tenant foreign keys: a child row can only reference a parent row of the SAME org.
--      Plain FKs are checked without RLS, so before this an org that learned another org's repo id
--      could attach rows to it. Every tenant FK is now (org_id, parent_id) -> parent(org_id, id).
--   3. Missing FK indexes, updated_at columns + triggers, soft delete on orgs/users/repos,
--      ON DELETE behaviour for user references, and extra CHECK constraints.
--
-- Enumerations stay `text + CHECK` (not CREATE TYPE ... AS ENUM): values can be added AND removed
-- in a single transactional migration, sqlc maps them to string, and Go owns the canonical list
-- (packages/shared/ticket, packages/shared/auth). docs/database.md explains the trade-off.

-- 1. Tenant context ---------------------------------------------------------------------------
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION alpine_current_org() RETURNS uuid
LANGUAGE sql STABLE AS $$
  SELECT COALESCE(NULLIF(current_setting('app.org_id', true), ''),
                  NULLIF(current_setting('app.tenant_id', true), ''))::uuid
$$;
-- +goose StatementEnd

-- 2. Same-tenant composite foreign keys -------------------------------------------------------
ALTER TABLE scm_installations ADD CONSTRAINT scm_installations_org_id_id_key UNIQUE (org_id, id);
ALTER TABLE repos             ADD CONSTRAINT repos_org_id_id_key             UNIQUE (org_id, id);
ALTER TABLE inbox_events      ADD CONSTRAINT inbox_events_org_id_id_key      UNIQUE (org_id, id);
ALTER TABLE tickets           ADD CONSTRAINT tickets_org_id_id_key           UNIQUE (org_id, id);
ALTER TABLE runs              ADD CONSTRAINT runs_org_id_id_key              UNIQUE (org_id, id);
ALTER TABLE llm_calls         ADD CONSTRAINT llm_calls_org_id_id_key         UNIQUE (org_id, id);

ALTER TABLE repos DROP CONSTRAINT repos_installation_id_fkey,
  ADD CONSTRAINT repos_installation_fk FOREIGN KEY (org_id, installation_id)
    REFERENCES scm_installations(org_id, id) ON DELETE CASCADE;
ALTER TABLE factory_configs DROP CONSTRAINT factory_configs_repo_id_fkey,
  ADD CONSTRAINT factory_configs_repo_fk FOREIGN KEY (org_id, repo_id)
    REFERENCES repos(org_id, id) ON DELETE CASCADE;
ALTER TABLE tickets DROP CONSTRAINT tickets_repo_id_fkey,
  ADD CONSTRAINT tickets_repo_fk FOREIGN KEY (org_id, repo_id)
    REFERENCES repos(org_id, id) ON DELETE CASCADE;
ALTER TABLE tickets DROP CONSTRAINT tickets_duplicate_of_fkey,
  ADD CONSTRAINT tickets_duplicate_of_fk FOREIGN KEY (org_id, duplicate_of)
    REFERENCES tickets(org_id, id) ON DELETE SET NULL (duplicate_of);
ALTER TABLE ticket_sources DROP CONSTRAINT ticket_sources_ticket_id_fkey,
  ADD CONSTRAINT ticket_sources_ticket_fk FOREIGN KEY (org_id, ticket_id)
    REFERENCES tickets(org_id, id) ON DELETE CASCADE;
ALTER TABLE ticket_sources DROP CONSTRAINT ticket_sources_inbox_event_id_fkey,
  ADD CONSTRAINT ticket_sources_inbox_event_fk FOREIGN KEY (org_id, inbox_event_id)
    REFERENCES inbox_events(org_id, id) ON DELETE SET NULL (inbox_event_id);
ALTER TABLE ticket_stage_history DROP CONSTRAINT ticket_stage_history_ticket_id_fkey,
  ADD CONSTRAINT ticket_stage_history_ticket_fk FOREIGN KEY (org_id, ticket_id)
    REFERENCES tickets(org_id, id) ON DELETE CASCADE;
ALTER TABLE runs DROP CONSTRAINT runs_ticket_id_fkey,
  ADD CONSTRAINT runs_ticket_fk FOREIGN KEY (org_id, ticket_id)
    REFERENCES tickets(org_id, id) ON DELETE CASCADE;
ALTER TABLE agent_turns DROP CONSTRAINT agent_turns_run_id_fkey,
  ADD CONSTRAINT agent_turns_run_fk FOREIGN KEY (org_id, run_id)
    REFERENCES runs(org_id, id) ON DELETE CASCADE;
ALTER TABLE gate_results DROP CONSTRAINT gate_results_run_id_fkey,
  ADD CONSTRAINT gate_results_run_fk FOREIGN KEY (org_id, run_id)
    REFERENCES runs(org_id, id) ON DELETE CASCADE;
ALTER TABLE gate_results DROP CONSTRAINT gate_results_ticket_id_fkey,
  ADD CONSTRAINT gate_results_ticket_fk FOREIGN KEY (org_id, ticket_id)
    REFERENCES tickets(org_id, id) ON DELETE CASCADE;
ALTER TABLE artifacts DROP CONSTRAINT artifacts_ticket_id_fkey,
  ADD CONSTRAINT artifacts_ticket_fk FOREIGN KEY (org_id, ticket_id)
    REFERENCES tickets(org_id, id) ON DELETE CASCADE;
ALTER TABLE artifacts DROP CONSTRAINT artifacts_run_id_fkey,
  ADD CONSTRAINT artifacts_run_fk FOREIGN KEY (org_id, run_id)
    REFERENCES runs(org_id, id) ON DELETE CASCADE;
ALTER TABLE pull_requests DROP CONSTRAINT pull_requests_ticket_id_fkey,
  ADD CONSTRAINT pull_requests_ticket_fk FOREIGN KEY (org_id, ticket_id)
    REFERENCES tickets(org_id, id) ON DELETE CASCADE;
ALTER TABLE pull_requests DROP CONSTRAINT pull_requests_repo_id_fkey,
  ADD CONSTRAINT pull_requests_repo_fk FOREIGN KEY (org_id, repo_id)
    REFERENCES repos(org_id, id) ON DELETE CASCADE;
ALTER TABLE side_effects DROP CONSTRAINT side_effects_ticket_id_fkey,
  ADD CONSTRAINT side_effects_ticket_fk FOREIGN KEY (org_id, ticket_id)
    REFERENCES tickets(org_id, id) ON DELETE CASCADE;
ALTER TABLE llm_calls DROP CONSTRAINT llm_calls_repo_id_fkey,
  ADD CONSTRAINT llm_calls_repo_fk FOREIGN KEY (org_id, repo_id)
    REFERENCES repos(org_id, id) ON DELETE SET NULL (repo_id);
ALTER TABLE llm_calls DROP CONSTRAINT llm_calls_ticket_id_fkey,
  ADD CONSTRAINT llm_calls_ticket_fk FOREIGN KEY (org_id, ticket_id)
    REFERENCES tickets(org_id, id) ON DELETE SET NULL (ticket_id);
ALTER TABLE llm_calls DROP CONSTRAINT llm_calls_run_id_fkey,
  ADD CONSTRAINT llm_calls_run_fk FOREIGN KEY (org_id, run_id)
    REFERENCES runs(org_id, id) ON DELETE SET NULL (run_id);

-- User references must not block deleting a user.
ALTER TABLE factory_configs DROP CONSTRAINT factory_configs_created_by_fkey,
  ADD CONSTRAINT factory_configs_created_by_fkey FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE model_keys DROP CONSTRAINT model_keys_created_by_fkey,
  ADD CONSTRAINT model_keys_created_by_fkey FOREIGN KEY (created_by) REFERENCES users(id) ON DELETE SET NULL;

-- 3a. Soft delete -----------------------------------------------------------------------------
-- Orgs and users are soft-deleted (deleted_at) so audit history and billing meters stay intact; a
-- purge job hard-deletes after the retention window. Repos are soft-deleted when disconnected so
-- tickets/metrics survive; reconnecting the same repo creates a new live row.
ALTER TABLE orgs  ADD COLUMN deleted_at timestamptz;
ALTER TABLE users ADD COLUMN deleted_at timestamptz;
ALTER TABLE repos ADD COLUMN deleted_at timestamptz;
ALTER TABLE repos DROP CONSTRAINT repos_org_id_full_name_key;
ALTER TABLE repos DROP CONSTRAINT repos_installation_id_provider_repo_id_key;
CREATE UNIQUE INDEX repos_org_full_name_live_uq ON repos(org_id, full_name) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX repos_installation_repo_live_uq ON repos(installation_id, provider_repo_id) WHERE deleted_at IS NULL;

-- 3b. updated_at ------------------------------------------------------------------------------
-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['orgs','users','memberships','scm_installations','repos','model_keys',
                           'inbox_events','runs','pull_requests','side_effects']
  LOOP
    EXECUTE format('ALTER TABLE %I ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now()', t);
    EXECUTE format('CREATE TRIGGER %I BEFORE UPDATE ON %I FOR EACH ROW EXECUTE FUNCTION alpine_touch_updated_at()', t || '_touch', t);
  END LOOP;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER org_limits_touch BEFORE UPDATE ON org_limits FOR EACH ROW EXECUTE FUNCTION alpine_touch_updated_at();

-- 3c. Indexes for FK columns and hot tenant listings ------------------------------------------
CREATE INDEX repos_installation_idx        ON repos(org_id, installation_id);
CREATE INDEX factory_configs_created_by_idx ON factory_configs(created_by) WHERE created_by IS NOT NULL;
CREATE INDEX model_keys_created_by_idx     ON model_keys(created_by) WHERE created_by IS NOT NULL;
CREATE INDEX inbox_events_org_received_idx ON inbox_events(org_id, received_at DESC);
CREATE INDEX tickets_duplicate_of_idx      ON tickets(duplicate_of) WHERE duplicate_of IS NOT NULL;
CREATE INDEX tickets_repo_stage_idx        ON tickets(org_id, repo_id, stage, updated_at DESC);
CREATE INDEX ticket_sources_inbox_idx      ON ticket_sources(inbox_event_id) WHERE inbox_event_id IS NOT NULL;
CREATE INDEX runs_org_started_idx          ON runs(org_id, started_at DESC);
CREATE INDEX artifacts_run_idx             ON artifacts(run_id) WHERE run_id IS NOT NULL;
CREATE INDEX artifacts_retention_idx       ON artifacts(retention_until) WHERE retention_until IS NOT NULL;
CREATE INDEX pull_requests_ticket_idx      ON pull_requests(ticket_id);
CREATE INDEX side_effects_ticket_idx       ON side_effects(ticket_id) WHERE ticket_id IS NOT NULL;
CREATE INDEX side_effects_pending_idx      ON side_effects(created_at) WHERE status = 'pending';
CREATE INDEX llm_calls_run_idx             ON llm_calls(run_id) WHERE run_id IS NOT NULL;
CREATE INDEX llm_calls_repo_idx            ON llm_calls(repo_id, created_at) WHERE repo_id IS NOT NULL;
CREATE INDEX orgs_live_idx                 ON orgs(id) WHERE deleted_at IS NULL;

-- 3d. Constraints -----------------------------------------------------------------------------
ALTER TABLE orgs ADD CONSTRAINT orgs_name_len_chk CHECK (length(btrim(name)) BETWEEN 1 AND 200);
ALTER TABLE tickets
  ADD CONSTRAINT tickets_source_chk CHECK (source IN ('github','sentry','slack','linear','jira','widget')),
  ADD CONSTRAINT tickets_counters_chk CHECK (turns_used >= 0 AND gate_iterations >= 0 AND cost_usd >= 0
                                             AND tokens_used >= 0 AND max_turns > 0
                                             AND max_gate_iterations > 0 AND max_cost_usd >= 0),
  ADD CONSTRAINT tickets_not_self_duplicate_chk CHECK (duplicate_of IS DISTINCT FROM id);
ALTER TABLE ticket_sources
  ADD CONSTRAINT ticket_sources_source_chk CHECK (source IN ('github','sentry','slack','linear','jira','widget'));
ALTER TABLE runs
  ADD CONSTRAINT runs_counters_chk CHECK (turns >= 0 AND gate_iterations >= 0 AND cost_usd >= 0),
  ADD CONSTRAINT runs_ended_after_start_chk CHECK (ended_at IS NULL OR ended_at >= started_at);
ALTER TABLE agent_turns
  ADD CONSTRAINT agent_turns_counters_chk CHECK (tokens_in >= 0 AND tokens_out >= 0 AND tokens_cached >= 0 AND cost_usd >= 0);
ALTER TABLE pull_requests
  ADD CONSTRAINT pull_requests_merged_chk CHECK ((state = 'merged') = (merged_at IS NOT NULL)),
  ADD CONSTRAINT pull_requests_number_chk CHECK (number > 0);
ALTER TABLE llm_calls
  ADD CONSTRAINT llm_calls_counters_chk CHECK (tokens_in >= 0 AND tokens_out >= 0 AND tokens_cached >= 0
                                               AND cost_usd >= 0 AND latency_ms >= 0 AND attempts >= 1);
ALTER TABLE inbox_events ADD CONSTRAINT inbox_events_attempts_chk CHECK (attempts >= 0);

-- +goose Down
ALTER TABLE inbox_events DROP CONSTRAINT inbox_events_attempts_chk;
ALTER TABLE llm_calls DROP CONSTRAINT llm_calls_counters_chk;
ALTER TABLE pull_requests DROP CONSTRAINT pull_requests_number_chk, DROP CONSTRAINT pull_requests_merged_chk;
ALTER TABLE agent_turns DROP CONSTRAINT agent_turns_counters_chk;
ALTER TABLE runs DROP CONSTRAINT runs_ended_after_start_chk, DROP CONSTRAINT runs_counters_chk;
ALTER TABLE ticket_sources DROP CONSTRAINT ticket_sources_source_chk;
ALTER TABLE tickets DROP CONSTRAINT tickets_not_self_duplicate_chk, DROP CONSTRAINT tickets_counters_chk,
  DROP CONSTRAINT tickets_source_chk;
ALTER TABLE orgs DROP CONSTRAINT orgs_name_len_chk;

DROP INDEX IF EXISTS orgs_live_idx;
DROP INDEX IF EXISTS llm_calls_repo_idx;
DROP INDEX IF EXISTS llm_calls_run_idx;
DROP INDEX IF EXISTS side_effects_pending_idx;
DROP INDEX IF EXISTS side_effects_ticket_idx;
DROP INDEX IF EXISTS pull_requests_ticket_idx;
DROP INDEX IF EXISTS artifacts_retention_idx;
DROP INDEX IF EXISTS artifacts_run_idx;
DROP INDEX IF EXISTS runs_org_started_idx;
DROP INDEX IF EXISTS ticket_sources_inbox_idx;
DROP INDEX IF EXISTS tickets_repo_stage_idx;
DROP INDEX IF EXISTS tickets_duplicate_of_idx;
DROP INDEX IF EXISTS inbox_events_org_received_idx;
DROP INDEX IF EXISTS model_keys_created_by_idx;
DROP INDEX IF EXISTS factory_configs_created_by_idx;
DROP INDEX IF EXISTS repos_installation_idx;

DROP TRIGGER IF EXISTS org_limits_touch ON org_limits;
-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['orgs','users','memberships','scm_installations','repos','model_keys',
                           'inbox_events','runs','pull_requests','side_effects']
  LOOP
    EXECUTE format('DROP TRIGGER IF EXISTS %I ON %I', t || '_touch', t);
    EXECUTE format('ALTER TABLE %I DROP COLUMN updated_at', t);
  END LOOP;
END
$$;
-- +goose StatementEnd

DROP INDEX IF EXISTS repos_installation_repo_live_uq;
DROP INDEX IF EXISTS repos_org_full_name_live_uq;
ALTER TABLE repos ADD CONSTRAINT repos_installation_id_provider_repo_id_key UNIQUE (installation_id, provider_repo_id);
ALTER TABLE repos ADD CONSTRAINT repos_org_id_full_name_key UNIQUE (org_id, full_name);
ALTER TABLE repos DROP COLUMN deleted_at;
ALTER TABLE users DROP COLUMN deleted_at;
ALTER TABLE orgs  DROP COLUMN deleted_at;

ALTER TABLE model_keys DROP CONSTRAINT model_keys_created_by_fkey,
  ADD CONSTRAINT model_keys_created_by_fkey FOREIGN KEY (created_by) REFERENCES users(id);
ALTER TABLE factory_configs DROP CONSTRAINT factory_configs_created_by_fkey,
  ADD CONSTRAINT factory_configs_created_by_fkey FOREIGN KEY (created_by) REFERENCES users(id);

ALTER TABLE llm_calls DROP CONSTRAINT llm_calls_run_fk,
  ADD CONSTRAINT llm_calls_run_id_fkey FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE SET NULL;
ALTER TABLE llm_calls DROP CONSTRAINT llm_calls_ticket_fk,
  ADD CONSTRAINT llm_calls_ticket_id_fkey FOREIGN KEY (ticket_id) REFERENCES tickets(id) ON DELETE SET NULL;
ALTER TABLE llm_calls DROP CONSTRAINT llm_calls_repo_fk,
  ADD CONSTRAINT llm_calls_repo_id_fkey FOREIGN KEY (repo_id) REFERENCES repos(id) ON DELETE SET NULL;
ALTER TABLE side_effects DROP CONSTRAINT side_effects_ticket_fk,
  ADD CONSTRAINT side_effects_ticket_id_fkey FOREIGN KEY (ticket_id) REFERENCES tickets(id) ON DELETE CASCADE;
ALTER TABLE pull_requests DROP CONSTRAINT pull_requests_repo_fk,
  ADD CONSTRAINT pull_requests_repo_id_fkey FOREIGN KEY (repo_id) REFERENCES repos(id) ON DELETE CASCADE;
ALTER TABLE pull_requests DROP CONSTRAINT pull_requests_ticket_fk,
  ADD CONSTRAINT pull_requests_ticket_id_fkey FOREIGN KEY (ticket_id) REFERENCES tickets(id) ON DELETE CASCADE;
ALTER TABLE artifacts DROP CONSTRAINT artifacts_run_fk,
  ADD CONSTRAINT artifacts_run_id_fkey FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE;
ALTER TABLE artifacts DROP CONSTRAINT artifacts_ticket_fk,
  ADD CONSTRAINT artifacts_ticket_id_fkey FOREIGN KEY (ticket_id) REFERENCES tickets(id) ON DELETE CASCADE;
ALTER TABLE gate_results DROP CONSTRAINT gate_results_ticket_fk,
  ADD CONSTRAINT gate_results_ticket_id_fkey FOREIGN KEY (ticket_id) REFERENCES tickets(id) ON DELETE CASCADE;
ALTER TABLE gate_results DROP CONSTRAINT gate_results_run_fk,
  ADD CONSTRAINT gate_results_run_id_fkey FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE;
ALTER TABLE agent_turns DROP CONSTRAINT agent_turns_run_fk,
  ADD CONSTRAINT agent_turns_run_id_fkey FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE;
ALTER TABLE runs DROP CONSTRAINT runs_ticket_fk,
  ADD CONSTRAINT runs_ticket_id_fkey FOREIGN KEY (ticket_id) REFERENCES tickets(id) ON DELETE CASCADE;
ALTER TABLE ticket_stage_history DROP CONSTRAINT ticket_stage_history_ticket_fk,
  ADD CONSTRAINT ticket_stage_history_ticket_id_fkey FOREIGN KEY (ticket_id) REFERENCES tickets(id) ON DELETE CASCADE;
ALTER TABLE ticket_sources DROP CONSTRAINT ticket_sources_inbox_event_fk,
  ADD CONSTRAINT ticket_sources_inbox_event_id_fkey FOREIGN KEY (inbox_event_id) REFERENCES inbox_events(id) ON DELETE SET NULL;
ALTER TABLE ticket_sources DROP CONSTRAINT ticket_sources_ticket_fk,
  ADD CONSTRAINT ticket_sources_ticket_id_fkey FOREIGN KEY (ticket_id) REFERENCES tickets(id) ON DELETE CASCADE;
ALTER TABLE tickets DROP CONSTRAINT tickets_duplicate_of_fk,
  ADD CONSTRAINT tickets_duplicate_of_fkey FOREIGN KEY (duplicate_of) REFERENCES tickets(id);
ALTER TABLE tickets DROP CONSTRAINT tickets_repo_fk,
  ADD CONSTRAINT tickets_repo_id_fkey FOREIGN KEY (repo_id) REFERENCES repos(id) ON DELETE CASCADE;
ALTER TABLE factory_configs DROP CONSTRAINT factory_configs_repo_fk,
  ADD CONSTRAINT factory_configs_repo_id_fkey FOREIGN KEY (repo_id) REFERENCES repos(id) ON DELETE CASCADE;
ALTER TABLE repos DROP CONSTRAINT repos_installation_fk,
  ADD CONSTRAINT repos_installation_id_fkey FOREIGN KEY (installation_id) REFERENCES scm_installations(id) ON DELETE CASCADE;

ALTER TABLE llm_calls         DROP CONSTRAINT llm_calls_org_id_id_key;
ALTER TABLE runs              DROP CONSTRAINT runs_org_id_id_key;
ALTER TABLE tickets           DROP CONSTRAINT tickets_org_id_id_key;
ALTER TABLE inbox_events      DROP CONSTRAINT inbox_events_org_id_id_key;
ALTER TABLE repos             DROP CONSTRAINT repos_org_id_id_key;
ALTER TABLE scm_installations DROP CONSTRAINT scm_installations_org_id_id_key;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION alpine_current_org() RETURNS uuid
LANGUAGE sql STABLE AS $$
  SELECT NULLIF(current_setting('app.tenant_id', true), '')::uuid
$$;
-- +goose StatementEnd
