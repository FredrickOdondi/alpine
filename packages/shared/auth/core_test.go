package auth_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/alpine-dev/alpine/packages/shared/auth"
	"github.com/alpine-dev/alpine/packages/shared/db/dbq"
	"github.com/alpine-dev/alpine/packages/shared/db/dbtest"
)

func redisClient(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	mr := miniredis.RunT(t)
	return mr, redis.NewClient(&redis.Options{Addr: mr.Addr()})
}

var secret = []byte("0123456789abcdef0123456789abcdef")

func TestSessions(t *testing.T) {
	ctx := context.Background()
	mr, rdb := redisClient(t)
	now := time.Now()
	st := &auth.SessionStore{Redis: rdb, Secret: secret, IdleTTL: time.Hour, AbsoluteTTL: 24 * time.Hour, Now: func() time.Time { return now }}
	user, org := uuid.New(), uuid.New()

	s1, raw1, err := st.Create(ctx, user, org, "google")
	require.NoError(t, err)
	require.NotEmpty(t, s1.CSRFToken)
	for _, k := range mr.Keys() {
		require.NotContains(t, k, raw1, "raw session id must never be a Redis key")
	}
	got, err := st.Get(ctx, raw1)
	require.NoError(t, err)
	require.Equal(t, user, got.UserID)
	require.Equal(t, org, got.ActiveOrgID)

	_, err = st.Get(ctx, raw1+"x")
	require.ErrorIs(t, err, auth.ErrNoSession)

	// Idle expiry.
	mr.FastForward(2 * time.Hour)
	_, err = st.Get(ctx, raw1)
	require.ErrorIs(t, err, auth.ErrNoSession)

	// Absolute expiry even when active.
	_, raw2, err := st.Create(ctx, user, org, "google")
	require.NoError(t, err)
	now = now.Add(25 * time.Hour)
	_, err = st.Get(ctx, raw2)
	require.ErrorIs(t, err, auth.ErrNoSession)
	now = time.Now()

	// Logout everywhere.
	_, a, _ := st.Create(ctx, user, org, "google")
	_, b, _ := st.Create(ctx, user, org, "github")
	_, other, _ := st.Create(ctx, uuid.New(), org, "google")
	n, err := st.DeleteAllForUser(ctx, user)
	require.NoError(t, err)
	require.GreaterOrEqual(t, n, 2)
	for _, raw := range []string{a, b} {
		_, err := st.Get(ctx, raw)
		require.ErrorIs(t, err, auth.ErrNoSession)
	}
	_, err = st.Get(ctx, other)
	require.NoError(t, err)

	require.NoError(t, st.Delete(ctx, other))
	_, err = st.Get(ctx, other)
	require.ErrorIs(t, err, auth.ErrNoSession)
}

func TestRateLimitAndLockout(t *testing.T) {
	ctx := context.Background()
	mr, rdb := redisClient(t)
	rl := &auth.RateLimiter{Redis: rdb, Secret: secret}
	for i := 0; i < 3; i++ {
		ok, _, err := rl.Allow(ctx, "b", "1.2.3.4", 3, time.Minute)
		require.NoError(t, err)
		require.True(t, ok)
	}
	ok, retry, err := rl.Allow(ctx, "b", "1.2.3.4", 3, time.Minute)
	require.NoError(t, err)
	require.False(t, ok)
	require.Positive(t, retry)
	ok, _, _ = rl.Allow(ctx, "b", "5.6.7.8", 3, time.Minute)
	require.True(t, ok, "limits are per subject")
	for _, k := range mr.Keys() {
		require.False(t, strings.Contains(k, "1.2.3.4"), "no raw IPs in Redis keys")
	}

	lo := &auth.Lockout{Redis: rdb, Secret: secret, Threshold: 3, Base: time.Minute, Max: 10 * time.Minute}
	var d time.Duration
	for i := 0; i < 2; i++ {
		d, err = lo.Fail(ctx, "x", "ip")
		require.NoError(t, err)
		require.Zero(t, d)
	}
	d, _ = lo.Fail(ctx, "x", "ip")
	require.Equal(t, time.Minute, d)
	d, _ = lo.Fail(ctx, "x", "ip")
	require.Equal(t, 2*time.Minute, d, "exponential backoff")
	for i := 0; i < 10; i++ {
		d, _ = lo.Fail(ctx, "x", "ip")
	}
	require.Equal(t, 10*time.Minute, d, "capped")
	left, err := lo.Check(ctx, "x", "ip")
	require.NoError(t, err)
	require.Positive(t, left)
	require.NoError(t, lo.Reset(ctx, "x", "ip"))
	left, _ = lo.Check(ctx, "x", "ip")
	require.Zero(t, left)
}

func TestAPIKeyFormat(t *testing.T) {
	tok, prefix, sec := auth.GenerateAPIKey()
	require.True(t, strings.HasPrefix(tok, "alp_"+prefix+"_"))
	p, s, err := auth.ParseAPIKey(tok)
	require.NoError(t, err)
	require.Equal(t, prefix, p)
	require.Equal(t, sec, s)
	pepper := []byte("pepper-pepper-pepper-pepper-pepper")
	h := auth.HashAPIKeySecret(pepper, sec)
	require.True(t, auth.VerifyAPIKeySecret(pepper, sec, h))
	require.False(t, auth.VerifyAPIKeySecret([]byte("other-pepper-other-pepper-other!!"), sec, h))
	for _, bad := range []string{"", "alp_", "alp_short_x", "ghp_" + tok[4:], "alp_" + prefix + "_SHOUTING" + sec} {
		_, _, err := auth.ParseAPIKey(bad)
		require.Error(t, err, bad)
	}
}

func TestRBAC(t *testing.T) {
	require.True(t, auth.Can(auth.RoleOwner, auth.PermOrgDelete))
	require.False(t, auth.Can(auth.RoleAdmin, auth.PermOrgDelete))
	require.True(t, auth.Can(auth.RoleAdmin, auth.PermMembersInvite))
	require.False(t, auth.Can(auth.RoleMember, auth.PermMembersInvite))
	require.False(t, auth.Can(auth.RoleViewer, auth.PermTicketsWrite))
	require.True(t, auth.CanAssignRole(auth.RoleOwner, auth.RoleOwner))
	require.False(t, auth.CanAssignRole(auth.RoleAdmin, auth.RoleOwner))
	require.False(t, auth.CanAssignRole(auth.RoleMember, auth.RoleViewer))

	org := uuid.New()
	key := &auth.Principal{Kind: auth.KindAPIKey, OrgID: org, Role: auth.RoleAdmin, Scopes: []string{"tickets:read"}}
	require.True(t, key.Can(auth.PermTicketsRead))
	require.False(t, key.Can(auth.PermTicketsWrite), "scope limits even an admin key")
	require.False(t, key.Can(auth.PermMembersInvite))
	viewerKey := &auth.Principal{Kind: auth.KindAPIKey, OrgID: org, Role: auth.RoleViewer, Scopes: []string{"tickets:write"}}
	require.False(t, viewerKey.Can(auth.PermTicketsWrite), "role limits even a broad scope")
	noOrg := &auth.Principal{Kind: auth.KindSession, Role: auth.RoleOwner}
	require.False(t, noOrg.Can(auth.PermOrgRead))
}

// TestPolicyMatchesDB keeps the Go policy and the role_permissions table identical.
func TestPolicyMatchesDB(t *testing.T) {
	d, _ := dbtest.Open(t)
	ctx := context.Background()
	var rows []dbq.RolePermission
	require.NoError(t, d.Global(ctx, func(q *dbq.Queries) error {
		var err error
		rows, err = q.ListRolePermissions(ctx)
		return err
	}))
	fromDB := map[string]bool{}
	for _, r := range rows {
		fromDB[r.Role+"|"+r.Permission] = true
	}
	fromGo := map[string]bool{}
	for role, perms := range auth.Policy {
		for p := range perms {
			fromGo[string(role)+"|"+string(p)] = true
		}
	}
	require.Equal(t, fromGo, fromDB)
}

func TestSafeRedirectAndMasking(t *testing.T) {
	for in, want := range map[string]string{
		"/board": "/board", "": "/", "https://evil.com": "/", "//evil.com": "/", "/\\evil.com": "/", "/a\r\nb": "/",
	} {
		require.Equal(t, want, auth.SafeRedirectPath(in), in)
	}
	require.Equal(t, "a***@e***.com", auth.MaskEmail("alice@example.com"))
	require.Equal(t, "***", auth.MaskEmail("nope"))
}
