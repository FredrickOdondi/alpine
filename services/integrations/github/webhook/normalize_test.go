package webhook

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/alpine-dev/alpine/packages/shared/intake"
	m "github.com/alpine-dev/alpine/services/integrations/intakemeta"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func normalize(t *testing.T, typ string, body []byte) (*intake.TicketIntake, error) {
	t.Helper()
	out, err := Normalizer{}.Normalize(context.Background(), typ, body)
	if err != nil {
		if out != nil {
			t.Fatalf("non-nil result with error: %v", out)
		}
		return nil, err
	}
	if len(out) != 1 {
		t.Fatalf("want exactly 1 intake, got %d", len(out))
	}
	return &out[0], nil
}

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

var shopRepo = intake.RepoRef{Provider: "github", InstallationID: 5550001, RepoID: 777, FullName: "acme/shop", DefaultBranch: "main"}

func TestNormalizeFixtures(t *testing.T) {
	cases := []struct {
		file, typ string
		want      intake.TicketIntake
	}{
		{"issues_opened.json", "issues", intake.TicketIntake{
			Source: intake.SourceGitHub, Action: intake.ActionCreate,
			ExternalID: "gh:777#42", ExternalURL: "https://github.com/acme/shop/issues/42", Number: 42, Repo: shopRepo,
			DedupeKey: "gh:777#42", Severity: intake.SeverityError,
			Title: "Checkout button does nothing on Safari", Body: "Steps:\n1. Add item\n2. Click Checkout\nNothing happens.",
			Labels: []string{"bug", "alpine", "severity:high"}, Author: "octocat", OccurredAt: ts("2026-10-14T09:30:00Z"),
			Metadata: map[string]string{m.EventType: "issues", m.SourceAction: "opened", m.Kind: m.KindIssue,
				m.IssueID: "2001001", m.DisplayRef: "acme/shop#42", m.SenderLogin: "octocat"},
		}},
		{"issues_labeled.json", "issues", intake.TicketIntake{
			Source: intake.SourceGitHub, Action: intake.ActionUpdate,
			ExternalID: "gh:777#43", ExternalURL: "https://github.com/acme/shop/issues/43", Number: 43, Repo: shopRepo,
			DedupeKey: "gh:777#43", Severity: intake.SeverityFatal,
			Title: "Crash on empty cart", Labels: []string{"P0"}, Author: "dependabot[bot]", OccurredAt: ts("2026-10-14T10:05:00Z"),
			Metadata: map[string]string{m.EventType: "issues", m.SourceAction: "labeled", m.Kind: m.KindIssue,
				m.IssueID: "2001002", m.DisplayRef: "acme/shop#43", m.SenderLogin: "maintainer", m.AuthorIsBot: "true"},
		}},
		{"issues_closed.json", "issues", intake.TicketIntake{
			Source: intake.SourceGitHub, Action: intake.ActionClose,
			ExternalID: "gh:777#42", ExternalURL: "https://github.com/acme/shop/issues/42", Number: 42, Repo: shopRepo,
			DedupeKey: "gh:777#42",
			Title:     "Checkout button does nothing on Safari", Body: "Steps...", Author: "octocat", OccurredAt: ts("2026-10-15T08:00:00Z"),
			Metadata: map[string]string{m.EventType: "issues", m.SourceAction: "closed", m.Kind: m.KindIssue,
				m.IssueID: "2001001", m.DisplayRef: "acme/shop#42", m.SenderLogin: "octocat"},
		}},
		{"issue_comment_created.json", "issue_comment", intake.TicketIntake{
			Source: intake.SourceGitHub, Action: intake.ActionComment,
			ExternalID: "gh:777#42", ExternalURL: "https://github.com/acme/shop/issues/42#issuecomment-9000001", Number: 42, Repo: shopRepo,
			DedupeKey: "gh:777#42",
			Title:     "Checkout button does nothing on Safari", Body: "Also reproduces on iOS 18.", Labels: []string{"bug", "alpine"},
			Author: "hubot", OccurredAt: ts("2026-10-14T11:00:00Z"),
			Metadata: map[string]string{m.EventType: "issue_comment", m.SourceAction: "created", m.Kind: m.KindComment,
				m.IssueID: "2001001", m.DisplayRef: "acme/shop#42", m.CommentID: "9000001", m.CommentURL: "https://github.com/acme/shop/issues/42#issuecomment-9000001",
				m.SenderLogin: "hubot"},
		}},
		{"issue_comment_pr_edited.json", "issue_comment", intake.TicketIntake{
			Source: intake.SourceGitHub, Action: intake.ActionComment,
			ExternalID: "gh:777#50", ExternalURL: "https://github.com/acme/shop/pull/50#issuecomment-9000050", Number: 50, Repo: shopRepo,
			DedupeKey: "gh:777#50",
			Title:     "alpine: fix checkout on Safari", Body: "Please also handle the empty-cart case.", Labels: []string{"alpine"},
			Author: "reviewer", OccurredAt: ts("2026-10-15T07:30:00Z"),
			Metadata: map[string]string{m.EventType: "issue_comment", m.SourceAction: "edited", m.Kind: m.KindPullRequestComment,
				m.IssueID: "2001050", m.DisplayRef: "acme/shop#50", m.CommentID: "9000050", m.CommentURL: "https://github.com/acme/shop/pull/50#issuecomment-9000050",
				m.PullRequestNo: "50", m.SenderLogin: "reviewer"},
		}},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			raw := fixture(t, c.file)
			got, err := normalize(t, c.typ, raw)
			if err != nil {
				t.Fatal(err)
			}
			if string(got.Raw) != string(raw) {
				t.Error("Raw must be the verbatim payload")
			}
			raw[0] = 'X' // Raw must not alias the caller's buffer
			if got.Raw[0] == 'X' {
				t.Error("Raw aliases the input payload")
			}
			got.Raw = nil
			if !reflect.DeepEqual(*got, c.want) {
				t.Errorf("got  %#v\nwant %#v", *got, c.want)
			}
		})
	}
}

func TestExternalIDIsRenameProof(t *testing.T) {
	a, err := normalize(t, "issues", []byte(`{"action":"opened","issue":{"id":1,"number":5},"repository":{"id":9,"full_name":"old/name"}}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := normalize(t, "issues", []byte(`{"action":"edited","issue":{"id":1,"number":5},"repository":{"id":9,"full_name":"new-owner/renamed"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if a.ExternalID != "gh:9#5" || a.ExternalID != b.ExternalID || ExternalID(9, 5) != "gh:9#5" {
		t.Fatalf("%q vs %q", a.ExternalID, b.ExternalID)
	}
	if a.Metadata[m.DisplayRef] != "old/name#5" || b.Metadata[m.DisplayRef] != "new-owner/renamed#5" {
		t.Fatalf("display refs %q %q", a.Metadata[m.DisplayRef], b.Metadata[m.DisplayRef])
	}
}

func TestCommentLinksToIssue(t *testing.T) {
	is, err := normalize(t, "issues", fixture(t, "issues_opened.json"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := normalize(t, "issue_comment", fixture(t, "issue_comment_created.json"))
	if err != nil {
		t.Fatal(err)
	}
	if is.ExternalID != c.ExternalID || is.DedupeKey != c.DedupeKey || is.Metadata[m.IssueID] != c.Metadata[m.IssueID] {
		t.Fatalf("comment must link to its issue: %q vs %q", c.ExternalID, is.ExternalID)
	}
}

func TestActionMapping(t *testing.T) {
	issueWant := map[string]intake.Action{
		"opened": intake.ActionCreate, "edited": intake.ActionUpdate, "labeled": intake.ActionUpdate,
		"unlabeled": intake.ActionUpdate, "transferred": intake.ActionUpdate, "closed": intake.ActionClose,
		"deleted": intake.ActionClose, "reopened": intake.ActionReopen,
	}
	for a, want := range issueWant {
		body := []byte(`{"action":"` + a + `","issue":{"id":1,"number":2,"title":"t"},"repository":{"id":9,"full_name":"o/r"}}`)
		got, err := normalize(t, "issues", body)
		if err != nil || got.Action != want || got.Metadata[m.SourceAction] != a {
			t.Errorf("issues.%s: %v %+v", a, err, got)
		}
		if got != nil && (got.Author != "" || got.Repo.InstallationID != 0 || got.Labels != nil) {
			t.Errorf("issues.%s: unexpected fields %+v", a, got)
		}
	}
	for _, a := range []string{"created", "edited"} {
		// No comment.user: falls back to sender.
		body := []byte(`{"action":"` + a + `","issue":{"id":1,"number":2},"comment":{"id":3},"repository":{"id":9,"full_name":"o/r"},"sender":{"login":"s","type":"User"}}`)
		got, err := normalize(t, "issue_comment", body)
		if err != nil || got.Action != intake.ActionComment || got.Author != "s" {
			t.Errorf("issue_comment.%s: %v %+v", a, err, got)
		}
	}
}

func TestNormalizeIgnoredAndErrors(t *testing.T) {
	cases := []struct {
		name, typ, body string
		want            error
	}{
		{"ping", "ping", `{"zen":"hi"}`, intake.ErrIgnored},
		{"pull_request", "pull_request", `{"action":"opened"}`, intake.ErrIgnored},
		{"empty type", "", `{}`, intake.ErrIgnored},
		{"assigned", "issues", string(fixture(t, "issues_assigned.json")), intake.ErrIgnored},
		{"milestoned", "issues", `{"action":"milestoned"}`, intake.ErrIgnored},
		{"comment deleted", "issue_comment", `{"action":"deleted","issue":{"id":1,"number":1},"comment":{"id":3},"repository":{"id":9,"full_name":"o/r"}}`, intake.ErrIgnored},
		{"bot comment by type", "issue_comment", `{"action":"created","issue":{"id":1,"number":1},"comment":{"id":3,"user":{"login":"alpine-factory","type":"Bot"}},"repository":{"id":9,"full_name":"o/r"}}`, intake.ErrIgnored},
		{"bot comment by login", "issue_comment", `{"action":"created","issue":{"id":1,"number":1},"comment":{"id":3,"user":{"login":"renovate[bot]","type":"User"}},"repository":{"id":9,"full_name":"o/r"}}`, intake.ErrIgnored},
		{"bot comment via sender", "issue_comment", `{"action":"created","issue":{"id":1,"number":1},"comment":{"id":3},"repository":{"id":9,"full_name":"o/r"},"sender":{"login":"x[bot]"}}`, intake.ErrIgnored},
		{"not json", "issues", `{`, ErrMalformedPayload},
		{"missing action", "issues", `{"issue":{"id":1,"number":1}}`, ErrMalformedPayload},
		{"missing issue", "issues", `{"action":"opened","repository":{"id":9,"full_name":"o/r"}}`, ErrMalformedPayload},
		{"issue without number", "issues", `{"action":"opened","issue":{"id":1},"repository":{"id":9,"full_name":"o/r"}}`, ErrMalformedPayload},
		{"missing repository", "issues", `{"action":"opened","issue":{"id":1,"number":1}}`, ErrMalformedPayload},
		{"repository without id", "issues", `{"action":"opened","issue":{"id":1,"number":1},"repository":{"full_name":"o/r"}}`, ErrMalformedPayload},
		{"issues event for PR", "issues", `{"action":"opened","issue":{"id":1,"number":1,"pull_request":{}},"repository":{"id":9,"full_name":"o/r"}}`, ErrMalformedPayload},
		{"comment missing comment", "issue_comment", `{"action":"created","issue":{"id":1,"number":1},"repository":{"id":9,"full_name":"o/r"}}`, ErrMalformedPayload},
		{"comment missing id", "issue_comment", `{"action":"created","issue":{"id":1,"number":1},"comment":{"body":"x"},"repository":{"id":9,"full_name":"o/r"}}`, ErrMalformedPayload},
		{"wrong type for id", "issues", `{"action":"opened","issue":{"id":"abc","number":1},"repository":{"id":9,"full_name":"o/r"}}`, ErrMalformedPayload},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := normalize(t, c.typ, []byte(c.body))
			if !errors.Is(err, c.want) {
				t.Fatalf("got %v, want %v", err, c.want)
			}
			if c.want == ErrMalformedPayload && errors.Is(err, intake.ErrIgnored) {
				t.Fatal("malformed payloads must not be silently ignored")
			}
		})
	}
}

func TestRegistryDispatch(t *testing.T) {
	r := intake.Registry{Normalizer{}.Source(): Normalizer{}}
	out, err := r.Normalize(context.Background(), intake.SourceGitHub, "issues", fixture(t, "issues_opened.json"))
	if err != nil || len(out) != 1 || out[0].ExternalID != "gh:777#42" {
		t.Fatalf("%v %+v", err, out)
	}
}

func TestSeverityFromLabels(t *testing.T) {
	cases := []struct {
		labels []string
		want   intake.Severity
	}{
		{nil, intake.SeverityUnknown},
		{[]string{"bug", "frontend"}, intake.SeverityUnknown},
		{[]string{"Severity: Critical"}, intake.SeverityFatal},
		{[]string{"critical"}, intake.SeverityFatal},
		{[]string{"sev/low"}, intake.SeverityInfo},
		{[]string{"sev:medium", "severity/high"}, intake.SeverityError},
		{[]string{"priority:p2"}, intake.SeverityWarning},
		{[]string{"P3", "p1"}, intake.SeverityError},
		{[]string{"priority/P0", "sev:low"}, intake.SeverityFatal},
		{[]string{"severity:fatal"}, intake.SeverityFatal},
		{[]string{"severity:error"}, intake.SeverityError},
		{[]string{"sev:warning"}, intake.SeverityWarning},
		{[]string{"sev:info"}, intake.SeverityInfo},
		{[]string{"sev:debug", "nope"}, intake.SeverityDebug},
		{[]string{"error", "info", "debug"}, intake.SeverityUnknown}, // bare vocabulary is ambiguous
		{[]string{"severity:unknown"}, intake.SeverityUnknown},
	}
	for _, c := range cases {
		if got := severityFromLabels(c.labels); got != c.want {
			t.Errorf("%v: got %q want %q", c.labels, got, c.want)
		}
	}
}
