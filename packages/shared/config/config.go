// Package config loads Alpine configuration from environment variables (12-factor). Every value
// has a local-dev default matching docker-compose.yml and .env.example; nothing secret has a
// default.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/alpine-dev/alpine/packages/shared/budget"
	"github.com/alpine-dev/alpine/packages/shared/llm"
	"github.com/alpine-dev/alpine/packages/shared/ratelimit"
)

// String returns env key or def.
func String(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

// Int returns env key parsed as int or def.
func Int(key string, def int) int {
	if v, err := strconv.Atoi(String(key, "")); err == nil {
		return v
	}
	return def
}

// Float returns env key parsed as float64 or def.
func Float(key string, def float64) float64 {
	if v, err := strconv.ParseFloat(String(key, ""), 64); err == nil {
		return v
	}
	return def
}

// Bool returns env key parsed as bool or def.
func Bool(key string, def bool) bool {
	if v, err := strconv.ParseBool(String(key, "")); err == nil {
		return v
	}
	return def
}

// Duration returns env key parsed as time.Duration ("30s") or def.
func Duration(key string, def time.Duration) time.Duration {
	if v, err := time.ParseDuration(String(key, "")); err == nil {
		return v
	}
	return def
}

// Common holds settings shared by all services.
type Common struct {
	Env               string // dev | staging | prod
	LogLevel          string
	DatabaseURL       string // alpine_app role (RLS enforced)
	RedisURL          string
	TemporalHostPort  string
	TemporalNamespace string
	S3Endpoint        string
	S3Bucket          string
	S3AccessKey       string
	S3SecretKey       string
	S3UseSSL          bool
}

// LoadCommon reads Common.
func LoadCommon() Common {
	return Common{
		Env:               String("ALPINE_ENV", "dev"),
		LogLevel:          String("LOG_LEVEL", "info"),
		DatabaseURL:       String("DATABASE_URL", "postgres://alpine_app:alpine_app@localhost:5432/alpine?sslmode=disable"),
		RedisURL:          String("REDIS_URL", "redis://localhost:6379/0"),
		TemporalHostPort:  String("TEMPORAL_HOST_PORT", "localhost:7233"),
		TemporalNamespace: String("TEMPORAL_NAMESPACE", "default"),
		S3Endpoint:        String("S3_ENDPOINT", "localhost:9000"),
		S3Bucket:          String("S3_BUCKET", "alpine-artifacts"),
		S3AccessKey:       String("S3_ACCESS_KEY", ""),
		S3SecretKey:       String("S3_SECRET_KEY", ""),
		S3UseSSL:          Bool("S3_USE_SSL", false),
	}
}

// Default model chains (OpenAI is the only provider for now). Overridable per stage via
// LLM_MODELS_<STAGE>="primary,fallback,...". The reviewer intentionally uses a different model
// than the implementer.
var DefaultModelChains = map[llm.Stage]string{
	llm.StageTriage:     "gpt-5-mini,gpt-4.1-mini",
	llm.StageRefine:     "gpt-5-mini,gpt-4.1",
	llm.StagePrioritize: "gpt-5-nano,gpt-4.1-mini",
	llm.StageImplement:  "gpt-5,gpt-4.1",
	llm.StageReview:     "o3,gpt-5-mini",
}

// LLM holds model gateway settings.
type LLM struct {
	BaseURL                string
	APIKey                 llm.Secret // LiteLLM master/virtual key (never an end-customer key)
	BYOKInBody             bool
	Routing                llm.Routing
	Retry                  llm.RetryPolicy
	Breaker                llm.BreakerConfig
	MaxRepairs             int
	PricingFile            string
	LogPrompts             bool
	RedactOutbound         bool
	AllowPlatformKey       bool
	LocalKEK               llm.Secret // base64 32 bytes, dev only (prod: AWS KMS)
	Limits                 ratelimit.Limits
	OrgCaps                budget.OrgCaps
	MaxParallelTicketsOrg  int
	MaxParallelTicketsRepo int
}

// LoadLLM reads LLM settings and validates routing.
func LoadLLM() (LLM, error) {
	routing := llm.Routing{}
	for stage, def := range DefaultModelChains {
		key := "LLM_MODELS_" + strings.ToUpper(string(stage))
		routing[stage] = llm.Route{Models: llm.ParseChain(String(key, def)), MaxOutputTokens: Int("LLM_MAX_OUTPUT_TOKENS_"+strings.ToUpper(string(stage)), 0)}
	}
	if err := routing.Validate(); err != nil {
		return LLM{}, err
	}
	c := LLM{
		BaseURL:    String("LLM_BASE_URL", "http://localhost:4000/v1"),
		APIKey:     llm.Secret(String("LLM_API_KEY", "")),
		BYOKInBody: Bool("LLM_BYOK_IN_BODY", true),
		Routing:    routing,
		Retry: llm.RetryPolicy{
			MaxAttempts:       Int("LLM_MAX_ATTEMPTS", 4),
			BaseDelay:         Duration("LLM_RETRY_BASE_DELAY", 500*time.Millisecond),
			MaxDelay:          Duration("LLM_RETRY_MAX_DELAY", 20*time.Second),
			Multiplier:        2,
			AttemptTimeout:    Duration("LLM_ATTEMPT_TIMEOUT", 120*time.Second),
			MaxRetryAfterWait: Duration("LLM_MAX_RETRY_AFTER_WAIT", 60*time.Second),
		},
		Breaker: llm.BreakerConfig{
			FailureThreshold: Int("LLM_BREAKER_FAILURES", 5),
			OpenFor:          Duration("LLM_BREAKER_OPEN_FOR", 30*time.Second),
		},
		MaxRepairs:       Int("LLM_MAX_REPAIRS", 2),
		PricingFile:      String("LLM_PRICING_FILE", ""),
		LogPrompts:       Bool("LLM_LOG_PROMPTS", false),
		RedactOutbound:   Bool("LLM_REDACT_OUTBOUND", true),
		AllowPlatformKey: Bool("LLM_ALLOW_PLATFORM_KEY", true),
		LocalKEK:         llm.Secret(String("ALPINE_LOCAL_KEK", "")),
		Limits: ratelimit.Limits{
			GlobalConcurrency:    Int("LIMIT_GLOBAL_LLM_CONCURRENCY", 64),
			OrgConcurrency:       Int("LIMIT_ORG_LLM_CONCURRENCY", 8),
			OrgRequestsPerMinute: Int("LIMIT_ORG_LLM_RPM", 120),
			OrgTokensPerMinute:   Int("LIMIT_ORG_LLM_TPM", 400000),
			RepoRequestsPerMin:   Int("LIMIT_REPO_LLM_RPM", 60),
		},
		OrgCaps: budget.OrgCaps{
			DailyCostUSD:   Float("BUDGET_ORG_DAILY_USD", 100),
			MonthlyCostUSD: Float("BUDGET_ORG_MONTHLY_USD", 1000),
			DailyTokens:    int64(Int("BUDGET_ORG_DAILY_TOKENS", 0)),
			MonthlyTokens:  int64(Int("BUDGET_ORG_MONTHLY_TOKENS", 0)),
		},
		MaxParallelTicketsOrg:  Int("LIMIT_ORG_MAX_PARALLEL_TICKETS", 5),
		MaxParallelTicketsRepo: Int("LIMIT_REPO_MAX_PARALLEL_TICKETS", 3),
	}
	if c.MaxRepairs < 0 {
		return LLM{}, fmt.Errorf("LLM_MAX_REPAIRS must be >= 0")
	}
	return c, nil
}
