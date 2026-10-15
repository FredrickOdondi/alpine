package ticketrepo

import "testing"

func TestOutcomeCategory(t *testing.T) {
	for in, want := range map[string]string{
		"ticket_created:abc": "ticket_created", "workflow_started:x": "ticket_updated", "duplicate:x": "ignored",
		"ignored:unmapped_sentry_project": "ignored", "close:x": "signalled", "mark_merged:x": "signalled",
		"rejected: bad json": "ignored", "ignored": "ignored", "failed": "failed",
	} {
		if got := OutcomeCategory(in); got != want {
			t.Errorf("%q -> %q want %q", in, got, want)
		}
	}
}
