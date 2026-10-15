package pgrepo

import (
	"context"

	"github.com/google/uuid"

	"github.com/alpine-dev/alpine/packages/shared/db"
	"github.com/alpine-dev/alpine/packages/shared/db/dbq"
	"github.com/alpine-dev/alpine/packages/shared/repo"
)

func toAPIKey(k dbq.ApiKey) repo.APIKey {
	return repo.APIKey{
		ID: k.ID, OrgID: k.OrgID, Name: k.Name, Prefix: k.Prefix, SecretHash: k.SecretHash, Scopes: k.Scopes,
		Role: repo.Role(k.Role), CreatedBy: k.CreatedBy, ExpiresAt: k.ExpiresAt, LastUsedAt: k.LastUsedAt,
		RevokedAt: k.RevokedAt, CreatedAt: k.CreatedAt,
	}
}

// CreateAPIKey implements repo.APIKeys.
func (s *Store) CreateAPIKey(ctx context.Context, a repo.Actor, k repo.NewAPIKey) (repo.APIKey, error) {
	var out repo.APIKey
	err := s.DB.Tenant(ctx, db.Scope{OrgID: k.OrgID}, func(q *dbq.Queries) error {
		row, err := q.CreateAPIKey(ctx, dbq.CreateAPIKeyParams{
			OrgID: k.OrgID, Name: k.Name, Prefix: k.Prefix, SecretHash: k.SecretHash, Scopes: k.Scopes,
			Role: string(k.Role), CreatedBy: actorUser(a), ExpiresAt: k.ExpiresAt,
		})
		if err != nil {
			return err
		}
		out = toAPIKey(row)
		return audit(ctx, q, k.OrgID, a, "api_key.created", "api_key", row.ID.String(),
			map[string]any{"name": k.Name, "prefix": k.Prefix, "scopes": k.Scopes, "role": string(k.Role)})
	})
	return out, mapErr(err)
}

// ListAPIKeys implements repo.APIKeys.
func (s *Store) ListAPIKeys(ctx context.Context, orgID uuid.UUID) ([]repo.APIKey, error) {
	out := []repo.APIKey{}
	err := s.DB.Tenant(ctx, db.Scope{OrgID: orgID}, func(q *dbq.Queries) error {
		rows, err := q.ListAPIKeys(ctx, orgID)
		for _, r := range rows {
			out = append(out, toAPIKey(r))
		}
		return err
	})
	return out, mapErr(err)
}

// RevokeAPIKey implements repo.APIKeys.
func (s *Store) RevokeAPIKey(ctx context.Context, a repo.Actor, orgID, keyID uuid.UUID) error {
	err := s.DB.Tenant(ctx, db.Scope{OrgID: orgID}, func(q *dbq.Queries) error {
		n, err := q.RevokeAPIKey(ctx, dbq.RevokeAPIKeyParams{ID: keyID, OrgID: orgID, RevokedBy: actorUser(a)})
		if err != nil {
			return err
		}
		if n == 0 {
			return repo.ErrNotFound
		}
		return audit(ctx, q, orgID, a, "api_key.revoked", "api_key", keyID.String(), nil)
	})
	return mapErr(err)
}

// LookupAPIKey implements repo.APIKeys.
func (s *Store) LookupAPIKey(ctx context.Context, prefix string) (repo.APIKey, error) {
	var ref dbq.ResolveAPIKeyRow
	if err := s.DB.Global(ctx, func(q *dbq.Queries) error {
		var err error
		ref, err = q.ResolveAPIKey(ctx, prefix)
		return err
	}); err != nil {
		return repo.APIKey{}, mapErr(err)
	}
	var out repo.APIKey
	err := s.DB.Tenant(ctx, db.Scope{OrgID: ref.OrgID}, func(q *dbq.Queries) error {
		k, err := q.GetAPIKey(ctx, ref.ID)
		out = toAPIKey(k)
		return err
	})
	return out, mapErr(err)
}

// TouchAPIKey implements repo.APIKeys.
func (s *Store) TouchAPIKey(ctx context.Context, orgID, keyID uuid.UUID) error {
	return mapErr(s.DB.Tenant(ctx, db.Scope{OrgID: orgID}, func(q *dbq.Queries) error {
		return q.TouchAPIKey(ctx, keyID)
	}))
}
