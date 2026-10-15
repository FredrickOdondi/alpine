package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// ErrInvalidOutput is returned when a structured call still fails schema validation after all
// repair attempts.
var ErrInvalidOutput = errors.New("llm: model output failed schema validation")

var (
	schemaCacheMu sync.Mutex
	schemaCache   = map[string]*jsonschema.Schema{}
)

// CompileSchema compiles (and caches) a JSON schema.
func CompileSchema(s *Schema) (*jsonschema.Schema, error) {
	schemaCacheMu.Lock()
	defer schemaCacheMu.Unlock()
	cacheKey := s.Name + "\x00" + string(s.Schema)
	if c, ok := schemaCache[cacheKey]; ok {
		return c, nil
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(s.Schema))
	if err != nil {
		return nil, fmt.Errorf("schema %s: %w", s.Name, err)
	}
	c := jsonschema.NewCompiler()
	url := "mem://" + s.Name + ".json"
	if err := c.AddResource(url, doc); err != nil {
		return nil, err
	}
	compiled, err := c.Compile(url)
	if err != nil {
		return nil, fmt.Errorf("compile schema %s: %w", s.Name, err)
	}
	schemaCache[cacheKey] = compiled
	return compiled, nil
}

// ValidateJSON validates content against s and returns the decoded value.
func ValidateJSON(s *Schema, content string) error {
	compiled, err := CompileSchema(s)
	if err != nil {
		return err
	}
	content = stripFences(content)
	inst, err := jsonschema.UnmarshalJSON(strings.NewReader(content))
	if err != nil {
		return fmt.Errorf("not valid JSON: %w", err)
	}
	return compiled.Validate(inst)
}

// CompleteJSON performs a structured call: the response must validate against req.Schema and is
// decoded into out. On invalid output the model is shown the validation error and asked to repair
// (up to MaxRepairs), each repair being its own idempotent, metered call.
func (g *Gateway) CompleteJSON(ctx context.Context, req Request, out any) (*Response, error) {
	if req.Schema == nil {
		return nil, fmt.Errorf("%w: CompleteJSON requires a schema", ErrInvalidRequest)
	}
	if _, err := CompileSchema(req.Schema); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	baseKey := req.IdempotencyKey
	msgs := append([]Message(nil), req.Messages...)
	total := Usage{}
	var cost float64
	var lastErr error
	for attempt := 0; attempt <= g.o.MaxRepairs; attempt++ {
		r := req
		r.Messages = msgs
		if attempt > 0 {
			r.IdempotencyKey = fmt.Sprintf("%s/repair-%d", baseKey, attempt)
		}
		resp, err := g.Complete(ctx, r)
		if err != nil {
			return nil, err
		}
		total.InputTokens += resp.Usage.InputTokens
		total.OutputTokens += resp.Usage.OutputTokens
		total.CachedTokens += resp.Usage.CachedTokens
		cost += resp.CostUSD
		verr := ValidateJSON(req.Schema, resp.Content)
		if verr == nil {
			if err := json.Unmarshal([]byte(stripFences(resp.Content)), out); err != nil {
				verr = err
			} else {
				resp.Usage, resp.CostUSD = total, cost
				return resp, nil
			}
		}
		lastErr = verr
		g.o.Logger.WarnContext(ctx, "llm structured output invalid; requesting repair",
			"stage", req.Stage, "attempt", attempt, "err", verr)
		msgs = append(msgs,
			Message{Role: RoleAssistant, Content: resp.Content},
			Message{Role: RoleUser, Content: "Your previous reply did not match the required JSON schema `" + req.Schema.Name +
				"`. Validation error:\n" + verr.Error() + "\nReply again with ONLY a JSON object that satisfies the schema."},
		)
	}
	return nil, fmt.Errorf("%w: %w", ErrInvalidOutput, lastErr)
}

func stripFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	}
	return strings.TrimSpace(s)
}
