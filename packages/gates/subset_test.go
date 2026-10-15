package gates

import (
	"context"
	"errors"
	"testing"

	"github.com/alpine-dev/alpine/packages/shared/gate"
)

func subsetConfig(t *testing.T) Config {
	return mustConfig(t, Config{Gates: []GateConfig{
		{Spec: gate.Spec{ID: "lint", Run: "echo lint-broken >&2; exit 1"}},
		{Spec: gate.Spec{ID: "typecheck", Run: "true"}},
		{Spec: gate.Spec{ID: "unit", Run: "exit 1"}},
		{Spec: gate.Spec{ID: "off", Run: "exit 1"}, Enabled: ptr(false)},
	}})
}

func TestRunOnlySubset(t *testing.T) {
	r := NewRunner(nil)
	res, err := r.Run(context.Background(), RunRequest{JobID: "j", RepoDir: t.TempDir(), Config: subsetConfig(t), Only: []string{"typecheck", "typecheck"}})
	if err != nil {
		t.Fatal(err)
	}
	st := map[string]Status{}
	for _, g := range res.Gates {
		st[g.GateID] = g.Status
	}
	want := map[string]Status{"lint": StatusSkipped, "typecheck": StatusPass, "unit": StatusSkipped, "off": StatusSkipped}
	for id, s := range want {
		if st[id] != s {
			t.Errorf("%s = %s, want %s", id, st[id], s)
		}
	}
	if res.Status != StatusPass || res.GateRunsUsed != 1 {
		t.Errorf("run = %s used=%d", res.Status, res.GateRunsUsed)
	}

	// Selecting a failing gate still fails exactly as configured; selecting a
	// disabled gate does not enable it.
	res, _ = r.Run(context.Background(), RunRequest{JobID: "j", RepoDir: t.TempDir(), Config: subsetConfig(t), Only: []string{"lint", "off"}})
	if res.Status != StatusFail {
		t.Errorf("lint subset should fail, got %s", res.Status)
	}
	for _, g := range res.Gates {
		if g.GateID == "off" && g.Status != StatusSkipped {
			t.Errorf("disabled gate ran: %+v", g)
		}
	}
}

func TestRunOnlyUnknownGate(t *testing.T) {
	c := NewMemoryCounter()
	r := NewRunner(c)
	_, err := r.Run(context.Background(), RunRequest{JobID: "j", RepoDir: t.TempDir(), Config: subsetConfig(t), Only: []string{"lint", "lint-relaxed", "zzz"}})
	if !errors.Is(err, ErrUnknownGate) {
		t.Fatalf("want ErrUnknownGate, got %v", err)
	}
	if n, _ := c.Used(context.Background(), "j"); n != 0 {
		t.Errorf("an invalid request used %d gate runs", n)
	}
}

func TestSharedRunnerSubsetCannotAddOrLoosen(t *testing.T) {
	dir := newRepo(t, map[string]string{ConfigPath: "version: 1\ngates:\n  - id: lint\n    run: exit 1\n  - id: unit\n    run: \"true\"\n"})
	sr := SharedRunner{Runner: NewRunner(nil)} // checkout comes from req.RepoDir
	ctx := context.Background()

	// Loosening attempt: same id, permissive command, warn-only-ish fields are ignored.
	rep, err := sr.Run(ctx, gate.RunRequest{RunID: "r", BaseSHA: "main", RepoDir: dir, Gates: []gate.Spec{{ID: "lint", Run: "true", TimeoutSeconds: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Green() {
		t.Fatalf("subset request loosened the lint gate: %+v", rep)
	}
	for _, r := range rep.Results {
		if r.GateID == "unit" && r.Status != gate.StatusSkipped {
			t.Errorf("unit should be skipped in a lint-only rerun: %+v", r)
		}
	}
	// Adding attempt: a gate that is not in the base config.
	if _, err := sr.Run(ctx, gate.RunRequest{RunID: "r", BaseSHA: "main", RepoDir: dir, Gates: []gate.Spec{{ID: "extra", Run: "true"}}}); !errors.Is(err, ErrUnknownGate) {
		t.Errorf("want ErrUnknownGate, got %v", err)
	}
}
