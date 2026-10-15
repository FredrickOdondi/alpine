// Package gates runs Alpine's deterministic quality gates (lint, typecheck,
// tests, duplication, complexity, dead code, visual diff) against a checked-out repository
// and normalizes each tool's output into a common result type plus compact
// feedback for the implementing agent.
//
// Key properties:
//
//   - Gate configuration is loaded from the BASE ref (git show
//     <baseRef>:.alpine/factory.yaml), never from the agent's working tree, so
//     an agent cannot weaken its own gates. See LoadConfigFromBase.
//   - Gates run sequentially in a stable order (by gate type, then config
//     order); there is no randomness. Each gate has its own timeout and the
//     whole process group is killed when it expires.
//   - Every gate run for a job is counted against a cap (default 15,
//     limits.max_gate_iterations). See RunCounter.
//   - Gate commands get an explicit environment (PATH, HOME,
//     gate_runner.env_allowlist, Runner.ExtraEnv, per-gate env), never the
//     runner's own environment.
//   - Ratcheted gates also run on a temporary git-archive checkout of the
//     base SHA (cached per SHA and config) and fail only on regressions.
//   - RunRequest.Only reruns a subset of the base-config gates; it can never
//     add or loosen a gate.
//   - Visual gates diff before/after screenshots and write the images as
//     artifacts outside the repo.
//
// Result and config types build on packages/shared/gate (see result.go and
// GateConfig), and SharedRunner implements the shared gate.Runner interface.
package gates
