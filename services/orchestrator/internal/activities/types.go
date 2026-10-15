// Package activities holds the orchestrator's Temporal activities. Workflow code only makes
// decisions on recorded results; every LLM call, sandbox call, DB write and GitHub call happens
// here. Activities are idempotent: DB writes are keyed by run/turn/iteration or by the Temporal
// activity id, LLM calls by "<workflow id>/<activity id>/<seq>", and external side effects by a
// logical step key recorded in the side_effects ledger.
package activities

import (
	"github.com/alpine-dev/alpine/packages/agents"
	"github.com/alpine-dev/alpine/packages/shared/gate"
	"github.com/alpine-dev/alpine/packages/shared/ticket"
)

// Error types returned as non-retryable Temporal application errors.
const (
	ErrTypeBudgetExceeded = "BudgetExceeded"
	ErrTypeInvalidOutput  = "InvalidModelOutput"
	ErrTypeNotFound       = "NotFound"
	ErrTypeSlotLost       = "TicketSlotLost"
)

// Ref identifies a ticket.
type Ref struct {
	OrgID    string `json:"orgId"`
	TicketID string `json:"ticketId"`
	RepoID   string `json:"repoId"`
	Priority string `json:"priority,omitempty"`
}

// Usage is spend attributed to a ticket by one activity.
type Usage struct {
	Turns   int     `json:"turns"`
	CostUSD float64 `json:"costUsd"`
	Tokens  int64   `json:"tokens"`
}

// SetStageInput moves a ticket to a stage.
type SetStageInput struct {
	Ref    Ref          `json:"ref"`
	Stage  ticket.Stage `json:"stage"`
	Reason string       `json:"reason,omitempty"`
	Actor  string       `json:"actor,omitempty"`
}

// TriageResult is the triage activity output.
type TriageResult struct {
	Classification string `json:"classification"`
	Reason         string `json:"reason"`
	DuplicateOf    string `json:"duplicateOf,omitempty"`
	AlreadyFixed   bool   `json:"alreadyFixed"`
	Usage          Usage  `json:"usage"`
}

// RefineResult is the refinement activity output.
type RefineResult struct {
	Refine agents.RefineOutput `json:"refine"`
	Usage  Usage               `json:"usage"`
}

// PrioritizeInput feeds the prioritizer.
type PrioritizeInput struct {
	Ref    Ref                 `json:"ref"`
	Refine agents.RefineOutput `json:"refine"`
}

// PrioritizeResult is the prioritizer output (policy applied).
type PrioritizeResult struct {
	Decision string `json:"decision"` // ready | needs_human
	Reason   string `json:"reason"`
	Usage    Usage  `json:"usage"`
}

// SlotInput acquires/releases a per-org/per-repo ticket concurrency slot.
type SlotInput struct {
	Ref       Ref `json:"ref"`
	OrgLimit  int `json:"orgLimit"`
	RepoLimit int `json:"repoLimit"`
}

// RunInput starts/finishes an agent run.
type RunInput struct {
	Ref    Ref    `json:"ref"`
	RunID  string `json:"runId"`
	Kind   string `json:"kind"`
	Status string `json:"status,omitempty"`
}

// ImplementInput runs implementer turns until ready for gates or out of budget.
type ImplementInput struct {
	Ref                Ref            `json:"ref"`
	RunID              string         `json:"runId"`
	AcceptanceCriteria []string       `json:"acceptanceCriteria"`
	TurnsSoFar         int            `json:"turnsSoFar"`    // ticket-wide turns already used (this attempt)
	TurnBudget         int            `json:"turnBudget"`    // max turns this activity may take
	CostBudgetUSD      float64        `json:"costBudgetUsd"` // remaining ticket cost budget
	Feedback           []gate.Finding `json:"feedback,omitempty"`
	ReviewFeedback     []string       `json:"reviewFeedback,omitempty"`
}

// ImplementResult summarises the turns taken.
type ImplementResult struct {
	Usage         Usage  `json:"usage"`
	ReadyForGates bool   `json:"readyForGates"`
	HeadSHA       string `json:"headSha"`
	// BaseSHA is the base-branch commit the work branch was cut from.
	BaseSHA string `json:"baseSha"`
	// Branch is the ticket's work branch, alpine/<ticket_ulid>-<slug>.
	Branch  string `json:"branch"`
	Resumed bool   `json:"resumed"` // resumed after a worker crash/retry
	// RepoDir is the checkout the live implementer committed in (gates and review use it).
	RepoDir string `json:"repoDir,omitempty"`
	// Summary is the implementer's own description of the change.
	Summary string `json:"summary,omitempty"`
	// StopReason explains a not-ready result (turn_cap, cost_cap, no_change, ...).
	StopReason string `json:"stopReason,omitempty"`
}

// GatesInput runs one gate iteration.
type GatesInput struct {
	Ref       Ref    `json:"ref"`
	RunID     string `json:"runId"`
	Iteration int    `json:"iteration"`
	HeadSHA   string `json:"headSha"`
	BaseSHA   string `json:"baseSha"`
	// RepoDir is the per-ticket sandbox checkout path.
	RepoDir string `json:"repoDir"`
}

// GatesResult is the outcome of one gate iteration.
type GatesResult struct {
	Green    bool           `json:"green"`
	Failures []gate.Finding `json:"failures,omitempty"`
	Summary  string         `json:"summary"`
	// RequiresHuman: a non-blocking gate (visual diff above threshold) wants human approval.
	RequiresHuman bool `json:"requiresHuman,omitempty"`
}

// ReviewInput runs the independent reviewer.
type ReviewInput struct {
	Ref                Ref      `json:"ref"`
	HeadSHA            string   `json:"headSha"`
	BaseSHA            string   `json:"baseSha,omitempty"`
	RepoDir            string   `json:"repoDir,omitempty"`
	AcceptanceCriteria []string `json:"acceptanceCriteria"`
	BlastRadius        int      `json:"blastRadius"`
}

// ReviewResult is the reviewer's verdict.
type ReviewResult struct {
	Verdict  string   `json:"verdict"`
	Summary  string   `json:"summary"`
	Findings []string `json:"findings,omitempty"`
	RaiseBR  int      `json:"raiseBr,omitempty"`
	Usage    Usage    `json:"usage"`
}

// PRInput opens (or finds) the ticket's pull request.
type PRInput struct {
	Ref         Ref    `json:"ref"`
	StepKey     string `json:"stepKey"` // logical idempotency key, e.g. "<wf id>/pr/attempt-1"
	HeadSHA     string `json:"headSha"`
	BlastRadius int    `json:"blastRadius"`
	Summary     string `json:"summary"`
}

// PRResult identifies the PR.
type PRResult struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
	Branch string `json:"branch"`
}

// MergedInput records a human merge.
type MergedInput struct {
	Ref   Ref    `json:"ref"`
	Actor string `json:"actor"`
}
