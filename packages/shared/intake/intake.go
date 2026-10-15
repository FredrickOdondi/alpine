// Package intake defines the source-agnostic contract between intake sources (GitHub Issues,
// Sentry, later Slack/Linear/Jira/widget) and the Alpine pipeline.
//
// Flow: a webhook receiver verifies the request (Verifier) and stores the raw payload in the
// `inbox_events` table. The inbox processor later loads the row and asks the Normalizer for that
// source to turn it into zero or more TicketIntake values, which become tickets (or are attached
// to existing ones by dedupe) and start/signal the per-ticket Temporal workflow.
//
// Implementations live in services/integrations; this package only holds the types so that the
// API, the orchestrator and integrations can evolve independently.
package intake

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

// Source identifies where an intake event came from. Values match the OpenAPI `source` enum and
// the `inbox_events.source` / `ticket_sources.source` columns.
type Source string

const (
	SourceGitHub Source = "github"
	SourceSentry Source = "sentry"
	SourceSlack  Source = "slack"
	SourceLinear Source = "linear"
	SourceJira   Source = "jira"
	SourceWidget Source = "widget"
)

// Action is what the pipeline should do with a normalized event.
type Action string

const (
	// ActionCreate opens a new ticket (subject to dedupe in triage).
	ActionCreate Action = "create"
	// ActionUpdate updates title/body/labels of the ticket linked to ExternalID.
	ActionUpdate Action = "update"
	// ActionClose closes the linked ticket (e.g. issue closed, Sentry issue resolved).
	ActionClose Action = "close"
	// ActionReopen reopens a closed ticket.
	ActionReopen Action = "reopen"
	// ActionComment adds human context (e.g. an issue comment) to the linked ticket's workflow.
	ActionComment Action = "comment"
	// ActionPRMerged signals that an Alpine PR was merged by a human.
	ActionPRMerged Action = "pr_merged"
)

// Severity is a normalized severity.
type Severity string

const (
	SeverityUnknown Severity = ""
	SeverityDebug   Severity = "debug"
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
	SeverityFatal   Severity = "fatal"
)

// RepoRef identifies the SCM repository a ticket belongs to.
type RepoRef struct {
	Provider       string `json:"provider"`       // "github"
	InstallationID int64  `json:"installationId"` // GitHub App installation id
	RepoID         int64  `json:"repoId"`         // provider repo id (stable across renames)
	FullName       string `json:"fullName"`       // "acme/widgets"
	DefaultBranch  string `json:"defaultBranch"`  // "main"
}

// TicketIntake is the common, source-agnostic shape of an intake event. All text fields are
// UNTRUSTED user input (prompt-injection posture, architecture §9.2): never interpolate them into
// privileged actions; PII scrubbing happens before any LLM use.
type TicketIntake struct {
	Source     Source `json:"source"`
	DeliveryID string `json:"deliveryId"` // provider delivery id (idempotency key with Source)
	Action     Action `json:"action"`

	// ExternalID is the stable id of the item at the source, used to link updates, e.g.
	// "gh:<repo_id>#<n>" for a GitHub issue or the Sentry issue id.
	ExternalID  string `json:"externalId"`
	ExternalURL string `json:"externalUrl,omitempty"`
	// Number is the human-facing number at the source when there is one (GitHub issue #).
	Number int `json:"number,omitempty"`

	// Repo is empty for sources that are not repo-scoped (Sentry): the inbox processor resolves
	// the repo via the org's sentry project -> repo mapping.
	Repo RepoRef `json:"repo"`
	// SentryProject is the Sentry project slug/id for Sentry events (used for repo mapping).
	SentryProject string `json:"sentryProject,omitempty"`

	Title  string   `json:"title"`
	Body   string   `json:"body"`
	Labels []string `json:"labels,omitempty"`
	Author string   `json:"author,omitempty"`

	// DedupeKey groups events that describe the same underlying problem (e.g. the Sentry issue
	// fingerprint / group id, or "gh:<repo_id>#<n>" for GitHub). The inbox processor dedupes on
	// (Source, DeliveryID) for idempotency and on (Source, DedupeKey) to attach repeat events to
	// an existing ticket (Sentry has no delivery replay window).
	DedupeKey string `json:"dedupeKey,omitempty"`
	// Severity is the normalized severity (Sentry level, or derived from GitHub labels).
	Severity Severity `json:"severity,omitempty"`
	// Raw is the original provider payload (or the relevant sub-object), kept for triage context.
	// UNTRUSTED; PII-scrub before any LLM use.
	Raw json.RawMessage `json:"raw,omitempty"`

	// Deprecated: use DedupeKey. Sentry-style signals used by triage dedupe.
	Fingerprint string `json:"fingerprint,omitempty"`
	// Deprecated: use Severity.
	Level       string   `json:"level,omitempty"`
	Culprit     string   `json:"culprit,omitempty"` // top frame / transaction
	Release     string   `json:"release,omitempty"` // first-seen release
	StackFrames []string `json:"stackFrames,omitempty"`

	OccurredAt time.Time `json:"occurredAt"`
	// Metadata carries small, source-specific scalars (e.g. PR head branch for ActionPRMerged).
	Metadata map[string]string `json:"metadata,omitempty"`
}

// ErrIgnored is returned by a Normalizer for events the pipeline does not act on (ping, labels
// we don't watch, bot comments…). The inbox processor marks such rows processed without error.
var ErrIgnored = errors.New("intake: event ignored")

// ErrInvalidSignature is returned by a Verifier when a webhook signature is missing or wrong.
var ErrInvalidSignature = errors.New("intake: invalid webhook signature")

// Verifier authenticates a webhook request for one source (GitHub HMAC-SHA256, Sentry client
// secret signature, Slack signing secret, …). It must use constant-time comparison.
type Verifier interface {
	Source() Source
	Verify(headers http.Header, body []byte) error
}

// Normalizer turns one raw inbox payload into intake events. eventType is the provider's event
// name (e.g. GitHub "issues", Sentry "issue"). Implementations must be pure (no network calls) so
// the inbox processor can retry them safely.
type Normalizer interface {
	Source() Source
	Normalize(ctx context.Context, eventType string, payload []byte) ([]TicketIntake, error)
}

// Registry maps sources to normalizers.
type Registry map[Source]Normalizer

// Normalize dispatches to the normalizer for source, or returns ErrIgnored if none is registered.
func (r Registry) Normalize(ctx context.Context, source Source, eventType string, payload []byte) ([]TicketIntake, error) {
	n, ok := r[source]
	if !ok {
		return nil, ErrIgnored
	}
	return n.Normalize(ctx, eventType, payload)
}
