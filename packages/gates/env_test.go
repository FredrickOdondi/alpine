package gates

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/alpine-dev/alpine/packages/shared/gate"
)

func TestGateEnvIsAllowlistOnly(t *testing.T) {
	t.Setenv("ALPINE_TEST_SECRET", "s3cret-value")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "aws-s3cret")
	t.Setenv("ALLOWED_VAR", "ok")
	repo := t.TempDir()
	cfg := mustConfig(t, Config{
		Runner: RunnerSettings{EnvAllowlist: []string{"ALLOWED_VAR", "NOT_SET_ANYWHERE"}},
		Gates: []GateConfig{{Spec: gate.Spec{ID: "custom", Run: "env > envdump.txt"},
			Env: map[string]string{"GATE_LITERAL": "lit"}}},
	})
	r := NewRunner(nil)
	r.ExtraEnv = []string{"INJECTED_GATES_ONLY=x"}
	res, err := r.Run(context.Background(), RunRequest{JobID: "j", RepoDir: repo, BaseRef: "main", Config: cfg})
	if err != nil || res.Status != StatusPass {
		t.Fatalf("run: %v %+v", err, res)
	}
	dump, err := os.ReadFile(filepath.Join(repo, "envdump.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(dump), "s3cret") {
		t.Fatalf("an unlisted secret reached the gate:\n%s", dump)
	}
	got := map[string]string{}
	var keys []string
	for _, line := range strings.Split(strings.TrimSpace(string(dump)), "\n") {
		k, v, _ := strings.Cut(line, "=")
		got[k] = v
		keys = append(keys, k)
	}
	sort.Strings(keys)
	allowed := map[string]bool{
		"PATH": true, "HOME": true, "ALLOWED_VAR": true, "INJECTED_GATES_ONLY": true, "GATE_LITERAL": true,
		"CI": true, "NO_COLOR": true, "FORCE_COLOR": true, "ALPINE_BASE_REF": true, "ALPINE_GATE_ID": true, "ALPINE_SIDE": true,
		"PWD": true, "SHLVL": true, "_": true, "OLDPWD": true, // added by sh itself
	}
	for _, k := range keys {
		if !allowed[k] {
			t.Errorf("unexpected variable %s reached the gate (keys: %v)", k, keys)
		}
	}
	for k, want := range map[string]string{"ALLOWED_VAR": "ok", "GATE_LITERAL": "lit", "INJECTED_GATES_ONLY": "x", "ALPINE_SIDE": "head", "ALPINE_BASE_REF": "main"} {
		if got[k] != want {
			t.Errorf("%s = %q, want %q", k, got[k], want)
		}
	}
	if got["PATH"] != os.Getenv("PATH") {
		t.Error("PATH not passed through")
	}
	if _, ok := got["NOT_SET_ANYWHERE"]; ok {
		t.Error("unset allowlisted variable should stay unset")
	}
}

func TestEnvAllowlistValidation(t *testing.T) {
	for _, y := range []string{
		"gate_runner:\n  env_allowlist: [\"BAD-NAME\"]\n",
		"gates:\n  - {id: a, run: x, env: {\"1X\": y}}\n",
		"gate_runner:\n  unknown_key: 1\n",
	} {
		if _, err := ParseConfig([]byte(y)); err == nil {
			t.Errorf("expected validation error for %q", y)
		}
	}
}

// gate_runner.extra_env names get their values only from Runner.ExtraEnv (backend-injected),
// never from the runner process's own environment.
func TestExtraEnvComesFromBackendOnly(t *testing.T) {
	t.Setenv("GATES_ONLY_TOKEN", "from-process-env")
	t.Setenv("PLAYWRIGHT_BASE_URL", "from-process-env")
	repo := t.TempDir()
	cfg, err := ParseConfig([]byte("gate_runner:\n  extra_env: [GATES_ONLY_TOKEN, PLAYWRIGHT_BASE_URL]\ngates:\n  - {id: custom, run: env > envdump.txt}\n"))
	if err != nil {
		t.Fatal(err)
	}
	r := NewRunner(nil)
	r.ExtraEnv = []string{"GATES_ONLY_TOKEN=from-backend"}
	if res, err := r.Run(context.Background(), RunRequest{JobID: "j", RepoDir: repo, BaseRef: "main", Config: cfg}); err != nil || res.Status != StatusPass {
		t.Fatalf("run: %v %+v", err, res)
	}
	dump, err := os.ReadFile(filepath.Join(repo, "envdump.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(dump), "from-process-env") {
		t.Fatalf("an extra_env name was copied from the runner environment:\n%s", dump)
	}
	if !strings.Contains(string(dump), "GATES_ONLY_TOKEN=from-backend") {
		t.Fatalf("backend-injected value missing:\n%s", dump)
	}
}
