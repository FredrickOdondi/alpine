package github

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"
)

// sleepRecorder records requested waits without sleeping.
type sleepRecorder struct {
	mu    sync.Mutex
	waits []time.Duration
	err   error
}

func (s *sleepRecorder) Sleep(_ context.Context, d time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.waits = append(s.waits, d)
	return s.err
}

func (s *sleepRecorder) got() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.waits...)
}

func retryClient(t *testing.T, f *fakeGitHub, rp RetryPolicy, sr *sleepRecorder, rnd float64) *Client {
	t.Helper()
	c, err := NewClient(ClientConfig{BaseURL: f.URL, Tokens: StaticToken(tok), Owner: "acme", Repo: "shop",
		Retry: rp, Now: func() time.Time { return t0 }, Sleep: sr.Sleep, Rand: func() float64 { return rnd }})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

const refKey = "GET /repos/acme/shop/git/ref/heads/main"

var refOK = route{status: 200, body: `{"object":{"sha":"abc"}}`}

func assertWaits(t *testing.T, got []time.Duration, want ...time.Duration) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("waits %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("wait %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestRetry5xxThenSuccess(t *testing.T) {
	f := newFake(t, map[string]route{refKey: refOK})
	f.script(refKey, route{status: 502, body: `bad gateway`}, route{status: 503, body: `{}`, hdr: map[string]string{"Retry-After": "3"}})
	sr := &sleepRecorder{}
	sha, err := retryClient(t, f, RetryPolicy{}, sr, 0.5).BranchSHA(context.Background(), "main")
	if err != nil || sha != "abc" {
		t.Fatalf("%v %q", err, sha)
	}
	if n := len(f.requests()); n != 3 {
		t.Fatalf("requests %d", n)
	}
	// attempt 1: 500ms base, equal jitter at rand=0.5 -> 375ms; then Retry-After honoured.
	assertWaits(t, sr.got(), 375*time.Millisecond, 3*time.Second)
}

func TestRetryMaxAttemptsAndBackoffGrowth(t *testing.T) {
	f := newFake(t, map[string]route{refKey: {status: 500, body: `{"message":"boom"}`}})
	sr := &sleepRecorder{}
	_, err := retryClient(t, f, RetryPolicy{}, sr, 0.5).BranchSHA(context.Background(), "main")
	if StatusCode(err) != 500 || !IsAmbiguous(err) {
		t.Fatalf("want 500, got %v", err)
	}
	if n := len(f.requests()); n != 4 {
		t.Fatalf("default MaxAttempts is 4, got %d requests", n)
	}
	assertWaits(t, sr.got(), 375*time.Millisecond, 750*time.Millisecond, 1500*time.Millisecond)

	// MaxDelay caps a single step; jitter bounds are [d/2, d).
	f2 := newFake(t, map[string]route{refKey: {status: 500, body: `{}`}})
	sr2 := &sleepRecorder{}
	_, _ = retryClient(t, f2, RetryPolicy{MaxAttempts: 5, BaseDelay: time.Second, MaxDelay: 2 * time.Second}, sr2, 0).
		BranchSHA(context.Background(), "main")
	assertWaits(t, sr2.got(), 500*time.Millisecond, time.Second, time.Second, time.Second)

	// MaxAttempts 1 disables retries.
	f3 := newFake(t, map[string]route{refKey: {status: 500, body: `{}`}})
	_, _ = retryClient(t, f3, RetryPolicy{MaxAttempts: 1}, &sleepRecorder{}, 0).BranchSHA(context.Background(), "main")
	if n := len(f3.requests()); n != 1 {
		t.Fatalf("requests %d", n)
	}
}

func TestRetryRateLimits(t *testing.T) {
	cases := []struct {
		name string
		resp route
		want time.Duration
	}{
		{"secondary 403 message", route{status: 403, body: `{"message":"You have exceeded a secondary rate limit. Please wait a few minutes before you try again."}`}, SecondaryRateLimitMinWait},
		{"403 retry-after", route{status: 403, body: `{"message":"Forbidden"}`, hdr: map[string]string{"Retry-After": "7"}}, 7 * time.Second},
		{"429 retry-after date", route{status: 429, body: `{}`, hdr: map[string]string{"Retry-After": t0.Add(20 * time.Second).Format(http.TimeFormat)}}, 20 * time.Second},
		{"429 past date", route{status: 429, body: `{}`, hdr: map[string]string{"Retry-After": t0.Add(-time.Hour).Format(http.TimeFormat)}}, 0},
		{"429 bare", route{status: 429, body: `{}`}, SecondaryRateLimitMinWait},
		{"primary reset", route{status: 403, body: `{"message":"API rate limit exceeded"}`, hdr: map[string]string{
			"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": strconv.FormatInt(t0.Add(30*time.Second).Unix(), 10)}}, 31 * time.Second},
		{"primary bad reset", route{status: 403, body: `{}`, hdr: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "soon"}}, SecondaryRateLimitMinWait},
		{"bad retry-after falls back", route{status: 429, body: `{}`, hdr: map[string]string{"Retry-After": "later"}}, SecondaryRateLimitMinWait},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFake(t, map[string]route{refKey: refOK})
			f.script(refKey, c.resp)
			sr := &sleepRecorder{}
			if _, err := retryClient(t, f, RetryPolicy{}, sr, 0.5).BranchSHA(context.Background(), "main"); err != nil {
				t.Fatal(err)
			}
			assertWaits(t, sr.got(), c.want)
		})
	}
}

func TestRateLimitedCreateIsRetried(t *testing.T) {
	// A rate-limited POST was definitely not applied, so even creates retry it.
	f := newFake(t, map[string]route{"POST /repos/acme/shop/issues/42/comments": {status: 201, body: `{"id":1}`}})
	f.script("POST /repos/acme/shop/issues/42/comments", route{status: 429, body: `{}`, hdr: map[string]string{"Retry-After": "1"}})
	sr := &sleepRecorder{}
	if _, err := retryClient(t, f, RetryPolicy{}, sr, 0.5).CreateIssueComment(context.Background(), 42, "", "hi"); err != nil {
		t.Fatal(err)
	}
	assertSeq(t, f.requests(), "POST /repos/acme/shop/issues/42/comments", "POST /repos/acme/shop/issues/42/comments")
	assertWaits(t, sr.got(), time.Second)
}

func TestNoRetry(t *testing.T) {
	cases := map[string]route{
		"403 permission":    {status: 403, body: `{"message":"Resource not accessible by integration"}`},
		"404":               {status: 404, body: `{"message":"Not Found"}`},
		"422":               {status: 422, body: `{"message":"Validation Failed"}`},
		"wait over max":     {status: 429, body: `{}`, hdr: map[string]string{"Retry-After": "600"}},
		"5xx wait over max": {status: 503, body: `{}`, hdr: map[string]string{"Retry-After": "600"}},
		"reset over max":    {status: 403, body: `{}`, hdr: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": strconv.FormatInt(t0.Add(time.Hour).Unix(), 10)}},
	}
	for name, resp := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFake(t, map[string]route{refKey: resp})
			sr := &sleepRecorder{}
			_, err := retryClient(t, f, RetryPolicy{}, sr, 0.5).BranchSHA(context.Background(), "main")
			if StatusCode(err) != resp.status {
				t.Fatalf("got %v", err)
			}
			if n := len(f.requests()); n != 1 || len(sr.got()) != 0 {
				t.Fatalf("must not retry: %d requests, waits %v", n, sr.got())
			}
		})
	}
	if !IsRateLimited(&APIError{StatusCode: 429}) || IsRateLimited(errors.New("x")) || IsRateLimited(&APIError{StatusCode: 403}) {
		t.Fatal("IsRateLimited")
	}
}

func TestRetryTransportError(t *testing.T) {
	f := newFake(t, map[string]route{refKey: refOK})
	f.script(refKey, route{hangup: true})
	sr := &sleepRecorder{}
	if _, err := retryClient(t, f, RetryPolicy{}, sr, 0.5).BranchSHA(context.Background(), "main"); err != nil {
		t.Fatal(err)
	}
	if n := len(f.requests()); n != 2 || len(sr.got()) != 1 {
		t.Fatalf("requests %d waits %v", n, sr.got())
	}

	// A create that hangs up is ambiguous and is NOT blindly retried.
	f2 := newFake(t, map[string]route{})
	f2.script("POST /repos/acme/shop/issues/42/comments", route{hangup: true})
	_, err := retryClient(t, f2, RetryPolicy{}, &sleepRecorder{}, 0.5).CreateIssueComment(context.Background(), 42, "", "hi")
	var te *TransportError
	if !errors.As(err, &te) || !IsAmbiguous(err) || te.Unwrap() == nil || te.Error() == "" {
		t.Fatalf("want TransportError, got %v", err)
	}
	if n := len(f2.requests()); n != 1 {
		t.Fatalf("create retried blindly: %d", n)
	}
}

func TestRetryStopsOnContext(t *testing.T) {
	f := newFake(t, map[string]route{refKey: {status: 500, body: `{}`}})
	sr := &sleepRecorder{err: context.Canceled}
	_, err := retryClient(t, f, RetryPolicy{}, sr, 0.5).BranchSHA(context.Background(), "main")
	if !errors.Is(err, context.Canceled) || StatusCode(err) != 500 {
		t.Fatalf("got %v", err)
	}
	if n := len(f.requests()); n != 1 {
		t.Fatalf("requests %d", n)
	}
	// Default sleeper honours ctx.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepCtx(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := sleepCtx(context.Background(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
}

func TestAppTokenMintRetried(t *testing.T) {
	c := &clock{t: t0}
	f := newFake(t, map[string]route{"POST /app/installations/555/access_tokens": tokenResp("ghs_r", t0.Add(time.Hour))})
	f.script("POST /app/installations/555/access_tokens", route{status: 502, body: `{}`})
	a := newTestApp(t, f, c)
	tk, err := a.InstallationToken(context.Background(), TokenRequest{InstallationID: 555, Repository: "shop", Permissions: PermsPush()})
	if err != nil || tk.Token != "ghs_r" {
		t.Fatalf("%v %v", err, tk)
	}
	reqs := f.requests()
	if len(reqs) != 2 {
		t.Fatalf("requests %d", len(reqs))
	}
	parseJWT(t, reqs[1].Auth) // fresh JWT on the retry
}
