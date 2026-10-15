// Package intakemeta names the intake.TicketIntake.Metadata keys populated by
// the normalizers in services/integrations, so consumers (inbox processor,
// triage) do not hard-code strings.
package intakemeta

const (
	// EventType is the provider event name (X-GitHub-Event / Sentry-Hook-Resource).
	EventType = "event_type"
	// SourceAction is the provider's own action (opened, labeled, resolved, ...),
	// before mapping to intake.Action.
	SourceAction = "source_action"
	// Kind is the item kind: see the Kind* constants.
	Kind = "kind"

	// GitHub.
	IssueID       = "github_issue_id"    // numeric issue id, stable across renames/transfers
	DisplayRef    = "github_display_ref" // "owner/repo#n" for display only; ExternalID is gh:<repo_id>#<n>
	CommentID     = "github_comment_id"  // for comments
	CommentURL    = "github_comment_url"
	AuthorIsBot   = "author_is_bot" // "true" when the author is a bot account
	SenderLogin   = "github_sender"
	PullRequestNo = "github_pull_number" // set for PR conversation comments

	// Sentry.
	SentryInstallation = "sentry_installation" // integration installation uuid -> maps to a source/org
	SentryProjectID    = "sentry_project_id"
	SentryIssueID      = "sentry_issue_id"
	SentryShortID      = "sentry_short_id"
	SentryEventID      = "sentry_event_id"
	SentryStatus       = "sentry_status"
	SentryRule         = "sentry_triggered_rule"
	// AltDedupeKey is a secondary dedupe key (TicketIntake.DedupeKey holds the
	// primary one); kept here because the shared contract has no field for it.
	AltDedupeKey = "alt_dedupe_key"
)

// Values for the Kind key.
const (
	KindIssue              = "issue"
	KindComment            = "comment"
	KindPullRequestComment = "pull_request_comment"
	KindErrorGroup         = "error_group" // Sentry issue
	KindErrorEvent         = "error_event" // Sentry error / event alert
)
