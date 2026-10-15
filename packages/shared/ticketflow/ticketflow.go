// Package ticketflow is the contract between the API (which starts and signals ticket
// workflows) and the orchestrator (which runs them): workflow name, task queues, ids, input,
// signals and queries. Keeping it here avoids the API importing orchestrator code.
package ticketflow

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/alpine-dev/alpine/packages/shared/budget"
	"github.com/alpine-dev/alpine/packages/shared/ticket"
)

// WorkflowName is the registered name of the per-ticket workflow.
const WorkflowName = "TicketWorkflow"

// Task queues. Agent work is split by priority class so P0/P1 tickets are not stuck behind a
// backlog of P3s; workers poll the high queue with more slots (fair queuing across priorities),
// and per-org semaphores bound how much of each queue one tenant can occupy.
const (
	TaskQueueWorkflows = "alpine-tickets"
	TaskQueueControl   = "alpine-control" // DB/bookkeeping activities
	TaskQueueGates     = "alpine-gates"
	TaskQueueAgentsHi  = "alpine-agents-high"
	TaskQueueAgentsMid = "alpine-agents-normal"
	TaskQueueAgentsLow = "alpine-agents-low"
)

// AgentTaskQueue maps a ticket priority to its agent task queue.
func AgentTaskQueue(priority string) string {
	switch priority {
	case "P0", "P1":
		return TaskQueueAgentsHi
	case "P3":
		return TaskQueueAgentsLow
	default:
		return TaskQueueAgentsMid
	}
}

// WorkflowID is the deterministic workflow id for a ticket (architecture §5.1).
func WorkflowID(orgID, ticketID string) string { return fmt.Sprintf("ticket/%s/%s", orgID, ticketID) }

// Input starts a TicketWorkflow.
type Input struct {
	OrgID    string            `json:"orgId"`
	TicketID string            `json:"ticketId"`
	RepoID   string            `json:"repoId"`
	Caps     budget.TicketCaps `json:"caps"`
	// MaxParallelTicketsOrg/Repo bound concurrent implementing tickets (fairness).
	MaxParallelTicketsOrg  int `json:"maxParallelTicketsOrg"`
	MaxParallelTicketsRepo int `json:"maxParallelTicketsRepo"`
	// MergePolicy maps "BR1".."BR4" to "human" | "auto" (MVP: always human).
	MergePolicy map[string]string `json:"mergePolicy,omitempty"`
	// MaxReviewRounds bounds request_changes loops before escalation (default 3).
	MaxReviewRounds int `json:"maxReviewRounds,omitempty"`
}

// SignalHumanDecision carries a HumanDecision.
const SignalHumanDecision = "human-decision"

// Human decision actions (OpenAPI TicketAction).
const (
	ActionApprove    = "approve"
	ActionReject     = "reject"
	ActionResume     = "resume"
	ActionClose      = "close"
	ActionMarkMerged = "mark_merged"
)

// HumanDecision is a signal payload.
type HumanDecision struct {
	Action string `json:"action"`
	Note   string `json:"note,omitempty"`
	Actor  string `json:"actor"` // "user:<login>" or "github:<login>"
}

// QueryState returns the workflow's State.
const QueryState = "state"

// State is the queryable workflow state.
type State struct {
	Stage            ticket.Stage       `json:"stage"`
	Usage            budget.TicketUsage `json:"usage"`
	Caps             budget.TicketCaps  `json:"caps"`
	Attempt          int                `json:"attempt"`
	EscalationReason string             `json:"escalationReason,omitempty"`
	PullRequest      int                `json:"pullRequest,omitempty"`
}

// crockford is the ULID alphabet.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// TicketULID renders a ticket's 128-bit id in ULID text form (26 Crockford base32 chars). Ticket
// ids are UUIDs in Postgres; this is the stable short form used in branch names. It is a pure
// re-encoding, so ParseTicketULID recovers the UUID.
func TicketULID(id uuid.UUID) string {
	var out [26]byte
	// 128 bits -> 26 chars of 5 bits, first char carries the top 3 bits (ULID layout).
	var hi, lo uint64
	for i := 0; i < 8; i++ {
		hi = hi<<8 | uint64(id[i])
		lo = lo<<8 | uint64(id[8+i])
	}
	for i := 25; i >= 0; i-- {
		out[i] = crockford[lo&31]
		lo = lo>>5 | (hi&31)<<59
		hi >>= 5
	}
	return string(out[:])
}

// ParseTicketULID is the inverse of TicketULID.
func ParseTicketULID(s string) (uuid.UUID, error) {
	var id uuid.UUID
	if len(s) != 26 || strings.IndexByte("01234567", s[0]) < 0 {
		return id, fmt.Errorf("ticketflow: invalid ticket ulid %q", s)
	}
	var hi, lo uint64
	for i := 0; i < 26; i++ {
		v := strings.IndexByte(crockford, s[i])
		if v < 0 {
			return id, fmt.Errorf("ticketflow: invalid ticket ulid %q", s)
		}
		hi = hi<<5 | lo>>59
		lo = lo<<5 | uint64(v)
	}
	for i := 7; i >= 0; i-- {
		id[i] = byte(hi)
		id[8+i] = byte(lo)
		hi >>= 8
		lo >>= 8
	}
	return id, nil
}

// MaxSlugLen matches the integrations GitHub client (github.MaxSlugLen).
const MaxSlugLen = 48

// BranchName returns the ticket's work branch, "alpine/<ticket_ulid>-<slug>". The ULID makes it
// unique per ticket. Production names come from the integrations client's github.NewBranchName
// (via the orchestrator's SCM seam); this mirror keeps the stub/offline path on the same format.
func BranchName(ticketID uuid.UUID, title string) string {
	return "alpine/" + TicketULID(ticketID) + "-" + Slugify(title)
}

// Slugify lowercases title, keeps [a-z0-9], collapses everything else to single dashes and caps
// the result at MaxSlugLen (cutting at a dash when one is in the second half). "ticket" if empty.
func Slugify(title string) string {
	var b strings.Builder
	sep := false
	for _, r := range strings.ToLower(title) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if sep {
				b.WriteByte('-')
				sep = false
			}
			b.WriteRune(r)
			continue
		}
		sep = b.Len() > 0
	}
	out := b.String()
	if len(out) > MaxSlugLen {
		out = out[:MaxSlugLen]
		if i := strings.LastIndexByte(out, '-'); i >= MaxSlugLen/2 {
			out = out[:i]
		}
		out = strings.TrimRight(out, "-")
	}
	if out == "" {
		return "ticket"
	}
	return out
}

// ExternalKey turns an internal idempotency key (e.g. "<workflowID>/<activityID>", which can be
// long and contain any characters) into a key accepted by the GitHub client's comment markers:
// 1-128 chars of [A-Za-z0-9._:/@+=-], never "--". It is a stable hash, so retries of the same
// activity produce the same key.
func ExternalKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return "alpine:" + hex.EncodeToString(sum[:])[:48]
}

// StepKey builds the idempotency key for a side effect: "<workflowID>/<activityID>" or a named
// step. Keys stay within the GitHub client's comment-key alphabet ([A-Za-z0-9._:/@+=-]).
func StepKey(workflowID, step string) string { return workflowID + "/" + step }
