package gates

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alpine-dev/alpine/packages/shared/gate"
)

// fake returns the absolute path of a fake tool script in testdata/fake.
func fake(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("testdata", "fake", name))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// sample returns the absolute path of a parser sample in testdata/parsers.
func sample(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("testdata", "parsers", name))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// emitGate builds a gate that runs emit.sh with a canned output and exit code.
func emitGate(t *testing.T, id, parser, sampleFile, exitCode string) GateConfig {
	return GateConfig{Spec: gate.Spec{ID: id}, Parser: parser, Command: "sh", Args: []string{fake(t, "emit.sh"), sample(t, sampleFile), exitCode}}
}

func mustConfig(t *testing.T, cfg Config) Config {
	t.Helper()
	if err := cfg.normalizeAndValidate(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func run(t *testing.T, r *Runner, cfg Config) RunResult {
	t.Helper()
	res, err := r.Run(context.Background(), RunRequest{JobID: "job-1", RepoDir: t.TempDir(), BaseRef: "main", Config: mustConfig(t, cfg)})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func ptr[T any](v T) *T { return &v }

func TestRunnerGateOutcomes(t *testing.T) {
	tests := []struct {
		name       string
		gate       func(t *testing.T) GateConfig
		wantStatus Status
		wantRun    Status
		wantRule   string // a finding with this rule must exist
		wantFB     string // substring of gate feedback
		wantExit   int
	}{
		{name: "eslint pass", gate: func(t *testing.T) GateConfig { return emitGate(t, "lint", "eslint", "eslint_pass.json", "0") },
			wantStatus: StatusPass, wantRun: StatusPass, wantFB: "lint (eslint): PASS"},
		{name: "eslint fail", gate: func(t *testing.T) GateConfig { return emitGate(t, "lint", "eslint", "eslint_fail.json", "1") },
			wantStatus: StatusFail, wantRun: StatusFail, wantRule: "@typescript-eslint/no-unused-vars", wantFB: "[@typescript-eslint/no-unused-vars] 'unused' is assigned a value but never used.", wantExit: 1},
		{name: "tsc fail via shell run", gate: func(t *testing.T) GateConfig {
			return GateConfig{Spec: gate.Spec{ID: "typecheck", Run: "cat '" + sample(t, "tsc_fail.txt") + "'; exit 2"}, Parser: "tsc"}
		}, wantStatus: StatusFail, wantRun: StatusFail, wantRule: "TS2322", wantExit: 2},
		{name: "go test pass", gate: func(t *testing.T) GateConfig { return emitGate(t, "unit", "go-test", "gotest_pass.jsonl", "0") },
			wantStatus: StatusPass, wantRun: StatusPass},
		{name: "go test fail", gate: func(t *testing.T) GateConfig { return emitGate(t, "unit", "go-test", "gotest_fail.jsonl", "1") },
			wantStatus: StatusFail, wantRun: StatusFail, wantRule: "TestDiv/by_zero", wantFB: "calc_test.go:18", wantExit: 1},
		{name: "nonzero exit with clean output still fails", gate: func(t *testing.T) GateConfig {
			return emitGate(t, "lint", "eslint", "eslint_pass.json", "2")
		}, wantStatus: StatusFail, wantRun: StatusFail, wantRule: "exit-code", wantExit: 2},
		{name: "no parser uses exit code", gate: func(t *testing.T) GateConfig {
			return GateConfig{Spec: gate.Spec{ID: "custom", Run: "echo boom >&2; exit 3"}}
		}, wantStatus: StatusFail, wantRun: StatusFail, wantRule: "exit-code", wantFB: "boom", wantExit: 3},
		{name: "unparseable output with exit 0 is an error", gate: func(t *testing.T) GateConfig {
			return GateConfig{Spec: gate.Spec{ID: "lint", Run: "echo not json"}, Parser: "eslint"}
		}, wantStatus: StatusError, wantRun: StatusError, wantRule: "parse-error"},
		{name: "unparseable output with exit 1 fails with output tail", gate: func(t *testing.T) GateConfig {
			return GateConfig{Spec: gate.Spec{ID: "lint", Run: "echo 'ESLint couldn'\\''t find a config'; exit 2"}, Parser: "eslint"}
		}, wantStatus: StatusFail, wantRun: StatusFail, wantRule: "exit-code", wantFB: "find a config", wantExit: 2},
		{name: "command not found is an error", gate: func(t *testing.T) GateConfig {
			return GateConfig{Spec: gate.Spec{ID: "lint"}, Parser: "eslint", Command: "definitely-not-a-real-binary-alpine"}
		}, wantStatus: StatusError, wantRun: StatusError, wantRule: "start", wantExit: -1},
		{name: "warn_only downgrades failure", gate: func(t *testing.T) GateConfig {
			g := emitGate(t, "deadcode", "knip", "knip.json", "1")
			g.WarnOnly = true
			return g
		}, wantStatus: StatusWarn, wantRun: StatusPass, wantRule: "files", wantExit: 1},
		{name: "disabled gate is skipped", gate: func(t *testing.T) GateConfig {
			g := emitGate(t, "lint", "eslint", "eslint_fail.json", "1")
			g.Enabled = ptr(false)
			return g
		}, wantStatus: StatusSkipped, wantRun: StatusPass, wantExit: -1},
		{name: "visual gate is skipped", gate: func(t *testing.T) GateConfig { return GateConfig{Spec: gate.Spec{ID: "visual"}, Type: TypeVisual} },
			wantStatus: StatusSkipped, wantRun: StatusPass, wantExit: -1},
		{name: "gocyclo threshold ignores tool exit code", gate: func(t *testing.T) GateConfig {
			g := emitGate(t, "complexity", "gocyclo", "gocyclo.txt", "1")
			g.MaxComplexity = 30
			return g
		}, wantStatus: StatusPass, wantRun: StatusPass, wantExit: 1},
		{name: "gocyclo over threshold fails", gate: func(t *testing.T) GateConfig {
			g := emitGate(t, "complexity", "gocyclo", "gocyclo.txt", "1")
			g.MaxComplexity = 10
			return g
		}, wantStatus: StatusFail, wantRun: StatusFail, wantRule: "gocyclo", wantFB: "complexity 14 (max 10)", wantExit: 1},
		{name: "eslint complexity gate keeps only complexity rule", gate: func(t *testing.T) GateConfig {
			g := emitGate(t, "complexity", "eslint", "eslint_npm_banner.txt", "1")
			g.Type, g.OnlyRules = TypeComplexity, []string{"complexity"}
			return g
		}, wantStatus: StatusFail, wantRun: StatusFail, wantRule: "complexity", wantExit: 1},
		{name: "max_warnings exceeded", gate: func(t *testing.T) GateConfig {
			g := emitGate(t, "lint", "eslint", "eslint_fail.json", "1")
			g.OnlyRules = []string{"eqeqeq"} // leaves a single warning
			g.MaxWarnings = ptr(0)
			return g
		}, wantStatus: StatusFail, wantRun: StatusFail, wantRule: "max-warnings", wantExit: 1},
		{name: "base ref is exported to the gate", gate: func(t *testing.T) GateConfig {
			return GateConfig{Spec: gate.Spec{ID: "custom", Run: `test "$ALPINE_BASE_REF" = main && test "$ALPINE_GATE_ID" = custom && test "$EXTRA" = yes`}, Env: map[string]string{"EXTRA": "yes"}}
		}, wantStatus: StatusPass, wantRun: StatusPass},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := run(t, NewRunner(nil), Config{Gates: []GateConfig{tc.gate(t)}})
			if len(res.Gates) != 1 {
				t.Fatalf("gates = %d", len(res.Gates))
			}
			g := res.Gates[0]
			if g.Status != tc.wantStatus {
				t.Errorf("gate status = %s, want %s; findings=%+v", g.Status, tc.wantStatus, g.Findings)
			}
			if res.Status != tc.wantRun {
				t.Errorf("run status = %s, want %s", res.Status, tc.wantRun)
			}
			if g.ExitCode != tc.wantExit {
				t.Errorf("exit code = %d, want %d", g.ExitCode, tc.wantExit)
			}
			if tc.wantRule != "" && !hasRule(g.Findings, tc.wantRule) {
				t.Errorf("no finding with rule %q: %+v", tc.wantRule, g.Findings)
			}
			if !strings.Contains(g.Feedback, tc.wantFB) {
				t.Errorf("feedback %q does not contain %q", g.Feedback, tc.wantFB)
			}
			if g.Findings == nil {
				t.Error("Findings should be an empty slice, not nil (JSON [])")
			}
		})
	}
}

func hasRule(fs []Finding, rule string) bool {
	for _, f := range fs {
		if f.Rule == rule {
			return true
		}
	}
	return false
}

func TestRunnerTimeoutKillsProcessGroup(t *testing.T) {
	g := GateConfig{Spec: gate.Spec{ID: "unit"}, Parser: "go-test", Command: "sh", Args: []string{fake(t, "hang.sh")}, Timeout: Duration(300 * time.Millisecond)}
	start := time.Now()
	res := run(t, NewRunner(nil), Config{Gates: []GateConfig{g}})
	elapsed := time.Since(start)
	gr := res.Gates[0]
	if !gr.TimedOut || gr.Status != StatusFail || res.Status != StatusFail {
		t.Fatalf("want timed-out failure, got %+v", gr)
	}
	if !hasRule(gr.Findings, "timeout") || !strings.Contains(gr.Feedback, "exceeded its timeout of 300ms") {
		t.Errorf("missing timeout finding/feedback: %+v", gr)
	}
	// The background `sleep 30` child holds stdout open; only killing the whole
	// process group lets Wait return well before WaitDelay (3s).
	if elapsed > 2500*time.Millisecond {
		t.Errorf("gate took %v; process group was not killed promptly", elapsed)
	}
}

func TestRunnerWholeRunTimeout(t *testing.T) {
	cfg := Config{
		Runner: RunnerSettings{RunTimeout: Duration(300 * time.Millisecond)},
		Gates: []GateConfig{
			{Spec: gate.Spec{ID: "lint"}, Command: "sh", Args: []string{fake(t, "hang.sh")}},
			{Spec: gate.Spec{ID: "typecheck", Run: "true"}},
		},
	}
	res := run(t, NewRunner(nil), cfg)
	if res.Gates[0].Status != StatusFail || !res.Gates[0].TimedOut {
		t.Errorf("first gate: %+v", res.Gates[0])
	}
	if res.Gates[1].Status != StatusError || !hasRule(res.Gates[1].Findings, "run-timeout") {
		t.Errorf("second gate should not run: %+v", res.Gates[1])
	}
}

func TestRunnerCapReached(t *testing.T) {
	repo := t.TempDir()
	cfg := mustConfig(t, Config{
		Limits: Limits{MaxGateIterations: 2},
		Gates:  []GateConfig{{Spec: gate.Spec{ID: "custom"}, Command: "sh", Args: []string{fake(t, "count.sh")}}},
	})
	r := NewRunner(NewMemoryCounter())
	req := RunRequest{JobID: "job-cap", RepoDir: repo, BaseRef: "main", Config: cfg}
	for i := 1; i <= 2; i++ {
		res, err := r.Run(context.Background(), req)
		if err != nil || res.GateRunsUsed != i || res.GateRunCap != 2 || res.Status != StatusPass {
			t.Fatalf("run %d: res=%+v err=%v", i, res, err)
		}
	}
	res, err := r.Run(context.Background(), req)
	if !errors.Is(err, ErrGateRunCapReached) {
		t.Fatalf("want ErrGateRunCapReached, got %v", err)
	}
	if !res.CapReached || res.Status != StatusSkipped || len(res.Gates) != 0 || res.GateRunsUsed != 2 {
		t.Errorf("cap result = %+v", res)
	}
	if !strings.Contains(res.Feedback, "cap reached (2/2)") {
		t.Errorf("feedback = %q", res.Feedback)
	}
	b, _ := os.ReadFile(filepath.Join(repo, "invocations.txt"))
	if n := strings.Count(string(b), "run"); n != 2 {
		t.Errorf("gate command ran %d times, want 2 (must not run once the cap is reached)", n)
	}
	// Another job has its own budget.
	req.JobID = "job-other"
	if _, err := r.Run(context.Background(), req); err != nil {
		t.Errorf("other job: %v", err)
	}
}

func TestRunnerDefaultCapIs15(t *testing.T) {
	r := NewRunner(nil)
	cfg := Config{Gates: []GateConfig{{Spec: gate.Spec{ID: "custom", Run: "true"}}}}
	var err error
	n := 0
	for ; n < 20; n++ {
		if _, err = r.Run(context.Background(), RunRequest{JobID: "j", RepoDir: t.TempDir(), Config: mustConfig(t, cfg)}); err != nil {
			break
		}
	}
	if n != 15 || !errors.Is(err, ErrGateRunCapReached) {
		t.Errorf("ran %d times before cap (err=%v), want 15", n, err)
	}
}

func TestRunnerReportFileAndDuplicationThreshold(t *testing.T) {
	mk := func(maxPct *float64) GateConfig {
		return GateConfig{Spec: gate.Spec{ID: "dupes"}, Parser: "jscpd", Command: "sh",
			Args:       []string{fake(t, "report.sh"), sample(t, "jscpd_report.json"), "out/jscpd-report.json", "0"},
			ReportFile: "out/jscpd-report.json", MaxDuplicationPct: maxPct}
	}
	res := run(t, NewRunner(nil), Config{Gates: []GateConfig{mk(ptr(1.5))}})
	g := res.Gates[0]
	if g.Status != StatusFail || !hasRule(g.Findings, "duplication-threshold") || !strings.Contains(g.Feedback, "17.07% exceeds max_duplication_pct 1.50%") {
		t.Errorf("want duplication failure, got %+v\n%s", g, g.Feedback)
	}
	res = run(t, NewRunner(nil), Config{Gates: []GateConfig{mk(ptr(20.0))}})
	if g := res.Gates[0]; g.Status != StatusPass || len(g.Findings) != 1 {
		t.Errorf("within budget should pass with informational findings: %+v", g)
	}
	// Default threshold is 0%.
	res = run(t, NewRunner(nil), Config{Gates: []GateConfig{mk(nil)}})
	if res.Gates[0].Status != StatusFail {
		t.Errorf("default max_duplication_pct 0 should fail: %+v", res.Gates[0])
	}
}

func TestRunnerIgnoresStaleReportFile(t *testing.T) {
	repo := t.TempDir()
	// An agent-planted "clean" report must not be parsed if the tool writes nothing.
	writeFile(t, repo, "out/jscpd-report.json", string(readTestdata(t, "jscpd_clean.json")))
	cfg := mustConfig(t, Config{Gates: []GateConfig{{Spec: gate.Spec{ID: "dupes", Run: "true"}, Parser: "jscpd", ReportFile: "out/jscpd-report.json"}}})
	res, err := NewRunner(nil).Run(context.Background(), RunRequest{JobID: "j", RepoDir: repo, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if g := res.Gates[0]; g.Status != StatusError || !hasRule(g.Findings, "parse-error") {
		t.Errorf("stale report should not be trusted: %+v", g)
	}
}

func TestRunnerWorkingDir(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, repo, "apps/web/marker", "x")
	cfg := mustConfig(t, Config{Gates: []GateConfig{
		{Spec: gate.Spec{ID: "typecheck", Run: "test -f marker && echo 'src/x.ts(1,2): error TS1005: boom'; exit 1"}, Parser: "tsc", WorkingDir: "apps/web"},
		{Spec: gate.Spec{ID: "lint", Run: "true"}, WorkingDir: "missing"},
	}})
	res, err := NewRunner(nil).Run(context.Background(), RunRequest{JobID: "j", RepoDir: repo, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	var tc, lint GateResult
	for _, g := range res.Gates {
		if g.GateID == "typecheck" {
			tc = g
		} else {
			lint = g
		}
	}
	if len(tc.Findings) != 1 || tc.Findings[0].File != "apps/web/src/x.ts" {
		t.Errorf("typecheck findings = %+v", tc.Findings)
	}
	if lint.Status != StatusError || !hasRule(lint.Findings, "working-dir") {
		t.Errorf("missing working dir should be an error: %+v", lint)
	}
}

func TestRunnerDeterministicOrderAndResults(t *testing.T) {
	cfg := Config{Gates: []GateConfig{
		emitGate(t, "dead", "deadcode", "deadcode.txt", "0"),
		emitGate(t, "unit", "go-test", "gotest_fail.jsonl", "1"),
		emitGate(t, "lint-go", "golangci-lint", "golangci_fail.json", "1"),
		emitGate(t, "dupes", "jscpd", "jscpd_report.json", "0"),
		emitGate(t, "lint-web", "eslint", "eslint_fail.json", "1"),
		emitGate(t, "types", "tsc", "tsc_fail.txt", "2"),
		emitGate(t, "cyclo", "gocyclo", "gocyclo.txt", "1"),
	}}
	want := []string{"lint-go", "lint-web", "types", "unit", "dupes", "cyclo", "dead"}
	strip := func(r RunResult) RunResult {
		r.Duration = 0
		for i := range r.Gates {
			r.Gates[i].Duration = 0
		}
		return r
	}
	first := strip(run(t, NewRunner(nil), cfg))
	var got []string
	for _, g := range first.Gates {
		got = append(got, g.GateID)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", got, want)
	}
	for i := 0; i < 3; i++ {
		again := strip(run(t, NewRunner(nil), cfg))
		if again.Feedback != first.Feedback {
			t.Fatalf("feedback differs between runs:\n%s\n---\n%s", first.Feedback, again.Feedback)
		}
		for j := range again.Gates {
			a, b := first.Gates[j], again.Gates[j]
			if a.Status != b.Status || a.Summary != b.Summary || a.Feedback != b.Feedback || len(a.Findings) != len(b.Findings) {
				t.Fatalf("gate %s differs between runs", a.GateID)
			}
		}
	}
	if first.Status != StatusFail || len(first.Feedback) > MaxRunFeedbackChars {
		t.Errorf("status=%s feedback len=%d", first.Status, len(first.Feedback))
	}
	if !strings.HasPrefix(first.Feedback, "Gate run 1/15: FAIL") || !strings.Contains(first.Feedback, "14 gate runs left") {
		t.Errorf("run feedback header/footer wrong:\n%s", first.Feedback)
	}
}

func TestRunnerRequestValidation(t *testing.T) {
	r := NewRunner(nil)
	if _, err := r.Run(context.Background(), RunRequest{RepoDir: t.TempDir()}); err == nil {
		t.Error("missing JobID should error")
	}
	if _, err := r.Run(context.Background(), RunRequest{JobID: "j", RepoDir: "/definitely/not/here"}); err == nil {
		t.Error("missing repo dir should error")
	}
}

func TestMemoryCounterConcurrent(t *testing.T) {
	c := NewMemoryCounter()
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, capped := 0, 0
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := c.Acquire(context.Background(), "job", 15)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				ok++
			} else if errors.Is(err, ErrGateRunCapReached) {
				capped++
			}
		}()
	}
	wg.Wait()
	if ok != 15 || capped != 35 {
		t.Errorf("ok=%d capped=%d, want 15/35", ok, capped)
	}
	if n, _ := c.Used(context.Background(), "job"); n != 15 {
		t.Errorf("Used = %d", n)
	}
}

func TestGateFeedbackDedupesAndCaps(t *testing.T) {
	var fs []Finding
	for i := 1; i <= 6; i++ {
		fs = append(fs, Finding{File: "src/a.ts", Line: i, Rule: "no-explicit-any", Severity: SeverityError, Message: "Unexpected any."})
	}
	for i := 0; i < 30; i++ {
		fs = append(fs, Finding{File: "src/b.ts", Line: i + 1, Rule: "rule" + string(rune('a'+i%26)) + string(rune('a'+i/26)), Severity: SeverityWarning, Message: strings.Repeat("long message ", 40)})
	}
	SortFindings(fs)
	fb := GateFeedback(GateResult{Result: gate.Result{GateID: "lint", Status: StatusFail, Summary: "6 errors", Findings: fs}, Parser: "eslint"})
	if strings.Count(fb, "Unexpected any.") != 1 {
		t.Errorf("duplicate findings not collapsed:\n%s", fb)
	}
	if !strings.Contains(fb, "(also at src/a.ts:2, src/a.ts:3, src/a.ts:4 and 2 more)") {
		t.Errorf("missing collapsed locations:\n%s", fb)
	}
	if !strings.Contains(fb, "more distinct findings") || len(fb) > MaxGateFeedbackChars {
		t.Errorf("feedback not capped (len %d):\n%s", len(fb), fb)
	}
	if i := strings.Index(fb, "Unexpected any."); i < 0 || i > strings.Index(fb, "(warning)") {
		t.Errorf("errors should come before warnings:\n%s", fb)
	}
	if got := GateFeedback(GateResult{Result: gate.Result{GateID: "x", Status: StatusPass, Summary: "ok"}}); got != "x: PASS - ok" {
		t.Errorf("pass feedback = %q", got)
	}
}
