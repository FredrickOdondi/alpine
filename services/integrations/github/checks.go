package github

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"
)

// MaxCheckOutputChars is GitHub's limit for check run output summary/text.
const MaxCheckOutputChars = 65535

// CheckRunOutput is the rendered body of a check run.
type CheckRunOutput struct {
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Text    string `json:"text,omitempty"`
}

// CheckRunOptions is used for both create and update; zero fields are omitted.
type CheckRunOptions struct {
	Name        string          `json:"name,omitempty"`
	HeadSHA     string          `json:"head_sha,omitempty"` // create only
	DetailsURL  string          `json:"details_url,omitempty"`
	ExternalID  string          `json:"external_id,omitempty"`
	Status      string          `json:"status,omitempty"`     // queued, in_progress, completed
	Conclusion  string          `json:"conclusion,omitempty"` // success, failure, neutral, cancelled, skipped, timed_out, action_required
	StartedAt   *time.Time      `json:"started_at,omitempty"`
	CompletedAt *time.Time      `json:"completed_at,omitempty"`
	Output      *CheckRunOutput `json:"output,omitempty"`
}

// CheckRun is the subset of a check run response Alpine tracks.
type CheckRun struct {
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	ExternalID string  `json:"external_id"`
	HTMLURL    string  `json:"html_url"`
	Status     string  `json:"status"`
	Conclusion string  `json:"conclusion"`
	HeadSHA    string  `json:"head_sha"`
	App        *AppRef `json:"app"`
	// Reused is true when CreateCheckRun found an existing run with the same
	// name and ExternalID on HeadSHA instead of creating one.
	Reused bool `json:"-"`
}

// CreateCheckRun posts a new check run (POST /repos/{o}/{r}/check-runs).
//
// With an ExternalID it is idempotent: an existing run on HeadSHA with the
// same name and external_id is returned (Reused=true), and an ambiguous
// create is re-checked before retrying. Without ExternalID it posts once.
func (c *Client) CreateCheckRun(ctx context.Context, o CheckRunOptions) (*CheckRun, error) {
	if o.Name == "" || o.HeadSHA == "" {
		return nil, errors.New("github: check run requires name and head_sha")
	}
	in := clampOutput(o)
	path := c.repoPath("check-runs")
	var out CheckRun
	if o.ExternalID == "" {
		if err := c.doCreate(ctx, path, in, &out); err != nil {
			return nil, err
		}
		return &out, nil
	}
	existing, err := c.findCheckRun(ctx, o)
	if err != nil || existing != nil {
		return existing, err
	}
	err = c.createOnce(ctx,
		func() error { return c.doCreate(ctx, path, in, &out) },
		func() (bool, error) {
			found, err := c.findCheckRun(ctx, o)
			existing = found
			return found != nil, err
		}, nil)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}
	return &out, nil
}

func (c *Client) findCheckRun(ctx context.Context, o CheckRunOptions) (*CheckRun, error) {
	runs, err := c.ListCheckRunsForRef(ctx, o.HeadSHA, CheckRunListOptions{CheckName: o.Name, Filter: "all"})
	if err != nil {
		return nil, err
	}
	for i := range runs {
		if runs[i].ExternalID == o.ExternalID && runs[i].Name == o.Name && c.ownCheckRun(&runs[i]) {
			runs[i].Reused = true
			return &runs[i], nil
		}
	}
	return nil, nil
}

// UpdateCheckRun patches an existing check run. HeadSHA is ignored.
func (c *Client) UpdateCheckRun(ctx context.Context, id int64, o CheckRunOptions) (*CheckRun, error) {
	o.HeadSHA = ""
	var out CheckRun
	if err := c.do(ctx, http.MethodPatch, c.repoPath("check-runs", strconv.FormatInt(id, 10)), clampOutput(o), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func clampOutput(o CheckRunOptions) CheckRunOptions {
	if o.Output != nil {
		out := *o.Output
		out.Summary = truncate(out.Summary, MaxCheckOutputChars)
		out.Text = truncate(out.Text, MaxCheckOutputChars)
		o.Output = &out
	}
	return o
}

const truncationNote = "\n\n… (truncated)"

// truncate cuts s to at most max characters (runes), appending a note.
func truncate(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	keep := max - utf8.RuneCountInString(truncationNote)
	i := 0
	for n := 0; n < keep; n++ {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return s[:i] + truncationNote
}

// ownCheckRun: with an AppID configured, only runs created by that App are
// reused (another App's run could not be updated by us anyway). Without an
// AppID any matching run is accepted.
func (c *Client) ownCheckRun(cr *CheckRun) bool {
	return c.appID == 0 || (cr.App != nil && cr.App.ID == c.appID)
}
