# Alpine — the agentic software factory

> Status: **Phase 0 foundations** (Oct 2026). Go API + Temporal orchestrator + model gateway +
> sandbox runner run locally end to end with stub agents. See "Run it locally" below.

Alpine is a multi-tenant SaaS that engineering teams plug their repositories into. It runs agents
across the software development life cycle — **intake → triage → refinement → prioritization →
implementation → deterministic quality gates → independent review → blast-radius-based merge** —
and keeps humans in the loop exactly where the risk is.

The core idea (from the "agentic software factory" talk, https://youtu.be/WOhJflEz3mY): engineers
stop hand-building each component and instead **build the factory** — rules files, design system,
API conventions, quality gates and merge policies — while agents run the production line. Alpine
productizes that factory so a team doesn't have to duct-tape it together themselves.

## Why Alpine is different

Most products in this space (Oct 2026) do one or two of these steps well: a cloud coding agent
that opens a PR, or an AI reviewer. Alpine owns the **whole pipeline** and treats the
**deterministic build & quality pipeline** as the product's center of gravity:

1. **End-to-end pipeline** from a Sentry error or a "report a bug" click to a merged, verified change.
2. **Deterministic gates first, LLM judgment second** — lint, typecheck, static analysis
   (duplication, complexity, dead code), e2e, visual before/after — fed back into the agent
   with a **hard turn cap** and escalation to a human.
3. **Blast-radius merge policy** — every change is classified (UI tweak < design system < API <
   DB/schema); small changes can auto-merge through staging + smoke e2e + a QA agent, large ones
   always go to a human.
4. **Factory metrics** — turns per ticket, cost per ticket, gate failure reasons, escalation rate —
   so teams improve the factory, not individual PRs.

See [`docs/competitors.md`](docs/competitors.md) for the landscape and wedge.

## Docs

| Doc | What's in it |
| --- | --- |
| [`docs/product.md`](docs/product.md) | Personas, pipeline stages, UX (dashboard, PR inbox, factory config), pricing, metrics |
| [`docs/architecture.md`](docs/architecture.md) | Stack, services, Temporal workflows, data model, sandboxes, security, multi-tenancy, mermaid diagrams |
| [`docs/mvp-plan.md`](docs/mvp-plan.md) | Phased plan, MVP scope, milestones, exit criteria |
| [`docs/competitors.md`](docs/competitors.md) | Devin, Factory, Cursor, Copilot, OpenHands, Sweep, CodeRabbit, Graphite, Linear — coverage, gaps, wedge |

## Stack at a glance

- **API / services:** Go, REST + OpenAPI (generated TS client), SSE for live updates
- **Workflows:** Temporal (Go SDK) — one durable workflow per ticket
- **Frontend:** Vite + React + TypeScript SPA (TanStack Router/Query, Tailwind + shadcn/ui). No Next.js.
- **Data:** Postgres (RLS for tenancy, pgvector for dedupe), Redis/Valkey, S3 for artifacts (videos, screenshots, logs)
- **Sandboxes:** `Runner` interface; managed microVM provider (E2B/Daytona class) for MVP, own EKS + Kata/Firecracker or gVisor later
- **Integrations:** GitHub App (GitLab later), Sentry, Slack, Linear, Jira, in-app bug widget
- **Models:** OpenAI via a LiteLLM proxy behind a thin, provider-agnostic Go gateway client; BYO OpenAI keys, per-tenant limits and budgets
- **Platform:** AWS (EKS, RDS/Aurora Postgres, ElastiCache, S3, KMS), Terraform, Argo CD, OpenTelemetry

## Monorepo layout

```
apps/web                 Vite + React + TS dashboard SPA (+ embeddable bug-report widget later)
services/api             Go public API: auth, tenants, repos, tickets, config, SSE, webhooks ingress
services/orchestrator    Go Temporal workers: ticket workflow, agent activities, gate runs, merge policy
services/runner          Go sandbox broker: Runner interface over microVM providers / own k8s
services/integrations    Go webhook normalizers + outbound clients (GitHub, Sentry, Slack, Linear, Jira)
packages/agents          Agent definitions: prompts, tool schemas, policies (triage, refine, implement, review, QA)
packages/gates           Gate runners + result parsers (eslint, tsc, fallow/knip/jscpd, playwright, visual diff)
packages/shared          Shared Go libs: tenancy, authz, model gateway client, OTel, config schema
infra/terraform          AWS accounts, VPC, EKS, RDS, ElastiCache, S3, KMS, IAM
infra/k8s                Helm charts / Kustomize overlays, Argo CD apps
.github/workflows        CI for Alpine itself (dogfooded through Alpine gates once stable)
docs                     Specs
```

## Run it locally on a Mac

**Prereqs:** Docker Desktop, Go 1.26, Node 22 + pnpm (`corepack enable`), Temporal CLI optional
(the compose stack runs Temporal; UI at http://localhost:8233).

```bash
cp .env.example .env            # no real secrets; defaults match docker-compose.yml
make infra-up                   # Postgres (pgvector), Valkey, Temporal
make migrate seed               # goose migrations + dev org/user/repo (dev@alpine.local)
make run-api                    # terminal 1: API + inbox on :8080
make run-worker                 # terminal 2: Temporal worker (stub agents/SCM/gates)
make webhook                    # signed fake GitHub issue -> ticket -> workflow
make sentry-webhook             # signed fake Sentry issue (seeded installation/project)
make web-install                # pnpm install
make web                        # SPA on http://localhost:5173 against MSW mocks
make web-live                   # SPA against the live API (Vite proxies /v1 -> :8080)
```

**Signing in (live web):**
- Quickest: `AUTH_DEV_STUB=true` in .env, then every request without a session acts as the seeded
  `dev@alpine.local` owner (curl writes need `X-CSRF-Token: dev-stub-csrf`). Or keep
  `AUTH_DEV_LOGIN=true` and use "Continue with dev login".
- Google: in Google Cloud Console create an OAuth client (Web application) with authorized
  JavaScript origin `http://localhost:5173` and redirect URI
  `http://localhost:8080/v1/auth/google/callback`; set `GOOGLE_CLIENT_ID` / `GOOGLE_CLIENT_SECRET`.
  Generate `SESSION_SECRET` and `API_KEY_PEPPER` with `openssl rand -base64 48`.

**Env switches (all default to stubs, so nothing calls OpenAI or GitHub):**
`ALPINE_AGENT_MODE=stub|llm|live`, `SCM=stub|github|gh` (needs `GITHUB_APP_ID`, `GITHUB_APP_BOT_LOGIN`,
`GITHUB_APP_PRIVATE_KEY_FILE`), `GATES=stub|real` (needs a ticket checkout; gates-only values via
`GATES_ENV_<NAME>`), `RUNNER=docker|e2b`, `AUTH_DEV_STUB`, `VITE_API_MODE=mock|live`.

**Live mode: let the factory fix a real bug and open a real PR.** Agents call OpenAI (structured
outputs), the implementer edits a clone of the repo and runs commands in an offline Docker sandbox,
the repo's `.alpine/factory.yaml` gates run on that checkout (failures go back to the implementer),
an independent reviewer on a different model reads the real diff, and `SCM=gh` pushes
`alpine/<ulid>-<slug>` and opens a PR with your `gh` login, but only on `SCM_GH_ALLOWED_REPOS`. Nothing is merged.

| Variable | Live value | Notes |
|---|---|---|
| `ALPINE_AGENT_MODE` | `live` | `llm` keeps the stub implementer |
| `LLM_PROVIDER` | `openai` | `litellm` (default) goes through `LLM_BASE_URL` (`docker compose --profile llm up -d`) |
| `OPENAI_API_KEY` | export in your shell | never in a committed file; keep it commented in `.env` |
| `LLM_MODELS_<STAGE>` | triage/refine/prioritize/review `gpt-4.1-mini`, implement `gpt-4.1` | the reviewer must differ from the implementer |
| `LLM_MAX_OUTPUT_TOKENS_<STAGE>` | 800 / 1500 / 600 / 4000 / 1500 | per-call output cap |
| `TICKET_MAX_TURNS`, `TICKET_MAX_GATE_ITERATIONS`, `TICKET_MAX_COST_USD`, `TICKET_MAX_TOKENS` | 25, 3, 1.50, 800000 | per-ticket caps (the repo's factory.yaml limits apply too) |
| `BUDGET_ORG_DAILY_USD`, `BUDGET_ORG_MONTHLY_USD` | 3, 10 | org budget |
| `SCM`, `SCM_GH_ALLOWED_REPOS` | `gh`, `you/alpine-testbed` | any other repo is refused before gh runs |
| `GATES` | `real` | |
| `IMPLEMENTER_IMAGE`, `WORKSPACE_ROOT`, `IMPLEMENTER_COMMAND_TIMEOUT`, `RUNNER_DOCKER_BIN` | `golang:1.26-alpine`, `/tmp/alpine-workspaces`, `3m`, `docker` | sandbox: `--network none`, 2 CPU, 2 GB, the checkout mounted at `/work` |

Token usage and cost are logged per turn (`implementer turn`) and per call (`agent spend`), stored in
`llm_calls` and on the ticket, and summarized in the PR body.

```bash
export OPENAI_API_KEY=sk-...    # your shell only
gh auth login                   # account that owns the test repo
docker pull golang:1.26-alpine
cp .env.live.example .env.live  # set SCM_GH_ALLOWED_REPOS=<you>/alpine-testbed
make infra-up migrate
SEED_GITHUB_REPO=<you>/alpine-testbed \
SEED_GITHUB_REPO_ID=$(gh api repos/<you>/alpine-testbed --jq .id) \
SEED_FACTORY_YAML=/abs/path/to/alpine-testbed/.alpine/factory.yaml make seed
make run-api                    # terminal 1
make run-worker                 # terminal 2: logs "agents: live" with the chosen models
scripts/send-issue-webhook.sh <you>/alpine-testbed 1   # replays the real issue as a signed webhook
curl -s localhost:8080/v1/tickets | jq '.items[] | {key,stage,costUsd}'   # wait for pr_open
```

The test repo needs a `.alpine/factory.yaml` with Go gates, which the `golang` image can run offline.
A ticket the policy isn't sure about (low confidence, BR4, size L+, unconfirmed bug) waits in
`needs_human`. Approve it with `curl -X POST -H 'X-CSRF-Token: dev-stub-csrf' -H 'Content-Type: application/json'
-d '{"action":"approve"}' localhost:8080/v1/tickets/<id>/actions`.

**Other targets:** `make test` (vet + tests + web lint/typecheck), `make test-race`, `make test-db`
(Postgres integration tests), `make gen` (apigen + TS client from api/openapi.yaml), `make build`.

**MinIO:** MinIO stopped publishing updated `minio/minio` images on Docker Hub, so the image can be
stale or fail to pull. Nothing in the local loop needs it (artifact links are unsigned stubs), so it is
only in the optional `storage` profile: `docker compose --profile storage up -d minio minio-init`, or
point `S3_ENDPOINT` at any S3-compatible store. `docker compose --profile llm up -d` adds LiteLLM for
`ALPINE_AGENT_MODE=llm` (needs `OPENAI_API_KEY`).
