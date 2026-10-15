package activities

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"

	"github.com/alpine-dev/alpine/packages/agents"
	"github.com/alpine-dev/alpine/packages/shared/budget"
	"github.com/alpine-dev/alpine/packages/shared/events"
	"github.com/alpine-dev/alpine/packages/shared/gate"
	"github.com/alpine-dev/alpine/packages/shared/llm"
	"github.com/alpine-dev/alpine/packages/shared/ticket"
	"github.com/alpine-dev/alpine/packages/shared/ticketflow"
	"github.com/alpine-dev/alpine/packages/shared/ticketrepo"
)

// Repository is the pipeline data access the orchestrator needs (packages/shared/ticketrepo).
// Metering, budgets, rate limits and BYO keys use jeff/data's repo/pgrepo.Metering instead.
type Repository interface {
	GetTicket(ctx context.Context, org, id uuid.UUID) (ticketrepo.Ticket, error)
	SetStage(ctx context.Context, org, ticketID uuid.UUID, to ticket.Stage, reason, actor, idemKey string) (ticket.Stage, uuid.UUID, bool, error)
	ApplyTriage(ctx context.Context, org, ticketID uuid.UUID, u ticketrepo.TriageUpdate) error
	ApplyRefinement(ctx context.Context, org, ticketID uuid.UUID, u ticketrepo.RefinementUpdate) error
	RaiseBlastRadius(ctx context.Context, org, ticketID uuid.UUID, br int) error
	StartRun(ctx context.Context, org, ticketID, runID uuid.UUID, kind, model string) error
	FinishRun(ctx context.Context, org, runID uuid.UUID, status string) error
	RecordTurn(ctx context.Context, org uuid.UUID, t ticketrepo.NewTurn) (bool, error)
	LastTurn(ctx context.Context, org, runID uuid.UUID) (int, error)
	AddTicketCost(ctx context.Context, org, ticketID uuid.UUID, cost float64, tokens int64) error
	RecordGateReport(ctx context.Context, org, ticketID, runID uuid.UUID, rep gate.Report) error
	BeginSideEffect(ctx context.Context, org uuid.UUID, ticketID *uuid.UUID, key, kind string) (bool, string, error)
	CompleteSideEffect(ctx context.Context, org uuid.UUID, key, ref string) error
	UpsertPullRequest(ctx context.Context, org uuid.UUID, p ticketrepo.NewPullRequest) error
	MarkPullRequestsMerged(ctx context.Context, org, ticketID uuid.UUID, by string) error
	Audit(ctx context.Context, org uuid.UUID, actor, action, targetType, targetID string, meta map[string]any) error
	RecordReview(ctx context.Context, org uuid.UUID, rv ticketrepo.NewReview) error
}

// SlotManager is the per-org/per-repo ticket concurrency semaphore (ratelimit.Semaphore).
type SlotManager interface {
	TryAcquire(ctx context.Context, key, holder string, limit int, lease time.Duration) (bool, error)
	Refresh(ctx context.Context, key, holder string, lease time.Duration) (bool, error)
	Release(ctx context.Context, key, holder string) error
}

// SCM is the outbound source-control client (GitHub App client from services/integrations).
// Every method must be safe to call again with the same arguments.
//
// It mirrors services/integrations/github.Client (omar/integrations), which is repo-scoped and
// configured with the GitHub App ID + bot login; the worker adapts it per repo. All methods are
// idempotent: branches must be "alpine/<ticket_ulid>-<slug>" (ticketflow.BranchName),
// CreateBranchFrom returns Created=false for an existing branch, CreatePullRequest reuses an open
// PR for the same alpine/* head, and comments are keyed by workflowID/activityID.
type SCM interface {
	// BranchName delegates to github.NewBranchName(ticketULID, title).
	BranchName(ticketULID, title string) (string, error)
	CreateBranchFrom(ctx context.Context, repoFullName, branch, base string) (*SCMBranch, error)
	CreatePullRequest(ctx context.Context, repoFullName string, in SCMNewPullRequest) (*SCMPullRequest, error)
	CreateIssueComment(ctx context.Context, repoFullName string, number int, key, body string) (commentID int64, err error)
}

// SCMBranch mirrors github.Branch.
type SCMBranch struct {
	Name    string
	SHA     string
	Created bool
}

// SCMNewPullRequest mirrors github.NewPullRequest.
type SCMNewPullRequest struct {
	Title  string
	Head   string
	Base   string
	Body   string
	Draft  bool
	Labels []string
}

// SCMPullRequest is the subset of github.PullRequest the orchestrator uses.
type SCMPullRequest struct {
	Number  int
	URL     string
	HeadSHA string
	Reused  bool
}

// Workspaces opens per-ticket checkouts for the live implementer (internal/workspace).
type Workspaces interface {
	Open(ctx context.Context, ticketULID, repoFullName, branch, base string) (Checkout, error)
	// Diff is `git diff base..head` in a checkout directory (for the reviewer).
	Diff(ctx context.Context, dir, base, head string, maxBytes int) (string, error)
}

// Checkout is one ticket's working tree plus its command sandbox.
type Checkout interface {
	agents.Workspace
	Dir() string
	BaseSHA(ctx context.Context) (string, error)
	Commit(ctx context.Context, msg string) (sha string, changed bool, err error)
	Push(ctx context.Context, branch string) error
	Close(ctx context.Context) error
}

// Activities is registered on the worker. All dependencies are interfaces.
type Activities struct {
	Repo   Repository
	Agents agents.Agents
	Gates  gate.Runner
	SCM    SCM
	// Workspaces enables the real implementer loop (with an agents.ToolLoop engine) and real
	// diffs for the reviewer. Nil: the Phase 0 single-call implementer.
	Workspaces Workspaces
	Slots      SlotManager
	Events     events.Publisher
	Logger     *slog.Logger
	// SlotLease bounds how long a crashed workflow can hold a ticket slot.
	SlotLease time.Duration
	// SlotPoll is how often AcquireTicketSlot retries while waiting.
	SlotPoll time.Duration
}

var (
	meter          = otel.Meter("alpine/orchestrator")
	stageCounter   metric.Int64Counter
	turnCounter    metric.Int64Counter
	costCounter    metric.Float64Counter
	gateCounter    metric.Int64Counter
	escalationsCtr metric.Int64Counter
)

func init() {
	stageCounter, _ = meter.Int64Counter("alpine.tickets.stage_transitions")
	turnCounter, _ = meter.Int64Counter("alpine.agent.turns")
	costCounter, _ = meter.Float64Counter("alpine.agent.cost", metric.WithUnit("USD"))
	gateCounter, _ = meter.Int64Counter("alpine.gates.results", metric.WithDescription("gate results by gate and status (pass rate)"))
	escalationsCtr, _ = meter.Int64Counter("alpine.tickets.escalations")
}

func (a *Activities) log() *slog.Logger {
	if a.Logger != nil {
		return a.Logger
	}
	return slog.Default()
}

func ids(ref Ref) (org, tid uuid.UUID, err error) {
	if org, err = uuid.Parse(ref.OrgID); err != nil {
		return org, tid, temporal.NewNonRetryableApplicationError("bad org id", ErrTypeNotFound, err)
	}
	if tid, err = uuid.Parse(ref.TicketID); err != nil {
		return org, tid, temporal.NewNonRetryableApplicationError("bad ticket id", ErrTypeNotFound, err)
	}
	return org, tid, nil
}

// idemKey returns "<workflow id>/<activity id>" — stable across retries of this activity.
func idemKey(ctx context.Context) string {
	if !activity.IsActivity(ctx) {
		return "local/" + uuid.NewString()
	}
	info := activity.GetInfo(ctx)
	return info.WorkflowExecution.ID + "/" + info.ActivityID
}

func (a *Activities) tenant(ref Ref) llm.Tenant {
	return llm.Tenant{OrgID: ref.OrgID, RepoID: ref.RepoID, TicketID: ref.TicketID}
}

// classify converts agent/gateway errors into Temporal errors: budget and invalid-output errors
// are non-retryable (the workflow escalates); everything else retries with the activity policy.
func classify(err error) error {
	if err == nil {
		return nil
	}
	var ex *budget.ExceededError
	if errors.As(err, &ex) {
		return temporal.NewNonRetryableApplicationError(ex.Reason(), ErrTypeBudgetExceeded, err)
	}
	if llm.IsBudgetExceeded(err) {
		return temporal.NewNonRetryableApplicationError(err.Error(), ErrTypeBudgetExceeded, err)
	}
	if errors.Is(err, llm.ErrInvalidOutput) {
		return temporal.NewNonRetryableApplicationError(err.Error(), ErrTypeInvalidOutput, err)
	}
	if errors.Is(err, ticketrepo.ErrNotFound) {
		return temporal.NewNonRetryableApplicationError(err.Error(), ErrTypeNotFound, err)
	}
	return err
}

func (a *Activities) ticketInput(ctx context.Context, ref Ref) (agents.TicketInput, ticketrepo.Ticket, error) {
	org, tid, err := ids(ref)
	if err != nil {
		return agents.TicketInput{}, ticketrepo.Ticket{}, err
	}
	t, err := a.Repo.GetTicket(ctx, org, tid)
	if err != nil {
		return agents.TicketInput{}, t, classify(err)
	}
	return agents.TicketInput{Key: t.Key, Title: t.Title, Body: t.Body, Source: t.Source, RepoFullName: t.RepoFullName}, t, nil
}

// spend records non-turn agent spend on the ticket (skipped for idempotent replays).
func (a *Activities) spend(ctx context.Context, ref Ref, cost float64, u llm.Usage, replayed bool) Usage {
	out := Usage{CostUSD: cost, Tokens: u.Total()}
	if replayed {
		return out
	}
	if cost > 0 || u.Total() > 0 {
		a.log().InfoContext(ctx, "agent spend", "ticket", ref.TicketID, "tokens_in", u.InputTokens, "tokens_out", u.OutputTokens, "cost_usd", cost)
	}
	org, tid, err := ids(ref)
	if err == nil {
		if err := a.Repo.AddTicketCost(ctx, org, tid, cost, u.Total()); err != nil {
			a.log().WarnContext(ctx, "add ticket cost failed", "err", err)
		}
	}
	costCounter.Add(ctx, cost)
	return out
}

// SetStage moves the ticket to a stage (idempotent per activity id), writes history + audit and
// publishes a live board event.
func (a *Activities) SetStage(ctx context.Context, in SetStageInput) error {
	org, tid, err := ids(in.Ref)
	if err != nil {
		return err
	}
	actor := in.Actor
	if actor == "" {
		actor = "system"
	}
	prev, repoID, changed, err := a.Repo.SetStage(ctx, org, tid, in.Stage, in.Reason, actor, idemKey(ctx))
	if err != nil {
		return classify(err)
	}
	if changed {
		stageCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("stage", string(in.Stage))))
		if in.Stage == ticket.StageEscalated {
			escalationsCtr.Add(ctx, 1)
		}
	}
	if a.Events != nil {
		ev := events.BoardEvent{Type: events.TicketStageChanged, OrgID: in.Ref.OrgID, TicketID: in.Ref.TicketID,
			RepoID: repoID.String(), Stage: string(in.Stage), PreviousStage: string(prev), Reason: in.Reason, At: time.Now().UTC()}
		if err := a.Events.Publish(ctx, ev); err != nil {
			a.log().WarnContext(ctx, "publish board event failed", "err", err)
		}
	}
	return nil
}

// Triage classifies the ticket.
func (a *Activities) Triage(ctx context.Context, ref Ref) (TriageResult, error) {
	in, _, err := a.ticketInput(ctx, ref)
	if err != nil {
		return TriageResult{}, err
	}
	r, err := a.Agents.Triage(ctx, agents.Call{Tenant: a.tenant(ref), IdempotencyKey: idemKey(ctx)}, in)
	if err != nil {
		return TriageResult{}, classify(err)
	}
	org, tid, _ := ids(ref)
	typ := ticket.Type(r.Output.Classification)
	if err := a.Repo.ApplyTriage(ctx, org, tid, ticketrepo.TriageUpdate{Type: typ}); err != nil {
		return TriageResult{}, err
	}
	res := TriageResult{Classification: r.Output.Classification, Reason: r.Output.Reason, AlreadyFixed: r.Output.AlreadyFixed,
		Usage: a.spend(ctx, ref, r.CostUSD, r.Usage, r.Replayed)}
	if r.Output.DuplicateOfExternalID != nil {
		res.DuplicateOf = *r.Output.DuplicateOfExternalID
	}
	return res, nil
}

// Refine produces the refined ticket and estimates.
func (a *Activities) Refine(ctx context.Context, ref Ref) (RefineResult, error) {
	in, t, err := a.ticketInput(ctx, ref)
	if err != nil {
		return RefineResult{}, err
	}
	// With live workspaces, refinement reads the repo at the base branch to confirm the bug.
	if a.Workspaces != nil && t.RepoFullName != "" {
		_, tid, _ := ids(ref)
		base := t.DefaultBranch
		if base == "" {
			base = "main"
		}
		if branch, err := a.branchName(tid, t.Title); err == nil {
			if co, err := a.Workspaces.Open(ctx, ticketflow.TicketULID(tid), t.RepoFullName, branch, base); err == nil {
				in.RepoContext = repoContext(ctx, co, t.Title, t.Body, 24_000)
				_ = co.Close(ctx)
			} else {
				a.log().WarnContext(ctx, "refine: repo context unavailable", "err", err)
			}
		}
	}
	r, err := a.Agents.Refine(ctx, agents.Call{Tenant: a.tenant(ref), IdempotencyKey: idemKey(ctx)}, in, agents.TriageOutput{Classification: "bug"})
	if err != nil {
		return RefineResult{}, classify(err)
	}
	org, tid, _ := ids(ref)
	body := renderTicketBody(in.Body, r.Output)
	if err := a.Repo.ApplyRefinement(ctx, org, tid, ticketrepo.RefinementUpdate{
		Priority: r.Output.Priority, Size: r.Output.Size, BRPredicted: r.Output.BlastRadius, Confidence: r.Output.Confidence, Body: body,
	}); err != nil {
		return RefineResult{}, err
	}
	return RefineResult{Refine: r.Output, Usage: a.spend(ctx, ref, r.CostUSD, r.Usage, r.Replayed)}, nil
}

func renderTicketBody(original string, r agents.RefineOutput) string {
	var b strings.Builder
	b.WriteString("## Problem\n" + r.Problem + "\n")
	if len(r.ReproSteps) > 0 {
		b.WriteString("\n## Repro\n")
		for i, s := range r.ReproSteps {
			fmt.Fprintf(&b, "%d. %s\n", i+1, s)
		}
	}
	b.WriteString("\n## Acceptance criteria\n")
	for _, s := range r.AcceptanceCriteria {
		b.WriteString("- [ ] " + s + "\n")
	}
	if len(r.OutOfScope) > 0 {
		b.WriteString("\n## Out of scope\n")
		for _, s := range r.OutOfScope {
			b.WriteString("- " + s + "\n")
		}
	}
	fmt.Fprintf(&b, "\nBlast radius: BR%d — %s\n", r.BlastRadius, r.BlastRadiusJustification)
	if strings.TrimSpace(original) != "" {
		b.WriteString("\n---\nOriginal report:\n" + original + "\n")
	}
	return b.String()
}

// Prioritize decides ready vs needs_human.
func (a *Activities) Prioritize(ctx context.Context, in PrioritizeInput) (PrioritizeResult, error) {
	ti, _, err := a.ticketInput(ctx, in.Ref)
	if err != nil {
		return PrioritizeResult{}, err
	}
	r, err := a.Agents.Prioritize(ctx, agents.Call{Tenant: a.tenant(in.Ref), IdempotencyKey: idemKey(ctx)}, ti, in.Refine)
	if err != nil {
		return PrioritizeResult{}, classify(err)
	}
	return PrioritizeResult{Decision: r.Output.Decision, Reason: r.Output.Reason, Usage: a.spend(ctx, in.Ref, r.CostUSD, r.Usage, r.Replayed)}, nil
}

// StartRun records the start of an agent run (idempotent on run id).
func (a *Activities) StartRun(ctx context.Context, in RunInput) error {
	org, tid, err := ids(in.Ref)
	if err != nil {
		return err
	}
	runID, err := uuid.Parse(in.RunID)
	if err != nil {
		return temporal.NewNonRetryableApplicationError("bad run id", ErrTypeNotFound, err)
	}
	return classify(a.Repo.StartRun(ctx, org, tid, runID, in.Kind, ""))
}

// FinishRun closes a run.
func (a *Activities) FinishRun(ctx context.Context, in RunInput) error {
	org, _, err := ids(in.Ref)
	if err != nil {
		return err
	}
	runID, err := uuid.Parse(in.RunID)
	if err != nil {
		return temporal.NewNonRetryableApplicationError("bad run id", ErrTypeNotFound, err)
	}
	return classify(a.Repo.FinishRun(ctx, org, runID, in.Status))
}

// RunGates runs one gate iteration in the gate sandbox and records the results. Gate config
// comes from the base branch (gate.Runner contract).
func (a *Activities) RunGates(ctx context.Context, in GatesInput) (GatesResult, error) {
	org, tid, err := ids(in.Ref)
	if err != nil {
		return GatesResult{}, err
	}
	runID, err := uuid.Parse(in.RunID)
	if err != nil {
		return GatesResult{}, temporal.NewNonRetryableApplicationError("bad run id", ErrTypeNotFound, err)
	}
	stop := heartbeatEvery(ctx, 20*time.Second, in.Iteration)
	repoDir := in.RepoDir
	if repoDir == "" {
		repoDir = RepoDir(tid)
	}
	rep, err := a.Gates.Run(ctx, gate.RunRequest{OrgID: in.Ref.OrgID, TicketID: in.Ref.TicketID, RunID: in.RunID, Iteration: in.Iteration,
		BaseSHA: in.BaseSHA, HeadSHA: in.HeadSHA, RepoDir: repoDir,
		ArtifactsRoot: fmt.Sprintf("orgs/%s/tickets/%s/runs/%s/gates/%d", in.Ref.OrgID, in.Ref.TicketID, in.RunID, in.Iteration)})
	stop()
	if err != nil {
		return GatesResult{}, err
	}
	rep.Iteration = in.Iteration
	if err := a.Repo.RecordGateReport(ctx, org, tid, runID, rep); err != nil {
		return GatesResult{}, err
	}
	res := GatesResult{Green: rep.Green(), RequiresHuman: rep.RequiresHuman()}
	var failed []string
	for _, r := range rep.Results {
		gateCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("gate", r.GateID), attribute.String("status", string(r.Status))))
		if r.Status.Blocking() {
			failed = append(failed, r.GateID)
			res.Failures = append(res.Failures, r.Findings...)
		}
	}
	if res.Green {
		res.Summary = fmt.Sprintf("iteration %d: all %d gates green", in.Iteration, len(rep.Results))
	} else {
		res.Summary = fmt.Sprintf("iteration %d: failed %s", in.Iteration, strings.Join(failed, ", "))
	}
	return res, nil
}

// Review runs the independent reviewer (fresh context: diff + AC + rules + module map only).
func (a *Activities) Review(ctx context.Context, in ReviewInput) (ReviewResult, error) {
	ti, _, err := a.ticketInput(ctx, in.Ref)
	if err != nil {
		return ReviewResult{}, err
	}
	// The reviewer sees the real diff of the work branch (never the implementer transcript).
	diff := "(diff of " + in.HeadSHA + ")"
	if a.Workspaces != nil && in.RepoDir != "" && in.BaseSHA != "" {
		d, err := a.Workspaces.Diff(ctx, in.RepoDir, in.BaseSHA, in.HeadSHA, 60_000)
		if err != nil {
			return ReviewResult{}, fmt.Errorf("review diff: %w", err)
		}
		diff = d
	}
	r, err := a.Agents.Review(ctx, agents.Call{Tenant: a.tenant(in.Ref), IdempotencyKey: idemKey(ctx)}, agents.ReviewInput{
		Ticket: ti, AcceptanceCriteria: in.AcceptanceCriteria, Diff: diff, BlastRadius: in.BlastRadius,
	})
	if err != nil {
		return ReviewResult{}, classify(err)
	}
	out := ReviewResult{Verdict: r.Output.Verdict, Summary: r.Output.Summary, Usage: a.spend(ctx, in.Ref, r.CostUSD, r.Usage, r.Replayed)}
	findings := make([]ticketrepo.ReviewFinding, 0, len(r.Output.Findings))
	for _, f := range r.Output.Findings {
		out.Findings = append(out.Findings, fmt.Sprintf("[%s/%s] %s", f.Severity, f.Category, f.Message))
		findings = append(findings, ticketrepo.ReviewFinding{Severity: f.Severity, Category: f.Category, File: f.File, Line: f.Line, Message: f.Message})
	}
	model := r.Model
	if model == "" {
		model = "stub"
	}
	org, tid, err := ids(in.Ref)
	if err != nil {
		return ReviewResult{}, err
	}
	if err := a.Repo.RecordReview(ctx, org, ticketrepo.NewReview{TicketID: tid, Verdict: r.Output.Verdict, Model: model,
		Summary: r.Output.Summary, Findings: findings, IdempotencyKey: idemKey(ctx)}); err != nil {
		return ReviewResult{}, err
	}
	if r.Output.RaiseBlastRadiusTo != nil && *r.Output.RaiseBlastRadiusTo > in.BlastRadius {
		out.RaiseBR = *r.Output.RaiseBlastRadiusTo
		if err := a.Repo.RaiseBlastRadius(ctx, org, tid, out.RaiseBR); err != nil {
			return ReviewResult{}, err
		}
	}
	return out, nil
}

// OpenPullRequest creates the branch + PR exactly once per StepKey (side_effects ledger).
func (a *Activities) OpenPullRequest(ctx context.Context, in PRInput) (PRResult, error) {
	org, tid, err := ids(in.Ref)
	if err != nil {
		return PRResult{}, err
	}
	t, err := a.Repo.GetTicket(ctx, org, tid)
	if err != nil {
		return PRResult{}, classify(err)
	}
	branch, err := a.branchName(tid, t.Title)
	if err != nil {
		return PRResult{}, temporal.NewNonRetryableApplicationError(err.Error(), ErrTypeInvalidOutput, err)
	}
	done, ref, err := a.Repo.BeginSideEffect(ctx, org, &tid, in.StepKey, "pull_request")
	if err != nil {
		return PRResult{}, err
	}
	if done {
		var n int
		var url string
		if _, err := fmt.Sscanf(ref, "%d %s", &n, &url); err == nil {
			return PRResult{Number: n, URL: url, Branch: branch}, nil
		}
	}
	base := t.DefaultBranch
	if base == "" {
		base = "main"
	}
	// Safe to repeat: the branch create is idempotent (Created=false on retry) and
	// CreatePullRequest reuses the open PR for the same alpine/* head, so a crash between the call
	// and CompleteSideEffect cannot duplicate.
	if _, err := a.SCM.CreateBranchFrom(ctx, t.RepoFullName, branch, base); err != nil {
		return PRResult{}, err
	}
	body := in.Summary
	if issue := t.IssueNumber(); issue > 0 {
		body = fmt.Sprintf("Fixes #%d\n\n%s", issue, body)
	}
	pr, err := a.SCM.CreatePullRequest(ctx, t.RepoFullName, SCMNewPullRequest{
		Title: "[" + t.Key + "] " + t.Title, Head: branch, Base: base, Body: body,
		Labels: []string{"alpine", fmt.Sprintf("blast-radius:%d", max(1, in.BlastRadius))},
	})
	if err != nil {
		return PRResult{}, err
	}
	n, url := pr.Number, pr.URL
	if err := a.Repo.UpsertPullRequest(ctx, org, ticketrepo.NewPullRequest{TicketID: tid, RepoID: t.RepoID, Number: n, URL: url,
		HeadBranch: branch, HeadSHA: in.HeadSHA, BlastRadius: max(1, in.BlastRadius)}); err != nil {
		return PRResult{}, err
	}
	// Link the PR from the source issue. Keyed by workflowID/activityID so retries edit nothing
	// and never double-post.
	if issue := t.IssueNumber(); issue > 0 {
		body := fmt.Sprintf("Alpine opened #%d for this issue (blast radius %d). A human merges it.", n, max(1, in.BlastRadius))
		if _, err := a.SCM.CreateIssueComment(ctx, t.RepoFullName, issue, ticketflow.ExternalKey(idemKey(ctx)), body); err != nil {
			return PRResult{}, err
		}
	}
	if err := a.Repo.CompleteSideEffect(ctx, org, in.StepKey, fmt.Sprintf("%d %s", n, url)); err != nil {
		return PRResult{}, err
	}
	_ = a.Repo.Audit(ctx, org, "agent:implementer", "pull_request.opened", "ticket", tid.String(), map[string]any{"number": n, "branch": branch, "reused": pr.Reused})
	return PRResult{Number: n, URL: url, Branch: branch}, nil
}

// MarkMerged records a human merge.
func (a *Activities) MarkMerged(ctx context.Context, in MergedInput) error {
	org, tid, err := ids(in.Ref)
	if err != nil {
		return err
	}
	return a.Repo.MarkPullRequestsMerged(ctx, org, tid, in.Actor)
}

// RepoDir is the per-ticket checkout path inside the sandbox.
func RepoDir(ticketID uuid.UUID) string { return "/work/" + ticketflow.TicketULID(ticketID) }

// branchName returns the ticket's work branch via the SCM (github.NewBranchName), falling back
// to the identical ticketflow mirror when no SCM is configured.
func (a *Activities) branchName(tid uuid.UUID, title string) (string, error) {
	if a.SCM == nil {
		return ticketflow.BranchName(tid, title), nil
	}
	return a.SCM.BranchName(ticketflow.TicketULID(tid), title)
}

// heartbeatEvery heartbeats in the background while a long call runs.
func heartbeatEvery(ctx context.Context, d time.Duration, details ...any) func() {
	if !activity.IsActivity(ctx) {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(d)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				activity.RecordHeartbeat(ctx, details...)
			}
		}
	}()
	return func() { close(done) }
}
