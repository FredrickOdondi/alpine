# Alpine — Product Spec

Version 0.1 · Oct 2026 · Owner: Fredrick Odondi

## 1. Problem

AI coding agents can now produce PRs cheaply, but teams adopting them hit the same walls:

- **Intake is still manual.** Someone has to read Slack threads, Sentry errors and user reports,
  dedupe them, reproduce the bug, and write a ticket good enough for an agent.
- **Quality doesn't scale with volume.** Agents duplicate code, leave dead endpoints and tables,
  hardcode tests, violate module boundaries, and store JSON blobs in relational tables. Teams
  producing 10–20 agent PRs per developer per day (figure from the source talk) cannot read them all.
- **Review is the new bottleneck.** Every PR is treated the same; a CSS tweak and a schema
  migration wait in the same queue.
- **Agents loop.** Without a hard cap, an agent can burn 100+ attempts (and the token budget) on a
  bug it lacks the context to fix — and nobody learns why.
- **Teams duct-tape it together.** Reproducible environments, anonymized data, visual diffs,
  merge rules: every team rebuilds the same factory.

## 2. Product vision

**Alpine is the factory, not the worker.** Teams connect repos and intake sources, write their
rules (design system, API conventions, caching, DB rules) once, configure gates and merge
policies, and Alpine runs a durable pipeline per ticket. Humans spend their time on
(a) high-blast-radius reviews, (b) escalations, and (c) improving the factory based on metrics.

Positioning line: *"From bug report to verified merge — with deterministic gates and a merge
policy that knows what's risky."*

## 3. Personas

| Persona | Job to be done | Primary surface |
| --- | --- | --- |
| **Factory owner** (staff/lead engineer) | Encode team standards; tune gates and merge policy; diagnose escalations; improve turns/cost | Factory config, metrics, escalation inbox |
| **Reviewer** (any engineer) | Review the PRs that actually need a human, fast, with visual evidence | PR inbox sorted by blast radius |
| **Product/support** | Report bugs, see status, trust that duplicates are merged | Bug widget, Slack, ticket board |
| **Eng manager / buyer** | Throughput, cost, quality, security posture | Metrics, billing, audit log |
| **Security/admin** | Least privilege, data handling, auditability | Org settings, audit log, data policies |

## 4. The pipeline (stages)

Each ticket is one durable workflow moving through stages. Every stage writes structured output
(JSON schema-validated) plus human-readable artifacts. Every stage transition is an audit event.

### 4.1 Intake & Triage agent
- **Sources:** GitHub Issues (MVP), Sentry issues/alerts (MVP), Slack messages/shortcut (P2),
  Linear and Jira issues (P2), in-app "report a bug" widget (P2; captures URL, console, network
  HAR summary, screenshot, user/session id — with PII scrubbing client-side).
- **Does:** normalize into an `IntakeEvent`; classify (bug / feature / question / noise);
  **dedupe** against open tickets (pgvector similarity + Sentry fingerprint + stack-frame match);
  check **already fixed** (search merged PRs/commits since the error's first-seen release; check
  whether Sentry marks it resolved in a newer release); check backlog for an existing ticket.
- **Outputs:** new ticket, link-as-duplicate (with comment back to source), or close-as-noise
  (with reason). Never writes code. Untrusted input: triage has no write tools beyond
  labeling/commenting.

### 4.2 Refinement agent
- Spins up an **ephemeral environment** (MVP: repo's test/dev stack inside the sandbox; P2: full
  preview env) seeded with **anonymized or synthetic data only** (see architecture §9).
- **Reproduces** with Playwright (browser bugs) or a failing test/HTTP script (API bugs); records
  **video + screenshots + trace**.
- **Estimates:** scope (files/modules likely touched), **blast radius** layer, priority (P0 live
  prod bug … P3), size (XS–XL), confidence.
- **Writes the ticket:** concise problem statement, repro steps, **acceptance criteria** (testable,
  ideally mapped to an e2e test to add), out-of-scope list, evidence attachments.
- Brevity rule: ticket body capped (e.g. 250 words + evidence) — agent tickets are often too verbose.

**Blast radius layers** (configurable per repo via path globs + heuristics):

| Level | Layer | Examples |
| --- | --- | --- |
| BR1 | Leaf UI tweak | copy, styles, a single page component |
| BR2 | Shared UI / design system | tokens, shared components, layout |
| BR3 | API / service logic | handlers, business rules, auth, caching, cross-service contracts |
| BR4 | Data / schema / infra | migrations, DB schema, queues, IaC, payments, security-sensitive paths |

Final blast radius = max(predicted by refinement, computed from actual diff, escalations from
reviewer). If the actual diff exceeds the predicted layer, the ticket is re-classified and
re-routed.

### 4.3 Prioritizer
Routes each refined ticket to **Ready for agent** or **Needs human**, with a reason. Default rules
(editable):
- Needs human if: architecture-changing, product decision needed ("is it worth doing?"),
  ambiguous acceptance criteria, BR4 without an explicit allow, repro failed, low confidence,
  touches protected paths (auth, billing, `.github/workflows`, migrations by default).
- Ready for agent if: reproduced, clear AC, BR ≤ policy threshold, size ≤ M.
- Ordering: priority × confidence ÷ size; WIP limit per repo (concurrency budget).

### 4.4 Implementer agent
- Runs in an **isolated sandbox** on a branch `alpine/<ticket-key>-<slug>`.
- Context: the team's **rules file** (`AGENTS.md` / `CLAUDE.md` — Alpine reads whichever exists,
  plus `.alpine/rules/*.md`), **vendor skills** (e.g. framework/cloud provider skill packs),
  configured **MCP servers** (read-only by default: analytics, issue tracker, docs), the refined
  ticket and repro artifacts.
- Can fan out **parallel subagents** (e.g. backend + frontend + test-writer) inside one ticket,
  coordinated by a lead agent; each subagent has its own budget.
- Must add/adjust an **e2e or integration test** that encodes the acceptance criteria when the repo
  has an e2e suite.
- Must leave the code without ticket-reference comments or debug noise (enforced by a gate).

### 4.5 Build & quality pipeline (the core)
Deterministic gates run in a **separate gate sandbox** (agents can't tamper with gate config;
gate config is read from the base branch, not the agent's branch).

| Gate | Default tools (JS/TS) | Notes |
| --- | --- | --- |
| Format/lint | ESLint / Biome, repo config | |
| Typecheck | `tsc --noEmit` | |
| Static analysis | **Fallow** (dead code, dupes, complexity, boundaries), knip, jscpd | thresholds as *diff ratchets*: new code must not worsen baseline |
| Unit/integration tests | repo command | |
| E2E | Playwright (full or affected subset) | |
| Visual diff | before (base) vs after (branch) screenshots + video of affected routes | posted on PR |
| Policy | protected paths, no new `// TODO TICKET-123` comments, no snapshot-only test changes, lockfile rules, migration rules | |
| Invariants (P4) | formal rules, e.g. "no two payments by one user within 5s" | property tests / model checks |

Go, Python, etc. get equivalent gate packs (golangci-lint, go vet, staticcheck, ruff, mypy…).

**Feedback loop:** structured gate failures (file, line, rule, message, suggested fix) are fed back
to the implementer. **Hard cap:** default **100 agent turns** per ticket (configurable 10–300) and
a cost cap ($); on hitting either → stop, escalate to human with a diagnosis ("looping on X; likely
missing context Y"). **Turns are a first-class metric.**

> Definition: a *turn* = one model response + execution of its tool calls in the implementer loop
> (subagent turns count toward the ticket total). A *gate iteration* = one full gate run. Both
> are tracked; the cap applies to turns, and a secondary cap (default 15) applies to gate iterations.

### 4.6 Independent AI reviewer
- Fresh context: sees only the **diff, the ticket AC, the rules file, and an architecture map**
  (module graph from static analysis) — **never** the implementer's transcript or reasoning.
- Preferably a **different model family** than the implementer (configurable).
- Checks: architecture regressions, **module boundary violations**, **hardcoded/tautological
  tests**, redundant state / re-implemented existing functions, **schema shortcuts** (JSON blobs
  in relational tables, missing FKs, denormalization without reason), over-commenting, security
  smells, scope creep vs AC.
- Output: `approve | request_changes | escalate` with findings; request_changes goes back to the
  implementer (counts toward turns); escalate raises blast radius / routes to human.

### 4.7 PR & merge policy
- PR body: ticket summary, AC checklist, gate results table, reviewer findings, **visual
  before/after**, repro video, turns/cost, blast radius badge.
- Policy by blast radius (per repo, defaults):

| BR | MVP | Later (P3+) |
| --- | --- | --- |
| BR1 | human merge | **auto-merge** → staging → smoke e2e subset → **QA agent** (acts as a user, knows only the feature + AC) → close ticket → promote to prod |
| BR2 | human merge | auto-merge allowed if opted in + green streak |
| BR3 | human merge | human review required |
| BR4 | human merge + 2 approvals suggested | human review required, always |

- Auto-merge eligibility also requires: all gates green, reviewer approve, no protected paths,
  repo "trust score" above threshold (rolling success rate of past merges, rollback rate).
- Failed staging/QA → auto-revert PR + ticket reopened with evidence.

### 4.8 Future agents (roadmap)
- **Model router:** cheap model for triage/simple tickets, strong model for hard ones; escalate model on repeated gate failure.
- **Docs & changelog agent:** release notes from merged tickets; removes noisy comments.
- **Insights/postmortem agent:** clusters closed tickets by touched infra/modules; flags hotspots
  ("40% of last month's bugs touched `billing/`"), suggests architecture work.
- **Invariant rules:** formal/high-level rules checked on every change.

## 5. Dashboard UX (Vite + React SPA)

1. **Pipeline board** — Kanban by stage: Intake · Triaged · Refined · Needs human · Ready ·
   Implementing · Gates · Review · PR open · Staging/QA · Done · Escalated. Cards show source icon,
   priority, size, BR badge, live turn counter, cost so far. Filters by repo/source/BR.
2. **PR inbox** — sorted by **blast radius desc**, then priority, then age. Each row expands to a
   **visual diff viewer** (side-by-side screenshots, slider, video), gate summary, reviewer
   findings, one-click approve/merge (via GitHub), "send back with note".
3. **Ticket detail** — timeline of stages, evidence, agent transcript (collapsible), gate runs
   with logs, turn graph, cost breakdown, links to source(s) and duplicates.
4. **Escalation inbox** — tickets that hit turn/cost caps or reviewer escalations, with the
   diagnosis and a "fix the factory" prompt (edit rules / add gate / mark as human-only).
5. **Metrics** — turns per ticket (p50/p90), cost per ticket and per merged PR, gate failure
   reasons (top rules), escalation rate, auto-merge rate & rollback rate, lead time
   (intake → merge), dedupe rate, human review time saved.
6. **Factory config (per repo)** — rules file viewer (from repo) + Alpine overrides, gate
   catalogue with thresholds, blast-radius path mapping, merge policy matrix, turn/cost caps,
   model selection, MCP servers/skills, environment/seed recipe, protected paths. Config is
   **GitOps-first**: `.alpine/factory.yaml` in the repo is the source of truth; the UI edits it by
   opening a PR (UI-only overrides are allowed for org-level policy that repos can't loosen).
7. **Org settings** — members/roles, integrations, model keys (BYOK), budgets, data policies, audit log, billing.

## 6. Pricing model

Recommendation: **hybrid — platform fee per active repo (not per seat) + metered compute +
model usage at cost-plus or BYOK.**

Rationale: value scales with tickets processed, not with humans; per-seat pricing punishes the
exact outcome we want (fewer humans touching routine work). But pure usage pricing is hard to
budget, so a predictable base matters.

| Plan | Price (draft) | Includes |
| --- | --- | --- |
| **Starter** | $0 | 1 private repo, 50 tickets/mo, 300 factory minutes, BYOK required, community support |
| **Team** | $400/mo per org + $100 per active repo beyond 3 | 3 repos, 2,000 factory minutes, all MVP gates, Slack/Linear/Jira, 90-day artifact retention |
| **Business** | from $2,500/mo | preview envs + QA agent, auto-merge, SSO/SCIM, audit export, 1-yr retention, invariant rules |
| **Enterprise** | custom | dedicated cell / VPC runners (BYO cloud), data residency, custom retention, SLA |

- **Factory minutes:** sandbox + preview env compute, billed per vCPU-minute tier (e.g.
  $0.01–0.02/vCPU-min) above the included pool.
- **Model usage:** (a) **BYOK** — tenant's keys, no markup, Alpine only meters for budgets;
  or (b) Alpine-provided models at provider cost + ~15–20%.
- **Budgets:** per-org monthly cap, per-repo cap, per-ticket cost cap (doubles as a safety rail).
- "Seats" are free for viewers/reporters; optionally a small reviewer seat fee for Enterprise
  procurement that expects seats.

Open: validate willingness to pay for "per merged PR" pricing as an alternative outcome-based metric.

## 7. Success metrics

- North star: **verified merged tickets per week per repo** (merged + passed staging/QA + not reverted within 7 days).
- Quality: revert rate < 2%, escalation rate trend, reviewer-caught issues per PR.
- Efficiency: median turns per merged ticket, median cost per merged ticket, intake→merge lead time.
- Trust: % of BR1 PRs merged without human edits; auto-merge adoption.
- Intake: dedupe precision (target > 95%), % tickets reproduced.

## 8. Non-goals (for now)
- Replacing the team's CI/CD (we call it and post results; we don't host their prod deploys).
- IDE / inline completion.
- Greenfield app generation from a prompt.
- Running against real production data.
