package inbox

import (
	"context"

	"github.com/alpine-dev/alpine/packages/shared/intake"
	ghwebhook "github.com/alpine-dev/alpine/services/integrations/github/webhook"
	"github.com/alpine-dev/alpine/services/integrations/sentry"
)

// Normalizers is the production intake registry: omar/integrations' GitHub (issues,
// issue_comment) and Sentry (issue, error, event_alert) normalizers. GitHub `pull_request` events
// are not covered by the integrations normalizer yet, so GitHub wraps it and routes those to
// GitHubMinimal, which only turns "Alpine PR merged" into intake.ActionPRMerged.
func Normalizers(botLogin string) intake.Registry {
	return intake.Registry{
		intake.SourceGitHub: GitHub{PullRequests: GitHubMinimal{BotLogin: botLogin}},
		intake.SourceSentry: sentry.Normalizer{},
	}
}

// GitHub routes GitHub events: pull_request -> PullRequests, everything else -> integrations.
type GitHub struct {
	PullRequests GitHubMinimal
}

// Source implements intake.Normalizer.
func (GitHub) Source() intake.Source { return intake.SourceGitHub }

// Normalize implements intake.Normalizer.
func (g GitHub) Normalize(ctx context.Context, eventType string, payload []byte) ([]intake.TicketIntake, error) {
	if eventType == "pull_request" {
		return g.PullRequests.Normalize(ctx, eventType, payload)
	}
	return ghwebhook.Normalizer{}.Normalize(ctx, eventType, payload)
}
