package gates

import (
	"errors"
	"os"
	"testing"
)

// The example config at the repo root must stay parseable by the gates package.
func TestRepoExampleConfig(t *testing.T) {
	data, err := os.ReadFile("../../.alpine/factory.yaml")
	if err != nil {
		t.Skip("no repo example config:", err)
	}
	cfg, err := ParseConfig(data)
	if err != nil {
		t.Fatalf("parse .alpine/factory.yaml: %v", err)
	}
	for _, g := range cfg.Gates {
		t.Logf("%s: type=%s parser=%s ratchet=%v", g.ID, g.Type, g.Parser, g.RatchetEnabled())
	}
}

// Keys used by the shared factory.yaml example: gate_runner.extra_env and a
// visual gate's fractional threshold.
func TestSharedExampleKeys(t *testing.T) {
	cfg, err := ParseConfig([]byte(`gate_runner:
  env_allowlist: [GOPATH]
  extra_env: [PLAYWRIGHT_BASE_URL]
gates:
  - id: visual
    routes: auto
    warn_only: true
    threshold: 0.01
    visual: {before_url: "http://b", after_url: "http://a"}
`))
	if err != nil {
		t.Fatal(err)
	}
	// extra_env names are backend-injected (Runner.ExtraEnv), never copied from the runner env.
	if got := cfg.Runner.EnvAllowlist; len(got) != 1 || got[0] != "GOPATH" {
		t.Errorf("extra_env must not be merged into env_allowlist: %v", got)
	}
	if got := cfg.Runner.ExtraEnv; len(got) != 1 || got[0] != "PLAYWRIGHT_BASE_URL" {
		t.Errorf("extra_env = %v", got)
	}
	if m := MissingExtraEnv(cfg, []string{"PLAYWRIGHT_BASE_URL=http://x"}); len(m) != 0 {
		t.Errorf("missing = %v", m)
	}
	if m := MissingExtraEnv(cfg, nil); len(m) != 1 {
		t.Errorf("missing = %v", m)
	}
	v := cfg.Gates[0]
	if !v.WarnOnly || v.Visual.ThresholdPct == nil || *v.Visual.ThresholdPct != 1 || v.Visual.threshold() != 1 {
		t.Errorf("threshold 0.01 should be 1%%: %+v", v.Visual)
	}
	for name, doc := range map[string]string{
		"non-visual": "gates:\n  - {id: lint, run: x, threshold: 0.1}\n",
		"range":      "gates:\n  - {id: visual, threshold: 5}\n",
		"both":       "gates:\n  - {id: visual, threshold: 0.1, visual: {before_url: \"http://b\", after_url: \"http://a\", threshold_pct: 1}}\n",
		"bad env":    "gate_runner: {extra_env: [\"1BAD\"]}\n",
		"overlap":    "gate_runner: {env_allowlist: [X], extra_env: [X]}\n",
	} {
		if _, err := ParseConfig([]byte(doc)); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("%s: want ErrInvalidConfig, got %v", name, err)
		}
	}
}
