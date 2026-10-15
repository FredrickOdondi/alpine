package gates

import (
	"time"

	"github.com/alpine-dev/alpine/packages/shared/gate"
)

// RESULT TYPES
//
// The result schema is owned by packages/shared/gate (jeff/core). Status,
// Severity and Finding are aliases of the shared types, and GateResult embeds
// gate.Result so its JSON is a superset of the shared shape (gateId, status,
// durationNs, findings, summary, artifactKeys) plus runner details. Use
// GateResult.Shared / RunResult.Report to hand results to code that only
// knows the shared contract.

// Status is the outcome of a single gate or of a whole run (shared enum).
type Status = gate.Status

// Severity of a finding (shared enum).
type Severity = gate.Severity

// Finding is one normalized problem reported by a gate tool (shared type).
type Finding = gate.Finding

const (
	StatusPass    = gate.StatusPass
	StatusFail    = gate.StatusFail
	StatusWarn    = gate.StatusWarn    // failed, but the gate is configured warn_only
	StatusError   = gate.StatusError   // gate could not be evaluated (bad command, unparseable output)
	StatusSkipped = gate.StatusSkipped // disabled, not handled by this package, or run aborted

	SeverityError   = gate.SeverityError
	SeverityWarning = gate.SeverityWarning
	SeverityInfo    = gate.SeverityInfo
)

// GateType is the category of a gate. It determines run order.
type GateType string

const (
	TypeLint        GateType = "lint"
	TypeTypecheck   GateType = "typecheck"
	TypeTest        GateType = "test"
	TypeDuplication GateType = "duplication"
	TypeComplexity  GateType = "complexity"
	TypeDeadCode    GateType = "deadcode"
	TypeOther       GateType = "other"
	// Types handled by other components (Playwright/visual sandboxes). They are
	// accepted in the config and reported as skipped by this package.
	TypeE2E    GateType = "e2e"
	TypeVisual GateType = "visual"
)

// typeOrder is the stable execution order: cheap, high-signal gates first.
var typeOrder = map[GateType]int{
	TypeLint: 0, TypeTypecheck: 1, TypeTest: 2, TypeDuplication: 3,
	TypeComplexity: 4, TypeDeadCode: 5, TypeOther: 6, TypeE2E: 7, TypeVisual: 8,
}

// GateResult is the structured result of one gate: the shared gate.Result
// (GateID, Status, Duration, Findings, Summary, ArtifactKeys) plus details
// specific to this runner.
type GateResult struct {
	gate.Result
	Type     GateType `json:"type"`
	Parser   string   `json:"parser,omitempty"`
	ExitCode int      `json:"exitCode"` // -1 if the process did not exit normally (timeout, failed to start, not run)
	TimedOut bool     `json:"timedOut,omitempty"`
	// Feedback is a short, deduped, length-capped text for the agent.
	Feedback string `json:"feedback,omitempty"`
}

// Shared returns the shared gate.Result.
func (r GateResult) Shared() gate.Result { return r.Result }

// RunResult is the result of running all configured gates once.
type RunResult struct {
	JobID   string       `json:"jobId"`
	BaseRef string       `json:"baseRef"`
	Status  Status       `json:"status"` // pass, fail or error; skipped when the cap was reached
	Gates   []GateResult `json:"gates"`
	// GateRunsUsed is the job's gate-run count including this run (the gate
	// iteration number).
	GateRunsUsed int           `json:"gateRunsUsed"`
	GateRunCap   int           `json:"gateRunCap"`
	CapReached   bool          `json:"capReached,omitempty"`
	Duration     time.Duration `json:"durationNs"`
	// Feedback is the combined agent feedback for the whole run.
	Feedback string `json:"feedback,omitempty"`
}

// Passed reports whether no gate blocks (warn and skipped do not block).
func (r RunResult) Passed() bool { return r.Status == StatusPass }

// Report converts the run into the shared gate.Report (iteration = gate runs used).
func (r RunResult) Report() gate.Report {
	rep := gate.Report{Iteration: r.GateRunsUsed, Results: make([]gate.Result, 0, len(r.Gates))}
	for _, g := range r.Gates {
		rep.Results = append(rep.Results, g.Result)
	}
	return rep
}
