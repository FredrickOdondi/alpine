package adapters

import (
	"context"

	"github.com/alpine-dev/alpine/services/orchestrator/internal/activities"
	"github.com/alpine-dev/alpine/services/orchestrator/internal/workspace"
)

// Workspaces adapts workspace.Manager to activities.Workspaces.
type Workspaces struct{ M *workspace.Manager }

var _ activities.Workspaces = Workspaces{}

// Open implements activities.Workspaces.
func (w Workspaces) Open(ctx context.Context, ticketULID, repo, branch, base string) (activities.Checkout, error) {
	return w.M.Open(ctx, ticketULID, repo, branch, base)
}

// Diff implements activities.Workspaces.
func (w Workspaces) Diff(ctx context.Context, dir, base, head string, maxBytes int) (string, error) {
	return workspace.Diff(ctx, w.M, dir, base, head, maxBytes)
}
