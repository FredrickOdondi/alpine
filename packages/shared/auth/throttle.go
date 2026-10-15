package auth

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// RateLimiter is a Redis fixed-window counter for auth endpoints (per IP, per email, ...).
// Keys are HMAC'd so no IPs or emails are stored in Redis.
type RateLimiter struct {
	Redis  redis.UniversalClient
	Secret []byte
	Prefix string
}

var incrWithTTL = redis.NewScript(`
local n = redis.call('INCR', KEYS[1])
if n == 1 then redis.call('PEXPIRE', KEYS[1], ARGV[1]) end
return {n, redis.call('PTTL', KEYS[1])}
`)

// Allow counts one event for (bucket, subject) and reports whether it is within limit per window.
// retryAfter is set when not allowed.
func (l *RateLimiter) Allow(ctx context.Context, bucket, subject string, limit int, window time.Duration) (bool, time.Duration, error) {
	key := l.Prefix + "rl:" + bucket + ":" + keyed(l.Secret, "rl", subject)
	res, err := incrWithTTL.Run(ctx, l.Redis, []string{key}, window.Milliseconds()).Int64Slice()
	if err != nil {
		return false, 0, fmt.Errorf("auth: rate limit: %w", err)
	}
	if res[0] > int64(limit) {
		return false, time.Duration(max(res[1], 0)) * time.Millisecond, nil
	}
	return true, 0, nil
}

// Lockout implements exponential backoff after repeated failures (bad magic-link tokens, invalid
// API keys, failed invitation accepts, failed OAuth callbacks). After Threshold failures within
// Window the subject is locked for Base * 2^(failures-Threshold), capped at Max.
type Lockout struct {
	Redis     redis.UniversalClient
	Secret    []byte
	Prefix    string
	Threshold int           // default 5
	Window    time.Duration // default 15m
	Base      time.Duration // default 1m
	Max       time.Duration // default 1h
}

func (l *Lockout) defaults() (int, time.Duration, time.Duration, time.Duration) {
	th, w, b, m := l.Threshold, l.Window, l.Base, l.Max
	if th <= 0 {
		th = 5
	}
	if w <= 0 {
		w = 15 * time.Minute
	}
	if b <= 0 {
		b = time.Minute
	}
	if m <= 0 {
		m = time.Hour
	}
	return th, w, b, m
}

func (l *Lockout) keys(bucket, subject string) (fails, lock string) {
	h := keyed(l.Secret, "lock", subject)
	return l.Prefix + "fail:" + bucket + ":" + h, l.Prefix + "lock:" + bucket + ":" + h
}

// Check returns how long the subject remains locked (0 if not locked).
func (l *Lockout) Check(ctx context.Context, bucket, subject string) (time.Duration, error) {
	_, lock := l.keys(bucket, subject)
	ttl, err := l.Redis.PTTL(ctx, lock).Result()
	if err != nil {
		return 0, fmt.Errorf("auth: lockout: %w", err)
	}
	if ttl < 0 {
		return 0, nil
	}
	return ttl, nil
}

// Fail records a failure and returns the lock duration now in force (0 if below threshold).
func (l *Lockout) Fail(ctx context.Context, bucket, subject string) (time.Duration, error) {
	th, window, base, maxLock := l.defaults()
	fails, lock := l.keys(bucket, subject)
	res, err := incrWithTTL.Run(ctx, l.Redis, []string{fails}, window.Milliseconds()).Int64Slice()
	if err != nil {
		return 0, fmt.Errorf("auth: lockout: %w", err)
	}
	n := int(res[0])
	if n < th {
		return 0, nil
	}
	d := time.Duration(float64(base) * math.Pow(2, float64(n-th)))
	if d > maxLock || d <= 0 {
		d = maxLock
	}
	// Keep the failure counter alive at least as long as the lock so backoff keeps growing.
	pipe := l.Redis.TxPipeline()
	pipe.Set(ctx, lock, strconv.Itoa(n), d)
	pipe.PExpire(ctx, fails, max(window, d*2))
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, fmt.Errorf("auth: lockout: %w", err)
	}
	return d, nil
}

// Reset clears failures after a success.
func (l *Lockout) Reset(ctx context.Context, bucket, subject string) error {
	fails, lock := l.keys(bucket, subject)
	return l.Redis.Del(ctx, fails, lock).Err()
}
