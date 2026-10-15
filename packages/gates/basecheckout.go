package gates

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// baseCheckout is a temporary, read-only-by-convention copy of the tree at the
// base SHA. It is produced with `git archive <sha>` and extracted into a fresh
// temp dir, so the agent's repository (working tree, index, refs and
// .git/worktrees) is never touched.
type baseCheckout struct {
	Dir string
	SHA string
}

// resolveCommit returns the full commit SHA for ref in repoDir.
func resolveCommit(ctx context.Context, repoDir, ref string) (string, error) {
	if err := validateRef(ref); err != nil {
		return "", err
	}
	out, err := runGit(ctx, repoDir, "rev-parse", "--verify", "--quiet", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("%w: %q in %s", ErrBaseRefNotFound, ref, repoDir)
	}
	return strings.TrimSpace(string(out)), nil
}

// newBaseCheckout extracts the tree of sha (from repoDir) into a new temp dir
// under parent ("" = os.TempDir). The caller must call remove().
func newBaseCheckout(ctx context.Context, repoDir, sha, parent string) (*baseCheckout, error) {
	dir, err := os.MkdirTemp(parent, "alpine-base-")
	if err != nil {
		return nil, err
	}
	bc := &baseCheckout{Dir: dir, SHA: sha}
	if err := extractArchive(ctx, repoDir, sha, dir); err != nil {
		bc.remove()
		return nil, fmt.Errorf("gates: base checkout of %s: %w", sha, err)
	}
	return bc, nil
}

func (b *baseCheckout) remove() {
	if b != nil && b.Dir != "" {
		_ = os.RemoveAll(b.Dir)
	}
}

func extractArchive(ctx context.Context, repoDir, sha, dest string) error {
	cmd := exec.CommandContext(ctx, "git", "-C", repoDir, "archive", "--format=tar", sha)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	extractErr := untar(stdout, dest)
	_, _ = io.Copy(io.Discard, stdout)
	waitErr := cmd.Wait()
	if extractErr != nil {
		return extractErr
	}
	if waitErr != nil {
		return fmt.Errorf("git archive: %v: %s", waitErr, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func untar(r io.Reader, dest string) error {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.FromSlash(h.Name)
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) || strings.Contains(name, string(filepath.Separator)+".."+string(filepath.Separator)) {
			return fmt.Errorf("unsafe path in archive: %q", h.Name)
		}
		target := filepath.Join(dest, name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(h.Mode)&0o777)
			if err != nil {
				return err
			}
			_, cerr := io.Copy(f, tr) // #nosec G110 -- content comes from the repo's own git objects
			if err := f.Close(); cerr == nil {
				cerr = err
			}
			if cerr != nil {
				return cerr
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(h.Linkname, target); err != nil {
				return err
			}
		default:
			// pax headers (commit id comment), etc.
		}
	}
}
