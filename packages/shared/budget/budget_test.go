package budget

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alpine-dev/alpine/packages/shared/llm"
)

func TestTicketCaps(t *testing.T) {
	caps := TicketCaps{}.WithDefaults()
	require.Equal(t, 100, caps.MaxTurns)
	require.Equal(t, 15, caps.MaxGateIterations)
	require.Nil(t, caps.Exceeded(TicketUsage{Turns: 99, GateIterations: 14, CostUSD: 19.99}))

	e := caps.Exceeded(TicketUsage{Turns: 100})
	require.NotNil(t, e)
	require.Equal(t, MetricTurns, e.Metric)
	require.Contains(t, e.Reason(), "turn cap")

	e = caps.Exceeded(TicketUsage{GateIterations: 15})
	require.Equal(t, MetricGateIterations, e.Metric)

	e = caps.Exceeded(TicketUsage{CostUSD: 20})
	require.Equal(t, MetricCost, e.Metric)

	require.Equal(t, 0, caps.RemainingTurns(TicketUsage{Turns: 120}))
	require.Equal(t, 30, caps.RemainingTurns(TicketUsage{Turns: 70}))
}

func TestCheckerTicketAndOrgBudgets(t *testing.T) {
	src := &MemorySource{
		Tickets: map[string]TicketUsage{"t1": {CostUSD: 5, Tokens: 1000}, "t2": {CostUSD: 25}},
		Caps:    map[string]TicketCaps{"t1": {MaxCostUSD: 10, MaxTokens: 2000}},
		Org:     map[string]OrgCaps{"rich": {}, "capped": {DailyCostUSD: 50, MonthlyTokens: 10_000}},
		Spends:  map[string]Spend{"capped": {CostUSD: 50}, "tok": {Tokens: 9_500}},
	}
	c := &Checker{Source: src}
	ctx := context.Background()

	require.NoError(t, c.Check(ctx, llm.Tenant{OrgID: "rich", TicketID: "t1"}, 500))

	err := c.Check(ctx, llm.Tenant{OrgID: "rich", TicketID: "t1"}, 1500)
	var ex *ExceededError
	require.True(t, errors.As(err, &ex))
	require.Equal(t, MetricTokens, ex.Metric)
	require.True(t, llm.IsBudgetExceeded(err))

	err = c.Check(ctx, llm.Tenant{OrgID: "rich", TicketID: "t2"}, 1)
	require.True(t, errors.As(err, &ex))
	require.Equal(t, ScopeTicket, ex.Scope)
	require.Equal(t, MetricCost, ex.Metric)

	err = c.Check(ctx, llm.Tenant{OrgID: "capped"}, 1)
	require.True(t, errors.As(err, &ex))
	require.Equal(t, ScopeOrgDaily, ex.Scope)

	src.Org["tok"] = OrgCaps{MonthlyTokens: 10_000}
	err = c.Check(ctx, llm.Tenant{OrgID: "tok"}, 600)
	require.True(t, errors.As(err, &ex))
	require.Equal(t, ScopeOrgMonthly, ex.Scope)
	require.NoError(t, c.Check(ctx, llm.Tenant{OrgID: "tok"}, 400))
}
