package inbox

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/alpine-dev/alpine/packages/shared/intake"
)

// MetaHeadBranch is the TicketIntake.Metadata key carrying a merged PR's head branch.
const MetaHeadBranch = "head_branch"

// GitHubMinimal is a small intake.Normalizer. In production (Normalizers) it only handles
// `pull_request` closed+merged on an alpine/* branch -> intake.ActionPRMerged; issues and comments
// go to services/integrations/github/webhook. It still understands issues events so it can be
// used standalone in tests; both emit ExternalID "gh:<repo_id>#<n>".
type GitHubMinimal struct {
	// BotLogin ("<app-slug>[bot]") is ignored as an issue author to prevent loops.
	BotLogin string
}

func (GitHubMinimal) Source() intake.Source { return intake.SourceGitHub }

type ghPayload struct {
	Action string `json:"action"`
	Issue  *struct {
		Number    int        `json:"number"`
		Title     string     `json:"title"`
		Body      *string    `json:"body"`
		HTMLURL   string     `json:"html_url"`
		CreatedAt *time.Time `json:"created_at"`
		User      struct {
			Login string `json:"login"`
			Type  string `json:"type"`
		} `json:"user"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
		PullRequest json.RawMessage `json:"pull_request"`
	} `json:"issue"`
	PullRequest *struct {
		Number  int    `json:"number"`
		Merged  bool   `json:"merged"`
		HTMLURL string `json:"html_url"`
		Head    struct {
			Ref string `json:"ref"`
		} `json:"head"`
	} `json:"pull_request"`
	Repository struct {
		ID            int64  `json:"id"`
		FullName      string `json:"full_name"`
		DefaultBranch string `json:"default_branch"`
	} `json:"repository"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
}

// ExternalID is "gh:<repo_id>#<n>" (stable across renames and transfers).
func ExternalID(repoID int64, n int) string { return fmt.Sprintf("gh:%d#%d", repoID, n) }

// Normalize implements intake.Normalizer.
func (g GitHubMinimal) Normalize(_ context.Context, eventType string, raw []byte) ([]intake.TicketIntake, error) {
	var p ghPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("github: decode %s: %w", eventType, err)
	}
	repo := intake.RepoRef{Provider: "github", InstallationID: p.Installation.ID, RepoID: p.Repository.ID,
		FullName: p.Repository.FullName, DefaultBranch: p.Repository.DefaultBranch}
	switch eventType {
	case "issues":
		if p.Issue == nil || len(p.Issue.PullRequest) > 0 {
			return nil, intake.ErrIgnored
		}
		var action intake.Action
		switch p.Action {
		case "opened":
			action = intake.ActionCreate
		case "reopened":
			action = intake.ActionReopen
		case "closed":
			action = intake.ActionClose
		default:
			return nil, intake.ErrIgnored
		}
		if action == intake.ActionCreate && (p.Issue.User.Type == "Bot" || (g.BotLogin != "" && p.Issue.User.Login == g.BotLogin)) {
			return nil, intake.ErrIgnored
		}
		t := intake.TicketIntake{
			Source: intake.SourceGitHub, Action: action, ExternalID: ExternalID(p.Repository.ID, p.Issue.Number),
			ExternalURL: p.Issue.HTMLURL, Number: p.Issue.Number, Repo: repo, Title: p.Issue.Title,
			Author: p.Issue.User.Login, Raw: raw,
		}
		if p.Issue.Body != nil {
			t.Body = *p.Issue.Body
		}
		for _, l := range p.Issue.Labels {
			t.Labels = append(t.Labels, l.Name)
		}
		t.DedupeKey = t.ExternalID
		if p.Issue.CreatedAt != nil {
			t.OccurredAt = *p.Issue.CreatedAt
		}
		return []intake.TicketIntake{t}, nil
	case "pull_request":
		if p.PullRequest == nil || p.Action != "closed" || !p.PullRequest.Merged || !strings.HasPrefix(p.PullRequest.Head.Ref, "alpine/") {
			return nil, intake.ErrIgnored
		}
		return []intake.TicketIntake{{
			Source: intake.SourceGitHub, Action: intake.ActionPRMerged, Number: p.PullRequest.Number, Repo: repo,
			ExternalURL: p.PullRequest.HTMLURL, Metadata: map[string]string{MetaHeadBranch: p.PullRequest.Head.Ref},
		}}, nil
	default:
		return nil, intake.ErrIgnored
	}
}
