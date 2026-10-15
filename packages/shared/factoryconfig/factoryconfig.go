// Package factoryconfig parses and validates a repo's `.alpine/factory.yaml` (architecture §6).
// Gate config and limits are always read from the BASE branch.
package factoryconfig

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/alpine-dev/alpine/packages/shared/budget"
	"github.com/alpine-dev/alpine/packages/shared/gate"
)

// Config is the parsed factory.yaml.
type Config struct {
	Version        int                 `yaml:"version" json:"version"`
	Rules          Rules               `yaml:"rules" json:"rules"`
	Environment    Environment         `yaml:"environment" json:"environment"`
	Layers         map[string][]string `yaml:"layers" json:"layers"`
	ProtectedPaths []string            `yaml:"protected_paths" json:"protectedPaths"`
	// Gates and GateRunner are decoded loosely (raw YAML): their schema (parser, report_file,
	// warn_only, thresholds, …) belongs to packages/gates, which validates them.
	Gates       yaml.Node             `yaml:"gates" json:"-"`
	GateRunner  yaml.Node             `yaml:"gate_runner" json:"-"`
	Limits      Limits                `yaml:"limits" json:"limits"`
	Models      map[string]ModelChain `yaml:"models" json:"models"`
	MergePolicy map[string]string     `yaml:"merge_policy" json:"mergePolicy"`
}

// Rules configures agent context.
type Rules struct {
	Files  []string    `yaml:"files" json:"files"`
	Skills []string    `yaml:"skills" json:"skills"`
	MCP    []MCPServer `yaml:"mcp" json:"mcp"`
}

// MCPServer is a configured MCP endpoint.
type MCPServer struct {
	Name string `yaml:"name" json:"name"`
	URL  string `yaml:"url" json:"url"`
	Mode string `yaml:"mode" json:"mode"` // read-only (default) | read-write
}

// Environment describes the sandbox environment recipe.
type Environment struct {
	Build        string   `yaml:"build" json:"build,omitempty"`
	Devcontainer string   `yaml:"devcontainer" json:"devcontainer,omitempty"`
	Install      string   `yaml:"install" json:"install,omitempty"`
	Services     []string `yaml:"services" json:"services,omitempty"`
	Seed         string   `yaml:"seed" json:"seed,omitempty"`
}

// Limits are the per-ticket hard caps.
type Limits struct {
	MaxTurns           int     `yaml:"max_turns" json:"maxTurns"`
	MaxGateIterations  int     `yaml:"max_gate_iterations" json:"maxGateIterations"`
	MaxCostUSD         float64 `yaml:"max_cost_usd" json:"maxCostUsd"`
	MaxTokens          int64   `yaml:"max_tokens" json:"maxTokens"`
	MaxParallelTickets int     `yaml:"max_parallel_tickets" json:"maxParallelTickets"`
}

// TicketCaps converts limits to budget caps (defaults applied).
func (l Limits) TicketCaps() budget.TicketCaps {
	return budget.TicketCaps{MaxTurns: l.MaxTurns, MaxGateIterations: l.MaxGateIterations, MaxCostUSD: l.MaxCostUSD, MaxTokens: l.MaxTokens}.WithDefaults()
}

// ModelChain accepts either a single model name or a list (primary first).
type ModelChain []string

// UnmarshalYAML implements yaml.Unmarshaler.
func (m *ModelChain) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		*m = ModelChain{n.Value}
		return nil
	case yaml.SequenceNode:
		var s []string
		if err := n.Decode(&s); err != nil {
			return err
		}
		*m = s
		return nil
	}
	return errors.New("models: expected a model name or a list of model names")
}

// GateSpecs decodes the gates list loosely into the shared gate.Spec (unknown gate keys are
// ignored here; packages/gates owns full gate validation).
func (c *Config) GateSpecs() ([]gate.Spec, error) {
	if c.Gates.Kind == 0 {
		return nil, nil
	}
	var specs []gate.Spec
	if err := c.Gates.Decode(&specs); err != nil {
		return nil, fmt.Errorf("factory.yaml gates: %w", err)
	}
	return specs, nil
}

// Parse decodes and validates factory.yaml bytes. Unknown keys are rejected so typos surface.
func Parse(data []byte) (*Config, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("factory.yaml: %w", err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

var layerRe = regexp.MustCompile(`^BR[1-4]$`)

// Validate checks semantic constraints.
func (c *Config) Validate() error {
	var errs []error
	if c.Version != 1 {
		errs = append(errs, fmt.Errorf("version must be 1, got %d", c.Version))
	}
	for k := range c.Layers {
		if !layerRe.MatchString(k) {
			errs = append(errs, fmt.Errorf("layers: unknown level %q (want BR1..BR4)", k))
		}
	}
	for k, v := range c.MergePolicy {
		if !layerRe.MatchString(k) {
			errs = append(errs, fmt.Errorf("merge_policy: unknown level %q", k))
		}
		if v != "human" && v != "auto" {
			errs = append(errs, fmt.Errorf("merge_policy.%s: must be human or auto", k))
		}
	}
	if c.Gates.Kind != 0 && c.Gates.Kind != yaml.SequenceNode {
		errs = append(errs, errors.New("gates: must be a list"))
	}
	if l := c.Limits.MaxTurns; l != 0 && (l < 10 || l > 300) {
		errs = append(errs, fmt.Errorf("limits.max_turns must be within 10..300, got %d", l))
	}
	if l := c.Limits.MaxGateIterations; l < 0 || l > 50 {
		errs = append(errs, fmt.Errorf("limits.max_gate_iterations must be within 1..50"))
	}
	if c.Limits.MaxCostUSD < 0 {
		errs = append(errs, errors.New("limits.max_cost_usd must be >= 0"))
	}
	if impl, ok := c.Models["implementer"]; ok {
		for _, r := range c.Models["reviewer"] {
			for _, m := range impl {
				if r == m {
					errs = append(errs, fmt.Errorf("models: reviewer %q must differ from the implementer models", r))
				}
			}
		}
	}
	for _, m := range c.Rules.MCP {
		if m.Mode != "" && m.Mode != "read-only" && m.Mode != "read-write" {
			errs = append(errs, fmt.Errorf("rules.mcp.%s: mode must be read-only or read-write", m.Name))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("factory.yaml invalid: %w", errors.Join(errs...))
	}
	return nil
}

// BlastRadius computes the blast radius of a diff from `layers:` globs (highest match wins;
// unmatched files count as BR1). Detectors beyond globs (static-analysis fan-in, lockfile major
// bumps) are added in Phase 1.
func (c *Config) BlastRadius(paths []string) int {
	br := 0
	levels := make([]string, 0, len(c.Layers))
	for k := range c.Layers {
		levels = append(levels, k)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(levels)))
	for _, p := range paths {
		pbr := 1
		for _, lvl := range levels {
			if MatchAny(c.Layers[lvl], p) {
				pbr = int(lvl[2] - '0')
				break
			}
		}
		if pbr > br {
			br = pbr
		}
	}
	return br
}

// TouchesProtected returns the protected paths touched by a diff.
func (c *Config) TouchesProtected(paths []string) []string {
	var out []string
	for _, p := range paths {
		if MatchAny(c.ProtectedPaths, p) {
			out = append(out, p)
		}
	}
	return out
}

// MatchAny reports whether path matches any glob ("**" spans directories, "*" does not).
func MatchAny(globs []string, path string) bool {
	for _, g := range globs {
		if globRegexp(g).MatchString(path) {
			return true
		}
	}
	return false
}

func globRegexp(g string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(g); i++ {
		switch c := g[i]; c {
		case '*':
			if i+1 < len(g) && g[i+1] == '*' {
				i++
				if i+1 < len(g) && g[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}
