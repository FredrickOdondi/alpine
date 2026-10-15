package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// scriptedProvider returns queued results per model and records calls.
type scriptedProvider struct {
	mu       sync.Mutex
	script   map[string][]result
	calls    []ProviderRequest
	defaults map[string]result
}

type result struct {
	resp ProviderResponse
	err  error
}

func (p *scriptedProvider) Name() string { return "openai" }

func (p *scriptedProvider) Complete(_ context.Context, req ProviderRequest) (ProviderResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, req)
	q := p.script[req.Model]
	if len(q) == 0 {
		if d, ok := p.defaults[req.Model]; ok {
			return d.resp, d.err
		}
		return ProviderResponse{Content: "ok", Model: req.Model, Usage: Usage{InputTokens: 10, OutputTokens: 5}}, nil
	}
	r := q[0]
	p.script[req.Model] = q[1:]
	if r.err == nil && r.resp.Model == "" {
		r.resp.Model = req.Model
	}
	return r.resp, r.err
}

func (p *scriptedProvider) modelsCalled() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, c := range p.calls {
		out = append(out, c.Model)
	}
	return out
}

type sleepRecorder struct {
	mu     sync.Mutex
	sleeps []time.Duration
}

func (s *sleepRecorder) Sleep(_ context.Context, d time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sleeps = append(s.sleeps, d)
	return nil
}

func testRouting() Routing {
	return Routing{
		StageTriage:     {Models: []string{"gpt-5-mini", "gpt-4.1-mini"}},
		StageRefine:     {Models: []string{"gpt-5-mini"}},
		StagePrioritize: {Models: []string{"gpt-5-nano"}},
		StageImplement:  {Models: []string{"gpt-5", "gpt-4.1"}},
		StageReview:     {Models: []string{"o3", "gpt-5-mini"}},
	}
}

func newTestGateway(t *testing.T, p Provider, mutate func(*Options)) (*Gateway, *MemoryRecorder, *sleepRecorder) {
	t.Helper()
	rec := &MemoryRecorder{}
	sl := &sleepRecorder{}
	o := Options{
		Provider: p, Routing: testRouting(), Recorder: rec, Sleep: sl.Sleep,
		Random: func() float64 { return 0.5 },
		Retry:  RetryPolicy{MaxAttempts: 3, BaseDelay: 100 * time.Millisecond, MaxDelay: time.Second},
	}
	if mutate != nil {
		mutate(&o)
	}
	g, err := NewGateway(o)
	require.NoError(t, err)
	return g, rec, sl
}

func req(key string) Request {
	return Request{
		Tenant:         Tenant{OrgID: "org-1", TicketID: "t-1"},
		Stage:          StageTriage,
		IdempotencyKey: key,
		Messages:       []Message{{Role: RoleUser, Content: "classify this"}},
	}
}

func TestRetryHonoursRetryAfterThenSucceeds(t *testing.T) {
	p := &scriptedProvider{script: map[string][]result{
		"gpt-5-mini": {{err: &APIError{StatusCode: 429, RetryAfter: 3 * time.Second}}, {resp: ProviderResponse{Content: "done", Usage: Usage{InputTokens: 1000, OutputTokens: 100}}}},
	}}
	g, rec, sl := newTestGateway(t, p, nil)
	resp, err := g.Complete(context.Background(), req("wf/act/1"))
	require.NoError(t, err)
	require.Equal(t, "done", resp.Content)
	require.Equal(t, 2, resp.Attempts)
	require.Equal(t, "gpt-5-mini", resp.Model)
	require.Equal(t, []time.Duration{3 * time.Second}, sl.sleeps, "Retry-After must win over smaller jittered backoff")
	require.InDelta(t, (1000*0.25+100*2)/1e6, resp.CostUSD, 1e-12)
	require.Len(t, rec.All(), 1)
	require.Equal(t, "ok", rec.All()[0].Status)
	require.Equal(t, 2, rec.All()[0].Attempts)
}

func TestBackoffIsExponentialWithJitterAndCapped(t *testing.T) {
	p := RetryPolicy{BaseDelay: 100 * time.Millisecond, MaxDelay: time.Second, Multiplier: 2}
	one := func() float64 { return 1 }
	require.Equal(t, 100*time.Millisecond, p.Backoff(1, 0, one))
	require.Equal(t, 200*time.Millisecond, p.Backoff(2, 0, one))
	require.Equal(t, 400*time.Millisecond, p.Backoff(3, 0, one))
	require.Equal(t, time.Second, p.Backoff(10, 0, one))
	require.Equal(t, 50*time.Millisecond, p.Backoff(1, 0, func() float64 { return 0.5 }))
	require.Equal(t, 5*time.Second, p.Backoff(1, 5*time.Second, one))
}

func TestFallbackToSecondaryModelAfterRetriesExhausted(t *testing.T) {
	five := &APIError{StatusCode: 503, Message: "overloaded"}
	p := &scriptedProvider{script: map[string][]result{
		"gpt-5-mini": {{err: five}, {err: five}, {err: five}},
	}}
	g, rec, sl := newTestGateway(t, p, nil)
	resp, err := g.Complete(context.Background(), req("wf/act/2"))
	require.NoError(t, err)
	require.Equal(t, "gpt-4.1-mini", resp.Model)
	require.Equal(t, "gpt-5-mini", resp.RequestedModel)
	require.Equal(t, 4, resp.Attempts)
	require.Equal(t, []string{"gpt-5-mini", "gpt-5-mini", "gpt-5-mini", "gpt-4.1-mini"}, p.modelsCalled())
	require.Len(t, sl.sleeps, 2, "sleeps only between retries of the same model")
	require.Equal(t, "gpt-4.1-mini", rec.All()[0].Model)
	require.Equal(t, "gpt-5-mini", rec.All()[0].RequestedModel)
}

func TestNonRetryableErrorFailsFast(t *testing.T) {
	p := &scriptedProvider{script: map[string][]result{
		"gpt-5-mini": {{err: &APIError{StatusCode: 400, Message: "bad"}}},
	}}
	g, rec, _ := newTestGateway(t, p, nil)
	_, err := g.Complete(context.Background(), req("wf/act/3"))
	require.ErrorIs(t, err, ErrAllModelsFailed)
	require.Equal(t, []string{"gpt-5-mini"}, p.modelsCalled())
	require.Equal(t, "error", rec.All()[0].Status)
}

func TestModelNotFoundFallsBackWithoutRetry(t *testing.T) {
	p := &scriptedProvider{script: map[string][]result{
		"gpt-5-mini": {{err: &APIError{StatusCode: 404, Code: "model_not_found"}}},
	}}
	g, _, _ := newTestGateway(t, p, nil)
	resp, err := g.Complete(context.Background(), req("wf/act/4"))
	require.NoError(t, err)
	require.Equal(t, "gpt-4.1-mini", resp.Model)
	require.Equal(t, []string{"gpt-5-mini", "gpt-4.1-mini"}, p.modelsCalled())
}

func TestCircuitBreakerOpensAndSkipsModel(t *testing.T) {
	now := time.Unix(0, 0)
	clock := func() time.Time { return now }
	five := result{err: &APIError{StatusCode: 500}}
	p := &scriptedProvider{script: map[string][]result{}, defaults: map[string]result{"gpt-5-mini": five}}
	g, _, _ := newTestGateway(t, p, func(o *Options) {
		o.Breaker = BreakerConfig{FailureThreshold: 3, OpenFor: time.Minute}
		o.Now = clock
	})
	_, err := g.Complete(context.Background(), req("k1"))
	require.NoError(t, err) // served by fallback
	require.Equal(t, "open", g.Breakers().State("gpt-5-mini"))

	p.calls = nil
	_, err = g.Complete(context.Background(), req("k2"))
	require.NoError(t, err)
	require.Equal(t, []string{"gpt-4.1-mini"}, p.modelsCalled(), "open breaker must skip the primary")

	// After OpenFor, one half-open probe is allowed; success closes the breaker.
	now = now.Add(2 * time.Minute)
	p.defaults = nil
	p.calls = nil
	_, err = g.Complete(context.Background(), req("k3"))
	require.NoError(t, err)
	require.Equal(t, []string{"gpt-5-mini"}, p.modelsCalled())
	require.Equal(t, "closed", g.Breakers().State("gpt-5-mini"))
}

func TestIdempotentReplayDoesNotCallOrMeterTwice(t *testing.T) {
	p := &scriptedProvider{script: map[string][]result{}}
	g, rec, _ := newTestGateway(t, p, nil)
	r1, err := g.Complete(context.Background(), req("wf-1/activity-7/1"))
	require.NoError(t, err)
	require.False(t, r1.Replayed)
	r2, err := g.Complete(context.Background(), req("wf-1/activity-7/1"))
	require.NoError(t, err)
	require.True(t, r2.Replayed)
	require.Equal(t, r1.Content, r2.Content)
	require.Len(t, p.calls, 1)
	require.Len(t, rec.All(), 1)

	// Same key under another org is a different call (no cross-tenant sharing).
	other := req("wf-1/activity-7/1")
	other.Tenant.OrgID = "org-2"
	r3, err := g.Complete(context.Background(), other)
	require.NoError(t, err)
	require.False(t, r3.Replayed)
	require.Len(t, p.calls, 2)
}

type denyBudget struct{}

type budgetErr struct{}

func (budgetErr) Error() string        { return "budget exceeded" }
func (budgetErr) BudgetExceeded() bool { return true }

func (denyBudget) Check(context.Context, Tenant, int) error { return budgetErr{} }

func TestBudgetExceededBlocksBeforeProvider(t *testing.T) {
	p := &scriptedProvider{script: map[string][]result{}}
	g, _, _ := newTestGateway(t, p, func(o *Options) { o.Budget = denyBudget{} })
	_, err := g.Complete(context.Background(), req("k"))
	require.True(t, IsBudgetExceeded(err))
	require.Empty(t, p.calls)
}

func TestBYOKKeyIsPassedButNeverPrinted(t *testing.T) {
	p := &scriptedProvider{script: map[string][]result{}}
	g, rec, _ := newTestGateway(t, p, func(o *Options) {
		o.Keys = StaticKeys{Keys: map[string]Secret{"org-1": "sk-customer-key-1234567890abcdef"}}
	})
	resp, err := g.Complete(context.Background(), req("k"))
	require.NoError(t, err)
	require.True(t, resp.BYOK)
	require.True(t, rec.All()[0].BYOK)
	require.Equal(t, "sk-customer-key-1234567890abcdef", p.calls[0].APIKey.Reveal())
	printed := fmt.Sprintf("%v %+v %#v", p.calls[0].APIKey, p.calls[0], p.calls[0].APIKey)
	require.NotContains(t, printed, "sk-customer")
	js, _ := json.Marshal(p.calls[0].APIKey)
	require.NotContains(t, string(js), "sk-customer")
	require.Equal(t, CacheScope("org-1"), p.calls[0].CacheScope)
	require.NotContains(t, p.calls[0].CacheScope, "org-1")
}

func TestOutboundRedaction(t *testing.T) {
	p := &scriptedProvider{script: map[string][]result{}}
	g, _, _ := newTestGateway(t, p, func(o *Options) { o.RedactOutbound = true })
	r := req("k")
	r.Messages = []Message{{Role: RoleUser, Content: "crash with token ghp_abcdefghijklmnopqrstuvwxyz0123456789"}}
	_, err := g.Complete(context.Background(), r)
	require.NoError(t, err)
	require.NotContains(t, p.calls[0].Messages[0].Content, "ghp_")
}

var triageSchema = &Schema{Name: "triage", Schema: json.RawMessage(`{
  "type":"object","additionalProperties":false,"required":["classification","confidence"],
  "properties":{"classification":{"type":"string","enum":["bug","feature","noise"]},"confidence":{"type":"number","minimum":0,"maximum":1}}}`)}

func TestStructuredOutputRepairRetry(t *testing.T) {
	p := &scriptedProvider{script: map[string][]result{
		"gpt-5-mini": {
			{resp: ProviderResponse{Content: `{"classification":"bugg","confidence":2}`, Usage: Usage{InputTokens: 10}}},
			{resp: ProviderResponse{Content: "```json\n{\"classification\":\"bug\",\"confidence\":0.9}\n```", Usage: Usage{InputTokens: 10}}},
		},
	}}
	g, rec, _ := newTestGateway(t, p, nil)
	r := req("wf/triage/1")
	r.Schema = triageSchema
	var out struct {
		Classification string  `json:"classification"`
		Confidence     float64 `json:"confidence"`
	}
	resp, err := g.CompleteJSON(context.Background(), r, &out)
	require.NoError(t, err)
	require.Equal(t, "bug", out.Classification)
	require.Len(t, p.calls, 2)
	last := p.calls[1].Messages
	require.Contains(t, last[len(last)-1].Content, "did not match the required JSON schema")
	keys := []string{rec.All()[0].IdempotencyKey, rec.All()[1].IdempotencyKey}
	require.Equal(t, []string{"wf/triage/1", "wf/triage/1/repair-1"}, keys)
	require.Equal(t, int64(20), resp.Usage.InputTokens, "usage aggregates across repairs")
}

func TestStructuredOutputGivesUp(t *testing.T) {
	p := &scriptedProvider{script: map[string][]result{}, defaults: map[string]result{"gpt-5-mini": {resp: ProviderResponse{Content: "not json"}}}}
	g, _, _ := newTestGateway(t, p, func(o *Options) { o.MaxRepairs = 1 })
	r := req("k")
	r.Schema = triageSchema
	var out map[string]any
	_, err := g.CompleteJSON(context.Background(), r, &out)
	require.ErrorIs(t, err, ErrInvalidOutput)
	require.Len(t, p.calls, 2)
}

func TestRoutingValidationRequiresDistinctReviewer(t *testing.T) {
	require.NoError(t, testRouting().Validate())
	bad := testRouting()
	bad[StageReview] = Route{Models: []string{"gpt-4.1"}}
	require.ErrorContains(t, bad.Validate(), "different model")
}

func TestRequestValidation(t *testing.T) {
	g, _, _ := newTestGateway(t, &scriptedProvider{}, nil)
	r := req("")
	_, err := g.Complete(context.Background(), r)
	require.ErrorIs(t, err, ErrInvalidRequest)
}

func TestOpenAICompatibleTransport(t *testing.T) {
	var got map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/chat/completions", r.URL.Path)
		got = nil
		auth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(b, &got))
		if got["model"] == "busy" {
			w.Header().Set("retry-after-ms", "1500")
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"error":{"message":"slow down","type":"rate_limit","code":"rate_limit_exceeded"}}`))
			return
		}
		w.Header().Set("x-litellm-response-cost", "0.0123")
		_, _ = w.Write([]byte(`{"model":"gpt-5-mini-2026","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"{}"}}],
			"usage":{"prompt_tokens":100,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":40}}}`))
	}))
	defer srv.Close()
	p := &OpenAICompatible{BaseURL: srv.URL + "/v1", APIKey: "sk-litellm-master", BYOKInBody: true}
	resp, err := p.Complete(context.Background(), ProviderRequest{
		Model: "gpt-5-mini", Messages: []Message{{Role: RoleUser, Content: "hi"}},
		Schema: triageSchema, APIKey: "sk-byok", CacheScope: "org_abc",
	})
	require.NoError(t, err)
	require.Equal(t, "Bearer sk-litellm-master", auth)
	require.Equal(t, "sk-byok", got["api_key"])
	require.Equal(t, "org_abc", got["prompt_cache_key"])
	require.Equal(t, "json_schema", got["response_format"].(map[string]any)["type"])
	require.Equal(t, Usage{InputTokens: 100, OutputTokens: 20, CachedTokens: 40}, resp.Usage)
	require.Equal(t, 0.0123, resp.CostUSD)

	_, err = p.Complete(context.Background(), ProviderRequest{Model: "busy", Messages: []Message{{Role: RoleUser, Content: "hi"}}})
	var apiErr *APIError
	require.True(t, errors.As(err, &apiErr))
	require.Equal(t, 1500*time.Millisecond, apiErr.RetryAfter)
	require.True(t, Retryable(err))

	direct := &OpenAICompatible{BaseURL: srv.URL + "/v1", APIKey: "sk-platform"}
	_, err = direct.Complete(context.Background(), ProviderRequest{Model: "x", Messages: []Message{{Role: RoleUser, Content: "hi"}}, APIKey: "sk-byok"})
	require.NoError(t, err)
	require.Equal(t, "Bearer sk-byok", auth, "direct mode sends BYOK as bearer")
	require.Nil(t, got["api_key"])
}

func TestRetryAfterHTTPDate(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	h := http.Header{}
	h.Set("Retry-After", now.Add(7*time.Second).Format(http.TimeFormat))
	require.Equal(t, 7*time.Second, parseRetryAfter(h, now))
	h.Set("Retry-After", "2")
	require.Equal(t, 2*time.Second, parseRetryAfter(h, now))
	require.False(t, Retryable(context.Canceled))
	require.True(t, Retryable(context.DeadlineExceeded))
	require.False(t, Retryable(&APIError{StatusCode: 429, Code: "insufficient_quota"}))
	require.True(t, strings.Contains((&APIError{StatusCode: 500, Message: "x"}).Error(), "500"))
}

func TestPricingDatedSnapshot(t *testing.T) {
	c, ok := DefaultPricing.Cost("gpt-4.1-2025-04-14", Usage{InputTokens: 1_000_000})
	if !ok || c != 2 {
		t.Fatalf("got %v %v", c, ok)
	}
}
