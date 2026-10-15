package ticketrepo

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ---- reviews (independent reviewer verdicts) ------------------------------------------------

// ReviewFinding is one structured reviewer finding.
type ReviewFinding struct {
	Severity string  `json:"severity"` // info | minor | major | blocker
	Category string  `json:"category"`
	File     *string `json:"file"`
	Line     *int    `json:"line,omitempty"`
	Message  string  `json:"message"`
}

// NewReview is a reviewer verdict to persist.
type NewReview struct {
	TicketID       uuid.UUID
	RunID          *uuid.UUID
	Verdict        string // approve | request_changes | escalate
	Model          string
	Summary        string
	Findings       []ReviewFinding
	IdempotencyKey string // Temporal activity id: retries are no-ops
}

// Review is a persisted verdict.
type Review struct {
	ID        uuid.UUID
	TicketID  uuid.UUID
	Verdict   string
	Model     string
	Summary   *string
	Findings  []ReviewFinding
	CreatedAt time.Time
}

// RecordReview stores a reviewer verdict (idempotent on the idempotency key).
func (r *Store) RecordReview(ctx context.Context, org uuid.UUID, rv NewReview) error {
	if rv.Findings == nil {
		rv.Findings = []ReviewFinding{}
	}
	findings, err := json.Marshal(rv.Findings)
	if err != nil {
		return err
	}
	return r.tenant(ctx, org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO reviews (org_id, ticket_id, run_id, verdict, model, summary, findings, idempotency_key)
			VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), $7, $8) ON CONFLICT (org_id, idempotency_key) DO NOTHING`,
			org, rv.TicketID, rv.RunID, rv.Verdict, rv.Model, rv.Summary, findings, rv.IdempotencyKey)
		return err
	})
}

// LatestReview returns the newest verdict for a ticket (ErrNotFound when none).
func (r *Store) LatestReview(ctx context.Context, org, ticketID uuid.UUID) (Review, error) {
	var rv Review
	var raw []byte
	err := r.tenant(ctx, org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id, ticket_id, verdict, model, summary, findings, created_at FROM reviews
			WHERE ticket_id = $1 ORDER BY created_at DESC, id LIMIT 1`, ticketID).
			Scan(&rv.ID, &rv.TicketID, &rv.Verdict, &rv.Model, &rv.Summary, &raw, &rv.CreatedAt)
	})
	if err != nil {
		return rv, notFound(err)
	}
	_ = json.Unmarshal(raw, &rv.Findings)
	return rv, nil
}

// ---- artifacts --------------------------------------------------------------------------------

// Artifact is an object-storage artifact row (S3 key; URLs are signed by the caller).
type Artifact struct {
	ID          uuid.UUID
	TicketID    uuid.UUID
	RunID       *uuid.UUID
	Kind        string
	S3Key       string
	ContentType *string
	CreatedAt   time.Time
}

// ListArtifacts lists a ticket's artifacts, newest first.
func (r *Store) ListArtifacts(ctx context.Context, org, ticketID uuid.UUID) ([]Artifact, error) {
	var out []Artifact
	err := r.tenant(ctx, org, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, ticket_id, run_id, kind, s3_key, content_type, created_at FROM artifacts
			WHERE ticket_id = $1 ORDER BY created_at DESC`, ticketID)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, pgx.RowToStructByPos[Artifact])
		return err
	})
	return out, err
}

// ---- pull requests (inbox) --------------------------------------------------------------------

// GetPullRequest returns one PR with its inbox summary.
func (r *Store) GetPullRequest(ctx context.Context, org, id uuid.UUID) (PullRequest, error) {
	var out PullRequest
	err := r.tenant(ctx, org, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, prSelect+` WHERE p.id = $1`, id)
		if err != nil {
			return err
		}
		out, err = pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[PullRequest])
		return err
	})
	return out, notFound(err)
}

// ErrPullRequestClosed is returned when deciding on a PR that is no longer open.
var ErrPullRequestClosed = errors.New("ticketrepo: pull request is not open")

// SetPullRequestDecision records the human approve / send-back decision on an open PR and audits it.
func (r *Store) SetPullRequestDecision(ctx context.Context, org, id uuid.UUID, decision, actor, note string) error {
	return r.tenant(ctx, org, func(tx pgx.Tx) error {
		var state string
		if err := tx.QueryRow(ctx, `SELECT state FROM pull_requests WHERE id = $1 FOR UPDATE`, id).Scan(&state); err != nil {
			return notFound(err)
		}
		if state != "open" {
			return ErrPullRequestClosed
		}
		if _, err := tx.Exec(ctx, `UPDATE pull_requests SET human_decision = $2, human_decision_by = $3, human_decision_at = now()
			WHERE id = $1`, id, decision, actor); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO audit_log (org_id, actor_type, actor_id, action, target_type, target_id, metadata)
			VALUES ($1, $2, $3, 'pull_request.reviewed', 'pull_request', $4, $5)`,
			org, actorType(actor), actor, id.String(), map[string]any{"decision": decision, "note": note})
		return err
	})
}

// ---- repos / factory config ---------------------------------------------------------------------

// GetRepo returns one repo of the tenant.
func (r *Store) GetRepo(ctx context.Context, org, id uuid.UUID) (Repo, error) {
	var x Repo
	err := r.tenant(ctx, org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT r.id, r.org_id, r.full_name, r.default_branch, i.installation_id, r.config_sha, r.created_at
			FROM repos r JOIN scm_installations i ON i.id = r.installation_id WHERE r.id = $1`, id).
			Scan(&x.ID, &x.OrgID, &x.FullName, &x.DefaultBranch, &x.GitHubInstallationID, &x.ConfigSHA, &x.CreatedAt)
	})
	return x, notFound(err)
}

// FactoryConfigRow is the latest stored .alpine/factory.yaml for a repo.
type FactoryConfigRow struct {
	RepoID    uuid.UUID
	Version   int
	Source    string
	CommitSHA *string
	RawYAML   *string
	Parsed    []byte
	CreatedAt time.Time
}

// LatestFactoryConfig returns the newest factory config version of a repo (ErrNotFound when none).
func (r *Store) LatestFactoryConfig(ctx context.Context, org, repoID uuid.UUID) (FactoryConfigRow, error) {
	var c FactoryConfigRow
	err := r.tenant(ctx, org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT repo_id, version, source, commit_sha, raw_yaml, parsed, created_at FROM factory_configs
			WHERE repo_id = $1 ORDER BY version DESC LIMIT 1`, repoID).
			Scan(&c.RepoID, &c.Version, &c.Source, &c.CommitSHA, &c.RawYAML, &c.Parsed, &c.CreatedAt)
	})
	return c, notFound(err)
}

// ---- Sentry routing (sentry_installations / sentry_project_repos) -----------------------------

// OrgForInstallation maps a Sentry integration installation uuid to its org (pre-tenant, ids only).
func (r *Store) OrgForInstallation(ctx context.Context, installationUUID string) (uuid.UUID, bool, error) {
	var org uuid.UUID
	err := r.DB.WithoutTenant(ctx, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT org_id FROM alpine_resolve_sentry_installation($1)`, installationUUID).Scan(&org)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	return org, err == nil, err
}

// RepoForProject maps a Sentry project (slug or numeric id) to a repo of the tenant.
func (r *Store) RepoForProject(ctx context.Context, org uuid.UUID, project string) (uuid.UUID, bool, error) {
	var repo uuid.UUID
	err := r.tenant(ctx, org, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT repo_id FROM sentry_project_repos WHERE sentry_project = $1`, project).Scan(&repo)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	return repo, err == nil, err
}

// ---- metrics ----------------------------------------------------------------------------------

// MetricsDay is one day of the metrics series.
type MetricsDay struct {
	Date          time.Time
	TicketsMerged int
	TurnsP50      float64
	TurnsP90      float64
	CostUSD       float64
	GatePassRate  float64
}

// GateRate is the pass rate of one gate.
type GateRate struct {
	GateID   string
	PassRate float64
	Runs     int
}

// FailureReason is a frequent failing (gate, rule).
type FailureReason struct {
	GateID string
	Rule   string
	Count  int
}

// Metrics is the factory dashboard for a window.
type Metrics struct {
	TicketsMerged    int
	TurnsP50         float64
	TurnsP90         float64
	CostTotalUSD     float64
	CostPerTicketUSD float64
	GatePassRate     float64
	EscalationRate   float64
	LeadTimeHoursP50 float64
	Series           []MetricsDay
	ByGate           []GateRate
	TopFailures      []FailureReason
}

// Metrics computes the dashboard over [since, now) for the tenant (optionally one repo). "Merged"
// tickets are tickets whose PR merged in the window; turns/cost/lead time are over those tickets.
func (r *Store) Metrics(ctx context.Context, org uuid.UUID, since time.Time, repoID *uuid.UUID) (Metrics, error) {
	var m Metrics
	err := r.tenant(ctx, org, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			WITH merged AS (
				SELECT DISTINCT ON (t.id) t.id, t.turns_used, t.cost_usd::float8 AS cost, t.created_at, p.merged_at
				FROM pull_requests p JOIN tickets t ON t.id = p.ticket_id
				WHERE p.state = 'merged' AND p.merged_at >= $1 AND ($2::uuid IS NULL OR p.repo_id = $2)
				ORDER BY t.id, p.merged_at)
			SELECT count(*)::int,
				COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY turns_used), 0)::float8,
				COALESCE(percentile_cont(0.9) WITHIN GROUP (ORDER BY turns_used), 0)::float8,
				COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY extract(epoch FROM merged_at - created_at) / 3600), 0)::float8,
				COALESCE(avg(cost), 0)::float8
			FROM merged`, since, repoID).Scan(&m.TicketsMerged, &m.TurnsP50, &m.TurnsP90, &m.LeadTimeHoursP50, &m.CostPerTicketUSD); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(cost_usd), 0)::float8 FROM tickets
			WHERE created_at >= $1 AND ($2::uuid IS NULL OR repo_id = $2)`, since, repoID).Scan(&m.CostTotalUSD); err != nil {
			return err
		}
		var created, escalated int
		if err := tx.QueryRow(ctx, `SELECT count(*)::int,
				count(*) FILTER (WHERE EXISTS (SELECT 1 FROM ticket_stage_history h WHERE h.ticket_id = t.id AND h.to_stage = 'escalated'))::int
			FROM tickets t WHERE t.created_at >= $1 AND ($2::uuid IS NULL OR t.repo_id = $2)`, since, repoID).Scan(&created, &escalated); err != nil {
			return err
		}
		if created > 0 {
			m.EscalationRate = float64(escalated) / float64(created)
		}
		var passed, total int
		if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE g.status IN ('pass','warn','skipped'))::int, count(*)::int
			FROM gate_results g JOIN tickets t ON t.id = g.ticket_id
			WHERE g.created_at >= $1 AND ($2::uuid IS NULL OR t.repo_id = $2)`, since, repoID).Scan(&passed, &total); err != nil {
			return err
		}
		if total > 0 {
			m.GatePassRate = float64(passed) / float64(total)
		}

		rows, err := tx.Query(ctx, `
			WITH days AS (SELECT generate_series(date_trunc('day', $1::timestamptz), date_trunc('day', now()), interval '1 day') AS d),
			merged AS (
				SELECT date_trunc('day', p.merged_at) AS d, t.turns_used, t.cost_usd::float8 AS cost
				FROM pull_requests p JOIN tickets t ON t.id = p.ticket_id
				WHERE p.state = 'merged' AND p.merged_at >= $1 AND ($2::uuid IS NULL OR p.repo_id = $2)),
			gates AS (
				SELECT date_trunc('day', g.created_at) AS d,
					count(*) FILTER (WHERE g.status IN ('pass','warn','skipped'))::float8 / NULLIF(count(*), 0) AS rate
				FROM gate_results g JOIN tickets t ON t.id = g.ticket_id
				WHERE g.created_at >= $1 AND ($2::uuid IS NULL OR t.repo_id = $2) GROUP BY 1)
			SELECT days.d,
				(SELECT count(*) FROM merged WHERE merged.d = days.d)::int,
				COALESCE((SELECT percentile_cont(0.5) WITHIN GROUP (ORDER BY turns_used) FROM merged WHERE merged.d = days.d), 0)::float8,
				COALESCE((SELECT percentile_cont(0.9) WITHIN GROUP (ORDER BY turns_used) FROM merged WHERE merged.d = days.d), 0)::float8,
				COALESCE((SELECT sum(cost) FROM merged WHERE merged.d = days.d), 0)::float8,
				COALESCE((SELECT rate FROM gates WHERE gates.d = days.d), 0)::float8
			FROM days ORDER BY days.d`, since, repoID)
		if err != nil {
			return err
		}
		if m.Series, err = pgx.CollectRows(rows, pgx.RowToStructByPos[MetricsDay]); err != nil {
			return err
		}

		rows, err = tx.Query(ctx, `SELECT g.gate_id,
				(count(*) FILTER (WHERE g.status IN ('pass','warn','skipped'))::float8 / count(*))::float8, count(*)::int
			FROM gate_results g JOIN tickets t ON t.id = g.ticket_id
			WHERE g.created_at >= $1 AND ($2::uuid IS NULL OR t.repo_id = $2) GROUP BY g.gate_id ORDER BY g.gate_id`, since, repoID)
		if err != nil {
			return err
		}
		if m.ByGate, err = pgx.CollectRows(rows, pgx.RowToStructByPos[GateRate]); err != nil {
			return err
		}

		rows, err = tx.Query(ctx, `SELECT g.gate_id, COALESCE(NULLIF(f->>'rule', ''), '(none)'), count(*)::int
			FROM gate_results g JOIN tickets t ON t.id = g.ticket_id, jsonb_array_elements(g.findings) f
			WHERE g.status IN ('fail','error') AND g.created_at >= $1 AND ($2::uuid IS NULL OR t.repo_id = $2)
			GROUP BY 1, 2 ORDER BY 3 DESC, 1, 2 LIMIT 10`, since, repoID)
		if err != nil {
			return err
		}
		m.TopFailures, err = pgx.CollectRows(rows, pgx.RowToStructByPos[FailureReason])
		return err
	})
	if m.Series == nil {
		m.Series = []MetricsDay{}
	}
	sort.SliceStable(m.TopFailures, func(i, j int) bool { return m.TopFailures[i].Count > m.TopFailures[j].Count })
	return m, err
}
