package llm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// Provider is the transport to a model API. Implementations must not retry on their own; the
// Gateway owns retries, fallbacks and breakers.
type Provider interface {
	Name() string
	Complete(ctx context.Context, req ProviderRequest) (ProviderResponse, error)
}

// ProviderRequest is one attempt against one model.
type ProviderRequest struct {
	Model           string
	Messages        []Message
	Tools           []Tool
	Schema          *Schema
	MaxOutputTokens int
	Temperature     *float64
	// APIKey is the tenant's BYO key; empty means "use the proxy's platform key".
	APIKey Secret
	// CacheScope isolates provider-side prompt caching per tenant (sent as prompt_cache_key) and
	// identifies the end customer for abuse monitoring (safety_identifier). It is a hash, never a
	// raw org id.
	CacheScope string
}

// ProviderResponse is a successful attempt.
type ProviderResponse struct {
	Content      string
	ToolCalls    []ToolCall
	FinishReason string
	Model        string
	Usage        Usage
	// CostUSD as reported by the proxy (LiteLLM x-litellm-response-cost); 0 if unknown.
	CostUSD float64
}

// APIError is a non-2xx answer from the provider.
type APIError struct {
	StatusCode int
	Type       string
	Code       string
	Message    string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf("llm provider: HTTP %d %s: %s", e.StatusCode, e.Code, e.Message)
}

// Retryable classifies errors: 408/409/429/5xx, timeouts and connection failures are retryable;
// other 4xx (bad request, auth, content policy) and caller cancellation are not.
func Retryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.StatusCode == http.StatusRequestTimeout,
			apiErr.StatusCode == http.StatusConflict,
			apiErr.StatusCode == http.StatusTooManyRequests,
			apiErr.StatusCode >= 500:
			return apiErr.Code != "insufficient_quota" // quota exhaustion won't fix itself
		default:
			return false
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true // per-attempt timeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var opErr *net.OpError
	return errors.As(err, &opErr)
}

// fallbackWorthy reports whether a non-retryable error should still move on to the next model in
// the chain (model missing / model-specific rejection) rather than failing the call.
func fallbackWorthy(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusNotFound || apiErr.Code == "model_not_found" ||
			apiErr.Code == "context_length_exceeded" || apiErr.Code == "insufficient_quota"
	}
	return false
}

// retryAfter extracts a provider-requested delay.
func retryAfter(err error) time.Duration {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.RetryAfter
	}
	return 0
}

// Secret is a string that never prints (fmt, slog, JSON) its value.
type Secret string

// String implements fmt.Stringer.
func (s Secret) String() string {
	if s == "" {
		return ""
	}
	return "[REDACTED]"
}

// GoString implements fmt.GoStringer.
func (s Secret) GoString() string { return s.String() }

// MarshalJSON never serializes the value.
func (s Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + s.String() + `"`), nil }

// Reveal returns the raw value; only transports call it.
func (s Secret) Reveal() string { return string(s) }
