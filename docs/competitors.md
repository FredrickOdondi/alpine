# Alpine — Competitive Landscape (Oct 2026)

Researched 2026-10-15. Each entry lists only what the cited pages say; where we're inferring a
gap we say "not described on the pages we read", which is not the same as "doesn't exist".
Pricing and features in this space change monthly; re-verify before external use.

Sources marked **[fetched]** were read in full (WebFetch or curl). Sources marked **[search]**
were seen only as search-result snippets and should be re-checked.

---

## Devin (Cognition)
**Covers**
- "Parallel cloud agents for serious engineering teams"; Devin spins up its own Windows, Mac and
  Linux VMs to build, test in its browser and record itself clicking through what it built; it can
  split a big task across parallel cloud agents; automations so it handles bugs, failed CI and
  other alerts as they land; Devin Review for bugs/vulnerabilities; every session runs in its own
  VM; RBAC/SSO/SCIM/OIDC; private networking. [devin.ai, fetched]
- Self-serve plans: Free, Pro $20/mo, Max $200/mo, Teams $80/mo minimum with $40/mo full seats or
  free flex seats drawing on shared on-demand credits; Automations and Devin Review draw from the
  team's on-demand credit pool; review trigger modes configurable per repo. [docs.devin.ai self-serve, fetched]

**Gaps vs Alpine**
- A powerful general agent + review + automations, but the pages we read don't describe a
  team-configured **deterministic gate pipeline with a hard turn cap** as a product primitive, nor
  **merge policy keyed to blast radius**. The implementer and the reviewer are the same vendor's
  system (Alpine insists on an independent, no-shared-context reviewer).
- Intake/triage/dedupe across Sentry + Slack + issue trackers into refined tickets is not the focus described.

## Factory (Droids)
**Covers**
- **Environments** (GA Oct 12, 2026): reusable sandbox definitions (repos, tools, deps) prebuilt
  into images; rebuilt every few hours and only validated builds promoted. **Automations** run on a
  schedule or in response to Slack, GitHub and webhooks on fresh sandboxes. **Agent Readiness**
  scores repos 1–5 ("most enterprise customers start at level two"). **Missions** = long-running
  multi-agent framework. Admins control reachable hosts, allowed/blocked shell commands,
  connectors and skills. [factory.com/news/droids-now-have-a-fleet-of-sandboxes, fetched]
- Droids work in terminal, IDE, browser, Slack; model choice per task; "one prompt to PR". [factory.ai/product/droids, search]
- Automations marketplace (code review, security audit, PR babysitter), per-automation model choice, BYOK, Factory Router. [factory.com/news/automations, search]

**Gaps vs Alpine**
- Strong platform primitives (environments, automations, policy), but it's a **toolkit**: the team
  still designs the intake → refine → implement → gate → review → merge pipeline themselves. No
  blast-radius merge policy or turn-cap/escalation metrics described on the pages we read.
- Closest competitor in spirit; "Agent Readiness" overlaps our onboarding check — expect convergence.

## Cursor Cloud Agents (formerly Background Agents) + Bugbot/Graphite
**Covers**
- Agents in isolated cloud VMs with full dev environments; unlimited parallel agents; can build,
  test and use desktop/browser; MCP servers; multi-repo environments; triggers from web, desktop,
  iOS, Slack, GitHub/Bitbucket comments, Linear, API; repo hooks (`.cursor/hooks.json`) run in the
  cloud; artifacts (screenshots, videos, logs) and remote desktop takeover; Builds prewarm
  environments; billed at API pricing for the chosen model with a spend limit. [cursor.com/docs/cloud-agent, fetched]
- Graphite signed an agreement to join Cursor (Dec 19, 2025); Graphite continues as an independent
  product; plans to combine Graphite's AI Reviewer with Cursor's Bugbot and integrate background
  agents with PRs. [graphite.com/blog/graphite-joins-cursor, fetched]

**Gaps vs Alpine**
- Excellent implementer + environment + review stack, increasingly integrated end-to-end on the
  *code* side. Intake (Sentry/user reports), dedupe, refinement with repro, and **risk-tiered merge
  automation** are not what the docs describe. Gates are whatever hooks/CI the team wires up.
- Bundling risk: Cursor + Graphite could add policy-based merging. Alpine should be able to *use*
  Cursor agents as an engine adapter rather than compete on raw implementation.

## GitHub Copilot cloud agent (formerly "coding agent")
**Covers**
- Researches a repo, plans, edits and runs tests and linters in an ephemeral GitHub Actions-powered
  environment; started from issues, agents panel, chat, and other surfaces; Copilot code review;
  **automations** (e.g. triage incoming issues, weekly tasks); GitHub Agentic Workflows (natural
  language tasks run as Actions); custom instructions, custom agents, skills, MCP, hooks. [docs.github.com about Copilot on GitHub.com, fetched]
- Documented limits: one repository per run, one branch and one PR per task, **59-minute max
  session**, GitHub-hosted repos only; incompatible rulesets block the agent. [same page, fetched]

**Gaps vs Alpine**
- Deeply native to GitHub, but single-repo/single-session scoped and GitHub-only. No refinement
  with repro evidence, cross-source dedupe, configurable gate ratchets with turn caps, or
  blast-radius merge policy described. The biggest **distribution** threat (it's already installed).

## OpenHands (All Hands AI)
**Covers**
- MIT-licensed open-source agent platform (Agent Canvas, CLI, Software Agent SDK); OpenHands Cloud
  (hosted) and Enterprise (self-hosted). Scheduled, polling and event-driven automations; isolated
  sandboxes and scalable agents in Cloud/Enterprise; Slack/GitHub/GitLab/Jira/Bitbucket; RBAC;
  Enterprise adds SAML, custom runtime images, LLM gateway & budgeting (LiteLLM), observability (Laminar). [docs.openhands.dev enterprise-vs-oss, fetched]
- BYOK supported across OSS and Cloud. [openhands.dev/pricing, search]

**Gaps vs Alpine**
- A model-agnostic agent runtime and building block, not an opinionated SDLC pipeline. Teams
  assemble triage, gates, review independence and merge policy themselves.
- Potential **ingredient** for Alpine (SDK as an engine adapter; self-host story for enterprise).

## Sweep
**Covers**
- Sweep open-sourced *sweep-next-edit-v2-7B*, a next-edit prediction model (fine-tuned from
  Qwen2.5-Coder-7B, 32k context) for in-editor edit prediction. [huggingface.co/sweepai/sweep-next-edit-v2-7B, fetched]
- Search results describe Sweep as a JetBrains coding assistant with an agent that turns GitHub or
  Jira tasks into PRs. [search; blog.sweep.dev and sweep.dev returned HTTP 500 when fetched]

**Gaps vs Alpine**
- IDE/inner-loop focus. Not an outer-loop pipeline competitor today.

## CodeRabbit (AI review)
**Covers**
- Agentic reviews in PR, CLI, IDE and agent loops; learnings; pre-merge checks; slop detection;
  1-click fixes; Team adds a prioritized PR queue with risk/effort guidance, test generation and
  post-merge actions; Advanced adds **"Blast Radius Audit"** and architecture impact review plus
  continuous security monitoring. Pricing (annual): Essentials $24, Team $48, Advanced $72 per
  user/mo; usage-based reviews $0.25/reviewed file; CodeRabbit Agent $0.40 per agent-minute;
  only developers who create PRs are billed. [coderabbit.ai/pricing, fetched]

**Gaps vs Alpine**
- Review + PR triage specialist. It *analyzes* blast radius and architecture impact; the page does
  not describe using blast radius to **drive auto-merge → staging → QA → prod** policy, nor intake,
  refinement or implementation loops with turn caps.
- Note: "blast radius" as a term is not unique to us — our differentiation is the **policy and
  pipeline** built on it, not the analysis alone. CodeRabbit could be an optional reviewer plug-in.

## Graphite
**Covers**
- Stacked PRs, merge queue keeping trunk green, CI optimizations (selective/batched runs for
  stacks), unified PR inbox; AI reviewer (Diamond, renamed under "Graphite Agent" as of
  10/08/2025). [graphite.com/blog/making-ai-code-review-accessible, fetched]
- Now part of Cursor, operating independently. [graphite.com/blog/graphite-joins-cursor, fetched]

**Gaps vs Alpine**
- Best-in-class review/merge *mechanics* for humans and agents, not ticket intake or implementation.
  Alpine's merge policy could sit on top of a merge queue (Graphite or GitHub's) rather than replace it.

## Linear (Linear Agent coding sessions)
**Covers**
- Delegate an issue → Linear Agent writes and tests the change with Claude Code or Codex in a
  secure cloud environment and returns a **draft PR**; can start apps, use browser automation and
  capture screenshots/recordings; **triage automations** can run a coding session whenever Triage
  Intelligence identifies a new bug; per-repo environments (runtimes, secrets, prepare script,
  custom guidance; one repo per environment); usage from AI credits; GitHub permissions and review
  requirements still determine what ships. [linear.app/docs/coding-sessions, fetched]
- Issues can also be delegated to third-party agents (Copilot, Cursor, Codex, Devin…). [linear.app/docs/agents-in-linear, search]

**Gaps vs Alpine**
- Owns the issue tracker and now the first implementation hop — a serious threat for Linear-native
  teams. Intake is Linear-centric; no deterministic gate pipeline config, turn caps, independent
  reviewer or blast-radius merge policy described. Alpine should integrate (Linear as source/sink) rather than replace.

---

## Summary matrix (from the pages above; ✓ = described, ◐ = partial, — = not described)

| | Intake + dedupe | Repro/refine w/ evidence | Implement in sandbox | Configurable deterministic gates + turn cap | Independent reviewer | Visual diff on PR | Blast-radius merge policy | Factory metrics (turns/cost/gates) |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Devin | ◐ (automations on alerts) | ◐ (records its testing) | ✓ | — | ◐ (Devin Review, same vendor) | ◐ | — | ◐ |
| Factory | ◐ (automations) | — | ✓ | ◐ (policy, readiness) | ◐ | — | — | ◐ |
| Cursor (+Graphite) | ◐ (triggers) | ◐ (artifacts) | ✓ | ◐ (hooks) | ◐ (Bugbot) | ✓ artifacts | — | ◐ |
| Copilot cloud agent | ◐ (issue triage automations) | — | ✓ | ◐ (runs tests/linters) | ◐ (Copilot review) | — | — | ◐ |
| OpenHands | ◐ | — | ✓ | — | — | — | — | ◐ |
| Sweep | — | — | ◐ | — | — | — | — | — |
| CodeRabbit | — | — | ◐ (agent minutes) | ◐ (pre-merge checks) | ✓ | — | ◐ (analysis only) | ◐ |
| Graphite | — | — | — | ◐ (merge queue/CI) | ✓ | — | — | ◐ |
| Linear | ✓ (Triage Intelligence) | ◐ | ✓ | — | — | ◐ recordings | — | ◐ |
| **Alpine** | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |

## Alpine's wedge

Everyone above sells a **worker** (an agent that writes code) or an **inspector** (an AI reviewer).
Alpine sells the **factory**: one durable pipeline from Sentry error / bug report to verified merge,
where **deterministic quality gates** (lint, types, duplication, dead code, complexity, e2e, visual
diff) with a **hard turn cap** decide whether agent work is acceptable, and a **blast-radius merge
policy** decides how much human attention each change gets — with turns/cost/gate metrics so teams
improve the factory instead of babysitting PRs.

Strategic posture: be **agent-agnostic** (engine adapters for our own loop and, where licensing
allows, third-party agents) and **tool-agnostic at the edges** (Linear/Jira/GitHub/Sentry as
sources and sinks; GitHub merge queue/Graphite for merge mechanics). Compete on the pipeline,
the gates and the policy — the layer incumbents are least likely to make neutral across tools.

## Context source
- The concept talk: "agentic software factory" interview, https://youtu.be/WOhJflEz3mY [transcript fetched].
  Key specifics used: triage from Slack/Sentry/user reports; refinement with Playwright video on
  mock prod data; blast radius as layers; Fallow for duplication/complexity; 100-turn hard limit;
  independent reviewer without shared context; smoke subset of ~950 e2e suites on staging; QA
  agent; formal invariants; model router, docs/changelog and postmortem agents as future work.

## URLs actually fetched
- https://www.youtube.com/watch?v=WOhJflEz3mY
- https://devin.ai/ (via curl; WebFetch returned 404)
- https://docs.devin.ai/admin/billing/self-serve
- https://factory.com/news/droids-now-have-a-fleet-of-sandboxes
- https://cursor.com/docs/cloud-agent
- https://docs.github.com/en/copilot/using-github-copilot/coding-agent/about-assigning-tasks-to-copilot
- https://docs.openhands.dev/enterprise/enterprise-vs-oss
- https://huggingface.co/sweepai/sweep-next-edit-v2-7B
- https://www.coderabbit.ai/pricing
- https://graphite.com/blog/graphite-joins-cursor
- https://graphite.com/blog/making-ai-code-review-accessible
- https://linear.app/docs/coding-sessions
- https://github.com/fallow-rs/fallow (gate tooling reference)

Search-only (not fetched): factory.ai/product/droids, factory.com/news/automations,
openhands.dev/pricing, linear.app/docs/agents-in-linear, Sweep descriptions.
