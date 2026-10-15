package ticketrepo

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/alpine-dev/alpine/packages/shared/budget"
	"github.com/alpine-dev/alpine/packages/shared/db"
	"github.com/alpine-dev/alpine/packages/shared/gate"
	"github.com/alpine-dev/alpine/packages/shared/intake"
	"github.com/alpine-dev/alpine/packages/shared/llm"
	"github.com/alpine-dev/alpine/packages/shared/ticket"
)

// ErrNotFound is returned when a row does not exist or is not visible to the tenant.
var ErrNotFound = errors.New("ticketrepo: not found")

// Store implements the API and orchestrator repository interfaces on Postgres.
type Store struct {
	DB *db.DB
}

func (r *Store) tenant(ctx context.Context, org uuid.UUID, fn func(tx pgx.Tx) error) error {
	return r.DB.WithTenant(ctx, db.Scope{OrgID: org}, fn)
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// ---- repos ---------------------------------------------------------------------------------

// ListRepos lists repos of the tenant.
func (r *Store) ListRepos(ctx context.Context, org uuid.UUID) ([]Repo, error) {
	var out []Repo
	err := r.tenant(ctx, org, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT r.id, r.org_id, r.full_name, r.default_branch, i.installation_id, r.config_sha, r.created_at
			FROM repos r JOIN scm_installations i ON i.id = r.installation_id
			WHERE r.enabled AND r.deleted_at IS NULL AND i.uninstalled_at IS NULL ORDER BY r.full_name`)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Repo, error) {
			var x Repo
			err := row.Scan(&x.ID, &x.OrgID, &x.FullName, &x.DefaultBranch, &x.GitHubInstallationID, &x.ConfigSHA, &x.CreatedAt)
			return x, err
		})
		return err
	})
	return out, err
}

// ---- webhook inbox -------------------------------------------------------------------------

// ResolveInstallation maps a GitHub App installation id to its org (cross-tenant, ids only).
func (r *Store) ResolveInstallation(ctx context.Context, provider string, installationID int64) (uuid.UUID, bool, error) {
	var org uuid.UUID
	err := r.DB.WithoutTenant(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT org_id FROM alpine_resolve_installation($1, $2)`, provider, installationID).Scan(&org)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	return org, err == nil, err
}

// InsertInboxEvent stores a webhook delivery; inserted=false means duplicate delivery.
func (r *Store) InsertInboxEvent(ctx context.Context, org uuid.UUID, source, deliveryID, eventType, action string, payload []byte) (bool, error) {
	var inserted bool
	err := r.tenant(ctx, org, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO inbox_events (org_id, source, delivery_id, event_type, action, payload)
			VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6) ON CONFLICT (source, delivery_id) DO NOTHING`,
			org, source, deliveryID, eventType, action, payload)
		inserted = tag.RowsAffected() == 1
		return err
	})
	return inserted, err
}

// PendingInbox lists unprocessed events across tenants (ids + org only).
func (r *Store) PendingInbox(ctx context.Context, limit int) ([]PendingInbox, error) {
	var out []PendingInbox
	err := r.DB.WithoutTenant(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, org_id FROM alpine_pending_inbox($1)`, limit)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, pgx.RowToStructByPos[PendingInbox])
		return err
	})
	return out, err
}

// LoadInboxEvent loads one event inside the tenant.
func (r *Store) LoadInboxEvent(ctx context.Context, org, id uuid.UUID) (InboxEvent, error) {
	var e InboxEvent
	err := r.tenant(ctx, org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id, org_id, source, delivery_id, event_type, action, payload, attempts
			FROM inbox_events WHERE id = $1`, id).
			Scan(&e.ID, &e.OrgID, &e.Source, &e.DeliveryID, &e.EventType, &e.Action, &e.Payload, &e.Attempts)
	})
	return e, notFound(err)
}

// OutcomeCategory maps a processor outcome ("ticket_created:<id>", "ignored:unmapped_sentry_project",
// "close:<id>", …) to the inbox_events.outcome enum.
func OutcomeCategory(outcome string) string {
	head, _, _ := strings.Cut(outcome, ":")
	switch head {
	case "ticket_created", "ticket_updated", "signalled", "ignored", "failed":
		return head
	case "workflow_started":
		return "ticket_updated"
	case "close", "mark_merged", "approve", "reject", "resume":
		return "signalled"
	default: // duplicate, rejected, …
		return "ignored"
	}
}

// MarkInbox records the outcome of processing (processed when errMsg is empty): the outcome
// category (inbox_events.outcome enum), the detailed outcome string (outcome_detail) and the
// issue-level dedupe key the normalizer produced (dedupe_key).
func (r *Store) MarkInbox(ctx context.Context, org, id uuid.UUID, outcome, dedupeKey, errMsg string) error {
	category := OutcomeCategory(outcome)
	detail := outcome
	if len(detail) > 1024 {
		detail = detail[:1024]
	}
	if len(dedupeKey) > 256 {
		dedupeKey = dedupeKey[:256]
	}
	return r.tenant(ctx, org, func(tx pgx.Tx) error {
		if errMsg == "" {
			_, err := tx.Exec(ctx, `UPDATE inbox_events SET processed_at = now(), outcome = $2, outcome_detail = NULLIF($3, ''),
				dedupe_key = COALESCE(NULLIF($4, ''), dedupe_key), attempts = attempts + 1, last_error = NULL WHERE id = $1`,
				id, category, detail, dedupeKey)
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE inbox_events SET attempts = attempts + 1, last_error = $2,
			dedupe_key = COALESCE(NULLIF($3, ''), dedupe_key),
			processed_at = CASE WHEN attempts + 1 >= 10 THEN now() END,
			outcome = CASE WHEN attempts + 1 >= 10 THEN 'failed' END WHERE id = $1`, id, errMsg, dedupeKey)
		return err
	})
}

// UpsertRepoForIntake makes sure the repo referenced by an intake event exists (known installation).
func (r *Store) UpsertRepoForIntake(ctx context.Context, org uuid.UUID, ref intake.RepoRef) (uuid.UUID, error) {
	var id uuid.UUID
	err := r.tenant(ctx, org, func(tx pgx.Tx) error {
		branch := ref.DefaultBranch
		if branch == "" {
			branch = "main"
		}
		return tx.QueryRow(ctx, `INSERT INTO repos (org_id, installation_id, provider_repo_id, full_name, default_branch)
			SELECT $1, i.id, $3, $4, $5 FROM scm_installations i WHERE i.provider = 'github' AND i.installation_id = $2
			ON CONFLICT (installation_id, provider_repo_id) WHERE deleted_at IS NULL DO UPDATE SET full_name = EXCLUDED.full_name
			RETURNING id`, org, ref.InstallationID, ref.RepoID, ref.FullName, branch).Scan(&id)
	})
	return id, notFound(err)
}

// NewTicket is the input to CreateTicket.
type NewTicket struct {
	ID           uuid.UUID
	OrgID        uuid.UUID
	RepoID       uuid.UUID
	Key          string
	Title        string
	Body         string
	Source       string
	SourceURL    string
	ExternalID   string
	InboxEventID uuid.UUID
	WorkflowID   string
	Caps         budget.TicketCaps
}

// CreateTicket inserts a ticket (idempotent on id) with its source link and initial history.
// created=false means it already existed.
func (r *Store) CreateTicket(ctx context.Context, t NewTicket) (bool, error) {
	caps := t.Caps.WithDefaults()
	var created bool
	err := r.tenant(ctx, t.OrgID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO tickets (id, org_id, repo_id, key, title, body, source, source_url, workflow_id,
				max_turns, max_gate_iterations, max_cost_usd)
			VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, ''), $9, $10, $11, $12) ON CONFLICT DO NOTHING`,
			t.ID, t.OrgID, t.RepoID, t.Key, t.Title, t.Body, t.Source, t.SourceURL, t.WorkflowID,
			caps.MaxTurns, caps.MaxGateIterations, caps.MaxCostUSD)
		if err != nil {
			return err
		}
		created = tag.RowsAffected() == 1
		if !created {
			return nil
		}
		var inbox any
		if t.InboxEventID != uuid.Nil {
			inbox = t.InboxEventID
		}
		if _, err := tx.Exec(ctx, `INSERT INTO ticket_sources (org_id, ticket_id, inbox_event_id, source, external_id, external_url)
			VALUES ($1, $2, $3, $4, $5, NULLIF($6, '')) ON CONFLICT DO NOTHING`,
			t.OrgID, t.ID, inbox, t.Source, t.ExternalID, t.SourceURL); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO ticket_stage_history (org_id, ticket_id, from_stage, to_stage, reason, actor, idempotency_key)
			VALUES ($1, $2, NULL, 'intake', $3, 'system', 'create')`, t.OrgID, t.ID, "created from "+t.Source); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO audit_log (org_id, actor_type, actor_id, action, target_type, target_id, metadata)
			VALUES ($1, 'system', 'inbox-processor', 'ticket.created', 'ticket', $2, $3)`,
			t.OrgID, t.ID.String(), map[string]any{"source": t.Source, "externalId": t.ExternalID})
		return err
	})
	return created, err
}

// ---- tickets (read) ------------------------------------------------------------------------

const ticketCols = `t.id, t.org_id, t.repo_id, r.full_name, t.key, t.title, t.body, t.type, t.stage, t.source, t.source_url,
	t.priority, t.size, t.br_predicted, t.br_final, t.confidence::float8, t.workflow_id, t.turns_used, t.gate_iterations,
	t.cost_usd::float8, t.tokens_used, t.max_turns, t.max_gate_iterations, t.max_cost_usd::float8, t.escalation_reason,
	t.created_at, t.updated_at, r.default_branch,
	(SELECT s.external_id FROM ticket_sources s WHERE s.ticket_id = t.id AND s.source = 'github' ORDER BY s.created_at LIMIT 1)`

func scanTicket(row pgx.Row) (Ticket, error) {
	var t Ticket
	var stage string
	err := row.Scan(&t.ID, &t.OrgID, &t.RepoID, &t.RepoFullName, &t.Key, &t.Title, &t.Body, &t.Type, &stage, &t.Source, &t.SourceURL,
		&t.Priority, &t.Size, &t.BRPredicted, &t.BRFinal, &t.Confidence, &t.WorkflowID, &t.TurnsUsed, &t.GateIterations,
		&t.CostUSD, &t.TokensUsed, &t.MaxTurns, &t.MaxGateIters, &t.MaxCostUSD, &t.EscalationReason, &t.CreatedAt, &t.UpdatedAt,
		&t.DefaultBranch, &t.GitHubExternalID)
	t.Stage = ticket.Stage(stage)
	return t, err
}

// ListTickets lists tickets for the board.
func (r *Store) ListTickets(ctx context.Context, org uuid.UUID, f TicketFilter) ([]Ticket, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 200
	}
	stages := make([]string, 0, len(f.Stages))
	for _, s := range f.Stages {
		stages = append(stages, string(s))
	}
	var out []Ticket
	err := r.tenant(ctx, org, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+ticketCols+` FROM tickets t JOIN repos r ON r.id = t.repo_id
			WHERE ($1::uuid IS NULL OR t.repo_id = $1) AND (cardinality($2::text[]) = 0 OR t.stage = ANY($2))
			ORDER BY t.updated_at DESC LIMIT $3`, f.RepoID, stages, f.Limit)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Ticket, error) { return scanTicket(row) })
		return err
	})
	return out, err
}

// GetTicket returns one ticket.
func (r *Store) GetTicket(ctx context.Context, org, id uuid.UUID) (Ticket, error) {
	var t Ticket
	err := r.tenant(ctx, org, func(tx pgx.Tx) error {
		var err error
		t, err = scanTicket(tx.QueryRow(ctx, `SELECT `+ticketCols+` FROM tickets t JOIN repos r ON r.id = t.repo_id WHERE t.id = $1`, id))
		return err
	})
	return t, notFound(err)
}

// StageHistory returns a ticket's transitions, oldest first.
func (r *Store) StageHistory(ctx context.Context, org, ticketID uuid.UUID) ([]StageTransition, error) {
	var out []StageTransition
	err := r.tenant(ctx, org, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, from_stage, to_stage, reason, actor, at FROM ticket_stage_history
			WHERE ticket_id = $1 ORDER BY at, id`, ticketID)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, pgx.RowToStructByPos[StageTransition])
		return err
	})
	return out, err
}

// ListRuns returns a ticket's runs.
func (r *Store) ListRuns(ctx context.Context, org, ticketID uuid.UUID) ([]Run, error) {
	var out []Run
	err := r.tenant(ctx, org, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, ticket_id, kind, status, model, turns, gate_iterations, cost_usd::float8, started_at, ended_at
			FROM runs WHERE ticket_id = $1 ORDER BY started_at, id`, ticketID)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, pgx.RowToStructByPos[Run])
		return err
	})
	return out, err
}

// ListTurns returns a run's turns; found=false if the run is not visible.
func (r *Store) ListTurns(ctx context.Context, org, runID uuid.UUID) ([]Turn, bool, error) {
	var out []Turn
	found := false
	err := r.tenant(ctx, org, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM runs WHERE id = $1)`, runID).Scan(&found); err != nil || !found {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id, run_id, n, model, tokens_in, tokens_out, tokens_cached, cost_usd::float8, tool_calls, transcript_ref, created_at
			FROM agent_turns WHERE run_id = $1 ORDER BY n`, runID)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Turn, error) {
			var t Turn
			var tools []byte
			err := row.Scan(&t.ID, &t.RunID, &t.N, &t.Model, &t.TokensIn, &t.TokensOut, &t.TokensCached, &t.CostUSD, &tools, &t.TranscriptRef, &t.CreatedAt)
			if err == nil {
				_ = json.Unmarshal(tools, &t.ToolCalls)
			}
			return t, err
		})
		return err
	})
	return out, found, err
}

// ListGateResults returns a ticket's gate results (optionally one run).
func (r *Store) ListGateResults(ctx context.Context, org, ticketID uuid.UUID, runID *uuid.UUID) ([]GateResult, error) {
	var out []GateResult
	err := r.tenant(ctx, org, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, run_id, ticket_id, iteration, gate_id, status, duration_ms, findings, artifacts_ref, created_at
			FROM gate_results WHERE ticket_id = $1 AND ($2::uuid IS NULL OR run_id = $2) ORDER BY iteration, gate_id`, ticketID, runID)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (GateResult, error) {
			var g GateResult
			var findings []byte
			var status string
			err := row.Scan(&g.ID, &g.RunID, &g.TicketID, &g.Iteration, &g.GateID, &status, &g.DurationMs, &findings, &g.ArtifactsRef, &g.CreatedAt)
			g.Status = gate.Status(status)
			if err == nil {
				_ = json.Unmarshal(findings, &g.Findings)
			}
			return g, err
		})
		return err
	})
	return out, err
}

// ListPullRequests returns the PR inbox: blast radius desc, then priority, then age.
func (r *Store) ListPullRequests(ctx context.Context, org uuid.UUID, state *string, ticketID *uuid.UUID) ([]PullRequest, error) {
	var out []PullRequest
	err := r.tenant(ctx, org, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, prSelect+`
			WHERE ($1::text IS NULL OR p.state = $1) AND ($2::uuid IS NULL OR p.ticket_id = $2)
			ORDER BY p.blast_radius DESC, COALESCE(t.priority, 'P9') ASC, p.opened_at ASC`, state, ticketID)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, pgx.RowToStructByPos[PullRequest])
		return err
	})
	return out, err
}

// prSelect joins a PR with its ticket, repo and the gate counts of the ticket's latest gate
// iteration. Column order matches PullRequest.
const prSelect = `SELECT p.id, p.ticket_id, t.key, t.title, r.full_name, p.number, p.url, p.head_branch, p.head_sha,
		p.state, p.blast_radius, t.priority, t.turns_used, t.cost_usd::float8, p.opened_at, p.merged_at,
		p.additions, p.deletions, p.files_changed, p.human_decision,
		COALESCE(g.passed, 0)::int, COALESCE(g.failed, 0)::int, COALESCE(g.warned, 0)::int, COALESCE(g.total, 0)::int
	FROM pull_requests p JOIN tickets t ON t.id = p.ticket_id JOIN repos r ON r.id = p.repo_id
	LEFT JOIN LATERAL (
		SELECT count(*) FILTER (WHERE gr.status IN ('pass','skipped')) AS passed,
		       count(*) FILTER (WHERE gr.status IN ('fail','error')) AS failed,
		       count(*) FILTER (WHERE gr.status = 'warn') AS warned,
		       count(*) AS total
		FROM gate_results gr
		WHERE (gr.run_id, gr.iteration) = (
			SELECT l.run_id, l.iteration FROM gate_results l WHERE l.ticket_id = p.ticket_id
			ORDER BY l.created_at DESC, l.iteration DESC LIMIT 1)
	) g ON true`

// ---- orchestrator writes -------------------------------------------------------------------

// SetStage moves a ticket to a stage, writing history + audit. idemKey (the Temporal activity
// id) makes a retried activity a no-op. Returns the previous stage and the repo id.
func (r *Store) SetStage(ctx context.Context, org, ticketID uuid.UUID, to ticket.Stage, reason, actor, idemKey string) (ticket.Stage, uuid.UUID, bool, error) {
	var prev string
	var repo uuid.UUID
	changed := false
	err := r.tenant(ctx, org, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT stage, repo_id FROM tickets WHERE id = $1 FOR UPDATE`, ticketID).Scan(&prev, &repo); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `INSERT INTO ticket_stage_history (org_id, ticket_id, from_stage, to_stage, reason, actor, idempotency_key)
			VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6, NULLIF($7, '')) ON CONFLICT (ticket_id, idempotency_key) DO NOTHING`,
			org, ticketID, prev, string(to), reason, actor, idemKey)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil // replayed activity
		}
		changed = true
		esc := any(nil)
		if to == ticket.StageEscalated {
			esc = reason
		}
		if _, err := tx.Exec(ctx, `UPDATE tickets SET stage = $2, escalation_reason = $3 WHERE id = $1`, ticketID, string(to), esc); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO audit_log (org_id, actor_type, actor_id, action, target_type, target_id, metadata)
			VALUES ($1, $2, $3, 'ticket.stage_changed', 'ticket', $4, $5)`, org, actorType(actor), actor, ticketID.String(),
			map[string]any{"from": prev, "to": to, "reason": reason})
		return err
	})
	return ticket.Stage(prev), repo, changed, notFound(err)
}

func actorType(actor string) string {
	switch {
	case len(actor) >= 5 && actor[:5] == "user:":
		return "user"
	case len(actor) >= 6 && actor[:6] == "agent:":
		return "agent"
	default:
		return "system"
	}
}

// TriageUpdate is the persisted triage outcome.
type TriageUpdate struct {
	Type        ticket.Type
	DuplicateOf *uuid.UUID
}

// ApplyTriage stores triage classification.
func (r *Store) ApplyTriage(ctx context.Context, org, ticketID uuid.UUID, u TriageUpdate) error {
	return r.tenant(ctx, org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE tickets SET type = $2, duplicate_of = $3 WHERE id = $1`, ticketID, string(u.Type), u.DuplicateOf)
		return err
	})
}

// RefinementUpdate is the persisted refinement outcome.
type RefinementUpdate struct {
	Priority    string
	Size        string
	BRPredicted int
	Confidence  float64
	Body        string // refined ticket body (problem, repro, AC); empty keeps the original
}

// ApplyRefinement stores refinement estimates.
func (r *Store) ApplyRefinement(ctx context.Context, org, ticketID uuid.UUID, u RefinementUpdate) error {
	return r.tenant(ctx, org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE tickets SET priority = $2, size = $3, br_predicted = $4::int, br_final = GREATEST(COALESCE(br_final, 0), $4::int),
			confidence = $5, body = CASE WHEN $6::text = '' THEN body ELSE $6::text END WHERE id = $1`,
			ticketID, u.Priority, u.Size, u.BRPredicted, u.Confidence, u.Body)
		return err
	})
}

// RaiseBlastRadius raises br_final (never lowers).
func (r *Store) RaiseBlastRadius(ctx context.Context, org, ticketID uuid.UUID, br int) error {
	return r.tenant(ctx, org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE tickets SET br_final = GREATEST(COALESCE(br_final, 0), $2) WHERE id = $1`, ticketID, br)
		return err
	})
}

// StartRun inserts a run (idempotent on id).
func (r *Store) StartRun(ctx context.Context, org, ticketID, runID uuid.UUID, kind, model string) error {
	return r.tenant(ctx, org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO runs (id, org_id, ticket_id, kind, model) VALUES ($1, $2, $3, $4, NULLIF($5, ''))
			ON CONFLICT (id) DO NOTHING`, runID, org, ticketID, kind, model)
		return err
	})
}

// FinishRun closes a run (idempotent).
func (r *Store) FinishRun(ctx context.Context, org, runID uuid.UUID, status string) error {
	return r.tenant(ctx, org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE runs SET status = $2, ended_at = COALESCE(ended_at, now()) WHERE id = $1`, runID, status)
		return err
	})
}

// NewTurn is one implementer turn to persist.
type NewTurn struct {
	RunID        uuid.UUID
	TicketID     uuid.UUID
	N            int
	Model        string
	Usage        llm.Usage
	CostUSD      float64
	ToolCalls    []ToolCallSummary
	GateIterDiff int
}

// RecordTurn persists a turn and bumps run/ticket counters exactly once (unique run_id, n).
func (r *Store) RecordTurn(ctx context.Context, org uuid.UUID, t NewTurn) (bool, error) {
	inserted := false
	err := r.tenant(ctx, org, func(tx pgx.Tx) error {
		tools, _ := json.Marshal(t.ToolCalls)
		if t.ToolCalls == nil {
			tools = []byte("[]")
		}
		tag, err := tx.Exec(ctx, `INSERT INTO agent_turns (org_id, run_id, n, model, tokens_in, tokens_out, tokens_cached, cost_usd, tool_calls)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) ON CONFLICT (run_id, n) DO NOTHING`,
			org, t.RunID, t.N, t.Model, t.Usage.InputTokens, t.Usage.OutputTokens, t.Usage.CachedTokens, t.CostUSD, tools)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		inserted = true
		if _, err := tx.Exec(ctx, `UPDATE runs SET turns = turns + 1, cost_usd = cost_usd + $2 WHERE id = $1`, t.RunID, t.CostUSD); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE tickets SET turns_used = turns_used + 1, cost_usd = cost_usd + $2, tokens_used = tokens_used + $3 WHERE id = $1`,
			t.TicketID, t.CostUSD, t.Usage.Total())
		return err
	})
	return inserted, err
}

// LastTurn returns the highest persisted turn number of a run (0 if none) — used to resume
// an implement activity after a worker crash.
func (r *Store) LastTurn(ctx context.Context, org, runID uuid.UUID) (int, error) {
	var n int
	err := r.tenant(ctx, org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT COALESCE(max(n), 0) FROM agent_turns WHERE run_id = $1`, runID).Scan(&n)
	})
	return n, err
}

// AddTicketCost adds non-turn spend (triage/refine/review calls) to the ticket counters.
func (r *Store) AddTicketCost(ctx context.Context, org, ticketID uuid.UUID, cost float64, tokens int64) error {
	return r.tenant(ctx, org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE tickets SET cost_usd = cost_usd + $2, tokens_used = tokens_used + $3 WHERE id = $1`, ticketID, cost, tokens)
		return err
	})
}

// RecordGateReport persists one gate iteration (idempotent per run/iteration/gate) and bumps
// the ticket's gate iteration counter once.
func (r *Store) RecordGateReport(ctx context.Context, org, ticketID, runID uuid.UUID, rep gate.Report) error {
	return r.tenant(ctx, org, func(tx pgx.Tx) error {
		newRows := int64(0)
		for _, res := range rep.Results {
			findings, _ := json.Marshal(res.Findings)
			if res.Findings == nil {
				findings = []byte("[]")
			}
			var ref any
			if len(res.ArtifactKeys) > 0 {
				ref = res.ArtifactKeys[0]
			}
			tag, err := tx.Exec(ctx, `INSERT INTO gate_results (org_id, run_id, ticket_id, iteration, gate_id, status, duration_ms, summary, findings, artifacts_ref)
				VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, ''), $9, $10) ON CONFLICT (run_id, iteration, gate_id) DO NOTHING`,
				org, runID, ticketID, rep.Iteration, res.GateID, string(res.Status), res.Duration.Milliseconds(), res.Summary, findings, ref)
			if err != nil {
				return err
			}
			newRows += tag.RowsAffected()
		}
		if newRows == 0 {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE runs SET gate_iterations = GREATEST(gate_iterations, $2) WHERE id = $1`, runID, rep.Iteration); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE tickets SET gate_iterations = gate_iterations + 1 WHERE id = $1`, ticketID)
		return err
	})
}

// BeginSideEffect claims an idempotency key for an external side effect. If the effect already
// completed, done=true and ref is the stored external reference (e.g. PR number).
func (r *Store) BeginSideEffect(ctx context.Context, org uuid.UUID, ticketID *uuid.UUID, key, kind string) (done bool, ref string, err error) {
	err = r.tenant(ctx, org, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO side_effects (org_id, ticket_id, idempotency_key, kind) VALUES ($1, $2, $3, $4)
			ON CONFLICT (idempotency_key) DO NOTHING`, org, ticketID, key, kind); err != nil {
			return err
		}
		var status string
		var ext *string
		if err := tx.QueryRow(ctx, `SELECT status, external_ref FROM side_effects WHERE idempotency_key = $1`, key).Scan(&status, &ext); err != nil {
			return err
		}
		done = status == "done"
		if ext != nil {
			ref = *ext
		}
		return nil
	})
	return done, ref, err
}

// CompleteSideEffect marks a side effect done with its external reference.
func (r *Store) CompleteSideEffect(ctx context.Context, org uuid.UUID, key, ref string) error {
	return r.tenant(ctx, org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE side_effects SET status = 'done', external_ref = $2, completed_at = now() WHERE idempotency_key = $1`, key, ref)
		return err
	})
}

// NewPullRequest is a PR row to insert.
type NewPullRequest struct {
	TicketID    uuid.UUID
	RepoID      uuid.UUID
	Number      int
	URL         string
	HeadBranch  string
	HeadSHA     string
	BlastRadius int
	// Optional diff summary (nil = unknown; a later upsert with values fills it in).
	Additions    *int
	Deletions    *int
	FilesChanged *int
}

// UpsertPullRequest records an opened PR (idempotent on repo+number).
func (r *Store) UpsertPullRequest(ctx context.Context, org uuid.UUID, p NewPullRequest) error {
	return r.tenant(ctx, org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO pull_requests (org_id, ticket_id, repo_id, number, url, head_branch, head_sha, blast_radius,
				additions, deletions, files_changed)
			VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''), $8, $9, $10, $11) ON CONFLICT (repo_id, number) DO UPDATE SET
				head_sha = EXCLUDED.head_sha, additions = COALESCE(EXCLUDED.additions, pull_requests.additions),
				deletions = COALESCE(EXCLUDED.deletions, pull_requests.deletions),
				files_changed = COALESCE(EXCLUDED.files_changed, pull_requests.files_changed)`,
			org, p.TicketID, p.RepoID, p.Number, p.URL, p.HeadBranch, p.HeadSHA, p.BlastRadius, p.Additions, p.Deletions, p.FilesChanged)
		return err
	})
}

// MarkPullRequestsMerged marks a ticket's open PRs merged.
func (r *Store) MarkPullRequestsMerged(ctx context.Context, org, ticketID uuid.UUID, by string) error {
	return r.tenant(ctx, org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE pull_requests SET state = 'merged', merged_by = $2, merged_at = COALESCE(merged_at, now())
			WHERE ticket_id = $1 AND state = 'open'`, ticketID, by)
		return err
	})
}

// Audit appends to the audit log.
func (r *Store) Audit(ctx context.Context, org uuid.UUID, actor, action, targetType, targetID string, meta map[string]any) error {
	if meta == nil {
		meta = map[string]any{}
	}
	return r.tenant(ctx, org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO audit_log (org_id, actor_type, actor_id, action, target_type, target_id, metadata)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`, org, actorType(actor), actor, action, targetType, targetID, meta)
		return err
	})
}
