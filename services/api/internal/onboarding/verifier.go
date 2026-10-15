package onboarding

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/alpine-dev/alpine/packages/shared/githubapp"
	"github.com/alpine-dev/alpine/packages/shared/intake"
	ghwebhook "github.com/alpine-dev/alpine/services/integrations/github/webhook"
)

// WebhookVerifier checks GitHub signatures against the registered App's webhook secret (read
// through the githubapp cache, so a newly created App is honoured without a restart) and the
// static GITHUB_WEBHOOK_SECRET, which keeps scripts/send-issue-webhook.sh and `make webhook` working.
type WebhookVerifier struct {
	Apps   *githubapp.Cached
	Static []byte
}

// Verify implements intake.Verifier.
func (v WebhookVerifier) Verify(h http.Header, body []byte) error {
	var errs []error
	if v.Apps != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		c, err := v.Apps.Load(ctx)
		cancel()
		if err == nil && c.WebhookSecret != "" {
			if err := (ghwebhook.Verifier{Secret: []byte(c.WebhookSecret)}).Verify(h, body); err == nil {
				return nil
			} else {
				errs = append(errs, err)
			}
		}
	}
	if len(v.Static) > 0 {
		if err := (ghwebhook.Verifier{Secret: v.Static}).Verify(h, body); err == nil {
			return nil
		} else {
			errs = append(errs, err)
		}
	}
	if len(errs) == 0 {
		return errors.New("webhook: no GitHub webhook secret configured")
	}
	return errors.Join(errs...)
}

// Source implements intake.Verifier.
func (v WebhookVerifier) Source() intake.Source { return intake.SourceGitHub }
