package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/pressly/goose/v3"
)

// Migrations holds the goose SQL migrations (db/migrations/*.sql).
//
//go:embed migrations/*.sql
var Migrations embed.FS

// Migrate runs a goose command ("up", "down", "status", "reset", "redo", "version",
// "up-to <v>", "down-to <v>") against databaseURL using the embedded migrations. It must run as
// the owner role, not alpine_app.
func Migrate(ctx context.Context, databaseURL, command string, args ...string) error {
	sqlDB, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer func() { _ = sqlDB.Close() }()
	goose.SetBaseFS(Migrations)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	return goose.RunContext(ctx, command, sqlDB, "migrations", args...)
}
