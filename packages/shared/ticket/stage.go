// Package ticket holds the ticket pipeline vocabulary shared by API, orchestrator and web
// (via OpenAPI): stages, blast radius, priorities and sizes.
package ticket

// Stage is a ticket's position in the pipeline. Values match the OpenAPI Stage enum and the
// `tickets.stage` check constraint.
type Stage string

const (
	StageIntake      Stage = "intake"
	StageTriage      Stage = "triage"
	StageRefinement  Stage = "refinement"
	StagePrioritize  Stage = "prioritize"
	StageNeedsHuman  Stage = "needs_human"
	StageImplement   Stage = "implement"
	StageGates       Stage = "gates"
	StageReview      Stage = "review"
	StageMergePolicy Stage = "merge_policy"
	StagePROpen      Stage = "pr_open"
	StageDone        Stage = "done"
	StageEscalated   Stage = "escalated"
	StageClosed      Stage = "closed"
	StageDuplicate   Stage = "duplicate"
)

// AllStages in board order.
var AllStages = []Stage{
	StageIntake, StageTriage, StageRefinement, StagePrioritize, StageNeedsHuman, StageImplement,
	StageGates, StageReview, StageMergePolicy, StagePROpen, StageDone, StageEscalated,
	StageClosed, StageDuplicate,
}

// Terminal reports whether the workflow ends in this stage.
func (s Stage) Terminal() bool {
	return s == StageDone || s == StageClosed || s == StageDuplicate
}

// Valid reports whether s is a known stage.
func (s Stage) Valid() bool {
	for _, v := range AllStages {
		if v == s {
			return true
		}
	}
	return false
}

// BlastRadius levels (product §4.2): 1 leaf UI, 2 shared UI, 3 API/service, 4 data/schema/infra.
type BlastRadius int

const (
	BR1 BlastRadius = 1
	BR2 BlastRadius = 2
	BR3 BlastRadius = 3
	BR4 BlastRadius = 4
)

// Max returns the highest of the given blast radii (final BR = max(predicted, computed, reviewer)).
func Max(brs ...BlastRadius) BlastRadius {
	var m BlastRadius
	for _, b := range brs {
		if b > m {
			m = b
		}
	}
	return m
}

// Type of ticket as classified by triage.
type Type string

const (
	TypeUnknown  Type = "unknown"
	TypeBug      Type = "bug"
	TypeFeature  Type = "feature"
	TypeQuestion Type = "question"
	TypeNoise    Type = "noise"
)
