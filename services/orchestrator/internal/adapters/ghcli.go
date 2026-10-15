package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strconv"
	"strings"

	"github.com/alpine-dev/alpine/services/integrations/github"
	"github.com/alpine-dev/alpine/services/orchestrator/internal/activities"
)

// GHCLISCM implements activities.SCM with the GitHub CLI's existing login (`gh auth login`), for
// running the factory against a test repo without a GitHub App. Writes are limited to
// AllowedRepos; anything else is refused before a gh call is made.
type GHCLISCM struct {
	Bin          string   // default "gh"
	AllowedRepos []string // "owner/repo" (case-insensitive); required
	// Run executes gh (swapped in tests). stdin may be nil.
	Run func(ctx context.Context, stdin []byte, args ...string) ([]byte, error)
}

var _ activities.SCM = (*GHCLISCM)(nil)

// ErrRepoNotAllowed is returned for repos outside AllowedRepos.
var ErrRepoNotAllowed = errors.New("scm(gh): repository is not in SCM_GH_ALLOWED_REPOS")

func (g *GHCLISCM) check(repo string) error {
	for _, r := range g.AllowedRepos {
		if strings.EqualFold(strings.TrimSpace(r), repo) {
			return nil
		}
	}
	return fmt.Errorf("%w: %s", ErrRepoNotAllowed, repo)
}

func (g *GHCLISCM) gh(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	if g.Run != nil {
		return g.Run(ctx, stdin, args...)
	}
	bin := g.Bin
	if bin == "" {
		bin = "gh"
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.Bytes(), fmt.Errorf("gh %s: %w: %s", args[0], err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

// BranchName implements activities.SCM (Omar's github.NewBranchName).
func (g *GHCLISCM) BranchName(ticketULID, title string) (string, error) {
	return github.NewBranchName(ticketULID, title)
}

func (g *GHCLISCM) refSHA(ctx context.Context, repo, branch string) (string, bool, error) {
	out, err := g.gh(ctx, nil, "api", "repos/"+repo+"/git/ref/heads/"+branch, "--jq", ".object.sha")
	if err != nil {
		if strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "Not Found") {
			return "", false, nil
		}
		return "", false, err
	}
	return strings.TrimSpace(string(out)), true, nil
}

// CreateBranchFrom implements activities.SCM: creates refs/heads/<branch> at <base>'s head, or
// returns the existing branch (Created=false).
func (g *GHCLISCM) CreateBranchFrom(ctx context.Context, repo, branch, base string) (*activities.SCMBranch, error) {
	if err := g.check(repo); err != nil {
		return nil, err
	}
	if sha, ok, err := g.refSHA(ctx, repo, branch); err != nil {
		return nil, err
	} else if ok {
		return &activities.SCMBranch{Name: branch, SHA: sha}, nil
	}
	baseSHA, ok, err := g.refSHA(ctx, repo, base)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("scm(gh): base branch %s not found in %s", base, repo)
	}
	if _, err := g.gh(ctx, nil, "api", "-X", "POST", "repos/"+repo+"/git/refs", "-f", "ref=refs/heads/"+branch, "-f", "sha="+baseSHA); err != nil {
		if strings.Contains(err.Error(), "Reference already exists") {
			sha, _, err := g.refSHA(ctx, repo, branch)
			return &activities.SCMBranch{Name: branch, SHA: sha}, err
		}
		return nil, err
	}
	return &activities.SCMBranch{Name: branch, SHA: baseSHA, Created: true}, nil
}

type ghPR struct {
	Number      int    `json:"number"`
	URL         string `json:"url"`
	HeadRefOid  string `json:"headRefOid"`
	HeadRefName string `json:"headRefName"`
}

// CreatePullRequest implements activities.SCM: reuses an open PR for the same head branch.
func (g *GHCLISCM) CreatePullRequest(ctx context.Context, repo string, in activities.SCMNewPullRequest) (*activities.SCMPullRequest, error) {
	if err := g.check(repo); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(in.Head, "alpine/") {
		return nil, fmt.Errorf("scm(gh): refusing non-alpine head branch %q", in.Head)
	}
	out, err := g.gh(ctx, nil, "pr", "list", "--repo", repo, "--head", in.Head, "--state", "open", "--json", "number,url,headRefOid,headRefName")
	if err != nil {
		return nil, err
	}
	var open []ghPR
	if err := json.Unmarshal(out, &open); err != nil {
		return nil, fmt.Errorf("scm(gh): decode pr list: %w", err)
	}
	if i := slices.IndexFunc(open, func(p ghPR) bool { return p.HeadRefName == in.Head }); i >= 0 {
		p := open[i]
		return &activities.SCMPullRequest{Number: p.Number, URL: p.URL, HeadSHA: p.HeadRefOid, Reused: true}, nil
	}
	args := []string{"pr", "create", "--repo", repo, "--head", in.Head, "--base", in.Base, "--title", in.Title, "--body-file", "-"}
	if in.Draft {
		args = append(args, "--draft")
	}
	out, err = g.gh(ctx, []byte(in.Body), args...)
	if err != nil {
		return nil, err
	}
	url := strings.TrimSpace(string(out))
	if i := strings.LastIndex(url, "\n"); i >= 0 {
		url = strings.TrimSpace(url[i+1:])
	}
	n, err := strconv.Atoi(url[strings.LastIndex(url, "/")+1:])
	if err != nil {
		return nil, fmt.Errorf("scm(gh): unexpected pr create output %q", url)
	}
	return &activities.SCMPullRequest{Number: n, URL: url}, nil
}

// CreateIssueComment implements activities.SCM. The key is embedded as a hidden marker so a retry
// finds the earlier comment instead of posting twice.
func (g *GHCLISCM) CreateIssueComment(ctx context.Context, repo string, number int, key, body string) (int64, error) {
	if err := g.check(repo); err != nil {
		return 0, err
	}
	marker := "<!-- alpine:" + key + " -->"
	out, err := g.gh(ctx, nil, "api", fmt.Sprintf("repos/%s/issues/%d/comments?per_page=100", repo, number))
	if err != nil {
		return 0, err
	}
	var existing []struct {
		ID   int64  `json:"id"`
		Body string `json:"body"`
	}
	if err := json.Unmarshal(out, &existing); err == nil {
		for _, c := range existing {
			if strings.Contains(c.Body, marker) {
				return c.ID, nil
			}
		}
	}
	payload, _ := json.Marshal(map[string]string{"body": body + "\n\n" + marker})
	out, err = g.gh(ctx, payload, "api", "-X", "POST", fmt.Sprintf("repos/%s/issues/%d/comments", repo, number), "--input", "-")
	if err != nil {
		return 0, err
	}
	var created struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(out, &created)
	return created.ID, nil
}
