package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// OpenAICompatible talks the OpenAI Chat Completions wire format. Point BaseURL at the LiteLLM
// proxy (default, e.g. http://localhost:4000/v1) or directly at https://api.openai.com/v1.
type OpenAICompatible struct {
	BaseURL string
	// APIKey authenticates to BaseURL: the LiteLLM master/virtual key, or a platform OpenAI key
	// when talking to OpenAI directly.
	APIKey Secret
	// BYOKInBody sends a tenant BYO key in the request body as `api_key` (LiteLLM client-side
	// credentials) instead of replacing the Authorization header (direct OpenAI).
	BYOKInBody bool
	HTTP       *http.Client
}

// Name implements Provider.
func (p *OpenAICompatible) Name() string { return "openai" }

type oaMessage struct {
	Role       string       `json:"role"`
	Content    string       `json:"content"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
	ToolCalls  []oaToolCall `json:"tool_calls,omitempty"`
}

type oaToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oaTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type oaRequest struct {
	Model            string      `json:"model"`
	Messages         []oaMessage `json:"messages"`
	Tools            []oaTool    `json:"tools,omitempty"`
	ResponseFormat   any         `json:"response_format,omitempty"`
	MaxTokens        int         `json:"max_completion_tokens,omitempty"`
	Temperature      *float64    `json:"temperature,omitempty"`
	PromptCacheKey   string      `json:"prompt_cache_key,omitempty"`
	SafetyIdentifier string      `json:"safety_identifier,omitempty"`
	APIKey           string      `json:"api_key,omitempty"`
}

type oaResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		FinishReason string    `json:"finish_reason"`
		Message      oaMessage `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens        int64 `json:"prompt_tokens"`
		CompletionTokens    int64 `json:"completion_tokens"`
		PromptTokensDetails struct {
			CachedTokens int64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
}

type oaError struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	} `json:"error"`
}

// Complete implements Provider.
func (p *OpenAICompatible) Complete(ctx context.Context, req ProviderRequest) (ProviderResponse, error) {
	body := oaRequest{
		Model:            req.Model,
		MaxTokens:        req.MaxOutputTokens,
		Temperature:      req.Temperature,
		PromptCacheKey:   req.CacheScope,
		SafetyIdentifier: req.CacheScope,
	}
	for _, m := range req.Messages {
		om := oaMessage{Role: string(m.Role), Content: m.Content, ToolCallID: m.ToolCallID}
		for _, tc := range m.ToolCalls {
			otc := oaToolCall{ID: tc.ID, Type: "function"}
			otc.Function.Name, otc.Function.Arguments = tc.Name, string(tc.Arguments)
			om.ToolCalls = append(om.ToolCalls, otc)
		}
		body.Messages = append(body.Messages, om)
	}
	for _, t := range req.Tools {
		ot := oaTool{Type: "function"}
		ot.Function.Name, ot.Function.Description, ot.Function.Parameters = t.Name, t.Description, t.Parameters
		body.Tools = append(body.Tools, ot)
	}
	if req.Schema != nil {
		body.ResponseFormat = map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name": req.Schema.Name, "schema": req.Schema.Schema, "strict": true,
			},
		}
	}
	auth := p.APIKey
	if req.APIKey != "" {
		if p.BYOKInBody {
			body.APIKey = req.APIKey.Reveal()
		} else {
			auth = req.APIKey
		}
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return ProviderResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.BaseURL, "/")+"/chat/completions", bytes.NewReader(buf))
	if err != nil {
		return ProviderResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if auth != "" {
		httpReq.Header.Set("Authorization", "Bearer "+auth.Reveal())
	}
	client := p.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return ProviderResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return ProviderResponse{}, err
	}
	if resp.StatusCode/100 != 2 {
		apiErr := &APIError{StatusCode: resp.StatusCode, RetryAfter: parseRetryAfter(resp.Header, time.Now())}
		var e oaError
		if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
			apiErr.Message, apiErr.Type = e.Error.Message, e.Error.Type
			if e.Error.Code != nil {
				apiErr.Code = fmt.Sprint(e.Error.Code)
			}
		} else {
			apiErr.Message = http.StatusText(resp.StatusCode)
		}
		return ProviderResponse{}, apiErr
	}
	var out oaResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return ProviderResponse{}, fmt.Errorf("llm provider: decode response: %w", err)
	}
	if len(out.Choices) == 0 {
		return ProviderResponse{}, &APIError{StatusCode: http.StatusBadGateway, Message: "no choices in response"}
	}
	ch := out.Choices[0]
	pr := ProviderResponse{
		Content:      ch.Message.Content,
		FinishReason: ch.FinishReason,
		Model:        out.Model,
		Usage: Usage{
			InputTokens:  out.Usage.PromptTokens,
			OutputTokens: out.Usage.CompletionTokens,
			CachedTokens: out.Usage.PromptTokensDetails.CachedTokens,
		},
	}
	if pr.Model == "" {
		pr.Model = req.Model
	}
	for _, tc := range ch.Message.ToolCalls {
		pr.ToolCalls = append(pr.ToolCalls, ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: json.RawMessage(tc.Function.Arguments)})
	}
	if c := resp.Header.Get("x-litellm-response-cost"); c != "" {
		if v, err := strconv.ParseFloat(c, 64); err == nil {
			pr.CostUSD = v
		}
	}
	return pr, nil
}

// parseRetryAfter reads retry-after-ms, then Retry-After (seconds or HTTP date).
func parseRetryAfter(h http.Header, now time.Time) time.Duration {
	if ms := h.Get("retry-after-ms"); ms != "" {
		if v, err := strconv.ParseFloat(ms, 64); err == nil && v >= 0 {
			return time.Duration(v * float64(time.Millisecond))
		}
	}
	ra := h.Get("Retry-After")
	if ra == "" {
		return 0
	}
	if secs, err := strconv.ParseFloat(ra, 64); err == nil && secs >= 0 {
		return time.Duration(secs * float64(time.Second))
	}
	if t, err := http.ParseTime(ra); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}
