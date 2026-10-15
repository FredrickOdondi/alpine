package gates

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alpine-dev/alpine/packages/shared/gate"
)

// ratchetRepo creates a fixture repo whose base commit (main) contains
// baseFiles, then writes headFiles into the working tree (uncommitted, like an
// agent mid-turn). Fake tools just print .fake/* files from whichever
// checkout they run in.
func ratchetRepo(t *testing.T, baseFiles, headFiles map[string]string) string {
	t.Helper()
	dir := newRepo(t, baseFiles)
	for p, c := range headFiles {
		writeFile(t, dir, p, c)
	}
	return dir
}

// repoSnapshot captures everything an agent could observe in its repo.
func repoSnapshot(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	for _, args := range [][]string{{"status", "--porcelain", "--untracked-files=all"}, {"worktree", "list", "--porcelain"}, {"stash", "list"}, {"for-each-ref"}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = gitEnv()
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		b.Write(out)
	}
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.Name() == ".git" && d.IsDir() {
			return filepath.SkipDir
		}
		if err == nil && !d.IsDir() {
			c, _ := os.ReadFile(p)
			b.WriteString(p + "\n" + string(c) + "\n")
		}
		return nil
	})
	return b.String()
}

func runRatchet(t *testing.T, r *Runner, dir string, g GateConfig, extra ...func(*Config)) GateResult {
	t.Helper()
	cfg := Config{Gates: []GateConfig{g}}
	for _, f := range extra {
		f(&cfg)
	}
	cfg = mustConfig(t, cfg)
	res, err := r.Run(context.Background(), RunRequest{JobID: t.Name(), RepoDir: dir, BaseRef: "main", Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	return res.Gates[0]
}

func countBySeverity(fs []Finding) map[Severity]int {
	m := map[Severity]int{}
	for _, f := range fs {
		m[f.Severity]++
	}
	return m
}

const cycloBase = `20 calc Big calc/calc.go:10:1
16 calc Medium calc/calc.go:40:1
`

func cycloGate(countFile string) GateConfig {
	return GateConfig{Spec: gate.Spec{ID: "complexity", Ratchet: true,
		Run: `echo run >> "$COUNT_FILE"; cat .fake/gocyclo.txt; exit 1`},
		Parser: "gocyclo", MaxComplexity: 15, Env: map[string]string{"COUNT_FILE": countFile}}
}

func TestRatchetComplexity(t *testing.T) {
	tests := []struct {
		name       string
		head       string
		wantStatus Status
		wantErrors int // regressions
		wantInfo   int // pre-existing
		wantMsg    []string
	}{
		{name: "only shifted lines", head: "20 calc Big calc/calc.go:25:1\n16 calc Medium calc/calc.go:70:1\n",
			wantStatus: StatusPass, wantInfo: 2, wantMsg: []string{"pre-existing: function calc.Big"}},
		{name: "became more complex and new function", head: "20 calc Big calc/calc.go:12:1\n18 calc Medium calc/calc.go:44:1\n17 calc Added calc/calc.go:90:1\n",
			wantStatus: StatusFail, wantErrors: 2, wantInfo: 1,
			wantMsg: []string{"worse: function calc.Medium has cyclomatic complexity 18 (max 15) (was 16 on base)", "new: function calc.Added"}},
		{name: "improved", head: "18 calc Big calc/calc.go:10:1\n", wantStatus: StatusPass, wantInfo: 1},
		{name: "same name in another file is new", head: cycloBase + "16 other Medium other/m.go:3:1\n", wantStatus: StatusFail, wantErrors: 1, wantInfo: 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := ratchetRepo(t, map[string]string{".fake/gocyclo.txt": cycloBase}, map[string]string{".fake/gocyclo.txt": tc.head})
			before := repoSnapshot(t, dir)
			tmp := t.TempDir()
			r := NewRunner(nil)
			r.TempDir = tmp
			gr := runRatchet(t, r, dir, cycloGate(filepath.Join(t.TempDir(), "count")))
			if gr.Status != tc.wantStatus {
				t.Errorf("status = %s, want %s: %s\n%+v", gr.Status, tc.wantStatus, gr.Summary, gr.Findings)
			}
			c := countBySeverity(gr.Findings)
			if c[SeverityError] != tc.wantErrors || c[SeverityInfo] != tc.wantInfo {
				t.Errorf("errors/info = %d/%d, want %d/%d: %+v", c[SeverityError], c[SeverityInfo], tc.wantErrors, tc.wantInfo, gr.Findings)
			}
			all := ""
			for _, f := range gr.Findings {
				all += f.Message + "\n"
			}
			for _, m := range tc.wantMsg {
				if !strings.Contains(all, m) {
					t.Errorf("missing %q in:\n%s", m, all)
				}
			}
			if tc.wantInfo > 0 && !strings.Contains(gr.Feedback, "pre-existing") {
				t.Errorf("pre-existing issues should be visible in feedback:\n%s", gr.Feedback)
			}
			if after := repoSnapshot(t, dir); after != before {
				t.Errorf("the agent's repo changed during the run")
			}
			if ents, _ := os.ReadDir(tmp); len(ents) != 0 {
				t.Errorf("base checkout not cleaned up: %v", ents)
			}
		})
	}
}

func TestRatchetDeadcode(t *testing.T) {
	base := "util/s.go:12:6: unreachable func: Reverse\n"
	g := GateConfig{Spec: gate.Spec{ID: "dead", Ratchet: true, Run: "cat .fake/deadcode.txt"}, Parser: "deadcode"}
	dir := ratchetRepo(t, map[string]string{".fake/deadcode.txt": base},
		map[string]string{".fake/deadcode.txt": "util/s.go:40:6: unreachable func: Reverse\nutil/s.go:50:6: unreachable func: padLeft\n"})
	gr := runRatchet(t, NewRunner(nil), dir, g)
	if gr.Status != StatusFail || countBySeverity(gr.Findings)[SeverityError] != 1 || !strings.Contains(gr.Feedback, "new: unreachable func: padLeft") {
		t.Errorf("want 1 new dead-code finding: %+v\n%s", gr, gr.Feedback)
	}
	dir = ratchetRepo(t, map[string]string{".fake/deadcode.txt": base}, map[string]string{".fake/deadcode.txt": "util/s.go:99:6: unreachable func: Reverse\n"})
	if gr := runRatchet(t, NewRunner(nil), dir, g); gr.Status != StatusPass {
		t.Errorf("pre-existing dead code should pass: %+v", gr)
	}
}

func jscpdReport(pct float64, dups string) string {
	return `{"statistics":{"total":{"lines":1000,"duplicatedLines":0,"percentage":` + strings.TrimRight(strings.TrimRight(ftoa(pct), "0"), ".") + `,"clones":1}},"duplicates":[` + dups + `]}`
}

func ftoa(f float64) string { return fmt.Sprintf("%.4f", f) }

const clone1 = `{"format":"typescript","lines":12,"firstFile":{"name":"src/a.ts","start":10,"end":21},"secondFile":{"name":"src/b.ts","start":5,"end":16}}`
const clone1Shifted = `{"format":"typescript","lines":12,"firstFile":{"name":"src/a.ts","start":30,"end":41},"secondFile":{"name":"src/b.ts","start":8,"end":19}}`
const clone2 = `{"format":"typescript","lines":9,"firstFile":{"name":"src/c.ts","start":1,"end":9},"secondFile":{"name":"src/d.ts","start":1,"end":9}}`

func TestRatchetDuplicationDelta(t *testing.T) {
	g := GateConfig{Spec: gate.Spec{ID: "dupes", MaxNewDuplicationPct: ptr(0.5),
		Run: "mkdir -p out && cp .fake/jscpd.json out/jscpd-report.json"}, Parser: "jscpd", ReportFile: "out/jscpd-report.json"}
	base := map[string]string{".fake/jscpd.json": jscpdReport(2.0, clone1)}
	tests := []struct {
		name   string
		head   string
		status Status
		want   string
	}{
		{"shifted clone, same pct", jscpdReport(2.0, clone1Shifted), StatusPass, "pre-existing: 12 duplicated lines"},
		{"new clone within budget", jscpdReport(2.4, clone1+","+clone2), StatusPass, "(warning) new: 9 duplicated lines"},
		{"rise beyond budget", jscpdReport(3.0, clone1+","+clone2), StatusFail, "duplication rose from 2.00% to 3.00% (+1.00 points, max_new_duplication_pct 0.50)"},
		{"less duplication", jscpdReport(1.0, ""), StatusPass, "1 fixed vs base"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := ratchetRepo(t, base, map[string]string{".fake/jscpd.json": tc.head})
			gr := runRatchet(t, NewRunner(nil), dir, g)
			text := gr.Feedback + "\n" + gr.Summary
			for _, f := range gr.Findings {
				text += "\n(" + string(f.Severity) + ") " + f.Message
			}
			if gr.Status != tc.status || !strings.Contains(text, tc.want) {
				t.Errorf("status %s (want %s), missing %q:\n%s", gr.Status, tc.status, tc.want, text)
			}
		})
	}
}

func TestRatchetBaselineCache(t *testing.T) {
	dir := ratchetRepo(t, map[string]string{".fake/gocyclo.txt": cycloBase}, map[string]string{".fake/gocyclo.txt": cycloBase})
	count := filepath.Join(t.TempDir(), "count")
	runs := func() int {
		b, _ := os.ReadFile(count)
		return strings.Count(string(b), "run")
	}
	for _, cache := range []BaselineCache{NewMemoryBaselineCache(), DirBaselineCache{Dir: t.TempDir()}} {
		_ = os.Remove(count)
		r := NewRunner(nil)
		r.Baselines = cache
		g := cycloGate(count)
		first := runRatchet(t, r, dir, g)
		if runs() != 2 || strings.Contains(first.Summary, "cached") {
			t.Fatalf("%T first run: tool ran %d times (want head+base=2), summary %q", cache, runs(), first.Summary)
		}
		second := runRatchet(t, r, dir, g)
		if runs() != 3 || !strings.Contains(second.Summary, "baseline cached") {
			t.Errorf("%T second run: tool ran %d times (want 3: head only), summary %q", cache, runs(), second.Summary)
		}
		if first.Status != second.Status || len(first.Findings) != len(second.Findings) {
			t.Errorf("%T cached result differs", cache)
		}
		// A different gate config is a different cache key.
		g.MaxComplexity = 17
		_ = runRatchet(t, r, dir, g)
		if runs() != 5 {
			t.Errorf("%T changed config should recompute the baseline (runs=%d)", cache, runs())
		}
	}
	// The key covers base SHA and config.
	cfg := Config{}
	g := cycloGate("x")
	k1 := baselineKey("aaaa", cfg, g)
	if k1 == baselineKey("bbbb", cfg, g) || k1 != baselineKey("aaaa", cfg, g) {
		t.Error("baseline key must depend on SHA and be stable")
	}
	cfg.Runner.EnvAllowlist = []string{"X"}
	if k1 == baselineKey("aaaa", cfg, g) {
		t.Error("baseline key must depend on the env allowlist")
	}
}

func TestRatchetBaseSetupAndFailure(t *testing.T) {
	// base_setup runs in the base checkout before the baseline (e.g. installs deps).
	dir := ratchetRepo(t, map[string]string{".fake/deadcode.txt": "a.go:1:6: unreachable func: X\n"},
		map[string]string{".fake/deadcode.txt": "a.go:1:6: unreachable func: X\n", "installed.flag": "head has deps\n"})
	g := GateConfig{Spec: gate.Spec{ID: "dead", Ratchet: true, Run: "test -f installed.flag && cat .fake/deadcode.txt"}, Parser: "deadcode"}
	gr := runRatchet(t, NewRunner(nil), dir, g, func(c *Config) { c.Runner.BaseSetup = "touch installed.flag" })
	if gr.Status != StatusPass {
		t.Errorf("with base_setup: %+v", gr)
	}
	// Without base_setup the base run fails, so the gate reports an error (escalate), not a pass.
	gr = runRatchet(t, NewRunner(nil), dir, g)
	if gr.Status != StatusError || !hasRule(gr.Findings, "baseline") {
		t.Errorf("baseline failure should be an error: %+v", gr)
	}
	// A head-side crash without findings fails instead of passing silently.
	dir2 := ratchetRepo(t, map[string]string{".fake/deadcode.txt": ""}, nil)
	gr = runRatchet(t, NewRunner(nil), dir2, GateConfig{Spec: gate.Spec{ID: "dead", Ratchet: true, Run: "echo boom >&2; exit 2"}, Parser: "deadcode"})
	if gr.Status != StatusFail || !hasRule(gr.Findings, "exit-code") {
		t.Errorf("head crash should fail: %+v", gr)
	}
	// A failing base_setup is reported too.
	gr = runRatchet(t, NewRunner(nil), dir, g, func(c *Config) { c.Runner.BaseSetup = "echo nope >&2; exit 3" })
	if gr.Status != StatusError || !strings.Contains(gr.Feedback, "base_setup exited 3") {
		t.Errorf("base_setup failure: %+v\n%s", gr, gr.Feedback)
	}
}

func TestCompareFindingsKeys(t *testing.T) {
	base := []Finding{
		{File: "a.ts", Line: 3, Rule: "complexity", Message: "Arrow function has a complexity of 12. Maximum allowed is 10."},
		{File: "a.ts", Line: 9, Rule: "complexity", Message: "Arrow function has a complexity of 11. Maximum allowed is 10."},
		{File: "p.json", Line: 4, Rule: "dependencies", Message: "unused dependency: lodash"},
	}
	head := []Finding{
		{File: "a.ts", Line: 5, Rule: "complexity", Message: "Arrow function has a complexity of 12. Maximum allowed is 10."},
		{File: "a.ts", Line: 20, Rule: "complexity", Message: "Arrow function has a complexity of 11. Maximum allowed is 10."},
		{File: "a.ts", Line: 30, Rule: "complexity", Message: "Arrow function has a complexity of 11. Maximum allowed is 10."},
		{File: "p.json", Line: 7, Rule: "dependencies", Message: "unused dependency: lodash"},
	}
	c := compareFindings(base, head)
	if c.regressions != 1 || c.preexisting != 3 || c.fixed != 0 {
		t.Errorf("regressions=%d preexisting=%d fixed=%d", c.regressions, c.preexisting, c.fixed)
	}
	if k1, _, _ := findingKey(Finding{File: "x", Rule: "r", Message: "declared at line 3"}); k1 != func() string {
		k, _, _ := findingKey(Finding{File: "x", Rule: "r", Message: "declared at line 9"})
		return k
	}() {
		t.Error("line references inside messages must not change the key")
	}
}
