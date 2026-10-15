package github

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
)

// ErrBranchNotWritable is returned when a write targets a branch outside the
// Client's WritableBranchPrefixes.
var ErrBranchNotWritable = errors.New("github: branch is not writable by Alpine")

func (c *Client) checkWritable(branch string) error {
	if err := validRefName(branch); err != nil {
		return err
	}
	// Every write under alpine/ must use the per-ticket name format, even when
	// WritableBranchPrefixes allows other branches too.
	if strings.HasPrefix(branch, BranchPrefix) {
		if err := ValidateBranchName(branch); err != nil {
			return err
		}
	}
	if len(c.prefixes) == 0 {
		return nil
	}
	for _, p := range c.prefixes {
		if strings.HasPrefix(branch, p) && len(branch) > len(p) {
			return nil
		}
	}
	return fmt.Errorf("%w: %q (allowed prefixes %v)", ErrBranchNotWritable, branch, c.prefixes)
}

// refPath returns /repos/o/r/git/<kind>/heads/<branch...>; branch slashes are
// kept as path separators, each segment escaped.
func (c *Client) refPath(kind, branch string) string {
	return c.repoPath(append([]string{"git", kind, "heads"}, strings.Split(branch, "/")...)...)
}

type refResponse struct {
	Ref    string `json:"ref"`
	Object struct {
		SHA string `json:"sha"`
	} `json:"object"`
}

// BranchSHA returns the commit SHA at the head of branch.
func (c *Client) BranchSHA(ctx context.Context, branch string) (string, error) {
	var r refResponse
	if err := c.do(ctx, http.MethodGet, c.refPath("ref", branch), nil, &r); err != nil {
		return "", err
	}
	return r.Object.SHA, nil
}

// Branch is the result of CreateBranch / CreateBranchFrom.
type Branch struct {
	Name string
	SHA  string // current head of the branch
	// Created is false when the branch already existed (e.g. a retried
	// activity); the caller decides whether a different SHA is acceptable.
	Created bool
}

// lookupBranch returns the branch head SHA, or "" if the branch does not exist.
func (c *Client) lookupBranch(ctx context.Context, branch string) (string, error) {
	sha, err := c.BranchSHA(ctx, branch)
	if StatusCode(err) == 404 {
		return "", nil
	}
	return sha, err
}

// CreateBranch creates refs/heads/<branch> at sha, idempotently: if the ref
// already exists it is returned (Created=false) instead of failing, and an
// ambiguous or 422 create is re-checked before being retried.
func (c *Client) CreateBranch(ctx context.Context, branch, sha string) (*Branch, error) {
	if err := c.checkWritable(branch); err != nil {
		return nil, err
	}
	if sha == "" {
		return nil, errors.New("github: CreateBranch requires a sha")
	}
	existing, err := c.lookupBranch(ctx, branch)
	if err != nil {
		return nil, err
	}
	if existing != "" {
		return &Branch{Name: branch, SHA: existing}, nil
	}
	return c.createBranch(ctx, branch, sha)
}

func (c *Client) createBranch(ctx context.Context, branch, sha string) (*Branch, error) {
	res := &Branch{Name: branch, SHA: sha, Created: true}
	in := map[string]string{"ref": "refs/heads/" + branch, "sha": sha}
	err := c.createOnce(ctx,
		func() error { return c.doCreate(ctx, c.repoPath("git", "refs"), in, nil) },
		func() (bool, error) {
			found, err := c.lookupBranch(ctx, branch)
			if found != "" {
				res.SHA, res.Created = found, found == sha
			}
			return found != "", err
		},
		isUnprocessable) // 422 "Reference already exists": created concurrently or by a lost attempt
	if err != nil {
		return nil, err
	}
	return res, nil
}

// CreateBranchFrom creates branch at the current head of base. If branch
// already exists it is returned as-is (Created=false) without reading base.
func (c *Client) CreateBranchFrom(ctx context.Context, branch, base string) (*Branch, error) {
	if err := c.checkWritable(branch); err != nil {
		return nil, err
	}
	existing, err := c.lookupBranch(ctx, branch)
	if err != nil {
		return nil, err
	}
	if existing != "" {
		return &Branch{Name: branch, SHA: existing}, nil
	}
	if err := validRefName(base); err != nil {
		return nil, err
	}
	sha, err := c.BranchSHA(ctx, base)
	if err != nil {
		return nil, err
	}
	return c.createBranch(ctx, branch, sha)
}

// UpdateRef moves branch to sha. With force=false GitHub rejects non
// fast-forward updates (422), which is how concurrent pushes are detected.
func (c *Client) UpdateRef(ctx context.Context, branch, sha string, force bool) error {
	if err := c.checkWritable(branch); err != nil {
		return err
	}
	in := map[string]any{"sha": sha, "force": force}
	return c.do(ctx, http.MethodPatch, c.refPath("refs", branch), in, nil)
}

// File modes accepted in a FileChange.
const (
	ModeFile       = "100644"
	ModeExecutable = "100755"
	ModeSymlink    = "120000"
)

// FileChange is one path to write or delete in a push.
type FileChange struct {
	Path    string // repo-relative, forward slashes
	Content []byte // ignored when Delete
	Mode    string // default ModeFile
	Delete  bool
}

// Signature is a git author/committer. Zero value lets GitHub use the App identity.
type Signature struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// Push describes a single commit to add on top of Branch.
type Push struct {
	Branch  string
	Message string
	Changes []FileChange
	// ExpectedHeadSHA, if set, must equal the branch head before pushing
	// (optimistic concurrency); the ref update itself is always fast-forward.
	ExpectedHeadSHA string
	Author          *Signature
}

type treeEntry struct {
	Path string  `json:"path"`
	Mode string  `json:"mode"`
	Type string  `json:"type"`
	SHA  *string `json:"sha"` // null deletes the path
}

type shaResponse struct {
	SHA  string `json:"sha"`
	Tree struct {
		SHA string `json:"sha"`
	} `json:"tree"`
}

// ErrStaleHead is returned when Push.ExpectedHeadSHA does not match.
var ErrStaleHead = errors.New("github: branch head moved")

// PushFiles commits Changes onto Branch using the Git Data API:
// get ref -> get parent commit -> create blobs -> create tree (base_tree) ->
// create commit -> fast-forward ref. Returns the new commit SHA.
func (c *Client) PushFiles(ctx context.Context, p Push) (string, error) {
	if err := c.checkWritable(p.Branch); err != nil {
		return "", err
	}
	if strings.TrimSpace(p.Message) == "" {
		return "", errors.New("github: push requires a commit message")
	}
	if len(p.Changes) == 0 {
		return "", errors.New("github: push has no changes")
	}
	seen := map[string]bool{}
	for _, ch := range p.Changes {
		if err := validPath(ch.Path); err != nil {
			return "", err
		}
		if seen[ch.Path] {
			return "", fmt.Errorf("github: duplicate path %q in push", ch.Path)
		}
		seen[ch.Path] = true
		switch ch.Mode {
		case "", ModeFile, ModeExecutable, ModeSymlink:
		default:
			return "", fmt.Errorf("github: unsupported mode %q for %q", ch.Mode, ch.Path)
		}
	}

	parent, err := c.BranchSHA(ctx, p.Branch)
	if err != nil {
		return "", err
	}
	if p.ExpectedHeadSHA != "" && p.ExpectedHeadSHA != parent {
		return "", fmt.Errorf("%w: expected %s, found %s", ErrStaleHead, p.ExpectedHeadSHA, parent)
	}
	var pc shaResponse
	if err := c.do(ctx, http.MethodGet, c.repoPath("git", "commits", parent), nil, &pc); err != nil {
		return "", err
	}

	entries := make([]treeEntry, 0, len(p.Changes))
	for _, ch := range p.Changes {
		mode := ch.Mode
		if mode == "" {
			mode = ModeFile
		}
		e := treeEntry{Path: ch.Path, Mode: mode, Type: "blob"}
		if !ch.Delete {
			var blob shaResponse
			in := map[string]string{"content": base64.StdEncoding.EncodeToString(ch.Content), "encoding": "base64"}
			if err := c.do(ctx, http.MethodPost, c.repoPath("git", "blobs"), in, &blob); err != nil {
				return "", err
			}
			e.SHA = &blob.SHA
		}
		entries = append(entries, e)
	}

	var tree shaResponse
	if err := c.do(ctx, http.MethodPost, c.repoPath("git", "trees"),
		map[string]any{"base_tree": pc.Tree.SHA, "tree": entries}, &tree); err != nil {
		return "", err
	}

	commitIn := map[string]any{"message": p.Message, "tree": tree.SHA, "parents": []string{parent}}
	if p.Author != nil {
		commitIn["author"] = p.Author
	}
	var commit shaResponse
	if err := c.do(ctx, http.MethodPost, c.repoPath("git", "commits"), commitIn, &commit); err != nil {
		return "", err
	}
	if err := c.UpdateRef(ctx, p.Branch, commit.SHA, false); err != nil {
		return "", err
	}
	return commit.SHA, nil
}

func validPath(p string) error {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") || strings.Contains(p, "\x00") ||
		path.Clean(p) != p || p == "." || p == ".." || strings.HasPrefix(p, "../") {
		return fmt.Errorf("github: invalid path %q", p)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".git" {
			return fmt.Errorf("github: invalid path %q", p)
		}
	}
	return nil
}
