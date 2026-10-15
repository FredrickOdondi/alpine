package activities

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"

	"github.com/alpine-dev/alpine/packages/agents"
	"github.com/alpine-dev/alpine/packages/shared/ticketflow"
	"github.com/alpine-dev/alpine/packages/shared/ticketrepo"
)

// implementProgress is heartbeated after every persisted turn.
type implementProgress struct {
	LastTurn int     `json:"lastTurn"` // ticket-wide turn number
	Turns    int     `json:"turns"`    // turns taken by this activity
	CostUSD  float64 `json:"costUsd"`
	Tokens   int64   `json:"tokens"`
}

// Implement runs implementer turns until the agent is ready for gates or the turn/cost budget
// for this activity is used up. It is resumable: each turn is persisted (unique run_id, n) and
// heartbeated, so after a worker crash the retried activity continues from the last turn rather
// than starting over, and the per-turn LLM idempotency key replays any in-flight call.
func (a *Activities) Implement(ctx context.Context, in ImplementInput) (ImplementResult, error) {
	org, tid, err := ids(in.Ref)
	if err != nil {
		return ImplementResult{}, err
	}
	runID, err := uuid.Parse(in.RunID)
	if err != nil {
		return ImplementResult{}, temporal.NewNonRetryableApplicationError("bad run id", ErrTypeNotFound, err)
	}
	ti, t, err := a.ticketInput(ctx, in.Ref)
	if err != nil {
		return ImplementResult{}, err
	}
	// The work branch is created (idempotently) before the first turn so the sandbox can push to
	// it. Its name is unique per ticket: alpine/<ticket_ulid>-<slug>.
	branch, err := a.branchName(tid, t.Title)
	if err != nil {
		return ImplementResult{}, temporal.NewNonRetryableApplicationError(err.Error(), ErrTypeInvalidOutput, err)
	}
	headSHA := ""
	if a.SCM != nil {
		base := t.DefaultBranch
		if base == "" {
			base = "main"
		}
		b, err := a.SCM.CreateBranchFrom(ctx, t.RepoFullName, branch, base)
		if err != nil {
			return ImplementResult{}, err
		}
		headSHA = b.SHA
	}

	if loop, ok := a.Agents.(agents.ToolLoop); ok && a.Workspaces != nil {
		return a.implementLive(ctx, in, loop, ti, t, org, tid, runID, branch)
	}

	var prog implementProgress
	resumed := false
	if activity.IsActivity(ctx) && activity.HasHeartbeatDetails(ctx) {
		if err := activity.GetHeartbeatDetails(ctx, &prog); err == nil {
			resumed = true
		}
	}
	// The DB is the source of truth for completed turns (heartbeats can lag by one turn).
	if last, err := a.Repo.LastTurn(ctx, org, runID); err == nil && last > prog.LastTurn && last > in.TurnsSoFar {
		prog.Turns += last - max(prog.LastTurn, in.TurnsSoFar)
		prog.LastTurn = last
		resumed = true
	}
	if prog.LastTurn < in.TurnsSoFar {
		prog.LastTurn = in.TurnsSoFar
	}

	base := idemKey(ctx)
	for prog.Turns < in.TurnBudget {
		if prog.CostUSD >= in.CostBudgetUSD && in.CostBudgetUSD > 0 {
			break
		}
		if err := ctx.Err(); err != nil {
			return ImplementResult{}, err
		}
		n := prog.LastTurn + 1
		// The LLM key is per ticket turn (not per activity attempt) so a retry replays the
		// in-flight call instead of paying for a divergent one.
		key := fmt.Sprintf("%s/turn-%d", base, n)
		r, err := a.Agents.ImplementTurn(ctx, agents.Call{Tenant: a.tenant(in.Ref), IdempotencyKey: key}, agents.ImplementInput{
			Ticket: ti, AcceptanceCriteria: in.AcceptanceCriteria, Turn: n, Feedback: in.Feedback, ReviewFeedback: in.ReviewFeedback,
		})
		if err != nil {
			return ImplementResult{}, classify(err)
		}
		tools := make([]ticketrepo.ToolCallSummary, 0, len(r.Output.ToolCalls))
		for _, tc := range r.Output.ToolCalls {
			tools = append(tools, ticketrepo.ToolCallSummary{Name: tc.Name, Summary: tc.Summary})
		}
		inserted, err := a.Repo.RecordTurn(ctx, org, ticketrepo.NewTurn{RunID: runID, TicketID: tid, N: n, Model: r.Model, Usage: r.Usage, CostUSD: r.CostUSD, ToolCalls: tools})
		if err != nil {
			return ImplementResult{}, err
		}
		prog.LastTurn = n
		prog.Turns++
		if inserted {
			prog.CostUSD += r.CostUSD
			prog.Tokens += r.Usage.Total()
			turnCounter.Add(ctx, 1)
			costCounter.Add(ctx, r.CostUSD)
		}
		if activity.IsActivity(ctx) {
			activity.RecordHeartbeat(ctx, prog)
		}
		if r.Output.ReadyForGates {
			return ImplementResult{
				Usage:         Usage{Turns: prog.Turns, CostUSD: prog.CostUSD, Tokens: prog.Tokens},
				ReadyForGates: true,
				// Stub: the real implementer pushes commits from the sandbox and reports the new head.
				HeadSHA: stubHead(headSHA, n),
				BaseSHA: headSHA,
				Branch:  branch,
				Resumed: resumed,
			}, nil
		}
	}
	return ImplementResult{Usage: Usage{Turns: prog.Turns, CostUSD: prog.CostUSD, Tokens: prog.Tokens}, Branch: branch, BaseSHA: headSHA, Resumed: resumed}, nil
}

// implementLive runs the real implementer: a per-ticket checkout + offline Docker sandbox, the
// tool-using agent loop, then a commit pushed to the work branch. Turns are persisted as they
// happen; the LLM idempotency keys make a retried activity replay paid calls.
func (a *Activities) implementLive(ctx context.Context, in ImplementInput, loop agents.ToolLoop, ti agents.TicketInput,
	t ticketrepo.Ticket, org, tid, runID uuid.UUID, branch string) (ImplementResult, error) {
	base := t.DefaultBranch
	if base == "" {
		base = "main"
	}
	co, err := a.Workspaces.Open(ctx, ticketflow.TicketULID(tid), t.RepoFullName, branch, base)
	if err != nil {
		return ImplementResult{}, fmt.Errorf("open workspace: %w", err)
	}
	defer func() { _ = co.Close(context.WithoutCancel(ctx)) }()
	baseSHA, err := co.BaseSHA(ctx)
	if err != nil {
		return ImplementResult{}, err
	}
	stop := heartbeatEvery(ctx, 20*time.Second, implementProgress{LastTurn: in.TurnsSoFar})
	defer stop()

	log := a.log().With("ticket", t.Key, "run", runID.String())
	var prog implementProgress
	prog.LastTurn = in.TurnsSoFar
	key := idemKey(ctx)
	res, err := loop.ImplementLoop(ctx, agents.Call{Tenant: a.tenant(in.Ref), IdempotencyKey: key},
		agents.ImplementInput{Ticket: ti, AcceptanceCriteria: in.AcceptanceCriteria, Turn: in.TurnsSoFar + 1, Feedback: in.Feedback, ReviewFeedback: in.ReviewFeedback},
		co, agents.LoopOptions{MaxTurns: in.TurnBudget, MaxCostUSD: in.CostBudgetUSD, FirstTurn: in.TurnsSoFar + 1},
		func(lt agents.LoopTurn) error {
			tools := make([]ticketrepo.ToolCallSummary, 0, len(lt.ToolCalls))
			for _, tc := range lt.ToolCalls {
				tools = append(tools, ticketrepo.ToolCallSummary{Name: tc.Name, Summary: tc.Summary})
			}
			inserted, err := a.Repo.RecordTurn(ctx, org, ticketrepo.NewTurn{RunID: runID, TicketID: tid, N: lt.N, Model: lt.Model, Usage: lt.Usage, CostUSD: lt.CostUSD, ToolCalls: tools})
			if err != nil {
				return err
			}
			prog.LastTurn = lt.N
			prog.Turns++
			if inserted && !lt.Replayed {
				prog.CostUSD += lt.CostUSD
				prog.Tokens += lt.Usage.Total()
				turnCounter.Add(ctx, 1)
				costCounter.Add(ctx, lt.CostUSD)
			}
			log.Info("implementer turn", "turn", lt.N, "model", lt.Model, "tokens_in", lt.Usage.InputTokens,
				"tokens_out", lt.Usage.OutputTokens, "cost_usd", lt.CostUSD, "tools", len(lt.ToolCalls))
			if activity.IsActivity(ctx) {
				activity.RecordHeartbeat(ctx, prog)
			}
			return nil
		})
	usage := Usage{Turns: prog.Turns, CostUSD: prog.CostUSD, Tokens: prog.Tokens}
	if err != nil {
		return ImplementResult{Usage: usage}, classify(err)
	}
	out := ImplementResult{Usage: usage, Branch: branch, BaseSHA: baseSHA, RepoDir: co.Dir(), Summary: res.Summary, StopReason: res.StopReason}
	log.Info("implementer finished", "stop", res.StopReason, "ready", res.ReadyForGates, "turns", prog.Turns,
		"tokens", prog.Tokens, "cost_usd", prog.CostUSD, "summary", res.Summary)
	if !res.ReadyForGates {
		return out, nil
	}
	msg := fmt.Sprintf("%s: %s\n\n%s\n\nAlpine-Ticket: %s", t.Key, t.Title, res.Summary, t.Key)
	head, changed, err := co.Commit(ctx, msg)
	if err != nil {
		return out, err
	}
	if head == baseSHA && !changed {
		out.StopReason = "no_change"
		return out, nil
	}
	if a.SCM != nil {
		if err := co.Push(ctx, branch); err != nil {
			return out, fmt.Errorf("push %s: %w", branch, err)
		}
	}
	out.HeadSHA, out.ReadyForGates = head, true
	return out, nil
}

// stubHead derives a deterministic fake commit SHA for turn n on top of base.
func stubHead(base string, n int) string {
	return fmt.Sprintf("%08x%032x", fnvHash(fmt.Sprintf("%s/%d", base, n)), n)
}
