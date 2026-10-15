-- Runs once on first container start. The `alpine` superuser (POSTGRES_USER) owns the schema and
-- runs migrations; services connect as `alpine_app`, which is subject to RLS (NOBYPASSRLS).
-- Migration 00001 also creates/grants alpine_app if missing; this makes the login exist early.
DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'alpine_app') THEN
    CREATE ROLE alpine_app LOGIN PASSWORD 'alpine_app' NOBYPASSRLS;
  END IF;
END $$;
CREATE EXTENSION IF NOT EXISTS vector;
