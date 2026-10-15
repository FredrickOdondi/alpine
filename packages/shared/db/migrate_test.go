package db_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/alpine-dev/alpine/packages/shared/db"
	"github.com/alpine-dev/alpine/packages/shared/db/dbtest"
)

// TestMigrationsUpDownUp applies every migration, rolls all of them back, and re-applies them on
// a fresh database, then checks the partition maintenance functions.
func TestMigrationsUpDownUp(t *testing.T) {
	env := dbtest.Fresh(t)
	ctx := context.Background()
	require.NoError(t, db.Migrate(ctx, env.AdminURL, "up"))
	require.NoError(t, db.Migrate(ctx, env.AdminURL, "down-to", "0"))

	conn, err := pgx.Connect(ctx, env.AdminURL)
	require.NoError(t, err)
	defer conn.Close(ctx)
	var tables int
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT count(*) FROM pg_tables WHERE schemaname = 'public' AND tablename <> 'goose_db_version'`).Scan(&tables))
	require.Zero(t, tables, "down migrations must remove every table")

	require.NoError(t, db.Migrate(ctx, env.AdminURL, "up"))

	// Step-wise down/up of the newest migrations once more (exercise each Down individually).
	for i := 0; i < 3; i++ {
		require.NoError(t, db.Migrate(ctx, env.AdminURL, "down"))
	}
	require.NoError(t, db.Migrate(ctx, env.AdminURL, "up"))

	t.Run("partition maintenance is idempotent and owner-only", func(t *testing.T) {
		var created int
		require.NoError(t, conn.QueryRow(ctx, `SELECT alpine_ensure_monthly_partitions('audit_log', 1, 3)`).Scan(&created))
		require.Zero(t, created, "already created by the migration")
		require.NoError(t, conn.QueryRow(ctx, `SELECT alpine_ensure_monthly_partitions('audit_log', 1, 6)`).Scan(&created))
		require.Equal(t, 3, created)
		var detached []string
		rows, err := conn.Query(ctx, `SELECT alpine_retire_partitions('audit_log', 120, true)`)
		require.NoError(t, err)
		for rows.Next() {
			var s string
			require.NoError(t, rows.Scan(&s))
			detached = append(detached, s)
		}
		require.Empty(t, detached)

		app, err := pgx.Connect(ctx, env.AppURL)
		require.NoError(t, err)
		defer app.Close(ctx)
		_, err = app.Exec(ctx, `SELECT alpine_ensure_monthly_partitions('audit_log', 0, 1)`)
		require.ErrorContains(t, err, "permission denied")
	})
}
