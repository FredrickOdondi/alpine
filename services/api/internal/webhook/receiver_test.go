package webhook

import (
	ghwebhook "github.com/alpine-dev/alpine/services/integrations/github/webhook"
	"github.com/alpine-dev/alpine/services/integrations/sentry"

	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

type fakeInbox struct {
	org  uuid.UUID
	rows map[string]bool
}

func (f *fakeInbox) ResolveInstallation(_ context.Context, _ string, id int64) (uuid.UUID, bool, error) {
	return f.org, id == 42, nil
}
func (f *fakeInbox) InsertInboxEvent(_ context.Context, _ uuid.UUID, src, d, _, _ string, _ []byte) (bool, error) {
	k := src + "|" + d
	if f.rows[k] {
		return false, nil
	}
	f.rows[k] = true
	return true, nil
}

type sentryOrgs map[string]uuid.UUID

func (s sentryOrgs) OrgForInstallation(_ context.Context, u string) (uuid.UUID, bool, error) {
	id, ok := s[u]
	return id, ok, nil
}

var secret = []byte("whsec")

func newRC() (*Receiver, *fakeInbox) {
	in := &fakeInbox{org: uuid.New(), rows: map[string]bool{}}
	return &Receiver{Inbox: in, GitHubVerifier: ghwebhook.Verifier{Secret: secret}, SentryVerifier: sentry.Verifier{ClientSecret: []byte("sntry")},
		SentryOrg: sentryOrgs{"inst-1": in.org}}, in
}

func ghReq(body []byte, sig, delivery string) *http.Request {
	r := httptest.NewRequest("POST", "/v1/webhooks/github", bytes.NewReader(body))
	r.Header.Set("X-GitHub-Event", "issues")
	r.Header.Set("X-GitHub-Delivery", delivery)
	r.Header.Set("X-Hub-Signature-256", sig)
	return r
}

func TestGitHubReceiver(t *testing.T) {
	rc, _ := newRC()
	body := []byte(`{"action":"opened","installation":{"id":42},"issue":{"number":1}}`)
	good := ghwebhook.Sign(secret, body)

	res := rc.GitHub(httptest.NewRecorder(), ghReq(body, good, "d1"))
	if res.HTTPStatus != 202 || res.Status != StatusAccepted {
		t.Fatalf("%+v", res)
	}
	if res := rc.GitHub(httptest.NewRecorder(), ghReq(body, good, "d1")); res.Status != StatusDuplicate {
		t.Fatalf("replay must be duplicate: %+v", res)
	}
	if res := rc.GitHub(httptest.NewRecorder(), ghReq(body, "sha256="+strings.Repeat("0", 64), "d2")); res.HTTPStatus != 401 {
		t.Fatalf("bad sig: %+v", res)
	}
	if res := rc.GitHub(httptest.NewRecorder(), ghReq(body, "", "d3")); res.HTTPStatus != 401 {
		t.Fatalf("missing sig: %+v", res)
	}
	other := []byte(`{"action":"opened","installation":{"id":7}}`)
	if res := rc.GitHub(httptest.NewRecorder(), ghReq(other, ghwebhook.Sign(secret, other), "d4")); res.Status != StatusIgnored {
		t.Fatalf("unknown installation: %+v", res)
	}
}

func TestGitHubBodyLimit(t *testing.T) {
	rc, _ := newRC()
	big := bytes.Repeat([]byte("a"), MaxGitHubBody+1)
	r := ghReq(big, ghwebhook.Sign(secret, big), "big")
	if res := rc.GitHub(httptest.NewRecorder(), r); res.HTTPStatus != 413 {
		t.Fatalf("%+v", res)
	}
	r = ghReq(big, "x", "big2")
	r.ContentLength = -1 // chunked: enforced while reading
	if res := rc.GitHub(httptest.NewRecorder(), r); res.HTTPStatus != 413 {
		t.Fatalf("chunked: %+v", res)
	}
}

func sentryReq(body []byte, requestID string, secret []byte) *http.Request {
	r := httptest.NewRequest("POST", "/v1/webhooks/sentry", bytes.NewReader(body))
	r.Header.Set("Sentry-Hook-Resource", "issue")
	r.Header.Set("Request-ID", requestID)
	r.Header.Set("Sentry-Hook-Signature", sentry.Sign(secret, body))
	return r
}

func TestSentryReceiverDedupesByRequestID(t *testing.T) {
	rc, _ := newRC()
	body := []byte(`{"action":"created","installation":{"uuid":"inst-1"},"data":{"issue":{"id":"9"}}}`)
	if res := rc.Sentry(httptest.NewRecorder(), sentryReq(body, "req-1", []byte("sntry"))); res.Status != StatusAccepted {
		t.Fatalf("%+v", res)
	}
	if res := rc.Sentry(httptest.NewRecorder(), sentryReq(body, "req-1", []byte("sntry"))); res.Status != StatusDuplicate {
		t.Fatalf("%+v", res)
	}
	if res := rc.Sentry(httptest.NewRecorder(), sentryReq(body, "req-2", []byte("wrong"))); res.HTTPStatus != 401 {
		t.Fatalf("%+v", res)
	}
	big := bytes.Repeat([]byte("a"), MaxSentryBody+1)
	if res := rc.Sentry(httptest.NewRecorder(), sentryReq(big, "req-3", []byte("sntry"))); res.HTTPStatus != 413 {
		t.Fatalf("%+v", res)
	}
	r := sentryReq(body, "", []byte("sntry"))
	if res := rc.Sentry(httptest.NewRecorder(), r); res.HTTPStatus != 400 {
		t.Fatalf("missing Request-ID: %+v", res)
	}
}
