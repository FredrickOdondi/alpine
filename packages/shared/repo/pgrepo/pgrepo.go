// Package pgrepo implements packages/shared/repo on Postgres using the sqlc queries in
// packages/shared/db/dbq. Org-scoped calls run in db.WithTenant transactions (RLS); global identity
// calls run without tenant context; pre-tenant lookups use the SECURITY DEFINER resolvers.
package pgrepo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/alpine-dev/alpine/packages/shared/db"
	"github.com/alpine-dev/alpine/packages/shared/db/dbq"
	"github.com/alpine-dev/alpine/packages/shared/repo"
)

// Store is the Postgres repo.Store.
type Store struct {
	DB  *db.DB
	Now func() time.Time
}

var _ repo.Store = (*Store)(nil)

// New returns a Store.
func New(d *db.DB) *Store { return &Store{DB: d} }

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// mapErr turns driver errors into repo errors.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return repo.ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation
			return fmt.Errorf("%w: %s", repo.ErrConflict, pgErr.ConstraintName)
		case "23514": // check_violation (incl. the keep-owner trigger)
			if strings.Contains(pgErr.Message, "at least one owner") {
				return repo.ErrLastOwner
			}
		}
	}
	return err
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func audit(ctx context.Context, q *dbq.Queries, orgID uuid.UUID, a repo.Actor, action, targetType, targetID string, meta map[string]any) error {
	if meta == nil {
		meta = map[string]any{}
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	actorType := a.Type
	if actorType == "" {
		actorType = "system"
	}
	actorID := a.ID
	if actorID == "" {
		actorID = "system"
	}
	return q.InsertAuditLog(ctx, dbq.InsertAuditLogParams{
		OrgID: orgID, ActorType: actorType, ActorID: actorID, Action: action,
		TargetType: targetType, TargetID: targetID, Metadata: b, Ip: a.IP, RequestID: strPtr(a.RequestID),
	})
}
