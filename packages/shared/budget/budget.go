// Package budget enforces hard limits: per-ticket turn cap (default 100), gate-run cap (15),
// token and cost caps per ticket, and per-org daily/monthly cost and token caps. Ticket caps are
// evaluated in the Temporal workflow (deterministically, from recorded activity results) and
// again by the model gateway before every call; org caps are checked by the gateway.
package budget

import (
	"context"
	"fmt"
	"time"

	"github.com/alpine-dev/alpine/packages/shared/llm"
)

// Defaults from the spec (factory.yaml `limits:`).
const (
	DefaultMaxTurns          = 100
	DefaultMaxGateIterations = 15
	DefaultMaxCostUSD        = 20.0
	DefaultMaxTokens         = 4_000_000
)

// TicketCaps are the hard per-ticket limits. Zero disables a cap (except turns/gates, which use
// defaults via WithDefaults).
type TicketCaps struct {
	MaxTurns          int     `json:"maxTurns"`
	MaxGateIterations int     `json:"maxGateIterations"`
	MaxCostUSD        float64 `json:"maxCostUsd"`
	MaxTokens         int64   `json:"maxTokens"`
}

// WithDefaults fills zero fields with spec defaults.
func (c TicketCaps) WithDefaults() TicketCaps {
	if c.MaxTurns <= 0 {
		c.MaxTurns = DefaultMaxTurns
	}
	if c.MaxGateIterations <= 0 {
		c.MaxGateIterations = DefaultMaxGateIterations
	}
	if c.MaxCostUSD <= 0 {
		c.MaxCostUSD = DefaultMaxCostUSD
	}
	if c.MaxTokens <= 0 {
		c.MaxTokens = DefaultMaxTokens
	}
	return c
}

// TicketUsage is what a ticket has consumed so far.
type TicketUsage struct {
	Turns          int     `json:"turns"`
	GateIterations int     `json:"gateIterations"`
	CostUSD        float64 `json:"costUsd"`
	Tokens         int64   `json:"tokens"`
}

// Add returns u + o.
func (u TicketUsage) Add(o TicketUsage) TicketUsage {
	return TicketUsage{u.Turns + o.Turns, u.GateIterations + o.GateIterations, u.CostUSD + o.CostUSD, u.Tokens + o.Tokens}
}

// Scope of a budget.
type Scope string

const (
	ScopeTicket     Scope = "ticket"
	ScopeOrgDaily   Scope = "org_daily"
	ScopeOrgMonthly Scope = "org_monthly"
)

// Metric limited by a budget.
type Metric string

const (
	MetricTurns          Metric = "turns"
	MetricGateIterations Metric = "gate_iterations"
	MetricCost           Metric = "cost_usd"
	MetricTokens         Metric = "tokens"
)

// ExceededError reports which budget was hit. It satisfies llm.BudgetError.
type ExceededError struct {
	Scope  Scope
	Metric Metric
	Limit  float64
	Used   float64
}

func (e *ExceededError) Error() string {
	return fmt.Sprintf("budget exceeded: %s %s used %.4g of %.4g", e.Scope, e.Metric, e.Used, e.Limit)
}

// BudgetExceeded implements llm.BudgetError.
func (e *ExceededError) BudgetExceeded() bool { return true }

// Reason is a short human-readable escalation reason.
func (e *ExceededError) Reason() string {
	switch e.Metric {
	case MetricTurns:
		return fmt.Sprintf("turn cap reached (%d turns)", int(e.Limit))
	case MetricGateIterations:
		return fmt.Sprintf("gate-run cap reached (%d gate iterations without green)", int(e.Limit))
	case MetricCost:
		return fmt.Sprintf("%s cost cap reached ($%.2f of $%.2f)", e.Scope, e.Used, e.Limit)
	default:
		return fmt.Sprintf("%s token cap reached (%d of %d)", e.Scope, int64(e.Used), int64(e.Limit))
	}
}

// Exceeded returns the first ticket cap that usage has reached, or nil. A cap is "reached" when
// used >= limit: with max_turns=100 the 100th turn is the last one allowed.
func (c TicketCaps) Exceeded(u TicketUsage) *ExceededError {
	c = c.WithDefaults()
	switch {
	case u.Turns >= c.MaxTurns:
		return &ExceededError{ScopeTicket, MetricTurns, float64(c.MaxTurns), float64(u.Turns)}
	case u.CostUSD >= c.MaxCostUSD:
		return &ExceededError{ScopeTicket, MetricCost, c.MaxCostUSD, u.CostUSD}
	case u.Tokens >= c.MaxTokens:
		return &ExceededError{ScopeTicket, MetricTokens, float64(c.MaxTokens), float64(u.Tokens)}
	case u.GateIterations >= c.MaxGateIterations:
		return &ExceededError{ScopeTicket, MetricGateIterations, float64(c.MaxGateIterations), float64(u.GateIterations)}
	}
	return nil
}

// RemainingTurns is how many more turns are allowed.
func (c TicketCaps) RemainingTurns(u TicketUsage) int {
	c = c.WithDefaults()
	if r := c.MaxTurns - u.Turns; r > 0 {
		return r
	}
	return 0
}

// OrgCaps are per-org spend limits. Zero disables a cap.
type OrgCaps struct {
	DailyCostUSD   float64
	MonthlyCostUSD float64
	DailyTokens    int64
	MonthlyTokens  int64
}

// Spend is consumption in a window.
type Spend struct {
	CostUSD float64
	Tokens  int64
}

// Source provides usage and caps (Postgres in production, memory in tests).
type Source interface {
	Ticket(ctx context.Context, orgID, ticketID string) (TicketUsage, TicketCaps, error)
	OrgCaps(ctx context.Context, orgID string) (OrgCaps, error)
	OrgSpend(ctx context.Context, orgID string, since time.Time) (Spend, error)
}

// Checker implements llm.BudgetChecker.
type Checker struct {
	Source Source
	Now    func() time.Time
}

var _ llm.BudgetChecker = (*Checker)(nil)

// Check implements llm.BudgetChecker.
func (c *Checker) Check(ctx context.Context, t llm.Tenant, estimatedTokens int) error {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	if t.TicketID != "" {
		u, caps, err := c.Source.Ticket(ctx, t.OrgID, t.TicketID)
		if err != nil {
			return fmt.Errorf("budget: ticket usage: %w", err)
		}
		caps = caps.WithDefaults()
		if u.CostUSD >= caps.MaxCostUSD {
			return &ExceededError{ScopeTicket, MetricCost, caps.MaxCostUSD, u.CostUSD}
		}
		if u.Tokens+int64(estimatedTokens) > caps.MaxTokens {
			return &ExceededError{ScopeTicket, MetricTokens, float64(caps.MaxTokens), float64(u.Tokens + int64(estimatedTokens))}
		}
	}
	caps, err := c.Source.OrgCaps(ctx, t.OrgID)
	if err != nil {
		return fmt.Errorf("budget: org caps: %w", err)
	}
	n := now().UTC()
	windows := []struct {
		scope Scope
		since time.Time
		cost  float64
		toks  int64
	}{
		{ScopeOrgDaily, time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC), caps.DailyCostUSD, caps.DailyTokens},
		{ScopeOrgMonthly, time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, time.UTC), caps.MonthlyCostUSD, caps.MonthlyTokens},
	}
	for _, w := range windows {
		if w.cost <= 0 && w.toks <= 0 {
			continue
		}
		spend, err := c.Source.OrgSpend(ctx, t.OrgID, w.since)
		if err != nil {
			return fmt.Errorf("budget: org spend: %w", err)
		}
		if w.cost > 0 && spend.CostUSD >= w.cost {
			return &ExceededError{w.scope, MetricCost, w.cost, spend.CostUSD}
		}
		if w.toks > 0 && spend.Tokens+int64(estimatedTokens) > w.toks {
			return &ExceededError{w.scope, MetricTokens, float64(w.toks), float64(spend.Tokens + int64(estimatedTokens))}
		}
	}
	return nil
}

// MemorySource is an in-memory Source for tests and evals.
type MemorySource struct {
	Tickets map[string]TicketUsage
	Caps    map[string]TicketCaps
	Org     map[string]OrgCaps
	Spends  map[string]Spend // same spend reported for any window
}

// Ticket implements Source.
func (m *MemorySource) Ticket(_ context.Context, _ string, ticketID string) (TicketUsage, TicketCaps, error) {
	return m.Tickets[ticketID], m.Caps[ticketID], nil
}

// OrgCaps implements Source.
func (m *MemorySource) OrgCaps(_ context.Context, orgID string) (OrgCaps, error) {
	return m.Org[orgID], nil
}

// OrgSpend implements Source.
func (m *MemorySource) OrgSpend(_ context.Context, orgID string, _ time.Time) (Spend, error) {
	return m.Spends[orgID], nil
}
