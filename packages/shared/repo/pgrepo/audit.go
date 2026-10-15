package pgrepo

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/alpine-dev/alpine/packages/shared/db"
	"github.com/alpine-dev/alpine/packages/shared/db/dbq"
	"github.com/alpine-dev/alpine/packages/shared/repo"
)

// Record implements repo.Audit.
func (s *Store) Record(ctx context.Context, e repo.AuditEntry) error {
	return mapErr(s.DB.Tenant(ctx, db.Scope{OrgID: e.OrgID}, func(q *dbq.Queries) error {
		return audit(ctx, q, e.OrgID, e.Actor, e.Action, e.TargetType, e.TargetID, e.Metadata)
	}))
}

// List implements repo.Audit.
func (s *Store) List(ctx context.Context, orgID uuid.UUID, f repo.AuditFilter) ([]repo.AuditEntry, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	out := []repo.AuditEntry{}
	err := s.DB.WithTenantRead(ctx, db.Scope{OrgID: orgID}, func(tx db.Tx) error {
		rows, err := dbq.New(tx).ListAuditLog(ctx, dbq.ListAuditLogParams{
			OrgID: orgID, BeforeAt: f.BeforeAt, BeforeID: f.BeforeID, Action: strPtr(f.Action), Lim: int32(f.Limit),
		})
		for _, r := range rows {
			e := repo.AuditEntry{
				ID: r.ID, OrgID: r.OrgID, Action: r.Action, TargetType: r.TargetType, TargetID: r.TargetID, At: r.At,
				Actor: repo.Actor{Type: r.ActorType, ID: r.ActorID, IP: r.Ip, RequestID: str(r.RequestID)},
			}
			_ = json.Unmarshal(r.Metadata, &e.Metadata)
			out = append(out, e)
		}
		return err
	})
	return out, mapErr(err)
}

// RecordAuthEvent implements repo.Audit.
func (s *Store) RecordAuthEvent(ctx context.Context, e repo.AuthEvent) error {
	meta := e.Metadata
	if meta == nil {
		meta = map[string]any{}
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	outcome := "failure"
	if e.Success {
		outcome = "success"
	}
	return mapErr(s.DB.Global(ctx, func(q *dbq.Queries) error {
		return q.InsertAuthEvent(ctx, dbq.InsertAuthEventParams{
			UserID: e.UserID, OrgID: e.OrgID, Event: e.Event, Provider: strPtr(e.Provider), Outcome: outcome,
			Reason: strPtr(e.Reason), Ip: e.IP, Metadata: b,
		})
	}))
}

// ListAuthEvents returns the user's own security events (newest first).
func (s *Store) ListAuthEvents(ctx context.Context, userID uuid.UUID, limit int) ([]repo.AuthEvent, error) {
	out := []repo.AuthEvent{}
	err := s.DB.User(ctx, userID, func(q *dbq.Queries) error {
		rows, err := q.ListAuthEventsForUser(ctx, dbq.ListAuthEventsForUserParams{UserID: &userID, Limit: int32(limit)})
		for _, r := range rows {
			e := repo.AuthEvent{UserID: r.UserID, OrgID: r.OrgID, Event: r.Event, Provider: str(r.Provider),
				Success: r.Outcome == "success", Reason: str(r.Reason), IP: r.Ip, At: r.At}
			_ = json.Unmarshal(r.Metadata, &e.Metadata)
			out = append(out, e)
		}
		return err
	})
	return out, mapErr(err)
}
