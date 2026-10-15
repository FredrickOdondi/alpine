# Alpine — MVP & Phased Plan

Version 0.1 · Oct 2026. Timelines assume a founding team of ~4 engineers (2 backend/Go,
1 frontend, 1 infra/agents) plus a design partner or two. Weeks are rough; treat them as sequencing, not commitments.

## Guiding rule
Ship the **closed loop on one repo** first: issue in → verified PR out, with gates and a turn cap.
Everything that widens the funnel (more sources, more SCMs) or removes the human (auto-merge)
comes after the loop has proven quality on real design-partner repos.

## Phase 0 — Foundations (weeks 0–3)
- Monorepo, Go module layout, OpenAPI spec skeleton, codegen (Go server, TS client), CI.
- Terraform: dev/staging AWS accounts, VPC, EKS, Aurora Postgres (+pgvector), Valkey, S3, KMS, ECR, CloudFront for SPA.
- Temporal Cloud namespace; orchestrator worker skeleton with a no-op `TicketWorkflow`.
- Auth (GitHub OAuth), orgs/memberships, RLS middleware + tests proving cross-tenant isolation.
- Sandbox provider bake-off (E2B / Daytona / Modal): cold start, snapshots, Docker-in-VM, egress control, price → pick one behind `Runner`.
- Model gateway v0: 2 providers, BYOK storage (KMS envelope), usage metering, per-ticket cost cap.
- **Exit:** a workflow can create a sandbox, clone a repo through the credential proxy, run a command, store logs in S3, and show it in the SPA.

## Phase 1 — MVP (weeks 3–12)

**Scope (exactly):**
1. **GitHub App install on one repo** (least-privilege permissions; installation + repo selection UI).
2. **Issue intake:** GitHub Issues (label `alpine` or all, configurable) + **Sentry** (issue webhooks for the linked project).
3. **Triage agent:** classify, dedupe (pgvector + Sentry fingerprint), already-fixed check, comment back on source.
4. **Refinement agent:** reproduce inside the sandbox using the repo's own dev/test stack with
   **synthetic seed data** (from `.alpine/seed`), Playwright video/screenshots for UI bugs or a failing
   test for API bugs; BR/priority/size estimate; ticket with acceptance criteria.
5. **Prioritizer:** rules-based ready-for-agent vs needs-human, with UI approve/reject.
6. **Implementer agent in sandbox:** reads `AGENTS.md`/`CLAUDE.md` + `.alpine/rules`, configured
   skills, read-only MCP servers; works on `alpine/*` branch; optional parallel subagents (behind a flag).
7. **Quality gates:** lint, typecheck, unit/integration tests, **duplication** (jscpd / Fallow dupes),
   plus Fallow dead-code/complexity as warn-only ratchets; structured feedback to agent; **turn cap
   (default 100) + cost cap**, escalation with diagnosis; turns tracked as a metric.
8. **Independent reviewer:** fresh context, diff + AC + rules + module map, different model family.
9. **PR with visual diff:** before/after screenshots + video for affected routes (Playwright run on base and head inside gate sandbox), gate table, reviewer findings, BR badge, turns/cost.
10. **Human merge** only (no auto-merge). Ticket closes on merge.
11. **Dashboard:** pipeline board, PR inbox sorted by BR, ticket detail, basic metrics (turns, cost, gate failures, lead time), repo factory config viewer (config is edited in-repo).
12. Audit log for all agent and human actions.

**Explicitly out of MVP:** preview envs, QA agent, auto-merge, staging deploys, Slack/Linear/Jira/widget,
GitLab, model router, insights agent, SSO, invariants, billing automation (manual invoicing / Stripe later).

**Milestones**
| Week | Milestone |
| --- | --- |
| 4 | GitHub App installed on Alpine's own repo; GitHub issue → triaged ticket on board |
| 6 | Sentry intake + dedupe; refinement reproduces a seeded UI bug with Playwright video |
| 8 | Implementer + gates loop green on 5 seeded bugs in a sample repo; turn cap + escalation working |
| 10 | Reviewer + PR with visual diff; PR inbox; first design partner installed (read-only intake) |
| 12 | **MVP exit:** 2 design partners, ≥ 30 agent PRs merged by humans |

**MVP exit criteria**
- ≥ 50% of "ready for agent" tickets produce a PR that a human merges with ≤ minor edits.
- Median turns per merged PR tracked; p90 cost per merged PR < $10 (target, to validate).
- Dedupe precision ≥ 95% on partner data (sampled manually).
- Zero cross-tenant data access in tests and pen-test-lite; no secrets in sandbox images/logs (scanner verified).

## Phase 2 — Preview envs + QA agent + more sources (weeks 12–20)
- **Preview environments:** preview EKS cluster, namespace per PR from devcontainer/compose/Helm, seed
  recipe; customer-side **anonymization toolkit** (CLI/container) producing pseudonymized seed bundles; TTL reaper.
- Refinement reproduces against preview envs (closer to prod topology, multi-service).
- **QA agent:** acts as a user with only feature + AC context, runs in preview env, records evidence.
- **Intake:** Slack (shortcut + channel watch), **Linear**, **Jira**, in-app **bug widget**.
- Gate packs for Go and Python; affected-e2e selection.
- Stripe billing: platform fee + metered factory minutes + model usage; budgets UI.

## Phase 3 — Auto-merge by blast radius (weeks 20–28)
- Merge policy engine live: BR1 (then BR2 opt-in) **auto-merge → staging → smoke e2e subset → QA
  agent → close ticket → promote to prod** via customer's deploy hook (GitHub Deployments / webhook / Argo CD).
- Repo trust score (rolling merge success, revert rate) gating auto-merge; auto-revert on staging/QA failure.
- SSO/SCIM (WorkOS or similar), granular RBAC, audit export.
- Own sandbox pool option (EKS + Kata/Firecracker on metal nodes, or gVisor) for cost/residency; keep managed provider as fallback.
- SOC 2 Type I.

## Phase 4 — Scale & intelligence (weeks 28–40+)
- **GitLab** support (app/OAuth, MR flow, pipelines).
- **Model router:** cheap vs strong by role/size/BR; escalate on repeated gate failures; per-tenant routing policies.
- **Docs/changelog agent**; comment-noise cleanup gate.
- **Insights/postmortem agent:** cluster closed tickets by touched modules/infra; hotspot reports; architecture recommendations.
- **Invariant rules (beta):** declarative high-level rules (e.g. "no two payments by one user within 5s") compiled to property-based tests / runtime checks, run as a gate on every change.
- Cells / dedicated tenancy and **BYO-cloud runners**; data residency (EU).
- SOC 2 Type II.

## Risks & mitigations
| Risk | Mitigation |
| --- | --- |
| Reproducible environments are hard per customer | Start with repos whose test stack runs in one VM (monolith + compose); onboarding wizard + "agent readiness" check; services offering for first partners |
| Agents loop / cost blowups | Hard turn + cost caps from day one; prebuilt snapshots; model router in P4 |
| Gate gaming (agent edits tests/config) | Gate config from base branch; protected paths; reviewer checks for hardcoded tests; snapshot-only test changes flagged |
| Prompt injection via issues/Sentry | Untrusted-input policy, no write tools in triage/refine, egress allowlist, human merge in MVP |
| Incumbents bundle (GitHub, Cursor, Linear) | Differentiate on cross-tool pipeline + deterministic gates + merge policy + factory metrics; integrate with their agents rather than fight (engine adapters) |
| Data handling concerns | No prod data policy, customer-side anonymization, BYOK, BYO-cloud runners (P4) |
