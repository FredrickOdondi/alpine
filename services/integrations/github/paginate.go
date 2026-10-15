package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// ListOptions controls pagination of list calls.
type ListOptions struct {
	// PerPage is the page size; default and maximum 100.
	PerPage int
	// MaxPages caps how many pages are fetched; default 10. If more pages
	// remain, the items fetched so far are returned with ErrPageCapReached.
	MaxPages int
}

func (o ListOptions) withDefaults() ListOptions {
	if o.PerPage <= 0 || o.PerPage > 100 {
		o.PerPage = 100
	}
	if o.MaxPages <= 0 {
		o.MaxPages = 10
	}
	return o
}

// ErrPageCapReached is returned (wrapped) with partial results when
// ListOptions.MaxPages was hit before the last page.
var ErrPageCapReached = errors.New("github: page cap reached before the last page")

// listPages GETs path?q and follows Link rel="next" until there is no next
// page or MaxPages is reached. decode turns one page body into items.
// Next links must stay on the client's base URL (enforced by the transport).
func listPages[T any](ctx context.Context, c *Client, path string, q url.Values, opt ListOptions,
	decode func(json.RawMessage) ([]T, error)) ([]T, error) {
	opt = opt.withDefaults()
	if q == nil {
		q = url.Values{}
	}
	q.Set("per_page", strconv.Itoa(opt.PerPage))
	next := path + "?" + q.Encode()
	var all []T
	for page := 1; ; page++ {
		var raw json.RawMessage
		hdr, err := c.call(ctx, call{method: http.MethodGet, path: next, out: &raw, mode: retrySafe})
		if err != nil {
			return all, err
		}
		items, err := decode(raw)
		if err != nil {
			return all, fmt.Errorf("github: GET %s: decode page %d: %w", path, page, err)
		}
		all = append(all, items...)
		next = nextLink(hdr.Get("Link"))
		if next == "" {
			return all, nil
		}
		if page >= opt.MaxPages {
			return all, fmt.Errorf("%w (%d pages of %s)", ErrPageCapReached, page, path)
		}
	}
}

func decodeArray[T any](raw json.RawMessage) ([]T, error) {
	var out []T
	if len(raw) == 0 {
		return nil, nil
	}
	err := json.Unmarshal(raw, &out)
	return out, err
}

// nextLink extracts the rel="next" URL from an RFC 8288 Link header.
func nextLink(h string) string {
	for _, part := range strings.Split(h, ",") {
		segs := strings.Split(part, ";")
		u := strings.TrimSpace(segs[0])
		if len(u) < 2 || u[0] != '<' || u[len(u)-1] != '>' {
			continue
		}
		for _, p := range segs[1:] {
			k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
			if !ok || strings.TrimSpace(k) != "rel" {
				continue
			}
			for _, rel := range strings.Fields(strings.Trim(strings.TrimSpace(v), `"`)) {
				if rel == "next" {
					return u[1 : len(u)-1]
				}
			}
		}
	}
	return ""
}

// PullRequestListOptions filters ListPullRequests.
type PullRequestListOptions struct {
	State string // open (default), closed, all
	// Head filters by head branch. A bare branch name is qualified with the
	// repository owner ("owner:branch") as GitHub requires.
	Head string
	Base string
	ListOptions
}

// ListPullRequests lists PRs (GET /repos/{o}/{r}/pulls), following pagination.
func (c *Client) ListPullRequests(ctx context.Context, o PullRequestListOptions) ([]PullRequest, error) {
	q := url.Values{}
	q.Set("state", orString(o.State, "open"))
	if o.Head != "" {
		h := o.Head
		if !strings.Contains(h, ":") {
			h = c.owner + ":" + h
		}
		q.Set("head", h)
	}
	if o.Base != "" {
		q.Set("base", o.Base)
	}
	return listPages(ctx, c, c.repoPath("pulls"), q, o.ListOptions, decodeArray[PullRequest])
}

// ListIssueComments lists the conversation comments of an issue or PR.
func (c *Client) ListIssueComments(ctx context.Context, number int, o ListOptions) ([]Comment, error) {
	return listPages(ctx, c, c.repoPath("issues", strconv.Itoa(number), "comments"), nil, o, decodeArray[Comment])
}

// CheckRunListOptions filters ListCheckRunsForRef.
type CheckRunListOptions struct {
	CheckName string
	Status    string // queued, in_progress, completed
	Filter    string // latest (GitHub default) or all
	ListOptions
}

// ListCheckRunsForRef lists check runs for a SHA, branch or tag
// (GET /repos/{o}/{r}/commits/{ref}/check-runs), following pagination.
func (c *Client) ListCheckRunsForRef(ctx context.Context, ref string, o CheckRunListOptions) ([]CheckRun, error) {
	if ref == "" {
		return nil, errors.New("github: ListCheckRunsForRef requires a ref")
	}
	q := url.Values{}
	for k, v := range map[string]string{"check_name": o.CheckName, "status": o.Status, "filter": o.Filter} {
		if v != "" {
			q.Set(k, v)
		}
	}
	return listPages(ctx, c, c.repoPath("commits", ref, "check-runs"), q, o.ListOptions,
		func(raw json.RawMessage) ([]CheckRun, error) {
			var page struct {
				CheckRuns []CheckRun `json:"check_runs"`
			}
			err := json.Unmarshal(raw, &page)
			return page.CheckRuns, err
		})
}
