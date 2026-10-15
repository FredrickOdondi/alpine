package github

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	refT1     = "GET /repos/acme/shop/git/ref/heads/alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout"
	postRefs  = "POST /repos/acme/shop/git/refs"
	listPRsT1 = "GET /repos/acme/shop/pulls?head=acme%3Aalpine%2F01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout&per_page=100&state=open"
	postPulls = "POST /repos/acme/shop/pulls"
	comments  = "GET /repos/acme/shop/issues/42/comments?per_page=100"
	postCmt   = "POST /repos/acme/shop/issues/42/comments"
	runsC2    = "GET /repos/acme/shop/commits/commit2/check-runs?check_name=Alpine+%2F+lint&filter=all&per_page=100"
	postRuns  = "POST /repos/acme/shop/check-runs"
)

var notFound = route{status: 404, body: `{"message":"Not Found"}`}

const (
	testAppID    = 4242
	testBotLogin = "alpine-dev[bot]"
)

func idemClient(t *testing.T, f *fakeGitHub, sr *sleepRecorder) *Client {
	t.Helper()
	c := retryClient(t, f, RetryPolicy{}, sr, 0.5)
	c.appID, c.botLogin = testAppID, testBotLogin
	return c
}

// ---- branches

func TestCreateBranchAlreadyExists(t *testing.T) {
	f := newFake(t, map[string]route{refT1: {status: 200, body: `{"object":{"sha":"moved"}}`}})
	c := idemClient(t, f, &sleepRecorder{})
	br, err := c.CreateBranch(context.Background(), "alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout", "base")
	if err != nil || br.Created || br.SHA != "moved" {
		t.Fatalf("%v %+v", err, br)
	}
	br, err = c.CreateBranchFrom(context.Background(), "alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout", "main")
	if err != nil || br.Created || br.SHA != "moved" {
		t.Fatalf("%v %+v", err, br)
	}
	assertSeq(t, f.requests(), refT1, refT1) // never POSTs, never reads base
}

func TestCreateBranchAmbiguousTimeoutFoundOnRecheck(t *testing.T) {
	f := newFake(t, map[string]route{refT1: {status: 200, body: `{"object":{"sha":"base"}}`}})
	f.script(refT1, notFound)               // pre-check: absent
	f.script(postRefs, route{hangup: true}) // create: connection dropped (applied server-side)
	sr := &sleepRecorder{}
	br, err := idemClient(t, f, sr).CreateBranch(context.Background(), "alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout", "base")
	if err != nil || !br.Created || br.SHA != "base" {
		t.Fatalf("%v %+v", err, br)
	}
	assertSeq(t, f.requests(), refT1, postRefs, refT1)
	if len(sr.got()) != 0 {
		t.Fatal("no backoff needed when the re-check finds it")
	}
}

func TestCreateBranch5xxNotFoundThenRetried(t *testing.T) {
	f := newFake(t, map[string]route{refT1: notFound, postRefs: {status: 201, body: `{}`}})
	f.script(postRefs, route{status: 502, body: `{}`})
	sr := &sleepRecorder{}
	br, err := idemClient(t, f, sr).CreateBranch(context.Background(), "alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout", "base")
	if err != nil || !br.Created {
		t.Fatalf("%v %+v", err, br)
	}
	assertSeq(t, f.requests(), refT1, postRefs, refT1, postRefs)
	assertWaits(t, sr.got(), 375*time.Millisecond)
}

func TestCreateBranchConflicts(t *testing.T) {
	// 422 "Reference already exists" from a concurrent creator at another sha.
	f := newFake(t, map[string]route{refT1: {status: 200, body: `{"object":{"sha":"other"}}`},
		postRefs: {status: 422, body: `{"message":"Reference already exists"}`}})
	f.script(refT1, notFound)
	br, err := idemClient(t, f, &sleepRecorder{}).CreateBranch(context.Background(), "alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout", "base")
	if err != nil || br.Created || br.SHA != "other" {
		t.Fatalf("%v %+v", err, br)
	}
	// 422 for another reason (e.g. bad sha) and still absent -> error, no retry.
	f2 := newFake(t, map[string]route{refT1: notFound, postRefs: {status: 422, body: `{"message":"Object does not exist"}`}})
	if _, err := idemClient(t, f2, &sleepRecorder{}).CreateBranch(context.Background(), "alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout", "bad"); StatusCode(err) != 422 {
		t.Fatalf("got %v", err)
	}
	assertSeq(t, f2.requests(), refT1, postRefs, refT1)
	// Persistent 5xx gives up after MaxAttempts creates.
	f3 := newFake(t, map[string]route{refT1: notFound, postRefs: {status: 500, body: `{}`}})
	if _, err := idemClient(t, f3, &sleepRecorder{}).CreateBranch(context.Background(), "alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout", "b"); StatusCode(err) != 500 {
		t.Fatalf("got %v", err)
	}
	if n := len(f3.requests()); n != 9 { // pre-check, then 4 x (create + re-check)
		t.Fatalf("requests %d", n)
	}
	// Re-check failure is reported together with the create error.
	f4 := newFake(t, map[string]route{postRefs: {status: 503, body: `{}`}, refT1: {status: 403, body: `{"message":"denied"}`}})
	f4.script(refT1, notFound)
	_, err = idemClient(t, f4, &sleepRecorder{}).CreateBranch(context.Background(), "alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout", "b")
	if StatusCode(err) != 503 || !strings.Contains(err.Error(), "re-check") {
		t.Fatalf("got %v", err)
	}
	// Pre-check errors other than 404 abort.
	f5 := newFake(t, map[string]route{refT1: {status: 403, body: `{}`}})
	if _, err := idemClient(t, f5, &sleepRecorder{}).CreateBranchFrom(context.Background(), "alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout", "main"); StatusCode(err) != 403 {
		t.Fatalf("got %v", err)
	}
	if _, err := idemClient(t, f5, &sleepRecorder{}).CreateBranch(context.Background(), "alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout", "x"); StatusCode(err) != 403 {
		t.Fatalf("got %v", err)
	}
	// Base lookup failure.
	f6 := newFake(t, map[string]route{refT1: notFound})
	if _, err := idemClient(t, f6, &sleepRecorder{}).CreateBranchFrom(context.Background(), "alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout", "main"); StatusCode(err) != 404 {
		t.Fatalf("got %v", err)
	}
	// Sleep cancelled between attempts.
	f7 := newFake(t, map[string]route{refT1: notFound, postRefs: {status: 500, body: `{}`}})
	_, err = idemClient(t, f7, &sleepRecorder{err: context.Canceled}).CreateBranch(context.Background(), "alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout", "b")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

// ---- pull requests

const existingPR = `[{"number":50,"title":"old title","body":"old body","head":{"ref":"alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout","sha":"c1"},"base":{"ref":"develop"}}]`

func TestCreatePullRequestReusesOpenPR(t *testing.T) {
	f := newFake(t, map[string]route{
		listPRsT1:                                {status: 200, body: existingPR},
		"PATCH /repos/acme/shop/pulls/50":        {status: 200, body: `{"number":50,"title":"new","body":"b","base":{"ref":"main"}}`},
		"POST /repos/acme/shop/issues/50/labels": {status: 200, body: `[{"name":"alpine"}]`},
	})
	pr, err := idemClient(t, f, &sleepRecorder{}).CreatePullRequest(context.Background(),
		NewPullRequest{Title: "new", Head: "alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout", Base: "main", Body: "b", Labels: []string{"alpine"}})
	if err != nil || !pr.Reused || pr.Number != 50 || len(pr.Labels) != 1 {
		t.Fatalf("%v %+v", err, pr)
	}
	reqs := f.requests()
	assertSeq(t, reqs, "GET /repos/acme/shop/pulls", "PATCH /repos/acme/shop/pulls/50", "POST /repos/acme/shop/issues/50/labels")
	if b := reqs[1].JSON(t); b["title"] != "new" || b["body"] != "b" || b["base"] != "main" {
		t.Errorf("patch %v", b)
	}

	// Already up to date: no PATCH.
	f2 := newFake(t, map[string]route{listPRsT1: {status: 200, body: `[{"number":50,"title":"t","body":"b","head":{"ref":"alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout"},"base":{"ref":"main"}}]`}})
	pr, err = idemClient(t, f2, &sleepRecorder{}).CreatePullRequest(context.Background(), NewPullRequest{Title: "t", Head: "alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout", Base: "main", Body: "b"})
	if err != nil || !pr.Reused {
		t.Fatalf("%v %+v", err, pr)
	}
	assertSeq(t, f2.requests(), "GET /repos/acme/shop/pulls")

	// PATCH failure surfaces.
	f3 := newFake(t, map[string]route{listPRsT1: {status: 200, body: existingPR}})
	if _, err := idemClient(t, f3, &sleepRecorder{}).CreatePullRequest(context.Background(), NewPullRequest{Title: "x", Head: "alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout", Base: "main"}); StatusCode(err) != 404 {
		t.Fatalf("got %v", err)
	}
}

func TestCreatePullRequestAmbiguousFoundOnRecheck(t *testing.T) {
	f := newFake(t, map[string]route{
		listPRsT1: {status: 200, body: `[{"number":51,"title":"t","body":"b","head":{"ref":"alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout"},"base":{"ref":"main"}}]`},
	})
	f.script(listPRsT1, route{status: 200, body: `[]`})
	f.script(postPulls, route{status: 504, body: `{"message":"Gateway Timeout"}`})
	pr, err := idemClient(t, f, &sleepRecorder{}).CreatePullRequest(context.Background(), NewPullRequest{Title: "t", Head: "alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout", Base: "main", Body: "b"})
	if err != nil || pr.Number != 51 {
		t.Fatalf("%v %+v", err, pr)
	}
	assertSeq(t, f.requests(), "GET /repos/acme/shop/pulls", "POST /repos/acme/shop/pulls", "GET /repos/acme/shop/pulls")
}

func TestCreatePullRequestAlreadyExists422(t *testing.T) {
	f := newFake(t, map[string]route{
		listPRsT1:                         {status: 200, body: existingPR},
		postPulls:                         {status: 422, body: `{"message":"Validation Failed","errors":[{"message":"A pull request already exists for acme:alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout."}]}`},
		"PATCH /repos/acme/shop/pulls/50": {status: 200, body: `{"number":50}`},
	})
	f.script(listPRsT1, route{status: 200, body: `[]`})
	pr, err := idemClient(t, f, &sleepRecorder{}).CreatePullRequest(context.Background(), NewPullRequest{Title: "t", Head: "alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout", Base: "main"})
	if err != nil || pr.Number != 50 || !pr.Reused {
		t.Fatalf("%v %+v", err, pr)
	}
	assertSeq(t, f.requests(), "GET /repos/acme/shop/pulls", "POST /repos/acme/shop/pulls", "GET /repos/acme/shop/pulls", "PATCH /repos/acme/shop/pulls/50")

	// Re-found PR whose reconcile fails.
	f2 := newFake(t, map[string]route{listPRsT1: {status: 200, body: existingPR}, postPulls: {status: 422, body: `{}`}})
	f2.script(listPRsT1, route{status: 200, body: `[]`})
	if _, err := idemClient(t, f2, &sleepRecorder{}).CreatePullRequest(context.Background(), NewPullRequest{Title: "t", Head: "alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout", Base: "main"}); StatusCode(err) != 404 {
		t.Fatalf("got %v", err)
	}
}

func TestCreatePullRequestGuards(t *testing.T) {
	f := newFake(t, map[string]route{})
	c := idemClient(t, f, &sleepRecorder{})
	for _, head := range []string{"main", "feature/x"} {
		if _, err := c.CreatePullRequest(context.Background(), NewPullRequest{Title: "t", Head: head, Base: "main"}); !errors.Is(err, ErrBranchNotWritable) {
			t.Errorf("head %q: %v", head, err)
		}
	}
	for _, in := range []NewPullRequest{
		{Title: "t", Head: "acme:alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout", Base: "main"}, // cross-repo head
		{Title: "t", Head: "alpine/t-1", Base: "main"},                                          // old free-form name
		{Title: "t", Head: "alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout", Base: "main..x"},   // bad base
	} {
		if _, err := c.CreatePullRequest(context.Background(), in); !errors.Is(err, ErrInvalidBranchName) {
			t.Errorf("%+v: %v", in, err)
		}
	}
	if n := len(f.requests()); n != 0 {
		t.Fatalf("requests %d", n)
	}
	// List errors abort before creating; other-head PRs are not reused.
	f2 := newFake(t, map[string]route{listPRsT1: {status: 200, body: `[{"number":9,"head":{"ref":"alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y6-other"}}]`}, postPulls: {status: 201, body: `{"number":10}`}})
	pr, err := idemClient(t, f2, &sleepRecorder{}).CreatePullRequest(context.Background(), NewPullRequest{Title: "t", Head: "alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout", Base: "main"})
	if err != nil || pr.Number != 10 || pr.Reused {
		t.Fatalf("%v %+v", err, pr)
	}
	pr, err = idemClient(t, f2, &sleepRecorder{}).FindOpenPullRequest(context.Background(), "alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout")
	if err != nil || pr != nil {
		t.Fatalf("%v %+v", err, pr)
	}
}

// ---- comments

func TestCreateIssueCommentWithKey(t *testing.T) {
	marker, _ := CommentMarker("triage:t-1")
	f := newFake(t, map[string]route{
		comments: {status: 200, body: `[{"id":1,"body":"spoof ` + marker + `","user":{"login":"mallory","type":"User"}}]`},
		postCmt:  {status: 201, body: `{"id":2}`},
	})
	cm, err := idemClient(t, f, &sleepRecorder{}).CreateIssueComment(context.Background(), 42, "triage:t-1", "Triaged as bug")
	if err != nil || cm.ID != 2 || cm.Reused {
		t.Fatalf("%v %+v", err, cm)
	}
	reqs := f.requests()
	assertSeq(t, reqs, "GET /repos/acme/shop/issues/42/comments", "POST /repos/acme/shop/issues/42/comments")
	if b := reqs[1].JSON(t); b["body"] != "Triaged as bug\n\n"+marker {
		t.Errorf("body %q", b["body"])
	}
}

func TestCreateIssueCommentReusesExisting(t *testing.T) {
	marker, _ := CommentMarker("triage:t-1")
	f := newFake(t, map[string]route{
		comments: {status: 200, body: `[{"id":1,"body":"x"}]`, hdr: map[string]string{"Link": `<{{base}}/repos/acme/shop/issues/42/comments?page=2>; rel="next"`}},
		"GET /repos/acme/shop/issues/42/comments?page=2": {status: 200, body: `[{"id":7,"body":"Triaged\n\n` + marker + `","user":{"login":"alpine-dev[bot]","type":"Bot"}}]`},
	})
	cm, err := idemClient(t, f, &sleepRecorder{}).CreateIssueComment(context.Background(), 42, "triage:t-1", "Triaged")
	if err != nil || cm.ID != 7 || !cm.Reused {
		t.Fatalf("%v %+v", err, cm)
	}
	for _, r := range f.requests() {
		if r.Method == "POST" {
			t.Fatal("must not post a duplicate")
		}
	}
}

func TestCreateIssueCommentAmbiguous(t *testing.T) {
	marker, _ := CommentMarker("k1")
	f := newFake(t, map[string]route{comments: {status: 200, body: `[{"id":3,"body":"hi\n\n` + marker + `","user":{"login":"Alpine-Dev[bot]","type":"Bot"},"performed_via_github_app":{"id":4242,"slug":"alpine-dev"}}]`}})
	f.script(comments, route{status: 200, body: `[]`})
	f.script(postCmt, route{hangup: true})
	cm, err := idemClient(t, f, &sleepRecorder{}).CreateIssueComment(context.Background(), 42, "k1", "hi")
	if err != nil || cm.ID != 3 || !cm.Reused {
		t.Fatalf("%v %+v", err, cm)
	}
	assertSeq(t, f.requests(), "GET /repos/acme/shop/issues/42/comments", "POST /repos/acme/shop/issues/42/comments", "GET /repos/acme/shop/issues/42/comments")

	// 500, re-check says absent -> posts again once.
	f2 := newFake(t, map[string]route{comments: {status: 200, body: `[]`}, postCmt: {status: 201, body: `{"id":4}`}})
	f2.script(postCmt, route{status: 500, body: `{}`})
	cm, err = idemClient(t, f2, &sleepRecorder{}).CreateIssueComment(context.Background(), 42, "k1", "hi")
	if err != nil || cm.ID != 4 || cm.Reused {
		t.Fatalf("%v %+v", err, cm)
	}
	assertSeq(t, f2.requests(), "GET /repos/acme/shop/issues/42/comments", "POST /repos/acme/shop/issues/42/comments",
		"GET /repos/acme/shop/issues/42/comments", "POST /repos/acme/shop/issues/42/comments")
}

func TestCreateIssueCommentFailsClosed(t *testing.T) {
	// More pages than the cap: refuse rather than risk a duplicate.
	c := idemClient(t, newFake(t, map[string]route{}), &sleepRecorder{})
	f2 := newFake(t, map[string]route{})
	for i := 1; i <= 11; i++ {
		key := "GET /repos/acme/shop/issues/42/comments?per_page=100"
		if i > 1 {
			key = "GET /repos/acme/shop/issues/42/comments?page=" + strconv.Itoa(i)
		}
		f2.set(key, route{status: 200, body: `[]`, hdr: map[string]string{"Link": `<{{base}}/repos/acme/shop/issues/42/comments?page=` + strconv.Itoa(i+1) + `>; rel="next"`}})
	}
	_, err := idemClient(t, f2, &sleepRecorder{}).CreateIssueComment(context.Background(), 42, "k", "hi")
	if !errors.Is(err, ErrPageCapReached) {
		t.Fatalf("got %v", err)
	}
	for _, r := range f2.requests() {
		if r.Method == "POST" {
			t.Fatal("must not post when the search was incomplete")
		}
	}
	// Invalid keys.
	for _, k := range []string{"has space", "a--b", "x>y", strings.Repeat("a", 129)} {
		if _, err := c.CreateIssueComment(context.Background(), 42, k, "hi"); !errors.Is(err, ErrInvalidIdempotencyKey) {
			t.Errorf("key %q: %v", k, err)
		}
		if _, err := c.UpdateIssueComment(context.Background(), 1, k, "hi"); !errors.Is(err, ErrInvalidIdempotencyKey) {
			t.Errorf("update key %q: %v", k, err)
		}
		if _, err := c.FindIssueCommentByKey(context.Background(), 42, k); !errors.Is(err, ErrInvalidIdempotencyKey) {
			t.Errorf("find key %q: %v", k, err)
		}
	}
	// Persistent ambiguous failure with re-check error.
	f3 := newFake(t, map[string]route{postCmt: {status: 502, body: `{}`}})
	f3.script(comments, route{status: 200, body: `[]`})
	if _, err := idemClient(t, f3, &sleepRecorder{}).CreateIssueComment(context.Background(), 42, "k", "hi"); StatusCode(err) != 502 {
		t.Fatalf("got %v", err)
	}
}

func TestFindIssueCommentOnlyOurApp(t *testing.T) {
	marker, _ := CommentMarker("triage:t-1")
	body := `"body":"x\n\n` + marker + `"`
	f := newFake(t, map[string]route{comments: {status: 200, body: `[
		{"id":1,` + body + `,"user":{"login":"mallory","type":"User"}},
		{"id":2,` + body + `,"user":{"login":"alpine-dev[bot]","type":"User"}},
		{"id":3,` + body + `,"user":{"login":"other-app[bot]","type":"Bot"},"performed_via_github_app":{"id":4242}},
		{"id":4,` + body + `,"user":{"login":"alpine-dev[bot]","type":"Bot"},"performed_via_github_app":{"id":999,"slug":"impostor"}},
		{"id":5,"body":"no marker","user":{"login":"alpine-dev[bot]","type":"Bot"}},
		{"id":6,` + body + `,"user":{"login":"alpine-dev[bot]","type":"Bot"},"performed_via_github_app":{"id":4242,"slug":"alpine-dev"}}
	]`}})
	c := idemClient(t, f, &sleepRecorder{})
	cm, err := c.FindIssueCommentByKey(context.Background(), 42, "triage:t-1")
	if err != nil || cm == nil || cm.ID != 6 || !cm.Reused || cm.PerformedViaGitHubApp.Slug != "alpine-dev" {
		t.Fatalf("%v %+v", err, cm)
	}
	// Another bot's comment with the same marker is ignored -> we post our own.
	f2 := newFake(t, map[string]route{
		comments: {status: 200, body: `[{"id":3,` + body + `,"user":{"login":"other-app[bot]","type":"Bot"},"performed_via_github_app":{"id":7}}]`},
		postCmt:  {status: 201, body: `{"id":10}`},
	})
	cm, err = idemClient(t, f2, &sleepRecorder{}).CreateIssueComment(context.Background(), 42, "triage:t-1", "x")
	if err != nil || cm.ID != 10 || cm.Reused {
		t.Fatalf("%v %+v", err, cm)
	}
}

func TestKeyedCommentsRequireIdentity(t *testing.T) {
	ctx := context.Background()
	f := newFake(t, map[string]route{})
	for _, id := range []struct {
		app   int64
		login string
	}{{0, ""}, {testAppID, ""}, {0, testBotLogin}} {
		c := retryClient(t, f, RetryPolicy{}, &sleepRecorder{}, 0.5)
		c.appID, c.botLogin = id.app, id.login
		if _, err := c.CreateIssueComment(ctx, 42, "k", "hi"); !errors.Is(err, ErrBotIdentityRequired) {
			t.Errorf("%+v create: %v", id, err)
		}
		if _, err := c.FindIssueCommentByKey(ctx, 42, "k"); !errors.Is(err, ErrBotIdentityRequired) {
			t.Errorf("%+v find: %v", id, err)
		}
	}
	if n := len(f.requests()); n != 0 {
		t.Fatalf("identity errors must not hit the network: %d", n)
	}
}

func TestClientIdentityValidation(t *testing.T) {
	base := ClientConfig{BaseURL: "https://api.example", Tokens: StaticToken(tok), Owner: "acme", Repo: "shop"}
	for _, ok := range []struct {
		app   int64
		login string
	}{{0, ""}, {1, "alpine[bot]"}, {1, "Alpine-Dev-2[bot]"}, {1, "a[bot]"}} {
		cfg := base
		cfg.AppID, cfg.BotLogin = ok.app, ok.login
		if _, err := NewClient(cfg); err != nil {
			t.Errorf("%+v: %v", ok, err)
		}
	}
	for _, bad := range []struct {
		app   int64
		login string
	}{{-1, ""}, {1, "alpine"}, {1, "alpine-bot"}, {1, "[bot]"}, {1, "-alpine[bot]"}, {1, "alpine-[bot]"}, {1, "al pine[bot]"}, {1, "alpine[bot] "}} {
		cfg := base
		cfg.AppID, cfg.BotLogin = bad.app, bad.login
		if _, err := NewClient(cfg); err == nil {
			t.Errorf("%+v: expected error", bad)
		}
	}
}

func TestUpdateIssueCommentKeepsMarker(t *testing.T) {
	f := newFake(t, map[string]route{"PATCH /repos/acme/shop/issues/comments/7": {status: 200, body: `{"id":7}`}})
	if _, err := idemClient(t, f, &sleepRecorder{}).UpdateIssueComment(context.Background(), 7, "k1", "v2"); err != nil {
		t.Fatal(err)
	}
	marker, _ := CommentMarker("k1")
	if b := f.requests()[0].JSON(t); b["body"] != "v2\n\n"+marker {
		t.Fatalf("body %q", b["body"])
	}
	if s, _ := BodyWithKey("k1", "v2\n\n\n"); s != "v2\n\n"+marker {
		t.Fatalf("%q", s)
	}
}

// ---- check runs

func TestCreateCheckRunIdempotent(t *testing.T) {
	o := CheckRunOptions{Name: "Alpine / lint", HeadSHA: "commit2", ExternalID: "run-1", Status: "queued"}
	// Existing run with the same external id -> reused.
	f := newFake(t, map[string]route{runsC2: {status: 200, body: `{"check_runs":[{"id":4,"name":"Alpine / lint","external_id":"run-1","app":{"id":7}},{"id":5,"name":"Alpine / lint","external_id":"run-0","app":{"id":4242}},{"id":6,"name":"Alpine / lint","external_id":"run-1","app":{"id":4242}}]}`}})
	cr, err := idemClient(t, f, &sleepRecorder{}).CreateCheckRun(context.Background(), o)
	if err != nil || cr.ID != 6 || !cr.Reused {
		t.Fatalf("%v %+v", err, cr)
	}
	assertSeq(t, f.requests(), "GET /repos/acme/shop/commits/commit2/check-runs")

	// Ambiguous create found on re-check.
	f2 := newFake(t, map[string]route{runsC2: {status: 200, body: `{"check_runs":[{"id":8,"name":"Alpine / lint","external_id":"run-1","app":{"id":4242}}]}`}})
	f2.script(runsC2, route{status: 200, body: `{"check_runs":[]}`})
	f2.script(postRuns, route{hangup: true})
	cr, err = idemClient(t, f2, &sleepRecorder{}).CreateCheckRun(context.Background(), o)
	if err != nil || cr.ID != 8 {
		t.Fatalf("%v %+v", err, cr)
	}
	assertSeq(t, f2.requests(), "GET /repos/acme/shop/commits/commit2/check-runs", "POST /repos/acme/shop/check-runs", "GET /repos/acme/shop/commits/commit2/check-runs")

	// Created normally; list error aborts; create error surfaces.
	f3 := newFake(t, map[string]route{runsC2: {status: 200, body: `{"check_runs":[]}`}, postRuns: {status: 201, body: `{"id":9}`}})
	if cr, err := idemClient(t, f3, &sleepRecorder{}).CreateCheckRun(context.Background(), o); err != nil || cr.ID != 9 || cr.Reused {
		t.Fatalf("%v %+v", err, cr)
	}
	f4 := newFake(t, map[string]route{})
	if _, err := idemClient(t, f4, &sleepRecorder{}).CreateCheckRun(context.Background(), o); StatusCode(err) != 404 {
		t.Fatalf("got %v", err)
	}
	// Without an AppID any matching run is accepted (previous behaviour).
	f6 := newFake(t, map[string]route{runsC2: {status: 200, body: `{"check_runs":[{"id":4,"name":"Alpine / lint","external_id":"run-1"}]}`}})
	if cr, err := retryClient(t, f6, RetryPolicy{}, &sleepRecorder{}, 0.5).CreateCheckRun(context.Background(), o); err != nil || cr.ID != 4 || !cr.Reused {
		t.Fatalf("%v %+v", err, cr)
	}
	f5 := newFake(t, map[string]route{runsC2: {status: 200, body: `{"check_runs":[]}`}, postRuns: {status: 422, body: `{}`}})
	if _, err := idemClient(t, f5, &sleepRecorder{}).CreateCheckRun(context.Background(), o); StatusCode(err) != 422 {
		t.Fatalf("got %v", err)
	}
}
