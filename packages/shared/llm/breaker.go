package llm

import (
	"sync"
	"time"
)

// BreakerConfig configures the per-model circuit breaker.
type BreakerConfig struct {
	// FailureThreshold consecutive retryable failures open the breaker.
	FailureThreshold int
	// OpenFor is how long an open breaker rejects calls before allowing one half-open probe.
	OpenFor time.Duration
}

// DefaultBreakerConfig is used when fields are zero.
var DefaultBreakerConfig = BreakerConfig{FailureThreshold: 5, OpenFor: 30 * time.Second}

type breakerState int

const (
	breakerClosed breakerState = iota
	breakerOpen
	breakerHalfOpen
)

type breaker struct {
	state     breakerState
	failures  int
	openUntil time.Time
	probing   bool
}

// BreakerSet holds one circuit breaker per model. It is process-local by design: each worker
// trips independently, which avoids a shared-state dependency on the hot path.
type BreakerSet struct {
	mu  sync.Mutex
	cfg BreakerConfig
	now func() time.Time
	m   map[string]*breaker
}

// NewBreakerSet creates a breaker set.
func NewBreakerSet(cfg BreakerConfig, now func() time.Time) *BreakerSet {
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = DefaultBreakerConfig.FailureThreshold
	}
	if cfg.OpenFor <= 0 {
		cfg.OpenFor = DefaultBreakerConfig.OpenFor
	}
	if now == nil {
		now = time.Now
	}
	return &BreakerSet{cfg: cfg, now: now, m: map[string]*breaker{}}
}

func (s *BreakerSet) get(model string) *breaker {
	b, ok := s.m[model]
	if !ok {
		b = &breaker{}
		s.m[model] = b
	}
	return b
}

// Allow reports whether a call to model may proceed. In half-open state exactly one probe is let
// through until it reports success or failure.
func (s *BreakerSet) Allow(model string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.get(model)
	switch b.state {
	case breakerOpen:
		if s.now().Before(b.openUntil) {
			return false
		}
		b.state, b.probing = breakerHalfOpen, true
		return true
	case breakerHalfOpen:
		if b.probing {
			return false
		}
		b.probing = true
		return true
	default:
		return true
	}
}

// Success records a successful call.
func (s *BreakerSet) Success(model string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.get(model)
	b.state, b.failures, b.probing = breakerClosed, 0, false
}

// Failure records a retryable failure (non-retryable client errors should not be reported).
func (s *BreakerSet) Failure(model string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.get(model)
	b.failures++
	if b.state == breakerHalfOpen || b.failures >= s.cfg.FailureThreshold {
		b.state, b.openUntil, b.probing = breakerOpen, s.now().Add(s.cfg.OpenFor), false
	}
}

// State returns "closed", "open" or "half_open" (for metrics/tests).
func (s *BreakerSet) State(model string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch s.get(model).state {
	case breakerOpen:
		return "open"
	case breakerHalfOpen:
		return "half_open"
	default:
		return "closed"
	}
}
