// Package ratelimit implements Redis-backed, multi-instance-safe tenant fairness primitives:
//
//   - Buckets: atomic multi-bucket token buckets (requests/min, tokens/min per org and per repo).
//   - Semaphore: lease-based counting semaphores (concurrent LLM calls per org, parallel tickets
//     per org/repo, global gateway concurrency). Leases expire, so a crashed worker can never
//     leak a slot forever.
//   - Limiter: the llm.Limiter used by the model gateway, combining both with a fair wait loop.
package ratelimit

import (
	"context"
	_ "embed"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Bucket is one token bucket in an atomic TryTake.
type Bucket struct {
	Key      string
	Capacity float64 // burst size
	PerSec   float64 // refill rate
	Cost     float64 // tokens to take
}

// PerMinute builds a bucket that allows n per minute with a burst of n.
func PerMinute(key string, n int, cost float64) Bucket {
	return Bucket{Key: key, Capacity: float64(n), PerSec: float64(n) / 60, Cost: cost}
}

var takeScript = redis.NewScript(`
local now = tonumber(ARGV[1])
local n = #KEYS
local state = {}
local maxwait = 0
for i = 1, n do
  local base = 1 + (i - 1) * 3
  local cap = tonumber(ARGV[base + 1])
  local rate = tonumber(ARGV[base + 2]) -- tokens per ms
  local cost = tonumber(ARGV[base + 3])
  if cost > cap then cost = cap end
  local v = redis.call('HMGET', KEYS[i], 'tokens', 'ts')
  local tokens = tonumber(v[1])
  local ts = tonumber(v[2])
  if tokens == nil then tokens = cap; ts = now end
  if now > ts then tokens = math.min(cap, tokens + (now - ts) * rate) end
  if tokens < cost then
    local w = math.ceil((cost - tokens) / rate)
    if w > maxwait then maxwait = w end
  end
  state[i] = {tokens, cost, cap, rate}
end
if maxwait > 0 then return {0, maxwait} end
for i = 1, n do
  local s = state[i]
  redis.call('HSET', KEYS[i], 'tokens', s[1] - s[2], 'ts', now)
  redis.call('PEXPIRE', KEYS[i], math.ceil(s[3] / s[4]) * 2 + 1000)
end
return {1, 0}
`)

// Buckets takes from several buckets atomically (all or nothing).
type Buckets struct {
	R   redis.Scripter
	Now func() time.Time
}

// TryTake returns ok, or the wait until enough tokens will be available.
func (b Buckets) TryTake(ctx context.Context, buckets ...Bucket) (bool, time.Duration, error) {
	if len(buckets) == 0 {
		return true, 0, nil
	}
	now := time.Now
	if b.Now != nil {
		now = b.Now
	}
	keys := make([]string, 0, len(buckets))
	args := []any{now().UnixMilli()}
	for _, bk := range buckets {
		if bk.Capacity <= 0 || bk.PerSec <= 0 {
			return false, 0, fmt.Errorf("ratelimit: bucket %s needs positive capacity and rate", bk.Key)
		}
		keys = append(keys, bk.Key)
		args = append(args, bk.Capacity, bk.PerSec/1000, bk.Cost)
	}
	res, err := takeScript.Run(ctx, b.R, keys, args...).Int64Slice()
	if err != nil {
		return false, 0, fmt.Errorf("ratelimit: take: %w", err)
	}
	if res[0] == 1 {
		return true, 0, nil
	}
	return false, time.Duration(res[1]) * time.Millisecond, nil
}

var acquireScript = redis.NewScript(`
local now = tonumber(ARGV[1])
local lease = tonumber(ARGV[2])
local limit = tonumber(ARGV[3])
local holder = ARGV[4]
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now)
if redis.call('ZSCORE', KEYS[1], holder) then
  redis.call('ZADD', KEYS[1], now + lease, holder)
  redis.call('PEXPIRE', KEYS[1], lease * 2)
  return 1
end
if redis.call('ZCARD', KEYS[1]) < limit then
  redis.call('ZADD', KEYS[1], now + lease, holder)
  redis.call('PEXPIRE', KEYS[1], lease * 2)
  return 1
end
return 0
`)

var refreshScript = redis.NewScript(`
local now = tonumber(ARGV[1])
local lease = tonumber(ARGV[2])
local s = redis.call('ZSCORE', KEYS[1], ARGV[3])
if s and tonumber(s) > now then
  redis.call('ZADD', KEYS[1], now + lease, ARGV[3])
  redis.call('PEXPIRE', KEYS[1], lease * 2)
  return 1
end
return 0
`)

// Semaphore is a lease-based counting semaphore stored in a Redis sorted set
// (member = holder id, score = lease expiry in ms).
type Semaphore struct {
	R   redis.Scripter
	Now func() time.Time
}

func (s Semaphore) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// TryAcquire takes a slot for holder (idempotent: re-acquiring refreshes the lease).
func (s Semaphore) TryAcquire(ctx context.Context, key, holder string, limit int, lease time.Duration) (bool, error) {
	if limit <= 0 {
		return true, nil // unlimited
	}
	res, err := acquireScript.Run(ctx, s.R, []string{key}, s.now().UnixMilli(), lease.Milliseconds(), limit, holder).Int64()
	if err != nil {
		return false, fmt.Errorf("ratelimit: acquire %s: %w", key, err)
	}
	return res == 1, nil
}

// Refresh extends holder's lease; false means the lease was lost (expired).
func (s Semaphore) Refresh(ctx context.Context, key, holder string, lease time.Duration) (bool, error) {
	res, err := refreshScript.Run(ctx, s.R, []string{key}, s.now().UnixMilli(), lease.Milliseconds(), holder).Int64()
	if err != nil {
		return false, fmt.Errorf("ratelimit: refresh %s: %w", key, err)
	}
	return res == 1, nil
}

// Release frees holder's slot.
func (s Semaphore) Release(ctx context.Context, key, holder string) error {
	r, ok := s.R.(redis.Cmdable)
	if !ok {
		return fmt.Errorf("ratelimit: release needs a redis.Cmdable")
	}
	return r.ZRem(ctx, key, holder).Err()
}

// Holders returns the number of live holders.
func (s Semaphore) Holders(ctx context.Context, key string) (int64, error) {
	r, ok := s.R.(redis.Cmdable)
	if !ok {
		return 0, fmt.Errorf("ratelimit: holders needs a redis.Cmdable")
	}
	return r.ZCount(ctx, key, strconv.FormatInt(s.now().UnixMilli(), 10), "+inf").Result()
}
