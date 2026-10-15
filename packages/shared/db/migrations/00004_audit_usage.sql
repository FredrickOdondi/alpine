-- +goose Up
-- Append-only audit log, per-call LLM metering and generic usage meters (hybrid pricing:
-- metering only, no billing integration).

CREATE TABLE audit_log (
  id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  org_id       uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  actor_type   text NOT NULL CHECK (actor_type IN ('user','agent','system')),
  actor_id     text NOT NULL,
  action       text NOT NULL,
  target_type  text NOT NULL,
  target_id    text NOT NULL,
  metadata     jsonb NOT NULL DEFAULT '{}'::jsonb,
  ip           inet,
  at           timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_log_org_at_idx ON audit_log(org_id, at DESC);

-- +goose StatementBegin
CREATE FUNCTION alpine_audit_log_immutable() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'audit_log is append-only';
END
$$;
-- +goose StatementEnd
CREATE TRIGGER audit_log_no_update BEFORE UPDATE OR DELETE ON audit_log
  FOR EACH ROW EXECUTE FUNCTION alpine_audit_log_immutable();
REVOKE UPDATE, DELETE ON audit_log FROM alpine_app;

-- One row per model gateway call (after retries/fallbacks resolve). idempotency_key =
-- `<workflow id>/<activity id>/<call seq>` so a retried activity never double-meters.
CREATE TABLE llm_calls (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id           uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  repo_id          uuid REFERENCES repos(id) ON DELETE SET NULL,
  ticket_id        uuid REFERENCES tickets(id) ON DELETE SET NULL,
  run_id           uuid REFERENCES runs(id) ON DELETE SET NULL,
  idempotency_key  text NOT NULL,
  stage            text NOT NULL, -- agent role: triage, refine, prioritize, implement, review
  provider         text NOT NULL DEFAULT 'openai',
  model            text NOT NULL,
  requested_model  text NOT NULL, -- primary model of the chain; differs from model on fallback
  status           text NOT NULL CHECK (status IN ('ok','error','invalid_output')),
  attempts         int NOT NULL DEFAULT 1,
  tokens_in        bigint NOT NULL DEFAULT 0,
  tokens_out       bigint NOT NULL DEFAULT 0,
  tokens_cached    bigint NOT NULL DEFAULT 0,
  cost_usd         numeric(12,6) NOT NULL DEFAULT 0,
  latency_ms       bigint NOT NULL DEFAULT 0,
  byok             boolean NOT NULL DEFAULT false,
  error            text,
  created_at       timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, idempotency_key)
);
CREATE INDEX llm_calls_org_created_idx ON llm_calls(org_id, created_at);
CREATE INDEX llm_calls_ticket_idx ON llm_calls(ticket_id);

-- Generic usage meters used for budgets and (later) invoicing.
CREATE TABLE usage_records (
  id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  org_id         uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  repo_id        uuid REFERENCES repos(id) ON DELETE SET NULL,
  ticket_id      uuid REFERENCES tickets(id) ON DELETE SET NULL,
  llm_call_id    uuid REFERENCES llm_calls(id) ON DELETE SET NULL,
  kind           text NOT NULL CHECK (kind IN ('llm_tokens_in','llm_tokens_out','llm_tokens_cached',
                                               'sandbox_vcpu_seconds','preview_minutes','gate_runs')),
  quantity       numeric(18,4) NOT NULL CHECK (quantity >= 0),
  unit           text NOT NULL,
  unit_cost_usd  numeric(18,10) NOT NULL DEFAULT 0,
  cost_usd       numeric(12,6) NOT NULL DEFAULT 0,
  byok           boolean NOT NULL DEFAULT false,
  model          text,
  recorded_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX usage_records_org_recorded_idx ON usage_records(org_id, recorded_at);
CREATE INDEX usage_records_ticket_idx ON usage_records(ticket_id);

-- Daily rollup used by budget checks and the metrics page.
CREATE VIEW usage_daily WITH (security_invoker = true) AS
  SELECT org_id, date_trunc('day', recorded_at) AS day, kind,
         sum(quantity) AS quantity, sum(cost_usd) AS cost_usd
  FROM usage_records GROUP BY 1, 2, 3;
GRANT SELECT ON usage_daily TO alpine_app;

-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['audit_log','llm_calls','usage_records']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (org_id = alpine_current_org()) WITH CHECK (org_id = alpine_current_org())', t);
  END LOOP;
END
$$;
-- +goose StatementEnd

-- +goose Down
DROP VIEW IF EXISTS usage_daily;
DROP TABLE IF EXISTS usage_records;
DROP TABLE IF EXISTS llm_calls;
DROP TABLE IF EXISTS audit_log;
DROP FUNCTION IF EXISTS alpine_audit_log_immutable();
