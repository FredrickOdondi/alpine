package db_test

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/alpine-dev/alpine/packages/shared/db"
)

// Cross-tenant guarantees beyond plain RLS: same-tenant composite FKs, the new auth tables,
// partition privileges, user-scoped visibility and the keep-an-owner trigger.
func TestTenantIsolationHardening(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	suffix := uuid.NewString()[:8]
	orgA, instA := createOrg(t, d, "iso-a-"+suffix)
	orgB, instB := createOrg(t, d, "iso-b-"+suffix)

	userA, userB := uuid.New(), uuid.New()
	require.NoError(t, d.WithoutTenant(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO users (id, email, email_verified) VALUES ($1, $2, true), ($3, $4, true)`,
			userA, "a-"+suffix+"@example.com", userB, "b-"+suffix+"@example.com")
		return err
	}))
	repoA := uuid.New()
	tokenA := sha256.Sum256([]byte("invite-a-" + suffix))
	require.NoError(t, d.WithTenant(ctx, db.Scope{OrgID: orgA}, func(tx pgx.Tx) error {
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO memberships (org_id, user_id, role) VALUES ($1, $2, 'owner')`, []any{orgA, userA}},
			{`INSERT INTO repos (id, org_id, installation_id, provider_repo_id, full_name) VALUES ($1, $2, $3, 7, 'a/r')`, []any{repoA, orgA, instA}},
			{`INSERT INTO invitations (org_id, email, role, token_hash, expires_at) VALUES ($1, 'x@example.com', 'member', $2, now() + interval '1 day')`, []any{orgA, tokenA[:]}},
			{`INSERT INTO api_keys (org_id, name, prefix, secret_hash, scopes) VALUES ($1, 'ci', $2, $3, ARRAY['tickets:read'])`, []any{orgA, "aaaa" + suffix, tokenA[:]}},
		} {
			if _, err := tx.Exec(ctx, q.sql, q.args...); err != nil {
				return err
			}
		}
		return nil
	}))
	require.NoError(t, d.WithTenant(ctx, db.Scope{OrgID: orgB}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO memberships (org_id, user_id, role) VALUES ($1, $2, 'owner')`, orgB, userB)
		return err
	}))

	countIn := func(org uuid.UUID, q string, args ...any) int {
		var n int
		require.NoError(t, d.WithTenant(ctx, db.Scope{OrgID: org}, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, q, args...).Scan(&n)
		}))
		return n
	}

	t.Run("cannot reference another tenant's repo even with own org_id", func(t *testing.T) {
		err := d.WithTenant(ctx, db.Scope{OrgID: orgB}, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO tickets (id, org_id, repo_id, key, title) VALUES ($1, $2, $3, 'X-1', 'x')`,
				uuid.New(), orgB, repoA)
			return err
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "tickets_repo_fk")
	})
	t.Run("cannot attach a repo to another tenant's installation", func(t *testing.T) {
		err := d.WithTenant(ctx, db.Scope{OrgID: orgB}, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO repos (org_id, installation_id, provider_repo_id, full_name) VALUES ($1, $2, 8, 'b/r')`,
				orgB, instA)
			return err
		})
		require.Error(t, err)
		_ = instB
	})
	t.Run("invitations and api keys are tenant-isolated", func(t *testing.T) {
		require.Equal(t, 1, countIn(orgA, `SELECT count(*) FROM invitations`))
		require.Equal(t, 0, countIn(orgB, `SELECT count(*) FROM invitations WHERE org_id = $1`, orgA))
		require.Equal(t, 0, countIn(orgB, `SELECT count(*) FROM api_keys WHERE org_id = $1`, orgA))
		var affected int64
		require.NoError(t, d.WithTenant(ctx, db.Scope{OrgID: orgB}, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `UPDATE api_keys SET revoked_at = now() WHERE org_id = $1`, orgA)
			affected = tag.RowsAffected()
			return err
		}))
		require.Zero(t, affected)
	})
	t.Run("pre-tenant resolvers return ids only for live rows", func(t *testing.T) {
		var gotOrg uuid.UUID
		require.NoError(t, d.WithoutTenant(ctx, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT org_id FROM alpine_resolve_api_key($1)`, "aaaa"+suffix).Scan(&gotOrg)
		}))
		require.Equal(t, orgA, gotOrg)
		require.NoError(t, d.WithoutTenant(ctx, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT org_id FROM alpine_resolve_invitation($1)`, tokenA[:]).Scan(&gotOrg)
		}))
		require.Equal(t, orgA, gotOrg)
	})
	t.Run("memberships visible to own user across orgs only", func(t *testing.T) {
		var n int
		require.NoError(t, d.WithUser(ctx, userB, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM memberships`).Scan(&n)
		}))
		require.Equal(t, 1, n)
		require.NoError(t, d.WithUser(ctx, userB, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM orgs WHERE id = $1`, orgA).Scan(&n)
		}))
		require.Zero(t, n)
	})
	t.Run("a user cannot add themselves to another org", func(t *testing.T) {
		err := d.WithUser(ctx, userB, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO memberships (org_id, user_id, role) VALUES ($1, $2, 'owner')`, orgA, userB)
			return err
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "row-level security")
	})
	t.Run("partitions are not directly accessible to the app role", func(t *testing.T) {
		err := d.WithTenant(ctx, db.Scope{OrgID: orgA}, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `SELECT count(*) FROM audit_log_default`)
			return err
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "permission denied")
	})
	t.Run("audit log reads are tenant scoped across partitions", func(t *testing.T) {
		require.NoError(t, d.WithTenant(ctx, db.Scope{OrgID: orgA}, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO audit_log (org_id, actor_type, actor_id, action, target_type, target_id, at)
				VALUES ($1, 'system', 't', 'test.old', 'org', $2, now() - interval '400 days'),
				       ($1, 'system', 't', 'test.now', 'org', $2, now())`, orgA, orgA.String())
			return err
		}))
		require.Equal(t, 2, countIn(orgA, `SELECT count(*) FROM audit_log WHERE action LIKE 'test.%'`))
		require.Zero(t, countIn(orgB, `SELECT count(*) FROM audit_log WHERE org_id = $1`, orgA))
	})
	t.Run("auth events: append from anywhere, read only your own", func(t *testing.T) {
		require.NoError(t, d.WithoutTenant(ctx, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO auth_events (user_id, event, provider) VALUES ($1, 'login.succeeded', 'google')`, userA)
			return err
		}))
		var n int
		require.NoError(t, d.WithUser(ctx, userA, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM auth_events`).Scan(&n)
		}))
		require.Equal(t, 1, n)
		require.NoError(t, d.WithUser(ctx, userB, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM auth_events`).Scan(&n)
		}))
		require.Zero(t, n)
	})
	t.Run("RBAC policy table is read-only to the app", func(t *testing.T) {
		err := d.WithoutTenant(ctx, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO role_permissions VALUES ('viewer', 'org:delete')`)
			return err
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "permission denied")
	})
	t.Run("org keeps at least one owner", func(t *testing.T) {
		err := d.WithTenant(ctx, db.Scope{OrgID: orgA}, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE memberships SET role = 'admin' WHERE org_id = $1 AND user_id = $2`, orgA, userA)
			return err
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "at least one owner")
		err = d.WithTenant(ctx, db.Scope{OrgID: orgA}, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `DELETE FROM memberships WHERE org_id = $1 AND user_id = $2`, orgA, userA)
			return err
		})
		require.Error(t, err)
	})
	t.Run("expired invitation timestamps are constrained", func(t *testing.T) {
		err := d.WithTenant(ctx, db.Scope{OrgID: orgA}, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO invitations (org_id, email, role, token_hash, expires_at) VALUES ($1, 'y@example.com', 'member', $2, $3)`,
				orgA, make([]byte, 32), time.Now().Add(-time.Hour))
			return err
		})
		require.Error(t, err)
	})
}
