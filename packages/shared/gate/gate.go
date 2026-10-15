// Package gate defines the result schema of deterministic quality gates (lint, typecheck,
// static analysis, tests, e2e, visual, policy) and the interface the orchestrator uses to run
// them. The gate runner implementation lives in packages/gates; gate config always comes from
// the repo's BASE branch, never the agent's branch (architecture §1.2).
package gate

import (
	"context"
	"time"
)

// Status of one gate in one iteration. Values match the OpenAPI GateStatus enum and the
// `gate_results.status` column.
type Status string

const (
	StatusPass    Status = "pass"
	StatusFail    Status = "fail"
	StatusWarn    Status = "warn" // warn-only ratchets (e.g. Fallow complexity) never block
	StatusError   Status = "error"
	StatusSkipped Status = "skipped"
)

// Blocking reports whether a status blocks the PR (and feeds back to the implementer).
func (s Status) Blocking() bool { return s == StatusFail || s == StatusError }

// Severity of a finding.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

// Finding is one normalized, structured problem reported by a gate tool. It is what gets fed back
// to the implementer agent ("file, line, rule, message, suggested fix").
type Finding struct {
	File       string   `json:"file,omitempty"`
	Line       int      `json:"line,omitempty"`
	Column     int      `json:"column,omitempty"`
	Rule       string   `json:"rule,omitempty"`
	Severity   Severity `json:"severity,omitempty"`
	Message    string   `json:"message"`
	Suggestion string   `json:"suggestion,omitempty"`
}

// Result is the outcome of one gate in one gate iteration.
type Result struct {
	GateID   string        `json:"gateId"`
	Status   Status        `json:"status"`
	Duration time.Duration `json:"durationNs"`
	Findings []Finding     `json:"findings"`
	// Summary is a one-line human-readable summary ("3 type errors in 2 files").
	Summary string `json:"summary,omitempty"`
	// ArtifactKeys are object-storage keys (org/<org_id>/…) for logs, traces, screenshots.
	ArtifactKeys []string `json:"artifactKeys,omitempty"`
	// RequiresHuman is set by non-blocking gates whose output needs a human look before merge
	// (e.g. a visual diff above its threshold). The PR is then routed to human approval.
	RequiresHuman bool `json:"requiresHuman,omitempty"`
}

// Spec is one gate as configured in `.alpine/factory.yaml` (`gates:`).
type Spec struct {
	ID                   string   `json:"id" yaml:"id"`
	Run                  string   `json:"run,omitempty" yaml:"run,omitempty"`
	Ratchet              bool     `json:"ratchet,omitempty" yaml:"ratchet,omitempty"`
	MaxNewDuplicationPct *float64 `json:"maxNewDuplicationPct,omitempty" yaml:"max_new_duplication_pct,omitempty"`
	Artifacts            []string `json:"artifacts,omitempty" yaml:"artifacts,omitempty"`
	Routes               string   `json:"routes,omitempty" yaml:"routes,omitempty"`
	TimeoutSeconds       int      `json:"timeoutSeconds,omitempty" yaml:"timeout_seconds,omitempty"`
}

// RunRequest asks the gate runner to run all configured gates once (one "gate iteration").
type RunRequest struct {
	OrgID        string `json:"orgId"`
	TicketID     string `json:"ticketId"`
	RunID        string `json:"runId"`
	Iteration    int    `json:"iteration"`
	RepoFullName string `json:"repoFullName"`
	BaseSHA      string `json:"baseSha"` // gate config and baselines are read from here
	HeadSHA      string `json:"headSha"` // code under test
	// RepoDir is the per-ticket checkout path inside the sandbox.
	RepoDir string `json:"repoDir"`
	Gates   []Spec `json:"gates"`
	// ArtifactsRoot is the S3 key prefix under which gate artifacts are stored
	// (Result.ArtifactKeys are keys under it).
	ArtifactsRoot string `json:"artifactsRoot,omitempty"`
}

// Report is the outcome of one gate iteration.
type Report struct {
	Iteration int      `json:"iteration"`
	Results   []Result `json:"results"`
}

// RequiresHuman reports whether any gate asked for human approval (e.g. visual diff above
// threshold; the visual gate is warn_only by default so it never blocks).
func (r Report) RequiresHuman() bool {
	for _, res := range r.Results {
		if res.RequiresHuman {
			return true
		}
	}
	return false
}

// Green reports whether no gate blocks.
func (r Report) Green() bool {
	for _, res := range r.Results {
		if res.Status.Blocking() {
			return false
		}
	}
	return true
}

// Failures returns the blocking results, in order.
func (r Report) Failures() []Result {
	var out []Result
	for _, res := range r.Results {
		if res.Status.Blocking() {
			out = append(out, res)
		}
	}
	return out
}

// Runner runs gates in an isolated gate sandbox. packages/gates provides the implementation;
// the orchestrator depends only on this interface.
type Runner interface {
	Run(ctx context.Context, req RunRequest) (Report, error)
}
