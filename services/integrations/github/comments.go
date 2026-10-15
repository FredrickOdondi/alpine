package github

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// AppRef identifies the GitHub App behind a comment or check run.
type AppRef struct {
	ID   int64  `json:"id"`
	Slug string `json:"slug"`
}

// ErrBotIdentityRequired is returned by keyed comment operations when the
// Client has no AppID or BotLogin: without them Alpine's own comments cannot
// be told apart from other bots', so the lookup would not be safe.
var ErrBotIdentityRequired = errors.New("github: keyed comments require ClientConfig.AppID and BotLogin")

// Comment is an issue or PR conversation comment.
type Comment struct {
	ID      int64  `json:"id"`
	HTMLURL string `json:"html_url"`
	Body    string `json:"body"`
	User    struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"user"`
	// PerformedViaGitHubApp is set by GitHub for comments created with an
	// App's installation token (nil otherwise or when omitted).
	PerformedViaGitHubApp *AppRef `json:"performed_via_github_app"`
	// Reused is true when CreateIssueComment found an existing comment with
	// the same idempotency key instead of posting.
	Reused bool `json:"-"`
}

// CreateIssueComment comments on an issue or PR conversation
// (POST /repos/{o}/{r}/issues/{n}/comments).
//
// With a non-empty key the call is idempotent: the body carries a hidden
// marker (see CommentMarker); an existing comment of ours (see
// FindIssueCommentByKey) with that marker is returned (Reused=true) instead of posting, and an ambiguous
// create is re-checked before retrying. If the issue has more comments than
// the listing page cap, ErrPageCapReached is returned rather than risking a
// duplicate. With key == "" the comment is posted once and ambiguous failures
// are returned to the caller.
func (c *Client) CreateIssueComment(ctx context.Context, number int, key, body string) (*Comment, error) {
	if body == "" {
		return nil, errors.New("github: empty comment")
	}
	path := c.repoPath("issues", strconv.Itoa(number), "comments")
	if key == "" {
		var out Comment
		if err := c.doCreate(ctx, path, map[string]string{"body": body}, &out); err != nil {
			return nil, err
		}
		return &out, nil
	}
	full, err := BodyWithKey(key, body)
	if err != nil {
		return nil, err
	}
	if err := c.requireIdentity(); err != nil {
		return nil, err
	}
	existing, err := c.FindIssueCommentByKey(ctx, number, key)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}
	var out Comment
	err = c.createOnce(ctx,
		func() error { return c.doCreate(ctx, path, map[string]string{"body": full}, &out) },
		func() (bool, error) {
			found, err := c.FindIssueCommentByKey(ctx, number, key)
			if found != nil {
				existing = found
			}
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

// FindIssueCommentByKey returns Alpine's comment on issue/PR number carrying
// key's idempotency marker, or nil. Only comments by our own App count: the
// author must be a Bot whose login equals BotLogin (case-insensitively, as
// GitHub logins are), and if the comment reports performed_via_github_app its
// id must equal AppID. Markers in comments by people or other bots are
// ignored. Requires AppID and BotLogin (ErrBotIdentityRequired).
func (c *Client) FindIssueCommentByKey(ctx context.Context, number int, key string) (*Comment, error) {
	marker, err := CommentMarker(key)
	if err != nil {
		return nil, err
	}
	if err := c.requireIdentity(); err != nil {
		return nil, err
	}
	comments, err := c.ListIssueComments(ctx, number, ListOptions{})
	if err != nil {
		return nil, err
	}
	for i := range comments {
		cm := &comments[i]
		if c.ownComment(cm) && strings.Contains(cm.Body, marker) {
			cm.Reused = true
			return cm, nil
		}
	}
	return nil, nil
}

// UpdateIssueComment edits a comment (PATCH /repos/{o}/{r}/issues/comments/{id}),
// e.g. to keep a single, updated Alpine status comment per ticket. Pass the
// comment's key (or "") so the idempotency marker is preserved.
func (c *Client) UpdateIssueComment(ctx context.Context, id int64, key, body string) (*Comment, error) {
	if body == "" {
		return nil, errors.New("github: empty comment")
	}
	if key != "" {
		var err error
		if body, err = BodyWithKey(key, body); err != nil {
			return nil, err
		}
	}
	var out Comment
	if err := c.do(ctx, http.MethodPatch, c.repoPath("issues", "comments", strconv.FormatInt(id, 10)),
		map[string]string{"body": body}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) requireIdentity() error {
	if c.appID == 0 || c.botLogin == "" {
		return ErrBotIdentityRequired
	}
	return nil
}

// ownComment reports whether cm was written by this Client's App.
func (c *Client) ownComment(cm *Comment) bool {
	if cm.User.Type != "Bot" || !strings.EqualFold(cm.User.Login, c.botLogin) {
		return false
	}
	return cm.PerformedViaGitHubApp == nil || cm.PerformedViaGitHubApp.ID == c.appID
}
