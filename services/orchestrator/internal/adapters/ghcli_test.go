package adapters

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/alpine-dev/alpine/services/orchestrator/internal/activities"
)

func TestGHCLIRefusesOtherRepos(t *testing.T) {
	calls := 0
	g := &GHCLISCM{AllowedRepos: []string{"Owner/testbed"}, Run: func(context.Context, []byte, ...string) ([]byte, error) {
		calls++
		return nil, nil
	}}
	ctx := context.Background()
	if _, err := g.CreatePullRequest(ctx, "owner/other", activities.SCMNewPullRequest{Head: "alpine/x", Base: "main"}); !errors.Is(err, ErrRepoNotAllowed) {
		t.Fatalf("want ErrRepoNotAllowed, got %v", err)
	}
	if _, err := g.CreateBranchFrom(ctx, "owner/other", "alpine/x", "main"); !errors.Is(err, ErrRepoNotAllowed) {
		t.Fatalf("want ErrRepoNotAllowed, got %v", err)
	}
	if _, err := g.CreateIssueComment(ctx, "owner/other", 1, "k", "b"); !errors.Is(err, ErrRepoNotAllowed) {
		t.Fatalf("want ErrRepoNotAllowed, got %v", err)
	}
	if calls != 0 {
		t.Fatalf("gh was called %d times for a refused repo", calls)
	}
}

func TestGHCLIPullRequestReuseAndCreate(t *testing.T) {
	var created []string
	open := `[]`
	g := &GHCLISCM{AllowedRepos: []string{"owner/testbed"}, Run: func(_ context.Context, stdin []byte, args ...string) ([]byte, error) {
		switch {
		case args[0] == "pr" && args[1] == "list":
			return []byte(open), nil
		case args[0] == "pr" && args[1] == "create":
			created = append(created, string(stdin))
			return []byte("https://github.com/owner/testbed/pull/7\n"), nil
		}
		return []byte(`{}`), nil
	}}
	ctx := context.Background()
	if _, err := g.CreatePullRequest(ctx, "owner/testbed", activities.SCMNewPullRequest{Head: "feature", Base: "main"}); err == nil {
		t.Fatal("non-alpine head must be refused")
	}
	pr, err := g.CreatePullRequest(ctx, "owner/testbed", activities.SCMNewPullRequest{Head: "alpine/x", Base: "main", Title: "t", Body: "evidence"})
	if err != nil || pr.Number != 7 || len(created) != 1 || !strings.Contains(created[0], "evidence") {
		t.Fatalf("create: pr=%+v err=%v created=%q", pr, err, created)
	}
	open = `[{"number":7,"url":"u","headRefOid":"abc","headRefName":"alpine/x"}]`
	pr, err = g.CreatePullRequest(ctx, "owner/testbed", activities.SCMNewPullRequest{Head: "alpine/x", Base: "main"})
	if err != nil || !pr.Reused || len(created) != 1 {
		t.Fatalf("reuse: pr=%+v err=%v created=%d", pr, err, len(created))
	}
}
