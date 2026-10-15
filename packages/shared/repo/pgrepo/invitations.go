package pgrepo

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/alpine-dev/alpine/packages/shared/db"
	"github.com/alpine-dev/alpine/packages/shared/db/dbq"
	"github.com/alpine-dev/alpine/packages/shared/repo"
)

func toInvitation(i dbq.Invitation) repo.Invitation {
	return repo.Invitation{
		ID: i.ID, OrgID: i.OrgID, Email: i.Email, Role: repo.Role(i.Role), InvitedBy: i.InvitedBy,
		ExpiresAt: i.ExpiresAt, AcceptedAt: i.AcceptedAt, AcceptedBy: i.AcceptedBy, RevokedAt: i.RevokedAt,
		CreatedAt: i.CreatedAt,
	}
}

func actorUser(a repo.Actor) *uuid.UUID {
	if a.Type != "user" {
		return nil
	}
	id, err := uuid.Parse(a.ID)
	if err != nil {
		return nil
	}
	return &id
}

// CreateInvitation implements repo.Invitations.
func (s *Store) CreateInvitation(ctx context.Context, a repo.Actor, orgID uuid.UUID, email string, role repo.Role, tokenHash []byte, expiresAt time.Time) (repo.Invitation, error) {
	var out repo.Invitation
	err := s.DB.Tenant(ctx, db.Scope{OrgID: orgID}, func(q *dbq.Queries) error {
		if _, err := q.RevokePendingInvitationsForEmail(ctx, dbq.RevokePendingInvitationsForEmailParams{OrgID: orgID, Email: email}); err != nil {
			return err
		}
		inv, err := q.CreateInvitation(ctx, dbq.CreateInvitationParams{
			OrgID: orgID, Email: email, Role: string(role), TokenHash: tokenHash, InvitedBy: actorUser(a), ExpiresAt: expiresAt,
		})
		if err != nil {
			return err
		}
		out = toInvitation(inv)
		// The email is stored on the invitation row; the audit entry references it by id only.
		return audit(ctx, q, orgID, a, "invitation.created", "invitation", inv.ID.String(),
			map[string]any{"role": string(role), "expires_at": expiresAt.UTC()})
	})
	return out, mapErr(err)
}

// ListInvitations implements repo.Invitations.
func (s *Store) ListInvitations(ctx context.Context, orgID uuid.UUID, pendingOnly bool) ([]repo.Invitation, error) {
	out := []repo.Invitation{}
	err := s.DB.Tenant(ctx, db.Scope{OrgID: orgID}, func(q *dbq.Queries) error {
		rows, err := q.ListInvitations(ctx, dbq.ListInvitationsParams{OrgID: orgID, PendingOnly: pendingOnly})
		for _, r := range rows {
			out = append(out, toInvitation(r))
		}
		return err
	})
	return out, mapErr(err)
}

// RevokeInvitation implements repo.Invitations.
func (s *Store) RevokeInvitation(ctx context.Context, a repo.Actor, orgID, id uuid.UUID) error {
	err := s.DB.Tenant(ctx, db.Scope{OrgID: orgID}, func(q *dbq.Queries) error {
		n, err := q.RevokeInvitation(ctx, dbq.RevokeInvitationParams{ID: id, OrgID: orgID})
		if err != nil {
			return err
		}
		if n == 0 {
			return repo.ErrNotFound
		}
		return audit(ctx, q, orgID, a, "invitation.revoked", "invitation", id.String(), nil)
	})
	return mapErr(err)
}

// AcceptInvitation implements repo.Invitations.
func (s *Store) AcceptInvitation(ctx context.Context, a repo.Actor, tokenHash []byte, user repo.User) (repo.Invitation, repo.Membership, error) {
	var ref dbq.ResolveInvitationRow
	if err := s.DB.Global(ctx, func(q *dbq.Queries) error {
		var err error
		ref, err = q.ResolveInvitation(ctx, tokenHash)
		return err
	}); err != nil {
		if errors.Is(mapErr(err), repo.ErrNotFound) {
			return repo.Invitation{}, repo.Membership{}, repo.ErrInvitationInvalid
		}
		return repo.Invitation{}, repo.Membership{}, mapErr(err)
	}
	var (
		inv repo.Invitation
		mem repo.Membership
	)
	err := s.DB.Tenant(ctx, db.Scope{OrgID: ref.OrgID, UserID: user.ID}, func(q *dbq.Queries) error {
		row, err := q.GetInvitation(ctx, ref.ID)
		if err != nil {
			return err
		}
		inv = toInvitation(row)
		switch inv.Status(s.now()) {
		case "expired":
			return repo.ErrInvitationExpired
		case "pending":
		default:
			return repo.ErrInvitationInvalid
		}
		if !user.EmailVerified || !strings.EqualFold(user.Email, inv.Email) {
			return repo.ErrInvitationMismatch
		}
		n, err := q.MarkInvitationAccepted(ctx, dbq.MarkInvitationAcceptedParams{ID: inv.ID, UserID: &user.ID})
		if err != nil {
			return err
		}
		if n == 0 { // lost a race with a concurrent accept/revoke
			return repo.ErrInvitationInvalid
		}
		m, err := q.AddMembership(ctx, dbq.AddMembershipParams{OrgID: inv.OrgID, UserID: user.ID, Role: string(inv.Role), InvitedBy: inv.InvitedBy})
		if err != nil {
			return err
		}
		mem = toMembership(m)
		now := s.now()
		inv.AcceptedAt, inv.AcceptedBy = &now, &user.ID
		if err := audit(ctx, q, inv.OrgID, a, "invitation.accepted", "invitation", inv.ID.String(), map[string]any{"user_id": user.ID}); err != nil {
			return err
		}
		return audit(ctx, q, inv.OrgID, a, "member.added", "user", user.ID.String(),
			map[string]any{"role": mem.Role, "via": "invitation", "invitation_id": inv.ID})
	})
	if err != nil {
		return repo.Invitation{}, repo.Membership{}, mapErr(err)
	}
	return inv, mem, nil
}
