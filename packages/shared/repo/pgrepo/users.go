package pgrepo

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/alpine-dev/alpine/packages/shared/db/dbq"
	"github.com/alpine-dev/alpine/packages/shared/repo"
)

func toUser(u dbq.User) repo.User {
	return repo.User{
		ID: u.ID, Email: str(u.Email), EmailVerified: u.EmailVerified, Name: str(u.Name),
		AvatarURL: str(u.AvatarUrl), GitHubLogin: str(u.GithubLogin), CreatedAt: u.CreatedAt, LastLoginAt: u.LastLoginAt,
	}
}

func toIdentity(i dbq.UserIdentity) repo.Identity {
	return repo.Identity{
		ID: i.ID, UserID: i.UserID, Provider: i.Provider, Subject: i.Subject, Email: str(i.Email),
		EmailVerified: i.EmailVerified, HostedDomain: str(i.HostedDomain), Login: str(i.Login),
		CreatedAt: i.CreatedAt, LastLoginAt: i.LastLoginAt,
	}
}

// GetUser implements repo.Users.
func (s *Store) GetUser(ctx context.Context, id uuid.UUID) (repo.User, error) {
	var out repo.User
	err := s.DB.Global(ctx, func(q *dbq.Queries) error {
		u, err := q.GetUser(ctx, id)
		out = toUser(u)
		return err
	})
	return out, mapErr(err)
}

// GetUserByVerifiedEmail implements repo.Users.
func (s *Store) GetUserByVerifiedEmail(ctx context.Context, email string) (repo.User, error) {
	var out repo.User
	err := s.DB.Global(ctx, func(q *dbq.Queries) error {
		u, err := q.GetUserByVerifiedEmail(ctx, email)
		out = toUser(u)
		return err
	})
	return out, mapErr(err)
}

func ghID(ext repo.ExternalIdentity) *int64 {
	if ext.Provider == "github" && ext.GitHubID != 0 {
		return &ext.GitHubID
	}
	return nil
}

func ghLogin(ext repo.ExternalIdentity) *string {
	if ext.Provider == "github" {
		return strPtr(ext.Login)
	}
	return nil
}

func touchParams(u uuid.UUID, ext repo.ExternalIdentity) dbq.UpdateUserOnLoginParams {
	return dbq.UpdateUserOnLoginParams{
		ID: u, Name: strPtr(ext.Name), AvatarUrl: strPtr(ext.AvatarURL), Email: strPtr(ext.Email),
		EmailVerified: ext.EmailVerified, GithubID: ghID(ext), GithubLogin: ghLogin(ext),
	}
}

// ResolveLogin implements repo.Users.
func (s *Store) ResolveLogin(ctx context.Context, ext repo.ExternalIdentity, allowCreate bool) (repo.User, repo.LoginOutcome, error) {
	var (
		user    repo.User
		outcome repo.LoginOutcome
	)
	err := s.DB.Global(ctx, func(q *dbq.Queries) error {
		outcome = repo.LoginOutcome{}
		// 1. Known identity.
		id, err := q.GetIdentity(ctx, dbq.GetIdentityParams{Provider: ext.Provider, Subject: ext.Subject})
		if err == nil {
			if _, err := q.GetUser(ctx, id.UserID); err != nil {
				if errors.Is(mapErr(err), repo.ErrNotFound) {
					return repo.ErrUserDisabled
				}
				return err
			}
			if err := q.TouchIdentity(ctx, dbq.TouchIdentityParams{
				ID: id.ID, Email: strPtr(ext.Email), EmailVerified: ext.EmailVerified,
				HostedDomain: strPtr(ext.HostedDomain), Login: strPtr(ext.Login),
			}); err != nil {
				return err
			}
			u, err := q.UpdateUserOnLogin(ctx, touchParams(id.UserID, ext))
			if err != nil {
				return err
			}
			user = toUser(u)
			return nil
		}
		if !errors.Is(mapErr(err), repo.ErrNotFound) {
			return err
		}
		// 2. Account linking by VERIFIED email on both sides.
		var target *dbq.User
		if ext.Email != "" && ext.EmailVerified {
			u, err := q.GetUserByVerifiedEmail(ctx, ext.Email)
			switch {
			case err == nil:
				target = &u
				outcome.Linked = true
			case !errors.Is(mapErr(err), repo.ErrNotFound):
				return err
			}
		}
		// 3. New user.
		if target == nil {
			if !allowCreate {
				return repo.ErrNotFound
			}
			u, err := q.CreateUser(ctx, dbq.CreateUserParams{
				Email: strPtr(ext.Email), EmailVerified: ext.EmailVerified && ext.Email != "",
				Name: strPtr(ext.Name), AvatarUrl: strPtr(ext.AvatarURL), GithubID: ghID(ext), GithubLogin: ghLogin(ext),
			})
			if err != nil {
				return err
			}
			target = &u
			outcome.Created = true
		}
		if _, err := q.CreateIdentity(ctx, dbq.CreateIdentityParams{
			UserID: target.ID, Provider: ext.Provider, Subject: ext.Subject, Email: strPtr(ext.Email),
			EmailVerified: ext.EmailVerified, HostedDomain: strPtr(ext.HostedDomain), Login: strPtr(ext.Login),
		}); err != nil {
			return err
		}
		u, err := q.UpdateUserOnLogin(ctx, touchParams(target.ID, ext))
		if err != nil {
			return err
		}
		user = toUser(u)
		return nil
	})
	if err != nil {
		err = mapErr(err)
		if errors.Is(err, repo.ErrConflict) {
			// Lost a race with a concurrent first login (or the user already has another identity
			// for this provider): the caller can retry once.
			return repo.User{}, repo.LoginOutcome{}, err
		}
		return repo.User{}, repo.LoginOutcome{}, err
	}
	return user, outcome, nil
}

// LinkIdentity implements repo.Users.
func (s *Store) LinkIdentity(ctx context.Context, userID uuid.UUID, ext repo.ExternalIdentity) (repo.Identity, error) {
	var out repo.Identity
	err := s.DB.Global(ctx, func(q *dbq.Queries) error {
		existing, err := q.GetIdentity(ctx, dbq.GetIdentityParams{Provider: ext.Provider, Subject: ext.Subject})
		if err == nil {
			if existing.UserID != userID {
				return repo.ErrIdentityTaken
			}
			out = toIdentity(existing)
			return nil
		}
		if !errors.Is(mapErr(err), repo.ErrNotFound) {
			return err
		}
		if _, err := q.GetUser(ctx, userID); err != nil {
			return err
		}
		id, err := q.CreateIdentity(ctx, dbq.CreateIdentityParams{
			UserID: userID, Provider: ext.Provider, Subject: ext.Subject, Email: strPtr(ext.Email),
			EmailVerified: ext.EmailVerified, HostedDomain: strPtr(ext.HostedDomain), Login: strPtr(ext.Login),
		})
		if err != nil {
			return err
		}
		if ext.Provider == "github" {
			if _, err := q.UpdateUserOnLogin(ctx, dbq.UpdateUserOnLoginParams{ID: userID, GithubID: ghID(ext), GithubLogin: ghLogin(ext)}); err != nil {
				return err
			}
		}
		out = toIdentity(id)
		return nil
	})
	return out, mapErr(err)
}

// ListIdentities implements repo.Users.
func (s *Store) ListIdentities(ctx context.Context, userID uuid.UUID) ([]repo.Identity, error) {
	var out []repo.Identity
	err := s.DB.Global(ctx, func(q *dbq.Queries) error {
		rows, err := q.ListIdentitiesForUser(ctx, userID)
		for _, r := range rows {
			out = append(out, toIdentity(r))
		}
		return err
	})
	return out, mapErr(err)
}
