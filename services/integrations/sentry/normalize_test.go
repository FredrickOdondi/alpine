package sentry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
			t.Fatalf("non-nil result with error")
		}
		return nil, err
	}
	if len(out) != 1 {
		t.Fatalf("want 1 intake, got %d", len(out))
	}
	return &out[0], nil
}

func fpKey(project string, fp ...string) string {
	h := sha256.New()
	h.Write([]byte(project))
	for _, f := range fp {
		h.Write([]byte{0})
		h.Write([]byte(f))
	}
	return "sentry:fp:" + project + ":" + hex.EncodeToString(h.Sum(nil))
}

const inst = "24b397fc-a86e-43ef-9297-949e21b82480"
const inst2 = "a8e5d37a-696c-4c54-adb5-b3f28d64c7de"

func TestNormalizeFixtures(t *testing.T) {
	cases := []struct {
		file, typ string
		want      intake.TicketIntake
	}{
		{"issue_created.json", "issue", intake.TicketIntake{
			Source: intake.SourceSentry, Severity: intake.SeverityFatal, SentryProject: "shop-web", Action: intake.ActionCreate,
			ExternalID: "1234567890", ExternalURL: "https://acme.sentry.io/issues/1234567890/",
			Title: "TypeError: Cannot read properties of undefined (reading 'total')",
			Body: "TypeError: Cannot read properties of undefined (reading 'total')\n" +
				"Culprit: app/checkout/Cart.tsx in computeTotal\n" +
				"Location: app/checkout/Cart.tsx computeTotal\n" +
				"Short ID: SHOP-WEB-Y\nStatus: unresolved\n" +
				"First seen: 2026-10-14T20:56:00.679000+00:00\nLast seen: 2026-10-14T20:58:00.738000+00:00",
			Labels: []string{"level:fatal", "platform:javascript", "category:error"}, Author: "Sentry",
			DedupeKey: "sentry:issue:1234567890", Culprit: "app/checkout/Cart.tsx in computeTotal",
			OccurredAt: time.Date(2026, 10, 14, 20, 58, 0, 738000000, time.UTC),
			Metadata: map[string]string{m.EventType: "issue", m.SourceAction: "created", m.Kind: m.KindErrorGroup,
				m.SentryInstallation: inst, m.SentryIssueID: "1234567890", m.SentryShortID: "SHOP-WEB-Y", m.SentryStatus: "unresolved",
				m.SentryProjectID: "4509877862268928"},
		}},
		{"issue_resolved.json", "issue", intake.TicketIntake{
			Source: intake.SourceSentry, Severity: intake.SeverityError, SentryProject: "shop-web", Action: intake.ActionClose,
			ExternalID: "1234567890", ExternalURL: "https://acme.sentry.io/issues/1234567890/",
			Title: "TypeError: Cannot read properties of undefined (reading 'total')",
			Body: "TypeError: Cannot read properties of undefined (reading 'total')\n" +
				"Culprit: app/checkout/Cart.tsx in computeTotal\n" +
				"Short ID: SHOP-WEB-Y\nStatus: resolved\n" +
				"First seen: 2026-10-14T20:56:00.679000+00:00\nLast seen: 2026-10-15T06:00:00+00:00",
			Labels: []string{"level:error", "platform:javascript"}, Author: "Jane Dev",
			DedupeKey: "sentry:issue:1234567890", Culprit: "app/checkout/Cart.tsx in computeTotal",
			OccurredAt: time.Date(2026, 10, 15, 6, 0, 0, 0, time.UTC),
			Metadata: map[string]string{m.EventType: "issue", m.SourceAction: "resolved", m.Kind: m.KindErrorGroup,
				m.SentryInstallation: inst, m.SentryIssueID: "1234567890", m.SentryShortID: "SHOP-WEB-Y", m.SentryStatus: "resolved",
				m.SentryProjectID: "4509877862268928"},
		}},
		{"error_created_default_fingerprint.json", "error", intake.TicketIntake{
			Source: intake.SourceSentry, Severity: intake.SeverityError, SentryProject: "front-end", Action: intake.ActionCreate,
			ExternalID:  "1170820242",
			ExternalURL: "https://sentry.io/organizations/test-org/issues/1170820242/events/bb78c1407cea4519aa397afc059c793d/",
			Title:       "ReferenceError: blooopy is not defined",
			Body: "ReferenceError: blooopy is not defined\nCulprit: ?(runner)\nLocation: /runner\n" +
				"Release: front-end@2.0.1\nEnvironment: production",
			Labels: []string{"level:error", "platform:javascript", "environment:production", "release:front-end@2.0.1"},
			Author: "Sentry", DedupeKey: "sentry:issue:1170820242", Culprit: "?(runner)", Release: "front-end@2.0.1",
			StackFrames: []string{"computeTotal (/app/cart.js:42)", "run (/runner:3)"},
			OccurredAt:  time.Date(2026, 10, 14, 20, 58, 37, 391000000, time.UTC),
			Metadata: map[string]string{m.EventType: "error", m.SourceAction: "created", m.Kind: m.KindErrorEvent,
				m.SentryInstallation: inst2, m.SentryEventID: "bb78c1407cea4519aa397afc059c793d", m.SentryIssueID: "1170820242",
				m.SentryProjectID: "1"},
		}},
		{"error_created_custom_fingerprint.json", "error", intake.TicketIntake{
			Source: intake.SourceSentry, Severity: intake.SeverityWarning, SentryProject: "payments-api", Action: intake.ActionCreate,
			ExternalID: "2200000001", ExternalURL: "https://acme.sentry.io/issues/2200000001/events/0c9e1f2a3b4c5d6e7f8091a2b3c4d5e6/",
			Title: "Stripe request timed out after 30s", Body: "Message: Stripe request timed out after 30s\nCulprit: payments.charge",
			Labels: []string{"level:warning", "platform:python"}, Author: "Sentry",
			DedupeKey: fpKey("42", "payments", "stripe-timeout"), Culprit: "payments.charge",
			OccurredAt: time.Unix(1760518800, 500*int64(time.Millisecond)).UTC(),
			Metadata: map[string]string{m.EventType: "error", m.SourceAction: "created", m.Kind: m.KindErrorEvent,
				m.SentryInstallation: inst2, m.SentryEventID: "0c9e1f2a3b4c5d6e7f8091a2b3c4d5e6", m.SentryIssueID: "2200000001",
				m.SentryProjectID: "42", m.AltDedupeKey: "sentry:issue:2200000001"},
		}},
		{"event_alert_triggered.json", "event_alert", intake.TicketIntake{
			Source: intake.SourceSentry, Severity: intake.SeverityInfo, SentryProject: "7", Action: intake.ActionCreate,
			ExternalID: "3300000003", ExternalURL: "https://acme.sentry.io/issues/3300000003/events/a1b2c3d4e5f60718293a4b5c6d7e8f90/",
			Title: "Slow checkout", Body: "Message: Slow checkout",
			Labels: []string{"level:info", "platform:node", "rule:Checkout errors"}, Author: "Sentry",
			DedupeKey:  "sentry:issue:3300000003", // fingerprint has {{ default }} -> not literal
			OccurredAt: time.Date(2026, 10, 15, 5, 0, 0, 0, time.UTC),
			Metadata: map[string]string{m.EventType: "event_alert", m.SourceAction: "triggered", m.Kind: m.KindErrorEvent,
				m.SentryInstallation: inst2, m.SentryEventID: "a1b2c3d4e5f60718293a4b5c6d7e8f90", m.SentryIssueID: "3300000003",
				m.SentryProjectID: "7", m.SentryRule: "Checkout errors"},
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
			got.Raw = nil
			if got.Fingerprint != "" || got.Level != "" {
				t.Error("deprecated Fingerprint/Level must not be populated")
			}
			if !reflect.DeepEqual(*got, c.want) {
				t.Errorf("got  %#v\nwant %#v", *got, c.want)
			}
		})
	}
}

func TestIssueAndEventsLinkToSameTicket(t *testing.T) {
	is, err := normalize(t, "issue", []byte(`{"action":"created","data":{"issue":{"id":"1170820242"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	e, err := normalize(t, "error", fixture(t, "error_created_default_fingerprint.json"))
	if err != nil {
		t.Fatal(err)
	}
	if is.ExternalID != e.ExternalID || is.DedupeKey != e.DedupeKey {
		t.Fatalf("%q/%q vs %q/%q", is.ExternalID, is.DedupeKey, e.ExternalID, e.DedupeKey)
	}
	c, err := normalize(t, "error", fixture(t, "error_created_custom_fingerprint.json"))
	if err != nil {
		t.Fatal(err)
	}
	is2, _ := normalize(t, "issue", []byte(`{"action":"created","data":{"issue":{"id":"2200000001"}}}`))
	if c.Metadata[m.AltDedupeKey] != is2.DedupeKey || c.ExternalID != is2.ExternalID {
		t.Fatalf("custom fingerprint event must be reachable from its issue: %+v", c)
	}
}

func TestDedupeKeys(t *testing.T) {
	cases := []struct {
		name             string
		project          string
		fp               []string
		group            string
		wantKey, wantAlt string
	}{
		{"custom fp with group", "42", []string{"a", "b"}, "9", fpKey("42", "a", "b"), "sentry:issue:9"},
		{"custom fp no group", "42", []string{"a"}, "", fpKey("42", "a"), ""},
		{"default fp", "42", []string{"{{ default }}"}, "9", "sentry:issue:9", ""},
		{"templated fp", "42", []string{"{{ transaction }}", "x"}, "9", "sentry:issue:9", ""},
		{"no fp", "42", nil, "9", "sentry:issue:9", ""},
		{"fp without project", "", []string{"a"}, "9", "sentry:issue:9", ""},
		{"nothing", "", nil, "", "sentry:event:ev1", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			k, alt := dedupeKeys(c.project, c.fp, c.group, "ev1")
			if k != c.wantKey || alt != c.wantAlt {
				t.Fatalf("got %q %q want %q %q", k, alt, c.wantKey, c.wantAlt)
			}
		})
	}
	if a, _ := dedupeKeys("1", []string{"a", "b"}, "", "e"); a == fpKey("1", "ab") {
		t.Fatal("fingerprint elements must be delimited")
	}
	if a, _ := dedupeKeys("1", []string{"x"}, "", "e"); a == fpKey("2", "x") {
		t.Fatal("fingerprint key must be project scoped")
	}
}

func TestActionMappingAndVariants(t *testing.T) {
	want := map[string]intake.Action{
		"created": intake.ActionCreate, "assigned": intake.ActionUpdate, "resolved": intake.ActionClose,
		"archived": intake.ActionClose, "ignored": intake.ActionClose, "unresolved": intake.ActionReopen,
	}
	for a, w := range want {
		got, err := normalize(t, "issue", []byte(`{"action":"`+a+`","data":{"issue":{"id":1,"priority":"Medium","project":{"id":5}}}}`))
		if err != nil || got.Action != w || got.ExternalID != "1" || got.SentryProject != "5" || got.Severity != intake.SeverityWarning {
			t.Errorf("issue.%s: %v %+v", a, err, got)
		}
	}
	// group_id / groupID fallbacks, numeric timestamp, object-style tags, actor id fallback,
	// frames without in_app markers, chained exceptions (last value wins), frame cap.
	frames := ""
	for i := 0; i < 15; i++ {
		frames += `{"module":"m","function":"f` + string(rune('a'+i)) + `"},`
	}
	got, err := normalize(t, "error", []byte(`{"action":"created","actor":{"id":"u1"},"data":{"error":{
		"event_id":"e1","groupID":77,"project":3,"timestamp":1760000000,"level":"debug",
		"tags":[{"key":"environment","value":"staging"}],"metadata":{"title":"T"},
		"exception":{"values":[{"stacktrace":{"frames":[{"function":"cause"}]}},{"stacktrace":{"frames":[`+frames+`{"abs_path":"https://x/app.js","lineno":7}]}}]}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.ExternalID != "77" || got.DedupeKey != "sentry:issue:77" || got.Title != "T" || got.Author != "u1" ||
		got.Severity != intake.SeverityDebug || !got.OccurredAt.Equal(time.Unix(1760000000, 0)) ||
		!reflect.DeepEqual(got.Labels, []string{"level:debug", "environment:staging"}) {
		t.Errorf("unexpected %+v", got)
	}
	if len(got.StackFrames) != MaxStackFrames || got.StackFrames[0] != "? (https://x/app.js:7)" || got.StackFrames[1] != "fo (m)" {
		t.Errorf("frames %v", got.StackFrames)
	}
	// Unknown level, malformed tags ignored, no actor, unparsable datetime, no group -> event id.
	got, err = normalize(t, "error", []byte(`{"action":"created","data":{"error":{"event_id":"e2","level":"weird","tags":"bogus","datetime":"yesterday","exception":{"values":[{}]}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.Severity != intake.SeverityUnknown || got.Author != "" || !got.OccurredAt.IsZero() || got.DedupeKey != "sentry:event:e2" ||
		got.ExternalID != "e2" || got.StackFrames != nil || got.Metadata[m.SentryIssueID] != "" {
		t.Errorf("unexpected %+v", got)
	}
	got, err = normalize(t, "issue", []byte(`{"action":"created","data":{"issue":{"id":"9","project":null,"priority":"urgent","issueCategory":"feedback"}}}`))
	if err != nil || got.Severity != intake.SeverityUnknown || got.SentryProject != "" ||
		!reflect.DeepEqual(got.Labels, []string{"category:feedback"}) {
		t.Errorf("unexpected %v %+v", err, got)
	}
}

func TestSeverityMapping(t *testing.T) {
	levels := map[string]intake.Severity{
		"fatal": intake.SeverityFatal, "ERROR": intake.SeverityError, "warning": intake.SeverityWarning,
		"info": intake.SeverityInfo, "debug": intake.SeverityDebug, "sample": intake.SeverityDebug, "": intake.SeverityUnknown,
	}
	for l, want := range levels {
		if got := severityFromLevel(l); got != want {
			t.Errorf("level %q: %q want %q", l, got, want)
		}
	}
	prios := map[string]intake.Severity{"high": intake.SeverityError, "medium": intake.SeverityWarning, "Low": intake.SeverityInfo, "x": intake.SeverityUnknown}
	for p, want := range prios {
		if got := severityFromPriority(p); got != want {
			t.Errorf("priority %q: %q want %q", p, got, want)
		}
	}
}

func TestRegistryDispatch(t *testing.T) {
	r := intake.Registry{Normalizer{}.Source(): Normalizer{}}
	out, err := r.Normalize(context.Background(), intake.SourceSentry, "issue", fixture(t, "issue_created.json"))
	if err != nil || len(out) != 1 || out[0].ExternalID != "1234567890" {
		t.Fatalf("%v %+v", err, out)
	}
}

func TestNormalizeIgnoredAndErrors(t *testing.T) {
	cases := []struct {
		name, typ, body string
		want            error
	}{
		{"installation", "installation", `{"action":"created"}`, intake.ErrIgnored},
		{"metric_alert", "metric_alert", `{"action":"critical"}`, intake.ErrIgnored},
		{"comment", "comment", `{"action":"created"}`, intake.ErrIgnored},
		{"empty resource", "", `{}`, intake.ErrIgnored},
		{"issue unknown action", "issue", `{"action":"deleted","data":{"issue":{"id":"1"}}}`, intake.ErrIgnored},
		{"error unknown action", "error", `{"action":"updated"}`, intake.ErrIgnored},
		{"event_alert unknown action", "event_alert", `{"action":"created"}`, intake.ErrIgnored},
		{"not json", "issue", `nope`, ErrMalformedPayload},
		{"missing action", "issue", `{"data":{"issue":{"id":"1"}}}`, ErrMalformedPayload},
		{"missing issue", "issue", `{"action":"created","data":{}}`, ErrMalformedPayload},
		{"issue without id", "issue", `{"action":"created","data":{"issue":{"title":"x"}}}`, ErrMalformedPayload},
		{"issue id null", "issue", `{"action":"created","data":{"issue":{"id":null}}}`, ErrMalformedPayload},
		{"issue id bool", "issue", `{"action":"created","data":{"issue":{"id":true}}}`, ErrMalformedPayload},
		{"issue id bad string", "issue", `{"action":"created","data":{"issue":{"id":"\x"}}}`, ErrMalformedPayload},
		{"missing error", "error", `{"action":"created","data":{}}`, ErrMalformedPayload},
		{"error without event_id", "error", `{"action":"created","data":{"error":{"issue_id":"1"}}}`, ErrMalformedPayload},
		{"alert without event", "event_alert", `{"action":"triggered","data":{"triggered_rule":"r"}}`, ErrMalformedPayload},
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
