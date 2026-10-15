package activities

import (
	"context"
	"fmt"
	"hash/fnv"
	"regexp"
	"strings"
	"time"

	"github.com/alpine-dev/alpine/packages/shared/gate"
	"github.com/alpine-dev/alpine/packages/shared/ticketflow"
)

// StubGates is a deterministic gate.Runner used until packages/gates (lena/gates) is wired in:
// the first FailIterations iterations fail typecheck with one finding, later ones are green.
type StubGates struct {
	FailIterations int
}

// Run implements gate.Runner.
func (s StubGates) Run(_ context.Context, req gate.RunRequest) (gate.Report, error) {
	rep := gate.Report{Iteration: req.Iteration}
	for _, id := range []string{"lint", "typecheck", "dupes", "unit"} {
		res := gate.Result{GateID: id, Status: gate.StatusPass, Duration: 1200 * time.Millisecond, Findings: []gate.Finding{}, Summary: "ok"}
		if id == "typecheck" && req.Iteration <= s.FailIterations {
			res.Status = gate.StatusFail
			res.Summary = "1 type error"
			res.Findings = []gate.Finding{{File: "src/pages/login.tsx", Line: 42, Rule: "TS2532", Severity: gate.SeverityError,
				Message: "Object is possibly 'undefined'.", Suggestion: "guard provider before reading .id"}}
		}
		rep.Results = append(rep.Results, res)
	}
	return rep, nil
}

// StubSCM fabricates branches/PRs/comments (no GitHub calls) until the GitHub App client from
// services/integrations is wired in. Results are deterministic per branch, which also makes
// repeated calls idempotent, and it enforces the same branch rules as the real client.
type StubSCM struct{}

// BranchName implements SCM (same format as github.NewBranchName).
func (StubSCM) BranchName(ticketULID, title string) (string, error) {
	id, err := ticketflow.ParseTicketULID(strings.ToUpper(ticketULID))
	if err != nil {
		return "", err
	}
	return ticketflow.BranchName(id, title), nil
}

// CreateBranchFrom implements SCM.
func (StubSCM) CreateBranchFrom(_ context.Context, repo, branch, _ string) (*SCMBranch, error) {
	if err := CheckBranch(branch); err != nil {
		return nil, err
	}
	return &SCMBranch{Name: branch, SHA: fmt.Sprintf("%040x", fnvHash(repo+branch)), Created: true}, nil
}

// CreatePullRequest implements SCM.
func (StubSCM) CreatePullRequest(_ context.Context, repo string, in SCMNewPullRequest) (*SCMPullRequest, error) {
	if err := CheckBranch(in.Head); err != nil {
		return nil, err
	}
	n := int(fnvHash(repo+in.Head)%9000) + 1000
	return &SCMPullRequest{Number: n, URL: fmt.Sprintf("https://github.com/%s/pull/%d", repo, n)}, nil
}

// CreateIssueComment implements SCM.
func (StubSCM) CreateIssueComment(_ context.Context, repo string, number int, key, _ string) (int64, error) {
	if !commentKeyRe.MatchString(key) || strings.Contains(key, "--") {
		return 0, fmt.Errorf("scm: invalid comment idempotency key %q", key)
	}
	return int64(fnvHash(fmt.Sprintf("%s#%d/%s", repo, number, key))), nil
}

func fnvHash(s string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return h.Sum32()
}

var commentKeyRe = regexp.MustCompile(`^[A-Za-z0-9._:/@+=-]{1,128}$`)

var branchRe = regexp.MustCompile(`^alpine/[0-7][0-9A-HJKMNP-TV-Z]{25}-[a-z0-9]+(-[a-z0-9]+)*$`)

// CheckBranch enforces "alpine/<ticket_ulid>-<slug>", the format the integrations GitHub client
// requires for every branch Alpine writes.
func CheckBranch(branch string) error {
	if !branchRe.MatchString(branch) {
		return fmt.Errorf("scm: branch %q is not alpine/<ticket_ulid>-<slug>", branch)
	}
	return nil
}
