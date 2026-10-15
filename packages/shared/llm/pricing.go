package llm

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
)

// Price is USD per 1M tokens.
type Price struct {
	InputPerMTok       float64 `json:"input" yaml:"input"`
	CachedInputPerMTok float64 `json:"cachedInput" yaml:"cachedInput"`
	OutputPerMTok      float64 `json:"output" yaml:"output"`
}

// Pricing maps model -> price. Used when the proxy does not report a cost.
type Pricing map[string]Price

// DefaultPricing holds draft list prices for the default OpenAI models (USD / 1M tokens). They are
// placeholders for metering/budgets; override with LLM_PRICING_FILE (JSON) to match your contract.
var DefaultPricing = Pricing{
	"gpt-5":        {InputPerMTok: 1.25, CachedInputPerMTok: 0.125, OutputPerMTok: 10},
	"gpt-5-mini":   {InputPerMTok: 0.25, CachedInputPerMTok: 0.025, OutputPerMTok: 2},
	"gpt-5-nano":   {InputPerMTok: 0.05, CachedInputPerMTok: 0.005, OutputPerMTok: 0.4},
	"gpt-4.1":      {InputPerMTok: 2, CachedInputPerMTok: 0.5, OutputPerMTok: 8},
	"gpt-4.1-mini": {InputPerMTok: 0.4, CachedInputPerMTok: 0.1, OutputPerMTok: 1.6},
	"o3":           {InputPerMTok: 2, CachedInputPerMTok: 0.5, OutputPerMTok: 8},
	"gpt-4.1-nano": {InputPerMTok: 0.1, CachedInputPerMTok: 0.025, OutputPerMTok: 0.4},
	"gpt-4o-mini":  {InputPerMTok: 0.15, CachedInputPerMTok: 0.075, OutputPerMTok: 0.6},
	"o4-mini":      {InputPerMTok: 1.1, CachedInputPerMTok: 0.275, OutputPerMTok: 4.4},
}

// dated matches the "-YYYY-MM-DD" snapshot suffix OpenAI returns in response.model.
var dated = regexp.MustCompile(`-\d{4}-\d{2}-\d{2}$`)

// Cost computes the USD cost of usage on model; unknown models cost 0 (and are logged by the caller).
func (p Pricing) Cost(model string, u Usage) (float64, bool) {
	pr, ok := p[model]
	if !ok {
		// Responses name the dated snapshot (gpt-4.1-2025-04-14); price it as its alias.
		if pr, ok = p[dated.ReplaceAllString(model, "")]; !ok {
			return 0, false
		}
	}
	uncached := u.InputTokens - u.CachedTokens
	if uncached < 0 {
		uncached = 0
	}
	return (float64(uncached)*pr.InputPerMTok + float64(u.CachedTokens)*pr.CachedInputPerMTok +
		float64(u.OutputTokens)*pr.OutputPerMTok) / 1e6, true
}

// LoadPricing reads a JSON pricing file and merges it over DefaultPricing.
func LoadPricing(path string) (Pricing, error) {
	out := Pricing{}
	for k, v := range DefaultPricing {
		out[k] = v
	}
	if path == "" {
		return out, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read pricing file: %w", err)
	}
	var extra Pricing
	if err := json.Unmarshal(raw, &extra); err != nil {
		return nil, fmt.Errorf("parse pricing file: %w", err)
	}
	for k, v := range extra {
		out[k] = v
	}
	return out, nil
}
