package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/alpine-dev/alpine/packages/shared/redact"
)

// Options configures a Gateway. Only Provider and Routing are required; everything else has a
// safe default (no limits/budget, in-memory idempotency, no metering).
type Options struct {
	Provider    Provider
	Routing     Routing
	Keys        KeyResolver
	Limiter     Limiter
	Budget      BudgetChecker
	Recorder    UsageRecorder
	Idempotency IdempotencyStore
	Pricing     Pricing
	Retry       RetryPolicy
	Breaker     BreakerConfig
	// MaxRepairs is how many repair-retries a structured call gets after invalid output.
	MaxRepairs int
	// RedactOutbound scrubs secrets from prompts before they leave Alpine.
	RedactOutbound bool
	// LogPrompts logs (redacted, truncated) prompts and responses at debug level.
	LogPrompts bool
	Logger     *slog.Logger
	// Test hooks.
	Now    func() time.Time
	Sleep  func(ctx context.Context, d time.Duration) error
	Random func() float64
}

// Gateway is the reliable, multi-tenant model client. It is safe for concurrent use.
type Gateway struct {
	o        Options
	breakers *BreakerSet
	redactor *redact.Redactor
	tracer   trace.Tracer
	m        gatewayMetrics
}

type gatewayMetrics struct {
	requests metric.Int64Counter
	latency  metric.Float64Histogram
	tokens   metric.Int64Counter
	cost     metric.Float64Counter
	retries  metric.Int64Counter
	fallback metric.Int64Counter
}

// ErrInvalidRequest is returned for malformed requests.
var ErrInvalidRequest = errors.New("llm: invalid request")

// ErrAllModelsFailed wraps the last error when every model in the chain failed.
var ErrAllModelsFailed = errors.New("llm: all models in the fallback chain failed")

// NewGateway validates options and builds a Gateway.
func NewGateway(o Options) (*Gateway, error) {
	if o.Provider == nil {
		return nil, fmt.Errorf("%w: provider is required", ErrInvalidRequest)
	}
	if len(o.Routing) == 0 {
		return nil, fmt.Errorf("%w: routing is required", ErrInvalidRequest)
	}
	if o.Keys == nil {
		o.Keys = StaticKeys{}
	}
	if o.Limiter == nil {
		o.Limiter = NoopLimiter{}
	}
	if o.Budget == nil {
		o.Budget = NoopBudget{}
	}
	if o.Idempotency == nil {
		o.Idempotency = &MemoryIdempotency{}
	}
	if o.Pricing == nil {
		o.Pricing = DefaultPricing
	}
	if o.MaxRepairs < 0 {
		o.MaxRepairs = 0
	} else if o.MaxRepairs == 0 {
		o.MaxRepairs = 2
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Sleep == nil {
		o.Sleep = sleepCtx
	}
	o.Retry = o.Retry.withDefaults()
	g := &Gateway{
		o:        o,
		breakers: NewBreakerSet(o.Breaker, o.Now),
		redactor: redact.New(),
		tracer:   otel.Tracer("alpine/llm"),
	}
	meter := otel.Meter("alpine/llm")
	g.m.requests, _ = meter.Int64Counter("alpine.llm.requests", metric.WithDescription("Gateway calls by stage/model/status"))
	g.m.latency, _ = meter.Float64Histogram("alpine.llm.latency", metric.WithUnit("ms"))
	g.m.tokens, _ = meter.Int64Counter("alpine.llm.tokens", metric.WithDescription("Tokens by direction"))
	g.m.cost, _ = meter.Float64Counter("alpine.llm.cost", metric.WithUnit("USD"))
	g.m.retries, _ = meter.Int64Counter("alpine.llm.retries")
	g.m.fallback, _ = meter.Int64Counter("alpine.llm.fallbacks")
	return g, nil
}

// Breakers exposes breaker state (metrics, tests).
func (g *Gateway) Breakers() *BreakerSet { return g.breakers }

// Routing returns the configured routing.
func (g *Gateway) Routing() Routing { return g.o.Routing }

// CacheScope derives the per-tenant prompt-cache partition key (hash of org id).
func CacheScope(orgID string) string {
	sum := sha256.Sum256([]byte("alpine-cache:" + orgID))
	return "org_" + hex.EncodeToString(sum[:12])
}

// Complete runs one logical model call with idempotency, budgets, limits, retries, breakers and
// fallbacks, and meters it.
func (g *Gateway) Complete(ctx context.Context, req Request) (*Response, error) {
	if err := validate(req); err != nil {
		return nil, err
	}
	route, ok := g.o.Routing[req.Stage]
	if !ok || len(route.Models) == 0 {
		return nil, fmt.Errorf("%w: no route for stage %q", ErrInvalidRequest, req.Stage)
	}
	ctx, span := g.tracer.Start(ctx, "llm.complete "+string(req.Stage), trace.WithAttributes(
		attribute.String("gen_ai.system", g.o.Provider.Name()),
		attribute.String("gen_ai.operation.name", "chat"),
		attribute.String("gen_ai.request.model", route.Models[0]),
		attribute.String("alpine.stage", string(req.Stage)),
		attribute.String("alpine.org_id", req.Tenant.OrgID),
		attribute.String("alpine.ticket_id", req.Tenant.TicketID),
		attribute.String("alpine.idempotency_key", req.IdempotencyKey),
	))
	defer span.End()

	// 1. Idempotency: a retried activity replays the stored result (no new spend).
	if cached, hit, err := g.o.Idempotency.Get(ctx, req.Tenant.OrgID, req.IdempotencyKey); err != nil {
		g.o.Logger.WarnContext(ctx, "llm idempotency lookup failed", "err", err)
	} else if hit {
		cached.Replayed = true
		span.SetAttributes(attribute.Bool("alpine.replayed", true))
		return cached, nil
	}

	// 2. Outbound secret scanning.
	msgs := req.Messages
	if g.o.RedactOutbound {
		msgs = g.redactMessages(ctx, msgs)
	}

	estimate := req.EstimatedInputTokens
	if estimate <= 0 {
		estimate = estimateTokens(msgs)
	}
	maxOut := route.MaxOutputTokens
	if req.MaxOutputTokens > 0 {
		maxOut = req.MaxOutputTokens
	}

	// 3. Budget pre-flight.
	if err := g.o.Budget.Check(ctx, req.Tenant, estimate+maxOut); err != nil {
		g.fail(ctx, span, req, route.Models[0], "budget_exceeded", err)
		return nil, err
	}

	// 4. Key resolution (BYOK first).
	key, byok, err := g.o.Keys.Resolve(ctx, req.Tenant.OrgID)
	if err != nil {
		g.fail(ctx, span, req, route.Models[0], "no_key", err)
		return nil, err
	}

	// 5. Tenant concurrency + rate limits (fair across orgs).
	release, err := g.o.Limiter.Acquire(ctx, LimitRequest{OrgID: req.Tenant.OrgID, RepoID: req.Tenant.RepoID, Tokens: estimate + maxOut})
	if err != nil {
		g.fail(ctx, span, req, route.Models[0], "rate_limited", err)
		return nil, err
	}
	defer release()

	temp := route.Temperature
	if req.Temperature != nil {
		temp = req.Temperature
	}
	preq := ProviderRequest{
		Messages: msgs, Tools: req.Tools, Schema: req.Schema, MaxOutputTokens: maxOut,
		Temperature: temp, APIKey: key, CacheScope: CacheScope(req.Tenant.OrgID),
	}
	g.logPrompt(ctx, req, msgs)

	// 6. Fallback chain x retries x breaker.
	start := g.o.Now()
	attempts := 0
	var lastErr error
	var resp ProviderResponse
	var model string
	answered := false
chain:
	for i, m := range route.Models {
		if i > 0 {
			g.m.fallback.Add(ctx, 1, metric.WithAttributes(attribute.String("stage", string(req.Stage)), attribute.String("model", m)))
			span.AddEvent("fallback", trace.WithAttributes(attribute.String("model", m)))
		}
		if !g.breakers.Allow(m) {
			lastErr = fmt.Errorf("llm: circuit open for model %s", m)
			continue
		}
		for try := 1; try <= g.o.Retry.MaxAttempts; try++ {
			attempts++
			preq.Model = m
			actx, cancel := context.WithTimeout(ctx, g.o.Retry.AttemptTimeout)
			r, err := g.o.Provider.Complete(actx, preq)
			cancel()
			if err == nil {
				g.breakers.Success(m)
				resp, model, answered = r, m, true
				break chain
			}
			lastErr = err
			if ctx.Err() != nil { // caller cancelled / activity timed out
				break chain
			}
			if !Retryable(err) {
				if fallbackWorthy(err) {
					continue chain
				}
				break chain
			}
			g.breakers.Failure(m)
			ra := retryAfter(err)
			if try == g.o.Retry.MaxAttempts || ra > g.o.Retry.MaxRetryAfterWait || !g.breakers.Allow(m) {
				continue chain
			}
			g.m.retries.Add(ctx, 1, metric.WithAttributes(attribute.String("model", m)))
			if err := g.o.Sleep(ctx, g.o.Retry.Backoff(try, ra, g.o.Random)); err != nil {
				lastErr = err
				break chain
			}
		}
	}
	latency := g.o.Now().Sub(start)

	if !answered {
		if lastErr == nil {
			lastErr = errors.New("no model available")
		}
		err := fmt.Errorf("%w: %w", ErrAllModelsFailed, lastErr)
		g.record(ctx, req, CallRecord{Model: route.Models[0], Status: "error", Attempts: attempts, Latency: latency, BYOK: byok, Error: g.redactor.String(lastErr.Error())}, route.Models[0])
		g.fail(ctx, span, req, route.Models[0], "error", err)
		return nil, err
	}

	cost := resp.CostUSD
	if cost == 0 {
		var known bool
		if cost, known = g.o.Pricing.Cost(model, resp.Usage); !known {
			g.o.Logger.WarnContext(ctx, "llm pricing unknown for model; cost metered as 0", "model", model)
		}
	}
	out := &Response{
		Content: resp.Content, ToolCalls: resp.ToolCalls, FinishReason: resp.FinishReason,
		Model: model, RequestedModel: route.Models[0], Usage: resp.Usage, CostUSD: cost,
		Latency: latency, Attempts: attempts, BYOK: byok,
	}
	g.record(ctx, req, CallRecord{Model: model, Status: "ok", Attempts: attempts, Usage: resp.Usage, CostUSD: cost, Latency: latency, BYOK: byok}, route.Models[0])
	if err := g.o.Idempotency.Put(ctx, req.Tenant.OrgID, req.IdempotencyKey, out); err != nil {
		g.o.Logger.WarnContext(ctx, "llm idempotency store failed", "err", err)
	}

	attrs := metric.WithAttributes(attribute.String("stage", string(req.Stage)), attribute.String("model", model), attribute.String("status", "ok"))
	g.m.requests.Add(ctx, 1, attrs)
	g.m.latency.Record(ctx, float64(latency.Milliseconds()), attrs)
	g.m.cost.Add(ctx, cost, attrs)
	g.m.tokens.Add(ctx, resp.Usage.InputTokens, metric.WithAttributes(attribute.String("direction", "input"), attribute.String("model", model)))
	g.m.tokens.Add(ctx, resp.Usage.OutputTokens, metric.WithAttributes(attribute.String("direction", "output"), attribute.String("model", model)))
	span.SetAttributes(
		attribute.String("gen_ai.response.model", model),
		attribute.Int64("gen_ai.usage.input_tokens", resp.Usage.InputTokens),
		attribute.Int64("gen_ai.usage.output_tokens", resp.Usage.OutputTokens),
		attribute.Float64("alpine.cost_usd", cost),
		attribute.Int("alpine.attempts", attempts),
		attribute.Bool("alpine.byok", byok),
	)
	g.logResponse(ctx, req, out)
	return out, nil
}

func (g *Gateway) record(ctx context.Context, req Request, rec CallRecord, requested string) {
	if g.o.Recorder == nil {
		return
	}
	rec.Tenant, rec.IdempotencyKey, rec.Stage = req.Tenant, req.IdempotencyKey, req.Stage
	rec.Provider, rec.RequestedModel = g.o.Provider.Name(), requested
	// Metering must not be lost on caller cancellation.
	mctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := g.o.Recorder.Record(mctx, rec); err != nil {
		g.o.Logger.ErrorContext(ctx, "llm usage metering failed", "err", err, "org_id", req.Tenant.OrgID, "key", req.IdempotencyKey)
	}
}

func (g *Gateway) fail(ctx context.Context, span trace.Span, req Request, model, status string, err error) {
	span.RecordError(err)
	span.SetStatus(codes.Error, status)
	g.m.requests.Add(ctx, 1, metric.WithAttributes(attribute.String("stage", string(req.Stage)), attribute.String("model", model), attribute.String("status", status)))
}

func (g *Gateway) redactMessages(ctx context.Context, msgs []Message) []Message {
	out := make([]Message, len(msgs))
	for i, m := range msgs {
		red, hits := g.redactor.Redact(m.Content)
		if len(hits) > 0 {
			g.o.Logger.WarnContext(ctx, "secrets redacted from outbound prompt", "rules", hits)
		}
		m.Content = red
		out[i] = m
	}
	return out
}

func (g *Gateway) logPrompt(ctx context.Context, req Request, msgs []Message) {
	if !g.o.LogPrompts {
		return
	}
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(string(m.Role))
		b.WriteString(": ")
		b.WriteString(m.Content)
		b.WriteString("\n")
	}
	g.o.Logger.DebugContext(ctx, "llm prompt", "stage", req.Stage, "org_id", req.Tenant.OrgID, "ticket_id", req.Tenant.TicketID,
		"key", req.IdempotencyKey, "prompt", redact.Truncate(g.redactor.String(b.String()), 8000))
}

func (g *Gateway) logResponse(ctx context.Context, req Request, r *Response) {
	if !g.o.LogPrompts {
		return
	}
	g.o.Logger.DebugContext(ctx, "llm response", "stage", req.Stage, "model", r.Model, "key", req.IdempotencyKey,
		"tokens_in", r.Usage.InputTokens, "tokens_out", r.Usage.OutputTokens, "cost_usd", r.CostUSD,
		"response", redact.Truncate(g.redactor.String(r.Content), 8000))
}

func validate(req Request) error {
	switch {
	case req.Tenant.OrgID == "":
		return fmt.Errorf("%w: tenant org id is required", ErrInvalidRequest)
	case req.IdempotencyKey == "":
		return fmt.Errorf("%w: idempotency key is required", ErrInvalidRequest)
	case req.Stage == "":
		return fmt.Errorf("%w: stage is required", ErrInvalidRequest)
	case len(req.Messages) == 0:
		return fmt.Errorf("%w: messages are required", ErrInvalidRequest)
	}
	return nil
}

// estimateTokens is a cheap ~4 chars/token estimate for rate limiting.
func estimateTokens(msgs []Message) int {
	n := 0
	for _, m := range msgs {
		n += len(m.Content)/4 + 4
	}
	return n
}
