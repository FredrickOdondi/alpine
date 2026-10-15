/**
 * In-memory mock database + live pipeline simulator.
 * Resources match api/openapi.yaml (via the generated types re-exported from src/api/types).
 */
import type {
  AgentRun,
  AgentTurn,
  Artifact,
  BlastRadius,
  BoardEvent,
  GateResult,
  GateStatus,
  HumanDecision,
  Me,
  Metrics,
  MetricsWindow,
  Priority,
  PullRequest,
  PullRequestEvidence,
  Repo,
  Review,
  Size,
  SourceKind,
  Stage,
  StageTransition,
  TicketAction,
  TicketDetail,
  TicketSummary,
} from '@/api/types'
import { CONTENT } from './content'
import { ticketShot } from './images'

/* ----------------------------------------------------------------------------- rng / ids */

function mulberry32(seed: number) {
  return () => {
    seed |= 0
    seed = (seed + 0x6d2b79f5) | 0
    let t = Math.imul(seed ^ (seed >>> 15), 1 | seed)
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}
const hash = (s: string) => [...s].reduce((h, c) => (Math.imul(h, 31) + c.charCodeAt(0)) | 0, 7)
const pick = <T,>(rng: () => number, arr: readonly T[]): T => arr[Math.floor(rng() * arr.length)]
const round2 = (n: number) => Math.round(n * 100) / 100
/** Deterministic UUID-shaped ids, e.g. uuid('7a1c', 3) → 7a1c0000-0000-4000-8000-000000000003. */
const uuid = (prefix: string, n: number) => `${prefix.padEnd(8, '0')}-0000-4000-8000-${String(n).padStart(12, '0')}`

/* ----------------------------------------------------------------------------- fixtures */

const NOW = Date.now()
const minutesAgo = (m: number) => new Date(NOW - m * 60_000).toISOString()

const ORG_ID = uuid('0a9e', 1)
const org = { id: ORG_ID, slug: 'kilima', name: 'Kilima Commerce', plan: 'team' as const }

export const me: Me = {
  user: { id: uuid('05e5', 1), githubLogin: 'tess-mwangi', name: 'Tess Mwangi', email: 'tess@kilima.example', avatarUrl: null },
  activeOrg: org,
  role: 'factory_owner',
  memberships: [{ org, role: 'factory_owner' }],
  csrfToken: 'mock-csrf-token',
  authMethod: 'github',
}

export const STOREFRONT = uuid('4e90', 1)
export const PAYMENTS = uuid('4e90', 2)

export const repos: Repo[] = [
  { id: STOREFRONT, orgId: ORG_ID, fullName: 'kilima/storefront-web', defaultBranch: 'main', githubInstallationId: 51873310, configSha: '4f2c9e1', createdAt: minutesAgo(60 * 24 * 21) },
  { id: PAYMENTS, orgId: ORG_ID, fullName: 'kilima/payments-api', defaultBranch: 'main', githubInstallationId: 51873310, configSha: 'b81d07a', createdAt: minutesAgo(60 * 24 * 9) },
]

const TITLES: [string, SourceKind, BlastRadius][] = [
  ['Checkout button stays disabled after coupon removal', 'sentry', 1],
  ['TypeError: cart.total is undefined on /checkout', 'sentry', 1],
  ['Product grid jumps when images lazy-load', 'github', 1],
  ['Order history pagination skips page 2', 'github', 3],
  ['Refund webhook retried with stale idempotency key', 'sentry', 4],
  ['Button component ignores `size="sm"` in dark surfaces', 'github', 2],
  ['Currency formatter drops trailing zero for KES', 'github', 1],
  ['Add index on payments(created_at, merchant_id)', 'github', 4],
  ['Session cookie missing SameSite on staging', 'sentry', 3],
  ['Search suggestions flicker on slow 3G', 'github', 1],
  ['Design tokens: spacing scale off-by-one in Tag', 'github', 2],
  ['Rate limiter counts OPTIONS preflight requests', 'sentry', 3],
  ['Empty state copy on wishlist is outdated', 'github', 1],
  ['Payout report CSV has wrong column order', 'github', 3],
  ['Modal focus trap escapes on Shift+Tab', 'github', 2],
  ['N+1 query on /merchants/:id/settlements', 'sentry', 3],
  ['Migrate ledger_entries.amount to numeric(20,4)', 'github', 4],
  ['Footer links 404 after locale switch', 'sentry', 1],
  ['Toast stacks overlap the cookie banner', 'github', 2],
  ['Webhook signature check uses non-constant-time compare', 'github', 4],
  ['Address autocomplete ignores apartment field', 'github', 1],
  ['Retry storm when card network times out', 'sentry', 3],
  ['Skeleton loaders never clear on 304 responses', 'sentry', 1],
  ['Inventory badge shows "0 left" for preorders', 'github', 1],
  ['Flaky e2e: guest checkout races the payment iframe', 'github', 2],
  ['Admin table sorting resets after inline edit', 'github', 2],
]

/** Happy-path order through the TicketWorkflow (spec stages). */
const FLOW: Stage[] = ['intake', 'triage', 'refinement', 'prioritize', 'implement', 'gates', 'review', 'merge_policy', 'pr_open', 'done']
const idx = (s: Stage) => FLOW.indexOf(s)

const SEED_STAGES: Stage[] = [
  'intake', 'triage', 'refinement', 'refinement', 'prioritize', 'needs_human', 'prioritize',
  'implement', 'implement', 'implement', 'gates', 'gates', 'review', 'review',
  'pr_open', 'pr_open', 'pr_open', 'merge_policy', 'pr_open', 'done', 'done',
  'escalated', 'escalated', 'implement', 'intake', 'pr_open',
]

const PRIORITIES: Priority[] = ['P0', 'P1', 'P1', 'P2', 'P2', 'P2', 'P3']
const SIZES: Size[] = ['XS', 'S', 'S', 'M', 'M', 'L']
export const LIMITS = { maxTurns: 100, maxGateIterations: 15, maxCostUsd: 20 }
const MODELS = { cheap: 'gpt-5-mini', strong: 'gpt-5', alt: 'o4-reviewer' }

const refined = (s: Stage) => s === 'needs_human' || s === 'escalated' || idx(s) > idx('refinement')
const implemented = (s: Stage) => s === 'escalated' || idx(s) >= idx('implement')
const reviewed = (s: Stage) => idx(s) > idx('review')

interface MockTicket extends TicketSummary {
  n: number
}

const contentFor = (t: MockTicket) => CONTENT[t.n % CONTENT.length]
const escalationText = (n: number) =>
  `Hit the 100-turn cap. Gate e2e kept failing on ${CONTENT[n % CONTENT.length].files.at(-1)}; likely missing seed data for this path.`

function makeTicket(n: number, stage: Stage, createdMinutesAgo: number): MockTicket {
  const rng = mulberry32(1000 + n)
  const [title, source, br] = TITLES[n % TITLES.length]
  const repo = br === 4 || /payout|ledger|webhook|card|settlement|refund/i.test(title) ? repos[1] : repos[0]
  const turns = implemented(stage) ? (stage === 'escalated' ? 100 : Math.floor(8 + rng() * 60)) : 0
  const prefix = repo.id === PAYMENTS ? 'PAY' : 'SHOP'
  return {
    n,
    id: uuid('7a1c', n + 1),
    orgId: ORG_ID,
    repoId: repo.id,
    repoFullName: repo.fullName,
    key: `${prefix}-${120 + n}`,
    title,
    type: 'bug',
    stage,
    source,
    sourceUrl: source === 'sentry' ? `https://kilima.sentry.io/issues/${48000 + n}/` : `https://github.com/${repo.fullName}/issues/${300 + n}`,
    priority: pick(rng, PRIORITIES),
    size: refined(stage) ? pick(rng, SIZES) : undefined,
    brPredicted: refined(stage) ? br : undefined,
    brFinal: idx(stage) >= idx('merge_policy') ? br : undefined,
    turnsUsed: turns,
    gateIterations: implemented(stage) ? Math.max(1, Math.floor(turns / 14)) : 0,
    costUsd: round2(0.02 + (refined(stage) ? 0.38 : 0) + turns * (0.06 + rng() * 0.05)),
    escalationReason:
      stage === 'needs_human'
        ? 'Ambiguous acceptance criteria: product decision needed on whether coupons can stack.'
        : stage === 'escalated'
          ? escalationText(n)
          : null,
    createdAt: minutesAgo(createdMinutesAgo),
    updatedAt: minutesAgo(Math.max(1, createdMinutesAgo - 30 - Math.floor(rng() * 60))),
  }
}

export const tickets: MockTicket[] = SEED_STAGES.map((s, i) => makeTicket(i, s, 60 * 30 - i * 55))
const publicTicket = (t: MockTicket): TicketSummary => {
  const { n, ...summary } = t
  void n
  return summary
}
export const ticketSummaries = () => tickets.map(publicTicket)
export const findTicket = (id: string) => tickets.find((t) => t.id === id)

/* ----------------------------------------------------------------------------- detail */

function actorFor(s: Stage): string {
  if (s === 'intake') return 'system'
  if (s === 'triage') return 'agent:triage'
  if (s === 'refinement' || s === 'prioritize' || s === 'needs_human') return 'agent:refine'
  if (s === 'implement') return 'agent:implement'
  if (s === 'gates') return 'system'
  if (s === 'review' || s === 'merge_policy') return 'agent:review'
  if (s === 'done') return `user:${me.user.githubLogin}`
  return 'system'
}

function historyFor(t: MockTicket): StageTransition[] {
  const path: Stage[] = []
  if (t.stage === 'needs_human') path.push('intake', 'triage', 'refinement', 'prioritize', 'needs_human')
  else if (t.stage === 'escalated') path.push('intake', 'triage', 'refinement', 'prioritize', 'implement', 'gates', 'implement', 'gates', 'escalated')
  else {
    for (let i = 0; i <= idx(t.stage); i++) {
      path.push(FLOW[i])
      if (FLOW[i] === 'gates' && idx(t.stage) > idx('gates')) path.push('implement', 'gates')
    }
  }
  const start = new Date(t.createdAt).getTime()
  const span = Math.max(60_000, new Date(t.updatedAt).getTime() - start)
  return path.map((to, i) => ({
    id: t.n * 100 + i,
    fromStage: i === 0 ? undefined : path[i - 1],
    toStage: to,
    reason:
      to === 'implement' && path[i - 1] === 'gates'
        ? 'Gate failures fed back to implementer'
        : to === 'escalated' || to === 'needs_human'
          ? t.escalationReason
          : to === 'pr_open'
            ? `Blast radius BR${t.brFinal ?? t.brPredicted}: human merge required`
            : null,
    actor: actorFor(to),
    at: new Date(start + (span * i) / Math.max(1, path.length - 1)).toISOString(),
  }))
}

function runsFor(t: MockTicket): AgentRun[] {
  const runs: AgentRun[] = []
  const base = new Date(t.createdAt).getTime()
  const iso = (m: number) => new Date(base + m * 60_000).toISOString()
  const add = (r: Omit<AgentRun, 'id' | 'ticketId'>) => runs.push({ id: uuid('3c11', t.n * 10 + runs.length + 1), ticketId: t.id, ...r })
  const s = t.stage
  if (s !== 'intake')
    add({ kind: 'triage', status: s === 'triage' ? 'running' : 'succeeded', model: MODELS.cheap, turns: 3, gateIterations: 0, costUsd: 0.02, startedAt: iso(1), endedAt: s === 'triage' ? null : iso(2) })
  if (refined(s) || s === 'refinement')
    add({ kind: 'refine', status: s === 'refinement' ? 'running' : 'succeeded', model: MODELS.strong, turns: 14, gateIterations: 0, costUsd: 0.38, startedAt: iso(3), endedAt: s === 'refinement' ? null : iso(18) })
  if (implemented(s)) {
    const running = s === 'implement' || s === 'gates'
    add({
      kind: 'implement',
      status: s === 'escalated' ? 'escalated' : running ? 'running' : 'succeeded',
      model: MODELS.strong,
      turns: t.turnsUsed,
      gateIterations: t.gateIterations,
      costUsd: round2(Math.max(0, t.costUsd - 0.4 - (reviewed(s) ? 0.21 : 0))),
      startedAt: iso(25),
      endedAt: running ? null : iso(25 + t.turnsUsed),
    })
  }
  if (idx(s) >= idx('review'))
    add({ kind: 'review', status: s === 'review' ? 'running' : 'succeeded', model: MODELS.alt, turns: 6, gateIterations: 0, costUsd: 0.21, startedAt: iso(30 + t.turnsUsed), endedAt: s === 'review' ? null : iso(36 + t.turnsUsed) })
  return runs
}

export function turnsFor(runId: string): AgentTurn[] | null {
  for (const t of tickets) {
    const run = runsFor(t).find((r) => r.id === runId)
    if (!run) continue
    const rng = mulberry32(hash(runId))
    const tools = ['read_file', 'search_code', 'edit_file', 'run_tests', 'run_command', 'git_diff']
    return Array.from({ length: Math.min(run.turns, 40) }, (_, i) => ({
      id: hash(runId) * 100 + i,
      runId,
      n: i + 1,
      model: run.model ?? MODELS.strong,
      tokensIn: Math.floor(12_000 + rng() * 18_000),
      tokensOut: Math.floor(300 + rng() * 1200),
      tokensCached: Math.floor(8_000 + rng() * 9_000),
      costUsd: round2(run.costUsd / Math.max(1, run.turns)),
      toolCalls: [{ name: pick(rng, tools), summary: contentFor(t).files[i % contentFor(t).files.length] }],
      transcriptRef: null,
      createdAt: new Date(new Date(run.startedAt).getTime() + i * 40_000).toISOString(),
    }))
  }
  return null
}

const GATES = ['lint', 'typecheck', 'dupes', 'unit', 'e2e', 'visual', 'policy'] as const
const FAILURES: Record<string, { rule: string; message: string; suggestion?: string }> = {
  lint: { rule: 'no-unused-vars', message: "'quote' is assigned a value but never used.", suggestion: 'Remove the unused binding.' },
  typecheck: { rule: 'TS2532', message: "Object is possibly 'undefined'." },
  dupes: { rule: 'jscpd', message: '22 duplicated lines with src/lib/money/formatPrice.ts' },
  unit: { rule: 'expect(received).toBe(expected)', message: 'Expected 24550, received NaN' },
  e2e: { rule: 'locator.click: Timeout 15000ms exceeded', message: 'waiting for getByRole("button", { name: "Place order" }) to be enabled' },
  policy: { rule: 'no-ticket-comments', message: 'Found a `// TODO SHOP-121` reference comment' },
  visual: { rule: 'visual-threshold', message: 'Unexpected 4.1% pixel change outside the ticket scope' },
}

function gatesFor(t: MockTicket): GateResult[] {
  const impl = runsFor(t).find((r) => r.kind === 'implement')
  if (!impl || t.gateIterations === 0) return []
  const rng = mulberry32(hash(t.id) + 7)
  const files = contentFor(t).files
  const out: GateResult[] = []
  const shown = Math.min(t.gateIterations, 4)
  for (let k = 1; k <= shown; k++) {
    const iteration = t.gateIterations - shown + k
    const last = k === shown
    for (const gate of GATES) {
      if (last && t.stage === 'gates' && (gate === 'e2e' || gate === 'visual')) continue // still running
      let status: GateStatus = 'pass'
      if (!last && rng() < 0.3) status = 'fail'
      if (last && t.stage === 'escalated' && gate === 'e2e') status = 'fail'
      if (gate === 'dupes' && status === 'pass' && rng() < 0.15) status = 'warn'
      if (gate === 'visual' && t.repoId === PAYMENTS) status = 'skipped'
      const f = FAILURES[gate]
      const file = gate === 'e2e' ? files.find((p) => p.startsWith('e2e/')) ?? files.at(-1)! : files[0]
      out.push({
        id: uuid('6a7e', t.n * 1000 + iteration * 10 + GATES.indexOf(gate)),
        runId: impl.id,
        ticketId: t.id,
        iteration,
        gateId: gate,
        status,
        durationMs: Math.floor((gate === 'e2e' ? 90_000 : gate === 'visual' ? 40_000 : 6_000) * (0.6 + rng())),
        findings:
          status === 'fail' || status === 'warn'
            ? [{ file, line: Math.ceil(rng() * 120), rule: f.rule, severity: status === 'fail' ? 'error' : 'warning', message: f.message, suggestion: f.suggestion }]
            : [],
        artifactsRef: gate === 'e2e' ? `s3://alpine-artifacts/org/${ORG_ID}/tickets/${t.id}/gates/${iteration}/e2e/` : null,
        createdAt: new Date(new Date(impl.startedAt).getTime() + iteration * 7 * 60_000).toISOString(),
      })
    }
  }
  return out
}

/** Ticket body in the shape refinement writes: problem statement + acceptance criteria. */
function bodyFor(t: MockTicket): string {
  const c = contentFor(t)
  if (!refined(t.stage)) return `${c.body}\n\nAwaiting refinement: reproduction and acceptance criteria are written by the refinement agent.`
  return `${c.body}\n\n## Acceptance criteria\n${c.ac.map((a) => `- ${a}`).join('\n')}`
}

export const prDecisions = new Map<string, HumanDecision>()

export function prFor(t: MockTicket): PullRequest | null {
  if (idx(t.stage) < idx('pr_open')) return null
  const n = 400 + t.n
  const slug = t.title.toLowerCase().replace(/[^a-z0-9]+/g, '-').slice(0, 32).replace(/-$/, '')
  const pr: PullRequest = {
    id: uuid('9b2e', t.n + 1),
    ticketId: t.id,
    ticketKey: t.key,
    ticketTitle: t.title,
    repoFullName: t.repoFullName,
    number: n,
    url: `https://github.com/${t.repoFullName}/pull/${n}`,
    headBranch: `alpine/${t.key}-${slug}`,
    headSha: (hash(t.id + 'sha') >>> 0).toString(16).padStart(8, '0').slice(0, 7),
    state: t.stage === 'done' ? 'merged' : 'open',
    blastRadius: t.brFinal ?? t.brPredicted ?? 1,
    priority: t.priority,
    turnsUsed: t.turnsUsed,
    costUsd: t.costUsd,
    openedAt: historyFor(t).find((h) => h.toStage === 'pr_open')?.at ?? t.updatedAt,
    mergedAt: t.stage === 'done' ? t.updatedAt : null,
  }
  return pr
}

export const allPullRequests = () => tickets.map(prFor).filter((p): p is PullRequest => p !== null)

export function evidenceFor(prId: string): PullRequestEvidence | null {
  const t = tickets.find((x) => prFor(x)?.id === prId)
  if (!t) return null
  const rng = mulberry32(hash(t.id) + 3)
  const c = contentFor(t)
  const ci = t.n % CONTENT.length
  const files = c.files.map((path, i) => {
    const migration = path.startsWith('migrations/')
    const test = /test|spec/.test(path)
    return {
      path,
      status: (migration || (test && i > 0 && rng() < 0.4) ? 'added' : 'modified') as 'added' | 'modified',
      additions: Math.floor((test ? 18 : 6) + rng() * (test ? 60 : 40)),
      deletions: migration ? 0 : Math.floor(rng() * 24),
    }
  })
  const br = t.brFinal ?? t.brPredicted ?? 1
  const hasVisual = t.repoId === STOREFRONT && br <= 2
  return {
    pullRequestId: prId,
    additions: files.reduce((s, f) => s + f.additions, 0),
    deletions: files.reduce((s, f) => s + f.deletions, 0),
    files,
    gateSummary: { passed: hasVisual ? 7 : 6, failed: 0, warned: rng() < 0.3 ? 1 : 0, total: hasVisual ? 7 : 6 },
    reviewVerdict: 'approve',
    visualDiffs: hasVisual
      ? [
          {
            route: c.route,
            beforeUrl: ticketShot(ci, c.route, 'before'),
            afterUrl: ticketShot(ci, c.route, 'after'),
            heatmapUrl: ticketShot(ci, c.route, 'heatmap'),
            changedPixelsPct: round2(0.6 + rng() * 4),
          },
        ]
      : [],
    humanDecision: prDecisions.get(prId) ?? null,
  }
}

export function artifactsFor(t: MockTicket): Artifact[] {
  if (!refined(t.stage)) return []
  const ci = t.n % CONTENT.length
  const route = contentFor(t).route
  let k = 0
  const a = (kind: Artifact['kind'], label: string, url: string, contentType: string, extra: Partial<Artifact> = {}): Artifact => ({
    id: uuid('a47f', t.n * 10 + k++),
    ticketId: t.id,
    kind,
    label,
    url,
    contentType,
    createdAt: historyFor(t).find((h) => h.toStage === 'refinement')?.at ?? t.createdAt,
    ...extra,
  })
  if (t.repoId === PAYMENTS) {
    // API/back-end bugs are reproduced with a failing test or HTTP script, not a browser video.
    return [
      a('log', `Failing test output · ${t.key}`, '#log', 'text/plain', { sizeBytes: 18_400 + t.n * 310 }),
      a('log', `HTTP repro script (repro.http) · ${t.key}`, '#http', 'text/plain', { sizeBytes: 2_100 + t.n * 40 }),
      a('trace', `OpenTelemetry trace · ${t.key}`, '#trace', 'application/json', { sizeBytes: 412_000 + t.n * 9_000 }),
    ]
  }
  return [
    a('video', `${route} · ${t.key}`, `/mock-artifacts/repro-${ci}.webm`, 'video/webm', { posterUrl: `/mock-artifacts/repro-${ci}.jpg` }),
    a('screenshot', `${route} · failure`, ticketShot(ci, route, 'repro'), 'image/svg+xml'),
    a('screenshot', `${route} · before the action`, ticketShot(ci, route, 'before'), 'image/svg+xml'),
    a('trace', `Playwright trace · ${t.key}`, '#trace', 'application/zip', { sizeBytes: 1_840_000 + t.n * 23_000 }),
  ]
}

export function reviewFor(t: MockTicket): Review | null {
  if (!reviewed(t.stage)) return null
  const files = contentFor(t).files
  return {
    id: uuid('2e71', t.n + 1),
    ticketId: t.id,
    verdict: 'approve',
    model: MODELS.alt,
    createdAt: t.updatedAt,
    findings: [
      { severity: 'minor', category: 'redundant state', file: files[0], message: 'A derived value is stored in state; compute it from the source of truth instead.' },
      { severity: 'info', category: 'tests', file: files.at(-1) ?? null, message: 'New test encodes the acceptance criteria; assertions are not tautological.' },
    ],
  }
}

export function ticketDetail(t: MockTicket): TicketDetail {
  const pr = prFor(t)
  return {
    ...publicTicket(t),
    body: bodyFor(t),
    confidence: refined(t.stage) ? round2(0.55 + mulberry32(t.n)() * 0.4) : null,
    workflowId: `ticket/${ORG_ID}/${t.id}`,
    limits: LIMITS,
    stageHistory: historyFor(t),
    runs: runsFor(t),
    pullRequests: pr ? [pr] : [],
  }
}

export const gateResultsFor = gatesFor

/* ----------------------------------------------------------------------------- PR inbox */

const PRIO_RANK: Record<Priority, number> = { P0: 0, P1: 1, P2: 2, P3: 3 }

/** Inbox order (spec): blast radius desc, then priority, then age (oldest first). */
export function sortByBlastRadius(prs: PullRequest[]): PullRequest[] {
  return [...prs].sort(
    (a, b) =>
      b.blastRadius - a.blastRadius ||
      PRIO_RANK[a.priority ?? 'P3'] - PRIO_RANK[b.priority ?? 'P3'] ||
      a.openedAt.localeCompare(b.openedAt),
  )
}

/* ----------------------------------------------------------------------------- metrics */

export function buildMetrics(window: MetricsWindow, repoId: string | null): Metrics {
  const days = window === '7d' ? 7 : window === '90d' ? 90 : 30
  const rng = mulberry32(hash(window + (repoId ?? 'all')))
  const scale = repoId ? (repoId === PAYMENTS ? 0.4 : 0.6) : 1
  const series = Array.from({ length: days }, (_, i) => {
    const d = new Date(NOW - (days - 1 - i) * 86_400_000)
    const trend = i / days
    const merged = Math.round((3 + rng() * 6 + trend * 4) * scale)
    const p50 = Math.round(34 - trend * 10 + rng() * 8)
    return {
      date: d.toISOString().slice(0, 10),
      ticketsMerged: merged,
      turnsP50: p50,
      turnsP90: Math.round(p50 * (1.9 + rng() * 0.6)),
      costUsd: round2(merged * (3.2 - trend + rng() * 1.5)),
      gatePassRate: round2(0.58 + trend * 0.18 + rng() * 0.08),
    }
  })
  const totalMerged = series.reduce((s, p) => s + p.ticketsMerged, 0)
  const totalCost = round2(series.reduce((s, p) => s + p.costUsd, 0))
  const avg = (f: (p: (typeof series)[number]) => number) => series.reduce((s, p) => s + f(p), 0) / series.length
  return {
    window,
    repoId,
    totals: {
      ticketsMerged: totalMerged,
      turnsP50: Math.round(avg((p) => p.turnsP50)),
      turnsP90: Math.round(avg((p) => p.turnsP90)),
      costPerTicketUsd: round2(totalCost / Math.max(1, totalMerged)),
      costTotalUsd: totalCost,
      gatePassRate: round2(avg((p) => p.gatePassRate)),
      escalationRate: round2(0.06 + rng() * 0.05),
      leadTimeHoursP50: round2(5 + rng() * 6),
    },
    series,
    gatePassRateByGate: [
      { gateId: 'lint', passRate: 0.94, runs: 412 },
      { gateId: 'typecheck', passRate: 0.88, runs: 412 },
      { gateId: 'dupes', passRate: 0.91, runs: 398 },
      { gateId: 'unit', passRate: 0.79, runs: 405 },
      { gateId: 'e2e', passRate: 0.66, runs: 371 },
      { gateId: 'visual', passRate: 0.83, runs: 220 },
      { gateId: 'policy', passRate: 0.97, runs: 412 },
    ].map((g) => ({ ...g, runs: Math.round(g.runs * scale * (days / 30)) })),
    topFailureReasons: [
      { gateId: 'e2e', rule: 'locator timeout waiting for enabled button', count: Math.round(48 * scale) },
      { gateId: 'unit', rule: 'assertion mismatch', count: Math.round(37 * scale) },
      { gateId: 'typecheck', rule: 'TS2532 object is possibly undefined', count: Math.round(29 * scale) },
      { gateId: 'dupes', rule: 'jscpd new duplication', count: Math.round(17 * scale) },
      { gateId: 'policy', rule: 'no-ticket-comments', count: Math.round(9 * scale) },
    ],
  }
}

/* ----------------------------------------------------------------------------- simulator */

type Listener = (e: BoardEvent) => void
const listeners = new Set<Listener>()
let timer: ReturnType<typeof setInterval> | null = null
const simRng = mulberry32(42)
let created = 0

export const SIM_INTERVAL_MS = 2500

function broadcast(e: BoardEvent) {
  for (const l of listeners) l(e)
}

function nextStage(t: MockTicket): Stage | null {
  switch (t.stage) {
    case 'done':
    case 'escalated':
    case 'closed':
    case 'duplicate':
      return null
    case 'needs_human':
      return null // waits for a human action
    case 'prioritize':
      return simRng() < 0.15 ? 'needs_human' : 'implement'
    case 'gates':
      if (t.turnsUsed >= LIMITS.maxTurns) return 'escalated'
      return simRng() < 0.4 ? 'implement' : 'review'
    case 'implement':
      return simRng() < 0.5 ? null : 'gates'
    case 'pr_open':
      return simRng() < 0.2 ? 'done' : null
    default:
      return FLOW[idx(t.stage) + 1] ?? null
  }
}

function event(t: MockTicket, type: BoardEvent['type'], previousStage?: Stage, reason?: string | null): BoardEvent {
  return { type, orgId: ORG_ID, ticketId: t.id, repoId: t.repoId, stage: t.stage, previousStage, reason: reason ?? null, at: t.updatedAt }
}

/** Advance the world by one tick and return the event to broadcast. */
export function simulateTick(): BoardEvent {
  const now = new Date().toISOString()
  const active = tickets.filter((t) => nextStage(t) !== null || t.stage === 'implement')
  if (created < 12 && (simRng() < 0.12 || active.length < 8)) {
    const t = makeTicket(tickets.length, 'intake', 0)
    t.createdAt = now
    t.updatedAt = now
    tickets.push(t)
    created++
    return event(t, 'ticket.created')
  }
  const t = pick(simRng, active)
  if (t.stage === 'implement') {
    t.turnsUsed = Math.min(LIMITS.maxTurns, t.turnsUsed + 1 + Math.floor(simRng() * 4))
    t.costUsd = round2(t.costUsd + 0.05 + simRng() * 0.2)
  }
  const to = nextStage(t)
  t.updatedAt = now
  if (!to) return event(t, 'ticket.updated')
  const from = t.stage
  t.stage = to
  if (to === 'gates') t.gateIterations += 1
  if (to === 'refinement' || to === 'prioritize') {
    t.size ??= pick(simRng, SIZES)
    t.brPredicted ??= TITLES[t.n % TITLES.length][2]
  }
  if (to === 'merge_policy') t.brFinal = t.brPredicted
  if (to === 'needs_human') t.escalationReason = 'Low confidence (0.58) and blast radius above the policy threshold.'
  if (to === 'escalated') t.escalationReason = escalationText(t.n)
  return event(t, 'ticket.stage_changed', from, t.escalationReason)
}

/** Apply a human action (a Temporal signal in production). Returns false if not allowed. */
export function applyAction(id: string, action: TicketAction): boolean {
  const t = findTicket(id)
  if (!t) return false
  const from = t.stage
  if ((action === 'approve' || action === 'reject') && from === 'needs_human') t.stage = action === 'approve' ? 'implement' : 'closed'
  else if ((action === 'resume' || action === 'close') && from === 'escalated') {
    t.stage = action === 'resume' ? 'implement' : 'closed'
    if (action === 'resume') {
      t.turnsUsed = 0
      t.gateIterations = 0
    }
  } else if (action === 'mark_merged' && from === 'pr_open') t.stage = 'done'
  else return false
  t.escalationReason = null
  t.updatedAt = new Date().toISOString()
  broadcast(event(t, 'ticket.stage_changed', from, `${action} by ${me.user.githubLogin}`))
  return true
}

/** Subscribe to simulated board events. The timer only runs while someone listens. */
export function subscribe(listener: Listener): () => void {
  listeners.add(listener)
  timer ??= setInterval(() => broadcast(simulateTick()), SIM_INTERVAL_MS)
  return () => {
    listeners.delete(listener)
    if (listeners.size === 0 && timer) {
      clearInterval(timer)
      timer = null
    }
  }
}
