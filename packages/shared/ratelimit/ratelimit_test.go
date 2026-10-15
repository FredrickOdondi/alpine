package ratelimit

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/alpine-dev/alpine/packages/shared/llm"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func setup(t *testing.T) (*redis.Client, *clock) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb, &clock{t: time.Unix(1_700_000_000, 0)}
}

func TestTokenBucketRefillsOverTime(t *testing.T) {
	rdb, c := setup(t)
	b := Buckets{R: rdb, Now: c.Now}
	ctx := context.Background()
	bucket := PerMinute("tb:org:a", 60, 1) // 1 per second, burst 60
	for i := 0; i < 60; i++ {
		ok, _, err := b.TryTake(ctx, bucket)
		require.NoError(t, err)
		require.True(t, ok, "burst %d", i)
	}
	ok, wait, err := b.TryTake(ctx, bucket)
	require.NoError(t, err)
	require.False(t, ok)
	require.Equal(t, time.Second, wait)
	c.Advance(time.Second)
	ok, _, err = b.TryTake(ctx, bucket)
	require.NoError(t, err)
	require.True(t, ok)
}

func TestMultiBucketIsAllOrNothing(t *testing.T) {
	rdb, c := setup(t)
	b := Buckets{R: rdb, Now: c.Now}
	ctx := context.Background()
	rpm := PerMinute("rpm", 100, 1)
	tpm := PerMinute("tpm", 1000, 800)
	ok, _, err := b.TryTake(ctx, rpm, tpm)
	require.NoError(t, err)
	require.True(t, ok)
	ok, wait, err := b.TryTake(ctx, rpm, tpm) // tpm has 200 left; needs 800
	require.NoError(t, err)
	require.False(t, ok)
	require.Equal(t, 36*time.Second, wait) // 600 tokens at 1000/60 per s
	// rpm must not have been charged by the failed attempt.
	v, err := rdb.HGet(ctx, "rpm", "tokens").Float64()
	require.NoError(t, err)
	require.Equal(t, 99.0, v)
}

func TestSemaphoreLeasesExpire(t *testing.T) {
	rdb, c := setup(t)
	s := Semaphore{R: rdb, Now: c.Now}
	ctx := context.Background()
	ok, err := s.TryAcquire(ctx, "sem", "a", 2, time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	ok, _ = s.TryAcquire(ctx, "sem", "b", 2, time.Minute)
	require.True(t, ok)
	ok, _ = s.TryAcquire(ctx, "sem", "c", 2, time.Minute)
	require.False(t, ok, "limit reached")
	ok, _ = s.TryAcquire(ctx, "sem", "a", 2, time.Minute)
	require.True(t, ok, "re-acquire by holder is idempotent")
	require.NoError(t, s.Release(ctx, "sem", "b"))
	ok, _ = s.TryAcquire(ctx, "sem", "c", 2, time.Minute)
	require.True(t, ok)
	// a crashed holder's lease expires.
	c.Advance(2 * time.Minute)
	ok, _ = s.Refresh(ctx, "sem", "a", time.Minute)
	require.False(t, ok, "expired lease cannot be refreshed")
	ok, _ = s.TryAcquire(ctx, "sem", "d", 2, time.Minute)
	require.True(t, ok)
}

func TestLimiterCapsPerOrgConcurrencyAndIsFair(t *testing.T) {
	rdb, _ := setup(t)
	l := &Limiter{
		Redis:    rdb,
		Provider: StaticLimits{OrgConcurrency: 2, GlobalConcurrency: 3},
		Poll:     2 * time.Millisecond,
		MaxWait:  2 * time.Second,
	}
	ctx := context.Background()
	var inflightA, maxA, inflightTotal, maxTotal atomic.Int64
	var bDone atomic.Int64
	run := func(org string, n int, wg *sync.WaitGroup) {
		defer wg.Done()
		for i := 0; i < n; i++ {
			rel, err := l.Acquire(ctx, llm.LimitRequest{OrgID: org})
			require.NoError(t, err)
			tot := inflightTotal.Add(1)
			for { // track max
				m := maxTotal.Load()
				if tot <= m || maxTotal.CompareAndSwap(m, tot) {
					break
				}
			}
			if org == "A" {
				v := inflightA.Add(1)
				for {
					m := maxA.Load()
					if v <= m || maxA.CompareAndSwap(m, v) {
						break
					}
				}
			}
			time.Sleep(3 * time.Millisecond)
			if org == "A" {
				inflightA.Add(-1)
			} else {
				bDone.Add(1)
			}
			inflightTotal.Add(-1)
			rel()
		}
	}
	var wg sync.WaitGroup
	// Org A floods with 8 workers; org B has a single worker.
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go run("A", 5, &wg)
	}
	wg.Add(1)
	go run("B", 5, &wg)
	wg.Wait()
	require.LessOrEqual(t, maxA.Load(), int64(2), "org A never exceeds its concurrency cap")
	require.LessOrEqual(t, maxTotal.Load(), int64(3), "global cap respected")
	require.Equal(t, int64(5), bDone.Load(), "org B is not starved by org A")
}

func TestLimiterRateLimitsAndGivesUp(t *testing.T) {
	rdb, c := setup(t)
	var slept time.Duration
	l := &Limiter{
		Redis:    rdb,
		Provider: StaticLimits{OrgRequestsPerMinute: 2},
		Now:      c.Now,
		MaxWait:  time.Second,
		Sleep: func(_ context.Context, d time.Duration) error {
			slept += d
			c.Advance(d)
			return nil
		},
	}
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		rel, err := l.Acquire(ctx, llm.LimitRequest{OrgID: "A"})
		require.NoError(t, err)
		rel()
	}
	_, err := l.Acquire(ctx, llm.LimitRequest{OrgID: "A"})
	require.ErrorIs(t, err, llm.ErrRateLimited)
	require.Greater(t, slept, time.Duration(0))
	// Another org has its own bucket.
	rel, err := l.Acquire(ctx, llm.LimitRequest{OrgID: "B"})
	require.NoError(t, err)
	rel()
}
