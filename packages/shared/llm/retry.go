package llm

import (
	"context"
	"math/rand/v2"
	"time"
)

// RetryPolicy configures per-model retries. Delay for attempt n (1-based) is
// full-jitter exponential: rand[0, min(MaxDelay, BaseDelay*Multiplier^(n-1))], but never less
// than the provider's Retry-After.
type RetryPolicy struct {
	MaxAttempts       int           // per model, including the first attempt
	BaseDelay         time.Duration // first backoff ceiling
	MaxDelay          time.Duration // backoff cap
	Multiplier        float64
	AttemptTimeout    time.Duration // per-attempt deadline
	MaxRetryAfterWait time.Duration // if Retry-After exceeds this, fall back to the next model instead
}

// DefaultRetryPolicy is a sensible default for OpenAI.
var DefaultRetryPolicy = RetryPolicy{
	MaxAttempts:       4,
	BaseDelay:         500 * time.Millisecond,
	MaxDelay:          20 * time.Second,
	Multiplier:        2,
	AttemptTimeout:    120 * time.Second,
	MaxRetryAfterWait: 60 * time.Second,
}

func (p RetryPolicy) withDefaults() RetryPolicy {
	d := DefaultRetryPolicy
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = d.MaxAttempts
	}
	if p.BaseDelay <= 0 {
		p.BaseDelay = d.BaseDelay
	}
	if p.MaxDelay <= 0 {
		p.MaxDelay = d.MaxDelay
	}
	if p.Multiplier < 1 {
		p.Multiplier = d.Multiplier
	}
	if p.AttemptTimeout <= 0 {
		p.AttemptTimeout = d.AttemptTimeout
	}
	if p.MaxRetryAfterWait <= 0 {
		p.MaxRetryAfterWait = d.MaxRetryAfterWait
	}
	return p
}

// Backoff returns the delay before retry number attempt (attempt = number of failures so far).
func (p RetryPolicy) Backoff(attempt int, retryAfter time.Duration, rnd func() float64) time.Duration {
	p = p.withDefaults()
	ceiling := float64(p.BaseDelay)
	for i := 1; i < attempt; i++ {
		ceiling *= p.Multiplier
		if ceiling >= float64(p.MaxDelay) {
			ceiling = float64(p.MaxDelay)
			break
		}
	}
	if rnd == nil {
		rnd = rand.Float64
	}
	d := time.Duration(rnd() * ceiling)
	if retryAfter > d {
		d = retryAfter
	}
	return d
}

// sleepCtx sleeps or returns early with ctx.Err().
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
