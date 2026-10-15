package agents

import "fmt"

// Policy holds the deterministic prioritizer rules (product §4.3). Rules can only turn "ready"
// into "needs_human", never the reverse.
type Policy struct {
	MaxAgentBlastRadius int     // BR above this needs a human (BR4 needs an explicit allow)
	MinConfidence       float64 // below this needs a human
	MaxSize             string  // sizes above this need a human
	RequireReproForBugs bool
}

// DefaultPolicy matches the spec defaults.
var DefaultPolicy = Policy{MaxAgentBlastRadius: 3, MinConfidence: 0.6, MaxSize: "M", RequireReproForBugs: true}

var sizeRank = map[string]int{"XS": 1, "S": 2, "M": 3, "L": 4, "XL": 5}

// ApplyPolicy overlays the deterministic rules on the model's decision.
func ApplyPolicy(p Policy, r RefineOutput, d PrioritizeOutput) PrioritizeOutput {
	human := func(reason string) PrioritizeOutput {
		d.Decision = "needs_human"
		if d.Reason != "" {
			d.Reason = reason + "; " + d.Reason
		} else {
			d.Reason = reason
		}
		return d
	}
	switch {
	case d.NeedsProductDecision:
		return human("product decision needed")
	case d.ArchitectureChanging:
		return human("architecture-changing")
	case r.BlastRadius > p.MaxAgentBlastRadius:
		return human(fmt.Sprintf("BR%d exceeds agent policy (max BR%d)", r.BlastRadius, p.MaxAgentBlastRadius))
	case r.Confidence < p.MinConfidence:
		return human(fmt.Sprintf("low confidence %.2f", r.Confidence))
	case sizeRank[r.Size] > sizeRank[p.MaxSize]:
		return human("size " + r.Size + " above " + p.MaxSize)
	case p.RequireReproForBugs && !r.Reproduced:
		return human("reproduction failed")
	case len(r.AcceptanceCriteria) == 0:
		return human("no acceptance criteria")
	}
	return d
}
