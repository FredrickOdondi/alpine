// Package db provides tenancy-aware Postgres access. Every tenant-scoped query runs inside
// WithTenant, which sets `app.org_id` (and optionally `app.user_id`) for the transaction so
// Row-Level Security policies apply. Services connect as the non-owner `alpine_app` role, which
// has no BYPASSRLS — forgetting WithTenant therefore returns no rows instead of leaking data.
//
// The settings are applied with set_config(name, value, true), which is exactly `SET LOCAL` but
// accepts bind parameters (SET LOCAL cannot be parameterised, and string-building it would be an
// injection risk). They vanish at COMMIT/ROLLBACK, so pooled connections (pgxpool, or PgBouncer
// in transaction mode) can never carry one tenant's context into another request.
package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alpine-dev/alpine/packages/shared/db/dbq"
)

// DB wraps the primary pool and an optional read-replica pool.
type DB struct {
	Pool *pgxpool.Pool
	// Replica, when set, serves WithTenantRead transactions (eventually consistent reads such as
	// dashboards and metrics). Nil means reads go to the primary.
	Replica *pgxpool.Pool
}

// Open connects to Postgres with DefaultPoolConfig and pings it.
func Open(ctx context.Context, url string) (*DB, error) {
	return OpenWithConfig(ctx, url, DefaultPoolConfig())
}

// OpenWithConfig connects with explicit pool settings. If pc.ReplicaURL is set a second pool is
// opened for read-only transactions.
func OpenWithConfig(ctx context.Context, url string, pc PoolConfig) (*DB, error) {
	pool, err := newPool(ctx, url, pc)
	if err != nil {
		return nil, err
	}
	d := &DB{Pool: pool}
	if pc.ReplicaURL != "" {
		rp, err := newPool(ctx, pc.ReplicaURL, pc)
		if err != nil {
			pool.Close()
			return nil, fmt.Errorf("replica: %w", err)
		}
		d.Replica = rp
	}
	return d, nil
}

func newPool(ctx context.Context, url string, pc PoolConfig) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	pc.apply(cfg)
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return pool, nil
}

// Close closes the pools.
func (d *DB) Close() {
	d.Pool.Close()
	if d.Replica != nil {
		d.Replica.Close()
	}
}

// Ping checks connectivity.
func (d *DB) Ping(ctx context.Context) error { return d.Pool.Ping(ctx) }

// Scope identifies the tenant (and optionally the user) for a transaction.
type Scope struct {
	OrgID  uuid.UUID
	UserID uuid.UUID // optional; enables "my memberships across orgs" visibility
}

// ErrNoTenant is returned when WithTenant is called without an org id.
var ErrNoTenant = errors.New("db: tenant scope requires an org id")

// ErrNoUser is returned when WithUser is called without a user id.
var ErrNoUser = errors.New("db: user scope requires a user id")

// WithTenant runs fn in a transaction with RLS tenant context set. The transaction commits if fn
// returns nil and rolls back otherwise.
func (d *DB) WithTenant(ctx context.Context, s Scope, fn func(tx pgx.Tx) error) error {
	if s.OrgID == uuid.Nil {
		return ErrNoTenant
	}
	return d.withSettings(ctx, d.Pool, pgx.TxOptions{}, s.OrgID.String(), userString(s.UserID), fn)
}

// WithTenantRead is WithTenant in a READ ONLY transaction, routed to the replica when configured.
func (d *DB) WithTenantRead(ctx context.Context, s Scope, fn func(tx pgx.Tx) error) error {
	if s.OrgID == uuid.Nil {
		return ErrNoTenant
	}
	pool := d.Pool
	if d.Replica != nil {
		pool = d.Replica
	}
	return d.withSettings(ctx, pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, s.OrgID.String(), userString(s.UserID), fn)
}

// WithUser runs fn with only the user context set (no tenant): used at login to list the user's
// memberships and orgs before an active org is chosen.
func (d *DB) WithUser(ctx context.Context, userID uuid.UUID, fn func(tx pgx.Tx) error) error {
	if userID == uuid.Nil {
		return ErrNoUser
	}
	return d.withSettings(ctx, d.Pool, pgx.TxOptions{}, "", userString(userID), fn)
}

// WithoutTenant runs fn without tenant context. Only global tables (users, identities,
// role_permissions) and SECURITY DEFINER lookup functions are usable; every tenant table returns
// zero rows.
func (d *DB) WithoutTenant(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return d.withSettings(ctx, d.Pool, pgx.TxOptions{}, "", "", fn)
}

// Tenant is WithTenant with sqlc queries bound to the transaction.
func (d *DB) Tenant(ctx context.Context, s Scope, fn func(q *dbq.Queries) error) error {
	return d.WithTenant(ctx, s, func(tx pgx.Tx) error { return fn(dbq.New(tx)) })
}

// User is WithUser with sqlc queries bound to the transaction.
func (d *DB) User(ctx context.Context, userID uuid.UUID, fn func(q *dbq.Queries) error) error {
	return d.WithUser(ctx, userID, func(tx pgx.Tx) error { return fn(dbq.New(tx)) })
}

// Global is WithoutTenant with sqlc queries bound to the transaction.
func (d *DB) Global(ctx context.Context, fn func(q *dbq.Queries) error) error {
	return d.WithoutTenant(ctx, func(tx pgx.Tx) error { return fn(dbq.New(tx)) })
}

func (d *DB) withSettings(ctx context.Context, pool *pgxpool.Pool, opts pgx.TxOptions, org, user string, fn func(tx pgx.Tx) error) error {
	tx, err := pool.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	// Equivalent to SET LOCAL app.org_id = ...; app.tenant_id is kept for older callers.
	if _, err := tx.Exec(ctx,
		`SELECT set_config('app.org_id', $1, true), set_config('app.tenant_id', $1, true),
		        set_config('app.user_id', $2, true)`,
		org, user); err != nil {
		return fmt.Errorf("set tenant context: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func userString(id uuid.UUID) string {
	if id == uuid.Nil {
		return ""
	}
	return id.String()
}

// IsNotFound reports whether err is pgx.ErrNoRows.
func IsNotFound(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// Tx is the pgx transaction type handed to WithTenant callbacks.
type Tx = pgx.Tx
