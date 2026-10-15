package gates

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "parsers", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParsers(t *testing.T) {
	type want struct {
		n, errs, warns int
		first          Finding // compared on non-zero fields; Message is a substring
		contains       []string
		summary        string // substring
		dupPct         float64
	}
	tests := []struct {
		name, parser, file string
		input              string // used when file == ""
		maxComplexity      int
		repoDir            string // default "/repo" (paths in recordings are normalized to it)
		want               want
		wantErr            bool
	}{
		// Recorded from real tool runs on testdata/fixtures (see testdata/README.md).
		{name: "eslint fail", parser: "eslint", file: "eslint_fail.json", want: want{
			n: 6, errs: 5, warns: 1,
			first:    Finding{File: "src/a.ts", Line: 1, Column: 8, Rule: "complexity", Severity: SeverityError, Message: "Function 'sumA' has a complexity of 6"},
			contains: []string{"@typescript-eslint/no-unused-vars 'unused' is assigned", "eqeqeq Expected '==='", "Arrow function has a complexity of 9"},
			summary:  "5 errors, 1 warning in 4 files"}},
		{name: "eslint pass", parser: "eslint", file: "eslint_pass.json", want: want{summary: "0 errors, 0 warnings"}},
		{name: "eslint via npm run (banner)", parser: "eslint", file: "eslint_npm_banner.txt", want: want{
			n: 2, errs: 2, first: Finding{File: "src/big.ts", Line: 1, Rule: "complexity", Message: "Function 'classify' has a complexity of 10"}}},
		{name: "eslint garbage", parser: "eslint", input: "Oops! Something went wrong!", wantErr: true},
		{name: "golangci fail", parser: "golangci-lint", file: "golangci_fail.json", want: want{
			n: 2, errs: 2,
			first:    Finding{File: "cmd/fixture/main.go", Line: 13, Column: 9, Rule: "errcheck", Message: "`f.Close` is not checked"},
			contains: []string{"govet printf: fmt.Printf call needs 1 arg but has 2 args"}}},
		{name: "golangci pass", parser: "golangci-lint", file: "golangci_pass.json", want: want{}},
		{name: "golangci garbage", parser: "golangci-lint", input: "level=error msg=\"typechecking error\"", wantErr: true},
		{name: "tsc fail", parser: "tsc", file: "tsc_fail.txt", want: want{
			n: 3, errs: 3,
			first:    Finding{File: "src/index.ts", Line: 1, Column: 19, Rule: "TS2307", Message: "Cannot find module 'dayjs'"},
			contains: []string{"TS2741 Property 'title' is missing", "TS2322"}, summary: "3 errors"}},
		{name: "tsc pass", parser: "tsc", file: "tsc_pass.txt", want: want{}},
		{name: "go test fail (subtest)", parser: "go-test", file: "gotest_fail.jsonl", repoDir: "testdata/fixtures/go", want: want{
			n: 1, errs: 1,
			first:   Finding{File: "calc/calc_test.go", Line: 18, Rule: "TestDiv/by_zero", Message: "Div(1, 1) = 1, want error"},
			summary: "1 test failed, 1 passed"}},
		{name: "go test fail without go.mod keeps the printed file", parser: "go-test", file: "gotest_fail.jsonl", want: want{
			n: 1, errs: 1, first: Finding{File: "calc_test.go", Line: 18}}},
		{name: "go test build fail", parser: "go-test", file: "gotest_buildfail.jsonl", want: want{
			n: 1, errs: 1, first: Finding{File: "calc/broken.go", Line: 4, Column: 9, Rule: "build", Message: "undefined: foo"},
			summary: "1 build failure"}},
		{name: "go test package panic", parser: "go-test", file: "gotest_panic.jsonl", want: want{
			n: 1, errs: 1, first: Finding{File: "panicky/panicky_test.go", Line: 5, Rule: "package", Message: "panic: init failed"}}},
		{name: "go test pass", parser: "go-test", file: "gotest_pass.jsonl", want: want{summary: "0 tests failed, 1 passed"}},
		{name: "go test without -json", parser: "go-test", input: "--- FAIL: TestX\nFAIL\n", wantErr: true},
		{name: "jest fail", parser: "jest", file: "jest_fail.json", want: want{
			n: 2, errs: 2,
			first:    Finding{File: "jest/broken.test.js", Rule: "suite", Message: "test suite failed to run: Cannot find module './nope'"},
			contains: []string{"sum adds negatives", "Expected: -3"}, summary: "1 test failed, 2 passed"}},
		{name: "jest assertion location from stack", parser: "jest", file: "jest_fail.json", want: want{
			n: 2, errs: 2, contains: []string{"sum adds negatives"}, first: Finding{File: "jest/broken.test.js"}}},
		{name: "vitest fail", parser: "vitest", file: "vitest_fail.json", want: want{
			n: 1, errs: 1, first: Finding{File: "vitest/fail.test.mjs", Line: 4, Column: 17, Rule: "multiplies", Message: "expected 6 to be 7"},
			summary: "1 test failed, 2 passed"}},
		{name: "vitest pass", parser: "vitest", file: "vitest_pass.json", want: want{summary: "0 tests failed, 2 passed"}},
		{name: "vitest stdout notice", parser: "vitest", file: "vitest_stdout_notice.txt", wantErr: true},
		{name: "jest garbage", parser: "jest", input: `{"foo":1}`, wantErr: true},
		{name: "jscpd duplicates (--absolute)", parser: "jscpd", file: "jscpd_report.json", want: want{
			n: 1, warns: 1, dupPct: 17.073170731707318,
			first:   Finding{File: "src/a.ts", Line: 1, Column: 20, Rule: "duplication", Message: "14 duplicated lines (typescript) also in src/b.ts:1-14"},
			summary: "17.07% duplicated"}},
		{name: "jscpd relative names", parser: "jscpd", file: "jscpd_relative.json", want: want{
			n: 1, warns: 1, first: Finding{File: "a.ts", Message: "also in b.ts:1-14"}}},
		{name: "jscpd clean", parser: "jscpd", file: "jscpd_clean.json", want: want{summary: "0.00% duplicated"}},
		{name: "jscpd missing stats", parser: "jscpd", input: `{}`, wantErr: true},
		{name: "gocyclo all", parser: "gocyclo", file: "gocyclo.txt", want: want{
			n: 10, errs: 10, first: Finding{File: "calc/calc.go", Line: 7, Column: 1, Rule: "gocyclo", Message: "calc.Div has cyclomatic complexity 2"}}},
		{name: "gocyclo threshold", parser: "gocyclo", file: "gocyclo.txt", maxComplexity: 10, want: want{
			n: 1, errs: 1, first: Finding{File: "calc/calc.go", Line: 15, Message: "function calc.Grade has cyclomatic complexity 14 (max 10)"},
			summary: "1 function over"}},
		{name: "gocyclo garbage", parser: "gocyclo", input: "flag provided but not defined: -x", wantErr: true},
		{name: "knip", parser: "knip", file: "knip.json", want: want{
			n: 8, errs: 8,
			first: Finding{File: "package.json", Line: 7, Column: 6, Rule: "dependencies", Message: "unused dependency: lodash"},
			contains: []string{"files unused file", "unlisted dependency: dayjs", "unused export: formatDate", "unused export: scorer",
				"unused exported type: DateFmt", "unused enum member: Purple"}}},
		{name: "knip legacy top-level files", parser: "knip", input: `{"files":["src/old.ts"],"issues":[{"file":"src/old.ts","files":[{"name":"src/old.ts"}]}]}`, want: want{
			n: 1, errs: 1, first: Finding{File: "src/old.ts", Rule: "files", Message: "unused file"}}},
		{name: "knip clean", parser: "knip", file: "knip_clean.json", want: want{}},
		{name: "knip garbage", parser: "knip", input: "not json", wantErr: true},
		{name: "deadcode", parser: "deadcode", file: "deadcode.txt", want: want{
			n: 2, errs: 2, first: Finding{File: "util/strings.go", Line: 7, Column: 6, Rule: "unreachable-func", Message: "unreachable func: Reverse"}}},
		{name: "deadcode clean", parser: "deadcode", input: "", want: want{}},
		{name: "none parser", parser: "none", input: "whatever", want: want{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := []byte(tc.input)
			if tc.file != "" {
				in = readTestdata(t, tc.file)
			}
			repo := "/repo"
			if tc.repoDir != "" {
				abs, err := filepath.Abs(tc.repoDir)
				if err != nil {
					t.Fatal(err)
				}
				repo = abs
			}
			out, err := Parse(tc.parser, ParseInput{Output: in, RepoDir: repo, WorkDir: repo, MaxComplexity: tc.maxComplexity})
			if tc.wantErr {
				if !errors.Is(err, ErrUnparseable) {
					t.Fatalf("want ErrUnparseable, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if len(out.Findings) != tc.want.n {
				t.Fatalf("findings = %d, want %d: %+v", len(out.Findings), tc.want.n, out.Findings)
			}
			e, w := countSeverities(out.Findings)
			if e != tc.want.errs || w != tc.want.warns {
				t.Errorf("errors/warnings = %d/%d, want %d/%d", e, w, tc.want.errs, tc.want.warns)
			}
			if tc.want.n > 0 {
				assertFinding(t, out.Findings[0], tc.want.first)
			}
			all := ""
			for _, f := range out.Findings {
				all += f.Rule + " " + f.Message + "\n"
			}
			for _, s := range tc.want.contains {
				if !strings.Contains(all, s) {
					t.Errorf("findings do not contain %q:\n%s", s, all)
				}
			}
			if !strings.Contains(out.Summary, tc.want.summary) {
				t.Errorf("summary = %q, want substring %q", out.Summary, tc.want.summary)
			}
			if tc.want.dupPct != 0 && (out.DuplicationPct == nil || *out.DuplicationPct != tc.want.dupPct) {
				t.Errorf("DuplicationPct = %v, want %v", out.DuplicationPct, tc.want.dupPct)
			}
			// Determinism: parsing twice gives identical results.
			again, _ := Parse(tc.parser, ParseInput{Output: in, RepoDir: repo, WorkDir: repo, MaxComplexity: tc.maxComplexity})
			for i := range out.Findings {
				if out.Findings[i] != again.Findings[i] {
					t.Fatalf("non-deterministic finding %d", i)
				}
			}
		})
	}
}

func assertFinding(t *testing.T, got, want Finding) {
	t.Helper()
	if want.File != "" && got.File != want.File {
		t.Errorf("file = %q, want %q", got.File, want.File)
	}
	if want.Line != 0 && got.Line != want.Line {
		t.Errorf("line = %d, want %d", got.Line, want.Line)
	}
	if want.Column != 0 && got.Column != want.Column {
		t.Errorf("column = %d, want %d", got.Column, want.Column)
	}
	if want.Rule != "" && got.Rule != want.Rule {
		t.Errorf("rule = %q, want %q", got.Rule, want.Rule)
	}
	if want.Severity != "" && got.Severity != want.Severity {
		t.Errorf("severity = %q, want %q", got.Severity, want.Severity)
	}
	if !strings.Contains(got.Message, want.Message) {
		t.Errorf("message = %q, want substring %q", got.Message, want.Message)
	}
}

func TestParseStripsANSIAndRelativizes(t *testing.T) {
	out, err := Parse("jest", ParseInput{Output: readTestdata(t, "jest_fail.json"), RepoDir: "/repo"})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range out.Findings {
		if strings.Contains(f.Message, "\x1b[") {
			t.Errorf("ANSI escape left in %q", f.Message)
		}
		if strings.HasPrefix(f.File, "/") {
			t.Errorf("absolute path left: %q", f.File)
		}
	}
	// Paths relative to a gate working_dir become repo-relative.
	out, err = Parse("tsc", ParseInput{Output: []byte("src/x.ts(1,2): error TS1: boom\n"), RepoDir: "/repo", WorkDir: "/repo/apps/web"})
	if err != nil {
		t.Fatal(err)
	}
	if got := out.Findings[0].File; got != "apps/web/src/x.ts" {
		t.Errorf("file = %q, want apps/web/src/x.ts", got)
	}
}

func TestParserNamesAndUnknown(t *testing.T) {
	names := strings.Join(ParserNames(), ",")
	for _, n := range []string{"eslint", "golangci-lint", "tsc", "go-test", "jest", "vitest", "jscpd", "gocyclo", "knip", "deadcode", "none"} {
		if !strings.Contains(names, n) {
			t.Errorf("parser %q not registered", n)
		}
	}
	if _, err := Parse("nope", ParseInput{}); err == nil {
		t.Error("expected error for unknown parser")
	}
}
