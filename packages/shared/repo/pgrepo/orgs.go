package pgrepo

import (
	"context"

	"github.com/google/uuid"

	"github.com/alpine-dev/alpine/packages/shared/db"
	"github.com/alpine-dev/alpine/packages/shared/db/dbq"
	"github.com/alpine-dev/alpine/packages/shared/repo"
)

func toOrg(o dbq.Org) repo.Org {
	return repo.Org{ID: o.ID, Slug: o.Slug, Name: o.Name, Plan: o.Plan, CreatedAt: o.CreatedAt}
}

func toMembership(m dbq.Membership) repo.Membership {
	return repo.Membership{OrgID: m.OrgID, UserID: m.UserID, Role: repo.Role(m.Role), CreatedAt: m.CreatedAt}
}

// CreateOrg implements repo.Orgs: creates the org and makes owner its first owner.
func (s *Store) CreateOrg(ctx context.Context, a repo.Actor, owner uuid.UUID, slug, name string) (repo.Org, error) {
	var out repo.Org
	id := uuid.New()
	err := s.DB.Tenant(ctx, db.Scope{OrgID: id, UserID: owner}, func(q *dbq.Queries) error {
		o, err := q.CreateOrg(ctx, dbq.CreateOrgParams{ID: id, Slug: slug, Name: name})
		if err != nil {
			return err
		}
		if _, err := q.AddMembership(ctx, dbq.AddMembershipParams{OrgID: id, UserID: owner, Role: "owner"}); err != nil {
			return err
		}
		out = toOrg(o)
		return audit(ctx, q, id, a, "org.created", "org", id.String(), map[string]any{"slug": slug})
	})
	return out, mapErr(err)
}

// GetOrg implements repo.Orgs.
func (s *Store) GetOrg(ctx context.Context, orgID uuid.UUID) (repo.Org, error) {
	var out repo.Org
	err := s.DB.Tenant(ctx, db.Scope{OrgID: orgID}, func(q *dbq.Queries) error {
		o, err := q.GetOrg(ctx, orgID)
		out = toOrg(o)
		return err
	})
	return out, mapErr(err)
}

// UpdateOrg implements repo.Orgs.
func (s *Store) UpdateOrg(ctx context.Context, a repo.Actor, orgID uuid.UUID, name string) (repo.Org, error) {
	var out repo.Org
	err := s.DB.Tenant(ctx, db.Scope{OrgID: orgID}, func(q *dbq.Queries) error {
		o, err := q.UpdateOrgName(ctx, dbq.UpdateOrgNameParams{ID: orgID, Name: name})
		if err != nil {
			return err
		}
		out = toOrg(o)
		return audit(ctx, q, orgID, a, "org.updated", "org", orgID.String(), map[string]any{"fields": []string{"name"}})
	})
	return out, mapErr(err)
}

// DeleteOrg implements repo.Orgs (soft delete; a purge job hard-deletes after retention).
func (s *Store) DeleteOrg(ctx context.Context, a repo.Actor, orgID uuid.UUID) error {
	err := s.DB.Tenant(ctx, db.Scope{OrgID: orgID}, func(q *dbq.Queries) error {
		n, err := q.SoftDeleteOrg(ctx, orgID)
		if err != nil {
			return err
		}
		if n == 0 {
			return repo.ErrNotFound
		}
		return audit(ctx, q, orgID, a, "org.deleted", "org", orgID.String(), nil)
	})
	return mapErr(err)
}

// ListOrgsForUser implements repo.Orgs.
func (s *Store) ListOrgsForUser(ctx context.Context, userID uuid.UUID) ([]repo.OrgMembership, error) {
	out := []repo.OrgMembership{}
	err := s.DB.User(ctx, userID, func(q *dbq.Queries) error {
		rows, err := q.ListOrgsForUser(ctx, userID)
		for _, r := range rows {
			out = append(out, repo.OrgMembership{
				Org:  repo.Org{ID: r.ID, Slug: r.Slug, Name: r.Name, Plan: r.Plan, CreatedAt: r.CreatedAt},
				Role: repo.Role(r.Role),
			})
		}
		return err
	})
	return out, mapErr(err)
}

// GetMembership implements repo.Orgs.
func (s *Store) GetMembership(ctx context.Context, orgID, userID uuid.UUID) (repo.Membership, error) {
	var out repo.Membership
	err := s.DB.Tenant(ctx, db.Scope{OrgID: orgID, UserID: userID}, func(q *dbq.Queries) error {
		m, err := q.GetMembership(ctx, dbq.GetMembershipParams{OrgID: orgID, UserID: userID})
		out = toMembership(m)
		return err
	})
	return out, mapErr(err)
}

// ListMembers implements repo.Orgs.
func (s *Store) ListMembers(ctx context.Context, orgID uuid.UUID) ([]repo.Member, error) {
	out := []repo.Member{}
	err := s.DB.Tenant(ctx, db.Scope{OrgID: orgID}, func(q *dbq.Queries) error {
		rows, err := q.ListMembers(ctx, orgID)
		for _, r := range rows {
			out = append(out, repo.Member{
				Membership: repo.Membership{OrgID: r.OrgID, UserID: r.UserID, Role: repo.Role(r.Role), CreatedAt: r.CreatedAt},
				Email:      str(r.Email), Name: str(r.Name), AvatarURL: str(r.AvatarUrl), GitHubLogin: str(r.GithubLogin),
				LastLoginAt: r.LastLoginAt,
			})
		}
		return err
	})
	return out, mapErr(err)
}

// UpdateMemberRole implements repo.Orgs.
func (s *Store) UpdateMemberRole(ctx context.Context, a repo.Actor, orgID, userID uuid.UUID, role repo.Role) (repo.Membership, error) {
	var out repo.Membership
	err := s.DB.Tenant(ctx, db.Scope{OrgID: orgID}, func(q *dbq.Queries) error {
		prev, err := q.GetMembership(ctx, dbq.GetMembershipParams{OrgID: orgID, UserID: userID})
		if err != nil {
			return err
		}
		m, err := q.UpdateMemberRole(ctx, dbq.UpdateMemberRoleParams{OrgID: orgID, UserID: userID, Role: string(role)})
		if err != nil {
			return err
		}
		out = toMembership(m)
		return audit(ctx, q, orgID, a, "member.role_changed", "user", userID.String(),
			map[string]any{"from": prev.Role, "to": string(role)})
	})
	return out, mapErr(err)
}

// RemoveMember implements repo.Orgs.
func (s *Store) RemoveMember(ctx context.Context, a repo.Actor, orgID, userID uuid.UUID) error {
	err := s.DB.Tenant(ctx, db.Scope{OrgID: orgID}, func(q *dbq.Queries) error {
		prev, err := q.GetMembership(ctx, dbq.GetMembershipParams{OrgID: orgID, UserID: userID})
		if err != nil {
			return err
		}
		if _, err := q.RemoveMember(ctx, dbq.RemoveMemberParams{OrgID: orgID, UserID: userID}); err != nil {
			return err
		}
		return audit(ctx, q, orgID, a, "member.removed", "user", userID.String(), map[string]any{"role": prev.Role})
	})
	return mapErr(err)
}
