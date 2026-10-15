package db_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/alpine-dev/alpine/packages/shared/db"
	"github.com/alpine-dev/alpine/packages/shared/db/dbtest"
)

// These integration tests need Postgres. They are skipped unless ALPINE_TEST_ADMIN_URL (fresh
// database per test) or ALPINE_TEST_DATABASE_URL (pre-migrated, alpine_app) is set; see
// packages/shared/db/dbtest and scripts/test-db.sh.
func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, _ := dbtest.Open(t)
	return d
}

func createOrg(t *testing.T, d *db.DB, slug string) (uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	org := uuid.New()
	inst := uuid.New()
	require.NoError(t, d.WithTenant(ctx, db.Scope{OrgID: org}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO orgs (id, slug, name) VALUES ($1, $2, $2)`, org, slug); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO scm_installations (id, org_id, installation_id, account_login)
			VALUES ($1, $2, $3, $4)`, inst, org, int64(uuid.New().ID()), slug)
		return err
	}))
	return org, inst
}

func TestRLSIsolatesTenants(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	suffix := uuid.NewString()[:8]
	orgA, instA := createOrg(t, d, "rls-a-"+suffix)
	orgB, _ := createOrg(t, d, "rls-b-"+suffix)

	repoA := uuid.New()
	require.NoError(t, d.WithTenant(ctx, db.Scope{OrgID: orgA}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO repos (id, org_id, installation_id, provider_repo_id, full_name)
			VALUES ($1, $2, $3, 1, 'a/repo')`, repoA, orgA, instA)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO tickets (id, org_id, repo_id, key, title) VALUES ($1, $2, $3, 'GH-1', 'secret A')`,
			uuid.New(), orgA, repoA)
		return err
	}))

	count := func(scope db.Scope, q string, args ...any) int {
		var n int
		run := func(tx pgx.Tx) error { return tx.QueryRow(ctx, q, args...).Scan(&n) }
		if scope.OrgID == uuid.Nil {
			require.NoError(t, d.WithoutTenant(ctx, run))
		} else {
			require.NoError(t, d.WithTenant(ctx, scope, run))
		}
		return n
	}

	t.Run("owner tenant sees its rows", func(t *testing.T) {
		require.Equal(t, 1, count(db.Scope{OrgID: orgA}, `SELECT count(*) FROM tickets WHERE repo_id = $1`, repoA))
		require.Equal(t, 1, count(db.Scope{OrgID: orgA}, `SELECT count(*) FROM repos WHERE id = $1`, repoA))
	})
	t.Run("other tenant sees nothing", func(t *testing.T) {
		require.Equal(t, 0, count(db.Scope{OrgID: orgB}, `SELECT count(*) FROM tickets WHERE repo_id = $1`, repoA))
		require.Equal(t, 0, count(db.Scope{OrgID: orgB}, `SELECT count(*) FROM repos WHERE id = $1`, repoA))
		require.Equal(t, 0, count(db.Scope{OrgID: orgB}, `SELECT count(*) FROM orgs WHERE id = $1`, orgA))
	})
	t.Run("no tenant context fails closed", func(t *testing.T) {
		require.Equal(t, 0, count(db.Scope{}, `SELECT count(*) FROM tickets WHERE repo_id = $1`, repoA))
		require.Equal(t, 0, count(db.Scope{}, `SELECT count(*) FROM orgs WHERE id = $1`, orgA))
	})
	t.Run("cannot write into another tenant", func(t *testing.T) {
		err := d.WithTenant(ctx, db.Scope{OrgID: orgB}, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO tickets (id, org_id, repo_id, key, title) VALUES ($1, $2, $3, 'GH-2', 'x')`,
				uuid.New(), orgA, repoA)
			return err
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "row-level security")
	})
	t.Run("cannot update another tenant's rows", func(t *testing.T) {
		var affected int64
		require.NoError(t, d.WithTenant(ctx, db.Scope{OrgID: orgB}, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `UPDATE tickets SET title = 'pwned' WHERE repo_id = $1`, repoA)
			affected = tag.RowsAffected()
			return err
		}))
		require.Zero(t, affected)
	})
	t.Run("audit log is append-only", func(t *testing.T) {
		err := d.WithTenant(ctx, db.Scope{OrgID: orgA}, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `INSERT INTO audit_log (org_id, actor_type, actor_id, action, target_type, target_id)
				VALUES ($1, 'system', 'test', 'test.append_only', 'org', $2)`, orgA, orgA.String()); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `DELETE FROM audit_log WHERE org_id = $1`, orgA)
			return err
		})
		require.Error(t, err)
	})
	t.Run("installation resolver exposes only ids", func(t *testing.T) {
		var inst int64
		require.NoError(t, d.WithTenant(ctx, db.Scope{OrgID: orgA}, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT installation_id FROM scm_installations WHERE id = $1`, instA).Scan(&inst)
		}))
		var got uuid.UUID
		require.NoError(t, d.WithoutTenant(ctx, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT org_id FROM alpine_resolve_installation('github', $1)`, inst).Scan(&got)
		}))
		require.Equal(t, orgA, got)
	})
}
