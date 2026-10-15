package agents

import "testing"

func TestApplyPolicyOnlyTightens(t *testing.T) {
	ok := RefineOutput{BlastRadius: 2, Confidence: 0.9, Size: "S", Reproduced: true, AcceptanceCriteria: []string{"x"}}
	if d := ApplyPolicy(DefaultPolicy, ok, PrioritizeOutput{Decision: "ready"}); d.Decision != "ready" {
		t.Fatalf("expected ready, got %+v", d)
	}
	// A human decision from the model is never loosened.
	if d := ApplyPolicy(DefaultPolicy, ok, PrioritizeOutput{Decision: "needs_human"}); d.Decision != "needs_human" {
		t.Fatalf("policy must not loosen: %+v", d)
	}
	cases := map[string]RefineOutput{
		"br4":     {BlastRadius: 4, Confidence: 0.9, Size: "S", Reproduced: true, AcceptanceCriteria: []string{"x"}},
		"lowconf": {BlastRadius: 1, Confidence: 0.3, Size: "S", Reproduced: true, AcceptanceCriteria: []string{"x"}},
		"big":     {BlastRadius: 1, Confidence: 0.9, Size: "XL", Reproduced: true, AcceptanceCriteria: []string{"x"}},
		"norepro": {BlastRadius: 1, Confidence: 0.9, Size: "S", Reproduced: false, AcceptanceCriteria: []string{"x"}},
		"noac":    {BlastRadius: 1, Confidence: 0.9, Size: "S", Reproduced: true},
	}
	for name, r := range cases {
		if d := ApplyPolicy(DefaultPolicy, r, PrioritizeOutput{Decision: "ready"}); d.Decision != "needs_human" {
			t.Errorf("%s: expected needs_human, got %+v", name, d)
		}
	}
}
