package db

import (
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PoolConfig holds pgxpool and per-connection server settings. See docs/database.md "Connection
// pooling" for sizing guidance.
type PoolConfig struct {
	MaxConns          int32         // per process; keep sum(all pods) below max_connections - reserve
	MinConns          int32         // warm connections kept open
	MaxConnLifetime   time.Duration // recycle connections (lets RDS proxy/failover rebalance)
	MaxConnIdleTime   time.Duration
	HealthCheckPeriod time.Duration
	// Server-side guards applied to every connection via startup parameters.
	StatementTimeout       time.Duration // kills runaway queries
	LockTimeout            time.Duration // fail fast instead of queueing behind DDL
	IdleInTxSessionTimeout time.Duration // reclaims connections leaked mid-transaction
	ApplicationName        string
	ReplicaURL             string // optional read replica for WithTenantRead
}

// DefaultPoolConfig returns production-safe defaults, overridable through env:
// DB_MAX_CONNS, DB_MIN_CONNS, DB_MAX_CONN_LIFETIME, DB_MAX_CONN_IDLE_TIME,
// DB_STATEMENT_TIMEOUT, DB_LOCK_TIMEOUT, DB_IDLE_IN_TX_TIMEOUT, DB_APPLICATION_NAME,
// DATABASE_REPLICA_URL.
func DefaultPoolConfig() PoolConfig {
	return PoolConfig{
		MaxConns:               int32(envInt("DB_MAX_CONNS", 20)),
		MinConns:               int32(envInt("DB_MIN_CONNS", 2)),
		MaxConnLifetime:        envDur("DB_MAX_CONN_LIFETIME", 30*time.Minute),
		MaxConnIdleTime:        envDur("DB_MAX_CONN_IDLE_TIME", 5*time.Minute),
		HealthCheckPeriod:      30 * time.Second,
		StatementTimeout:       envDur("DB_STATEMENT_TIMEOUT", 15*time.Second),
		LockTimeout:            envDur("DB_LOCK_TIMEOUT", 5*time.Second),
		IdleInTxSessionTimeout: envDur("DB_IDLE_IN_TX_TIMEOUT", 30*time.Second),
		ApplicationName:        envStr("DB_APPLICATION_NAME", "alpine"),
		ReplicaURL:             os.Getenv("DATABASE_REPLICA_URL"),
	}
}

func (pc PoolConfig) apply(cfg *pgxpool.Config) {
	if pc.MaxConns > 0 {
		cfg.MaxConns = pc.MaxConns
	}
	if pc.MinConns > 0 {
		cfg.MinConns = pc.MinConns
	}
	if pc.MaxConnLifetime > 0 {
		cfg.MaxConnLifetime = pc.MaxConnLifetime
		cfg.MaxConnLifetimeJitter = pc.MaxConnLifetime / 10
	}
	if pc.MaxConnIdleTime > 0 {
		cfg.MaxConnIdleTime = pc.MaxConnIdleTime
	}
	if pc.HealthCheckPeriod > 0 {
		cfg.HealthCheckPeriod = pc.HealthCheckPeriod
	}
	rp := cfg.ConnConfig.RuntimeParams
	if pc.StatementTimeout > 0 {
		rp["statement_timeout"] = strconv.FormatInt(pc.StatementTimeout.Milliseconds(), 10)
	}
	if pc.LockTimeout > 0 {
		rp["lock_timeout"] = strconv.FormatInt(pc.LockTimeout.Milliseconds(), 10)
	}
	if pc.IdleInTxSessionTimeout > 0 {
		rp["idle_in_transaction_session_timeout"] = strconv.FormatInt(pc.IdleInTxSessionTimeout.Milliseconds(), 10)
	}
	if pc.ApplicationName != "" {
		rp["application_name"] = pc.ApplicationName
	}
}

func envStr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil && v > 0 {
		return v
	}
	return def
}

func envDur(k string, def time.Duration) time.Duration {
	if v, err := time.ParseDuration(os.Getenv(k)); err == nil && v > 0 {
		return v
	}
	return def
}
