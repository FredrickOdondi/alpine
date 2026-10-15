// Package ticketrepo is the hand-written pgx store for the ticket pipeline (inbox, tickets, runs,
// turns, gates, PRs, side effects). Identity, tenancy, audit-by-actor and metering live in
// jeff/data's sqlc repos (packages/shared/repo/pgrepo); this package covers the tables those
// repos don't. Every tenant query runs inside db.WithTenant so RLS applies.
package ticketrepo

import (
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/alpine-dev/alpine/packages/shared/gate"
	"github.com/alpine-dev/alpine/packages/shared/ticket"
)

// Repo row.
type Repo struct {
	ID                   uuid.UUID
	OrgID                uuid.UUID
	FullName             string
	DefaultBranch        string
	GitHubInstallationID int64
	ConfigSHA            *string
	CreatedAt            time.Time
}

// Ticket is the ticket row joined with its repo name.
type Ticket struct {
	ID               uuid.UUID
	OrgID            uuid.UUID
	RepoID           uuid.UUID
	RepoFullName     string
	Key              string
	Title            string
	Body             string
	Type             string
	Stage            ticket.Stage
	Source           string
	SourceURL        *string
	Priority         *string
	Size             *string
	BRPredicted      *int
	BRFinal          *int
	Confidence       *float64
	WorkflowID       *string
	TurnsUsed        int
	GateIterations   int
	CostUSD          float64
	TokensUsed       int64
	MaxTurns         int
	MaxGateIters     int
	MaxCostUSD       float64
	EscalationReason *string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	DefaultBranch    string
	// GitHubExternalID is the first GitHub source ("gh:<repo_id>#<n>"), if any.
	GitHubExternalID *string
}

// IssueNumber parses the issue number from GitHubExternalID ("gh:<repo_id>#<n>"); 0 if none.
func (t Ticket) IssueNumber() int {
	if t.GitHubExternalID == nil {
		return 0
	}
	s := *t.GitHubExternalID
	i := strings.LastIndexByte(s, '#')
	if i < 0 {
		return 0
	}
	n, err := strconv.Atoi(s[i+1:])
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// StageTransition row.
type StageTransition struct {
	ID        int64
	FromStage *string
	ToStage   string
	Reason    *string
	Actor     string
	At        time.Time
}

// Run row.
type Run struct {
	ID             uuid.UUID
	TicketID       uuid.UUID
	Kind           string
	Status         string
	Model          *string
	Turns          int
	GateIterations int
	CostUSD        float64
	StartedAt      time.Time
	EndedAt        *time.Time
}

// ToolCallSummary is one entry of agent_turns.tool_calls.
type ToolCallSummary struct {
	Name    string `json:"name"`
	Summary string `json:"summary,omitempty"`
}

// Turn row.
type Turn struct {
	ID            int64
	RunID         uuid.UUID
	N             int
	Model         string
	TokensIn      int64
	TokensOut     int64
	TokensCached  int64
	CostUSD       float64
	ToolCalls     []ToolCallSummary
	TranscriptRef *string
	CreatedAt     time.Time
}

// GateResult row.
type GateResult struct {
	ID           uuid.UUID
	RunID        uuid.UUID
	TicketID     uuid.UUID
	Iteration    int
	GateID       string
	Status       gate.Status
	DurationMs   int64
	Findings     []gate.Finding
	ArtifactsRef *string
	CreatedAt    time.Time
}

// PullRequest row joined with ticket info.
type PullRequest struct {
	ID           uuid.UUID
	TicketID     uuid.UUID
	TicketKey    string
	TicketTitle  string
	RepoFullName string
	Number       int
	URL          string
	HeadBranch   string
	HeadSHA      *string
	State        string
	BlastRadius  int
	Priority     *string
	TurnsUsed    int
	CostUSD      float64
	OpenedAt     time.Time
	MergedAt     *time.Time
	// Diff summary (nil until the SCM reports it) and the human decision from the PR inbox.
	Additions     *int
	Deletions     *int
	FilesChanged  *int
	HumanDecision *string
	// Gate counts for the ticket's latest gate iteration.
	GatesPassed int
	GatesFailed int
	GatesWarned int
	GatesTotal  int
}

// TicketFilter for listing.
type TicketFilter struct {
	RepoID *uuid.UUID
	Stages []ticket.Stage
	Limit  int
}

// InboxEvent row.
type InboxEvent struct {
	ID         uuid.UUID
	OrgID      uuid.UUID
	Source     string
	DeliveryID string
	EventType  string
	Action     *string
	Payload    []byte
	Attempts   int
}

// PendingInbox is a work item for the inbox processor.
type PendingInbox struct {
	ID    uuid.UUID
	OrgID uuid.UUID
}
