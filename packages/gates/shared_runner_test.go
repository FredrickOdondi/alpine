package gates

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/alpine-dev/alpine/packages/shared/gate"
)

func headSHA(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "rev-parse", "HEAD")
	cmd.Env = gitEnv()
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func TestSharedRunnerUsesBaseConfig(t *testing.T) {
	// Base: a gate that fails and a cap of 2. Agent branch: gate "fixed" to pass, cap raised.
	dir := newRepo(t, map[string]string{ConfigPath: "version: 1\nlimits: {max_gate_iterations: 2}\ngates:\n  - id: unit\n    run: echo base-gate-ran >&2; exit 1\n"})
	git(t, dir, "checkout", "-q", "-b", "alpine/fix")
	writeFile(t, dir, ConfigPath, "version: 1\nlimits: {max_gate_iterations: 99}\ngates:\n  - id: unit\n    run: \"true\"\n")
	git(t, dir, "commit", "-q", "-am", "weaken")
	head := headSHA(t, dir)

	// req.RepoDir is preferred: the struct fallback points at a directory that
	// is not a repo, so any use of it would fail the run.
	sr := SharedRunner{Runner: NewRunner(nil), RepoDir: t.TempDir()}
	req := gate.RunRequest{RunID: "run-1", BaseSHA: "main", HeadSHA: head, RepoDir: dir,
		Gates: []gate.Spec{{ID: "unit", Run: "true"}}} // ignored: config comes from BaseSHA
	rep, err := sr.Run(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Green() || rep.Iteration != 1 || len(rep.Results) != 1 || rep.Results[0].Status != gate.StatusFail {
		t.Fatalf("base config should have failed the run: %+v", rep)
	}
	if !strings.Contains(rep.Results[0].Findings[0].Message, "base-gate-ran") {
		t.Errorf("finding = %+v", rep.Results[0].Findings)
	}
	if _, err := sr.Run(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := sr.Run(context.Background(), req); !errors.Is(err, ErrGateRunCapReached) {
		t.Errorf("third run: want cap from base config (2), got %v", err)
	}

	req.HeadSHA = "0000000"
	if _, err := sr.Run(context.Background(), req); err == nil || !strings.Contains(err.Error(), "want head") {
		t.Errorf("head mismatch should error, got %v", err)
	}
	if _, err := sr.Run(context.Background(), gate.RunRequest{RunID: "x", RepoDir: dir}); !errors.Is(err, ErrBaseRefNotFound) {
		t.Errorf("missing BaseSHA: %v", err)
	}
}

func TestSharedRunnerRepoDirFallback(t *testing.T) {
	dir := newRepo(t, map[string]string{ConfigPath: "version: 1\ngates:\n  - id: unit\n    run: echo ran-in-fallback >&2; exit 1\n"})
	head := headSHA(t, dir)
	ctx := context.Background()

	// Empty req.RepoDir: the struct RepoDir is used for the HEAD check, the
	// base config and the gate commands.
	sr := SharedRunner{Runner: NewRunner(nil), RepoDir: dir}
	rep, err := sr.Run(ctx, gate.RunRequest{RunID: "fb", BaseSHA: "main", HeadSHA: head})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Results) != 1 || rep.Results[0].Status != gate.StatusFail ||
		!strings.Contains(rep.Results[0].Findings[0].Message, "ran-in-fallback") {
		t.Fatalf("fallback RepoDir not used: %+v", rep)
	}

	// Both empty: a clear error, checked before any git call or gate run.
	empty := SharedRunner{Runner: NewRunner(nil)}
	_, err = empty.Run(ctx, gate.RunRequest{RunID: "none", BaseSHA: "main", HeadSHA: head})
	if !errors.Is(err, ErrRepoDirRequired) || !strings.Contains(err.Error(), "RepoDir is required") {
		t.Errorf("both RepoDirs empty: want ErrRepoDirRequired, got %v", err)
	}
	// A missing BaseSHA is still reported first.
	if _, err := empty.Run(ctx, gate.RunRequest{RunID: "none"}); !errors.Is(err, ErrBaseRefNotFound) {
		t.Errorf("missing BaseSHA with no RepoDir: %v", err)
	}
}

func TestResultJSONIsSupersetOfSharedShape(t *testing.T) {
	gr := GateResult{Result: gate.Result{GateID: "lint", Status: StatusFail, Duration: 1500 * time.Millisecond,
		Findings: []Finding{{File: "a.go", Line: 1, Rule: "x", Severity: SeverityError, Message: "m"}}, Summary: "1 error"},
		Type: TypeLint, Parser: "golangci-lint", ExitCode: 1}
	b, err := json.Marshal(gr)
	if err != nil {
		t.Fatal(err)
	}
	var shared gate.Result
	if err := json.Unmarshal(b, &shared); err != nil {
		t.Fatal(err)
	}
	if shared.GateID != "lint" || shared.Status != gate.StatusFail || shared.Duration != 1500*time.Millisecond || len(shared.Findings) != 1 {
		t.Errorf("shared decode = %+v from %s", shared, b)
	}
	for _, k := range []string{`"gateId"`, `"durationNs"`, `"findings"`, `"type":"lint"`, `"exitCode":1`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("JSON missing %s: %s", k, b)
		}
	}
	rr := RunResult{GateRunsUsed: 3, Gates: []GateResult{gr}}
	if rep := rr.Report(); rep.Iteration != 3 || len(rep.Results) != 1 || rep.Green() {
		t.Errorf("Report() = %+v", rep)
	}
}

func TestSharedSpecFieldsInConfig(t *testing.T) {
	cfg, err := ParseConfig([]byte(`gates:
  - id: dupes
    parser: jscpd
    run: npx jscpd
    report_file: r.json
    ratchet: true
    max_new_duplication_pct: 0.5
    timeout_seconds: 90
  - id: visual
    routes: auto
    artifacts: ["test-results/**"]
`))
	if err != nil {
		t.Fatal(err)
	}
	d := cfg.Gates[0]
	if d.EffectiveTimeout() != 90*time.Second || !d.Ratchet || !d.RatchetEnabled() || d.MaxNewDuplicationPct == nil || *d.MaxNewDuplicationPct != 0.5 || d.MaxDuplicationPct != nil {
		t.Errorf("dupes = %+v", d)
	}
	if v := cfg.Gates[1]; v.Type != TypeVisual || v.Routes != "auto" || len(v.Artifacts) != 1 {
		t.Errorf("visual = %+v", v)
	}
	if _, err := ParseConfig([]byte("gates:\n  - {id: a, run: x, timeout: 1m, timeout_seconds: 60}\n")); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("both timeouts should be rejected, got %v", err)
	}
}
