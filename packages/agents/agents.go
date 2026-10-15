// Package agents defines Alpine's agent roles — triage, refinement, prioritizer, implementer and
// independent reviewer — as prompts + JSON schemas + typed outputs, with two engines:
//
//   - LLM: real calls through the model gateway (structured outputs, repair-retry, budgets,
//     idempotency, metering are all enforced by the gateway).
//   - Stub: deterministic heuristics with synthetic usage, for local dev without model keys and
//     for workflow tests.
//
// Agent code never names a model: models are resolved per stage by the gateway routing.
package agents

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"

	"github.com/alpine-dev/alpine/packages/shared/gate"
	"github.com/alpine-dev/alpine/packages/shared/llm"
)

//go:embed schemas/*.json prompts/*.md
var assets embed.FS

func schema(name string) *llm.Schema {
	raw, err := assets.ReadFile("schemas/" + name + ".json")
	if err != nil {
		panic(fmt.Sprintf("agents: missing schema %s: %v", name, err))
	}
	return &llm.Schema{Name: name, Schema: json.RawMessage(raw)}
}

func prompt(name string) string {
	raw, err := assets.ReadFile("prompts/" + name + ".md")
	if err != nil {
		panic(fmt.Sprintf("agents: missing prompt %s: %v", name, err))
	}
	return string(raw)
}

// Schemas exposes the output schemas (for docs/evals).
func Schemas() map[string]*llm.Schema {
	return map[string]*llm.Schema{
		"triage": schema("triage"), "refine": schema("refine"), "prioritize": schema("prioritize"),
		"review": schema("review"), "implement_turn": schema("implement_turn"),
	}
}

// Call carries tenant scope and the idempotency key for one agent step. The orchestrator sets
// IdempotencyKey to "<workflow id>/<activity id>" (stable across activity retries).
type Call struct {
	Tenant         llm.Tenant
	IdempotencyKey string
}

// TicketInput is the (untrusted) ticket content given to agents.
type TicketInput struct {
	Key          string   `json:"key"`
	Title        string   `json:"title"`
	Body         string   `json:"body"`
	Source       string   `json:"source"`
	Labels       []string `json:"labels,omitempty"`
	RepoFullName string   `json:"repoFullName"`
	// OpenTickets are candidate duplicates ("externalId: title"), from pgvector search.
	OpenTickets []string `json:"openTickets,omitempty"`
}

// TriageOutput is the triage agent's structured output.
type TriageOutput struct {
	Classification        string  `json:"classification"`
	Summary               string  `json:"summary"`
	Confidence            float64 `json:"confidence"`
	AlreadyFixed          bool    `json:"already_fixed"`
	DuplicateOfExternalID *string `json:"duplicate_of_external_id"`
	Reason                string  `json:"reason"`
}

// RefineOutput is the refinement agent's structured output.
type RefineOutput struct {
	Problem                  string   `json:"problem"`
	ReproSteps               []string `json:"repro_steps"`
	AcceptanceCriteria       []string `json:"acceptance_criteria"`
	OutOfScope               []string `json:"out_of_scope"`
	Reproduced               bool     `json:"reproduced"`
	BlastRadius              int      `json:"blast_radius"`
	BlastRadiusJustification string   `json:"blast_radius_justification"`
	Priority                 string   `json:"priority"`
	Size                     string   `json:"size"`
	Confidence               float64  `json:"confidence"`
	LikelyFiles              []string `json:"likely_files"`
}

// PrioritizeOutput is the prioritizer's structured output (before policy).
type PrioritizeOutput struct {
	Decision             string `json:"decision"` // ready | needs_human
	Reason               string `json:"reason"`
	NeedsProductDecision bool   `json:"needs_product_decision"`
	ArchitectureChanging bool   `json:"architecture_changing"`
}

// ReviewFinding is one reviewer finding.
type ReviewFinding struct {
	Severity string  `json:"severity"`
	Category string  `json:"category"`
	File     *string `json:"file"`
	Line     *int    `json:"line"`
	Message  string  `json:"message"`
}

// ReviewOutput is the independent reviewer's structured output.
type ReviewOutput struct {
	Verdict            string          `json:"verdict"` // approve | request_changes | escalate
	Summary            string          `json:"summary"`
	Findings           []ReviewFinding `json:"findings"`
	RaiseBlastRadiusTo *int            `json:"raise_blast_radius_to"`
}

// ReviewInput is what the reviewer may see (never the implementer transcript).
type ReviewInput struct {
	Ticket             TicketInput `json:"ticket"`
	AcceptanceCriteria []string    `json:"acceptanceCriteria"`
	Diff               string      `json:"diff"`
	RulesFile          string      `json:"rulesFile"`
	ModuleMap          string      `json:"moduleMap"`
	BlastRadius        int         `json:"blastRadius"`
}

// ImplementInput is the context for one implementer turn.
type ImplementInput struct {
	Ticket             TicketInput    `json:"ticket"`
	AcceptanceCriteria []string       `json:"acceptanceCriteria"`
	Turn               int            `json:"turn"` // 1-based, ticket-wide
	Feedback           []gate.Finding `json:"feedback,omitempty"`
	ReviewFeedback     []string       `json:"reviewFeedback,omitempty"`
}

// ToolCallSummary summarises a tool call for metrics/transcripts.
type ToolCallSummary struct {
	Name    string `json:"name"`
	Summary string `json:"summary"`
}

// ImplementTurnOutput is one implementer turn.
type ImplementTurnOutput struct {
	Summary       string            `json:"summary"`
	ReadyForGates bool              `json:"ready_for_gates"`
	ToolCalls     []ToolCallSummary `json:"tool_calls"`
}

// Result wraps an agent output with its model usage.
type Result[T any] struct {
	Output   T
	Model    string
	Usage    llm.Usage
	CostUSD  float64
	Replayed bool
}

// Agents is implemented by the LLM and Stub engines.
type Agents interface {
	Triage(ctx context.Context, c Call, in TicketInput) (Result[TriageOutput], error)
	Refine(ctx context.Context, c Call, in TicketInput, triage TriageOutput) (Result[RefineOutput], error)
	Prioritize(ctx context.Context, c Call, in TicketInput, refine RefineOutput) (Result[PrioritizeOutput], error)
	ImplementTurn(ctx context.Context, c Call, in ImplementInput) (Result[ImplementTurnOutput], error)
	Review(ctx context.Context, c Call, in ReviewInput) (Result[ReviewOutput], error)
}
