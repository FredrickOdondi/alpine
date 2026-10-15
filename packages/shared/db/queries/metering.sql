-- Queries backing the model-gateway contracts (keys.Store, ratelimit.LimitsProvider,
-- budget.Source, llm.UsageRecorder). All run tenant-scoped.

-- name: ActiveModelKey :one
SELECT ciphertext, data_key_ciphertext, kek_id, last4 FROM model_keys
WHERE org_id = $1 AND provider = $2 AND revoked_at IS NULL;

-- name: GetOrgLimits :one
SELECT * FROM org_limits WHERE org_id = $1;

-- name: GetRepoLimits :one
SELECT max_parallel_tickets, llm_requests_per_minute FROM repos WHERE id = $1 AND org_id = $2;

-- name: GetTicketUsage :one
SELECT turns_used, gate_iterations, cost_usd::float8 AS cost_usd, tokens_used,
       max_turns, max_gate_iterations, max_cost_usd::float8 AS max_cost_usd
FROM tickets WHERE id = $1 AND org_id = $2;

-- name: GetOrgSpendSince :one
SELECT COALESCE(sum(cost_usd), 0)::float8 AS cost_usd,
       COALESCE(sum(tokens_in + tokens_out), 0)::bigint AS tokens
FROM llm_calls WHERE org_id = $1 AND created_at >= $2;

-- name: InsertLLMCall :one
INSERT INTO llm_calls (org_id, repo_id, ticket_id, run_id, idempotency_key, stage, provider, model, requested_model,
                       status, attempts, tokens_in, tokens_out, tokens_cached, cost_usd, latency_ms, byok, error)
VALUES (@org_id, sqlc.narg(repo_id), sqlc.narg(ticket_id), sqlc.narg(run_id), @idempotency_key, @stage, @provider,
        @model, @requested_model, @status, @attempts, @tokens_in, @tokens_out, @tokens_cached, @cost_usd::float8,
        @latency_ms, @byok, sqlc.narg(error))
ON CONFLICT (org_id, idempotency_key) DO NOTHING
RETURNING id;

-- name: InsertUsageRecord :exec
INSERT INTO usage_records (org_id, repo_id, ticket_id, llm_call_id, kind, quantity, unit, unit_cost_usd, cost_usd, byok, model)
VALUES (@org_id, sqlc.narg(repo_id), sqlc.narg(ticket_id), sqlc.narg(llm_call_id), @kind, @quantity::float8, @unit,
        @unit_cost_usd::float8, @cost_usd::float8, @byok, sqlc.narg(model));
