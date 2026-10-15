# Alpine: remaining work to 100%

State as of 2026-10-15, repo `FredrickOdondi/alpine` main `6b24e93`.

## What is real today
- Go API (chi, OpenAPI-first), Postgres 17 with RLS, sqlc, goose migrations 00001–00009, Redis sessions.
- Google OIDC and GitHub OAuth sign-in, orgs, invites, RBAC, API keys, audit log.
- GitHub and Sentry webhook intake (signature check, dedupe), one Temporal workflow per ticket.
- Live agents (`ALPINE_AGENT_MODE=live`, OpenAI direct): triage, refine (reads a repo snapshot), prioritize, implementer tool loop in a local Docker sandbox, independent reviewer on a different model. Per-ticket caps on turns, gate runs, tokens and $.
- Real gates (`GATES=real`, packages/gates SharedRunner) with ratchet against base and an env allowlist.
- `SCM=gh` adapter (gh CLI) opens real PRs. Proven: alpine-testbed PR #2, cost $0.03.
- Web app (Vite + React): board, ticket detail, PR inbox with blast-radius confirms, metrics, repo config, command palette. Runs on MSW mocks or the live API (`make web-live`).
- Onboarding backend (migration 00009): GitHub App manifest endpoints, install callback with state, repo enable/disable, installation webhooks. Mostly untested.

## P0: needed before a real customer can use it
1. **Onboarding UI** (apps/web, in Tess's design system):
   - Create workspace after first sign-in.
   - Operator page `/setup/github-app` that POSTs the manifest to GitHub.
   - Connect GitHub, which redirects to the install URL.
   - Repo picker with enable toggles and a starter `.alpine/factory.yaml` PR.
   - Settings > Integrations to disconnect and manage repos.
2. **Test the onboarding backend:** integration tests against `githubfake` / `cmd/fake-github` for the manifest callback, install callback, linking, the repo list, and the installation and installation_repositories webhooks.
3. **Public webhook URL for local dev:** a smee.io forwarder via `make webhook-proxy` using `WEBHOOK_PROXY_URL`, plus docs.
4. **Worker uses the stored GitHub App registration** (DB or `.alpine-local`) instead of `GITHUB_APP_*` env. Installation-token auth for clone, push and PR in the live implementer (`SCM=github`).
5. **Create the real GitHub App** through the manifest flow and verify it end to end on one of Fredrick's own repos: an issue becomes a ticket, then a PR.
6. **Re-run and retry a ticket:** a UI action and endpoint that reset state, use a new workflow ID suffix (the current ID policy rejects duplicates), and clear the LLM cache keys.
7. **Encrypt secrets at rest everywhere:** require `ALPINE_LOCAL_KEK` / KMS for GitHub App keys and customer OpenAI keys, with no plaintext fallback outside dev.

## P1: needed for a reliable product
8. **Gates inside the sandbox:** run gates in the Docker or E2B sandbox, not on the worker host.
9. **E2B runner:** implement and test the E2B sandbox backend, choose between it and local Docker per org, and track sandbox-minute metering.
10. **Language images:** sandbox images for Node/TS, Python and Go, picked from the repo or factory.yaml (today only `golang:1.26-alpine`).
11. **Gate-failure feedback loop:** built but never exercised live. Test it with a bug whose first fix fails the gates.
12. **LiteLLM proxy path:** run through the proxy in compose, with per-org virtual keys, budgets and fallbacks. Test bring-your-own OpenAI key per org.
13. **Playwright repro in refinement:** a real browser repro and video for UI bugs (today evidence is mock data).
14. **Artifacts storage:** S3 or MinIO (or R2) uploads for logs, videos, screenshots and visual diffs, with signed URLs in the API. MinIO is currently optional and unused.
15. **PR polish:** labels (blast radius, alpine), check-run status from the gates, artifact links, and mirroring review findings as PR review comments.
16. **Approve and merge from the inbox:** "Approve merge" actually merges through the GitHub App, with blast-radius policy, and send-back reopens the ticket with feedback.
17. **After merge:** a staging deploy hook, smoke tests, a QA agent and promotion to production, plus a configurable deploy webhook per repo.
18. **Sentry onboarding UI:** an install flow and mapping Sentry projects to repos (the tables exist in 00008).
19. **Rate limits and fairness per org** in the scheduler, a circuit breaker on OpenAI, and dead-letter handling for failed workflows.
20. **Billing:** a platform fee per org and repo plus metered sandbox minutes and model usage at cost-plus. Stripe integration, a usage page and hard budget stops.

## P2: launch and operations
21. **Deploy:** the frontend on Vercel; the API, worker, Temporal, Postgres and Redis on a VPS with Docker Compose (Hostinger, run by Harbor) or managed services (Neon, Temporal Cloud). HTTPS on a domain, and a prod cookie `__Host-alpine_session` with `SESSION_COOKIE_SECURE=true`.
22. **CI:** move `ci/github-ci.yml` to `.github/workflows/` (needs a token with workflow scope). Add web unit tests (Vitest) and e2e tests (Playwright).
23. **Observability:** OpenTelemetry traces to a backend, an LLM cost dashboard, and alerts.
24. **Eval suite:** a set of seeded-bug repos run on every agent or prompt change, tracking fix rate and cost.
25. **Docs:** `docs/database.md`, `docs/auth.md`, a customer quickstart, and the factory.yaml reference.
26. **Frontend P1/P2 leftovers** from `/workspace/alpine-review/critique.md`: KPI previous-period comparison, the design README colour note, and 20 P2 polish items.
27. **Security review:** check the sandbox's network egress, token scopes, RLS coverage tests, and prompt injection from issue text.
28. **Legal and marketing:** landing page, terms, privacy policy, and a GitHub App public listing.

## How to run (dev)
See README "Run it locally on a Mac" and the live-mode section. Key env:
- `ALPINE_AGENT_MODE=live`
- `OPENAI_API_KEY`
- `SCM=gh|github|stub`
- `GATES=real|stub`
- `GOOGLE_CLIENT_ID` and `GOOGLE_CLIENT_SECRET`
- `SESSION_SECRET` and `API_KEY_PEPPER`
