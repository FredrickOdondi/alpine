package onboarding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/alpine-dev/alpine/packages/shared/factoryconfig"
	"github.com/alpine-dev/alpine/packages/shared/intake"
	"github.com/alpine-dev/alpine/packages/shared/ticketflow"
	"github.com/alpine-dev/alpine/packages/shared/ticketrepo"
	"github.com/alpine-dev/alpine/services/integrations/github"
)

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// FactoryConfigPath is where a repo's factory config lives.
const FactoryConfigPath = ".alpine/factory.yaml"

// RepoOption is one row of the repo picker.
type RepoOption struct {
	ProviderRepoID int64
	FullName       string
	Private        bool
	DefaultBranch  string
	Enabled        bool
	RepoID         *uuid.UUID
	ConfigStatus   string // unknown | configured | missing | invalid
	ConfigSHA      *string
	ConfigError    string
	StarterYAML    string
}

// ErrGitHub wraps GitHub API failures (mapped to 502).
var ErrGitHub = errors.New("github call failed")

func (s *Service) installationFor(ctx context.Context, org uuid.UUID, installationID int64) (ticketrepo.Installation, error) {
	inst, err := s.Store.GetInstallation(ctx, org, installationID)
	if err != nil {
		return inst, err
	}
	return inst, nil
}

// ListRepos lists the installation's repositories merged with their enablement.
func (s *Service) ListRepos(ctx context.Context, org uuid.UUID, installationID int64) ([]RepoOption, error) {
	if _, err := s.installationFor(ctx, org, installationID); err != nil {
		return nil, err
	}
	app, _, err := s.githubApp(ctx)
	if err != nil {
		return nil, err
	}
	repos, err := app.ListInstallationRepos(ctx, installationID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrGitHub, err)
	}
	states, err := s.Store.RepoStates(ctx, org, installationID)
	if err != nil {
		return nil, err
	}
	out := make([]RepoOption, 0, len(repos))
	for _, r := range repos {
		if r.Archived {
			continue
		}
		o := RepoOption{ProviderRepoID: r.ID, FullName: r.FullName, Private: r.Private, DefaultBranch: orDefault(r.DefaultBranch, "main"), ConfigStatus: "unknown"}
		if st, ok := states[r.ID]; ok {
			id := st.ID
			o.Enabled, o.RepoID, o.ConfigSHA = st.Enabled, &id, st.ConfigSHA
			if st.Enabled && st.ConfigSHA != nil {
				o.ConfigStatus = "configured"
			}
		}
		out = append(out, o)
	}
	return out, nil
}

// SetEnabled enables or disables a repo. Enabling reads .alpine/factory.yaml from the default
// branch: valid -> stored and configured; invalid -> error shown; missing -> starter config.
func (s *Service) SetEnabled(ctx context.Context, org, user uuid.UUID, installationID, providerRepoID int64, enabled bool) (RepoOption, error) {
	if _, err := s.installationFor(ctx, org, installationID); err != nil {
		return RepoOption{}, err
	}
	app, _, err := s.githubApp(ctx)
	if err != nil {
		return RepoOption{}, err
	}
	repos, err := app.ListInstallationRepos(ctx, installationID)
	if err != nil {
		return RepoOption{}, fmt.Errorf("%w: %v", ErrGitHub, err)
	}
	var gr *github.InstallationRepo
	for i := range repos {
		if repos[i].ID == providerRepoID {
			gr = &repos[i]
		}
	}
	if gr == nil {
		return RepoOption{}, ticketrepo.ErrNotFound // not accessible to this installation
	}
	st, err := s.Store.SetRepoEnabled(ctx, org, intake.RepoRef{Provider: "github", InstallationID: installationID, RepoID: gr.ID,
		FullName: gr.FullName, DefaultBranch: orDefault(gr.DefaultBranch, "main")}, enabled)
	if err != nil {
		return RepoOption{}, err
	}
	id := st.ID
	o := RepoOption{ProviderRepoID: gr.ID, FullName: gr.FullName, Private: gr.Private, DefaultBranch: st.DefaultBranch, Enabled: st.Enabled,
		RepoID: &id, ConfigStatus: "unknown", ConfigSHA: st.ConfigSHA}
	action := "repo.disabled"
	if enabled {
		action = "repo.enabled"
		if err := s.readConfig(ctx, app, org, installationID, &o); err != nil {
			return o, err
		}
	}
	_ = s.Store.Audit(ctx, org, "user:"+user.String(), action, "repo", st.ID.String(), map[string]any{"full_name": gr.FullName})
	return o, nil
}

func (s *Service) readConfig(ctx context.Context, app *github.App, org uuid.UUID, installationID int64, o *RepoOption) error {
	owner, name, err := ownerRepo(o.FullName)
	if err != nil {
		return err
	}
	cl, err := app.RepoClient(installationID, owner, name, github.Permissions{"contents": github.Read, "metadata": github.Read})
	if err != nil {
		return err
	}
	raw, _, err := cl.GetFile(ctx, FactoryConfigPath, o.DefaultBranch)
	if errors.Is(err, github.ErrFileNotFound) {
		o.ConfigStatus = "missing"
		o.StarterYAML = StarterConfig(detectStack(ctx, cl, o.DefaultBranch))
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: %v", ErrGitHub, err)
	}
	cfg, perr := factoryconfig.Parse(raw)
	if perr != nil {
		o.ConfigStatus, o.ConfigError = "invalid", perr.Error()
		return nil
	}
	head, err := cl.BranchSHA(ctx, o.DefaultBranch)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrGitHub, err)
	}
	if err := s.Store.SaveFactoryConfig(ctx, org, *o.RepoID, head, string(raw), cfg); err != nil {
		return err
	}
	o.ConfigStatus, o.ConfigSHA = "configured", &head
	return nil
}

// Stack is a detected project type for the starter config.
type Stack string

const (
	StackGo      Stack = "go"
	StackNode    Stack = "node"
	StackPython  Stack = "python"
	StackUnknown Stack = ""
)

func detectStack(ctx context.Context, cl *github.Client, ref string) Stack {
	for _, c := range []struct {
		file  string
		stack Stack
	}{{"go.mod", StackGo}, {"package.json", StackNode}, {"pyproject.toml", StackPython}} {
		if _, _, err := cl.GetFile(ctx, c.file, ref); err == nil {
			return c.stack
		}
	}
	return StackUnknown
}

// StarterConfig is a conservative starter .alpine/factory.yaml for a stack: tight limits, gates
// that run the project's tests, human merge for every blast radius.
func StarterConfig(stack Stack) string {
	var gates, allow string
	switch stack {
	case StackGo:
		allow = "[GOPATH, GOCACHE, GOMODCACHE, GOFLAGS]"
		gates = `  - id: vet
    parser: none
    run: go vet ./...
  - id: unit
    parser: go-test
    run: go test -json ./...`
	case StackNode:
		allow = "[NODE_ENV, CI]"
		gates = `  - id: install
    parser: none
    run: npm ci
  - id: unit
    parser: none
    run: npm test`
	case StackPython:
		allow = "[PYTHONPATH, CI]"
		gates = `  - id: unit
    parser: none
    run: python -m pytest -q`
	default:
		allow = "[CI]"
		gates = `  - id: unit
    parser: none
    run: make test`
	}
	return strings.TrimLeft(`
# Alpine factory config: how Alpine may change this repo. Edit before merging.
# Docs: limits cap each ticket; gates must pass before a PR opens; merge_policy "human" means
# Alpine only opens PRs and a person merges them.
version: 1
limits:
  max_turns: 30
  max_gate_iterations: 3
  max_cost_usd: 2
gate_runner:
  env_allowlist: `+allow+`
gates:
`+gates+`
protected_paths:
  - .github/**
  - .alpine/**
merge_policy:
  BR1: human
  BR2: human
  BR3: human
  BR4: human
`, "\n")
}

// StarterPR is the result of OpenStarterPR.
type StarterPR struct {
	Number int
	URL    string
	Branch string
}

// OpenStarterPR commits a starter (or edited) config to an alpine/* branch and opens a PR.
// Repeated calls reuse the same branch and open PR.
func (s *Service) OpenStarterPR(ctx context.Context, org, user, repoID uuid.UUID, yamlText string) (StarterPR, error) {
	repo, err := s.Store.GetRepo(ctx, org, repoID)
	if err != nil {
		return StarterPR{}, err
	}
	app, _, err := s.githubApp(ctx)
	if err != nil {
		return StarterPR{}, err
	}
	owner, name, err := ownerRepo(repo.FullName)
	if err != nil {
		return StarterPR{}, err
	}
	cl, err := app.RepoClient(repo.GitHubInstallationID, owner, name, github.Merge(github.PermsPush(), github.PermsPullRequests()))
	if err != nil {
		return StarterPR{}, err
	}
	if strings.TrimSpace(yamlText) == "" {
		yamlText = StarterConfig(detectStack(ctx, cl, repo.DefaultBranch))
	}
	if _, err := factoryconfig.Parse([]byte(yamlText)); err != nil {
		return StarterPR{}, fmt.Errorf("%w: %v", ErrBadInput, err)
	}
	branch, err := github.NewBranchName(ticketflow.TicketULID(repoID), "add alpine factory config")
	if err != nil {
		return StarterPR{}, err
	}
	if pr, err := cl.FindOpenPullRequest(ctx, branch); err == nil && pr != nil {
		return StarterPR{Number: pr.Number, URL: pr.HTMLURL, Branch: branch}, nil
	}
	if _, err := cl.CreateBranchFrom(ctx, branch, repo.DefaultBranch); err != nil {
		return StarterPR{}, fmt.Errorf("%w: %v", ErrGitHub, err)
	}
	if _, err := cl.PushFiles(ctx, github.Push{Branch: branch, Message: "Add Alpine factory config (.alpine/factory.yaml)",
		Changes: []github.FileChange{{Path: FactoryConfigPath, Content: []byte(yamlText)}}}); err != nil {
		return StarterPR{}, fmt.Errorf("%w: %v", ErrGitHub, err)
	}
	pr, err := cl.CreatePullRequest(ctx, github.NewPullRequest{Title: "Add Alpine factory config", Head: branch, Base: repo.DefaultBranch,
		Body: "This adds `.alpine/factory.yaml`, which tells Alpine which gates must pass, its per-ticket limits and that every PR needs a human merge.\n\nReview the gate commands, then merge. Alpine never merges this PR itself.\n\n_Opened from Alpine onboarding._"})
	if err != nil {
		return StarterPR{}, fmt.Errorf("%w: %v", ErrGitHub, err)
	}
	_ = s.Store.Audit(ctx, org, "user:"+user.String(), "repo.starter_config_pr", "repo", repoID.String(),
		map[string]any{"pr": pr.Number, "branch": branch})
	return StarterPR{Number: pr.Number, URL: pr.HTMLURL, Branch: branch}, nil
}
