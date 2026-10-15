// Package evals is an offline regression harness for agent prompts, schemas and model changes.
//
// Each fixture (fixtures/*.json) holds a ticket, the stage under test, RECORDED model responses
// and expectations. In replay mode (default, used by `go test`) the recorded responses are served
// by a ReplayProvider through the real model gateway — so schema validation, repair-retry,
// policy overlays and output parsing are all exercised with zero network calls. In live mode
// (`go run ./cmd/evals -live -update`) the same fixtures are run against the configured gateway
// (LiteLLM → OpenAI) and the recordings can be refreshed.
package evals

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/alpine-dev/alpine/packages/agents"
	"github.com/alpine-dev/alpine/packages/shared/llm"
)

// Recording is one recorded model response.
type Recording struct {
	Model   string    `json:"model"`
	Content string    `json:"content"`
	Usage   llm.Usage `json:"usage"`
}

// Expect lists assertions; zero values are not checked.
type Expect struct {
	Classification string `json:"classification,omitempty"`
	BlastRadiusMin int    `json:"blastRadiusMin,omitempty"`
	BlastRadiusMax int    `json:"blastRadiusMax,omitempty"`
	Priority       string `json:"priority,omitempty"`
	Decision       string `json:"decision,omitempty"`
	Verdict        string `json:"verdict,omitempty"`
	MinFindings    int    `json:"minFindings,omitempty"`
	// Calls is the expected number of model calls (1 + repairs).
	Calls int `json:"calls,omitempty"`
	// ReasonContains checks the decision/verdict reason text.
	ReasonContains string `json:"reasonContains,omitempty"`
}

// Fixture is one eval case.
type Fixture struct {
	Name     string               `json:"name"`
	Stage    llm.Stage            `json:"stage"`
	Ticket   agents.TicketInput   `json:"ticket"`
	Triage   *agents.TriageOutput `json:"triage,omitempty"`
	Refine   *agents.RefineOutput `json:"refine,omitempty"`
	Review   *agents.ReviewInput  `json:"review,omitempty"`
	Recorded []Recording          `json:"recorded"`
	Expect   Expect               `json:"expect"`

	path string
}

// Load reads all fixtures in dir, sorted by name.
func Load(dir string) ([]*Fixture, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	var out []*Fixture
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var f Fixture
		if err := json.Unmarshal(raw, &f); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		f.path = p
		out = append(out, &f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Save writes the fixture back (live -update mode).
func (f *Fixture) Save() error {
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(f.path, append(raw, '\n'), 0o644)
}

// ReplayProvider serves recorded responses in order.
type ReplayProvider struct {
	mu    sync.Mutex
	recs  []Recording
	Calls []llm.ProviderRequest
}

// NewReplay creates a replay provider.
func NewReplay(recs []Recording) *ReplayProvider { return &ReplayProvider{recs: recs} }

// Name implements llm.Provider.
func (r *ReplayProvider) Name() string { return "openai" }

// Complete implements llm.Provider.
func (r *ReplayProvider) Complete(_ context.Context, req llm.ProviderRequest) (llm.ProviderResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Calls = append(r.Calls, req)
	if len(r.recs) == 0 {
		return llm.ProviderResponse{}, &llm.APIError{StatusCode: 400, Code: "replay_exhausted", Message: "no more recorded responses"}
	}
	rec := r.recs[0]
	r.recs = r.recs[1:]
	model := rec.Model
	if model == "" {
		model = req.Model
	}
	return llm.ProviderResponse{Content: rec.Content, Model: model, Usage: rec.Usage, FinishReason: "stop"}, nil
}

// RecordingProvider wraps a live provider and captures responses (live -update mode).
type RecordingProvider struct {
	Inner llm.Provider
	mu    sync.Mutex
	Recs  []Recording
}

// Name implements llm.Provider.
func (r *RecordingProvider) Name() string { return r.Inner.Name() }

// Complete implements llm.Provider.
func (r *RecordingProvider) Complete(ctx context.Context, req llm.ProviderRequest) (llm.ProviderResponse, error) {
	resp, err := r.Inner.Complete(ctx, req)
	if err == nil {
		r.mu.Lock()
		r.Recs = append(r.Recs, Recording{Model: resp.Model, Content: resp.Content, Usage: resp.Usage})
		r.mu.Unlock()
	}
	return resp, err
}

// Result of one fixture.
type Result struct {
	Name     string
	Stage    llm.Stage
	Pass     bool
	Failures []string
	Calls    int
	CostUSD  float64
}

// Run executes one fixture with the given gateway and returns the result. calls reports how many
// provider calls happened (from the provider used to build gw).
func Run(ctx context.Context, f *Fixture, gw *llm.Gateway, calls func() int) Result {
	a := &agents.LLM{Gateway: gw}
	c := agents.Call{Tenant: llm.Tenant{OrgID: "eval-org", TicketID: "eval-" + f.Name}, IdempotencyKey: "eval/" + f.Name}
	res := Result{Name: f.Name, Stage: f.Stage}
	fail := func(format string, args ...any) { res.Failures = append(res.Failures, fmt.Sprintf(format, args...)) }
	e := f.Expect

	switch f.Stage {
	case llm.StageTriage:
		r, err := a.Triage(ctx, c, f.Ticket)
		if err != nil {
			fail("error: %v", err)
			break
		}
		res.CostUSD = r.CostUSD
		if e.Classification != "" && r.Output.Classification != e.Classification {
			fail("classification = %q, want %q", r.Output.Classification, e.Classification)
		}
	case llm.StageRefine:
		tr := agents.TriageOutput{Classification: "bug"}
		if f.Triage != nil {
			tr = *f.Triage
		}
		r, err := a.Refine(ctx, c, f.Ticket, tr)
		if err != nil {
			fail("error: %v", err)
			break
		}
		res.CostUSD = r.CostUSD
		if e.BlastRadiusMin > 0 && r.Output.BlastRadius < e.BlastRadiusMin {
			fail("blast radius %d < min %d", r.Output.BlastRadius, e.BlastRadiusMin)
		}
		if e.BlastRadiusMax > 0 && r.Output.BlastRadius > e.BlastRadiusMax {
			fail("blast radius %d > max %d", r.Output.BlastRadius, e.BlastRadiusMax)
		}
		if e.Priority != "" && r.Output.Priority != e.Priority {
			fail("priority = %q, want %q", r.Output.Priority, e.Priority)
		}
	case llm.StagePrioritize:
		if f.Refine == nil {
			fail("fixture needs refine context")
			break
		}
		r, err := a.Prioritize(ctx, c, f.Ticket, *f.Refine)
		if err != nil {
			fail("error: %v", err)
			break
		}
		res.CostUSD = r.CostUSD
		if e.Decision != "" && r.Output.Decision != e.Decision {
			fail("decision = %q, want %q (%s)", r.Output.Decision, e.Decision, r.Output.Reason)
		}
		if e.ReasonContains != "" && !strings.Contains(r.Output.Reason, e.ReasonContains) {
			fail("reason %q does not contain %q", r.Output.Reason, e.ReasonContains)
		}
	case llm.StageReview:
		if f.Review == nil {
			fail("fixture needs review input")
			break
		}
		r, err := a.Review(ctx, c, *f.Review)
		if err != nil {
			fail("error: %v", err)
			break
		}
		res.CostUSD = r.CostUSD
		if e.Verdict != "" && r.Output.Verdict != e.Verdict {
			fail("verdict = %q, want %q", r.Output.Verdict, e.Verdict)
		}
		if len(r.Output.Findings) < e.MinFindings {
			fail("findings = %d, want >= %d", len(r.Output.Findings), e.MinFindings)
		}
	default:
		fail("unsupported stage %q", f.Stage)
	}
	if calls != nil {
		res.Calls = calls()
		if e.Calls > 0 && res.Calls != e.Calls {
			fail("model calls = %d, want %d", res.Calls, e.Calls)
		}
	}
	res.Pass = len(res.Failures) == 0
	return res
}

// ReplayGateway builds a gateway over a replay provider with the given routing.
func ReplayGateway(f *Fixture, routing llm.Routing) (*llm.Gateway, *ReplayProvider, error) {
	p := NewReplay(f.Recorded)
	gw, err := llm.NewGateway(llm.Options{Provider: p, Routing: routing, Recorder: &llm.MemoryRecorder{}})
	return gw, p, err
}

// DefaultRouting mirrors config.DefaultModelChains without importing env config.
func DefaultRouting() llm.Routing {
	return llm.Routing{
		llm.StageTriage:     {Models: []string{"gpt-5-mini", "gpt-4.1-mini"}},
		llm.StageRefine:     {Models: []string{"gpt-5-mini", "gpt-4.1"}},
		llm.StagePrioritize: {Models: []string{"gpt-5-nano", "gpt-4.1-mini"}},
		llm.StageImplement:  {Models: []string{"gpt-5", "gpt-4.1"}},
		llm.StageReview:     {Models: []string{"o3", "gpt-5-mini"}},
	}
}
