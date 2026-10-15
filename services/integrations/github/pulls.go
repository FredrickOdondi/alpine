package github

import (
	"context"
	"errors"
	"net/http"
	"strconv"
)

// NewPullRequest is the input to CreatePullRequest.
type NewPullRequest struct {
	Title  string
	Head   string // branch name in this repo
	Base   string
	Body   string
	Draft  bool
	Labels []string // applied after creation via the issues labels API
}

// PullRequest is the subset of a GitHub pull request Alpine tracks.
type PullRequest struct {
	ID      int64  `json:"id"`
	NodeID  string `json:"node_id"`
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
	State   string `json:"state"`
	Draft   bool   `json:"draft"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	Head    struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	Labels []Label `json:"labels"`
	// Reused is true when CreatePullRequest found an existing open PR for the
	// head branch and updated it instead of opening a new one.
	Reused bool `json:"-"`
}

// Label is a GitHub label.
type Label struct {
	Name string `json:"name"`
}

// CreatePullRequest opens a PR from an Alpine branch and applies Labels.
//
// Idempotent: it first looks up open PRs for the head branch; if one exists
// its title, body and base are updated to match and it is returned with
// Reused=true. An ambiguous create (timeout/5xx) or a 422 "already exists" is
// re-checked the same way before any second create. If labeling fails, the PR
// is still returned with the error so callers can retry labeling (AddLabels
// is idempotent).
func (c *Client) CreatePullRequest(ctx context.Context, in NewPullRequest) (*PullRequest, error) {
	if in.Title == "" || in.Head == "" || in.Base == "" {
		return nil, errors.New("github: pull request requires title, head and base")
	}
	if err := c.checkWritable(in.Head); err != nil {
		return nil, err
	}
	if err := validRefName(in.Base); err != nil {
		return nil, err
	}
	pr, err := c.findOpenPR(ctx, in.Head)
	if err != nil {
		return nil, err
	}
	if pr != nil {
		if pr, err = c.reconcilePR(ctx, pr, in); err != nil {
			return nil, err
		}
	} else {
		body := map[string]any{"title": in.Title, "head": in.Head, "base": in.Base, "body": in.Body, "draft": in.Draft}
		var created PullRequest
		err := c.createOnce(ctx,
			func() error { return c.doCreate(ctx, c.repoPath("pulls"), body, &created) },
			func() (bool, error) {
				found, err := c.findOpenPR(ctx, in.Head)
				if found != nil {
					pr = found
				}
				return found != nil, err
			},
			isUnprocessable) // 422 "A pull request already exists for owner:head"
		if err != nil {
			return nil, err
		}
		if pr == nil {
			pr = &created
		} else if pr, err = c.reconcilePR(ctx, pr, in); err != nil {
			return nil, err
		}
	}
	if len(in.Labels) > 0 {
		labels, err := c.AddLabels(ctx, pr.Number, in.Labels)
		if err != nil {
			return pr, err
		}
		pr.Labels = labels
	}
	return pr, nil
}

// FindOpenPullRequest returns the open PR whose head is branch, or nil.
func (c *Client) FindOpenPullRequest(ctx context.Context, branch string) (*PullRequest, error) {
	return c.findOpenPR(ctx, branch)
}

func (c *Client) findOpenPR(ctx context.Context, head string) (*PullRequest, error) {
	prs, err := c.ListPullRequests(ctx, PullRequestListOptions{State: "open", Head: head})
	if err != nil {
		return nil, err
	}
	for i := range prs {
		if prs[i].Head.Ref == "" || prs[i].Head.Ref == head {
			return &prs[i], nil
		}
	}
	return nil, nil
}

// reconcilePR updates an existing PR's title/body/base to match in (only the
// fields that differ) and marks it Reused.
func (c *Client) reconcilePR(ctx context.Context, pr *PullRequest, in NewPullRequest) (*PullRequest, error) {
	var up PullRequestUpdate
	if pr.Title != in.Title {
		up.Title = &in.Title
	}
	if pr.Body != in.Body {
		up.Body = &in.Body
	}
	if pr.Base.Ref != in.Base {
		up.Base = &in.Base
	}
	if up.Title != nil || up.Body != nil || up.Base != nil {
		updated, err := c.UpdatePullRequest(ctx, pr.Number, up)
		if err != nil {
			return nil, err
		}
		pr = updated
	}
	pr.Reused = true
	return pr, nil
}

// PullRequestUpdate holds optional PR fields; nil means unchanged.
type PullRequestUpdate struct {
	Title *string
	Body  *string
	State *string // "open" or "closed"
	Base  *string
	// Labels, when non-nil, replaces the PR's labels (PUT .../labels).
	Labels *[]string
}

// UpdatePullRequest patches a PR and optionally replaces its labels.
func (c *Client) UpdatePullRequest(ctx context.Context, number int, up PullRequestUpdate) (*PullRequest, error) {
	body := map[string]any{}
	for k, v := range map[string]*string{"title": up.Title, "body": up.Body, "state": up.State, "base": up.Base} {
		if v != nil {
			body[k] = *v
		}
	}
	var pr PullRequest
	if len(body) > 0 {
		if err := c.do(ctx, http.MethodPatch, c.repoPath("pulls", strconv.Itoa(number)), body, &pr); err != nil {
			return nil, err
		}
	} else if up.Labels == nil {
		return nil, errors.New("github: empty pull request update")
	}
	if up.Labels != nil {
		labels, err := c.SetLabels(ctx, number, *up.Labels)
		if err != nil {
			return nil, err
		}
		pr.Labels = labels
		if pr.Number == 0 {
			pr.Number = number
		}
	}
	return &pr, nil
}

// AddLabels adds labels to an issue or PR (POST /issues/{n}/labels).
func (c *Client) AddLabels(ctx context.Context, number int, labels []string) ([]Label, error) {
	var out []Label
	err := c.do(ctx, http.MethodPost, c.repoPath("issues", strconv.Itoa(number), "labels"),
		map[string][]string{"labels": labels}, &out)
	return out, err
}

// SetLabels replaces all labels on an issue or PR (PUT /issues/{n}/labels).
func (c *Client) SetLabels(ctx context.Context, number int, labels []string) ([]Label, error) {
	if labels == nil {
		labels = []string{}
	}
	var out []Label
	err := c.do(ctx, http.MethodPut, c.repoPath("issues", strconv.Itoa(number), "labels"),
		map[string][]string{"labels": labels}, &out)
	return out, err
}

// RemoveLabel removes one label from an issue or PR.
func (c *Client) RemoveLabel(ctx context.Context, number int, label string) error {
	return c.do(ctx, http.MethodDelete, c.repoPath("issues", strconv.Itoa(number), "labels", label), nil, nil)
}
