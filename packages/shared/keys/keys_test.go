package keys

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alpine-dev/alpine/packages/shared/llm"
)

func testKEK(t *testing.T) *LocalKEK {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	k, err := NewLocalKEK("local-1", base64.StdEncoding.EncodeToString(b))
	require.NoError(t, err)
	return k
}

type memStore map[string]*Envelope

func (m memStore) ActiveModelKey(_ context.Context, orgID, _ string) (*Envelope, error) {
	return m[orgID], nil
}

func TestEnvelopeRoundTripAndTenantBinding(t *testing.T) {
	ctx := context.Background()
	kek := testKEK(t)
	env, err := Seal(ctx, kek, "org-a", "model_key:openai", "sk-proj-customerkey1234")
	require.NoError(t, err)
	require.Equal(t, "1234", env.Last4)
	require.NotContains(t, string(env.Ciphertext), "customerkey")

	got, err := Open(ctx, kek, "org-a", "model_key:openai", env)
	require.NoError(t, err)
	require.Equal(t, "sk-proj-customerkey1234", got.Reveal())
	require.Equal(t, "[REDACTED]", fmt.Sprint(got))

	_, err = Open(ctx, kek, "org-b", "model_key:openai", env)
	require.Error(t, err, "an envelope copied to another org must not decrypt")
}

func TestResolver(t *testing.T) {
	ctx := context.Background()
	kek := testKEK(t)
	env, _ := Seal(ctx, kek, "org-a", "model_key:openai", "sk-proj-customerkey1234")
	r := &Resolver{Store: memStore{"org-a": &env}, KEK: kek, Provider: "openai"}
	k, byok, err := r.Resolve(ctx, "org-a")
	require.NoError(t, err)
	require.True(t, byok)
	require.Equal(t, "sk-proj-customerkey1234", k.Reveal())

	_, _, err = r.Resolve(ctx, "org-b")
	require.ErrorIs(t, err, llm.ErrNoKey)

	r.AllowPlatformKey = true
	k, byok, err = r.Resolve(ctx, "org-b")
	require.NoError(t, err)
	require.False(t, byok)
	require.Empty(t, k)
}
