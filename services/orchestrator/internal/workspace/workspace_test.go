package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitInit(t *testing.T) string {
	t.Helper()
	origin := t.TempDir()
	run := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run(origin, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(origin, "a.go"), []byte("package a\n\nfunc A() int { return 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(origin, "add", "-A")
	run(origin, "commit", "-qm", "init")
	return origin
}

func TestCheckoutToolsAndConfinement(t *testing.T) {
	ctx := context.Background()
	origin := gitInit(t)
	m := &Manager{Root: t.TempDir(), CloneURL: func(string) string { return origin }}
	co, err := m.Open(ctx, "01J00000000000000000000000", "o/r", "alpine/01J00000000000000000000000-x", "main")
	if err != nil {
		t.Fatal(err)
	}
	if err := co.ReplaceInFile(ctx, "a.go", "return 1", "return 2"); err != nil {
		t.Fatal(err)
	}
	if got, _ := co.ReadFile(ctx, "a.go"); !strings.Contains(got, "return 2") {
		t.Fatalf("replace failed: %s", got)
	}
	if out, _ := co.Search(ctx, "func A", "."); !strings.Contains(out, "a.go:3") {
		t.Fatalf("search: %q", out)
	}
	if files, _ := co.ListFiles(ctx, "."); len(files) != 1 || files[0] != "a.go" {
		t.Fatalf("list: %v", files)
	}
	for _, p := range []string{"../x", "/etc/passwd", ".git/config"} {
		if _, err := co.ReadFile(ctx, p); err == nil {
			t.Errorf("read %s should be refused", p)
		}
	}
	if err := co.WriteFile(ctx, ".alpine/factory.yaml", "x"); err == nil {
		t.Error("write to .alpine should be refused")
	}
	if err := os.Symlink("/etc", filepath.Join(co.Dir(), "esc")); err != nil {
		t.Fatal(err)
	}
	if _, err := co.ReadFile(ctx, "esc/passwd"); err == nil {
		t.Error("symlink escape should be refused")
	}
	_ = os.Remove(filepath.Join(co.Dir(), "esc"))
	base, _ := co.BaseSHA(ctx)
	head, changed, err := co.Commit(ctx, "change")
	if err != nil || !changed || head == base {
		t.Fatalf("commit: %v %v %s %s", err, changed, head, base)
	}
	d, err := Diff(ctx, m, co.Dir(), base, head, 0)
	if err != nil || !strings.Contains(d, "+func A() int { return 2 }") {
		t.Fatalf("diff: %v %s", err, d)
	}
	if _, err := co.RunCommand(ctx, "true"); err == nil {
		t.Error("commands must be refused without a sandbox runner")
	}
}
