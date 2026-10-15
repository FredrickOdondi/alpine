package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/alpine-dev/alpine/packages/shared/intake"
	"github.com/alpine-dev/alpine/services/integrations/intakemeta"
)

// ErrMalformedPayload is returned (wrapped) when a supported event cannot be
// decoded or lacks required fields. Unlike intake.ErrIgnored it is a real
// failure the inbox processor should surface.
var ErrMalformedPayload = errors.New("github webhook: malformed payload")

// Normalizer implements intake.Normalizer for GitHub "issues" and
// "issue_comment" events. Everything else returns intake.ErrIgnored.
type Normalizer struct{}

var _ intake.Normalizer = Normalizer{}

// Source implements intake.Normalizer.
func (Normalizer) Source() intake.Source { return intake.SourceGitHub }

// Action mapping. Actions not listed are intake.ErrIgnored.
var (
	issueActions = map[string]intake.Action{
		"opened":      intake.ActionCreate,
		"edited":      intake.ActionUpdate,
		"labeled":     intake.ActionUpdate,
		"unlabeled":   intake.ActionUpdate,
		"transferred": intake.ActionUpdate,
		"closed":      intake.ActionClose,
		"deleted":     intake.ActionClose,
		"reopened":    intake.ActionReopen,
	}
	commentActions = map[string]intake.Action{
		"created": intake.ActionComment,
		"edited":  intake.ActionComment,
	}
)

type user struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

func (u *user) login() string {
	if u == nil {
		return ""
	}
	return u.Login
}

func (u *user) isBot() bool {
	return u != nil && (u.Type == "Bot" || strings.HasSuffix(u.Login, "[bot]"))
}

type label struct {
	Name string `json:"name"`
}

type issue struct {
	ID          int64      `json:"id"`
	Number      int        `json:"number"`
	Title       string     `json:"title"`
	Body        *string    `json:"body"`
	HTMLURL     string     `json:"html_url"`
	User        *user      `json:"user"`
	Labels      []label    `json:"labels"`
	PullRequest *struct{}  `json:"pull_request"`
	CreatedAt   *time.Time `json:"created_at"`
	UpdatedAt   *time.Time `json:"updated_at"`
}

type comment struct {
	ID        int64      `json:"id"`
	Body      *string    `json:"body"`
	HTMLURL   string     `json:"html_url"`
	User      *user      `json:"user"`
	CreatedAt *time.Time `json:"created_at"`
	UpdatedAt *time.Time `json:"updated_at"`
}

type payload struct {
	Action     string   `json:"action"`
	Issue      *issue   `json:"issue"`
	Comment    *comment `json:"comment"`
	Repository *struct {
		ID            int64  `json:"id"`
		FullName      string `json:"full_name"`
		DefaultBranch string `json:"default_branch"`
	} `json:"repository"`
	Sender       *user `json:"sender"`
	Installation *struct {
		ID int64 `json:"id"`
	} `json:"installation"`
}

// ExternalID returns the rename/transfer-proof GitHub ticket id
// "gh:<repo_id>#<number>". The numeric repository id survives renames and
// owner changes; "owner/repo#n" is kept only as Metadata[github_display_ref].
func ExternalID(repoID int64, number int) string {
	return "gh:" + strconv.FormatInt(repoID, 10) + "#" + strconv.Itoa(number)
}

// Normalize implements intake.Normalizer. It is pure (no I/O). On success it
// returns exactly one TicketIntake. Comments by bot accounts (including
// Alpine's own) and deleted comments are intake.ErrIgnored to avoid loops.
func (Normalizer) Normalize(_ context.Context, eventType string, raw []byte) ([]intake.TicketIntake, error) {
	var actions map[string]intake.Action
	switch eventType {
	case "issues":
		actions = issueActions
	case "issue_comment":
		actions = commentActions
	default:
		return nil, fmt.Errorf("github %q: unsupported event: %w", eventType, intake.ErrIgnored)
	}
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("github %s: %w: %v", eventType, ErrMalformedPayload, err)
	}
	if p.Action == "" {
		return nil, fmt.Errorf("github %s: %w: missing action", eventType, ErrMalformedPayload)
	}
	action, ok := actions[p.Action]
	if !ok {
		return nil, fmt.Errorf("github %s.%s: %w", eventType, p.Action, intake.ErrIgnored)
	}
	if p.Issue == nil || p.Issue.ID == 0 || p.Issue.Number == 0 {
		return nil, fmt.Errorf("github %s: %w: missing issue id/number", eventType, ErrMalformedPayload)
	}
	if p.Repository == nil || p.Repository.FullName == "" || p.Repository.ID == 0 {
		return nil, fmt.Errorf("github %s: %w: missing repository id/full_name", eventType, ErrMalformedPayload)
	}

	is := p.Issue
	t := intake.TicketIntake{
		Source:     intake.SourceGitHub,
		Action:     action,
		ExternalID: ExternalID(p.Repository.ID, is.Number),
		Number:     is.Number,
		Repo: intake.RepoRef{
			Provider:      string(intake.SourceGitHub),
			RepoID:        p.Repository.ID,
			FullName:      p.Repository.FullName,
			DefaultBranch: p.Repository.DefaultBranch,
		},
		Title:  is.Title,
		Labels: labelNames(is.Labels),
		Metadata: map[string]string{
			intakemeta.EventType:    eventType,
			intakemeta.SourceAction: p.Action,
			intakemeta.IssueID:      strconv.FormatInt(is.ID, 10),
			intakemeta.DisplayRef:   p.Repository.FullName + "#" + strconv.Itoa(is.Number),
		},
	}
	if p.Installation != nil {
		t.Repo.InstallationID = p.Installation.ID
	}
	t.DedupeKey = t.ExternalID
	t.Severity = severityFromLabels(t.Labels)
	t.Raw = append(json.RawMessage(nil), raw...)
	if p.Sender != nil && p.Sender.Login != "" {
		t.Metadata[intakemeta.SenderLogin] = p.Sender.Login
	}

	var author *user
	switch eventType {
	case "issues":
		if is.PullRequest != nil { // GitHub never sends this, but be strict
			return nil, fmt.Errorf("github issues: %w: issue is a pull request", ErrMalformedPayload)
		}
		t.Metadata[intakemeta.Kind] = intakemeta.KindIssue
		t.ExternalURL = is.HTMLURL
		t.Body = deref(is.Body)
		author = is.User
		t.OccurredAt = firstTime(is.UpdatedAt, is.CreatedAt)
	case "issue_comment":
		c := p.Comment
		if c == nil || c.ID == 0 {
			return nil, fmt.Errorf("github issue_comment: %w: missing comment id", ErrMalformedPayload)
		}
		author = c.User
		if author == nil {
			author = p.Sender
		}
		if author.isBot() {
			return nil, fmt.Errorf("github issue_comment by bot %q: %w", author.login(), intake.ErrIgnored)
		}
		t.Metadata[intakemeta.Kind] = intakemeta.KindComment
		if is.PullRequest != nil {
			t.Metadata[intakemeta.Kind] = intakemeta.KindPullRequestComment
			t.Metadata[intakemeta.PullRequestNo] = strconv.Itoa(is.Number)
		}
		t.Metadata[intakemeta.CommentID] = strconv.FormatInt(c.ID, 10)
		t.Metadata[intakemeta.CommentURL] = c.HTMLURL
		t.ExternalURL = c.HTMLURL
		t.Body = deref(c.Body)
		t.OccurredAt = firstTime(c.UpdatedAt, c.CreatedAt)
	}
	if author == nil {
		author = p.Sender
	}
	t.Author = author.login()
	if author.isBot() {
		t.Metadata[intakemeta.AuthorIsBot] = "true"
	}
	return []intake.TicketIntake{t}, nil
}

func labelNames(ls []label) []string {
	var out []string
	for _, l := range ls {
		if l.Name != "" {
			out = append(out, l.Name)
		}
	}
	return out
}

var severityRank = map[intake.Severity]int{
	intake.SeverityDebug: 1, intake.SeverityInfo: 2, intake.SeverityWarning: 3, intake.SeverityError: 4, intake.SeverityFatal: 5,
}

// severityFromLabels maps conventional labels to intake.Severity; the highest
// wins. Case-insensitive, with an optional severity:/severity//sev:/sev//
// priority:/priority/ prefix:
//
//	critical, p0 -> fatal      high, p1 -> error
//	medium, p2   -> warning    low, p3  -> info
//
// With a prefix, the intake vocabulary itself is also accepted
// (severity:fatal|error|warning|info|debug).
func severityFromLabels(labels []string) intake.Severity {
	best := intake.SeverityUnknown
	for _, l := range labels {
		s := labelSeverity(strings.ToLower(strings.TrimSpace(l)))
		if severityRank[s] > severityRank[best] {
			best = s
		}
	}
	return best
}

func labelSeverity(l string) intake.Severity {
	prefixed := false
	for _, p := range []string{"severity:", "severity/", "sev:", "sev/", "priority:", "priority/"} {
		if rest, ok := strings.CutPrefix(l, p); ok {
			l, prefixed = strings.TrimSpace(rest), true
			break
		}
	}
	switch l {
	case "critical", "p0":
		return intake.SeverityFatal
	case "high", "p1":
		return intake.SeverityError
	case "medium", "p2":
		return intake.SeverityWarning
	case "low", "p3":
		return intake.SeverityInfo
	}
	if !prefixed { // bare "error", "info", "debug" … are too ambiguous as labels
		return intake.SeverityUnknown
	}
	switch l {
	case "fatal":
		return intake.SeverityFatal
	case "error":
		return intake.SeverityError
	case "warning":
		return intake.SeverityWarning
	case "info":
		return intake.SeverityInfo
	case "debug":
		return intake.SeverityDebug
	}
	return intake.SeverityUnknown
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func firstTime(ts ...*time.Time) time.Time {
	for _, t := range ts {
		if t != nil && !t.IsZero() {
			return t.UTC()
		}
	}
	return time.Time{}
}
