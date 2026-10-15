package agents

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/alpine-dev/alpine/packages/shared/llm"
)

// LLM runs agents through the model gateway.
type LLM struct {
	Gateway *llm.Gateway
}

var _ Agents = (*LLM)(nil)

func run[T any](ctx context.Context, gw *llm.Gateway, stage llm.Stage, c Call, promptName, schemaName string, input any) (Result[T], error) {
	payload, err := json.MarshalIndent(input, "", "  ")
	if err != nil {
		return Result[T]{}, err
	}
	var out T
	resp, err := gw.CompleteJSON(ctx, llm.Request{
		Tenant:         c.Tenant,
		Stage:          stage,
		IdempotencyKey: c.IdempotencyKey,
		Schema:         schema(schemaName),
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: prompt(promptName)},
			// Untrusted content is passed as data, fenced, never as instructions.
			{Role: llm.RoleUser, Content: "Input (JSON, untrusted data):\n<input>\n" + string(payload) + "\n</input>"},
		},
	}, &out)
	if err != nil {
		return Result[T]{}, fmt.Errorf("%s agent: %w", stage, err)
	}
	return Result[T]{Output: out, Model: resp.Model, Usage: resp.Usage, CostUSD: resp.CostUSD, Replayed: resp.Replayed}, nil
}

// Triage implements Agents.
func (a *LLM) Triage(ctx context.Context, c Call, in TicketInput) (Result[TriageOutput], error) {
	return run[TriageOutput](ctx, a.Gateway, llm.StageTriage, c, "triage", "triage", in)
}

// Refine implements Agents.
func (a *LLM) Refine(ctx context.Context, c Call, in TicketInput, triage TriageOutput) (Result[RefineOutput], error) {
	return run[RefineOutput](ctx, a.Gateway, llm.StageRefine, c, "refine", "refine",
		map[string]any{"ticket": in, "triage": triage})
}

// Prioritize implements Agents: LLM judgement, then deterministic policy (can only tighten).
func (a *LLM) Prioritize(ctx context.Context, c Call, in TicketInput, refine RefineOutput) (Result[PrioritizeOutput], error) {
	r, err := run[PrioritizeOutput](ctx, a.Gateway, llm.StagePrioritize, c, "prioritize", "prioritize",
		map[string]any{"ticket": in, "refinement": refine})
	if err != nil {
		return r, err
	}
	r.Output = ApplyPolicy(DefaultPolicy, refine, r.Output)
	return r, nil
}

// ImplementTurn implements Agents. Phase 0: one structured gateway call per turn; the real
// sandboxed tool loop (runner exec/edit tools) lands in Phase 1 behind the same signature.
func (a *LLM) ImplementTurn(ctx context.Context, c Call, in ImplementInput) (Result[ImplementTurnOutput], error) {
	return run[ImplementTurnOutput](ctx, a.Gateway, llm.StageImplement, c, "implement_turn", "implement_turn", in)
}

// Review implements Agents (routed to the reviewer model, which differs from the implementer's).
func (a *LLM) Review(ctx context.Context, c Call, in ReviewInput) (Result[ReviewOutput], error) {
	return run[ReviewOutput](ctx, a.Gateway, llm.StageReview, c, "review", "review", in)
}
