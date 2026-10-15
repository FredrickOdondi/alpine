-- +goose Up
-- Webhook inbox, tickets and their stage history, agent runs/turns, gate results, artifacts,
-- pull requests and the side-effect idempotency ledger.

-- Idempotent webhook inbox: unique on (source, delivery_id). Payload is stored as received
-- (TODO P1: encrypt with the org data key, architecture §8).
CREATE TABLE inbox_events (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id        uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  source        text NOT NULL CHECK (source IN ('github','sentry','slack','linear','jira','widget')),
  delivery_id   text NOT NULL,
  event_type    text NOT NULL,
  action        text,
  payload       jsonb NOT NULL,
  received_at   timestamptz NOT NULL DEFAULT now(),
  processed_at  timestamptz,
  attempts      int NOT NULL DEFAULT 0,
  last_error    text,
  outcome       text CHECK (outcome IN ('ticket_created','ticket_updated','signalled','ignored','failed')),
  UNIQUE (source, delivery_id)
);
CREATE INDEX inbox_events_pending_idx ON inbox_events(received_at) WHERE processed_at IS NULL;

CREATE TABLE tickets (
  id                 uuid PRIMARY KEY,
  org_id             uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  repo_id            uuid NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  key                text NOT NULL,
  title              text NOT NULL,
  body               text NOT NULL DEFAULT '',
  type               text NOT NULL DEFAULT 'unknown' CHECK (type IN ('unknown','bug','feature','question','noise')),
  stage              text NOT NULL DEFAULT 'intake' CHECK (stage IN (
                       'intake','triage','refinement','prioritize','needs_human','implement','gates',
                       'review','merge_policy','pr_open','done','escalated','closed','duplicate')),
  source             text NOT NULL DEFAULT 'github',
  source_url         text,
  priority           text CHECK (priority IN ('P0','P1','P2','P3')),
  size               text CHECK (size IN ('XS','S','M','L','XL')),
  br_predicted       smallint CHECK (br_predicted BETWEEN 1 AND 4),
  br_final           smallint CHECK (br_final BETWEEN 1 AND 4),
  confidence         numeric(4,3) CHECK (confidence BETWEEN 0 AND 1),
  embedding          vector(1536),
  workflow_id        text UNIQUE,
  duplicate_of       uuid REFERENCES tickets(id),
  turns_used         int NOT NULL DEFAULT 0,
  gate_iterations    int NOT NULL DEFAULT 0,
  cost_usd           numeric(12,4) NOT NULL DEFAULT 0,
  tokens_used        bigint NOT NULL DEFAULT 0,
  max_turns          int NOT NULL DEFAULT 100,
  max_gate_iterations int NOT NULL DEFAULT 15,
  max_cost_usd       numeric(12,4) NOT NULL DEFAULT 20,
  escalation_reason  text,
  created_at         timestamptz NOT NULL DEFAULT now(),
  updated_at         timestamptz NOT NULL DEFAULT now(),
  UNIQUE (repo_id, key)
);
CREATE INDEX tickets_org_stage_idx ON tickets(org_id, stage, updated_at DESC);
CREATE TRIGGER tickets_touch BEFORE UPDATE ON tickets FOR EACH ROW EXECUTE FUNCTION alpine_touch_updated_at();

CREATE TABLE ticket_sources (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id          uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  ticket_id       uuid NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
  inbox_event_id  uuid REFERENCES inbox_events(id) ON DELETE SET NULL,
  source          text NOT NULL,
  external_id     text NOT NULL,
  external_url    text,
  created_at      timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, source, external_id)
);
CREATE INDEX ticket_sources_ticket_idx ON ticket_sources(ticket_id);

CREATE TABLE ticket_stage_history (
  id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  org_id      uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  ticket_id   uuid NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
  from_stage  text,
  to_stage    text NOT NULL,
  reason      text,
  actor       text NOT NULL DEFAULT 'system',
  -- Temporal activity id that wrote the transition; makes retried activities idempotent.
  idempotency_key text,
  at          timestamptz NOT NULL DEFAULT now(),
  UNIQUE (ticket_id, idempotency_key)
);
CREATE INDEX ticket_stage_history_ticket_idx ON ticket_stage_history(ticket_id, at);

CREATE TABLE runs (
  id               uuid PRIMARY KEY,
  org_id           uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  ticket_id        uuid NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
  kind             text NOT NULL CHECK (kind IN ('triage','refine','implement','review')),
  status           text NOT NULL DEFAULT 'running' CHECK (status IN ('running','succeeded','failed','escalated','cancelled')),
  model            text,
  turns            int NOT NULL DEFAULT 0,
  gate_iterations  int NOT NULL DEFAULT 0,
  cost_usd         numeric(12,4) NOT NULL DEFAULT 0,
  started_at       timestamptz NOT NULL DEFAULT now(),
  ended_at         timestamptz
);
CREATE INDEX runs_ticket_idx ON runs(ticket_id, started_at);

CREATE TABLE agent_turns (
  id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  org_id         uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  run_id         uuid NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  n              int NOT NULL CHECK (n > 0),
  model          text NOT NULL,
  tokens_in      bigint NOT NULL DEFAULT 0,
  tokens_out     bigint NOT NULL DEFAULT 0,
  tokens_cached  bigint NOT NULL DEFAULT 0,
  cost_usd       numeric(12,6) NOT NULL DEFAULT 0,
  tool_calls     jsonb NOT NULL DEFAULT '[]'::jsonb, -- opaque, schema-versioned summary
  transcript_ref text,
  created_at     timestamptz NOT NULL DEFAULT now(),
  UNIQUE (run_id, n) -- resumable: a retried activity continues after the last persisted turn
);

CREATE TABLE gate_results (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id         uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  run_id         uuid NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  ticket_id      uuid NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
  iteration      int NOT NULL CHECK (iteration > 0),
  gate_id        text NOT NULL,
  status         text NOT NULL CHECK (status IN ('pass','fail','warn','error','skipped')),
  duration_ms    bigint NOT NULL DEFAULT 0,
  summary        text,
  findings       jsonb NOT NULL DEFAULT '[]'::jsonb, -- normalized gate.Finding list (opaque tool output)
  artifacts_ref  text,
  created_at     timestamptz NOT NULL DEFAULT now(),
  UNIQUE (run_id, iteration, gate_id)
);
CREATE INDEX gate_results_ticket_idx ON gate_results(ticket_id, iteration);

CREATE TABLE artifacts (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id           uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  ticket_id        uuid REFERENCES tickets(id) ON DELETE CASCADE,
  run_id           uuid REFERENCES runs(id) ON DELETE CASCADE,
  kind             text NOT NULL CHECK (kind IN ('video','screenshot','trace','log','visual_diff','transcript')),
  s3_key           text NOT NULL UNIQUE CHECK (s3_key LIKE 'org/%'),
  content_type     text,
  size_bytes       bigint,
  sha256           text,
  retention_until  timestamptz,
  created_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX artifacts_ticket_idx ON artifacts(ticket_id);

CREATE TABLE pull_requests (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id        uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  ticket_id     uuid NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
  repo_id       uuid NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  number        int NOT NULL,
  url           text NOT NULL,
  head_branch   text NOT NULL CHECK (head_branch LIKE 'alpine/%'),
  head_sha      text,
  state         text NOT NULL DEFAULT 'open' CHECK (state IN ('open','closed','merged')),
  blast_radius  smallint NOT NULL CHECK (blast_radius BETWEEN 1 AND 4),
  merge_mode    text NOT NULL DEFAULT 'human' CHECK (merge_mode IN ('human','auto')),
  merged_by     text,
  opened_at     timestamptz NOT NULL DEFAULT now(),
  merged_at     timestamptz,
  UNIQUE (repo_id, number)
);
CREATE INDEX pull_requests_inbox_idx ON pull_requests(org_id, state, blast_radius DESC, opened_at);

-- Ledger of external side effects (branch, PR, comment, label, check run). Every side effect is
-- keyed by `<workflow id>/<activity id or logical step>` so Temporal retries never duplicate.
CREATE TABLE side_effects (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id           uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  ticket_id        uuid REFERENCES tickets(id) ON DELETE CASCADE,
  idempotency_key  text NOT NULL UNIQUE,
  kind             text NOT NULL CHECK (kind IN ('branch','pull_request','comment','label','check_run')),
  status           text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','done','failed')),
  external_ref     text,
  created_at       timestamptz NOT NULL DEFAULT now(),
  completed_at     timestamptz
);

-- RLS -----------------------------------------------------------------------------------------
-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['inbox_events','tickets','ticket_sources','ticket_stage_history','runs',
                           'agent_turns','gate_results','artifacts','pull_requests','side_effects']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (org_id = alpine_current_org()) WITH CHECK (org_id = alpine_current_org())', t);
  END LOOP;
END
$$;
-- +goose StatementEnd

-- Cross-tenant work list for the inbox processor: ids + org only. The processor then handles each
-- event inside a transaction scoped to that org (RLS applies).
-- +goose StatementBegin
CREATE FUNCTION alpine_pending_inbox(p_limit int)
RETURNS TABLE (id uuid, org_id uuid)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public, pg_temp AS $$
  SELECT e.id, e.org_id FROM inbox_events e
  WHERE e.processed_at IS NULL AND e.attempts < 10
  ORDER BY e.received_at
  LIMIT LEAST(p_limit, 500)
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION alpine_pending_inbox(int) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION alpine_pending_inbox(int) TO alpine_app;

-- +goose Down
DROP FUNCTION IF EXISTS alpine_pending_inbox(int);
DROP TABLE IF EXISTS side_effects;
DROP TABLE IF EXISTS pull_requests;
DROP TABLE IF EXISTS artifacts;
DROP TABLE IF EXISTS gate_results;
DROP TABLE IF EXISTS agent_turns;
DROP TABLE IF EXISTS runs;
DROP TABLE IF EXISTS ticket_stage_history;
DROP TABLE IF EXISTS ticket_sources;
DROP TABLE IF EXISTS tickets;
DROP TABLE IF EXISTS inbox_events;
