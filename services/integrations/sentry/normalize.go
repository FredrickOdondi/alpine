package sentry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/alpine-dev/alpine/packages/shared/intake"
	"github.com/alpine-dev/alpine/services/integrations/intakemeta"
)

// ErrMalformedPayload is returned (wrapped) when a supported resource cannot
// be decoded or lacks required fields.
var ErrMalformedPayload = errors.New("sentry webhook: malformed payload")

// MaxStackFrames caps TicketIntake.StackFrames.
const MaxStackFrames = 10

// Normalizer implements intake.Normalizer for Sentry. Supported
// Sentry-Hook-Resource values:
//   - "issue": created->create, resolved/archived/ignored->close, unresolved->reopen, assigned->update
//   - "error": created->create
//   - "event_alert": triggered->create
//
// Everything else (installation, metric_alert, comment, seer, ...) is intake.ErrIgnored.
type Normalizer struct{}

var _ intake.Normalizer = Normalizer{}

// Source implements intake.Normalizer.
func (Normalizer) Source() intake.Source { return intake.SourceSentry }

var (
	issueActions = map[string]intake.Action{
		"created":    intake.ActionCreate,
		"assigned":   intake.ActionUpdate,
		"resolved":   intake.ActionClose,
		"archived":   intake.ActionClose,
		"ignored":    intake.ActionClose, // legacy name for archived
		"unresolved": intake.ActionReopen,
	}
	errorActions      = map[string]intake.Action{"created": intake.ActionCreate}
	eventAlertActions = map[string]intake.Action{"triggered": intake.ActionCreate}
)

// flexID accepts a JSON string or number (Sentry is inconsistent: issue.id
// and project.id are strings in issue payloads, project is a number in error
// payloads, group_id is a number in event alerts).
type flexID string

func (f *flexID) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if bytes.Equal(b, []byte("null")) {
		*f = ""
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = flexID(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("id must be string or number: %w", err)
	}
	*f = flexID(n.String())
	return nil
}

type actor struct {
	Type string `json:"type"`
	ID   flexID `json:"id"`
	Name string `json:"name"`
}

type metadata struct {
	Type     string `json:"type"`
	Value    string `json:"value"`
	Title    string `json:"title"`
	Filename string `json:"filename"`
	Function string `json:"function"`
}

type issue struct {
	ID       flexID   `json:"id"`
	ShortID  string   `json:"shortId"`
	Title    string   `json:"title"`
	Culprit  string   `json:"culprit"`
	WebURL   string   `json:"web_url"`
	Perma    string   `json:"permalink"`
	Level    string   `json:"level"`
	Priority string   `json:"priority"`
	Status   string   `json:"status"`
	Platform string   `json:"platform"`
	Category string   `json:"issueCategory"`
	Metadata metadata `json:"metadata"`
	Project  *struct {
		ID   flexID `json:"id"`
		Slug string `json:"slug"`
	} `json:"project"`
	FirstSeen string `json:"firstSeen"`
	LastSeen  string `json:"lastSeen"`
}

type frame struct {
	Filename string `json:"filename"`
	AbsPath  string `json:"abs_path"`
	Module   string `json:"module"`
	Function string `json:"function"`
	Lineno   *int   `json:"lineno"`
	InApp    *bool  `json:"in_app"`
}

type errEvent struct {
	EventID     string          `json:"event_id"`
	IssueID     flexID          `json:"issue_id"`
	GroupID     flexID          `json:"group_id"`
	GroupIDAlt  flexID          `json:"groupID"`
	Fingerprint []string        `json:"fingerprint"`
	Title       string          `json:"title"`
	Message     string          `json:"message"`
	Culprit     string          `json:"culprit"`
	Level       string          `json:"level"`
	Platform    string          `json:"platform"`
	Project     flexID          `json:"project"`
	Release     string          `json:"release"`
	Environment string          `json:"environment"`
	Tags        json.RawMessage `json:"tags"`
	Metadata    metadata        `json:"metadata"`
	URL         string          `json:"url"`
	WebURL      string          `json:"web_url"`
	Datetime    string          `json:"datetime"`
	Timestamp   *float64        `json:"timestamp"`
	Exception   *struct {
		Values []struct {
			Stacktrace *struct {
				Frames []frame `json:"frames"`
			} `json:"stacktrace"`
		} `json:"values"`
	} `json:"exception"`
}

type payload struct {
	Action       string `json:"action"`
	Actor        *actor `json:"actor"`
	Installation *struct {
		UUID string `json:"uuid"`
	} `json:"installation"`
	Data struct {
		Issue         *issue    `json:"issue"`
		Error         *errEvent `json:"error"`
		Event         *errEvent `json:"event"`
		TriggeredRule string    `json:"triggered_rule"`
	} `json:"data"`
}

// Normalize implements intake.Normalizer. It is pure and returns exactly one
// TicketIntake on success. Repo is left empty: mapping a Sentry project to a
// repository is source configuration applied by the inbox processor using
// TicketIntake.SentryProject and Metadata[sentry_installation].
func (Normalizer) Normalize(_ context.Context, resource string, raw []byte) ([]intake.TicketIntake, error) {
	var actions map[string]intake.Action
	switch resource {
	case "issue":
		actions = issueActions
	case "error":
		actions = errorActions
	case "event_alert":
		actions = eventAlertActions
	default:
		return nil, fmt.Errorf("sentry %q: unsupported resource: %w", resource, intake.ErrIgnored)
	}
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("sentry %s: %w: %v", resource, ErrMalformedPayload, err)
	}
	if p.Action == "" {
		return nil, fmt.Errorf("sentry %s: %w: missing action", resource, ErrMalformedPayload)
	}
	action, ok := actions[p.Action]
	if !ok {
		return nil, fmt.Errorf("sentry %s.%s: %w", resource, p.Action, intake.ErrIgnored)
	}
	t := intake.TicketIntake{
		Source: intake.SourceSentry,
		Action: action,
		Metadata: map[string]string{
			intakemeta.EventType:    resource,
			intakemeta.SourceAction: p.Action,
		},
	}
	if p.Installation != nil && p.Installation.UUID != "" {
		t.Metadata[intakemeta.SentryInstallation] = p.Installation.UUID
	}
	if p.Actor != nil {
		t.Author = p.Actor.Name
		if t.Author == "" {
			t.Author = string(p.Actor.ID)
		}
	}
	var err error
	switch resource {
	case "issue":
		err = fromIssue(&t, p.Data.Issue)
	case "error":
		err = fromEvent(&t, p.Data.Error)
	case "event_alert":
		err = fromEvent(&t, p.Data.Event)
		if err == nil && p.Data.TriggeredRule != "" {
			t.Labels = append(t.Labels, "rule:"+p.Data.TriggeredRule)
			t.Metadata[intakemeta.SentryRule] = p.Data.TriggeredRule
		}
	}
	if err != nil {
		return nil, fmt.Errorf("sentry %s: %w: %v", resource, ErrMalformedPayload, err)
	}
	t.Raw = append(json.RawMessage(nil), raw...)
	return []intake.TicketIntake{t}, nil
}

func fromIssue(t *intake.TicketIntake, is *issue) error {
	if is == nil || is.ID == "" {
		return fmt.Errorf("missing data.issue.id")
	}
	id := string(is.ID)
	t.ExternalID = id
	t.ExternalURL = firstNonEmpty(is.WebURL, is.Perma)
	t.Title = is.Title
	t.Culprit = is.Culprit
	t.DedupeKey = issueKey(id)
	md := t.Metadata
	md[intakemeta.Kind] = intakemeta.KindErrorGroup
	md[intakemeta.SentryIssueID] = id
	setIf(md, intakemeta.SentryShortID, is.ShortID)
	setIf(md, intakemeta.SentryStatus, is.Status)
	if is.Project != nil {
		t.SentryProject = firstNonEmpty(is.Project.Slug, string(is.Project.ID))
		setIf(md, intakemeta.SentryProjectID, string(is.Project.ID))
	}
	t.Severity = severityFromLevel(is.Level)
	if t.Severity == intake.SeverityUnknown {
		t.Severity = severityFromPriority(is.Priority)
	}
	t.Labels = labels(is.Level, is.Platform, "", "")
	if is.Category != "" {
		t.Labels = append(t.Labels, "category:"+is.Category)
	}
	t.Body = body(is.Metadata, is.Culprit, "", [][2]string{
		{"Short ID", is.ShortID}, {"Status", is.Status}, {"First seen", is.FirstSeen}, {"Last seen", is.LastSeen},
	})
	t.OccurredAt = parseTime(firstNonEmpty(is.LastSeen, is.FirstSeen))
	return nil
}

func fromEvent(t *intake.TicketIntake, e *errEvent) error {
	if e == nil || e.EventID == "" {
		return fmt.Errorf("missing event_id")
	}
	groupID := firstNonEmpty(string(e.IssueID), string(e.GroupID), string(e.GroupIDAlt))
	// ExternalID is the Sentry issue id so every event of a group links to the
	// same ticket; the event id is the fallback when Sentry omits the group.
	t.ExternalID = firstNonEmpty(groupID, e.EventID)
	t.ExternalURL = e.WebURL
	t.Title = firstNonEmpty(e.Title, e.Metadata.Title, e.Message)
	t.Culprit = e.Culprit
	t.Release = e.Release
	t.StackFrames = stackFrames(e)
	env := firstNonEmpty(e.Environment, tag(e.Tags, "environment"))
	t.Labels = labels(e.Level, e.Platform, env, e.Release)
	t.Body = body(e.Metadata, e.Culprit, e.Message, [][2]string{{"Release", e.Release}, {"Environment", env}})
	t.OccurredAt = parseTime(e.Datetime)
	if t.OccurredAt.IsZero() && e.Timestamp != nil {
		sec, frac := math.Modf(*e.Timestamp)
		t.OccurredAt = time.Unix(int64(sec), int64(math.Round(frac*1e3))*int64(time.Millisecond)).UTC()
	}
	var alt string
	t.DedupeKey, alt = dedupeKeys(string(e.Project), e.Fingerprint, groupID, e.EventID)

	md := t.Metadata
	md[intakemeta.Kind] = intakemeta.KindErrorEvent
	md[intakemeta.SentryEventID] = e.EventID
	setIf(md, intakemeta.SentryIssueID, groupID)
	t.SentryProject = firstNonEmpty(projectSlugFromAPIURL(e.URL), string(e.Project))
	setIf(md, intakemeta.SentryProjectID, string(e.Project))
	t.Severity = severityFromLevel(e.Level)
	setIf(md, intakemeta.AltDedupeKey, alt)
	return nil
}

// dedupeKeys derives the primary dedupe key (TicketIntake.DedupeKey) and an
// optional alternate key (Metadata[alt_dedupe_key]); see README.md:
//
//  1. "sentry:fp:<project_id>:<sha256 hex>" when the event has a fully custom
//     fingerprint (no "{{ ... }}" template variables such as "{{ default }}",
//     which Sentry expands server-side) and a project id. The hash covers the
//     project id and each element, NUL-separated. The alt key is then
//     "sentry:issue:<id>" when the group is known.
//  2. "sentry:issue:<issue_id|group_id>" otherwise; issue webhooks always use
//     this form, so they join custom-fingerprint events via the alt key.
//  3. "sentry:event:<event_id>" if neither is available (no dedupe possible).
func dedupeKeys(projectID string, fingerprint []string, groupID, eventID string) (key, alt string) {
	if projectID != "" && isLiteralFingerprint(fingerprint) {
		h := sha256.New()
		h.Write([]byte(projectID))
		for _, f := range fingerprint {
			h.Write([]byte{0})
			h.Write([]byte(f))
		}
		key = "sentry:fp:" + projectID + ":" + hex.EncodeToString(h.Sum(nil))
		if groupID != "" {
			alt = issueKey(groupID)
		}
		return key, alt
	}
	if groupID != "" {
		return issueKey(groupID), ""
	}
	return "sentry:event:" + eventID, ""
}

func issueKey(id string) string { return "sentry:issue:" + id }

func isLiteralFingerprint(fp []string) bool {
	if len(fp) == 0 {
		return false
	}
	for _, f := range fp {
		if strings.Contains(f, "{{") {
			return false
		}
	}
	return true
}

// stackFrames returns up to MaxStackFrames frames of the outermost exception
// (the last value), innermost (crashing) frame first, as "function (file:line)".
// In-app frames are preferred when the SDK marked any.
func stackFrames(e *errEvent) []string {
	if e.Exception == nil || len(e.Exception.Values) == 0 {
		return nil
	}
	st := e.Exception.Values[len(e.Exception.Values)-1].Stacktrace
	if st == nil || len(st.Frames) == 0 {
		return nil
	}
	frames := st.Frames
	var inApp []frame
	for _, f := range frames {
		if f.InApp != nil && *f.InApp {
			inApp = append(inApp, f)
		}
	}
	if len(inApp) > 0 {
		frames = inApp
	}
	var out []string
	for i := len(frames) - 1; i >= 0 && len(out) < MaxStackFrames; i-- {
		f := frames[i]
		fn := firstNonEmpty(f.Function, "?")
		loc := firstNonEmpty(f.Filename, f.AbsPath, f.Module)
		if f.Lineno != nil {
			loc += ":" + strconv.Itoa(*f.Lineno)
		}
		out = append(out, strings.TrimSpace(fn+" ("+loc+")"))
	}
	return out
}

// severityFromLevel maps a Sentry level onto intake.Severity (same vocabulary).
func severityFromLevel(level string) intake.Severity {
	switch strings.ToLower(level) {
	case "fatal":
		return intake.SeverityFatal
	case "error":
		return intake.SeverityError
	case "warning":
		return intake.SeverityWarning
	case "info":
		return intake.SeverityInfo
	case "debug", "sample":
		return intake.SeverityDebug
	}
	return intake.SeverityUnknown
}

// severityFromPriority maps Sentry's issue priority (used when level is absent).
func severityFromPriority(p string) intake.Severity {
	switch strings.ToLower(p) {
	case "high":
		return intake.SeverityError
	case "medium":
		return intake.SeverityWarning
	case "low":
		return intake.SeverityInfo
	}
	return intake.SeverityUnknown
}

func labels(level, platform, env, release string) []string {
	var out []string
	for _, kv := range [][2]string{{"level", level}, {"platform", platform}, {"environment", env}, {"release", release}} {
		if kv[1] != "" {
			out = append(out, kv[0]+":"+kv[1])
		}
	}
	return out
}

// body renders a short plain-text description. It deliberately omits user,
// request and tag data (PII: IPs, emails, cookies); the raw payload stays in
// the inbox row for downstream PII-scrubbed processing.
func body(m metadata, culprit, message string, extra [][2]string) string {
	var b strings.Builder
	if m.Type != "" || m.Value != "" {
		fmt.Fprintf(&b, "%s: %s\n", m.Type, m.Value)
	}
	if message != "" && message != m.Value {
		fmt.Fprintf(&b, "Message: %s\n", message)
	}
	if culprit != "" {
		fmt.Fprintf(&b, "Culprit: %s\n", culprit)
	}
	if m.Filename != "" || m.Function != "" {
		fmt.Fprintf(&b, "Location: %s\n", strings.TrimSpace(m.Filename+" "+m.Function))
	}
	for _, kv := range extra {
		if kv[1] != "" {
			fmt.Fprintf(&b, "%s: %s\n", kv[0], kv[1])
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// tag looks up a tag value. Sentry serializes tags as [[key, value], ...] but
// some payloads use [{"key":..,"value":..}]; both are accepted and anything
// else is ignored rather than failing the whole event.
func tag(raw json.RawMessage, key string) string {
	var pairs [][]string
	if json.Unmarshal(raw, &pairs) == nil {
		for _, p := range pairs {
			if len(p) == 2 && p[0] == key {
				return p[1]
			}
		}
		return ""
	}
	var objs []struct{ Key, Value string }
	if json.Unmarshal(raw, &objs) == nil {
		for _, o := range objs {
			if o.Key == key {
				return o.Value
			}
		}
	}
	return ""
}

var projectURLRe = regexp.MustCompile(`/api/0/projects/[^/]+/([^/]+)/`)

// projectSlugFromAPIURL extracts the project slug from an event API url like
// https://sentry.io/api/0/projects/<org>/<project>/events/<id>/.
func projectSlugFromAPIURL(u string) string {
	if m := projectURLRe.FindStringSubmatch(u); m != nil {
		return m[1]
	}
	return ""
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

func setIf(m map[string]string, k, v string) {
	if v != "" {
		m[k] = v
	}
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
