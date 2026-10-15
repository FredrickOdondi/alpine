package gates

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const strictConfig = `version: 1
rules:
  files: [AGENTS.md]
layers:
  BR1: ["src/**"]
gates:
  - id: lint
    parser: eslint
    run: pnpm eslint -f json src
    timeout: 5m
  - id: unit
    parser: go-test
    command: go
    args: [test, -json, ./...]
    working_dir: services/api
    timeout: 10m
  - id: dupes
    parser: jscpd
    run: npx jscpd --reporters json --output .jscpd src
    report_file: .jscpd/jscpd-report.json
    max_duplication_pct: 1.5
  - id: complexity
    parser: gocyclo
    run: gocyclo -over 10 .
    max_complexity: 10
  - id: visual
    routes: auto
limits:
  max_turns: 100
  max_gate_iterations: 7
gate_runner:
  run_timeout: 30m
  env_allowlist: [GOPATH, NPM_CONFIG_CACHE]
`

const weakConfig = `version: 1
gates:
  - id: lint
    run: "true"
limits:
  max_gate_iterations: 1000
`

// gitEnv isolates git from the host's global/system config.
func gitEnv() []string {
	return append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
	)
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = gitEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fixtureRepo creates a repo whose base branch "main" has the strict config,
// and an agent branch that committed a weaker config AND has an even weaker
// uncommitted working-tree edit.
func fixtureRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	writeFile(t, dir, ConfigPath, strictConfig)
	writeFile(t, dir, "README.md", "fixture\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "base config")
	git(t, dir, "checkout", "-q", "-b", "alpine/fix-1")
	writeFile(t, dir, ConfigPath, weakConfig)
	git(t, dir, "commit", "-q", "-am", "agent weakens gates")
	writeFile(t, dir, ConfigPath, "version: 1\ngates: []\n") // uncommitted, even weaker
	return dir
}

func TestLoadConfigFromBaseWinsOverWorkingTree(t *testing.T) {
	dir := fixtureRepo(t)
	ctx := context.Background()
	cfg, err := LoadConfigFromBase(ctx, dir, "main")
	if err != nil {
		t.Fatalf("LoadConfigFromBase: %v", err)
	}
	if len(cfg.Gates) != 5 {
		t.Fatalf("gates = %d, want 5 (base config)", len(cfg.Gates))
	}
	if got := cfg.GateRunCap(); got != 7 {
		t.Errorf("GateRunCap = %d, want 7 from base", got)
	}
	if got := cfg.RunTimeout(); got != 30*time.Minute {
		t.Errorf("RunTimeout = %v", got)
	}
	unit := cfg.Gates[1]
	if unit.Type != TypeTest || unit.Command != "go" || strings.Join(unit.Args, " ") != "test -json ./..." ||
		unit.WorkingDir != "services/api" || unit.EffectiveTimeout() != 10*time.Minute {
		t.Errorf("unit gate = %+v", unit)
	}
	if d := cfg.Gates[2]; d.Type != TypeDuplication || d.MaxDuplicationPct == nil || *d.MaxDuplicationPct != 1.5 || d.ReportFile == "" {
		t.Errorf("dupes gate = %+v", d)
	}
	if c := cfg.Gates[3]; c.Type != TypeComplexity || c.MaxComplexity != 10 || c.EffectiveTimeout() != DefaultGateTimeout {
		t.Errorf("complexity gate = %+v", c)
	}
	if v := cfg.Gates[4]; v.Type != TypeVisual {
		t.Errorf("visual gate type = %q", v.Type)
	}

	// Sanity: the agent branch really does carry the weak config.
	weak, err := LoadConfigFromBase(ctx, dir, "alpine/fix-1")
	if err != nil {
		t.Fatal(err)
	}
	if weak.GateRunCap() != 1000 || len(weak.Gates) != 1 {
		t.Fatalf("agent-branch config not as expected: %+v", weak)
	}
}

// newRepo creates a repo with one commit on "main" containing files.
func newRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	for rel, content := range files {
		writeFile(t, dir, rel, content)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "base")
	return dir
}

func TestLoadConfigFromBaseErrors(t *testing.T) {
	ctx := context.Background()

	// Config missing on the base branch, even though the agent created one in the working tree.
	dir := newRepo(t, map[string]string{"README.md": "x\n"})
	writeFile(t, dir, ConfigPath, weakConfig)
	_, err := LoadConfigFromBase(ctx, dir, "main")
	if !errors.Is(err, ErrConfigNotFound) {
		t.Fatalf("want ErrConfigNotFound, got %v", err)
	}
	if !strings.Contains(err.Error(), ".alpine/factory.yaml") {
		t.Errorf("error should name the file: %v", err)
	}

	// Bad / malicious refs.
	dir = fixtureRepo(t)
	for _, ref := range []string{"does-not-exist", "", "--output=/tmp/x", "main:README.md"} {
		if _, err := LoadConfigFromBase(ctx, dir, ref); !errors.Is(err, ErrBaseRefNotFound) {
			t.Errorf("ref %q: want ErrBaseRefNotFound, got %v", ref, err)
		}
	}

	// Invalid config on the base branch (typo in a gate key).
	dir = newRepo(t, map[string]string{ConfigPath: "version: 1\ngates:\n  - id: lint\n    run: x\n    timout: 5m\n"})
	if _, err := LoadConfigFromBase(ctx, dir, "main"); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("want ErrInvalidConfig for unknown gate key, got %v", err)
	}

	// Not a git repo.
	if _, err := LoadConfigFromBase(ctx, t.TempDir(), "main"); err == nil {
		t.Error("expected error outside a git repo")
	}
}

func TestParseConfigValidation(t *testing.T) {
	tests := []struct {
		name, yaml, wantErr string
	}{
		{"minimal ok", "gates:\n  - id: lint\n    run: make lint\n", ""},
		{"no gates ok", "version: 1\n", ""},
		{"disabled without command ok", "gates:\n  - id: lint\n    enabled: false\n", ""},
		{"missing id", "gates:\n  - run: x\n", "id is required"},
		{"duplicate id", "gates:\n  - {id: a, run: x}\n  - {id: a, run: y}\n", "duplicate id"},
		{"no command", "gates:\n  - id: a\n", "one of run or command"},
		{"both run and command", "gates:\n  - {id: a, run: x, command: y}\n", "only one of run or command"},
		{"args with run", "gates:\n  - {id: a, run: x, args: [y]}\n", "args are only valid"},
		{"unknown parser", "gates:\n  - {id: a, run: x, parser: pylint}\n", "unknown parser"},
		{"unknown type", "gates:\n  - {id: a, run: x, type: vibes}\n", "unknown type"},
		{"bad timeout", "gates:\n  - {id: a, run: x, timeout: soon}\n", "duration"},
		{"escaping working dir", "gates:\n  - {id: a, run: x, working_dir: ../other}\n", "must not contain '..'"},
		{"absolute report", "gates:\n  - {id: a, run: x, report_file: /etc/passwd}\n", "relative"},
		{"bad pct", "gates:\n  - {id: a, run: x, max_duplication_pct: 150}\n", "0..100"},
		{"bad version", "version: 2\n", "unsupported version"},
		{"negative cap", "limits:\n  max_gate_iterations: -1\n", "max_gate_iterations"},
		{"not yaml", "gates: [", "invalid config"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseConfig([]byte(tc.yaml))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want ErrInvalidConfig containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestConfigDefaultsAndTypeInference(t *testing.T) {
	cfg, err := ParseConfig([]byte(`gates:
  - {id: a, parser: eslint, run: x}
  - {id: b, parser: tsc, run: x}
  - {id: c, parser: vitest, run: x}
  - {id: d, parser: knip, run: x}
  - {id: e, parser: deadcode, run: x}
  - {id: complexity, run: x}
  - {id: custom, run: x}
  - {id: f, parser: eslint, type: complexity, run: x, only_rules: [complexity]}
`))
	if err != nil {
		t.Fatal(err)
	}
	want := []GateType{TypeLint, TypeTypecheck, TypeTest, TypeDeadCode, TypeDeadCode, TypeComplexity, TypeOther, TypeComplexity}
	for i, g := range cfg.Gates {
		if g.Type != want[i] {
			t.Errorf("gate %s type = %q, want %q", g.ID, g.Type, want[i])
		}
		if !g.IsEnabled() {
			t.Errorf("gate %s should default to enabled", g.ID)
		}
	}
	if cfg.GateRunCap() != DefaultGateRunCap || DefaultGateRunCap != 15 {
		t.Errorf("default cap = %d", cfg.GateRunCap())
	}
	if cfg.RunTimeout() != DefaultRunTimeout || cfg.Gates[0].EffectiveTimeout() != DefaultGateTimeout {
		t.Error("default timeouts not applied")
	}
}
