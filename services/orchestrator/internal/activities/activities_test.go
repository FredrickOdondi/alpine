package activities

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"

	"github.com/alpine-dev/alpine/packages/agents"
	"github.com/alpine-dev/alpine/packages/shared/gate"
	"github.com/alpine-dev/alpine/packages/shared/ticket"
	"github.com/alpine-dev/alpine/packages/shared/ticketflow"
	"github.com/alpine-dev/alpine/packages/shared/ticketrepo"
)

// fakeRepo is an in-memory Repository with the same idempotency semantics as Postgres.
type fakeRepo struct {
	mu          sync.Mutex
	turns       map[string]bool // run|n
	lastTurn    map[uuid.UUID]int
	turnCount   int
	effects     map[string]string // key -> ref ("" = pending)
	prs         int
	history     map[string]bool
	stage       ticket.Stage
	ticketCost  float64
	gateReports int
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{turns: map[string]bool{}, lastTurn: map[uuid.UUID]int{}, effects: map[string]string{}, history: map[string]bool{}, stage: ticket.StageIntake}
}

func (f *fakeRepo) GetTicket(_ context.Context, org, id uuid.UUID) (ticketrepo.Ticket, error) {
	ext := "gh:42#7"
	return ticketrepo.Ticket{ID: id, OrgID: org, RepoID: uuid.New(), Key: "GH-7", Title: "Login broken on Safari", RepoFullName: "acme/widgets",
		Stage: f.stage, DefaultBranch: "main", GitHubExternalID: &ext}, nil
}
func (f *fakeRepo) SetStage(_ context.Context, _, _ uuid.UUID, to ticket.Stage, _, _, key string) (ticket.Stage, uuid.UUID, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	prev := f.stage
	if f.history[key] {
		return prev, uuid.Nil, false, nil
	}
	f.history[key] = true
	f.stage = to
	return prev, uuid.Nil, true, nil
}
func (f *fakeRepo) ApplyTriage(context.Context, uuid.UUID, uuid.UUID, ticketrepo.TriageUpdate) error {
	return nil
}
func (f *fakeRepo) ApplyRefinement(context.Context, uuid.UUID, uuid.UUID, ticketrepo.RefinementUpdate) error {
	return nil
}
func (f *fakeRepo) RaiseBlastRadius(context.Context, uuid.UUID, uuid.UUID, int) error { return nil }
func (f *fakeRepo) StartRun(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, string, string) error {
	return nil
}
func (f *fakeRepo) FinishRun(context.Context, uuid.UUID, uuid.UUID, string) error { return nil }
func (f *fakeRepo) RecordTurn(_ context.Context, _ uuid.UUID, t ticketrepo.NewTurn) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := fmt.Sprintf("%s|%d", t.RunID, t.N)
	if f.turns[k] {
		return false, nil
	}
	f.turns[k] = true
	f.turnCount++
	if t.N > f.lastTurn[t.RunID] {
		f.lastTurn[t.RunID] = t.N
	}
	return true, nil
}
func (f *fakeRepo) LastTurn(_ context.Context, _, run uuid.UUID) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastTurn[run], nil
}
func (f *fakeRepo) AddTicketCost(_ context.Context, _, _ uuid.UUID, c float64, _ int64) error {
	f.ticketCost += c
	return nil
}
func (f *fakeRepo) RecordGateReport(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, gate.Report) error {
	f.gateReports++
	return nil
}
func (f *fakeRepo) BeginSideEffect(_ context.Context, _ uuid.UUID, _ *uuid.UUID, key, _ string) (bool, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ref, ok := f.effects[key]
	if !ok {
		f.effects[key] = ""
	}
	return ok && ref != "", ref, nil
}
func (f *fakeRepo) CompleteSideEffect(_ context.Context, _ uuid.UUID, key, ref string) error {
	f.effects[key] = ref
	return nil
}
func (f *fakeRepo) UpsertPullRequest(context.Context, uuid.UUID, ticketrepo.NewPullRequest) error {
	f.prs++
	return nil
}
func (f *fakeRepo) MarkPullRequestsMerged(context.Context, uuid.UUID, uuid.UUID, string) error {
	return nil
}
func (f *fakeRepo) RecordReview(context.Context, uuid.UUID, ticketrepo.NewReview) error { return nil }
func (f *fakeRepo) Audit(context.Context, uuid.UUID, string, string, string, string, map[string]any) error {
	return nil
}

type countingSCM struct {
	StubSCM
	mu       sync.Mutex
	branches map[string]bool
	prs      map[string]int
	opened   int
	comments map[string]int // key -> posts
	heads    []string
}

func newCountingSCM() *countingSCM {
	return &countingSCM{branches: map[string]bool{}, prs: map[string]int{}, comments: map[string]int{}}
}

func (c *countingSCM) CreateBranchFrom(ctx context.Context, repo, branch, base string) (*SCMBranch, error) {
	b, err := c.StubSCM.CreateBranchFrom(ctx, repo, branch, base)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	b.Created = !c.branches[branch]
	c.branches[branch] = true
	return b, nil
}

func (c *countingSCM) CreatePullRequest(ctx context.Context, repo string, in SCMNewPullRequest) (*SCMPullRequest, error) {
	pr, err := c.StubSCM.CreatePullRequest(ctx, repo, in)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.heads = append(c.heads, in.Head)
	if _, ok := c.prs[in.Head]; ok {
		pr.Reused = true
	} else {
		c.opened++
		c.prs[in.Head] = pr.Number
	}
	return pr, nil
}

func (c *countingSCM) CreateIssueComment(ctx context.Context, repo string, n int, key, body string) (int64, error) {
	c.mu.Lock()
	c.comments[key]++
	c.mu.Unlock()
	return c.StubSCM.CreateIssueComment(ctx, repo, n, key, body)
}

type recordingAgents struct {
	agents.Stub
	mu   sync.Mutex
	keys []string
}

func (r *recordingAgents) ImplementTurn(ctx context.Context, c agents.Call, in agents.ImplementInput) (agents.Result[agents.ImplementTurnOutput], error) {
	r.mu.Lock()
	r.keys = append(r.keys, c.IdempotencyKey)
	r.mu.Unlock()
	return r.Stub.ImplementTurn(ctx, c, in)
}

func ref() Ref {
	return Ref{OrgID: uuid.NewString(), TicketID: uuid.NewString(), RepoID: uuid.NewString()}
}

func TestImplementResumesFromLastPersistedTurn(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestActivityEnvironment()
	repo := newFakeRepo()
	ag := &recordingAgents{Stub: agents.Stub{TurnsPerAttempt: 5}}
	a := &Activities{Repo: repo, Agents: ag}
	env.RegisterActivity(a)
	runID := uuid.New()

	// Simulate a crash after 3 persisted turns of this attempt (turns 1..3).
	for n := 1; n <= 3; n++ {
		_, _ = repo.RecordTurn(context.Background(), uuid.Nil, ticketrepo.NewTurn{RunID: runID, N: n})
	}
	env.SetHeartbeatDetails(implementProgress{LastTurn: 3, Turns: 3, CostUSD: 0.15})

	val, err := env.ExecuteActivity(a.Implement, ImplementInput{Ref: ref(), RunID: runID.String(), TurnBudget: 100, CostBudgetUSD: 20})
	require.NoError(t, err)
	var res ImplementResult
	require.NoError(t, val.Get(&res))
	require.True(t, res.Resumed)
	require.True(t, res.ReadyForGates)
	require.Equal(t, 5, res.Usage.Turns, "3 before the crash + turns 4 and 5")
	require.Len(t, ag.keys, 2, "only turns 4..5 are executed after resume")
	require.Contains(t, ag.keys[0], "/turn-4")
	require.Equal(t, 5, repo.turnCount)
}

func TestImplementCreatesTicketBranch(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestActivityEnvironment()
	scm := newCountingSCM()
	a := &Activities{Repo: newFakeRepo(), Agents: agents.Stub{TurnsPerAttempt: 2}, SCM: scm}
	env.RegisterActivity(a)
	r := ref()
	in := ImplementInput{Ref: r, RunID: uuid.NewString(), TurnBudget: 100, CostBudgetUSD: 20}
	var res1, res2 ImplementResult
	v, err := env.ExecuteActivity(a.Implement, in)
	require.NoError(t, err)
	require.NoError(t, v.Get(&res1))
	in.RunID = uuid.NewString()
	v, err = env.ExecuteActivity(a.Implement, in)
	require.NoError(t, err)
	require.NoError(t, v.Get(&res2))
	tid, _ := uuid.Parse(r.TicketID)
	require.Equal(t, ticketflow.BranchName(tid, "Login broken on Safari"), res1.Branch)
	require.Equal(t, res1.Branch, res2.Branch, "one branch per ticket across attempts")
	require.Len(t, scm.branches, 1)
	require.NoError(t, CheckBranch(res1.Branch))
	require.Len(t, res1.HeadSHA, 40)
}

func TestStubSCMRejectsNonAlpineBranches(t *testing.T) {
	_, err := StubSCM{}.CreatePullRequest(context.Background(), "a/b", SCMNewPullRequest{Head: "main"})
	require.Error(t, err)
	_, err = StubSCM{}.CreateBranchFrom(context.Background(), "a/b", "alpine/GH-7-x", "main")
	require.Error(t, err)
	_, err = StubSCM{}.CreateIssueComment(context.Background(), "a/b", 1, "", "x")
	require.Error(t, err)
}

func TestImplementRespectsTurnBudget(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestActivityEnvironment()
	repo := newFakeRepo()
	a := &Activities{Repo: repo, Agents: agents.Stub{TurnsPerAttempt: 1000}}
	env.RegisterActivity(a)
	val, err := env.ExecuteActivity(a.Implement, ImplementInput{Ref: ref(), RunID: uuid.NewString(), TurnsSoFar: 95, TurnBudget: 5, CostBudgetUSD: 20})
	require.NoError(t, err)
	var res ImplementResult
	require.NoError(t, val.Get(&res))
	require.False(t, res.ReadyForGates)
	require.Equal(t, 5, res.Usage.Turns)
}

func TestOpenPullRequestIsIdempotent(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestActivityEnvironment()
	repo := newFakeRepo()
	scm := newCountingSCM()
	a := &Activities{Repo: repo, SCM: scm}
	env.RegisterActivity(a)
	r := ref()
	in := PRInput{Ref: r, StepKey: "ticket/x/y/pr/attempt-1", HeadSHA: "abc", BlastRadius: 2}
	var first, second PRResult
	v, err := env.ExecuteActivity(a.OpenPullRequest, in)
	require.NoError(t, err)
	require.NoError(t, v.Get(&first))
	v, err = env.ExecuteActivity(a.OpenPullRequest, in) // Temporal retry / replay
	require.NoError(t, err)
	require.NoError(t, v.Get(&second))
	require.Equal(t, first.Number, second.Number)
	require.Equal(t, 1, scm.opened, "PR is created exactly once")
	require.Equal(t, 1, repo.prs)
	tid, _ := uuid.Parse(r.TicketID)
	require.Equal(t, "alpine/"+ticketflow.TicketULID(tid)+"-login-broken-on-safari", first.Branch)
	require.NoError(t, CheckBranch(first.Branch))
	require.Len(t, scm.comments, 1, "issue comment keyed by workflowID/activityID")

	// Crash after the PR was created but before the ledger completed: the retry reuses the PR
	// and the keyed comment instead of duplicating either.
	repo2 := newFakeRepo()
	a.Repo = repo2
	v, err = env.ExecuteActivity(a.OpenPullRequest, in)
	require.NoError(t, err)
	var third PRResult
	require.NoError(t, v.Get(&third))
	require.Equal(t, first.Number, third.Number)
	require.Equal(t, 1, scm.opened)
	// (The test env assigns a fresh ActivityID per execution; a real Temporal retry keeps it, so
	// the GitHub client's keyed comment lookup returns the first comment.)
	for k := range scm.comments {
		require.Regexp(t, `^alpine:[0-9a-f]{48}$`, k, "long workflowID/activityID keys are hashed")
	}
}

func TestSetStageIsIdempotentPerActivity(t *testing.T) {
	repo := newFakeRepo()
	a := &Activities{Repo: repo}
	ctx := context.Background()
	_, _, changed, _ := repo.SetStage(ctx, uuid.Nil, uuid.Nil, ticket.StageTriage, "", "system", "wf/1")
	require.True(t, changed)
	_, _, changed, _ = repo.SetStage(ctx, uuid.Nil, uuid.Nil, ticket.StageTriage, "", "system", "wf/1")
	require.False(t, changed)
	require.NoError(t, a.SetStage(ctx, SetStageInput{Ref: ref(), Stage: ticket.StageRefinement}))
	require.Equal(t, ticket.StageRefinement, repo.stage)
}

func TestRunGatesRecordsAndSummarises(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestActivityEnvironment()
	repo := newFakeRepo()
	a := &Activities{Repo: repo, Gates: StubGates{FailIterations: 1}}
	env.RegisterActivity(a)
	v, err := env.ExecuteActivity(a.RunGates, GatesInput{Ref: ref(), RunID: uuid.NewString(), Iteration: 1})
	require.NoError(t, err)
	var res GatesResult
	require.NoError(t, v.Get(&res))
	require.False(t, res.Green)
	require.Len(t, res.Failures, 1)
	require.Contains(t, res.Summary, "typecheck")
	require.Equal(t, 1, repo.gateReports)
}

func TestTicketSlotsBoundConcurrency(t *testing.T) {
	sem := &memSem{holders: map[string]map[string]bool{}}
	a := &Activities{Slots: sem, SlotPoll: time.Millisecond}
	r1, r2 := ref(), ref()
	r2.OrgID = r1.OrgID
	ctx := context.Background()
	require.NoError(t, a.AcquireTicketSlot(ctx, SlotInput{Ref: r1, OrgLimit: 1, RepoLimit: 5}))
	cctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	require.Error(t, a.AcquireTicketSlot(cctx, SlotInput{Ref: r2, OrgLimit: 1, RepoLimit: 5}), "second ticket of the org waits")
	require.NoError(t, a.ReleaseTicketSlot(ctx, SlotInput{Ref: r1}))
	require.NoError(t, a.AcquireTicketSlot(ctx, SlotInput{Ref: r2, OrgLimit: 1, RepoLimit: 5}))
}

type memSem struct {
	mu      sync.Mutex
	holders map[string]map[string]bool
}

func (m *memSem) TryAcquire(_ context.Context, key, holder string, limit int, _ time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h := m.holders[key]
	if h == nil {
		h = map[string]bool{}
		m.holders[key] = h
	}
	if h[holder] || len(h) < limit {
		h[holder] = true
		return true, nil
	}
	return false, nil
}
func (m *memSem) Refresh(_ context.Context, key, holder string, _ time.Duration) (bool, error) {
	return m.holders[key][holder], nil
}
func (m *memSem) Release(_ context.Context, key, holder string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.holders[key], holder)
	return nil
}
