package selectrunner

import "testing"

func TestFromEnv(t *testing.T) {
	t.Setenv("RUNNER", "")
	t.Setenv("E2B_API_KEY", "")
	if r, _ := FromEnv(); r.Name() != "docker" {
		t.Fatal(r.Name())
	}
	t.Setenv("E2B_API_KEY", "e2b_x")
	if r, _ := FromEnv(); r.Name() != "e2b" {
		t.Fatal(r.Name())
	}
	t.Setenv("RUNNER", "nope")
	if _, err := FromEnv(); err == nil {
		t.Fatal("expected error")
	}
}
