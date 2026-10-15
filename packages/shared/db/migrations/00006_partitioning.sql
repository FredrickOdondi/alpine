-- +goose Up
-- Monthly range partitioning for the append-heavy tables audit_log and usage_records (and
-- auth_events, created in 00007). Retention becomes `DROP/DETACH PARTITION` instead of a huge
-- DELETE, and recent-month queries prune to one or two partitions.
--
-- Rules for every partitioned table (enforced by alpine_ensure_monthly_partitions):
--   * The app role only has privileges on the PARENT. Partitions are revoked from alpine_app, so
--     the parent's RLS policy can never be bypassed by querying a partition directly.
--   * A DEFAULT partition catches rows outside the pre-created range so writes never fail if the
--     maintenance job lapses; alert if it is non-empty (docs/database.md).
--   * Partition bounds are UTC month starts.
-- agent_turns and llm_calls are NOT partitioned yet: their idempotency keys (run_id, n) and
-- (org_id, idempotency_key) would have to include the partition key. See docs/database.md.

-- +goose StatementBegin
CREATE FUNCTION alpine_ensure_monthly_partitions(p_parent regclass, p_months_back int, p_months_ahead int)
RETURNS int LANGUAGE plpgsql AS $$
DECLARE
  base   timestamp := date_trunc('month', now() AT TIME ZONE 'UTC');
  m      timestamp;
  pname  text;
  n      int := 0;
BEGIN
  FOR i IN -p_months_back..p_months_ahead LOOP
    m := base + make_interval(months => i);
    pname := format('%s_p%s', p_parent::text, to_char(m, 'YYYYMM'));
    IF to_regclass(pname) IS NULL THEN
      EXECUTE format('CREATE TABLE %I PARTITION OF %s FOR VALUES FROM (%L) TO (%L)',
                     pname, p_parent, m::text || '+00', (m + interval '1 month')::text || '+00');
      EXECUTE format('REVOKE ALL ON %I FROM alpine_app', pname);
      n := n + 1;
    END IF;
  END LOOP;
  pname := p_parent::text || '_default';
  IF to_regclass(pname) IS NULL THEN
    EXECUTE format('CREATE TABLE %I PARTITION OF %s DEFAULT', pname, p_parent);
    EXECUTE format('REVOKE ALL ON %I FROM alpine_app', pname);
    n := n + 1;
  END IF;
  RETURN n;
END
$$;
-- +goose StatementEnd

-- Detaches (and optionally drops) monthly partitions whose whole range is older than
-- p_keep_months full months. Returns the affected partition names. Detached partitions can be
-- exported to S3 (pg_dump -t) before being dropped.
-- +goose StatementBegin
CREATE FUNCTION alpine_retire_partitions(p_parent regclass, p_keep_months int, p_drop boolean)
RETURNS SETOF text LANGUAGE plpgsql AS $$
DECLARE
  cutoff timestamp := date_trunc('month', now() AT TIME ZONE 'UTC') - make_interval(months => p_keep_months);
  r record;
BEGIN
  IF p_keep_months < 1 THEN
    RAISE EXCEPTION 'p_keep_months must be >= 1';
  END IF;
  FOR r IN
    SELECT c.relname
    FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
    WHERE i.inhparent = p_parent AND c.relname ~ '_p[0-9]{6}$'
      AND to_timestamp(right(c.relname, 6), 'YYYYMM') < cutoff
    ORDER BY c.relname
  LOOP
    EXECUTE format('ALTER TABLE %s DETACH PARTITION %I', p_parent, r.relname);
    IF p_drop THEN
      EXECUTE format('DROP TABLE %I', r.relname);
    END IF;
    RETURN NEXT r.relname;
  END LOOP;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION alpine_ensure_monthly_partitions(regclass, int, int) FROM PUBLIC;
REVOKE ALL ON FUNCTION alpine_retire_partitions(regclass, int, boolean) FROM PUBLIC;

-- audit_log -> partitioned ---------------------------------------------------------------------
-- The org FK is intentionally dropped: audit history must outlive a (soft- or hard-) deleted org
-- and is removed only by partition retention. Rows stay append-only.
ALTER TABLE audit_log RENAME TO audit_log_old;
ALTER INDEX audit_log_org_at_idx RENAME TO audit_log_old_org_at_idx;
ALTER TABLE audit_log_old RENAME CONSTRAINT audit_log_pkey TO audit_log_old_pkey;

CREATE SEQUENCE audit_log_seq;
CREATE TABLE audit_log (
  id           bigint NOT NULL DEFAULT nextval('audit_log_seq'),
  org_id       uuid NOT NULL,
  actor_type   text NOT NULL CHECK (actor_type IN ('user','agent','system','api_key')),
  actor_id     text NOT NULL,
  action       text NOT NULL CHECK (action ~ '^[a-z_]+(\.[a-z_]+)+$'),
  target_type  text NOT NULL,
  target_id    text NOT NULL,
  metadata     jsonb NOT NULL DEFAULT '{}'::jsonb,
  ip           inet,
  request_id   text,
  at           timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (id, at)
) PARTITION BY RANGE (at);
ALTER SEQUENCE audit_log_seq OWNED BY audit_log.id;
CREATE INDEX audit_log_org_at_idx ON audit_log (org_id, at DESC);
CREATE INDEX audit_log_org_action_idx ON audit_log (org_id, action, at DESC);
SELECT alpine_ensure_monthly_partitions('audit_log', 1, 3);

INSERT INTO audit_log (id, org_id, actor_type, actor_id, action, target_type, target_id, metadata, ip, at)
  SELECT id, org_id, actor_type, actor_id,
         CASE WHEN action ~ '^[a-z_]+(\.[a-z_]+)+$' THEN action ELSE 'legacy.' || regexp_replace(lower(action), '[^a-z_]', '_', 'g') END,
         target_type, target_id, metadata, ip, at
  FROM audit_log_old;
SELECT setval('audit_log_seq', COALESCE((SELECT max(id) FROM audit_log_old), 0) + 1, false);
DROP TABLE audit_log_old;

CREATE TRIGGER audit_log_no_update BEFORE UPDATE OR DELETE ON audit_log
  FOR EACH ROW EXECUTE FUNCTION alpine_audit_log_immutable();
REVOKE UPDATE, DELETE, TRUNCATE ON audit_log FROM alpine_app;
ALTER TABLE audit_log ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON audit_log
  USING (org_id = alpine_current_org()) WITH CHECK (org_id = alpine_current_org());

-- usage_records -> partitioned -----------------------------------------------------------------
DROP VIEW usage_daily;
ALTER TABLE usage_records RENAME TO usage_records_old;
ALTER INDEX usage_records_org_recorded_idx RENAME TO usage_records_old_org_recorded_idx;
ALTER INDEX usage_records_ticket_idx RENAME TO usage_records_old_ticket_idx;
ALTER TABLE usage_records_old RENAME CONSTRAINT usage_records_pkey TO usage_records_old_pkey;

CREATE SEQUENCE usage_records_seq;
CREATE TABLE usage_records (
  id             bigint NOT NULL DEFAULT nextval('usage_records_seq'),
  org_id         uuid NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  repo_id        uuid,
  ticket_id      uuid,
  llm_call_id    uuid,
  kind           text NOT NULL CHECK (kind IN ('llm_tokens_in','llm_tokens_out','llm_tokens_cached',
                                               'sandbox_vcpu_seconds','preview_minutes','gate_runs')),
  quantity       numeric(18,4) NOT NULL CHECK (quantity >= 0),
  unit           text NOT NULL,
  unit_cost_usd  numeric(18,10) NOT NULL DEFAULT 0 CHECK (unit_cost_usd >= 0),
  cost_usd       numeric(12,6) NOT NULL DEFAULT 0 CHECK (cost_usd >= 0),
  byok           boolean NOT NULL DEFAULT false,
  model          text,
  recorded_at    timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (id, recorded_at),
  FOREIGN KEY (org_id, repo_id) REFERENCES repos(org_id, id) ON DELETE SET NULL (repo_id),
  FOREIGN KEY (org_id, ticket_id) REFERENCES tickets(org_id, id) ON DELETE SET NULL (ticket_id),
  FOREIGN KEY (org_id, llm_call_id) REFERENCES llm_calls(org_id, id) ON DELETE SET NULL (llm_call_id)
) PARTITION BY RANGE (recorded_at);
ALTER SEQUENCE usage_records_seq OWNED BY usage_records.id;
CREATE INDEX usage_records_org_recorded_idx ON usage_records (org_id, recorded_at);
CREATE INDEX usage_records_ticket_idx ON usage_records (ticket_id) WHERE ticket_id IS NOT NULL;
CREATE INDEX usage_records_llm_call_idx ON usage_records (llm_call_id) WHERE llm_call_id IS NOT NULL;
CREATE INDEX usage_records_repo_idx ON usage_records (repo_id) WHERE repo_id IS NOT NULL;
SELECT alpine_ensure_monthly_partitions('usage_records', 1, 3);

INSERT INTO usage_records (id, org_id, repo_id, ticket_id, llm_call_id, kind, quantity, unit, unit_cost_usd,
                           cost_usd, byok, model, recorded_at)
  SELECT id, org_id, repo_id, ticket_id, llm_call_id, kind, quantity, unit, unit_cost_usd,
         cost_usd, byok, model, recorded_at
  FROM usage_records_old;
SELECT setval('usage_records_seq', COALESCE((SELECT max(id) FROM usage_records_old), 0) + 1, false);
DROP TABLE usage_records_old;

ALTER TABLE usage_records ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON usage_records
  USING (org_id = alpine_current_org()) WITH CHECK (org_id = alpine_current_org());

CREATE VIEW usage_daily WITH (security_invoker = true) AS
  SELECT org_id, date_trunc('day', recorded_at) AS day, kind,
         sum(quantity) AS quantity, sum(cost_usd) AS cost_usd
  FROM usage_records GROUP BY 1, 2, 3;
GRANT SELECT ON usage_daily TO alpine_app;

-- +goose Down
-- Converts both tables back to plain (unpartitioned) tables, preserving rows.
DROP VIEW usage_daily;
ALTER TABLE usage_records RENAME TO usage_records_part;
ALTER INDEX usage_records_org_recorded_idx RENAME TO usage_records_part_org_recorded_idx;
ALTER INDEX usage_records_ticket_idx RENAME TO usage_records_part_ticket_idx;
ALTER TABLE usage_records_part RENAME CONSTRAINT usage_records_pkey TO usage_records_part_pkey;
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
INSERT INTO usage_records (id, org_id, repo_id, ticket_id, llm_call_id, kind, quantity, unit, unit_cost_usd,
                           cost_usd, byok, model, recorded_at)
  OVERRIDING SYSTEM VALUE
  SELECT id, org_id, repo_id, ticket_id, llm_call_id, kind, quantity, unit, unit_cost_usd,
         cost_usd, byok, model, recorded_at FROM usage_records_part;
SELECT setval(pg_get_serial_sequence('usage_records', 'id'), COALESCE((SELECT max(id) FROM usage_records), 0) + 1, false);
DROP TABLE usage_records_part;
CREATE INDEX usage_records_org_recorded_idx ON usage_records(org_id, recorded_at);
CREATE INDEX usage_records_ticket_idx ON usage_records(ticket_id);
ALTER TABLE usage_records ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON usage_records
  USING (org_id = alpine_current_org()) WITH CHECK (org_id = alpine_current_org());
CREATE VIEW usage_daily WITH (security_invoker = true) AS
  SELECT org_id, date_trunc('day', recorded_at) AS day, kind,
         sum(quantity) AS quantity, sum(cost_usd) AS cost_usd
  FROM usage_records GROUP BY 1, 2, 3;
GRANT SELECT ON usage_daily TO alpine_app;

ALTER TABLE audit_log RENAME TO audit_log_part;
ALTER INDEX audit_log_org_at_idx RENAME TO audit_log_part_org_at_idx;
ALTER TABLE audit_log_part RENAME CONSTRAINT audit_log_pkey TO audit_log_part_pkey;
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
INSERT INTO audit_log (id, org_id, actor_type, actor_id, action, target_type, target_id, metadata, ip, at)
  OVERRIDING SYSTEM VALUE
  SELECT p.id, p.org_id, CASE WHEN p.actor_type = 'api_key' THEN 'system' ELSE p.actor_type END,
         p.actor_id, p.action, p.target_type, p.target_id, p.metadata, p.ip, p.at
  FROM audit_log_part p WHERE EXISTS (SELECT 1 FROM orgs o WHERE o.id = p.org_id);
SELECT setval(pg_get_serial_sequence('audit_log', 'id'), COALESCE((SELECT max(id) FROM audit_log), 0) + 1, false);
DROP TABLE audit_log_part;
CREATE INDEX audit_log_org_at_idx ON audit_log(org_id, at DESC);
CREATE TRIGGER audit_log_no_update BEFORE UPDATE OR DELETE ON audit_log
  FOR EACH ROW EXECUTE FUNCTION alpine_audit_log_immutable();
REVOKE UPDATE, DELETE ON audit_log FROM alpine_app;
ALTER TABLE audit_log ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON audit_log
  USING (org_id = alpine_current_org()) WITH CHECK (org_id = alpine_current_org());

DROP FUNCTION IF EXISTS alpine_retire_partitions(regclass, int, boolean);
DROP FUNCTION IF EXISTS alpine_ensure_monthly_partitions(regclass, int, int);
