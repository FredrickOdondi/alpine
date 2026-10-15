package gates

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/alpine-dev/alpine/packages/shared/gate"
)

// ConfigPath is the repo-relative path of the factory config.
const ConfigPath = ".alpine/factory.yaml"

// Defaults.
const (
	DefaultGateRunCap  = 15
	DefaultGateTimeout = 10 * time.Minute
	DefaultRunTimeout  = 45 * time.Minute
)

var (
	// ErrConfigNotFound is returned when .alpine/factory.yaml does not exist at the base ref.
	ErrConfigNotFound = errors.New("gates: " + ConfigPath + " not found on base ref")
	// ErrBaseRefNotFound is returned when the base ref cannot be resolved to a commit.
	ErrBaseRefNotFound = errors.New("gates: base ref not found")
	// ErrInvalidConfig wraps schema/validation problems.
	ErrInvalidConfig = errors.New("gates: invalid config")
)

// Duration is a time.Duration that unmarshals from YAML strings like "90s" or "5m".
type Duration time.Duration

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return fmt.Errorf("duration: %w", err)
	}
	v, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil {
		return fmt.Errorf("duration %q: %w", s, err)
	}
	if v < 0 {
		return fmt.Errorf("duration %q: must not be negative", s)
	}
	*d = Duration(v)
	return nil
}

// Config is the gates-relevant subset of .alpine/factory.yaml. Other top-level
// sections (rules, environment, layers, models, merge_policy, ...) are ignored here.
type Config struct {
	Version int          `yaml:"version"`
	Gates   []GateConfig `yaml:"gates"`
	Limits  Limits       `yaml:"limits"`
	// Runner holds gate-runner-wide settings (top-level `gate_runner:`).
	Runner RunnerSettings `yaml:"gate_runner"`
}

// RunnerSettings are global settings for the gate runner (`gate_runner:`).
type RunnerSettings struct {
	// EnvAllowlist names environment variables that are copied from the
	// runner's own environment into every gate command. Nothing else is
	// inherited except PATH and HOME; per-gate `env` adds literal values.
	EnvAllowlist []string `yaml:"env_allowlist"`
	// ExtraEnv (`extra_env`, the name used in the shared factory.yaml example)
	// declares the gates-only variables (e.g. PLAYWRIGHT_BASE_URL or a test
	// secret) the repo's gates expect the backend to provide. Their VALUES come
	// only from Runner.ExtraEnv (injected by the backend, never visible to the
	// implementer sandbox). They are deliberately NOT added to EnvAllowlist:
	// that would copy them from the runner process's own environment, which is
	// not where gates-only secrets live and may hold backend credentials.
	// Names are validated; a declared name the backend doesn't supply stays
	// unset (see MissingExtraEnv).
	ExtraEnv []string `yaml:"extra_env"`
	// RunTimeout bounds one whole run of all gates, including baseline runs
	// on the base checkout (default 45m).
	RunTimeout Duration `yaml:"run_timeout"`
	// BaseSetup is an optional shell command run once in the temporary base
	// checkout before any baseline gate runs there (e.g. "pnpm install
	// --frozen-lockfile --offline"), since the base checkout starts without
	// node_modules or build output.
	BaseSetup string `yaml:"base_setup"`
	// BaseSetupTimeout bounds BaseSetup (default 10m).
	BaseSetupTimeout Duration `yaml:"base_setup_timeout"`
}

// Limits holds global caps. Only gate-related fields are read here.
type Limits struct {
	// MaxGateIterations caps how many gate runs a job may use (default 15).
	MaxGateIterations int `yaml:"max_gate_iterations"`
}

// GateConfig configures a single gate. It embeds the shared gate.Spec
// (id, run, ratchet, max_new_duplication_pct, artifacts, routes,
// timeout_seconds) and adds the fields this runner needs.
type GateConfig struct {
	gate.Spec `yaml:",inline"`

	// Type is the gate category. If empty it is inferred from Parser (or from
	// the id when it is a type name, e.g. "lint").
	Type GateType `yaml:"type"`
	// Enabled defaults to true.
	Enabled *bool `yaml:"enabled"`
	// Parser selects the output parser (eslint, golangci-lint, tsc, go-test,
	// jest, vitest, jscpd, gocyclo, knip, deadcode, none). Default: none
	// (exit code only).
	Parser string `yaml:"parser"`
	// Exactly one of Spec.Run (a shell command line, executed with `sh -c`)
	// or Command (+Args, executed directly without a shell) must be set.
	Command string   `yaml:"command"`
	Args    []string `yaml:"args"`
	// WorkingDir is relative to the repo root (default: repo root). It must
	// not escape the repo.
	WorkingDir string `yaml:"working_dir"`
	// Env is extra environment (KEY: value) for the command.
	Env map[string]string `yaml:"env"`
	// Timeout for this gate as a duration string ("90s", "5m"). The shared
	// timeout_seconds is also accepted; set at most one. Default 10m.
	Timeout Duration `yaml:"timeout"`
	// ReportFile, if set, is read (relative to WorkingDir) instead of stdout
	// for parsing, for tools that write their JSON report to a file (jscpd).
	ReportFile string `yaml:"report_file"`
	// WarnOnly turns a failure into StatusWarn (does not block the run).
	WarnOnly bool `yaml:"warn_only"`
	// OnlyRules keeps only findings whose rule is listed (e.g. [complexity]
	// for an ESLint complexity gate).
	OnlyRules []string `yaml:"only_rules"`
	// Thresholds.
	MaxDuplicationPct *float64 `yaml:"max_duplication_pct"` // duplication: fail if total % > this (default 0)
	MaxComplexity     int      `yaml:"max_complexity"`      // complexity (gocyclo): report functions with complexity > this
	MaxWarnings       *int     `yaml:"max_warnings"`        // fail if warning-level findings exceed this (default: unlimited)
	// Threshold (visual gates only) is the allowed fraction (0..1) of changed
	// pixels per route, the form used in the shared factory.yaml example. It
	// sets visual.threshold_pct (Threshold*100); set at most one of the two.
	Threshold *float64 `yaml:"threshold"`

	// Visual configures a type: visual gate (see visual.go).
	Visual *VisualConfig `yaml:"visual"`

	// Shared Spec fields:
	//   - ratchet: compare against the same gate run on the base SHA and fail
	//     only on regressions (see ratchet.go). Needs a parser.
	//   - max_new_duplication_pct: allowed rise in duplication percentage vs
	//     base (implies ratchet for duplication gates).
	//   - routes: "auto" lets the orchestrator pass routes for visual gates.
	//   - artifacts: used by the e2e sandbox.
}

// IsEnabled reports whether the gate is enabled (default true).
func (g GateConfig) IsEnabled() bool { return g.Enabled == nil || *g.Enabled }

// EffectiveTimeout returns the gate timeout (timeout or timeout_seconds) or the default.
func (g GateConfig) EffectiveTimeout() time.Duration {
	switch {
	case g.Timeout > 0:
		return time.Duration(g.Timeout)
	case g.TimeoutSeconds > 0:
		return time.Duration(g.TimeoutSeconds) * time.Second
	}
	return DefaultGateTimeout
}

// RatchetEnabled reports whether the gate is evaluated against the base SHA.
// Duplication gates with max_new_duplication_pct are ratcheted implicitly.
func (g GateConfig) RatchetEnabled() bool {
	if g.Parser == "" || g.Parser == "none" || g.Type == TypeVisual || g.Type == TypeE2E {
		return false
	}
	return g.Ratchet || (g.Type == TypeDuplication && g.MaxNewDuplicationPct != nil)
}

// GateRunCap returns the configured cap or the default (15).
func (c Config) GateRunCap() int {
	if c.Limits.MaxGateIterations > 0 {
		return c.Limits.MaxGateIterations
	}
	return DefaultGateRunCap
}

// RunTimeout returns the whole-run timeout or the default.
func (c Config) RunTimeout() time.Duration {
	if c.Runner.RunTimeout > 0 {
		return time.Duration(c.Runner.RunTimeout)
	}
	return DefaultRunTimeout
}

// ParseConfig parses and validates factory.yaml content. Unknown top-level
// sections are ignored; unknown keys inside a gate are rejected so typos
// (e.g. "timout") don't silently weaken a gate.
func ParseConfig(data []byte) (Config, error) {
	var raw struct {
		Version int       `yaml:"version"`
		Gates   yaml.Node `yaml:"gates"`
		Limits  Limits    `yaml:"limits"`
		Runner  yaml.Node `yaml:"gate_runner"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return Config{}, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	cfg := Config{Version: raw.Version, Limits: raw.Limits}
	if err := decodeStrict(&raw.Gates, &cfg.Gates); err != nil {
		return Config{}, fmt.Errorf("%w: gates: %v", ErrInvalidConfig, err)
	}
	if err := decodeStrict(&raw.Runner, &cfg.Runner); err != nil {
		return Config{}, fmt.Errorf("%w: gate_runner: %v", ErrInvalidConfig, err)
	}
	if err := cfg.normalizeAndValidate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// decodeStrict re-encodes a node and decodes it rejecting unknown keys.
func decodeStrict(n *yaml.Node, out any) error {
	if n.Kind == 0 {
		return nil
	}
	b, err := yaml.Marshal(n)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	return dec.Decode(out)
}

var envNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (c *Config) normalizeAndValidate() error {
	var errs []string
	if c.Version != 0 && c.Version != 1 {
		errs = append(errs, fmt.Sprintf("unsupported version %d (want 1)", c.Version))
	}
	if c.Limits.MaxGateIterations < 0 {
		errs = append(errs, "limits.max_gate_iterations must be >= 0")
	}
	for _, name := range c.Runner.EnvAllowlist {
		if !envNameRE.MatchString(name) {
			errs = append(errs, fmt.Sprintf("gate_runner.env_allowlist: invalid variable name %q", name))
		}
	}
	for _, name := range c.Runner.ExtraEnv {
		if !envNameRE.MatchString(name) {
			errs = append(errs, fmt.Sprintf("gate_runner.extra_env: invalid variable name %q", name))
		}
		if containsString(c.Runner.EnvAllowlist, name) {
			errs = append(errs, fmt.Sprintf("gate_runner.extra_env: %q is also in env_allowlist; gates-only values come from the backend, not the runner environment", name))
		}
	}
	seen := map[string]bool{}
	for i := range c.Gates {
		g := &c.Gates[i]
		where := fmt.Sprintf("gates[%d]", i)
		if g.ID == "" {
			errs = append(errs, where+": id is required")
		} else {
			where = fmt.Sprintf("gates[%d] (%s)", i, g.ID)
			if seen[g.ID] {
				errs = append(errs, where+": duplicate id")
			}
			seen[g.ID] = true
		}
		g.Parser = strings.ToLower(strings.TrimSpace(g.Parser))
		if g.Parser != "" && g.Parser != "none" {
			if _, ok := parsers[g.Parser]; !ok {
				errs = append(errs, fmt.Sprintf("%s: unknown parser %q (known: %s)", where, g.Parser, strings.Join(ParserNames(), ", ")))
			}
		}
		if g.Type == "" {
			g.Type = inferType(g.Parser, g.ID)
		}
		if _, ok := typeOrder[g.Type]; !ok {
			errs = append(errs, fmt.Sprintf("%s: unknown type %q", where, g.Type))
		}
		handled := g.Type != TypeE2E && g.Type != TypeVisual // visual uses visual.screenshot_command
		if handled && g.IsEnabled() {
			switch {
			case g.Run == "" && g.Command == "":
				errs = append(errs, where+": one of run or command is required")
			case g.Run != "" && g.Command != "":
				errs = append(errs, where+": set only one of run or command")
			case g.Run != "" && len(g.Args) > 0:
				errs = append(errs, where+": args are only valid with command")
			}
		}
		if err := checkRelPath(g.WorkingDir); err != nil {
			errs = append(errs, fmt.Sprintf("%s: working_dir: %v", where, err))
		}
		if err := checkRelPath(g.ReportFile); err != nil {
			errs = append(errs, fmt.Sprintf("%s: report_file: %v", where, err))
		}
		if g.MaxNewDuplicationPct != nil && *g.MaxNewDuplicationPct < 0 {
			errs = append(errs, where+": max_new_duplication_pct must be >= 0")
		}
		for k := range g.Env {
			if !envNameRE.MatchString(k) {
				errs = append(errs, fmt.Sprintf("%s: env: invalid variable name %q", where, k))
			}
		}
		if g.Threshold != nil {
			switch {
			case g.Type != TypeVisual:
				errs = append(errs, where+": threshold is only valid for type: visual")
			case *g.Threshold < 0 || *g.Threshold > 1:
				errs = append(errs, where+": threshold must be within 0..1 (fraction of changed pixels)")
			case g.Visual != nil && g.Visual.ThresholdPct != nil:
				errs = append(errs, where+": set only one of threshold or visual.threshold_pct")
			case g.Visual != nil:
				pct := *g.Threshold * 100
				g.Visual.ThresholdPct = &pct
			}
		}
		if g.Visual != nil {
			if g.Type != TypeVisual {
				errs = append(errs, where+": visual is only valid for type: visual")
			} else {
				for _, e := range g.Visual.validate() {
					errs = append(errs, where+": visual: "+e)
				}
			}
		}
		if g.MaxDuplicationPct != nil && (*g.MaxDuplicationPct < 0 || *g.MaxDuplicationPct > 100) {
			errs = append(errs, where+": max_duplication_pct must be within 0..100")
		}
		if g.Timeout > 0 && g.TimeoutSeconds > 0 {
			errs = append(errs, where+": set only one of timeout or timeout_seconds")
		}
		if g.TimeoutSeconds < 0 {
			errs = append(errs, where+": timeout_seconds must be >= 0")
		}
		if g.MaxComplexity < 0 {
			errs = append(errs, where+": max_complexity must be >= 0")
		}
		if g.MaxWarnings != nil && *g.MaxWarnings < 0 {
			errs = append(errs, where+": max_warnings must be >= 0")
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%w: %s", ErrInvalidConfig, strings.Join(errs, "; "))
	}
	return nil
}

func inferType(parser, id string) GateType {
	switch parser {
	case "eslint", "golangci-lint":
		return TypeLint
	case "tsc":
		return TypeTypecheck
	case "go-test", "jest", "vitest":
		return TypeTest
	case "jscpd":
		return TypeDuplication
	case "gocyclo":
		return TypeComplexity
	case "knip", "deadcode":
		return TypeDeadCode
	}
	if _, ok := typeOrder[GateType(id)]; ok {
		return GateType(id)
	}
	return TypeOther
}

func checkRelPath(p string) error {
	if p == "" {
		return nil
	}
	if strings.HasPrefix(p, "/") {
		return errors.New("must be relative to the repo root")
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return errors.New("must not contain '..'")
		}
	}
	return nil
}

// LoadConfigFromBase reads .alpine/factory.yaml from baseRef (NOT the working
// tree) in the git repository at repoDir, via `git show <baseRef>:<path>`, and
// parses it. This is what prevents an agent from weakening its own gates by
// editing the config on its branch.
func LoadConfigFromBase(ctx context.Context, repoDir, baseRef string) (Config, error) {
	data, err := ReadFileAtRef(ctx, repoDir, baseRef, ConfigPath)
	if err != nil {
		return Config{}, err
	}
	cfg, err := ParseConfig(data)
	if err != nil {
		return Config{}, fmt.Errorf("%s at %s: %w", ConfigPath, baseRef, err)
	}
	return cfg, nil
}

// ReadFileAtRef returns the content of path at ref using `git show ref:path`.
func ReadFileAtRef(ctx context.Context, repoDir, ref, path string) ([]byte, error) {
	if err := validateRef(ref); err != nil {
		return nil, err
	}
	// Resolve the ref first so a bad ref and a missing file give distinct errors.
	if _, err := runGit(ctx, repoDir, "rev-parse", "--verify", "--quiet", "--end-of-options", ref+"^{commit}"); err != nil {
		return nil, fmt.Errorf("%w: %q in %s", ErrBaseRefNotFound, ref, repoDir)
	}
	out, err := runGit(ctx, repoDir, "show", "--no-textconv", ref+":"+path)
	if err != nil {
		// git exits 128 with "path ... does not exist in ..." / "exists on disk, but not in ...".
		if strings.Contains(err.Error(), "does not exist") || strings.Contains(err.Error(), "not in '") {
			return nil, fmt.Errorf("%w: %s:%s (gates config must be committed on the base branch)", ErrConfigNotFound, ref, path)
		}
		return nil, fmt.Errorf("gates: git show %s:%s: %w", ref, path, err)
	}
	return out, nil
}

func validateRef(ref string) error {
	switch {
	case ref == "":
		return fmt.Errorf("%w: empty base ref", ErrBaseRefNotFound)
	case strings.HasPrefix(ref, "-"), strings.ContainsAny(ref, ": \t\n\x00"):
		return fmt.Errorf("%w: invalid base ref %q", ErrBaseRefNotFound, ref)
	}
	return nil
}

func runGit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// MissingExtraEnv returns the names declared in gate_runner.extra_env that are
// not present in the backend-provided pairs (Runner.ExtraEnv), so callers can
// log or surface a misconfigured gates-only secret before gates run.
func MissingExtraEnv(cfg Config, provided []string) []string {
	have := map[string]bool{}
	for _, kv := range provided {
		if k, _, ok := strings.Cut(kv, "="); ok {
			have[k] = true
		}
	}
	var missing []string
	for _, name := range cfg.Runner.ExtraEnv {
		if !have[name] {
			missing = append(missing, name)
		}
	}
	return missing
}
