package gates

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/alpine-dev/alpine/packages/shared/gate"
)

// ErrRepoDirRequired is returned when neither RunRequest.RepoDir nor the
// SharedRunner.RepoDir fallback names a checkout to run gates in.
var ErrRepoDirRequired = errors.New("gates: RunRequest.RepoDir is required (SharedRunner.RepoDir fallback is also empty)")

// SharedRunner adapts Runner to the shared gate.Runner interface the
// orchestrator depends on (packages/shared/gate). It runs inside the gate
// sandbox, where the checkout of req.HeadSHA is req.RepoDir. When req.RepoDir
// is empty the struct RepoDir is used as a fallback (for callers that build
// one SharedRunner per checkout); when both are empty Run returns
// ErrRepoDirRequired.
//
// The gate config is always loaded from req.BaseSHA via LoadConfigFromBase.
// req.Gates is only a subset filter: when non-empty, just the gates with those
// ids run (e.g. rerun the ones that failed). Every other Spec field in the
// request is ignored, an id missing from the base config is an error
// (ErrUnknownGate), so the request can never add or loosen a gate. The job ID for the gate-run cap is
// req.RunID (falling back to req.TicketID). The returned Report.Iteration is
// the counter's gate-run number.
type SharedRunner struct {
	Runner *Runner
	// RepoDir is the fallback checkout path, used only when the request's
	// RepoDir is empty.
	RepoDir string
	// VisualRoutes are passed to visual gates configured with routes: auto.
	VisualRoutes []string
}

var _ gate.Runner = SharedRunner{}

// Run implements gate.Runner. When the cap is reached it returns an empty
// report and an error wrapping ErrGateRunCapReached.
func (s SharedRunner) Run(ctx context.Context, req gate.RunRequest) (gate.Report, error) {
	if s.Runner == nil {
		return gate.Report{}, errors.New("gates: SharedRunner.Runner is nil")
	}
	if req.BaseSHA == "" {
		return gate.Report{}, fmt.Errorf("%w: RunRequest.BaseSHA is required", ErrBaseRefNotFound)
	}
	repoDir, err := s.repoDir(req)
	if err != nil {
		return gate.Report{}, err
	}
	if req.HeadSHA != "" {
		head, gitErr := runGit(ctx, repoDir, "rev-parse", "HEAD")
		if gitErr != nil {
			return gate.Report{}, fmt.Errorf("gates: resolve HEAD in %s: %w", repoDir, gitErr)
		}
		if got := strings.TrimSpace(string(head)); !strings.HasPrefix(got, req.HeadSHA) {
			return gate.Report{}, fmt.Errorf("gates: checkout is at %s, want head %s", got, req.HeadSHA)
		}
	}
	cfg, err := LoadConfigFromBase(ctx, repoDir, req.BaseSHA)
	if err != nil {
		return gate.Report{}, err
	}
	jobID := req.RunID
	if jobID == "" {
		jobID = req.TicketID
	}
	var only []string
	for _, sp := range req.Gates {
		only = append(only, sp.ID)
	}
	res, err := s.Runner.Run(ctx, RunRequest{JobID: jobID, RepoDir: repoDir, BaseRef: req.BaseSHA, Config: cfg, Only: only, VisualRoutes: s.VisualRoutes})
	return res.Report(), err
}

// repoDir resolves the checkout path: req.RepoDir, else the struct fallback.
func (s SharedRunner) repoDir(req gate.RunRequest) (string, error) {
	if req.RepoDir != "" {
		return req.RepoDir, nil
	}
	if s.RepoDir != "" {
		return s.RepoDir, nil
	}
	return "", ErrRepoDirRequired
}
