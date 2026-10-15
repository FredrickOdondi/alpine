package ratelimit

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/alpine-dev/alpine/packages/shared/llm"
)

// Limits for one call scope. Zero means unlimited.
type Limits struct {
	GlobalConcurrency    int // whole gateway (protects the proxy / provider account)
	OrgConcurrency       int // concurrent LLM calls per org
	OrgRequestsPerMinute int
	OrgTokensPerMinute   int
	RepoRequestsPerMin   int
}

// LimitsProvider returns the effective limits for an org/repo (org_limits / repos rows merged
// over platform defaults).
type LimitsProvider interface {
	Limits(ctx context.Context, orgID, repoID string) (Limits, error)
}

// StaticLimits returns the same limits for everyone.
type StaticLimits Limits

// Limits implements LimitsProvider.
func (s StaticLimits) Limits(context.Context, string, string) (Limits, error) { return Limits(s), nil }

// Limiter is the Redis-backed llm.Limiter.
//
// Fairness: every org is capped by its own concurrency semaphore and token buckets, and the
// shared gateway capacity is a global semaphore. Because an org can never hold more than its cap
// of the global pool, a noisy org cannot starve others; waiting callers poll with jitter so no
// tenant is systematically favoured. (Workflow-level fairness — parallel tickets per org/repo and
// priority task queues — is handled in the orchestrator.)
type Limiter struct {
	Redis    *redis.Client
	Provider LimitsProvider
	Lease    time.Duration // max duration of one LLM call slot (default 5m)
	MaxWait  time.Duration // give up with llm.ErrRateLimited after this (default 2m)
	Poll     time.Duration // max sleep between attempts (default 250ms)
	Prefix   string        // key prefix (default "alpine:rl")
	Now      func() time.Time
	Sleep    func(ctx context.Context, d time.Duration) error

	once sync.Once
}

// defaults fills unset fields exactly once (Limiter is shared by concurrent callers).
func (l *Limiter) defaults() { l.once.Do(l.applyDefaults) }

func (l *Limiter) applyDefaults() {
	if l.Lease <= 0 {
		l.Lease = 5 * time.Minute
	}
	if l.MaxWait <= 0 {
		l.MaxWait = 2 * time.Minute
	}
	if l.Poll <= 0 {
		l.Poll = 250 * time.Millisecond
	}
	if l.Prefix == "" {
		l.Prefix = "alpine:rl"
	}
	if l.Now == nil {
		l.Now = time.Now
	}
	if l.Sleep == nil {
		l.Sleep = func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		}
	}
}

// Acquire implements llm.Limiter.
func (l *Limiter) Acquire(ctx context.Context, req llm.LimitRequest) (func(), error) {
	l.defaults()
	lim, err := l.Provider.Limits(ctx, req.OrgID, req.RepoID)
	if err != nil {
		return nil, fmt.Errorf("ratelimit: load limits: %w", err)
	}
	sem := Semaphore{R: l.Redis, Now: l.Now}
	bk := Buckets{R: l.Redis, Now: l.Now}
	holder := uuid.NewString()
	globalKey := l.Prefix + ":sem:llm:global"
	orgKey := l.Prefix + ":sem:llm:org:" + req.OrgID

	var buckets []Bucket
	if lim.OrgRequestsPerMinute > 0 {
		buckets = append(buckets, PerMinute(l.Prefix+":tb:rpm:org:"+req.OrgID, lim.OrgRequestsPerMinute, 1))
	}
	if lim.OrgTokensPerMinute > 0 && req.Tokens > 0 {
		buckets = append(buckets, PerMinute(l.Prefix+":tb:tpm:org:"+req.OrgID, lim.OrgTokensPerMinute, float64(req.Tokens)))
	}
	if lim.RepoRequestsPerMin > 0 && req.RepoID != "" {
		buckets = append(buckets, PerMinute(l.Prefix+":tb:rpm:repo:"+req.RepoID, lim.RepoRequestsPerMin, 1))
	}

	deadline := l.Now().Add(l.MaxWait)
	for {
		wait := l.Poll
		okOrg, err := sem.TryAcquire(ctx, orgKey, holder, lim.OrgConcurrency, l.Lease)
		if err != nil {
			return nil, err
		}
		if okOrg {
			okGlobal, err := sem.TryAcquire(ctx, globalKey, holder, lim.GlobalConcurrency, l.Lease)
			if err != nil {
				_ = sem.Release(context.WithoutCancel(ctx), orgKey, holder)
				return nil, err
			}
			if okGlobal {
				okBuckets, bw, err := bk.TryTake(ctx, buckets...)
				if err != nil {
					l.release(ctx, sem, holder, orgKey, globalKey)
					return nil, err
				}
				if okBuckets {
					return func() { l.release(ctx, sem, holder, orgKey, globalKey) }, nil
				}
				if bw > 0 && bw < wait {
					wait = bw
				} else if bw > wait {
					wait = min(bw, 5*l.Poll)
				}
			}
			l.release(ctx, sem, holder, orgKey, globalKey)
		}
		if !l.Now().Before(deadline) {
			return nil, llm.ErrRateLimited
		}
		// Jitter (50–100% of wait) so contending tenants interleave instead of thundering.
		jittered := time.Duration(float64(wait) * (0.5 + rand.Float64()/2))
		if err := l.Sleep(ctx, jittered); err != nil {
			return nil, err
		}
	}
}

func (l *Limiter) release(ctx context.Context, sem Semaphore, holder string, keys ...string) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	for _, k := range keys {
		_ = sem.Release(rctx, k, holder)
	}
}
