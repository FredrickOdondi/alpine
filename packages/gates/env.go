package gates

import (
	"os"
	"sort"
)

// gateEnv builds the environment for a gate command from an explicit
// allowlist. Gate commands do NOT inherit the runner's environment: they get
//
//   - PATH and HOME from the runner process (so tools resolve and caches work),
//   - the variables named in gate_runner.env_allowlist, copied from the
//     runner process when set,
//   - Runner.ExtraEnv (explicit KEY=value pairs injected by the backend, e.g.
//     gates_only repo secrets),
//   - fixed CI settings (CI, NO_COLOR, FORCE_COLOR) and ALPINE_BASE_REF,
//     ALPINE_GATE_ID, ALPINE_SIDE ("head" or "base"),
//   - the gate's own `env` values (literal, from the base-branch config).
//
// Later entries win. The result is sorted by key for determinism.
func (r *Runner) gateEnv(cfg Config, g GateConfig, baseRef, side string) []string {
	env := map[string]string{}
	for _, k := range []string{"PATH", "HOME"} {
		if v, ok := os.LookupEnv(k); ok {
			env[k] = v
		}
	}
	for _, k := range cfg.Runner.EnvAllowlist {
		if v, ok := os.LookupEnv(k); ok {
			env[k] = v
		}
	}
	for _, kv := range r.ExtraEnv {
		for i := 0; i < len(kv); i++ {
			if kv[i] == '=' {
				env[kv[:i]] = kv[i+1:]
				break
			}
		}
	}
	env["CI"] = "true"
	env["NO_COLOR"] = "1"
	env["FORCE_COLOR"] = "0"
	env["ALPINE_BASE_REF"] = baseRef
	env["ALPINE_GATE_ID"] = g.ID
	env["ALPINE_SIDE"] = side
	for k, v := range g.Env {
		env[k] = v
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}
