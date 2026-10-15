package gates

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type goTestEvent struct {
	Action     string `json:"Action"`
	Package    string `json:"Package"`
	Test       string `json:"Test"`
	Output     string `json:"Output"`
	ImportPath string `json:"ImportPath"` // build-output / build-fail (go1.24+)
}

var (
	// "    foo_test.go:12: expected 1, got 2"
	goStackFrameRE = regexp.MustCompile(`^\s+(/\S+\.go):(\d+)(?:\s|$)`)
	goTestLocRE    = regexp.MustCompile(`^\s*([\w./\-]+\.go):(\d+):(?:(\d+):)?\s*(.*)$`)
)

// parseGoTest parses `go test -json` output. Each failing (leaf) test becomes
// a finding; build failures become one finding per compiler error.
func parseGoTest(in ParseInput) (ParseOutput, error) {
	type key struct{ pkg, test string }
	outputs := map[key][]string{}
	buildOut := map[string][]string{}
	var failedTests []key
	var failedPkgs []string
	var buildFailed []string
	passed, events := 0, 0
	sc := bufio.NewScanner(bytes.NewReader(in.Output))
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	nonJSON := []string{}
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev goTestEvent
		if line[0] != '{' || json.Unmarshal(line, &ev) != nil {
			// e.g. stderr build errors from older go versions, "FAIL" summary lines.
			nonJSON = append(nonJSON, string(line))
			continue
		}
		events++
		k := key{ev.Package, ev.Test}
		switch ev.Action {
		case "output":
			outputs[k] = append(outputs[k], ev.Output)
		case "build-output":
			buildOut[ev.ImportPath] = append(buildOut[ev.ImportPath], ev.Output)
		case "build-fail":
			buildFailed = append(buildFailed, ev.ImportPath)
		case "pass":
			if ev.Test != "" {
				passed++
			}
		case "fail":
			if ev.Test != "" {
				failedTests = append(failedTests, k)
			} else {
				failedPkgs = append(failedPkgs, ev.Package)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return ParseOutput{}, fmt.Errorf("go-test: %w: %v", ErrUnparseable, err)
	}
	if events == 0 && len(nonJSON) > 0 {
		return ParseOutput{}, fmt.Errorf("go-test: %w: no JSON events (did you pass -json?)", ErrUnparseable)
	}

	mod := findGoModule(in)
	var out ParseOutput
	// Build failures: one finding per compiler error line.
	for _, ip := range buildFailed {
		n := 0
		for _, l := range buildOut[ip] {
			if m := goTestLocRE.FindStringSubmatch(strings.TrimRight(l, "\r\n")); m != nil {
				ln, _ := strconv.Atoi(m[2])
				col, _ := strconv.Atoi(m[3])
				out.Findings = append(out.Findings, Finding{File: m[1], Line: ln, Column: col, Rule: "build", Severity: SeverityError, Message: m[4]})
				n++
			}
		}
		if n == 0 {
			out.Findings = append(out.Findings, Finding{Rule: "build", Severity: SeverityError, Message: "build failed: " + ip + "\n" + capLines(buildOut[ip], 10)})
		}
	}
	// Leaf test failures (skip a parent when one of its subtests failed).
	pkgHasTestFail := map[string]bool{}
	leafFailed := 0
	for _, k := range failedTests {
		pkgHasTestFail[k.pkg] = true
		hasFailedChild := false
		for _, o := range failedTests {
			if o.pkg == k.pkg && strings.HasPrefix(o.test, k.test+"/") {
				hasFailedChild = true
				break
			}
		}
		if hasFailedChild {
			continue
		}
		leafFailed++
		f := Finding{Rule: k.test, Severity: SeverityError}
		var msg []string
		for _, l := range outputs[k] {
			t := strings.TrimRight(l, "\n")
			trim := strings.TrimSpace(t)
			if strings.HasPrefix(trim, "=== ") || strings.HasPrefix(trim, "--- ") || trim == "" {
				continue
			}
			if f.File == "" {
				if m := goTestLocRE.FindStringSubmatch(t); m != nil {
					// go test prints t.Error locations relative to the
					// package dir; make them relative to the working dir.
					f.File = path.Join(mod.pkgDir(k.pkg), m[1])
					f.Line, _ = strconv.Atoi(m[2])
					trim = m[4]
				}
			}
			msg = append(msg, trim)
		}
		f.Message = fmt.Sprintf("test failed in %s", k.pkg)
		if len(msg) > 0 {
			f.Message += ": " + capLines(msg, 8)
		}
		out.Findings = append(out.Findings, f)
	}
	// Packages that failed without a failing test (setup failure, panic in init, timeout ...).
	builtFail := map[string]bool{}
	for _, ip := range buildFailed {
		builtFail[ip] = true
	}
	for _, p := range failedPkgs {
		if pkgHasTestFail[p] || builtFail[p] || len(buildFailed) > 0 && strings.Contains(strings.Join(outputs[key{p, ""}], ""), "build failed") {
			continue
		}
		f := Finding{Rule: "package", Severity: SeverityError,
			Message: "package " + p + " failed: " + capLines(outputs[key{p, ""}], 10)}
		// Point at the first stack frame inside the repo (e.g. a panic in init).
		for _, l := range outputs[key{p, ""}] {
			if m := goStackFrameRE.FindStringSubmatch(l); m != nil && in.RepoDir != "" && strings.HasPrefix(m[1], in.RepoDir+"/") {
				f.File = m[1]
				f.Line, _ = strconv.Atoi(m[2])
				break
			}
		}
		out.Findings = append(out.Findings, f)
	}
	out.Summary = fmt.Sprintf("%s failed, %d passed", plural(leafFailed, "test"), passed)
	if len(buildFailed) > 0 {
		out.Summary += ", " + plural(len(buildFailed), "build failure")
	}
	return out, nil
}

func capLines(lines []string, n int) string {
	var keep []string
	for _, l := range lines {
		l = strings.TrimRight(l, "\n")
		if strings.TrimSpace(l) == "" {
			continue
		}
		keep = append(keep, strings.TrimSpace(l))
	}
	if len(keep) > n {
		keep = append(keep[:n], fmt.Sprintf("... (%d more lines)", len(keep)-n))
	}
	return strings.Join(keep, "\n")
}

// parseVitest parses `vitest run --reporter=json` output. vitest 5 writes the
// JSON report to a file and prints only a notice, so point at report_file.
func parseVitest(in ParseInput) (ParseOutput, error) {
	out, err := parseJestLike("vitest", in)
	if err != nil {
		if m := vitestNoticeRE.FindSubmatch(in.Output); m != nil {
			return out, fmt.Errorf("vitest: %w: the JSON report was written to %s, not stdout; set report_file to it or pass --outputFile.json", ErrUnparseable, m[1])
		}
	}
	return out, err
}

var vitestNoticeRE = regexp.MustCompile(`JSON report written to (\S+)`)

// parseJest parses `jest --json` output.
func parseJest(in ParseInput) (ParseOutput, error) { return parseJestLike("jest", in) }

func parseJestLike(name string, in ParseInput) (ParseOutput, error) {
	var rep struct {
		NumFailedTests *int `json:"numFailedTests"`
		NumPassedTests int  `json:"numPassedTests"`
		NumTotalTests  int  `json:"numTotalTests"`
		TestResults    []struct {
			Name             string `json:"name"`
			Status           string `json:"status"`
			Message          string `json:"message"`
			AssertionResults []struct {
				FullName        string   `json:"fullName"`
				Title           string   `json:"title"`
				Status          string   `json:"status"`
				FailureMessages []string `json:"failureMessages"`
				Location        *struct {
					Line   int `json:"line"`
					Column int `json:"column"`
				} `json:"location"`
			} `json:"assertionResults"`
		} `json:"testResults"`
	}
	if err := json.Unmarshal(extractJSON(in.Output, '{'), &rep); err != nil {
		return ParseOutput{}, fmt.Errorf("%s: %w: %v", name, ErrUnparseable, err)
	}
	if rep.NumFailedTests == nil && rep.TestResults == nil {
		return ParseOutput{}, fmt.Errorf("%s: %w: not a jest/vitest json report", name, ErrUnparseable)
	}
	var out ParseOutput
	failed := 0
	for _, tr := range rep.TestResults {
		suiteHasFail := false
		for _, a := range tr.AssertionResults {
			if a.Status != "failed" {
				continue
			}
			failed++
			suiteHasFail = true
			name := a.FullName
			if name == "" {
				name = a.Title
			}
			f := Finding{File: tr.Name, Rule: name, Severity: SeverityError,
				Message: name + ": " + firstLines(stripANSI(strings.Join(a.FailureMessages, "\n")), 6)}
			if a.Location != nil {
				f.Line, f.Column = a.Location.Line, a.Location.Column
			} else {
				f.Line, f.Column = stackLocation(strings.Join(a.FailureMessages, "\n"), tr.Name)
			}
			out.Findings = append(out.Findings, f)
		}
		if tr.Status == "failed" && !suiteHasFail {
			// Suite failed to run (syntax error, import error ...).
			msg := strings.TrimSpace(stripANSI(tr.Message))
			msg = strings.TrimSpace(strings.TrimPrefix(msg, "● Test suite failed to run"))
			out.Findings = append(out.Findings, Finding{File: tr.Name, Rule: "suite", Severity: SeverityError,
				Message: "test suite failed to run: " + firstLines(msg, 8)})
		}
	}
	out.Summary = fmt.Sprintf("%s failed, %d passed", plural(failed, "test"), rep.NumPassedTests)
	return out, nil
}

func firstLines(s string, n int) string {
	return capLines(strings.Split(s, "\n"), n)
}

// goModule locates the Go module containing the command's working dir, to
// map import paths to directories.
type goModule struct {
	path string // module path from go.mod
	dir  string // module dir relative to the working dir ("" or "../..")
}

func findGoModule(in ParseInput) goModule {
	dir := in.WorkDir
	for i := 0; dir != "" && i < 32; i++ {
		if b, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil {
			if m := goModuleRE.FindSubmatch(b); m != nil {
				rel, err := filepath.Rel(in.WorkDir, dir)
				if err != nil {
					return goModule{}
				}
				return goModule{path: string(m[1]), dir: filepath.ToSlash(rel)}
			}
			return goModule{}
		}
		if dir == in.RepoDir || filepath.Dir(dir) == dir {
			break
		}
		dir = filepath.Dir(dir)
	}
	return goModule{}
}

var goModuleRE = regexp.MustCompile(`(?m)^module\s+"?([^\s"]+)"?`)

// pkgDir returns the package's directory relative to the working dir, or ""
// when it cannot be derived (the file name is then kept as printed).
func (m goModule) pkgDir(pkg string) string {
	switch {
	case m.path == "":
		return ""
	case pkg == m.path:
		return path.Clean(m.dir)
	case strings.HasPrefix(pkg, m.path+"/"):
		return path.Join(m.dir, strings.TrimPrefix(pkg, m.path+"/"))
	}
	return ""
}

// stackLocation finds the first "file:line:col" stack frame for file in a
// failure message (jest only reports locations with --testLocationInResults).
func stackLocation(msg, file string) (int, int) {
	if file == "" {
		return 0, 0
	}
	re := regexp.MustCompile(regexp.QuoteMeta(file) + `:(\d+):(\d+)`)
	if m := re.FindStringSubmatch(msg); m != nil {
		l, _ := strconv.Atoi(m[1])
		c, _ := strconv.Atoi(m[2])
		return l, c
	}
	return 0, 0
}
