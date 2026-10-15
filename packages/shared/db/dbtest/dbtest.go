// Package dbtest provides throwaway, fully migrated Postgres databases for integration tests —
// a dependency-free stand-in for testcontainers that works against any reachable Postgres.
//
// Configuration (tests SKIP when neither is set, so `go test ./...` stays green without Postgres):
//
//	ALPINE_TEST_ADMIN_URL     owner/superuser URL on a server with pgvector, e.g.
//	                          postgres://alpine:alpine@localhost:5432/postgres?sslmode=disable
//	                          A fresh database is created per call, migrated, and dropped on cleanup.
//	ALPINE_TEST_APP_PASSWORD  password of the alpine_app login role (default "alpine_app"); the
//	                          returned DB connects AS alpine_app so RLS applies exactly as in prod.
//	ALPINE_TEST_DATABASE_URL  fallback: an already-migrated database, connected as alpine_app.
//
// scripts/test-db.sh sets these up locally (or against `docker compose up postgres`).
package dbtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/alpine-dev/alpine/packages/shared/db"
)

// Env holds the URLs of a test database.
type Env struct {
	AdminURL string // owner role (migrations, maintenance functions)
	AppURL   string // alpine_app role (RLS applies)
	Name     string
}

// Fresh creates a new empty database (NOT migrated) and returns its URLs. Requires
// ALPINE_TEST_ADMIN_URL; skips otherwise.
func Fresh(t testing.TB) Env {
	t.Helper()
	admin := os.Getenv("ALPINE_TEST_ADMIN_URL")
	if admin == "" {
		t.Skip("ALPINE_TEST_ADMIN_URL not set; skipping Postgres integration test")
	}
	ctx := context.Background()
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := "alpine_t_" + hex.EncodeToString(b)
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("dbtest: connect admin: %v", err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("dbtest: create database: %v", err)
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), admin)
		if err != nil {
			return
		}
		defer c.Close(context.Background())
		_, _ = c.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatalf("dbtest: parse admin url: %v", err)
	}
	u.Path = "/" + name
	env := Env{AdminURL: u.String(), Name: name}
	pw := os.Getenv("ALPINE_TEST_APP_PASSWORD")
	if pw == "" {
		pw = "alpine_app"
	}
	app := *u
	app.User = url.UserPassword("alpine_app", pw)
	env.AppURL = app.String()
	return env
}

// Open returns a migrated database connected as alpine_app. With ALPINE_TEST_ADMIN_URL a fresh
// database is created and migrated; otherwise ALPINE_TEST_DATABASE_URL is used as-is.
func Open(t testing.TB) (*db.DB, Env) {
	t.Helper()
	ctx := context.Background()
	var env Env
	if os.Getenv("ALPINE_TEST_ADMIN_URL") != "" {
		env = Fresh(t)
		if err := db.Migrate(ctx, env.AdminURL, "up"); err != nil {
			t.Fatalf("dbtest: migrate: %v", err)
		}
	} else {
		env.AppURL = os.Getenv("ALPINE_TEST_DATABASE_URL")
		if env.AppURL == "" {
			t.Skip("ALPINE_TEST_ADMIN_URL / ALPINE_TEST_DATABASE_URL not set; skipping Postgres integration test")
		}
	}
	d, err := db.OpenWithConfig(ctx, env.AppURL, db.PoolConfig{MaxConns: 8, ApplicationName: "alpine-test"})
	if err != nil {
		t.Fatalf("dbtest: open: %v", err)
	}
	t.Cleanup(d.Close)
	return d, env
}
