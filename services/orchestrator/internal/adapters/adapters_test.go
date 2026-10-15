package adapters

import (
	"context"
	"errors"
	"testing"

	"github.com/alpine-dev/alpine/packages/gates"
	"github.com/alpine-dev/alpine/packages/shared/gate"
	"github.com/alpine-dev/alpine/packages/shared/ticketflow"
	"github.com/alpine-dev/alpine/services/integrations/github"
	"github.com/alpine-dev/alpine/services/orchestrator/internal/activities"
	"github.com/google/uuid"
)

type fakeClient struct {
	branches, prs, comments int
}

func (f *fakeClient) CreateBranchFrom(_ context.Context, branch, _ string) (*github.Branch, error) {
	f.branches++
	return &github.Branch{Name: branch, SHA: "abc123", Created: f.branches == 1}, nil
}

func (f *fakeClient) CreatePullRequest(_ context.Context, in github.NewPullRequest) (*github.PullRequest, error) {
	f.prs++
	pr := &github.PullRequest{Number: 7, HTMLURL: "https://github.com/acme/widgets/pull/7", Reused: f.prs > 1}
	pr.Head.SHA = "abc123"
	pr.Head.Ref = in.Head
	return pr, nil
}

func (f *fakeClient) CreateIssueComment(context.Context, int, string, string) (*github.Comment, error) {
	f.comments++
	return &github.Comment{ID: 99}, nil
}

func TestGitHubSCMAdaptsRepoScopedClient(t *testing.T) {
	fc := &fakeClient{}
	lookups := 0
	scm := &GitHubSCM{Clients: func(_ context.Context, owner, repo string) (RepoClient, error) {
		lookups++
		if owner != "acme" || repo != "widgets" {
			t.Fatalf("owner/repo = %s/%s", owner, repo)
		}
		return fc, nil
	}}
	tid := uuid.MustParse("0192f0c4-7b1e-7c3a-9d2e-1f2a3b4c5d6e")
	ulid := ticketflow.TicketULID(tid)
	name, err := scm.BranchName(ulid, "Login button does nothing")
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := github.NewBranchName(ulid, "Login button does nothing"); name != want {
		t.Fatalf("branch %q, want %q", name, want)
	}
	ctx := context.Background()
	b, err := scm.CreateBranchFrom(ctx, "acme/widgets", name, "main")
	if err != nil || !b.Created || b.SHA != "abc123" {
		t.Fatalf("branch %+v %v", b, err)
	}
	pr, err := scm.CreatePullRequest(ctx, "acme/widgets", activities.SCMNewPullRequest{Title: "t", Head: name, Base: "main"})
	if err != nil || pr.Number != 7 || pr.HeadSHA != "abc123" || pr.URL == "" {
		t.Fatalf("pr %+v %v", pr, err)
	}
	if id, err := scm.CreateIssueComment(ctx, "acme/widgets", 7, "k", "body"); err != nil || id != 99 {
		t.Fatalf("comment %d %v", id, err)
	}
	if lookups != 1 {
		t.Fatalf("repo client should be cached, lookups=%d", lookups)
	}
	if _, err := scm.CreateBranchFrom(ctx, "bad", name, "main"); err == nil {
		t.Fatal("want error for bad repo name")
	}
}

func TestRepoGatesRequiresRepoDir(t *testing.T) {
	g := RepoGates{Runner: gates.NewRunner(nil)}
	if _, err := g.Run(context.Background(), gate.RunRequest{BaseSHA: "x"}); !errors.Is(err, gates.ErrRepoDirRequired) {
		t.Fatalf("want ErrRepoDirRequired without RepoDir, got %v", err)
	}
	_, err := g.Run(context.Background(), gate.RunRequest{RepoDir: t.TempDir(), BaseSHA: "deadbeef"})
	if err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("want base-ref error from SharedRunner, got %v", err)
	}
}
