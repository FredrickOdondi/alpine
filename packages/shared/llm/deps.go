package llm

import (
	"context"
	"errors"
	"sync"
	"time"
)

// KeyResolver returns the API key to use for an org: its BYO OpenAI key when one is stored
// (byok=true), otherwise "" meaning "use the platform key configured in the proxy". Implementations
// must keep plaintext in memory only and must never cache across orgs.
type KeyResolver interface {
	Resolve(ctx context.Context, orgID string) (key Secret, byok bool, err error)
}

// ErrNoKey is returned when an org has no BYO key and platform keys are disallowed.
var ErrNoKey = errors.New("llm: org has no model key configured")

// LimitRequest describes the capacity a call needs.
type LimitRequest struct {
	OrgID  string
	RepoID string
	Tokens int // estimated input+output tokens, for tokens-per-minute buckets
}

// Limiter enforces per-org/per-repo concurrency and rate limits. Acquire blocks until capacity
// is available (fairly across tenants) or ctx ends; release must be called exactly once.
type Limiter interface {
	Acquire(ctx context.Context, req LimitRequest) (release func(), err error)
}

// ErrRateLimited is returned by a Limiter that gave up waiting. It is retryable.
var ErrRateLimited = errors.New("llm: tenant rate limit exceeded")

// BudgetChecker is the pre-flight budget gate (ticket token/cost caps, org daily/monthly caps).
// It returns a *budget.ExceededError-compatible error (see IsBudgetExceeded) when over budget.
type BudgetChecker interface {
	Check(ctx context.Context, t Tenant, estimatedTokens int) error
}

// BudgetError is implemented by budget exceeded errors so the gateway can classify them
// without importing the budget package.
type BudgetError interface {
	error
	BudgetExceeded() bool
}

// IsBudgetExceeded reports whether err is a budget violation (never retryable).
func IsBudgetExceeded(err error) bool {
	var be BudgetError
	return errors.As(err, &be) && be.BudgetExceeded()
}

// CallRecord is the per-call meter row (llm_calls + usage_records).
type CallRecord struct {
	Tenant         Tenant
	IdempotencyKey string
	Stage          Stage
	Provider       string
	Model          string
	RequestedModel string
	Status         string // ok | error | invalid_output
	Attempts       int
	Usage          Usage
	CostUSD        float64
	Latency        time.Duration
	BYOK           bool
	Error          string
}

// UsageRecorder persists call records. It must be idempotent on (org, idempotency key).
type UsageRecorder interface {
	Record(ctx context.Context, rec CallRecord) error
}

// IdempotencyStore stores completed responses by (org, key). Keys are always namespaced by org,
// so there is no cross-tenant response sharing.
type IdempotencyStore interface {
	Get(ctx context.Context, orgID, key string) (*Response, bool, error)
	Put(ctx context.Context, orgID, key string, resp *Response) error
}

// --- in-memory implementations (tests, evals, single-process dev) --------------------------

// StaticKeys resolves BYO keys from a map; orgs without an entry use the platform key unless
// RequireBYOK is set.
type StaticKeys struct {
	Keys        map[string]Secret
	RequireBYOK bool
}

// Resolve implements KeyResolver.
func (s StaticKeys) Resolve(_ context.Context, orgID string) (Secret, bool, error) {
	if k, ok := s.Keys[orgID]; ok && k != "" {
		return k, true, nil
	}
	if s.RequireBYOK {
		return "", false, ErrNoKey
	}
	return "", false, nil
}

// NoopLimiter never limits.
type NoopLimiter struct{}

// Acquire implements Limiter.
func (NoopLimiter) Acquire(context.Context, LimitRequest) (func(), error) { return func() {}, nil }

// NoopBudget never blocks.
type NoopBudget struct{}

// Check implements BudgetChecker.
func (NoopBudget) Check(context.Context, Tenant, int) error { return nil }

// MemoryRecorder keeps records in memory, idempotent on (org, key).
type MemoryRecorder struct {
	mu      sync.Mutex
	Records []CallRecord
	seen    map[string]bool
}

// Record implements UsageRecorder.
func (m *MemoryRecorder) Record(_ context.Context, rec CallRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.seen == nil {
		m.seen = map[string]bool{}
	}
	k := rec.Tenant.OrgID + "|" + rec.IdempotencyKey
	if m.seen[k] {
		return nil
	}
	m.seen[k] = true
	m.Records = append(m.Records, rec)
	return nil
}

// All returns a copy of the records.
func (m *MemoryRecorder) All() []CallRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]CallRecord(nil), m.Records...)
}

// MemoryIdempotency is an in-memory IdempotencyStore.
type MemoryIdempotency struct {
	mu sync.Mutex
	m  map[string]Response
}

// Get implements IdempotencyStore.
func (s *MemoryIdempotency) Get(_ context.Context, orgID, key string) (*Response, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.m[orgID+"|"+key]
	if !ok {
		return nil, false, nil
	}
	return &r, true, nil
}

// Put implements IdempotencyStore.
func (s *MemoryIdempotency) Put(_ context.Context, orgID, key string, resp *Response) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]Response{}
	}
	s.m[orgID+"|"+key] = *resp
	return nil
}
