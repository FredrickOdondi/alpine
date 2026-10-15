package workflows

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"

	"github.com/alpine-dev/alpine/packages/agents"
	"github.com/alpine-dev/alpine/packages/shared/budget"
	"github.com/alpine-dev/alpine/packages/shared/gate"
	"github.com/alpine-dev/alpine/packages/shared/ticket"
	"github.com/alpine-dev/alpine/packages/shared/ticketflow"
	"github.com/alpine-dev/alpine/services/orchestrator/internal/activities"
)

type TicketWorkflowSuite struct {
	suite.Suite
	testsuite.WorkflowTestSuite
	env *testsuite.TestWorkflowEnvironment
	act *activities.Activities

	mu          sync.Mutex
	stages      []ticket.Stage
	reasons     map[ticket.Stage]string
	implCalls   []activities.ImplementInput
	gateCalls   int
	slotHeld    int
	runStatuses []string
}

func TestTicketWorkflow(t *testing.T) { suite.Run(t, new(TicketWorkflowSuite)) }

func (s *TicketWorkflowSuite) SetupTest() {
	s.env = s.NewTestWorkflowEnvironment()
	s.act = &activities.Activities{}
	s.env.RegisterActivity(s.act)
	s.env.RegisterWorkflow(TicketWorkflow)
	s.stages, s.reasons, s.implCalls, s.gateCalls, s.slotHeld, s.runStatuses = nil, map[ticket.Stage]string{}, nil, 0, 0, nil

	s.env.OnActivity(s.act.SetStage, mock.Anything, mock.Anything).Return(func(_ context.Context, in activities.SetStageInput) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.stages = append(s.stages, in.Stage)
		s.reasons[in.Stage] = in.Reason
		return nil
	}).Maybe()
	s.env.OnActivity(s.act.AcquireTicketSlot, mock.Anything, mock.Anything).Return(func(context.Context, activities.SlotInput) error {
		s.slotHeld++
		return nil
	}).Maybe()
	s.env.OnActivity(s.act.ReleaseTicketSlot, mock.Anything, mock.Anything).Return(func(context.Context, activities.SlotInput) error {
		s.slotHeld--
		return nil
	}).Maybe()
	s.env.OnActivity(s.act.RefreshTicketSlot, mock.Anything, mock.Anything).Return(nil).Maybe()
	s.env.OnActivity(s.act.StartRun, mock.Anything, mock.Anything).Return(nil).Maybe()
	s.env.OnActivity(s.act.FinishRun, mock.Anything, mock.Anything).Return(func(_ context.Context, in activities.RunInput) error {
		s.runStatuses = append(s.runStatuses, in.Status)
		return nil
	}).Maybe()
	s.env.OnActivity(s.act.MarkMerged, mock.Anything, mock.Anything).Return(nil).Maybe()
	s.env.OnActivity(s.act.OpenPullRequest, mock.Anything, mock.Anything).Return(activities.PRResult{Number: 1234, URL: "https://github.com/acme/widgets/pull/1234", Branch: "alpine/GH-1-x"}, nil).Maybe()
}

func (s *TicketWorkflowSuite) input() ticketflow.Input {
	return ticketflow.Input{OrgID: "org", TicketID: "t", RepoID: "r", Caps: budget.TicketCaps{}, MaxParallelTicketsOrg: 5, MaxParallelTicketsRepo: 3}
}

func (s *TicketWorkflowSuite) triage(cls string) {
	s.env.OnActivity(s.act.Triage, mock.Anything, mock.Anything).Return(activities.TriageResult{Classification: cls, Usage: activities.Usage{CostUSD: 0.01}}, nil)
}

func (s *TicketWorkflowSuite) refine(br int) {
	s.env.OnActivity(s.act.Refine, mock.Anything, mock.Anything).Return(activities.RefineResult{
		Refine: agents.RefineOutput{BlastRadius: br, Priority: "P1", Size: "S", Confidence: 0.9, Reproduced: true, AcceptanceCriteria: []string{"works"}},
		Usage:  activities.Usage{CostUSD: 0.02},
	}, nil)
}

func (s *TicketWorkflowSuite) prioritize(decision string) {
	s.env.OnActivity(s.act.Prioritize, mock.Anything, mock.Anything).Return(activities.PrioritizeResult{Decision: decision, Reason: "policy"}, nil)
}

// implement: each call takes `turns` turns at `cost` each and reports ready.
func (s *TicketWorkflowSuite) implement(turns int, cost float64, ready bool) {
	s.env.OnActivity(s.act.Implement, mock.Anything, mock.Anything).Return(func(_ context.Context, in activities.ImplementInput) (activities.ImplementResult, error) {
		s.implCalls = append(s.implCalls, in)
		n := min(turns, in.TurnBudget)
		return activities.ImplementResult{Usage: activities.Usage{Turns: n, CostUSD: cost * float64(n)}, ReadyForGates: ready, HeadSHA: "abc123"}, nil
	})
}

// gates fail for the first `failing` iterations.
func (s *TicketWorkflowSuite) gates(failing int) {
	s.env.OnActivity(s.act.RunGates, mock.Anything, mock.Anything).Return(func(_ context.Context, in activities.GatesInput) (activities.GatesResult, error) {
		s.gateCalls++
		if in.Iteration <= failing {
			return activities.GatesResult{Green: false, Failures: []gate.Finding{{File: "a.ts", Line: 1, Rule: "TS2532", Message: "possibly undefined"}}}, nil
		}
		return activities.GatesResult{Green: true, Summary: "all green"}, nil
	})
}

func (s *TicketWorkflowSuite) review(verdicts ...string) {
	i := 0
	s.env.OnActivity(s.act.Review, mock.Anything, mock.Anything).Return(func(context.Context, activities.ReviewInput) (activities.ReviewResult, error) {
		v := verdicts[min(i, len(verdicts)-1)]
		i++
		r := activities.ReviewResult{Verdict: v, Summary: v}
		if v == "request_changes" {
			r.Findings = []string{"[major/tautological_test] fix the test"}
		}
		return r, nil
	})
}

func (s *TicketWorkflowSuite) signal(after time.Duration, action string) {
	s.env.RegisterDelayedCallback(func() {
		s.env.SignalWorkflow(ticketflow.SignalHumanDecision, ticketflow.HumanDecision{Action: action, Actor: "user:tester", Note: "note"})
	}, after)
}

func (s *TicketWorkflowSuite) run() ticketflow.State {
	s.env.ExecuteWorkflow(TicketWorkflow, s.input())
	s.True(s.env.IsWorkflowCompleted())
	s.NoError(s.env.GetWorkflowError())
	var st ticketflow.State
	s.NoError(s.env.GetWorkflowResult(&st))
	return st
}

func (s *TicketWorkflowSuite) TestHappyPathWithGateFeedbackLoop() {
	s.triage("bug")
	s.refine(2)
	s.prioritize("ready")
	s.implement(3, 0.10, true)
	s.gates(1)
	s.review("approve")
	s.signal(time.Hour, ticketflow.ActionMarkMerged)

	st := s.run()
	s.Equal(ticket.StageDone, st.Stage)
	s.Equal(6, st.Usage.Turns)
	s.Equal(2, st.Usage.GateIterations)
	s.Equal(1234, st.PullRequest)
	s.Equal([]ticket.Stage{
		ticket.StageTriage, ticket.StageRefinement, ticket.StagePrioritize,
		ticket.StageImplement, ticket.StageGates, ticket.StageImplement, ticket.StageGates,
		ticket.StageReview, ticket.StageMergePolicy, ticket.StagePROpen, ticket.StageDone,
	}, s.stages)
	s.Require().Len(s.implCalls, 2)
	s.Len(s.implCalls[1].Feedback, 1, "gate failures are fed back to the implementer")
	s.Equal(97, s.implCalls[1].TurnBudget)
	s.Equal(0, s.slotHeld, "slot released")
	s.Equal([]string{"succeeded"}, s.runStatuses)
	s.Contains(s.reasons[ticket.StageMergePolicy], "human merge")
}

func (s *TicketWorkflowSuite) TestTurnCapEscalates() {
	s.triage("bug")
	s.refine(1)
	s.prioritize("ready")
	s.implement(1000, 0.001, false) // never ready; burns the whole turn budget
	s.gates(0)
	s.signal(time.Hour, ticketflow.ActionClose)

	st := s.run()
	s.Equal(ticket.StageClosed, st.Stage)
	s.Contains(s.reasons[ticket.StageEscalated], "turn cap reached (100 turns)")
	s.Equal(100, st.Usage.Turns)
	s.Equal(0, s.gateCalls)
	s.Equal(0, s.slotHeld)
	s.Equal([]string{"escalated"}, s.runStatuses)
}

func (s *TicketWorkflowSuite) TestGateRunCapEscalates() {
	s.triage("bug")
	s.refine(1)
	s.prioritize("ready")
	s.implement(1, 0.01, true)
	s.gates(1000) // never green
	s.signal(time.Hour, ticketflow.ActionClose)

	st := s.run()
	s.Equal(ticket.StageClosed, st.Stage)
	s.Contains(s.reasons[ticket.StageEscalated], "gate-run cap reached (15")
	s.Equal(15, s.gateCalls)
	s.Len(s.implCalls, 15)
}

func (s *TicketWorkflowSuite) TestCostCapEscalatesBeforeGates() {
	s.triage("bug")
	s.refine(1)
	s.prioritize("ready")
	s.implement(5, 5.0, true) // $25 > $20 cap
	s.gates(0)
	s.signal(time.Hour, ticketflow.ActionClose)

	s.run()
	s.Contains(s.reasons[ticket.StageEscalated], "cost cap")
	s.Equal(0, s.gateCalls)
}

func (s *TicketWorkflowSuite) TestBudgetErrorFromActivityEscalatesThenResumes() {
	s.triage("bug")
	s.refine(1)
	s.prioritize("ready")
	calls := 0
	s.env.OnActivity(s.act.Implement, mock.Anything, mock.Anything).Return(func(context.Context, activities.ImplementInput) (activities.ImplementResult, error) {
		calls++
		if calls == 1 {
			return activities.ImplementResult{}, temporal.NewNonRetryableApplicationError("org_daily cost cap reached ($100.00 of $100.00)", activities.ErrTypeBudgetExceeded, nil)
		}
		return activities.ImplementResult{Usage: activities.Usage{Turns: 2}, ReadyForGates: true, HeadSHA: "def"}, nil
	})
	s.gates(0)
	s.review("approve")
	s.signal(time.Hour, ticketflow.ActionResume)
	s.signal(2*time.Hour, ticketflow.ActionMarkMerged)

	st := s.run()
	s.Equal(ticket.StageDone, st.Stage)
	s.Equal(2, st.Attempt)
	s.Contains(s.reasons[ticket.StageEscalated], "org_daily cost cap")
	s.Equal(2, calls, "budget errors are not retried by Temporal")
}

func (s *TicketWorkflowSuite) TestNeedsHumanApproveAndReject() {
	s.triage("bug")
	s.refine(4)
	s.prioritize("needs_human")
	s.signal(time.Minute, ticketflow.ActionReject)
	st := s.run()
	s.Equal(ticket.StageClosed, st.Stage)
	s.Contains(s.stages, ticket.StageNeedsHuman)
	s.Empty(s.implCalls)
}

func (s *TicketWorkflowSuite) TestNeedsHumanApproveContinues() {
	s.triage("bug")
	s.refine(4)
	s.prioritize("needs_human")
	s.implement(2, 0.01, true)
	s.gates(0)
	s.review("approve")
	s.signal(time.Minute, ticketflow.ActionApprove)
	s.signal(time.Hour, ticketflow.ActionMarkMerged)
	st := s.run()
	s.Equal(ticket.StageDone, st.Stage)
}

func (s *TicketWorkflowSuite) TestNoiseIsClosedWithoutRefinement() {
	s.triage("noise")
	st := s.run()
	s.Equal(ticket.StageClosed, st.Stage)
	s.Equal([]ticket.Stage{ticket.StageTriage, ticket.StageClosed}, s.stages)
}

func (s *TicketWorkflowSuite) TestReviewRequestChangesLoopsBackToImplement() {
	s.triage("bug")
	s.refine(1)
	s.prioritize("ready")
	s.implement(2, 0.01, true)
	s.gates(0)
	s.review("request_changes", "approve")
	s.signal(time.Hour, ticketflow.ActionMarkMerged)
	st := s.run()
	s.Equal(ticket.StageDone, st.Stage)
	s.Require().Len(s.implCalls, 2)
	s.Equal([]string{"[major/tautological_test] fix the test"}, s.implCalls[1].ReviewFeedback)
	s.Equal(4, st.Usage.Turns)
}

func (s *TicketWorkflowSuite) TestQueryState() {
	s.triage("bug")
	s.refine(1)
	s.prioritize("needs_human")
	s.env.RegisterDelayedCallback(func() {
		v, err := s.env.QueryWorkflow(ticketflow.QueryState)
		require.NoError(s.T(), err)
		var st ticketflow.State
		require.NoError(s.T(), v.Get(&st))
		s.Equal(ticket.StageNeedsHuman, st.Stage)
		s.Equal(100, st.Caps.MaxTurns)
		s.env.SignalWorkflow(ticketflow.SignalHumanDecision, ticketflow.HumanDecision{Action: ticketflow.ActionClose})
	}, time.Minute)
	s.run()
}

func (s *TicketWorkflowSuite) TestVisualDiffRoutesToHumanApproval() {
	s.triage("bug")
	s.refine(1)
	s.prioritize("ready")
	s.implement(1, 0.01, true)
	s.env.OnActivity(s.act.RunGates, mock.Anything, mock.Anything).Return(func(_ context.Context, in activities.GatesInput) (activities.GatesResult, error) {
		s.Equal("abc123", in.HeadSHA)
		return activities.GatesResult{Green: true, RequiresHuman: true}, nil
	})
	s.review("approve")
	s.signal(time.Hour, ticketflow.ActionMarkMerged)
	st := s.run()
	s.Equal(ticket.StageDone, st.Stage)
	s.Contains(s.reasons[ticket.StageMergePolicy], "visual diff above threshold")
}
