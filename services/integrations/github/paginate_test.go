package github

import (
	"context"
	"errors"
	"testing"
)

func link(next string) map[string]string {
	return map[string]string{"Link": `<{{base}}` + next + `>; rel="next", <{{base}}/x?page=9>; rel="last"`}
}

func TestListPullRequestsPaginates(t *testing.T) {
	f := newFake(t, map[string]route{
		"GET /repos/acme/shop/pulls?head=acme%3Aalpine%2F01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout&per_page=100&state=open": {status: 200,
			body: `[{"number":1},{"number":2}]`, hdr: link("/repos/acme/shop/pulls?head=acme%3Aalpine%2F01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout&page=2&per_page=100&state=open")},
		"GET /repos/acme/shop/pulls?head=acme%3Aalpine%2F01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout&page=2&per_page=100&state=open": {status: 200,
			body: `[{"number":3}]`, hdr: link("/repos/acme/shop/pulls?head=acme%3Aalpine%2F01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout&page=3&per_page=100&state=open")},
		"GET /repos/acme/shop/pulls?head=acme%3Aalpine%2F01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout&page=3&per_page=100&state=open": {status: 200,
			body: `[{"number":4}]`, hdr: map[string]string{"Link": `<{{base}}/x?page=1>; rel="first", <{{base}}/x?page=2>; rel="prev"`}},
	})
	c := newRepoClient(t, f)
	prs, err := c.ListPullRequests(context.Background(), PullRequestListOptions{Head: "alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout"})
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 4 || prs[3].Number != 4 {
		t.Fatalf("got %+v", prs)
	}
	reqs := f.requests()
	if len(reqs) != 3 {
		t.Fatalf("requests %d", len(reqs))
	}
	for _, r := range reqs {
		assertStdHeaders(t, r, "Bearer "+tok)
		if r.Path != "/repos/acme/shop/pulls" {
			t.Errorf("path %s", r.Path)
		}
	}

	// Page cap: partial results + ErrPageCapReached.
	prs, err = c.ListPullRequests(context.Background(), PullRequestListOptions{Head: "acme:alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout", ListOptions: ListOptions{MaxPages: 2}})
	if !errors.Is(err, ErrPageCapReached) || len(prs) != 3 {
		t.Fatalf("cap: %v %d", err, len(prs))
	}
}

func TestListPullRequestsFiltersAndPerPage(t *testing.T) {
	f := newFake(t, map[string]route{
		"GET /repos/acme/shop/pulls?base=main&per_page=100&state=all": {status: 200, body: `[]`},
		"GET /repos/acme/shop/pulls?per_page=5&state=open":            {status: 200, body: ``},
	})
	c := newRepoClient(t, f)
	if prs, err := c.ListPullRequests(context.Background(), PullRequestListOptions{State: "all", Base: "main", ListOptions: ListOptions{PerPage: 500}}); err != nil || len(prs) != 0 {
		t.Fatalf("%v %v", err, prs)
	}
	if _, err := c.ListPullRequests(context.Background(), PullRequestListOptions{ListOptions: ListOptions{PerPage: 5}}); err != nil {
		t.Fatal(err)
	}
}

func TestListIssueCommentsPaginates(t *testing.T) {
	f := newFake(t, map[string]route{
		"GET /repos/acme/shop/issues/42/comments?per_page=100": {status: 200, body: `[{"id":1,"body":"a"}]`,
			hdr: map[string]string{"Link": `<{{base}}/repos/acme/shop/issues/42/comments?page=2&per_page=100>; rel="next"`}},
		"GET /repos/acme/shop/issues/42/comments?page=2&per_page=100": {status: 200, body: `[{"id":2,"body":"b"}]`},
	})
	cs, err := newRepoClient(t, f).ListIssueComments(context.Background(), 42, ListOptions{})
	if err != nil || len(cs) != 2 || cs[1].ID != 2 {
		t.Fatalf("%v %+v", err, cs)
	}
}

func TestListCheckRunsForRefPaginates(t *testing.T) {
	f := newFake(t, map[string]route{
		"GET /repos/acme/shop/commits/alpine%2F01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout/check-runs?check_name=Alpine+%2F+lint&filter=all&per_page=100&status=completed": {status: 200,
			body: `{"total_count":3,"check_runs":[{"id":1,"name":"Alpine / lint","external_id":"run-1"},{"id":2}]}`,
			hdr:  map[string]string{"Link": `<{{base}}/repos/acme/shop/commits/alpine%2F01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout/check-runs?page=2>; rel="next"`}},
		"GET /repos/acme/shop/commits/alpine%2F01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout/check-runs?page=2": {status: 200, body: `{"total_count":3,"check_runs":[{"id":3}]}`},
	})
	runs, err := newRepoClient(t, f).ListCheckRunsForRef(context.Background(), "alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout",
		CheckRunListOptions{CheckName: "Alpine / lint", Status: "completed", Filter: "all"})
	if err != nil || len(runs) != 3 || runs[0].ExternalID != "run-1" || runs[0].Name != "Alpine / lint" {
		t.Fatalf("%v %+v", err, runs)
	}
	if _, err := newRepoClient(t, f).ListCheckRunsForRef(context.Background(), "", CheckRunListOptions{}); err == nil {
		t.Fatal("empty ref should fail")
	}
}

func TestPaginationErrors(t *testing.T) {
	f := newFake(t, map[string]route{
		// next link pointing at another origin must not receive our token
		"GET /repos/acme/shop/issues/1/comments?per_page=100": {status: 200, body: `[{"id":1}]`,
			hdr: map[string]string{"Link": `<https://evil.example/steal?page=2>; rel="next"`}},
		"GET /repos/acme/shop/issues/2/comments?per_page=100": {status: 200, body: `{"not":"an array"}`},
		"GET /repos/acme/shop/issues/3/comments?per_page=100": {status: 200, body: `[{"id":1}]`,
			hdr: map[string]string{"Link": `<{{base}}/repos/acme/shop/issues/3/comments?page=2>; rel="next"`}},
		"GET /repos/acme/shop/issues/3/comments?page=2": {status: 404, body: `{}`},
	})
	c := newRepoClient(t, f)
	cs, err := c.ListIssueComments(context.Background(), 1, ListOptions{})
	if err == nil || len(cs) != 1 {
		t.Fatalf("cross-origin: %v %v", err, cs)
	}
	if _, err := c.ListIssueComments(context.Background(), 2, ListOptions{}); err == nil {
		t.Fatal("decode error expected")
	}
	cs, err = c.ListIssueComments(context.Background(), 3, ListOptions{})
	if StatusCode(err) != 404 || len(cs) != 1 {
		t.Fatalf("mid-pagination error: %v %v", err, cs)
	}
	for _, r := range f.requests() {
		if r.Path == "/steal" {
			t.Fatal("followed cross-origin link")
		}
	}
}

func TestNextLink(t *testing.T) {
	cases := map[string]string{
		``: ``,
		`<https://api.github.com/x?page=2>; rel="next"`:                      `https://api.github.com/x?page=2`,
		`<https://a/x?page=1>; rel="prev", <https://a/x?page=3>; rel="next"`: `https://a/x?page=3`,
		`<https://a/x?page=3>; rel="next last"`:                              `https://a/x?page=3`,
		`<https://a/x?page=3>; rel=next`:                                     `https://a/x?page=3`,
		`<https://a/x?page=9>; rel="last"`:                                   ``,
		`garbage; rel="next"`:                                                ``,
		`<https://a/x>; title="next"`:                                        ``,
		`<https://a/x>; foo`:                                                 ``,
	}
	for in, want := range cases {
		if got := nextLink(in); got != want {
			t.Errorf("nextLink(%q) = %q, want %q", in, got, want)
		}
	}
}
