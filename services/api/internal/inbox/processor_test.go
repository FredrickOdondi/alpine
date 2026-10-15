package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/alpine-dev/alpine/packages/shared/intake"
	"github.com/alpine-dev/alpine/packages/shared/ticket"
	"github.com/alpine-dev/alpine/packages/shared/ticketflow"
	"github.com/alpine-dev/alpine/packages/shared/ticketrepo"
)

type memStore struct {
	mu                sync.Mutex
	org               uuid.UUID
	repoID            uuid.UUID
	events            map[uuid.UUID]*ticketrepo.InboxEvent
	marks             map[uuid.UUID]string
	tickets           map[uuid.UUID]ticketrepo.NewTicket
	knownInstallation int64
}

func newMem() *memStore {
	return &memStore{org: uuid.New(), repoID: uuid.New(), events: map[uuid.UUID]*ticketrepo.InboxEvent{}, marks: map[uuid.UUID]string{},
		tickets: map[uuid.UUID]ticketrepo.NewTicket{}, knownInstallation: 42}
}

func (m *memStore) add(src, delivery, event string, payload any) uuid.UUID {
	b, _ := json.Marshal(payload)
	id := uuid.New()
	m.events[id] = &ticketrepo.InboxEvent{ID: id, OrgID: m.org, Source: src, DeliveryID: delivery, EventType: event, Payload: b}
	return id
}

func (m *memStore) ResolveInstallation(context.Context, string, int64) (uuid.UUID, bool, error) {
	return m.org, true, nil
}
func (m *memStore) InsertInboxEvent(context.Context, uuid.UUID, string, string, string, string, []byte) (bool, error) {
	return true, nil
}
func (m *memStore) PendingInbox(context.Context, int) ([]ticketrepo.PendingInbox, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []ticketrepo.PendingInbox
	for id := range m.events {
		if _, done := m.marks[id]; !done {
			out = append(out, ticketrepo.PendingInbox{ID: id, OrgID: m.org})
		}
	}
	return out, nil
}
func (m *memStore) LoadInboxEvent(_ context.Context, _, id uuid.UUID) (ticketrepo.InboxEvent, error) {
	return *m.events[id], nil
}
func (m *memStore) MarkInbox(_ context.Context, _, id uuid.UUID, outcome, _, errMsg string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if errMsg == "" {
		m.marks[id] = outcome
	}
	return nil
}
func (m *memStore) UpsertRepoForIntake(_ context.Context, _ uuid.UUID, ref intake.RepoRef) (uuid.UUID, error) {
	if ref.InstallationID != m.knownInstallation {
		return uuid.Nil, ticketrepo.ErrNotFound
	}
	return m.repoID, nil
}
func (m *memStore) CreateTicket(_ context.Context, t ticketrepo.NewTicket) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.tickets[t.ID]; ok {
		return false, nil
	}
	m.tickets[t.ID] = t
	return true, nil
}
func (m *memStore) GetTicket(_ context.Context, _, id uuid.UUID) (ticketrepo.Ticket, error) {
	t, ok := m.tickets[id]
	if !ok {
		return ticketrepo.Ticket{}, ticketrepo.ErrNotFound
	}
	wf := t.WorkflowID
	return ticketrepo.Ticket{ID: t.ID, WorkflowID: &wf, Stage: ticket.StagePROpen}, nil
}
func (m *memStore) ListRepos(context.Context, uuid.UUID) ([]ticketrepo.Repo, error) {
	return []ticketrepo.Repo{{ID: m.repoID, FullName: "acme/widgets"}}, nil
}

type fakeEngine struct {
	mu      sync.Mutex
	started map[string]ticketflow.Input
	signals []ticketflow.HumanDecision
	fail    error
}

func (f *fakeEngine) StartTicket(_ context.Context, in ticketflow.Input) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return false, f.fail
	}
	id := ticketflow.WorkflowID(in.OrgID, in.TicketID)
	if _, ok := f.started[id]; ok {
		return false, nil
	}
	f.started[id] = in
	return true, nil
}
func (f *fakeEngine) Signal(_ context.Context, _ string, d ticketflow.HumanDecision) error {
	f.signals = append(f.signals, d)
	return nil
}
func (f *fakeEngine) Ping(context.Context) error { return nil }

type sentryRoutes struct{ repo uuid.UUID }

func (s sentryRoutes) OrgForInstallation(context.Context, string) (uuid.UUID, bool, error) {
	return uuid.Nil, false, nil
}
func (s sentryRoutes) RepoForProject(_ context.Context, _ uuid.UUID, p string) (uuid.UUID, bool, error) {
	return s.repo, p == "web-frontend", nil
}

type sentryNorm struct{}

func (sentryNorm) Source() intake.Source { return intake.SourceSentry }
func (sentryNorm) Normalize(_ context.Context, _ string, raw []byte) ([]intake.TicketIntake, error) {
	var p struct{ Project, Key, Title string }
	json.Unmarshal(raw, &p)
	return []intake.TicketIntake{{Source: intake.SourceSentry, Action: intake.ActionCreate, ExternalID: "sentry-issue-" + p.Key,
		DedupeKey: "sentry:issue:" + p.Key, SentryProject: p.Project, Title: p.Title}}, nil
}

func issue(action string, n int, installation int64) map[string]any {
	return map[string]any{"action": action, "installation": map[string]any{"id": installation},
		"repository": map[string]any{"id": 777, "full_name": "acme/widgets", "default_branch": "main"},
		"issue":      map[string]any{"id": 1000 + n, "number": n, "title": "Login broken", "body": "steps", "html_url": "https://github.com/acme/widgets/issues/1", "user": map[string]any{"login": "jane", "type": "User"}}}
}

func setup() (*Processor, *memStore, *fakeEngine) {
	m := newMem()
	eng := &fakeEngine{started: map[string]ticketflow.Input{}}
	p := &Processor{Store: m, Engine: eng, Sentry: sentryRoutes{repo: m.repoID}, Concurrency: 2,
		Normalizer:            intake.Registry{intake.SourceGitHub: Normalizers("alpine-dev[bot]")[intake.SourceGitHub], intake.SourceSentry: sentryNorm{}},
		MaxParallelTicketsOrg: 5, MaxParallelTicketsRepo: 3}
	return p, m, eng
}

func TestIssueOpenedCreatesTicketAndStartsWorkflow(t *testing.T) {
	p, m, eng := setup()
	ev := m.add("github", "d1", "issues", issue("opened", 12, 42))
	n, err := p.Drain(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, n)
	tid := TicketID(m.org, intake.SourceGitHub, "gh:777#12")
	require.Equal(t, "ticket_created:"+tid.String(), m.marks[ev])
	tk := m.tickets[tid]
	require.Equal(t, "GH-12", tk.Key)
	require.Equal(t, "gh:777#12", tk.ExternalID)
	require.Equal(t, ticketflow.WorkflowID(m.org.String(), tid.String()), tk.WorkflowID)
	require.Len(t, eng.started, 1)
	require.Equal(t, 5, eng.started[tk.WorkflowID].MaxParallelTicketsOrg)

	// Same issue under a new delivery id (GitHub redelivery): no second ticket or workflow.
	ev2 := m.add("github", "d2", "issues", issue("opened", 12, 42))
	p.Drain(context.Background())
	require.Equal(t, "duplicate:"+tid.String(), m.marks[ev2])
	require.Len(t, m.tickets, 1)
	require.Len(t, eng.started, 1)
}

func TestWorkflowStartFailureRetriesWithoutDuplicateTicket(t *testing.T) {
	p, m, eng := setup()
	eng.fail = errors.New("temporal unavailable")
	ev := m.add("github", "d1", "issues", issue("opened", 3, 42))
	p.Drain(context.Background())
	_, marked := m.marks[ev]
	require.False(t, marked, "left pending for retry")
	require.Len(t, m.tickets, 1)
	eng.fail = nil
	p.Drain(context.Background())
	require.Contains(t, m.marks[ev], "workflow_started:")
	require.Len(t, eng.started, 1)
}

func TestIgnoredAndRejected(t *testing.T) {
	p, m, _ := setup()
	labeled := m.add("github", "d1", "issues", issue("labeled", 1, 42))
	unknown := m.add("github", "d2", "issues", issue("opened", 1, 99))
	bot := issue("opened", 2, 42)
	bot["issue"].(map[string]any)["user"] = map[string]any{"login": "alpine-dev[bot]", "type": "Bot"}
	botEv := m.add("github", "d3", "issues", bot)
	m.events[uuid.New()] = &ticketrepo.InboxEvent{ID: uuid.New(), OrgID: m.org, Source: "github", DeliveryID: "d4", EventType: "issues", Payload: []byte("{not json")}
	p.Drain(context.Background())
	require.Equal(t, "ignored:update", m.marks[labeled])
	require.Equal(t, "ignored:unknown_installation", m.marks[unknown])
	require.Equal(t, "ignored:bot_author", m.marks[botEv])
	require.Empty(t, m.tickets)
}

func TestSentryMapsProjectToRepoAndDedupes(t *testing.T) {
	p, m, eng := setup()
	a := m.add("sentry", "req-1", "issue", map[string]string{"Project": "web-frontend", "Key": "9", "Title": "TypeError"})
	b := m.add("sentry", "req-2", "issue", map[string]string{"Project": "web-frontend", "Key": "9", "Title": "TypeError again"})
	c := m.add("sentry", "req-3", "issue", map[string]string{"Project": "unmapped", "Key": "10"})
	p.Drain(context.Background())
	tid := TicketID(m.org, intake.SourceSentry, "sentry:issue:9")
	outcomes := []string{m.marks[a], m.marks[b]}
	require.Contains(t, outcomes, "ticket_created:"+tid.String())
	require.Contains(t, outcomes, "duplicate:"+tid.String())
	require.Equal(t, "ignored:unmapped_sentry_project", m.marks[c])
	require.Len(t, m.tickets, 1)
	require.Equal(t, m.repoID, m.tickets[tid].RepoID)
	require.Equal(t, "SENTRY-"+strings.ToUpper(tid.String()[:6]), m.tickets[tid].Key)
	require.Len(t, eng.started, 1)
}

func TestCloseAndPRMergedSignal(t *testing.T) {
	p, m, eng := setup()
	m.add("github", "d1", "issues", issue("opened", 5, 42))
	p.Drain(context.Background())
	tid := TicketID(m.org, intake.SourceGitHub, "gh:777#5")

	merged := m.add("github", "d2", "pull_request", map[string]any{"action": "closed", "installation": map[string]any{"id": 42},
		"repository":   map[string]any{"id": 777, "full_name": "acme/widgets"},
		"pull_request": map[string]any{"number": 99, "merged": true, "head": map[string]any{"ref": ticketflow.BranchName(tid, "Login broken")}}})
	p.Drain(context.Background())
	require.Equal(t, ticketflow.ActionMarkMerged+":"+tid.String(), m.marks[merged])

	closed := m.add("github", "d3", "issues", issue("closed", 5, 42))
	p.Drain(context.Background())
	require.Equal(t, ticketflow.ActionClose+":"+tid.String(), m.marks[closed])
	require.Len(t, eng.signals, 2)
	require.Equal(t, "integration:github", eng.signals[0].Actor)

	notOurs := m.add("github", "d4", "pull_request", map[string]any{"action": "closed", "installation": map[string]any{"id": 42},
		"pull_request": map[string]any{"number": 100, "merged": true, "head": map[string]any{"ref": "feature/x"}}})
	p.Drain(context.Background())
	require.Equal(t, "ignored", m.marks[notOurs])
}
