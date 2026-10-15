package llm

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestRedisIdempotencyIsOrgScoped(t *testing.T) {
	mr := miniredis.RunT(t)
	s := &RedisIdempotency{Redis: redis.NewClient(&redis.Options{Addr: mr.Addr()})}
	ctx := context.Background()
	require.NoError(t, s.Put(ctx, "org-a", "wf/1/a", &Response{Content: "x", Model: "gpt-5"}))
	r, ok, err := s.Get(ctx, "org-a", "wf/1/a")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "x", r.Content)
	_, ok, err = s.Get(ctx, "org-b", "wf/1/a")
	require.NoError(t, err)
	require.False(t, ok)
}
