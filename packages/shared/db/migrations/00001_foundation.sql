-- +goose Up
-- Foundation: extensions, the application role and tenancy helper functions.
--
-- Roles:
--   * The migration/owner role (whoever runs goose) owns all tables. It is privileged and audited;
--     only migrations and the narrow SECURITY DEFINER functions below run as it.
--   * alpine_app: what services connect as (directly in dev, via IAM/login users granted the role
--     in prod). It is NOT a table owner and has NO BYPASSRLS, so RLS always applies to it.
CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- +goose StatementBegin
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'alpine_app') THEN
    CREATE ROLE alpine_app NOLOGIN NOBYPASSRLS;
  END IF;
END
$$;
-- +goose StatementEnd

GRANT USAGE ON SCHEMA public TO alpine_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO alpine_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO alpine_app;

-- Tenant context, set per transaction by packages/shared/db.WithTenant via set_config(..., true).
-- An unset/empty setting yields NULL, which matches no rows (fail closed).
-- +goose StatementBegin
CREATE FUNCTION alpine_current_org() RETURNS uuid
LANGUAGE sql STABLE AS $$
  SELECT NULLIF(current_setting('app.tenant_id', true), '')::uuid
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION alpine_current_user() RETURNS uuid
LANGUAGE sql STABLE AS $$
  SELECT NULLIF(current_setting('app.user_id', true), '')::uuid
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION alpine_touch_updated_at() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  NEW.updated_at = now();
  RETURN NEW;
END
$$;
-- +goose StatementEnd

-- +goose Down
DROP FUNCTION IF EXISTS alpine_touch_updated_at();
DROP FUNCTION IF EXISTS alpine_current_user();
DROP FUNCTION IF EXISTS alpine_current_org();
ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE USAGE, SELECT ON SEQUENCES FROM alpine_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE SELECT, INSERT, UPDATE, DELETE ON TABLES FROM alpine_app;
REVOKE USAGE ON SCHEMA public FROM alpine_app;
-- The alpine_app role and extensions are cluster/database-level and intentionally left in place.
