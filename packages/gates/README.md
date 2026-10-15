# packages/gates

Deterministic quality gates for Alpine. The package runs the repo's configured lint, typecheck,
test, duplication, complexity, dead-code and visual gates against an agent's checkout. It
normalizes their output into one result type and produces short feedback for the agent. See
`docs/architecture.md` (sections 5.2 and 6).

```go
cfg, err := gates.LoadConfigFromBase(ctx, repoDir, baseSHA) // git show <base>:.alpine/factory.yaml
runner := gates.NewRunner(counter)                           // counter persists gate runs per job
runner.Baselines = gates.DirBaselineCache{Dir: cacheDir}     // optional: on-disk ratchet baselines
runner.ExtraEnv = []string{"NPM_TOKEN=" + tok}               // optional: explicit gates-only secrets
res, err := runner.Run(ctx, gates.RunRequest{
	JobID: runID, RepoDir: repoDir, BaseRef: baseSHA, Config: cfg,
	Only:         []string{"lint"},       // optional: rerun only these gates
	VisualRoutes: []string{"/settings"}, // optional: routes for `routes: auto` visual gates
})
if errors.Is(err, gates.ErrGateRunCapReached) { /* escalate to a human */ }
if errors.Is(err, gates.ErrUnknownGate) { /* Only named a gate not in the base config */ }
// res.Status: pass | fail | error; res.Gates[i] per gate; res.Feedback for the agent.
```

The orchestrator uses `SharedRunner`, which implements the shared `gate.Runner`. It runs in the
checkout at `gate.RunRequest.RepoDir`; when that is empty it falls back to `SharedRunner.RepoDir`,
and when both are empty it returns `ErrRepoDirRequired`. Config always comes from
`RunRequest.BaseSHA` (required), and `RunRequest.Gates` only selects a subset of the base gates.

## Config (`.alpine/factory.yaml` on the base branch)

```yaml
version: 1
limits:
  max_gate_iterations: 15       # gate-run cap per job (default 15)
gate_runner:
  run_timeout: 45m              # whole run incl. base-checkout baselines (default 45m)
  env_allowlist: [GOPATH, GOCACHE, GOFLAGS, NPM_CONFIG_CACHE, PNPM_HOME]
  extra_env: [PLAYWRIGHT_BASE_URL]   # gates-only names; values come ONLY from Runner.ExtraEnv (backend), never the runner env
  base_setup: pnpm install --frozen-lockfile --prefer-offline   # run once in the base checkout
  base_setup_timeout: 10m
gates:
  - id: lint-web
    parser: eslint              # eslint | golangci-lint | tsc | go-test | jest | vitest | jscpd | gocyclo | knip | deadcode | none
    run: pnpm exec eslint -f json .   # shell line (sh -c) ...
    working_dir: apps/web
    timeout: 5m                 # per gate (default 10m); the shared timeout_seconds: 300 also works
    max_warnings: 0
    env: {ESLINT_USE_FLAT_CONFIG: "true"}   # literal values, from the base config only
  - id: lint-go
    parser: golangci-lint
    command: golangci-lint      # ... or command + args (no shell)
    args: [run, --output.json.path=stdout, ./...]
  - id: typecheck
    parser: tsc
    run: pnpm exec tsc --noEmit --pretty false -p tsconfig.json
    working_dir: apps/web
  - id: unit-go
    parser: go-test
    run: go test -json ./...
  - id: unit-web
    parser: vitest
    run: pnpm exec vitest run --reporter=json --outputFile=.alpine-out/vitest.json
    report_file: .alpine-out/vitest.json      # vitest 5 doesn't print the JSON report to stdout
  - id: dupes
    parser: jscpd
    run: npx jscpd --absolute --reporters json --output .alpine-out/jscpd src
    report_file: .alpine-out/jscpd/jscpd-report.json   # read instead of stdout; deleted before the run
    max_new_duplication_pct: 0.5               # ratchet: may rise at most 0.5 points vs base
  - id: complexity-go
    parser: gocyclo
    run: gocyclo -over 15 .
    max_complexity: 15
    ratchet: true               # fail only on new or more complex functions
  - id: complexity-web
    type: complexity
    parser: eslint
    run: pnpm exec eslint -f json --rule 'complexity: [error, 10]' .
    only_rules: [complexity]
    ratchet: true
  - id: deadcode-go
    parser: deadcode
    run: deadcode ./...
    ratchet: true               # fail only on new dead code
  - id: deadcode-web
    parser: knip
    run: pnpm exec knip --reporter json
    ratchet: true
  - id: e2e                     # type e2e: accepted, reported as skipped (runs in the e2e sandbox)
    type: e2e
    artifacts: ["test-results/**"]
  - id: visual
    type: visual
    routes: auto                # or "/a, /b"; visual.routes wins when set
    warn_only: true             # recommended: intended UI changes need a human anyway
    visual:
      before_url: http://localhost:3001   # base preview (served by the orchestrator)
      after_url: http://localhost:3002    # head preview
      routes: [/, /settings]
      threshold_pct: 0.1        # max % of changed pixels per route (default 0.1)
      pixel_tolerance: 0.1      # per-channel tolerance 0..1 for anti-aliasing (default 0.1)
      viewport: {width: 1280, height: 720}
      full_page: true
      artifacts_dir: visual     # relative to Runner.ArtifactsRoot (default "visual")
      # screenshot_command: default is the Playwright CLI (npx --no-install playwright screenshot ...)
```

Gate types (`lint`, `typecheck`, `test`, `duplication`, `complexity`, `deadcode`, `other`,
`e2e`, `visual`) are inferred from the parser when omitted and define the run order; gates of the
same type keep config order. Unknown keys inside a gate or `gate_runner` are rejected.

## Environment

Gate commands do **not** inherit the runner's environment. Each gate gets only:

- `PATH` and `HOME`
- the variables named in `gate_runner.env_allowlist`, if they are set
- `Runner.ExtraEnv`, which is how the backend injects gates-only secrets. `gate_runner.extra_env`
  only declares which of those names the repo's gates expect (`MissingExtraEnv` reports gaps);
  those names are never copied from the runner's own environment
- `CI=true`, `NO_COLOR=1`, `FORCE_COLOR=0`, `ALPINE_BASE_REF`, `ALPINE_GATE_ID` and
  `ALPINE_SIDE` (`head` or `base`)
- the gate's own `env`

Later entries in that list win. `env_allowlist` names and `env` keys must be valid
variable names.

## Ratchet

A gate is ratcheted when it has a parser and either `ratchet: true` is set, or it is a
duplication gate with `max_new_duplication_pct`.

- **Base run.** The gate also runs on a temporary checkout of the base SHA, made with
  `git archive` into a temp dir. The agent's working dir, index, refs and worktrees are never
  touched. `gate_runner.base_setup` runs there once first.
- **Baseline cache.** Base results are cached by base SHA plus a hash of the gate config,
  allowlist and base_setup, behind `BaselineCache`. `MemoryBaselineCache` is the default;
  `DirBaselineCache` stores them on disk. Failures are not cached.
- **Matching.** Findings are matched as a multiset on line-independent keys:
  - gocyclo / ESLint complexity: file + function, then the complexity is compared
  - jscpd: file + other file + lines
  - everything else: file + rule + message, with line references stripped
- **What fails.** New findings and functions that got more complex fail. Pre-existing findings
  are kept as `info`.
- **Duplication.** It fails only when the total percentage rises by more than
  `max_new_duplication_pct` (a delta, default 0). New clones within the budget are warnings.
- **Ignored inputs.** Absolute thresholds, `max_warnings` and the exit code are ignored.
  `max_complexity` and `only_rules` filter both sides.
- **Errors.** If the base run cannot produce a baseline, the gate is `error` (escalate). A tool
  that exits non-zero without any findings fails.

## Subset reruns

`RunRequest.Only` (or `gate.RunRequest.Gates` in `SharedRunner`) reruns a subset, for example
only the gates that failed:

- Only ids are used. Each selected gate runs exactly as configured on the base branch, so a
  request can't add, change or loosen a gate.
- An unknown id returns `ErrUnknownGate` before a gate run is consumed.
- Gates that aren't selected are reported as `skipped`.

## Visual gate

For each route the gate:

1. asks the `Screenshotter` for a "before" screenshot (`before_url` + route) and an "after"
   screenshot (`after_url` + route);
2. diffs them pixel by pixel (`PixelDiff`, with a per-channel tolerance);
3. writes `NN-route.before.png`, `.after.png` and `.diff.png` under
   `<Runner.ArtifactsRoot>/<artifacts_dir>/<gate-id>/`;
4. adds them to `ArtifactKeys`.

A route fails when its changed-pixel percentage is above `threshold_pct`. A gate-level
`threshold:` (a fraction 0..1, as in the shared factory.yaml example) is also accepted and sets
`threshold_pct` to `threshold * 100`; set only one of them. The default
`CommandScreenshotter` runs `screenshot_command` (the Playwright CLI) with `ALPINE_URL`,
`ALPINE_OUT`, `ALPINE_VIEWPORT`, `ALPINE_FULL_PAGE` and `ALPINE_SIDE` set.

A `type: visual` gate without a `visual:` block is skipped, as are e2e gates.

## Status rules

- **Timeout:** the process group is SIGKILLed and the gate is `fail` with a `timeout` finding.
- **`error`:** the command can't start, the working dir is bad, the output is unparseable with
  exit 0, or the base baseline is unavailable.
- **Unparseable output with a non-zero exit:** `fail`, with the output tail.
- **Non-ratchet gates:** `fail` if there is any error-severity finding, a threshold is exceeded,
  warnings exceed `max_warnings`, or the exit code is non-zero. For threshold and `only_rules`
  gates, the parsed findings decide instead of the exit code.
- **`warn_only`:** turns `fail` into `warn`.
- **Run status:** any `fail` → `fail`; otherwise any `error` → `error`; otherwise `pass`.

## Shared types

Result types come from `packages/shared/gate`. Status, Severity and Finding are aliases.
`GateResult` embeds `gate.Result` and adds the type, parser, exit code, timeout flag and
feedback.

`SharedRunner` implements the shared `gate.Runner` interface for the orchestrator:

- loads the config from `RunRequest.BaseSHA`
- treats `RunRequest.Gates` ids as a subset filter
- checks the checkout is at `HeadSHA`
- uses `RunID` as the cap's job ID

Parser samples in `testdata/parsers` are real tool output; see `testdata/README.md` for the
tool versions and how to re-record them.
