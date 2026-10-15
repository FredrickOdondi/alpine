package pgrepo_test

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/alpine-dev/alpine/packages/shared/db/dbtest"
	"github.com/alpine-dev/alpine/packages/shared/llm"
	"github.com/alpine-dev/alpine/packages/shared/repo"
	"github.com/alpine-dev/alpine/packages/shared/repo/pgrepo"
)

func newStore(t *testing.T) *pgrepo.Store {
	d, _ := dbtest.Open(t)
	return pgrepo.New(d)
}

func userActor(u repo.User) repo.Actor { return repo.Actor{Type: "user", ID: u.ID.String()} }

func TestLoginResolutionAndAccountLinking(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	google := repo.ExternalIdentity{Provider: "google", Subject: "g-123", Email: "Ada@Example.com", EmailVerified: true, Name: "Ada"}
	u1, out, err := s.ResolveLogin(ctx, google, true)
	require.NoError(t, err)
	require.True(t, out.Created)
	require.Equal(t, "ada@example.com", u1.Email)
	require.True(t, u1.EmailVerified)

	// Same Google subject -> same user, no new identity.
	u2, out, err := s.ResolveLogin(ctx, google, true)
	require.NoError(t, err)
	require.Equal(t, u1.ID, u2.ID)
	require.False(t, out.Created || out.Linked)

	// GitHub with the same VERIFIED email links to the existing user.
	gh := repo.ExternalIdentity{Provider: "github", Subject: "42", GitHubID: 42, Login: "ada", Email: "ada@example.com", EmailVerified: true}
	u3, out, err := s.ResolveLogin(ctx, gh, true)
	require.NoError(t, err)
	require.Equal(t, u1.ID, u3.ID)
	require.True(t, out.Linked)
	require.Equal(t, "ada", u3.GitHubLogin)

	// An UNVERIFIED email never links: it creates a separate account.
	evil := repo.ExternalIdentity{Provider: "github", Subject: "666", GitHubID: 666, Login: "mallory", Email: "ada@example.com", EmailVerified: false}
	u4, out, err := s.ResolveLogin(ctx, evil, true)
	require.NoError(t, err)
	require.NotEqual(t, u1.ID, u4.ID)
	require.True(t, out.Created)
	require.False(t, u4.EmailVerified)

	ids, err := s.ListIdentities(ctx, u1.ID)
	require.NoError(t, err)
	require.Len(t, ids, 2)

	// Sign-up disabled: unknown identity is rejected.
	_, _, err = s.ResolveLogin(ctx, repo.ExternalIdentity{Provider: "email", Subject: "new@example.com", Email: "new@example.com", EmailVerified: true}, false)
	require.ErrorIs(t, err, repo.ErrNotFound)

	// Explicit linking refuses an identity owned by someone else.
	_, err = s.LinkIdentity(ctx, u1.ID, evil)
	require.ErrorIs(t, err, repo.ErrIdentityTaken)
}

func TestOrgsMembersInvitationsAPIKeys(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	owner, _, err := s.ResolveLogin(ctx, repo.ExternalIdentity{Provider: "google", Subject: "o1", Email: "owner@acme.test", EmailVerified: true}, true)
	require.NoError(t, err)
	bob, _, err := s.ResolveLogin(ctx, repo.ExternalIdentity{Provider: "google", Subject: "b1", Email: "bob@acme.test", EmailVerified: true}, true)
	require.NoError(t, err)
	eve, _, err := s.ResolveLogin(ctx, repo.ExternalIdentity{Provider: "google", Subject: "e1", Email: "eve@evil.test", EmailVerified: true}, true)
	require.NoError(t, err)

	org, err := s.CreateOrg(ctx, userActor(owner), owner.ID, "acme-"+uuid.NewString()[:6], "Acme")
	require.NoError(t, err)
	other, err := s.CreateOrg(ctx, userActor(eve), eve.ID, "evil-"+uuid.NewString()[:6], "Evil")
	require.NoError(t, err)

	orgs, err := s.ListOrgsForUser(ctx, owner.ID)
	require.NoError(t, err)
	require.Len(t, orgs, 1)
	require.Equal(t, repo.Role("owner"), orgs[0].Role)

	// Invitation: wrong user cannot accept; right user can, once.
	tok := sha256.Sum256([]byte("tok-" + uuid.NewString()))
	inv, err := s.CreateInvitation(ctx, userActor(owner), org.ID, "Bob@Acme.test", "member", tok[:], time.Now().Add(72*time.Hour))
	require.NoError(t, err)
	require.Equal(t, "bob@acme.test", inv.Email)
	_, _, err = s.AcceptInvitation(ctx, userActor(eve), tok[:], eve)
	require.ErrorIs(t, err, repo.ErrInvitationMismatch)
	_, m, err := s.AcceptInvitation(ctx, userActor(bob), tok[:], bob)
	require.NoError(t, err)
	require.Equal(t, repo.Role("member"), m.Role)
	_, _, err = s.AcceptInvitation(ctx, userActor(bob), tok[:], bob)
	require.ErrorIs(t, err, repo.ErrInvitationInvalid)

	// Expired invitation.
	s.Now = func() time.Time { return time.Now().Add(100 * time.Hour) }
	tok2 := sha256.Sum256([]byte("tok2-" + uuid.NewString()))
	_, err = s.CreateInvitation(ctx, userActor(owner), org.ID, "eve@evil.test", "viewer", tok2[:], time.Now().Add(time.Hour))
	require.NoError(t, err)
	_, _, err = s.AcceptInvitation(ctx, userActor(eve), tok2[:], eve)
	require.ErrorIs(t, err, repo.ErrInvitationExpired)
	s.Now = nil

	members, err := s.ListMembers(ctx, org.ID)
	require.NoError(t, err)
	require.Len(t, members, 2)

	// Role changes and last-owner protection.
	_, err = s.UpdateMemberRole(ctx, userActor(owner), org.ID, owner.ID, "admin")
	require.ErrorIs(t, err, repo.ErrLastOwner)
	_, err = s.UpdateMemberRole(ctx, userActor(owner), org.ID, bob.ID, "admin")
	require.NoError(t, err)
	require.ErrorIs(t, s.RemoveMember(ctx, userActor(owner), other.ID, bob.ID), repo.ErrNotFound)

	// API keys: create, lookup by prefix, revoke; cross-org revoke is a no-op NotFound.
	k, err := s.CreateAPIKey(ctx, userActor(owner), repo.NewAPIKey{OrgID: org.ID, Name: "ci", Prefix: "abcdefabcdef",
		SecretHash: tok[:], Scopes: []string{"tickets:read"}, Role: "member"})
	require.NoError(t, err)
	got, err := s.LookupAPIKey(ctx, "abcdefabcdef")
	require.NoError(t, err)
	require.Equal(t, k.ID, got.ID)
	require.Equal(t, org.ID, got.OrgID)
	require.ErrorIs(t, s.RevokeAPIKey(ctx, userActor(eve), other.ID, k.ID), repo.ErrNotFound)
	require.NoError(t, s.TouchAPIKey(ctx, org.ID, k.ID))
	require.NoError(t, s.RevokeAPIKey(ctx, userActor(owner), org.ID, k.ID))
	_, err = s.LookupAPIKey(ctx, "abcdefabcdef")
	require.ErrorIs(t, err, repo.ErrNotFound)

	// Every mutation above is in the audit log of the right org only.
	entries, err := s.List(ctx, org.ID, repo.AuditFilter{Limit: 100})
	require.NoError(t, err)
	actions := map[string]int{}
	for _, e := range entries {
		require.Equal(t, org.ID, e.OrgID)
		actions[e.Action]++
	}
	for _, a := range []string{"org.created", "invitation.created", "invitation.accepted", "member.added",
		"member.role_changed", "api_key.created", "api_key.revoked"} {
		require.Positive(t, actions[a], a)
	}
	otherEntries, err := s.List(ctx, other.ID, repo.AuditFilter{})
	require.NoError(t, err)
	require.Len(t, otherEntries, 1) // only its own org.created

	require.NoError(t, s.DeleteOrg(ctx, userActor(owner), org.ID))
	_, err = s.GetOrg(ctx, org.ID)
	require.ErrorIs(t, err, repo.ErrNotFound)
}

func TestMeteringRecorderIsIdempotent(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	u, _, err := s.ResolveLogin(ctx, repo.ExternalIdentity{Provider: "google", Subject: "m1", Email: "m@x.test", EmailVerified: true}, true)
	require.NoError(t, err)
	org, err := s.CreateOrg(ctx, userActor(u), u.ID, "meter-"+uuid.NewString()[:6], "Meter")
	require.NoError(t, err)
	m := &pgrepo.Metering{DB: s.DB}
	rec := llm.CallRecord{
		Tenant: llm.Tenant{OrgID: org.ID.String()}, IdempotencyKey: "wf/act/1", Stage: llm.StageTriage,
		Provider: "openai", Model: "gpt-x", RequestedModel: "gpt-x", Status: "ok", Attempts: 1,
		Usage: llm.Usage{InputTokens: 100, OutputTokens: 20}, CostUSD: 0.5,
	}
	require.NoError(t, m.Record(ctx, rec))
	require.NoError(t, m.Record(ctx, rec))
	spend, err := m.OrgSpend(ctx, org.ID.String(), time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.InDelta(t, 0.5, spend.CostUSD, 1e-9)
	require.EqualValues(t, 120, spend.Tokens)
	env, err := m.ActiveModelKey(ctx, org.ID.String(), "openai")
	require.NoError(t, err)
	require.Nil(t, env)
	caps, err := m.OrgCaps(ctx, org.ID.String())
	require.NoError(t, err)
	require.Zero(t, caps.DailyCostUSD)
}
