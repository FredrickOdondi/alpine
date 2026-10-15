package adapters

import (
	"context"

	"github.com/alpine-dev/alpine/packages/gates"
	"github.com/alpine-dev/alpine/packages/shared/gate"
)

// RepoGates implements gate.Runner with lena/gates' SharedRunner, using the checkout directory
// from each request (gate.RunRequest.RepoDir, the ticket's sandbox checkout) and loading the gate
// config from req.BaseSHA. Runner carries the shared run counter and baseline cache.
type RepoGates struct {
	Runner       *gates.Runner
	VisualRoutes []string
}

var _ gate.Runner = RepoGates{}

// Run implements gate.Runner.
func (g RepoGates) Run(ctx context.Context, req gate.RunRequest) (gate.Report, error) {
	// No struct RepoDir fallback: each ticket has its own checkout, so an empty req.RepoDir
	// fails with gates.ErrRepoDirRequired instead of running in some shared directory.
	return gates.SharedRunner{Runner: g.Runner, VisualRoutes: g.VisualRoutes}.Run(ctx, req)
}
