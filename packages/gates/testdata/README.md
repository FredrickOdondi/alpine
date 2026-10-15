# testdata

- `parsers/`: tool output that the parser tests read. The files were
  recorded from **real tool runs** on the fixture repos in `fixtures/`,
  using `record.sh`. Absolute paths are rewritten to `/repo` (fixture root)
  and `/tools` (tool install dir). Tests never run these tools.
- `fixtures/go`: a small Go module. It has an errcheck and a govet printf
  issue, a complex function, unreachable functions, a failing subtest, a
  package that panics in init, and a clean package. `record.sh` adds a
  broken file to a copy to record a build failure.
- `fixtures/ts`: a small TS/JS project. It has ESLint errors and warnings
  (complexity max 5), tsc errors, a jscpd clone (`src/a.ts` and
  `src/b.ts`), knip issues (an unused dependency, export, type and enum
  member, an unused file, and an unlisted dependency), plus failing
  jest and vitest tests.
- `fake/`: tiny shell scripts the runner tests use as gate commands.

Fixture code under `testdata/` is ignored by `go build`, `go vet` and
golangci-lint.

## Recorded with (2026-10-15)

| tool | version |
|------|---------|
| golangci-lint | 2.13.0 (built with go1.26.9) |
| gocyclo | github.com/fzipp/gocyclo v0.6.0 |
| deadcode | golang.org/x/tools/cmd/deadcode v0.52.0 |
| go (go test -json) | go1.24.4 |
| eslint | 10.12.0 (@eslint/js 10.0.1, typescript-eslint 8.71.2) |
| typescript (tsc) | 6.0.3 |
| jscpd | 5.4.1 |
| knip | 6.41.0 |
| vitest | 5.0.5 |
| jest | 30.5.2 |
| node | 22.23.3 |

To re-record, install the tools outside the repo and run:

    TOOLS_BIN=/path/to/go/bin NODE_TOOLS=/path/containing/node_modules sh testdata/record.sh

Then update the expectations in `parse_test.go` and this table.

## Things the recordings showed

- **jscpd 5** names files relative to the *scanned path*: `jscpd src`
  reports `a.ts`, not `src/a.ts`. Gates should pass `--absolute`; the parser
  makes absolute paths repo-relative. Both forms are recorded
  (`jscpd_report.json` uses `--absolute`, `jscpd_relative.json` does not).
  jscpd also writes its report to `<output>/jscpd-report.json`, never to
  stdout, so use `report_file`.
- **vitest 5** `--reporter=json` writes `.vitest/json/output.json` and only
  prints a notice on stdout (`vitest_stdout_notice.txt`). Use
  `--outputFile=<file>` plus `report_file`. The parser's error now says so.
- **knip 6** reports unused files as an issue category (`files: [{name}]`),
  not as a top-level `files` array.
- **go test -json** prints `t.Error` locations relative to the package dir
  (`calc_test.go:18`). The parser now uses the module path in `go.mod` to
  make them repo-relative, and points init panics at the first stack frame
  in the repo.
- **jest 30** omits assertion locations unless `--testLocationInResults` is
  set. The parser now reads the line and column from the failure stack.
- **golangci-lint 2** leaves `Severity` empty unless severity rules are
  configured, so all its issues are errors.
- **tsc 6** refuses to type-check files named on the command line when a
  `tsconfig.json` is present (TS5112). Use `-p <tsconfig>`.
