// Package workflows holds the per-ticket Temporal workflow. It is deterministic: it only
// sequences activities and makes decisions on their recorded results, enforcing the hard caps
// (turns, gate iterations, cost/tokens) and routing to escalate-to-human states.
package workflows

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/alpine-dev/alpine/packages/agents"
	"github.com/alpine-dev/alpine/packages/shared/budget"
	"github.com/alpine-dev/alpine/packages/shared/gate"
	"github.com/alpine-dev/alpine/packages/shared/ticket"
	"github.com/alpine-dev/alpine/packages/shared/ticketflow"
	"github.com/alpine-dev/alpine/services/orchestrator/internal/activities"
)

// a is used only for method references (activity names); it is never called directly.
var a *activities.Activities

type phase int

const (
	phaseTriage phase = iota
	phaseRefine
	phasePrioritize
	phaseNeedsHuman
	phaseBuild
	phaseMerge
	phaseEscalated
	phaseEnd
)

type flow struct {
	ctx   workflow.Context
	in    ticketflow.Input
	ref   activities.Ref
	state ticketflow.State
	sig   workflow.ReceiveChannel

	refine      agents.RefineOutput
	br          int
	humanReason string
	resumeTo    phase
	runID       string
	slotHeld    bool
	prFeedback  []string
	headSHA     string
	// visualReview: a gate (visual diff above threshold) requires human approval of the PR.
	visualReview bool
}

// TicketWorkflow drives one ticket: triage → refinement → prioritize → implement ⇄ gates →
// review → merge policy → PR open → done, with needs_human and escalated waiting states.
func TicketWorkflow(ctx workflow.Context, in ticketflow.Input) (ticketflow.State, error) {
	f := &flow{
		ctx: ctx,
		in:  in,
		ref: activities.Ref{OrgID: in.OrgID, TicketID: in.TicketID, RepoID: in.RepoID},
		sig: workflow.GetSignalChannel(ctx, ticketflow.SignalHumanDecision),
	}
	f.state.Caps = in.Caps.WithDefaults()
	f.state.Stage = ticket.StageIntake
	f.state.Attempt = 1
	if err := workflow.SetQueryHandler(ctx, ticketflow.QueryState, func() (ticketflow.State, error) { return f.state, nil }); err != nil {
		return f.state, err
	}

	p := phaseTriage
	for p != phaseEnd {
		switch p {
		case phaseTriage:
			p = f.triage()
		case phaseRefine:
			p = f.refinement()
		case phasePrioritize:
			p = f.prioritize()
		case phaseNeedsHuman:
			p = f.needsHuman()
		case phaseBuild:
			p = f.build()
		case phaseMerge:
			p = f.merge()
		case phaseEscalated:
			p = f.escalated()
		}
	}
	return f.state, nil
}

// ---- activity options -----------------------------------------------------------------------

var nonRetryable = []string{activities.ErrTypeBudgetExceeded, activities.ErrTypeInvalidOutput, activities.ErrTypeNotFound, activities.ErrTypeSlotLost}

func (f *flow) control() workflow.Context {
	return workflow.WithActivityOptions(f.ctx, workflow.ActivityOptions{
		TaskQueue:           ticketflow.TaskQueueControl,
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{ // bookkeeping must eventually succeed: retry forever
			InitialInterval: time.Second, BackoffCoefficient: 2, MaximumInterval: time.Minute,
			NonRetryableErrorTypes: nonRetryable,
		},
	})
}

func (f *flow) agent(timeout, heartbeat time.Duration) workflow.Context {
	return workflow.WithActivityOptions(f.ctx, workflow.ActivityOptions{
		TaskQueue:           ticketflow.AgentTaskQueue(f.ref.Priority),
		StartToCloseTimeout: timeout,
		HeartbeatTimeout:    heartbeat,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval: 2 * time.Second, BackoffCoefficient: 2, MaximumInterval: 2 * time.Minute,
			MaximumAttempts: 5, NonRetryableErrorTypes: nonRetryable,
		},
	})
}

func (f *flow) gates() workflow.Context {
	return workflow.WithActivityOptions(f.ctx, workflow.ActivityOptions{
		TaskQueue:           ticketflow.TaskQueueGates,
		StartToCloseTimeout: time.Hour,
		HeartbeatTimeout:    2 * time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{InitialInterval: 5 * time.Second, MaximumAttempts: 3, NonRetryableErrorTypes: nonRetryable},
	})
}

func (f *flow) slots() workflow.Context {
	return workflow.WithActivityOptions(f.ctx, workflow.ActivityOptions{
		TaskQueue:           ticketflow.TaskQueueControl,
		StartToCloseTimeout: 24 * time.Hour,
		HeartbeatTimeout:    time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{InitialInterval: time.Second, MaximumInterval: time.Minute},
	})
}

// ---- helpers -------------------------------------------------------------------------------

func (f *flow) setStage(s ticket.Stage, reason, actor string) {
	f.state.Stage = s
	_ = workflow.ExecuteActivity(f.control(), a.SetStage, activities.SetStageInput{Ref: f.ref, Stage: s, Reason: reason, Actor: actor}).Get(f.ctx, nil)
}

func (f *flow) addUsage(u activities.Usage) {
	f.state.Usage = f.state.Usage.Add(budget.TicketUsage{Turns: u.Turns, CostUSD: u.CostUSD, Tokens: u.Tokens})
}

func (f *flow) newID() string {
	var id string
	_ = workflow.SideEffect(f.ctx, func(workflow.Context) any { return uuid.NewString() }).Get(&id)
	return id
}

// errReason turns an activity error into an escalation reason.
func errReason(stage string, err error) string {
	var appErr *temporal.ApplicationError
	if errors.As(err, &appErr) {
		switch appErr.Type() {
		case activities.ErrTypeBudgetExceeded:
			return appErr.Message()
		case activities.ErrTypeInvalidOutput:
			return stage + ": model output failed schema validation after repairs"
		}
	}
	msg := err.Error()
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return stage + " failed: " + msg
}

func (f *flow) escalate(reason string, resumeTo phase) phase {
	f.state.EscalationReason = reason
	f.resumeTo = resumeTo
	return phaseEscalated
}

func (f *flow) wait() ticketflow.HumanDecision {
	var d ticketflow.HumanDecision
	f.sig.Receive(f.ctx, &d)
	return d
}

func (f *flow) closeTicket(reason, actor string) phase {
	f.setStage(ticket.StageClosed, reason, actor)
	return phaseEnd
}

// ---- phases --------------------------------------------------------------------------------

func (f *flow) triage() phase {
	f.setStage(ticket.StageTriage, "", "agent:triage")
	var res activities.TriageResult
	if err := workflow.ExecuteActivity(f.agent(10*time.Minute, 0), a.Triage, f.ref).Get(f.ctx, &res); err != nil {
		return f.escalate(errReason("triage", err), phaseTriage)
	}
	f.addUsage(res.Usage)
	switch {
	case res.Classification == "noise":
		return f.closeTicket("closed as noise: "+res.Reason, "agent:triage")
	case res.DuplicateOf != "":
		f.setStage(ticket.StageDuplicate, "duplicate of "+res.DuplicateOf, "agent:triage")
		return phaseEnd
	case res.AlreadyFixed:
		return f.closeTicket("already fixed: "+res.Reason, "agent:triage")
	case res.Classification == "question":
		f.humanReason = "question: needs a human answer"
		return phaseNeedsHuman
	}
	return phaseRefine
}

func (f *flow) refinement() phase {
	f.setStage(ticket.StageRefinement, "", "agent:refine")
	var res activities.RefineResult
	if err := workflow.ExecuteActivity(f.agent(30*time.Minute, 0), a.Refine, f.ref).Get(f.ctx, &res); err != nil {
		return f.escalate(errReason("refinement", err), phaseRefine)
	}
	f.addUsage(res.Usage)
	f.refine = res.Refine
	f.br = res.Refine.BlastRadius
	f.ref.Priority = res.Refine.Priority // routes later agent work to the priority task queue
	return phasePrioritize
}

func (f *flow) prioritize() phase {
	f.setStage(ticket.StagePrioritize, "", "agent:prioritizer")
	var res activities.PrioritizeResult
	if err := workflow.ExecuteActivity(f.agent(10*time.Minute, 0), a.Prioritize, activities.PrioritizeInput{Ref: f.ref, Refine: f.refine}).Get(f.ctx, &res); err != nil {
		return f.escalate(errReason("prioritize", err), phasePrioritize)
	}
	f.addUsage(res.Usage)
	if res.Decision == "needs_human" {
		f.humanReason = res.Reason
		return phaseNeedsHuman
	}
	return phaseBuild
}

func (f *flow) needsHuman() phase {
	f.setStage(ticket.StageNeedsHuman, f.humanReason, "agent:prioritizer")
	for {
		d := f.wait()
		switch d.Action {
		case ticketflow.ActionApprove:
			_ = workflow.ExecuteActivity(f.control(), a.SetStage, activities.SetStageInput{Ref: f.ref, Stage: ticket.StagePrioritize, Reason: "approved for agent: " + d.Note, Actor: d.Actor}).Get(f.ctx, nil)
			return phaseBuild
		case ticketflow.ActionReject, ticketflow.ActionClose:
			return f.closeTicket("rejected by human: "+d.Note, d.Actor)
		}
		workflow.GetLogger(f.ctx).Warn("ignored signal in needs_human", "action", d.Action)
	}
}

func (f *flow) acquireSlot() {
	if f.slotHeld {
		return
	}
	_ = workflow.ExecuteActivity(f.slots(), a.AcquireTicketSlot, f.slotInput()).Get(f.ctx, nil)
	f.slotHeld = true
}

func (f *flow) releaseSlot() {
	if !f.slotHeld {
		return
	}
	dctx, _ := workflow.NewDisconnectedContext(f.control())
	_ = workflow.ExecuteActivity(dctx, a.ReleaseTicketSlot, f.slotInput()).Get(f.ctx, nil)
	f.slotHeld = false
}

func (f *flow) slotInput() activities.SlotInput {
	return activities.SlotInput{Ref: f.ref, OrgLimit: f.in.MaxParallelTicketsOrg, RepoLimit: f.in.MaxParallelTicketsRepo}
}

func (f *flow) finishRun(status string) {
	if f.runID == "" {
		return
	}
	_ = workflow.ExecuteActivity(f.control(), a.FinishRun, activities.RunInput{Ref: f.ref, RunID: f.runID, Status: status}).Get(f.ctx, nil)
	f.runID = ""
}

// build runs the implement ⇄ gates loop and the independent review, enforcing all caps.
func (f *flow) build() phase {
	caps := f.state.Caps
	f.acquireSlot()
	if f.runID == "" {
		f.runID = f.newID()
		_ = workflow.ExecuteActivity(f.control(), a.StartRun, activities.RunInput{Ref: f.ref, RunID: f.runID, Kind: "implement"}).Get(f.ctx, nil)
	}
	var feedback []gate.Finding
	reviewFeedback := f.prFeedback
	f.prFeedback = nil
	reviewRounds := 0
	maxReview := f.in.MaxReviewRounds
	if maxReview <= 0 {
		maxReview = 3
	}
	fail := func(reason string) phase {
		f.finishRun("escalated")
		f.releaseSlot()
		return f.escalate(reason, phaseBuild)
	}

	for {
		if ex := caps.Exceeded(f.state.Usage); ex != nil {
			return fail(ex.Reason())
		}
		if err := workflow.ExecuteActivity(f.control(), a.RefreshTicketSlot, f.slotInput()).Get(f.ctx, nil); err != nil {
			f.slotHeld = false
			f.acquireSlot()
		}

		f.setStage(ticket.StageImplement, implementReason(feedback, reviewFeedback), "agent:implementer")
		var impl activities.ImplementResult
		err := workflow.ExecuteActivity(f.agent(2*time.Hour, 2*time.Minute), a.Implement, activities.ImplementInput{
			Ref: f.ref, RunID: f.runID, AcceptanceCriteria: f.refine.AcceptanceCriteria,
			TurnsSoFar: f.state.Usage.Turns, TurnBudget: caps.RemainingTurns(f.state.Usage),
			CostBudgetUSD: caps.MaxCostUSD - f.state.Usage.CostUSD, Feedback: feedback, ReviewFeedback: reviewFeedback,
		}).Get(f.ctx, &impl)
		f.addUsage(impl.Usage)
		if err != nil {
			return fail(errReason("implement", err))
		}
		if f.state.Usage.CostUSD >= caps.MaxCostUSD || f.state.Usage.Tokens >= caps.MaxTokens {
			return fail(caps.Exceeded(budget.TicketUsage{CostUSD: f.state.Usage.CostUSD, Tokens: f.state.Usage.Tokens}).Reason())
		}
		if !impl.ReadyForGates {
			if ex := caps.Exceeded(f.state.Usage); ex != nil {
				return fail(ex.Reason())
			}
			return fail("implementer stopped without producing a change for gates")
		}

		f.setStage(ticket.StageGates, "", "system")
		iteration := f.state.Usage.GateIterations + 1
		var gr activities.GatesResult
		if err := workflow.ExecuteActivity(f.gates(), a.RunGates, activities.GatesInput{Ref: f.ref, RunID: f.runID, Iteration: iteration, HeadSHA: impl.HeadSHA, BaseSHA: impl.BaseSHA}).Get(f.ctx, &gr); err != nil {
			return fail(errReason("gates", err))
		}
		f.state.Usage.GateIterations = iteration
		if !gr.Green {
			feedback = gr.Failures
			reviewFeedback = nil
			continue // caps are checked at the top of the loop (gate-run cap, turn cap)
		}
		feedback = nil
		f.visualReview = gr.RequiresHuman

		f.setStage(ticket.StageReview, gr.Summary, "agent:reviewer")
		var rev activities.ReviewResult
		if err := workflow.ExecuteActivity(f.agent(20*time.Minute, 0), a.Review, activities.ReviewInput{
			Ref: f.ref, HeadSHA: impl.HeadSHA, AcceptanceCriteria: f.refine.AcceptanceCriteria, BlastRadius: f.br,
		}).Get(f.ctx, &rev); err != nil {
			return fail(errReason("review", err))
		}
		f.addUsage(rev.Usage)
		if rev.RaiseBR > f.br {
			f.br = rev.RaiseBR
		}
		switch rev.Verdict {
		case "approve":
			f.finishRun("succeeded")
			f.releaseSlot()
			f.headSHA = impl.HeadSHA
			return phaseMerge
		case "request_changes":
			reviewRounds++
			if reviewRounds >= maxReview {
				return fail(fmt.Sprintf("reviewer requested changes %d times", reviewRounds))
			}
			reviewFeedback = rev.Findings
		default:
			return fail("reviewer escalated: " + rev.Summary)
		}
	}
}

func implementReason(feedback []gate.Finding, review []string) string {
	switch {
	case len(feedback) > 0:
		return fmt.Sprintf("fixing %d gate finding(s)", len(feedback))
	case len(review) > 0:
		return fmt.Sprintf("addressing %d review finding(s)", len(review))
	}
	return ""
}

func (f *flow) merge() phase {
	br := max(f.br, 1)
	mode := "human"
	if f.visualReview {
		mode = "human (visual diff above threshold)"
	}
	if m, ok := f.in.MergePolicy[fmt.Sprintf("BR%d", br)]; ok && m == "auto" {
		// Auto-merge is P3; MVP always routes to a human but records the configured intent.
		mode = "human (auto-merge configured; not enabled in MVP)"
	}
	f.setStage(ticket.StageMergePolicy, fmt.Sprintf("BR%d → %s merge", br, mode), "system")
	var pr activities.PRResult
	step := fmt.Sprintf("%s/pr/attempt-%d", workflow.GetInfo(f.ctx).WorkflowExecution.ID, f.state.Attempt)
	err := workflow.ExecuteActivity(f.agent(10*time.Minute, 0), a.OpenPullRequest, activities.PRInput{
		Ref: f.ref, StepKey: step, HeadSHA: f.headSHA, BlastRadius: br,
		Summary: fmt.Sprintf("Turns: %d · Gate iterations: %d · Cost: $%.2f", f.state.Usage.Turns, f.state.Usage.GateIterations, f.state.Usage.CostUSD),
	}).Get(f.ctx, &pr)
	if err != nil {
		return f.escalate(errReason("open pull request", err), phaseMerge)
	}
	f.state.PullRequest = pr.Number
	f.setStage(ticket.StagePROpen, fmt.Sprintf("PR #%d awaiting human review (BR%d)", pr.Number, br), "system")
	for {
		d := f.wait()
		switch d.Action {
		case ticketflow.ActionMarkMerged:
			_ = workflow.ExecuteActivity(f.control(), a.MarkMerged, activities.MergedInput{Ref: f.ref, Actor: d.Actor}).Get(f.ctx, nil)
			f.setStage(ticket.StageDone, fmt.Sprintf("PR #%d merged", pr.Number), d.Actor)
			return phaseEnd
		case ticketflow.ActionResume: // changes requested on the PR
			if strings.TrimSpace(d.Note) != "" {
				f.prFeedback = []string{d.Note}
			}
			return phaseBuild
		case ticketflow.ActionReject, ticketflow.ActionClose:
			return f.closeTicket("PR closed by human: "+d.Note, d.Actor)
		}
	}
}

// escalated waits for a human: resume (with added context) starts a fresh attempt with a new
// turn and gate-iteration budget (the ticket cost cap stays cumulative); close ends the ticket.
func (f *flow) escalated() phase {
	f.releaseSlot()
	f.setStage(ticket.StageEscalated, f.state.EscalationReason, "system")
	for {
		d := f.wait()
		switch d.Action {
		case ticketflow.ActionResume, ticketflow.ActionApprove:
			f.state.Attempt++
			f.state.Usage.Turns, f.state.Usage.GateIterations = 0, 0
			f.state.EscalationReason = ""
			if strings.TrimSpace(d.Note) != "" {
				f.prFeedback = []string{"Human context: " + d.Note}
			}
			return f.resumeTo
		case ticketflow.ActionClose, ticketflow.ActionReject:
			return f.closeTicket("closed after escalation: "+d.Note, d.Actor)
		}
	}
}
