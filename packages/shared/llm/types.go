// Package llm is Alpine's model gateway client. It is provider-agnostic: a Provider is the
// transport (today an OpenAI-compatible HTTP client that talks to the LiteLLM proxy, with OpenAI as
// the only configured upstream), and the Gateway wraps it with everything that makes the AI layer
// safe for many concurrent orgs:
//
//   - per-stage model routing with a fallback chain (e.g. primary OpenAI model -> secondary)
//   - retries with exponential backoff + full jitter on 429/5xx/timeouts, honouring Retry-After
//   - a circuit breaker per model
//   - per-org / per-repo concurrency limits and token-bucket rate limits (Limiter)
//   - pre-flight budget checks (ticket, org daily/monthly) and per-call usage metering
//   - idempotency: each call is keyed by <workflow id>/<activity id>/<seq>; a retried activity
//     replays the stored response instead of paying for (and possibly diverging on) a new one
//   - BYO keys resolved per org (envelope-encrypted at rest, never logged)
//   - JSON-schema structured outputs with repair-retry
//   - OpenTelemetry spans/metrics and redacted prompt logging
package llm

import (
	"encoding/json"
	"time"
)

// Stage is the agent role a call belongs to. Model routing, budgets and metrics are per stage.
type Stage string

const (
	StageTriage     Stage = "triage"
	StageRefine     Stage = "refine"
	StagePrioritize Stage = "prioritize"
	StageImplement  Stage = "implement"
	StageReview     Stage = "review"
)

// Role of a chat message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is one chat message.
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content"`
	ToolCallID string     `json:"toolCallId,omitempty"`
	ToolCalls  []ToolCall `json:"toolCalls,omitempty"`
}

// Tool is a function the model may call.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"` // JSON Schema
}

// ToolCall is a model-requested tool invocation.
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// Schema requests a structured (JSON-schema constrained) response.
type Schema struct {
	Name   string          `json:"name"`
	Schema json.RawMessage `json:"schema"`
}

// Tenant scopes a call for keys, limits, budgets, metering and caching.
type Tenant struct {
	OrgID    string `json:"orgId"`
	RepoID   string `json:"repoId,omitempty"`
	TicketID string `json:"ticketId,omitempty"`
	RunID    string `json:"runId,omitempty"`
}

// Request is a gateway call.
type Request struct {
	Tenant Tenant
	Stage  Stage
	// IdempotencyKey is required: "<workflow id>/<activity id>/<seq>". Same key => same result.
	IdempotencyKey string
	Messages       []Message
	Tools          []Tool
	Schema         *Schema
	// Optional overrides of the stage route.
	MaxOutputTokens int
	Temperature     *float64
	// EstimatedInputTokens feeds the tokens-per-minute limiter; 0 = estimate from message length.
	EstimatedInputTokens int
}

// Usage is token usage of one call.
type Usage struct {
	InputTokens  int64 `json:"inputTokens"`
	OutputTokens int64 `json:"outputTokens"`
	CachedTokens int64 `json:"cachedTokens"`
}

// Total tokens.
func (u Usage) Total() int64 { return u.InputTokens + u.OutputTokens }

// Response is the gateway result.
type Response struct {
	Content        string        `json:"content"`
	ToolCalls      []ToolCall    `json:"toolCalls,omitempty"`
	FinishReason   string        `json:"finishReason,omitempty"`
	Model          string        `json:"model"`          // model that answered
	RequestedModel string        `json:"requestedModel"` // primary model of the chain
	Usage          Usage         `json:"usage"`
	CostUSD        float64       `json:"costUsd"`
	Latency        time.Duration `json:"latencyNs"`
	Attempts       int           `json:"attempts"`
	BYOK           bool          `json:"byok"`
	// Replayed is true when the response came from the idempotency store (no new spend).
	Replayed bool `json:"replayed"`
}
