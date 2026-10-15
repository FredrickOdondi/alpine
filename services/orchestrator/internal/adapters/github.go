// Package adapters connects the orchestrator's activity seams to the real implementations owned by
// other workstreams: omar/integrations' GitHub App client (SCM) and lena/gates' gate runner.
package adapters

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/alpine-dev/alpine/services/integrations/github"
	"github.com/alpine-dev/alpine/services/orchestrator/internal/activities"
)

// RepoClient is the subset of *github.Client the SCM adapter uses (a client scoped to one repo).
type RepoClient interface {
	CreateBranchFrom(ctx context.Context, branch, base string) (*github.Branch, error)
	CreatePullRequest(ctx context.Context, in github.NewPullRequest) (*github.PullRequest, error)
	CreateIssueComment(ctx context.Context, number int, key, body string) (*github.Comment, error)
}

// RepoClients returns a repo-scoped client for owner/repo.
type RepoClients func(ctx context.Context, owner, repo string) (RepoClient, error)

// AppRepoClients builds repo-scoped clients from a GitHub App: it looks up the repo's installation
// and mints installation tokens scoped to that single repository with contents, pull_requests and
// issues write (the token source caches and refreshes tokens).
func AppRepoClients(app *github.App) RepoClients {
	perms := github.Merge(github.PermsPush(), github.PermsPullRequests(), github.PermsIssueComments())
	return func(ctx context.Context, owner, repo string) (RepoClient, error) {
		inst, err := app.RepoInstallation(ctx, owner, repo)
		if err != nil {
			return nil, fmt.Errorf("github: installation for %s/%s: %w", owner, repo, err)
		}
		return app.RepoClient(inst.ID, owner, repo, perms)
	}
}

// GitHubSCM implements activities.SCM with omar/integrations' GitHub client. Clients are cached
// per repo; branch names come from github.NewBranchName (alpine/<ticket_ulid>-<slug>), and the
// client itself only allows writes to alpine/* branches.
type GitHubSCM struct {
	Clients RepoClients

	mu    sync.Mutex
	cache map[string]RepoClient
}

var _ activities.SCM = (*GitHubSCM)(nil)

func (g *GitHubSCM) client(ctx context.Context, full string) (RepoClient, error) {
	owner, repo, ok := strings.Cut(full, "/")
	if !ok || owner == "" || repo == "" {
		return nil, fmt.Errorf("github: bad repo name %q", full)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if c, ok := g.cache[full]; ok {
		return c, nil
	}
	c, err := g.Clients(ctx, owner, repo)
	if err != nil {
		return nil, err
	}
	if g.cache == nil {
		g.cache = map[string]RepoClient{}
	}
	g.cache[full] = c
	return c, nil
}

// BranchName implements activities.SCM.
func (g *GitHubSCM) BranchName(ticketULID, title string) (string, error) {
	return github.NewBranchName(ticketULID, title)
}

// CreateBranchFrom implements activities.SCM.
func (g *GitHubSCM) CreateBranchFrom(ctx context.Context, repo, branch, base string) (*activities.SCMBranch, error) {
	c, err := g.client(ctx, repo)
	if err != nil {
		return nil, err
	}
	b, err := c.CreateBranchFrom(ctx, branch, base)
	if err != nil {
		return nil, err
	}
	return &activities.SCMBranch{Name: b.Name, SHA: b.SHA, Created: b.Created}, nil
}

// CreatePullRequest implements activities.SCM (idempotent: an open PR for the head is reused).
func (g *GitHubSCM) CreatePullRequest(ctx context.Context, repo string, in activities.SCMNewPullRequest) (*activities.SCMPullRequest, error) {
	c, err := g.client(ctx, repo)
	if err != nil {
		return nil, err
	}
	pr, err := c.CreatePullRequest(ctx, github.NewPullRequest{Title: in.Title, Head: in.Head, Base: in.Base, Body: in.Body,
		Draft: in.Draft, Labels: in.Labels})
	if err != nil {
		return nil, err
	}
	return &activities.SCMPullRequest{Number: pr.Number, URL: pr.HTMLURL, HeadSHA: pr.Head.SHA, Reused: pr.Reused}, nil
}

// CreateIssueComment implements activities.SCM (idempotent on key).
func (g *GitHubSCM) CreateIssueComment(ctx context.Context, repo string, number int, key, body string) (int64, error) {
	c, err := g.client(ctx, repo)
	if err != nil {
		return 0, err
	}
	cm, err := c.CreateIssueComment(ctx, number, key, body)
	if err != nil {
		return 0, err
	}
	return cm.ID, nil
}
