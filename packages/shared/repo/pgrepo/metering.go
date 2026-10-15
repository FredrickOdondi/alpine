package pgrepo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/alpine-dev/alpine/packages/shared/budget"
	"github.com/alpine-dev/alpine/packages/shared/db"
	"github.com/alpine-dev/alpine/packages/shared/db/dbq"
	"github.com/alpine-dev/alpine/packages/shared/keys"
	"github.com/alpine-dev/alpine/packages/shared/llm"
	"github.com/alpine-dev/alpine/packages/shared/ratelimit"
	"github.com/alpine-dev/alpine/packages/shared/repo"
)

// Metering implements the model-gateway persistence contracts on Postgres:
// keys.Store, ratelimit.LimitsProvider, budget.Source and llm.UsageRecorder.
type Metering struct {
	DB *db.DB
	// Defaults are the platform limits merged under org_limits/repo overrides.
	Defaults ratelimit.Limits
}

var (
	_ keys.Store               = (*Metering)(nil)
	_ ratelimit.LimitsProvider = (*Metering)(nil)
	_ budget.Source            = (*Metering)(nil)
	_ llm.UsageRecorder        = (*Metering)(nil)
)

func parseOrg(orgID string) (uuid.UUID, error) {
	id, err := uuid.Parse(orgID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("pgrepo: invalid org id: %w", err)
	}
	return id, nil
}

func optUUID(s string) *uuid.UUID {
	if s == "" {
		return nil
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return nil
	}
	return &id
}

// ActiveModelKey implements keys.Store.
func (m *Metering) ActiveModelKey(ctx context.Context, orgID, provider string) (*keys.Envelope, error) {
	org, err := parseOrg(orgID)
	if err != nil {
		return nil, err
	}
	var env *keys.Envelope
	err = m.DB.Tenant(ctx, db.Scope{OrgID: org}, func(q *dbq.Queries) error {
		r, err := q.ActiveModelKey(ctx, dbq.ActiveModelKeyParams{OrgID: org, Provider: provider})
		if err != nil {
			return err
		}
		env = &keys.Envelope{Ciphertext: r.Ciphertext, DataKeyCiphertext: r.DataKeyCiphertext, KEKID: r.KekID, Last4: r.Last4}
		return nil
	})
	if errors.Is(mapErr(err), repo.ErrNotFound) {
		return nil, nil
	}
	return env, err
}

// Limits implements ratelimit.LimitsProvider: org_limits and repo overrides over Defaults.
func (m *Metering) Limits(ctx context.Context, orgID, repoID string) (ratelimit.Limits, error) {
	l := m.Defaults
	org, err := parseOrg(orgID)
	if err != nil {
		return l, err
	}
	err = m.DB.Tenant(ctx, db.Scope{OrgID: org}, func(q *dbq.Queries) error {
		ol, err := q.GetOrgLimits(ctx, org)
		switch {
		case err == nil:
			if ol.MaxConcurrentLlmCalls != nil {
				l.OrgConcurrency = int(*ol.MaxConcurrentLlmCalls)
			}
			if ol.LlmRequestsPerMinute != nil {
				l.OrgRequestsPerMinute = int(*ol.LlmRequestsPerMinute)
			}
			if ol.LlmTokensPerMinute != nil {
				l.OrgTokensPerMinute = int(*ol.LlmTokensPerMinute)
			}
		case !errors.Is(mapErr(err), repo.ErrNotFound):
			return err
		}
		if rid := optUUID(repoID); rid != nil {
			rl, err := q.GetRepoLimits(ctx, dbq.GetRepoLimitsParams{ID: *rid, OrgID: org})
			switch {
			case err == nil:
				if rl.LlmRequestsPerMinute != nil {
					l.RepoRequestsPerMin = int(*rl.LlmRequestsPerMinute)
				}
			case !errors.Is(mapErr(err), repo.ErrNotFound):
				return err
			}
		}
		return nil
	})
	return l, err
}

// Ticket implements budget.Source.
func (m *Metering) Ticket(ctx context.Context, orgID, ticketID string) (budget.TicketUsage, budget.TicketCaps, error) {
	org, err := parseOrg(orgID)
	if err != nil {
		return budget.TicketUsage{}, budget.TicketCaps{}, err
	}
	tid := optUUID(ticketID)
	if tid == nil {
		return budget.TicketUsage{}, budget.TicketCaps{}, fmt.Errorf("pgrepo: invalid ticket id")
	}
	var (
		u budget.TicketUsage
		c budget.TicketCaps
	)
	err = m.DB.Tenant(ctx, db.Scope{OrgID: org}, func(q *dbq.Queries) error {
		r, err := q.GetTicketUsage(ctx, dbq.GetTicketUsageParams{ID: *tid, OrgID: org})
		if err != nil {
			return err
		}
		u = budget.TicketUsage{Turns: int(r.TurnsUsed), GateIterations: int(r.GateIterations), CostUSD: r.CostUsd, Tokens: r.TokensUsed}
		c = budget.TicketCaps{MaxTurns: int(r.MaxTurns), MaxGateIterations: int(r.MaxGateIterations), MaxCostUSD: r.MaxCostUsd}
		return nil
	})
	return u, c, mapErr(err)
}

// OrgCaps implements budget.Source.
func (m *Metering) OrgCaps(ctx context.Context, orgID string) (budget.OrgCaps, error) {
	org, err := parseOrg(orgID)
	if err != nil {
		return budget.OrgCaps{}, err
	}
	var c budget.OrgCaps
	err = m.DB.Tenant(ctx, db.Scope{OrgID: org}, func(q *dbq.Queries) error {
		ol, err := q.GetOrgLimits(ctx, org)
		if err != nil {
			return err
		}
		if ol.DailyCostUsdCap.Valid {
			f, _ := ol.DailyCostUsdCap.Float64Value()
			c.DailyCostUSD = f.Float64
		}
		if ol.MonthlyCostUsdCap.Valid {
			f, _ := ol.MonthlyCostUsdCap.Float64Value()
			c.MonthlyCostUSD = f.Float64
		}
		if ol.DailyTokenCap != nil {
			c.DailyTokens = *ol.DailyTokenCap
		}
		if ol.MonthlyTokenCap != nil {
			c.MonthlyTokens = *ol.MonthlyTokenCap
		}
		return nil
	})
	if errors.Is(mapErr(err), repo.ErrNotFound) {
		return budget.OrgCaps{}, nil
	}
	return c, err
}

// OrgSpend implements budget.Source.
func (m *Metering) OrgSpend(ctx context.Context, orgID string, since time.Time) (budget.Spend, error) {
	org, err := parseOrg(orgID)
	if err != nil {
		return budget.Spend{}, err
	}
	var s budget.Spend
	err = m.DB.Tenant(ctx, db.Scope{OrgID: org}, func(q *dbq.Queries) error {
		r, err := q.GetOrgSpendSince(ctx, dbq.GetOrgSpendSinceParams{OrgID: org, CreatedAt: since})
		s = budget.Spend{CostUSD: r.CostUsd, Tokens: r.Tokens}
		return err
	})
	return s, err
}

// Record implements llm.UsageRecorder: one llm_calls row plus token usage_records, idempotent on
// (org, idempotency key).
func (m *Metering) Record(ctx context.Context, rec llm.CallRecord) error {
	org, err := parseOrg(rec.Tenant.OrgID)
	if err != nil {
		return err
	}
	return mapErr(m.DB.Tenant(ctx, db.Scope{OrgID: org}, func(q *dbq.Queries) error {
		repoID, ticketID := optUUID(rec.Tenant.RepoID), optUUID(rec.Tenant.TicketID)
		callID, err := q.InsertLLMCall(ctx, dbq.InsertLLMCallParams{
			OrgID: org, RepoID: repoID, TicketID: ticketID, RunID: optUUID(rec.Tenant.RunID),
			IdempotencyKey: rec.IdempotencyKey, Stage: string(rec.Stage), Provider: rec.Provider, Model: rec.Model,
			RequestedModel: rec.RequestedModel, Status: rec.Status, Attempts: int32(max(rec.Attempts, 1)),
			TokensIn: rec.Usage.InputTokens, TokensOut: rec.Usage.OutputTokens, TokensCached: rec.Usage.CachedTokens,
			CostUsd: rec.CostUSD, LatencyMs: rec.Latency.Milliseconds(), Byok: rec.BYOK, Error: strPtr(rec.Error),
		})
		if errors.Is(mapErr(err), repo.ErrNotFound) {
			return nil // ON CONFLICT DO NOTHING: already recorded
		}
		if err != nil {
			return err
		}
		model := strPtr(rec.Model)
		// The gateway reports one cost per call; it is attributed to the first emitted meter so
		// sum(usage_records.cost_usd) == sum(llm_calls.cost_usd).
		costLeft := rec.CostUSD
		for _, u := range []struct {
			kind string
			n    int64
		}{
			{"llm_tokens_in", rec.Usage.InputTokens},
			{"llm_tokens_out", rec.Usage.OutputTokens},
			{"llm_tokens_cached", rec.Usage.CachedTokens},
		} {
			if u.n == 0 {
				continue
			}
			cost := costLeft
			costLeft = 0
			if err := q.InsertUsageRecord(ctx, dbq.InsertUsageRecordParams{
				OrgID: org, RepoID: repoID, TicketID: ticketID, LlmCallID: &callID, Kind: u.kind,
				Quantity: float64(u.n), Unit: "tokens", CostUsd: cost, Byok: rec.BYOK, Model: model,
			}); err != nil {
				return err
			}
		}
		return nil
	}))
}
