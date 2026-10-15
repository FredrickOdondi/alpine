package github

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

const tok = "ghs_fake_installation_token"

func newRepoClient(t *testing.T, f *fakeGitHub) *Client {
	t.Helper()
	c, err := NewClient(ClientConfig{BaseURL: f.URL, Tokens: StaticToken(tok), Owner: "acme", Repo: "shop", Sleep: noSleep})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNewClientValidation(t *testing.T) {
	bad := []ClientConfig{
		{Owner: "a", Repo: "b"},
		{Tokens: StaticToken("x"), Repo: "b"},
		{Tokens: StaticToken("x"), Owner: "a/b", Repo: "c"},
		{Tokens: StaticToken("x"), Owner: "a", Repo: "b", BaseURL: "nope"},
	}
	for i, c := range bad {
		if _, err := NewClient(c); err == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
	c, err := NewClient(ClientConfig{Tokens: StaticToken("x"), Owner: "a", Repo: "b"})
	if err != nil || c.t.base != DefaultBaseURL || c.prefixes[0] != "alpine/" {
		t.Fatalf("%v %+v", err, c)
	}
}

func TestCreateBranch(t *testing.T) {
	f := newFake(t, map[string]route{
		"GET /repos/acme/shop/git/ref/heads/main": {status: 200, body: `{"ref":"refs/heads/main","object":{"sha":"basesha"}}`},
		"POST /repos/acme/shop/git/refs":          {status: 201, body: `{"ref":"refs/heads/alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout"}`},
	})
	c := newRepoClient(t, f)
	br, err := c.CreateBranchFrom(context.Background(), "alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout", "main")
	if err != nil || br.SHA != "basesha" || !br.Created || br.Name != "alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout" {
		t.Fatalf("%v %+v", err, br)
	}
	reqs := f.requests()
	assertSeq(t, reqs, "GET /repos/acme/shop/git/ref/heads/alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout", "GET /repos/acme/shop/git/ref/heads/main", "POST /repos/acme/shop/git/refs")
	for _, r := range reqs {
		assertStdHeaders(t, r, "Bearer "+tok)
	}
	if b := reqs[2].JSON(t); b["ref"] != "refs/heads/alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout" || b["sha"] != "basesha" {
		t.Errorf("body %v", b)
	}
}

func TestBranchGuard(t *testing.T) {
	f := newFake(t, map[string]route{})
	c := newRepoClient(t, f)
	ctx := context.Background()
	for _, b := range []string{"main", "alpine/", "release/alpine/x", "refs/heads/alpine/x", ""} {
		if _, err := c.CreateBranch(ctx, b, "sha"); err == nil {
			t.Errorf("CreateBranch(%q) should be refused", b)
		}
		if _, err := c.CreateBranchFrom(ctx, b, "main"); err == nil {
			t.Errorf("CreateBranchFrom(%q) should be refused", b)
		}
		if err := c.UpdateRef(ctx, b, "sha", false); err == nil {
			t.Errorf("UpdateRef(%q) should be refused", b)
		}
		if _, err := c.PushFiles(ctx, Push{Branch: b, Message: "m", Changes: []FileChange{{Path: "a"}}}); err == nil {
			t.Errorf("PushFiles(%q) should be refused", b)
		}
	}
	if _, err := c.CreateBranch(ctx, "main", "sha"); !errors.Is(err, ErrBranchNotWritable) {
		t.Errorf("want ErrBranchNotWritable, got %v", err)
	}
	if _, err := c.CreateBranch(ctx, "alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y5-x", ""); err == nil {
		t.Error("empty sha should fail")
	}
	if n := len(f.requests()); n != 0 {
		t.Fatalf("guarded calls must not hit the network, got %d", n)
	}

	// Explicit empty prefix list allows any branch.
	open, _ := NewClient(ClientConfig{BaseURL: f.URL, Tokens: StaticToken(tok), Owner: "acme", Repo: "shop", WritableBranchPrefixes: []string{}})
	f.set("PATCH /repos/acme/shop/git/refs/heads/feature/x", route{status: 200, body: `{}`})
	if err := open.UpdateRef(ctx, "feature/x", "abc", true); err != nil {
		t.Fatal(err)
	}
	if b := f.requests()[0].JSON(t); b["sha"] != "abc" || b["force"] != true {
		t.Errorf("body %v", b)
	}
}

func pushRoutes() map[string]route {
	return map[string]route{
		"GET /repos/acme/shop/git/ref/heads/alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout":    {status: 200, body: `{"object":{"sha":"parent1"}}`},
		"GET /repos/acme/shop/git/commits/parent1":                                             {status: 200, body: `{"sha":"parent1","tree":{"sha":"basetree"}}`},
		"POST /repos/acme/shop/git/blobs":                                                      {status: 201, body: `{"sha":"blob1"}`},
		"POST /repos/acme/shop/git/trees":                                                      {status: 201, body: `{"sha":"tree2"}`},
		"POST /repos/acme/shop/git/commits":                                                    {status: 201, body: `{"sha":"commit2"}`},
		"PATCH /repos/acme/shop/git/refs/heads/alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout": {status: 200, body: `{"object":{"sha":"commit2"}}`},
	}
}

func TestPushFiles(t *testing.T) {
	f := newFake(t, pushRoutes())
	c := newRepoClient(t, f)
	sha, err := c.PushFiles(context.Background(), Push{
		Branch:          "alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout",
		Message:         "fix: guard empty cart",
		ExpectedHeadSHA: "parent1",
		Author:          &Signature{Name: "Alpine", Email: "bot@alpine.dev"},
		Changes: []FileChange{
			{Path: "src/cart.ts", Content: []byte("export const x = 1\n")},
			{Path: "scripts/run.sh", Content: []byte("#!/bin/sh\n"), Mode: ModeExecutable},
			{Path: "old.txt", Delete: true},
		},
	})
	if err != nil || sha != "commit2" {
		t.Fatalf("%v %q", err, sha)
	}
	reqs := f.requests()
	assertSeq(t, reqs,
		"GET /repos/acme/shop/git/ref/heads/alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout",
		"GET /repos/acme/shop/git/commits/parent1",
		"POST /repos/acme/shop/git/blobs",
		"POST /repos/acme/shop/git/blobs",
		"POST /repos/acme/shop/git/trees",
		"POST /repos/acme/shop/git/commits",
		"PATCH /repos/acme/shop/git/refs/heads/alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout",
	)
	for _, r := range reqs {
		assertStdHeaders(t, r, "Bearer "+tok)
	}
	blob := reqs[2].JSON(t)
	if blob["encoding"] != "base64" || blob["content"] != base64.StdEncoding.EncodeToString([]byte("export const x = 1\n")) {
		t.Errorf("blob body %v", blob)
	}
	tree := reqs[4].JSON(t)
	if tree["base_tree"] != "basetree" {
		t.Errorf("base_tree %v", tree["base_tree"])
	}
	entries := tree["tree"].([]any)
	want := []string{
		"map[mode:100644 path:src/cart.ts sha:blob1 type:blob]",
		"map[mode:100755 path:scripts/run.sh sha:blob1 type:blob]",
		"map[mode:100644 path:old.txt sha:<nil> type:blob]",
	}
	for i, w := range want {
		if g := fmt.Sprint(entries[i]); g != w {
			t.Errorf("tree entry %d: %s want %s", i, g, w)
		}
	}
	if !strings.Contains(string(reqs[4].Body), `"path":"old.txt","mode":"100644","type":"blob","sha":null`) {
		t.Errorf("deletion must send sha:null: %s", reqs[4].Body)
	}
	commit := reqs[5].JSON(t)
	if commit["message"] != "fix: guard empty cart" || commit["tree"] != "tree2" || fmt.Sprint(commit["parents"]) != "[parent1]" ||
		fmt.Sprint(commit["author"]) != "map[email:bot@alpine.dev name:Alpine]" {
		t.Errorf("commit body %v", commit)
	}
	ref := reqs[6].JSON(t)
	if ref["sha"] != "commit2" || ref["force"] != false {
		t.Errorf("ref body %v", ref)
	}
}

func TestPushFilesFailures(t *testing.T) {
	ctx := context.Background()
	f := newFake(t, pushRoutes())
	c := newRepoClient(t, f)

	invalid := []Push{
		{Branch: "alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout", Changes: []FileChange{{Path: "a"}}},
		{Branch: "alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout", Message: "m"},
		{Branch: "alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout", Message: "m", Changes: []FileChange{{Path: "/abs"}}},
		{Branch: "alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout", Message: "m", Changes: []FileChange{{Path: "../up"}}},
		{Branch: "alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout", Message: "m", Changes: []FileChange{{Path: "a/../b"}}},
		{Branch: "alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout", Message: "m", Changes: []FileChange{{Path: "a//b"}}},
		{Branch: "alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout", Message: "m", Changes: []FileChange{{Path: ".git/config"}}},
		{Branch: "alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout", Message: "m", Changes: []FileChange{{Path: "a\\b"}}},
		{Branch: "alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout", Message: "m", Changes: []FileChange{{Path: ""}}},
		{Branch: "alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout", Message: "m", Changes: []FileChange{{Path: "a"}, {Path: "a"}}},
		{Branch: "alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout", Message: "m", Changes: []FileChange{{Path: "a", Mode: "040000"}}},
	}
	for i, p := range invalid {
		if _, err := c.PushFiles(ctx, p); err == nil {
			t.Errorf("invalid %d: expected error", i)
		}
	}
	if n := len(f.requests()); n != 0 {
		t.Fatalf("invalid pushes must not hit the network, got %d", n)
	}

	ok := Push{Branch: "alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout", Message: "m", Changes: []FileChange{{Path: "a", Content: []byte("x")}}}

	stale := ok
	stale.ExpectedHeadSHA = "other"
	if _, err := c.PushFiles(ctx, stale); !errors.Is(err, ErrStaleHead) {
		t.Errorf("want ErrStaleHead, got %v", err)
	}

	// Each step failing aborts the push and surfaces the API error.
	steps := []string{
		"GET /repos/acme/shop/git/ref/heads/alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout",
		"GET /repos/acme/shop/git/commits/parent1",
		"POST /repos/acme/shop/git/blobs",
		"POST /repos/acme/shop/git/trees",
		"POST /repos/acme/shop/git/commits",
		"PATCH /repos/acme/shop/git/refs/heads/alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout",
	}
	for i, step := range steps {
		routes := pushRoutes()
		routes[step] = route{status: 422, body: `{"message":"Update is not a fast forward"}`}
		f := newFake(t, routes)
		c := newRepoClient(t, f)
		if _, err := c.PushFiles(ctx, ok); StatusCode(err) != 422 {
			t.Errorf("step %s: want 422, got %v", step, err)
		}
		if n := len(f.requests()); n != i+1 {
			t.Errorf("step %s: expected abort after %d requests, got %d", step, i+1, n)
		}
	}
}

func TestPullRequests(t *testing.T) {
	ctx := context.Background()
	f := newFake(t, map[string]route{
		"GET /repos/acme/shop/pulls?head=acme%3Aalpine%2F01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout&per_page=100&state=open": {status: 200, body: `[]`},
		"POST /repos/acme/shop/pulls":                                     {status: 201, body: `{"id":9,"number":50,"html_url":"https://github.com/acme/shop/pull/50","state":"open","draft":true,"head":{"ref":"alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout","sha":"commit2"}}`},
		"POST /repos/acme/shop/issues/50/labels":                          {status: 200, body: `[{"name":"alpine"},{"name":"br:1"}]`},
		"PATCH /repos/acme/shop/pulls/50":                                 {status: 200, body: `{"number":50,"title":"new","body":"b2","state":"open"}`},
		"PUT /repos/acme/shop/issues/50/labels":                           {status: 200, body: `[{"name":"alpine"}]`},
		"DELETE /repos/acme/shop/issues/50/labels/needs%20review%2Fhuman": {status: 200, body: `[]`},
	})
	c := newRepoClient(t, f)
	pr, err := c.CreatePullRequest(ctx, NewPullRequest{
		Title: "alpine: fix checkout", Head: "alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout", Base: "main", Body: "## Summary\n...", Draft: true,
		Labels: []string{"alpine", "br:1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 50 || pr.Head.SHA != "commit2" || len(pr.Labels) != 2 || !pr.Draft {
		t.Fatalf("pr %+v", pr)
	}
	reqs := f.requests()[1:]
	assertSeq(t, f.requests(), "GET /repos/acme/shop/pulls", "POST /repos/acme/shop/pulls", "POST /repos/acme/shop/issues/50/labels")
	if b := reqs[0].JSON(t); b["title"] != "alpine: fix checkout" || b["head"] != "alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout" || b["base"] != "main" ||
		b["body"] != "## Summary\n..." || b["draft"] != true {
		t.Errorf("create body %v", b)
	}
	if b := reqs[1].JSON(t); fmt.Sprint(b["labels"]) != "[alpine br:1]" {
		t.Errorf("labels body %v", b)
	}

	title, body := "new", "b2"
	labels := []string{"alpine"}
	pr, err = c.UpdatePullRequest(ctx, 50, PullRequestUpdate{Title: &title, Body: &body, Labels: &labels})
	if err != nil || pr.Title != "new" || len(pr.Labels) != 1 {
		t.Fatalf("%v %+v", err, pr)
	}
	reqs = f.requests()[3:]
	assertSeq(t, reqs, "PATCH /repos/acme/shop/pulls/50", "PUT /repos/acme/shop/issues/50/labels")
	if b := reqs[0].JSON(t); len(b) != 2 || b["title"] != "new" || b["body"] != "b2" {
		t.Errorf("patch body %v", b)
	}

	// Labels-only update clears labels with an empty array, skips PATCH.
	empty := []string(nil)
	pr, err = c.UpdatePullRequest(ctx, 50, PullRequestUpdate{Labels: &empty})
	if err != nil || pr.Number != 50 {
		t.Fatalf("%v %+v", err, pr)
	}
	last := f.requests()[5]
	if last.Method != "PUT" || string(last.Body) != `{"labels":[]}` {
		t.Errorf("clear labels: %s %s", last.Method, last.Body)
	}

	if _, err := c.UpdatePullRequest(ctx, 50, PullRequestUpdate{}); err == nil {
		t.Error("empty update should fail")
	}
	if err := c.RemoveLabel(ctx, 50, "needs review/human"); err != nil {
		t.Errorf("remove label: %v", err)
	}
	if _, err := c.CreatePullRequest(ctx, NewPullRequest{Title: "t", Head: "h"}); err == nil {
		t.Error("missing base should fail")
	}
	closed := "closed"
	if _, err := c.UpdatePullRequest(ctx, 51, PullRequestUpdate{State: &closed}); StatusCode(err) != 404 {
		t.Errorf("want 404, got %v", err)
	}
	if _, err := c.UpdatePullRequest(ctx, 51, PullRequestUpdate{Labels: &labels}); StatusCode(err) != 404 {
		t.Errorf("want 404, got %v", err)
	}
}

func TestCreatePullRequestLabelFailureReturnsPR(t *testing.T) {
	f := newFake(t, map[string]route{
		"GET /repos/acme/shop/pulls?head=acme%3Aalpine%2F01J9ZKX3M4Q7R8S9T0V1W2X3Y5-x&per_page=100&state=open": {status: 200, body: `[]`},
		"POST /repos/acme/shop/pulls":           {status: 201, body: `{"number":7}`},
		"POST /repos/acme/shop/issues/7/labels": {status: 403, body: `{"message":"Resource not accessible by integration"}`},
	})
	pr, err := newRepoClient(t, f).CreatePullRequest(context.Background(), NewPullRequest{Title: "t", Head: "alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y5-x", Base: "main", Labels: []string{"a"}})
	if pr == nil || pr.Number != 7 || StatusCode(err) != 403 {
		t.Fatalf("%+v %v", pr, err)
	}
	f2 := newFake(t, map[string]route{})
	if _, err := newRepoClient(t, f2).CreatePullRequest(context.Background(), NewPullRequest{Title: "t", Head: "alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y5-x", Base: "main"}); StatusCode(err) != 404 {
		t.Fatalf("%v", err)
	}
}

func TestCheckRuns(t *testing.T) {
	ctx := context.Background()
	f := newFake(t, map[string]route{
		"GET /repos/acme/shop/commits/commit2/check-runs?check_name=Alpine+%2F+lint&filter=all&per_page=100": {status: 200, body: `{"check_runs":[]}`},
		"POST /repos/acme/shop/check-runs":       {status: 201, body: `{"id":4242,"status":"in_progress","head_sha":"commit2","html_url":"https://github.com/acme/shop/runs/4242"}`},
		"PATCH /repos/acme/shop/check-runs/4242": {status: 200, body: `{"id":4242,"status":"completed","conclusion":"failure"}`},
	})
	c := newRepoClient(t, f)
	started := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	cr, err := c.CreateCheckRun(ctx, CheckRunOptions{
		Name: "Alpine / lint", HeadSHA: "commit2", Status: "in_progress", StartedAt: &started,
		ExternalID: "run-1", DetailsURL: "https://app.alpine.dev/runs/1",
		Output: &CheckRunOutput{Title: "Lint", Summary: "running"},
	})
	if err != nil || cr.ID != 4242 || cr.Status != "in_progress" {
		t.Fatalf("%v %+v", err, cr)
	}
	b := f.requests()[1].JSON(t)
	if b["name"] != "Alpine / lint" || b["head_sha"] != "commit2" || b["status"] != "in_progress" || b["started_at"] != "2026-10-15T12:00:00Z" ||
		b["external_id"] != "run-1" || b["details_url"] != "https://app.alpine.dev/runs/1" || b["conclusion"] != nil || b["completed_at"] != nil {
		t.Errorf("create body %v", b)
	}
	if fmt.Sprint(b["output"]) != "map[summary:running title:Lint]" {
		t.Errorf("output %v", b["output"])
	}

	huge := strings.Repeat("é", MaxCheckOutputChars+10)
	out := &CheckRunOutput{Title: "Lint", Summary: "3 errors", Text: huge}
	cr, err = c.UpdateCheckRun(ctx, 4242, CheckRunOptions{HeadSHA: "ignored", Status: "completed", Conclusion: "failure", Output: out})
	if err != nil || cr.Conclusion != "failure" {
		t.Fatalf("%v %+v", err, cr)
	}
	r := f.requests()[2]
	assertSeq(t, f.requests()[2:], "PATCH /repos/acme/shop/check-runs/4242")
	assertStdHeaders(t, r, "Bearer "+tok)
	ub := r.JSON(t)
	if ub["head_sha"] != nil || ub["conclusion"] != "failure" || ub["status"] != "completed" {
		t.Errorf("update body %v", ub)
	}
	text := ub["output"].(map[string]any)["text"].(string)
	if n := len([]rune(text)); n != MaxCheckOutputChars || !strings.HasSuffix(text, "(truncated)") {
		t.Errorf("text not truncated to limit: %d runes", n)
	}
	if out.Text != huge {
		t.Error("caller's output must not be mutated")
	}

	if _, err := c.CreateCheckRun(ctx, CheckRunOptions{Name: "x"}); err == nil {
		t.Error("missing head_sha should fail")
	}
	if _, err := c.UpdateCheckRun(ctx, 1, CheckRunOptions{Status: "completed"}); StatusCode(err) != 404 {
		t.Errorf("want 404, got %v", err)
	}
	if _, err := c.CreateCheckRun(ctx, CheckRunOptions{Name: "x", HeadSHA: "nope"}); err != nil {
		t.Errorf("unexpected %v", err)
	}
	f.set("POST /repos/acme/shop/check-runs", route{status: 422, body: `{"message":"Validation Failed","errors":[{"resource":"CheckRun","code":"invalid"}]}`})
	if _, err := c.CreateCheckRun(ctx, CheckRunOptions{Name: "x", HeadSHA: "y"}); err == nil || !strings.Contains(err.Error(), `"code":"invalid"`) {
		t.Errorf("validation errors should be surfaced: %v", err)
	}
}

func TestTruncate(t *testing.T) {
	if truncate("abc", 5) != "abc" {
		t.Fatal("short strings unchanged")
	}
	s := truncate(strings.Repeat("a", 100), 50)
	if len([]rune(s)) != 50 || !strings.HasSuffix(s, truncationNote) {
		t.Fatalf("%q", s)
	}
}

func TestComments(t *testing.T) {
	ctx := context.Background()
	f := newFake(t, map[string]route{
		"POST /repos/acme/shop/issues/42/comments":   {status: 201, body: `{"id":900,"html_url":"https://github.com/acme/shop/issues/42#issuecomment-900","body":"Triaged"}`},
		"PATCH /repos/acme/shop/issues/comments/900": {status: 200, body: `{"id":900,"body":"Triaged (updated)"}`},
	})
	c := newRepoClient(t, f)
	cm, err := c.CreateIssueComment(ctx, 42, "", "Triaged")
	if err != nil || cm.ID != 900 {
		t.Fatalf("%v %+v", err, cm)
	}
	cm, err = c.UpdateIssueComment(ctx, 900, "", "Triaged (updated)")
	if err != nil || cm.Body != "Triaged (updated)" {
		t.Fatalf("%v %+v", err, cm)
	}
	reqs := f.requests()
	assertSeq(t, reqs, "POST /repos/acme/shop/issues/42/comments", "PATCH /repos/acme/shop/issues/comments/900")
	for _, r := range reqs {
		assertStdHeaders(t, r, "Bearer "+tok)
	}
	if string(reqs[0].Body) != `{"body":"Triaged"}` || string(reqs[1].Body) != `{"body":"Triaged (updated)"}` {
		t.Errorf("bodies %s / %s", reqs[0].Body, reqs[1].Body)
	}
	if _, err := c.CreateIssueComment(ctx, 42, "", ""); err == nil {
		t.Error("empty comment should fail")
	}
	if _, err := c.UpdateIssueComment(ctx, 900, "", ""); err == nil {
		t.Error("empty comment should fail")
	}
	if _, err := c.CreateIssueComment(ctx, 1, "", "x"); StatusCode(err) != 404 {
		t.Error("want 404")
	}
	if _, err := c.UpdateIssueComment(ctx, 1, "", "x"); StatusCode(err) != 404 {
		t.Error("want 404")
	}
}

func TestTransportAndDecodeErrors(t *testing.T) {
	ctx := context.Background()
	f := newFake(t, map[string]route{
		"POST /repos/acme/shop/issues/1/comments": {status: 500, body: `<html>oops</html>`},
		"POST /repos/acme/shop/issues/2/comments": {status: 201, body: `{"id":"not-a-number"}`},
		"POST /repos/acme/shop/issues/3/comments": {status: 204, body: ``},
	})
	c := newRepoClient(t, f)
	_, err := c.CreateIssueComment(ctx, 1, "", "x")
	var ae *APIError
	if !errors.As(err, &ae) || ae.StatusCode != 500 || ae.Message != "Internal Server Error" || ae.Method != "POST" ||
		ae.Path != "/repos/acme/shop/issues/1/comments" {
		t.Errorf("500: %#v", err)
	}
	if _, err := c.CreateIssueComment(ctx, 2, "", "x"); err == nil || StatusCode(err) != 0 {
		t.Errorf("decode error expected, got %v", err)
	}
	if _, err := c.CreateIssueComment(ctx, 3, "", "x"); err != nil {
		t.Errorf("empty 2xx body should be ok: %v", err)
	}

	tokErr := errors.New("vault down")
	bad, _ := NewClient(ClientConfig{BaseURL: f.URL, Tokens: TokenFunc(func(context.Context) (string, error) { return "", tokErr }), Owner: "a", Repo: "b"})
	if _, err := bad.CreateIssueComment(ctx, 1, "", "x"); !errors.Is(err, tokErr) {
		t.Errorf("token error not wrapped: %v", err)
	}

	down, _ := NewClient(ClientConfig{BaseURL: "http://127.0.0.1:1", Tokens: StaticToken("x"), Owner: "a", Repo: "b",
		HTTPClient: &http.Client{Timeout: time.Second}, Sleep: noSleep})
	if _, err := down.CreateIssueComment(ctx, 1, "", "x"); err == nil {
		t.Error("expected transport error")
	}

	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.CreateIssueComment(cctx, 3, "", "x"); !errors.Is(err, context.Canceled) {
		t.Errorf("want context.Canceled, got %v", err)
	}
	if StatusCode(nil) != 0 {
		t.Error("StatusCode(nil)")
	}
}
