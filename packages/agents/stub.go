package agents

import (
	"context"
	"fmt"
	"strings"

	"github.com/alpine-dev/alpine/packages/shared/llm"
)

// Stub is a deterministic engine for local dev (no model keys) and tests. It fakes plausible
// usage so turn/cost accounting, caps and metering can be exercised end-to-end.
type Stub struct {
	// TurnsPerAttempt: implement reports ready_for_gates every N turns (default 3).
	TurnsPerAttempt int
	// CostPerTurnUSD is the synthetic cost of one implementer turn (default $0.05).
	CostPerTurnUSD float64
	// ReviewVerdict forces the review verdict (default approve).
	ReviewVerdict string
}

var _ Agents = Stub{}

func (s Stub) usage(in, out int64, cost float64, model string) (llm.Usage, float64, string) {
	return llm.Usage{InputTokens: in, OutputTokens: out}, cost, model
}

func lower(in TicketInput) string { return strings.ToLower(in.Title + " " + in.Body) }

// Triage implements Agents.
func (s Stub) Triage(_ context.Context, _ Call, in TicketInput) (Result[TriageOutput], error) {
	text := lower(in)
	cls := "bug"
	switch {
	case strings.TrimSpace(in.Title) == "" || strings.Contains(text, "spam") || strings.Contains(text, "[test]"):
		cls = "noise"
	case strings.Contains(text, "feature") || strings.Contains(text, "would be nice") || strings.HasPrefix(strings.ToLower(in.Title), "add "):
		cls = "feature"
	case strings.HasSuffix(strings.TrimSpace(in.Title), "?") || strings.Contains(text, "how do i"):
		cls = "question"
	}
	u, c, m := s.usage(900, 120, 0.0005, "stub-triage")
	return Result[TriageOutput]{Output: TriageOutput{
		Classification: cls, Summary: truncate(in.Title, 200), Confidence: 0.8, Reason: "stub heuristic classification",
	}, Model: m, Usage: u, CostUSD: c}, nil
}

// Refine implements Agents.
func (s Stub) Refine(_ context.Context, _ Call, in TicketInput, _ TriageOutput) (Result[RefineOutput], error) {
	text := lower(in)
	br, prio, size := 1, "P2", "S"
	switch {
	case containsAny(text, "migration", "schema", "database", "payment", "billing", "terraform"):
		br, size = 4, "M"
	case containsAny(text, "api", "endpoint", "auth", "login", "500", "server"):
		br, prio = 3, "P1"
	case containsAny(text, "button", "design system", "component", "theme"):
		br = 2
	}
	if containsAny(text, "outage", "production down", "p0", "crash") {
		prio = "P0"
	}
	u, c, m := s.usage(2500, 600, 0.002, "stub-refine")
	return Result[RefineOutput]{Output: RefineOutput{
		Problem:                  truncate(in.Title+": "+in.Body, 600),
		ReproSteps:               []string{"Open the affected page", "Perform the action from the report", "Observe the error"},
		AcceptanceCriteria:       []string{"The reported behaviour no longer occurs", "A regression test covers the scenario"},
		OutOfScope:               []string{"Unrelated refactors"},
		Reproduced:               !strings.Contains(text, "cannot reproduce"),
		BlastRadius:              br,
		BlastRadiusJustification: fmt.Sprintf("stub keyword heuristic → BR%d", br),
		Priority:                 prio, Size: size, Confidence: 0.75,
	}, Model: m, Usage: u, CostUSD: c}, nil
}

// Prioritize implements Agents.
func (s Stub) Prioritize(_ context.Context, _ Call, _ TicketInput, r RefineOutput) (Result[PrioritizeOutput], error) {
	out := ApplyPolicy(DefaultPolicy, r, PrioritizeOutput{Decision: "ready", Reason: "clear AC and reproduced"})
	u, c, m := s.usage(1200, 80, 0.0002, "stub-prioritize")
	return Result[PrioritizeOutput]{Output: out, Model: m, Usage: u, CostUSD: c}, nil
}

// ImplementTurn implements Agents.
func (s Stub) ImplementTurn(_ context.Context, _ Call, in ImplementInput) (Result[ImplementTurnOutput], error) {
	n := s.TurnsPerAttempt
	if n <= 0 {
		n = 3
	}
	cost := s.CostPerTurnUSD
	if cost <= 0 {
		cost = 0.05
	}
	out := ImplementTurnOutput{
		Summary:       fmt.Sprintf("turn %d: edited files for %s", in.Turn, in.Ticket.Key),
		ReadyForGates: in.Turn%n == 0,
		ToolCalls:     []ToolCallSummary{{Name: "read_file", Summary: "inspect code"}, {Name: "edit_file", Summary: "apply change"}},
	}
	if len(in.Feedback) > 0 {
		out.ToolCalls = append(out.ToolCalls, ToolCallSummary{Name: "fix_gate_findings", Summary: fmt.Sprintf("%d findings", len(in.Feedback))})
	}
	u, c, m := s.usage(8000, 900, cost, "stub-implement")
	return Result[ImplementTurnOutput]{Output: out, Model: m, Usage: u, CostUSD: c}, nil
}

// Review implements Agents.
func (s Stub) Review(_ context.Context, _ Call, _ ReviewInput) (Result[ReviewOutput], error) {
	v := s.ReviewVerdict
	if v == "" {
		v = "approve"
	}
	out := ReviewOutput{Verdict: v, Summary: "stub review", Findings: []ReviewFinding{}}
	if v == "request_changes" {
		out.Findings = append(out.Findings, ReviewFinding{Severity: "major", Category: "tautological_test", Message: "test asserts the mock"})
	}
	u, c, m := s.usage(6000, 400, 0.004, "stub-review")
	return Result[ReviewOutput]{Output: out, Model: m, Usage: u, CostUSD: c}, nil
}

func containsAny(s string, subs ...string) bool {
	for _, x := range subs {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
