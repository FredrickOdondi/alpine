// Package workspace gives the live implementer a per-ticket git checkout on the worker host plus a
// locked-down Docker sandbox (no network, dropped capabilities) with the checkout bind-mounted at
// /work. File tools run on the host with strict path confinement; commands (tests, builds) run
// only inside the sandbox. Gates then run Lena's SharedRunner on the same checkout.
package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/alpine-dev/alpine/packages/agents"
	"github.com/alpine-dev/alpine/services/runner"
)

// Manager opens per-ticket checkouts.
type Manager struct {
	Root string // host directory holding one checkout per ticket ULID
	// CloneURL maps "owner/repo" to a git URL (default https://github.com/<repo>.git).
	CloneURL func(repo string) string
	// GitConfig are extra `git -c` settings for clone/fetch/push (e.g. the gh credential helper).
	GitConfig []string
	// Runner and Image create the command sandbox (nil Runner: commands are refused).
	Runner runner.Runner
	Image  string
	Env    map[string]string // sandbox env (e.g. GOCACHE, GOFLAGS)
	// CommandTimeout bounds one run_command (default 3m).
	CommandTimeout time.Duration
	// Author is the commit identity ("Name <email>").
	Author string
}

// Checkout is one ticket's working tree (and, once Sandbox is called, its sandbox).
type Checkout struct {
	m    *Manager
	dir  string
	base string // base branch name
	sb   runner.Sandbox
}

var _ agents.Workspace = (*Checkout)(nil)

func (m *Manager) git(ctx context.Context, dir string, args ...string) (string, error) {
	full := []string{}
	for _, c := range m.GitConfig {
		full = append(full, "-c", c)
	}
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// Open returns the checkout for ticketULID, cloning repo and creating branch from origin/<base>
// on first use. Later calls (gate or review feedback rounds, activity retries) reuse the tree.
func (m *Manager) Open(ctx context.Context, ticketULID, repo, branch, base string) (*Checkout, error) {
	if !regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`).MatchString(repo) {
		return nil, fmt.Errorf("workspace: bad repo name %q", repo)
	}
	if err := os.MkdirAll(m.Root, 0o755); err != nil {
		return nil, err
	}
	dir := filepath.Join(m.Root, ticketULID)
	c := &Checkout{m: m, dir: dir, base: base}
	if _, err := os.Stat(filepath.Join(dir, ".git")); errors.Is(err, os.ErrNotExist) {
		url := "https://github.com/" + repo + ".git"
		if m.CloneURL != nil {
			url = m.CloneURL(repo)
		}
		_ = os.RemoveAll(dir)
		if _, err := m.git(ctx, m.Root, "clone", "--no-tags", "--branch", base, url, dir); err != nil {
			return nil, err
		}
		if _, err := m.git(ctx, dir, "checkout", "-B", branch, "origin/"+base); err != nil {
			return nil, err
		}
	} else {
		if _, err := m.git(ctx, dir, "fetch", "--no-tags", "origin", base); err != nil {
			return nil, err
		}
		if cur, _ := m.git(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD"); strings.TrimSpace(cur) != branch {
			if _, err := m.git(ctx, dir, "checkout", branch); err != nil {
				return nil, err
			}
		}
	}
	return c, nil
}

// Dir is the host path of the checkout (gates run here).
func (c *Checkout) Dir() string { return c.dir }

// BaseSHA is the commit the work branch was cut from.
func (c *Checkout) BaseSHA(ctx context.Context) (string, error) {
	out, err := c.m.git(ctx, c.dir, "merge-base", "HEAD", "origin/"+c.base)
	return strings.TrimSpace(out), err
}

// HeadSHA is the current commit.
func (c *Checkout) HeadSHA(ctx context.Context) (string, error) {
	out, err := c.m.git(ctx, c.dir, "rev-parse", "HEAD")
	return strings.TrimSpace(out), err
}

// Commit stages everything and commits; changed=false when there is nothing to commit.
func (c *Checkout) Commit(ctx context.Context, msg string) (sha string, changed bool, err error) {
	if _, err := c.m.git(ctx, c.dir, "add", "-A"); err != nil {
		return "", false, err
	}
	if _, err := c.m.git(ctx, c.dir, "diff", "--cached", "--quiet"); err == nil {
		sha, err := c.HeadSHA(ctx)
		return sha, false, err
	}
	author := c.m.Author
	if author == "" {
		author = "Alpine Bot <alpine-bot@users.noreply.github.com>"
	}
	name, email, _ := strings.Cut(strings.TrimSuffix(author, ">"), " <")
	if _, err := c.m.git(ctx, c.dir, "-c", "user.name="+name, "-c", "user.email="+email, "commit", "-q", "-m", msg); err != nil {
		return "", false, err
	}
	sha, err = c.HeadSHA(ctx)
	return sha, true, err
}

// Push pushes the work branch to origin.
func (c *Checkout) Push(ctx context.Context, branch string) error {
	_, err := c.m.git(ctx, c.dir, "push", "-q", "origin", "HEAD:refs/heads/"+branch)
	return err
}

// Diff returns `git diff base..head` (capped).
func (c *Checkout) Diff(ctx context.Context, base, head string, maxBytes int) (string, error) {
	return Diff(ctx, c.m, c.dir, base, head, maxBytes)
}

// Diff returns `git diff base..head` for dir, truncated to maxBytes.
func Diff(ctx context.Context, m *Manager, dir, base, head string, maxBytes int) (string, error) {
	out, err := m.git(ctx, dir, "diff", "--stat", "--patch", base+".."+head)
	if err != nil {
		return "", err
	}
	if maxBytes > 0 && len(out) > maxBytes {
		out = out[:maxBytes] + "\n…[diff truncated]"
	}
	return out, nil
}

// Close stops the sandbox (the checkout stays for gates, review and later rounds).
func (c *Checkout) Close(ctx context.Context) error {
	if c.sb == nil {
		return nil
	}
	err := c.sb.Close(ctx)
	c.sb = nil
	return err
}

// ---- path confinement -----------------------------------------------------------------------

var errOutside = errors.New("path is outside the repository")

// resolve maps a repo-relative path to a host path inside the checkout, refusing absolute paths,
// "..", symlink escapes, and (for writes) .git/ and .alpine/.
func (c *Checkout) resolve(p string, write bool) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" || p == "." || p == "./" {
		p = "."
	}
	if filepath.IsAbs(p) {
		p = strings.TrimPrefix(p, "/work/")
		if filepath.IsAbs(p) {
			return "", errOutside
		}
	}
	clean := filepath.Clean(p)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errOutside
	}
	first := strings.SplitN(clean, string(filepath.Separator), 2)[0]
	if first == ".git" || (write && first == ".alpine") {
		return "", fmt.Errorf("%s is protected", first+"/")
	}
	full := filepath.Join(c.dir, clean)
	// Resolve symlinks on the deepest existing ancestor and require it to stay inside the tree.
	probe := full
	for {
		if _, err := os.Lstat(probe); err == nil {
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			break
		}
		probe = parent
	}
	real, err := filepath.EvalSymlinks(probe)
	if err != nil {
		return "", err
	}
	root, err := filepath.EvalSymlinks(c.dir)
	if err != nil {
		return "", err
	}
	if real != root && !strings.HasPrefix(real, root+string(filepath.Separator)) {
		return "", errOutside
	}
	return full, nil
}

const maxFileBytes = 256 << 10

// ReadFile implements agents.Workspace.
func (c *Checkout) ReadFile(_ context.Context, p string) (string, error) {
	full, err := c.resolve(p, false)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(full)
	if err != nil {
		return "", err
	}
	if st.IsDir() {
		return "", errors.New("is a directory; use list_files")
	}
	if st.Size() > maxFileBytes {
		return "", fmt.Errorf("file too large (%d bytes)", st.Size())
	}
	b, err := os.ReadFile(full)
	return string(b), err
}

// WriteFile implements agents.Workspace.
func (c *Checkout) WriteFile(_ context.Context, p, content string) error {
	full, err := c.resolve(p, true)
	if err != nil {
		return err
	}
	if len(content) > maxFileBytes {
		return errors.New("content too large")
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, []byte(content), 0o644)
}

// ReplaceInFile implements agents.Workspace.
func (c *Checkout) ReplaceInFile(ctx context.Context, p, oldText, newText string) error {
	cur, err := c.ReadFile(ctx, p)
	if err != nil {
		return err
	}
	if oldText == "" {
		return errors.New("old_text is empty")
	}
	switch n := strings.Count(cur, oldText); n {
	case 0:
		return errors.New("old_text not found (it must match exactly, including whitespace)")
	case 1:
	default:
		return fmt.Errorf("old_text matches %d times; include more context to make it unique", n)
	}
	return c.WriteFile(ctx, p, strings.Replace(cur, oldText, newText, 1))
}

// ListFiles implements agents.Workspace (tracked + untracked, not ignored).
func (c *Checkout) ListFiles(ctx context.Context, dir string) ([]string, error) {
	full, err := c.resolve(dir, false)
	if err != nil {
		return nil, err
	}
	rel, _ := filepath.Rel(c.dir, full)
	out, err := c.m.git(ctx, c.dir, "ls-files", "-co", "--exclude-standard", "--", rel)
	if err != nil {
		return nil, err
	}
	files := strings.Split(strings.TrimSpace(out), "\n")
	if len(files) == 1 && files[0] == "" {
		return nil, nil
	}
	if len(files) > 300 {
		files = append(files[:300], fmt.Sprintf("… %d more", len(files)-300))
	}
	return files, nil
}

// Search implements agents.Workspace (git grep, extended regex).
func (c *Checkout) Search(ctx context.Context, pattern, dir string) (string, error) {
	full, err := c.resolve(dir, false)
	if err != nil {
		return "", err
	}
	rel, _ := filepath.Rel(c.dir, full)
	out, err := c.m.git(ctx, c.dir, "grep", "--untracked", "-nIE", "-e", pattern, "--", rel)
	if err != nil && out == "" {
		if strings.Contains(err.Error(), "exit status 1") {
			return "(no matches)", nil
		}
		return "", err
	}
	return out, nil
}

// RunCommand implements agents.Workspace: the command runs in the offline sandbox at /work.
func (c *Checkout) RunCommand(ctx context.Context, command string) (agents.CommandResult, error) {
	if c.m.Runner == nil {
		return agents.CommandResult{}, errors.New("no sandbox runner configured")
	}
	if c.sb == nil {
		spec := runner.Spec{
			Template: c.m.Image, Timeout: time.Hour, Workdir: "/work", Env: c.m.Env, CPUs: 2, MemoryMB: 2048,
			Mounts: []runner.Mount{{Source: c.dir, Target: "/work"}},
			User:   fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
			Labels: map[string]string{"role": "implementer", "ticket": filepath.Base(c.dir)},
		}
		sb, err := c.m.Runner.Create(ctx, spec)
		if err != nil {
			return agents.CommandResult{}, fmt.Errorf("create sandbox: %w", err)
		}
		c.sb = sb
	}
	timeout := c.m.CommandTimeout
	if timeout <= 0 {
		timeout = 3 * time.Minute
	}
	secs := int(timeout.Seconds())
	r, err := c.sb.Exec(ctx, runner.Command{Cmd: "sh", Args: []string{"-c", fmt.Sprintf("timeout %d sh -c %s 2>&1", secs, shellQuote(command))},
		Timeout: timeout + 10*time.Second})
	if err != nil {
		return agents.CommandResult{}, err
	}
	out := string(r.Stdout) + string(r.Stderr)
	if r.ExitCode == 124 {
		out += fmt.Sprintf("\n(command timed out after %ds)", secs)
	}
	return agents.CommandResult{ExitCode: r.ExitCode, Output: out}, nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
