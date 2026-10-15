package gates

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/alpine-dev/alpine/packages/shared/gate"
)

// ErrUnknownGate is returned when RunRequest.Only names a gate that is not in
// the base-branch config.
var ErrUnknownGate = errors.New("gates: unknown gate id")

// Runner runs configured gates against a checked-out repository.
type Runner struct {
	// Counter tracks gate runs per job. Required (see NewRunner).
	Counter RunCounter
	// Baselines caches ratchet baselines (gate results on a base SHA). Nil
	// disables caching. NewRunner sets an in-memory cache.
	Baselines BaselineCache
	// Screenshotter takes visual-gate screenshots. Nil uses
	// CommandScreenshotter (the gate's screenshot_command, Playwright CLI by default).
	Screenshotter Screenshotter
	// ArtifactsRoot is where visual artifacts are written (visual.artifacts_dir
	// is relative to it). Empty: a per-run temp dir. Never inside the repo.
	ArtifactsRoot string
	// TempDir is the parent for temporary base checkouts ("" = os.TempDir).
	TempDir string
	// MaxOutputBytes caps captured stdout/stderr per stream (default 16 MiB).
	MaxOutputBytes int
	// ExtraEnv is explicit KEY=value pairs added to every gate's environment
	// (e.g. gates_only secrets injected by the backend). See gateEnv.
	ExtraEnv []string
}

// NewRunner returns a Runner with an in-memory baseline cache. A nil counter
// uses a new MemoryCounter.
func NewRunner(counter RunCounter) *Runner {
	if counter == nil {
		counter = NewMemoryCounter()
	}
	return &Runner{Counter: counter, Baselines: NewMemoryBaselineCache()}
}

// RunRequest describes one gate run.
type RunRequest struct {
	// JobID identifies the job (ticket/implement run) the cap applies to.
	JobID string
	// RepoDir is the checked-out repository (the agent's head). It is never
	// modified by the runner (except report_file outputs of gate tools).
	RepoDir string
	// BaseRef is the base branch/commit the config was loaded from. Ratchet
	// gates also run on a temporary checkout of it. Exported to gate commands
	// as ALPINE_BASE_REF.
	BaseRef string
	// Config must come from LoadConfigFromBase(ctx, RepoDir, BaseRef).
	Config Config
	// Only, if non-empty, restricts this run to these gate ids (e.g. rerun
	// only the gates that failed). It can only select gates from Config; an
	// unknown id is an error, and the selected gates run exactly as
	// configured. Unselected gates are reported as skipped.
	Only []string
	// VisualRoutes are routes for visual gates configured with routes: auto
	// (derived by the orchestrator from the diff and ticket).
	VisualRoutes []string
}

// Run acquires a gate run from the counter and runs the selected gates once,
// sequentially, in a stable order. When the job's cap is reached it runs
// nothing and returns a result with CapReached=true together with an error
// wrapping ErrGateRunCapReached. Gate failures are not Go errors; they are
// reported in the result.
func (r *Runner) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	start := time.Now()
	res := RunResult{JobID: req.JobID, BaseRef: req.BaseRef, GateRunCap: req.Config.GateRunCap(), Gates: []GateResult{}}
	if req.JobID == "" {
		return res, errors.New("gates: RunRequest.JobID is required")
	}
	repo, err := filepath.Abs(req.RepoDir)
	if err != nil {
		return res, fmt.Errorf("gates: repo dir: %w", err)
	}
	if st, err := os.Stat(repo); err != nil || !st.IsDir() {
		return res, fmt.Errorf("gates: repo dir %q is not a directory", req.RepoDir)
	}
	selected, err := selectGates(req.Config.Gates, req.Only)
	if err != nil {
		return res, err // before Acquire: a bad request does not use up a gate run
	}
	if r.Counter == nil {
		return res, errors.New("gates: Runner.Counter is nil (use NewRunner)")
	}
	used, err := r.Counter.Acquire(ctx, req.JobID, res.GateRunCap)
	res.GateRunsUsed = used
	if err != nil {
		if errors.Is(err, ErrGateRunCapReached) {
			res.Status = StatusSkipped
			res.CapReached = true
			res.Feedback = RunFeedback(res)
		}
		return res, err
	}

	runCtx, cancel := context.WithTimeout(ctx, req.Config.RunTimeout())
	defer cancel()
	rs := &runState{r: r, cfg: req.Config, repo: repo, baseRef: req.BaseRef, visualRoutes: req.VisualRoutes, jobID: req.JobID}
	defer rs.cleanup()

	for _, g := range orderedGates(req.Config.Gates) {
		var gr GateResult
		if selected != nil && !selected[g.ID] {
			gr = newGateResult(g)
			gr.Status, gr.Summary = StatusSkipped, "not selected for this run"
		} else {
			gr = rs.runGate(runCtx, g)
		}
		gr.Feedback = GateFeedback(gr)
		res.Gates = append(res.Gates, gr)
	}
	res.Status = overallStatus(res.Gates)
	res.Duration = time.Since(start)
	res.Feedback = RunFeedback(res)
	return res, nil
}

// selectGates validates a subset request. nil means "all gates".
func selectGates(gs []GateConfig, only []string) (map[string]bool, error) {
	if len(only) == 0 {
		return nil, nil
	}
	known := map[string]bool{}
	for _, g := range gs {
		known[g.ID] = true
	}
	sel := map[string]bool{}
	var unknown []string
	for _, id := range only {
		if !known[id] {
			unknown = append(unknown, id)
			continue
		}
		sel[id] = true
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("%w: %s (not in the base-branch config)", ErrUnknownGate, strings.Join(unknown, ", "))
	}
	return sel, nil
}

// orderedGates returns gates sorted by type order, keeping config order within a type.
func orderedGates(gs []GateConfig) []GateConfig {
	out := append([]GateConfig(nil), gs...)
	sort.SliceStable(out, func(i, j int) bool { return typeOrder[out[i].Type] < typeOrder[out[j].Type] })
	return out
}

// overallStatus: any fail -> fail (actionable for the agent); else any error
// -> error (infra/config problem, escalate); else pass.
func overallStatus(gs []GateResult) Status {
	st := StatusPass
	for _, g := range gs {
		switch g.Status {
		case StatusFail:
			return StatusFail
		case StatusError:
			st = StatusError
		}
	}
	return st
}

// runState is the per-Run context shared by gates: config, head repo and the
// lazily created base checkout.
type runState struct {
	r            *Runner
	cfg          Config
	repo         string
	baseRef      string
	jobID        string
	visualRoutes []string

	baseOnce sync.Once
	base     *baseCheckout
	baseErr  error

	artifactsOnce sync.Once
	artifactsRoot string
	artifactsErr  error
}

func (rs *runState) cleanup() { rs.base.remove() }

// baseSHA resolves the base ref to a full commit SHA (cache key component).
func (rs *runState) baseSHA(ctx context.Context) (string, error) {
	return resolveCommit(ctx, rs.repo, rs.baseRef)
}

// baseDir returns the temporary base checkout, creating it (and running
// gate_runner.base_setup) on first use.
func (rs *runState) baseDir(ctx context.Context) (string, error) {
	rs.baseOnce.Do(func() {
		sha, err := rs.baseSHA(ctx)
		if err != nil {
			rs.baseErr = err
			return
		}
		bc, err := newBaseCheckout(ctx, rs.repo, sha, rs.r.TempDir)
		if err != nil {
			rs.baseErr = err
			return
		}
		rs.base = bc
		if setup := rs.cfg.Runner.BaseSetup; setup != "" {
			timeout := time.Duration(rs.cfg.Runner.BaseSetupTimeout)
			if timeout <= 0 {
				timeout = DefaultGateTimeout
			}
			sg := GateConfig{Spec: gate.Spec{ID: "base_setup", Run: setup}, Timeout: Duration(timeout)}
			ex := rs.r.execute(ctx, rs.cfg, sg, bc.Dir, rs.baseRef, "base")
			switch {
			case ex.setupErr != nil:
				rs.baseErr = ex.setupErr
			case ex.timedOut:
				rs.baseErr = fmt.Errorf("gate_runner.base_setup timed out after %s", timeout)
			case ex.startErr != nil:
				rs.baseErr = fmt.Errorf("gate_runner.base_setup: %v", ex.startErr)
			case ex.exitCode != 0:
				rs.baseErr = fmt.Errorf("gate_runner.base_setup exited %d:\n%s", ex.exitCode, outputTail(ex.stderr, ex.stdout))
			}
		}
	})
	if rs.baseErr != nil {
		return "", rs.baseErr
	}
	return rs.base.Dir, nil
}

func newGateResult(g GateConfig) GateResult {
	return GateResult{Result: gate.Result{GateID: g.ID, Findings: []Finding{}}, Type: g.Type, Parser: g.Parser, ExitCode: -1}
}

func (rs *runState) runGate(ctx context.Context, g GateConfig) GateResult {
	gr := newGateResult(g)
	switch {
	case !g.IsEnabled():
		gr.Status, gr.Summary = StatusSkipped, "disabled in config"
		return gr
	case g.Type == TypeE2E:
		gr.Status, gr.Summary = StatusSkipped, "not run by packages/gates (e2e runs in the e2e sandbox)"
		return gr
	}
	if err := ctx.Err(); err != nil {
		gr.Status, gr.Summary = StatusError, "not run: gate run timeout exceeded"
		gr.Findings = append(gr.Findings, Finding{Rule: "run-timeout", Severity: SeverityError, Message: "the whole gate run exceeded gate_runner.run_timeout before this gate started"})
		return gr
	}
	if g.Type == TypeVisual {
		return rs.runVisual(ctx, g, gr)
	}

	ex := rs.r.execute(ctx, rs.cfg, g, rs.repo, rs.baseRef, "head")
	gr.Duration, gr.ExitCode = ex.duration, ex.exitCode
	if done, out := headFailure(gr, g, ex); done {
		return out
	}
	if g.RatchetEnabled() {
		return rs.ratchet(ctx, gr, g, ex)
	}
	return decide(gr, g, ex)
}

// headFailure handles outcomes that need no parsing: bad config, timeout,
// failure to start, unparseable output.
func headFailure(gr GateResult, g GateConfig, ex execResult) (bool, GateResult) {
	switch {
	case ex.setupErr != nil:
		return true, errorResult(gr, ex.setupRule, ex.setupErr.Error())
	case ex.timedOut:
		gr.TimedOut = true
		gr.Status = StatusFail
		why := fmt.Sprintf("gate %q exceeded its timeout of %s and was killed", g.ID, ex.timeout)
		if ex.runTimedOut {
			why = fmt.Sprintf("gate %q was killed because the whole gate run exceeded its timeout", g.ID)
		}
		gr.Summary = "timed out (limit " + ex.timeout.String() + ")"
		gr.Findings = append(gr.Findings, Finding{Rule: "timeout", Severity: SeverityError, Message: why,
			Suggestion: "look for hanging tests, infinite loops or waits on network/input"})
		return true, applyWarnOnly(gr, g)
	case ex.startErr != nil:
		return true, errorResult(gr, "start", "failed to run gate command: "+ex.startErr.Error())
	case ex.parseErr != nil:
		tail := outputTail(ex.stderr, ex.stdout)
		if ex.exitCode == 0 {
			gr.Status = StatusError
			gr.Summary = "could not parse " + g.Parser + " output"
			gr.Findings = append(gr.Findings, Finding{Rule: "parse-error", Severity: SeverityError, Message: fmt.Sprintf("%v\n%s", ex.parseErr, tail)})
			return true, gr
		}
		gr.Status = StatusFail
		gr.Summary = fmt.Sprintf("exit code %d (output not parseable as %s)", ex.exitCode, g.Parser)
		gr.Findings = append(gr.Findings, Finding{Rule: "exit-code", Severity: SeverityError, Message: fmt.Sprintf("command exited with code %d:\n%s", ex.exitCode, tail)})
		return true, applyWarnOnly(gr, g)
	}
	return false, gr
}

// decide turns parser output + exit code into a status (non-ratchet gates).
func decide(gr GateResult, g GateConfig, ex execResult) GateResult {
	parsed := ex.parsed
	findings := filterRules(parsed.Findings, g.OnlyRules)
	gr.Findings = append(gr.Findings, findings...)
	gr.Summary = parsed.Summary
	errs, warns := countSeverities(findings)
	failed := errs > 0

	// The exit code is authoritative unless the gate's verdict is defined by
	// a threshold/filter applied here (the tool's own exit code then reflects
	// a different policy).
	thresholdGate := g.Type == TypeDuplication || (g.Parser == "gocyclo" && g.MaxComplexity > 0)
	exitAuthoritative := len(g.OnlyRules) == 0 && !thresholdGate

	if parsed.DuplicationPct != nil && g.Type == TypeDuplication {
		maxPct := 0.0
		if g.MaxDuplicationPct != nil {
			maxPct = *g.MaxDuplicationPct
		}
		if *parsed.DuplicationPct > maxPct {
			failed = true
			gr.Findings = append([]Finding{{Rule: "duplication-threshold", Severity: SeverityError,
				Message: fmt.Sprintf("duplication %.2f%% exceeds max_duplication_pct %.2f%%", *parsed.DuplicationPct, maxPct)}}, gr.Findings...)
		} else {
			failed = false // duplicates within the allowed budget are informational
		}
	}
	if g.MaxWarnings != nil && warns > *g.MaxWarnings {
		failed = true
		gr.Findings = append(gr.Findings, Finding{Rule: "max-warnings", Severity: SeverityError,
			Message: fmt.Sprintf("%d warnings exceed max_warnings %d", warns, *g.MaxWarnings)})
	}
	if gr.ExitCode != 0 && exitAuthoritative {
		failed = true
		if errs == 0 {
			gr.Findings = append(gr.Findings, Finding{Rule: "exit-code", Severity: SeverityError,
				Message: fmt.Sprintf("command exited with code %d:\n%s", gr.ExitCode, outputTail(ex.stderr, ex.stdout))})
		}
	}
	if gr.Summary == "" {
		gr.Summary = fmt.Sprintf("exit code %d", gr.ExitCode)
	}
	SortFindings(gr.Findings)
	if failed {
		gr.Status = StatusFail
	} else {
		gr.Status = StatusPass
	}
	return applyWarnOnly(gr, g)
}

func applyWarnOnly(gr GateResult, g GateConfig) GateResult {
	if g.WarnOnly && gr.Status == StatusFail {
		gr.Status = StatusWarn
	}
	return gr
}

func errorResult(gr GateResult, rule, msg string) GateResult {
	gr.Status = StatusError
	gr.Summary = firstLines(msg, 1)
	gr.Findings = append(gr.Findings, Finding{Rule: rule, Severity: SeverityError, Message: msg})
	return gr
}

func filterRules(fs []Finding, only []string) []Finding {
	if len(only) == 0 {
		return fs
	}
	keep := map[string]bool{}
	for _, r := range only {
		keep[r] = true
	}
	out := []Finding{}
	for _, f := range fs {
		if keep[f.Rule] {
			out = append(out, f)
		}
	}
	return out
}
