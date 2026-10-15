package gates

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// DefaultMaxOutputBytes caps captured stdout and stderr per gate.
const DefaultMaxOutputBytes = 16 << 20

// execResult is the raw outcome of running one gate command in one checkout.
type execResult struct {
	setupErr    error  // working_dir / report_file problems (config or checkout)
	setupRule   string // finding rule for setupErr
	startErr    error  // command could not be started
	timedOut    bool
	runTimedOut bool // the whole-run context expired (not just the gate's)
	timeout     time.Duration
	exitCode    int
	duration    time.Duration
	stdout      []byte
	stderr      []byte
	parsed      ParseOutput
	parseErr    error
}

// execute runs gate g in checkout root dir (head repo or base checkout) and
// parses its output. side is "head" or "base".
func (r *Runner) execute(ctx context.Context, cfg Config, g GateConfig, root, baseRef, side string) execResult {
	ex := execResult{exitCode: -1, timeout: g.EffectiveTimeout()}
	workDir, err := resolveWorkDir(root, g.WorkingDir)
	if err != nil {
		ex.setupErr, ex.setupRule = err, "working-dir"
		return ex
	}
	reportPath := ""
	if g.ReportFile != "" {
		reportPath = filepath.Join(workDir, filepath.FromSlash(g.ReportFile))
		// Never parse a stale (or agent-planted) report from the working tree.
		if err := os.Remove(reportPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			ex.setupErr, ex.setupRule = fmt.Errorf("cannot remove stale report file: %w", err), "report-file"
			return ex
		}
	}

	gctx, cancel := context.WithTimeout(ctx, ex.timeout)
	defer cancel()
	var cmd *exec.Cmd
	if g.Run != "" {
		cmd = exec.CommandContext(gctx, "sh", "-c", g.Run)
	} else {
		cmd = exec.CommandContext(gctx, g.Command, g.Args...)
	}
	cmd.Dir = workDir
	cmd.Env = r.gateEnv(cfg, g, baseRef, side)
	if g.Command != "" && !strings.Contains(g.Command, "/") {
		// exec.Command resolved Command with the runner's PATH; re-resolve
		// with the gate's PATH so both agree.
		if p, err := lookPathIn(g.Command, cmd.Env); err == nil {
			cmd.Path = p
			cmd.Err = nil
		}
	}
	configureKill(cmd)
	cmd.WaitDelay = 3 * time.Second
	maxOut := r.MaxOutputBytes
	if maxOut <= 0 {
		maxOut = DefaultMaxOutputBytes
	}
	stdout, stderr := &cappedBuffer{max: maxOut}, &cappedBuffer{max: maxOut}
	cmd.Stdout, cmd.Stderr = stdout, stderr

	start := time.Now()
	runErr := cmd.Run()
	ex.duration = time.Since(start)
	if cmd.ProcessState != nil {
		ex.exitCode = cmd.ProcessState.ExitCode()
	}
	ex.stdout, ex.stderr = stdout.Bytes(), stderr.Bytes()
	if gctx.Err() != nil {
		ex.timedOut = true
		ex.runTimedOut = ctx.Err() != nil
		return ex
	}
	var exitErr *exec.ExitError
	if runErr != nil && !errors.As(runErr, &exitErr) {
		ex.startErr = runErr
		return ex
	}
	report := ex.stdout
	if reportPath != "" {
		b, err := os.ReadFile(reportPath)
		if err != nil {
			ex.parseErr = fmt.Errorf("%w: report file %s not written: %v", ErrUnparseable, g.ReportFile, err)
			return ex
		}
		report = b
	}
	ex.parsed, ex.parseErr = Parse(g.Parser, ParseInput{Output: report, RepoDir: root, WorkDir: workDir, MaxComplexity: g.MaxComplexity})
	return ex
}

// lookPathIn resolves file using the PATH entry of env.
func lookPathIn(file string, env []string) (string, error) {
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			for _, dir := range filepath.SplitList(kv[len("PATH="):]) {
				if dir == "" {
					continue
				}
				p := filepath.Join(dir, file)
				if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
					return p, nil
				}
			}
		}
	}
	return "", exec.ErrNotFound
}

func resolveWorkDir(repo, rel string) (string, error) {
	dir := filepath.Join(repo, filepath.FromSlash(rel))
	realRepo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		return "", err
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("working_dir %q: %v", rel, err)
	}
	if r, err := filepath.Rel(realRepo, realDir); err != nil || r == ".." || strings.HasPrefix(r, "../") {
		return "", fmt.Errorf("working_dir %q resolves outside the repo", rel)
	}
	return dir, nil
}

// outputTail returns the last lines of stderr+stdout for error findings.
func outputTail(stderr, stdout []byte) string {
	combined := strings.TrimSpace(stripANSI(string(stderr)) + "\n" + stripANSI(string(stdout)))
	lines := strings.Split(combined, "\n")
	if len(lines) > 20 {
		lines = append([]string{fmt.Sprintf("... (%d earlier lines)", len(lines)-20)}, lines[len(lines)-20:]...)
	}
	s := strings.Join(lines, "\n")
	if len(s) > 2000 {
		s = "..." + s[len(s)-2000:]
	}
	return s
}

// cappedBuffer keeps at most max bytes and silently drops the rest.
type cappedBuffer struct {
	buf bytes.Buffer
	max int
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if room := c.max - c.buf.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		c.buf.Write(p)
	}
	return n, nil
}

func (c *cappedBuffer) Bytes() []byte { return c.buf.Bytes() }
