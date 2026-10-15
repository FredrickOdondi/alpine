package llm

import (
	"fmt"
	"strings"
)

// Route is the model configuration for one stage.
type Route struct {
	// Models is the fallback chain, primary first (e.g. ["gpt-5", "gpt-4.1"]).
	Models          []string
	MaxOutputTokens int
	Temperature     *float64
}

// Routing maps stages to routes. Model names are configuration (env / factory.yaml `models:`),
// never hardcoded in agent code.
type Routing map[Stage]Route

// Primary returns the primary model of a stage.
func (r Routing) Primary(s Stage) string {
	if rt, ok := r[s]; ok && len(rt.Models) > 0 {
		return rt.Models[0]
	}
	return ""
}

// Validate checks every stage has a chain and that the independent reviewer never runs on the
// implementer's model (the reviewer must be a different model than the implementer — with a
// single provider this replaces the spec's "different model family" rule).
func (r Routing) Validate() error {
	for _, s := range []Stage{StageTriage, StageRefine, StagePrioritize, StageImplement, StageReview} {
		rt, ok := r[s]
		if !ok || len(rt.Models) == 0 {
			return fmt.Errorf("llm routing: no models configured for stage %q", s)
		}
		for _, m := range rt.Models {
			if strings.TrimSpace(m) == "" {
				return fmt.Errorf("llm routing: empty model name in stage %q", s)
			}
		}
	}
	impl := r[StageImplement].Models
	for _, rm := range r[StageReview].Models {
		for _, im := range impl {
			if rm == im {
				return fmt.Errorf("llm routing: reviewer model %q is also an implementer model; the independent reviewer must use a different model", rm)
			}
		}
	}
	return nil
}

// ParseChain parses "gpt-5, gpt-4.1" into a chain.
func ParseChain(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
