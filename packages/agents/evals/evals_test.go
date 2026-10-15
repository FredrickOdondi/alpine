package evals

import (
	"context"
	"testing"
)

// TestEvals replays every fixture through the real gateway + agents (no network).
func TestEvals(t *testing.T) {
	fixtures, err := Load("fixtures")
	if err != nil {
		t.Fatal(err)
	}
	if len(fixtures) < 5 {
		t.Fatalf("expected at least 5 fixtures, got %d", len(fixtures))
	}
	for _, f := range fixtures {
		t.Run(f.Name, func(t *testing.T) {
			gw, p, err := ReplayGateway(f, DefaultRouting())
			if err != nil {
				t.Fatal(err)
			}
			res := Run(context.Background(), f, gw, func() int { return len(p.Calls) })
			for _, msg := range res.Failures {
				t.Error(msg)
			}
			for _, c := range p.Calls {
				if c.Schema == nil {
					t.Errorf("call without structured output schema")
				}
			}
		})
	}
}
