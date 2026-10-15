package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RetryPolicy controls retries of GitHub API calls.
//
// Retried: 5xx responses and transport errors (timeouts, resets) for
// idempotent calls, and rate-limit rejections (429, or 403 carrying
// Retry-After / X-RateLimit-Remaining: 0 / a "rate limit" message) for every
// call, since GitHub did not apply those. Non-idempotent creates (branch, PR,
// comment, check run) are not blindly retried on 5xx/timeouts; the
// operation re-checks for the created object first (see idempotency.go).
type RetryPolicy struct {
	// MaxAttempts is the total number of attempts including the first.
	// Default 4; 1 disables retries.
	MaxAttempts int
	// BaseDelay is the first backoff step; it doubles per attempt. Default 500ms.
	BaseDelay time.Duration
	// MaxDelay caps one exponential backoff step. Default 30s.
	MaxDelay time.Duration
	// MaxWait caps any single wait, including Retry-After and
	// X-RateLimit-Reset. If GitHub asks us to wait longer, the call fails
	// immediately with the rate-limit error so the caller (Temporal) can
	// reschedule instead of holding a worker. Default 2m.
	MaxWait time.Duration
}

// SecondaryRateLimitMinWait is GitHub's guidance for secondary rate limits
// without Retry-After: wait at least one minute.
const SecondaryRateLimitMinWait = time.Minute

func (p RetryPolicy) withDefaults() RetryPolicy {
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = 4
	}
	if p.BaseDelay <= 0 {
		p.BaseDelay = 500 * time.Millisecond
	}
	if p.MaxDelay <= 0 {
		p.MaxDelay = 30 * time.Second
	}
	if p.MaxWait <= 0 {
		p.MaxWait = 2 * time.Minute
	}
	return p
}

// sleepCtx is the default Sleep: waits d or until ctx is done.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// transport performs authenticated JSON calls with retries. It is shared by
// App (JWT-authenticated) and Client (installation-token-authenticated).
type transport struct {
	http  *http.Client
	base  string
	ua    string
	retry RetryPolicy
	now   func() time.Time
	sleep func(context.Context, time.Duration) error
	rand  func() float64
}

func newTransport(base string, hc *http.Client, ua string, rp RetryPolicy,
	now func() time.Time, sleep func(context.Context, time.Duration) error, rnd func() float64) *transport {
	if now == nil {
		now = time.Now
	}
	if sleep == nil {
		sleep = sleepCtx
	}
	if rnd == nil {
		rnd = rand.Float64
	}
	return &transport{http: orDefault(hc), base: base, ua: orString(ua, defaultUserAgent),
		retry: rp.withDefaults(), now: now, sleep: sleep, rand: rnd}
}

type retryMode int

const (
	// retrySafe: idempotent request; retry 5xx, transport errors and rate limits.
	retrySafe retryMode = iota
	// retryRateLimitOnly: non-idempotent create; only retry when GitHub
	// definitely rejected it (rate limited). Ambiguous failures are returned
	// to the caller, which re-checks before re-creating.
	retryRateLimitOnly
)

type call struct {
	method string
	path   string // relative to base, or an absolute same-origin URL (pagination)
	auth   func(context.Context) (string, error)
	in     any
	out    any
	mode   retryMode
}

// TransportError is a failure where no HTTP response was received (timeout,
// connection reset, ...). GitHub may or may not have applied the request.
type TransportError struct {
	Method, Path string
	Err          error
}

func (e *TransportError) Error() string {
	return fmt.Sprintf("github: %s %s: %v", e.Method, e.Path, e.Err)
}

func (e *TransportError) Unwrap() error { return e.Err }

// IsAmbiguous reports whether err leaves it unknown if GitHub applied the
// request: transport errors/timeouts and 5xx responses.
func IsAmbiguous(err error) bool {
	var te *TransportError
	if errors.As(err, &te) {
		return true
	}
	return StatusCode(err) >= 500
}

// IsRateLimited reports whether err is a GitHub primary or secondary rate-limit rejection.
func IsRateLimited(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.rateLimited()
}

func (t *transport) do(ctx context.Context, c call) (http.Header, error) {
	for attempt := 1; ; attempt++ {
		hdr, err := t.once(ctx, c)
		if err == nil {
			return hdr, nil
		}
		if ctx.Err() != nil || attempt >= t.retry.MaxAttempts {
			return nil, err
		}
		wait, ok := t.retryWait(err, c.mode, attempt)
		if !ok {
			return nil, err
		}
		if serr := t.sleep(ctx, wait); serr != nil {
			return nil, errors.Join(err, serr)
		}
	}
}

// retryWait decides whether err is retryable under mode and how long to wait.
func (t *transport) retryWait(err error, mode retryMode, attempt int) (time.Duration, bool) {
	var ae *APIError
	if errors.As(err, &ae) {
		if ae.rateLimited() {
			return t.capped(t.rateLimitWait(ae, attempt))
		}
		if ae.StatusCode >= 500 && mode == retrySafe {
			if d, ok := retryAfter(ae.header, t.now()); ok {
				return t.capped(d)
			}
			return t.backoff(attempt), true
		}
		return 0, false
	}
	var te *TransportError
	if errors.As(err, &te) && mode == retrySafe {
		return t.backoff(attempt), true
	}
	return 0, false
}

func (t *transport) capped(d time.Duration) (time.Duration, bool) {
	if d > t.retry.MaxWait {
		return 0, false
	}
	return max(d, 0), true
}

// backoff is exponential with "equal jitter": half fixed, half random, so the
// wait is never zero but concurrent clients still spread out.
func (t *transport) backoff(attempt int) time.Duration {
	d := t.retry.BaseDelay
	for i := 1; i < attempt && d < t.retry.MaxDelay; i++ {
		d *= 2
	}
	d = min(d, t.retry.MaxDelay)
	return d/2 + time.Duration(t.rand()*float64(d/2))
}

func (t *transport) rateLimitWait(ae *APIError, attempt int) time.Duration {
	now := t.now()
	if d, ok := retryAfter(ae.header, now); ok {
		return d
	}
	if ae.header.Get("X-RateLimit-Remaining") == "0" {
		if reset, err := strconv.ParseInt(ae.header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			return time.Unix(reset, 0).Sub(now) + time.Second // +1s for clock skew
		}
	}
	return max(t.backoff(attempt), SecondaryRateLimitMinWait)
}

// retryAfter parses Retry-After as delta-seconds or an HTTP date.
func retryAfter(h http.Header, now time.Time) (time.Duration, bool) {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second, true
	}
	if at, err := http.ParseTime(v); err == nil {
		return max(at.Sub(now), 0), true
	}
	return 0, false
}

func (t *transport) once(ctx context.Context, c call) (http.Header, error) {
	auth, err := c.auth(ctx)
	if err != nil {
		return nil, fmt.Errorf("github: auth: %w", err)
	}
	target := t.base + c.path
	if strings.HasPrefix(c.path, "http://") || strings.HasPrefix(c.path, "https://") {
		// Never send credentials to another origin, even if a Link header says so.
		if !strings.HasPrefix(c.path, t.base+"/") {
			return nil, fmt.Errorf("github: refusing cross-origin URL %q", c.path)
		}
		target = c.path
	}
	var body io.Reader
	if c.in != nil {
		b, err := json.Marshal(c.in)
		if err != nil {
			return nil, fmt.Errorf("github: encode %s %s: %w", c.method, c.path, err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, c.method, target, body)
	if err != nil {
		return nil, fmt.Errorf("github: build request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", APIVersion)
	req.Header.Set("User-Agent", t.ua)
	req.Header.Set("Authorization", "Bearer "+auth)
	if c.in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := t.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("github: %s %s: %w", c.method, c.path, ctx.Err())
		}
		return nil, &TransportError{Method: c.method, Path: c.path, Err: err}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, &TransportError{Method: c.method, Path: c.path, Err: fmt.Errorf("read response: %w", err)}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		ae := &APIError{StatusCode: resp.StatusCode, Method: c.method, Path: c.path, header: resp.Header}
		if json.Unmarshal(data, ae) != nil || ae.Message == "" {
			ae.Message = http.StatusText(resp.StatusCode)
		}
		return nil, ae
	}
	if c.out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, c.out); err != nil {
			return nil, fmt.Errorf("github: %s %s: decode response: %w", c.method, c.path, err)
		}
	}
	return resp.Header, nil
}
