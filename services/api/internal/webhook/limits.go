// Package webhook implements the GitHub and Sentry webhook receivers: verify (with the
// services/integrations verifiers), size-limit, resolve the tenant, and write an idempotent inbox
// row. Everything else happens in the inbox processor.
package webhook

import (
	ghwebhook "github.com/alpine-dev/alpine/services/integrations/github/webhook"
	"github.com/alpine-dev/alpine/services/integrations/sentry"
)

// Body limits, from the integrations packages (GitHub 25 MB, Sentry 5 MB).
const (
	MaxGitHubBody = ghwebhook.MaxPayloadBytes
	MaxSentryBody = sentry.MaxPayloadBytes
)
