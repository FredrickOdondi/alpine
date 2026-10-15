/**
 * MSW handlers for the Alpine API. Spec endpoints match api/openapi.yaml exactly (paths,
 * params, status codes, camelCase bodies).
 */
import { delay, http, HttpResponse, sse, type HttpResponseResolver } from 'msw'
import type {
  ApiError,
  Artifact,
  AuthProviders,
  BoardEvent,
  BoardEventType,
  GateResult,
  ListResponse,
  Me,
  MetricsWindow,
  PullRequest,
  PullRequestReviewRequest,
  PullRequestState,
  Repo,
  Stage,
  TicketActionRequest,
  TicketSummary,
} from '@/api/types'
import {
  applyAction,
  artifactsFor,
  buildMetrics,
  evidenceFor,
  findTicket,
  gateResultsFor,
  me,
  prDecisions,
  repos,
  reviewFor,
  sortByBlastRadius,
  subscribe,
  ticketDetail,
  ticketSummaries,
  tickets,
  allPullRequests,
  turnsFor,
} from './db'
import { factoryConfigFor } from './factory-config'

/** Stands in for the HTTP-only `alpine_session` cookie the real API sets. */
const SESSION_KEY = 'alpine.mock.session'
const isSignedIn = () => localStorage.getItem(SESSION_KEY) === '1'
const list = <T,>(items: T[]): ListResponse<T> => ({ items })
/** Typed JSON response widened to Response so handlers can mix success and error bodies. */
const json = <T,>(body: T, init?: ResponseInit): Response => HttpResponse.json(body as object, init)
const error = (status: number, code: string, message: string) => json<ApiError>({ code, message }, { status })
const notFound = (message: string) => error(404, 'not_found', message)
const unauthorized = () => error(401, 'unauthenticated', 'Sign in required')
const noContent = (status = 204) => new HttpResponse(null, { status })

/** Small latency so loading states are visible but the app stays snappy. */
const latency = () => delay(150 + Math.random() * 200)

type BoardEventMap = { [K in BoardEventType]: BoardEvent }

/** Wrap a handler so it 401s without a session, like every non-auth endpoint in the spec. */
type ResolverArgs = Parameters<HttpResponseResolver>[0]
const authed =
  (fn: (args: ResolverArgs) => Promise<Response> | Response) =>
  async (args: ResolverArgs) => {
    await latency()
    return isSignedIn() ? fn(args) : unauthorized()
  }

export const handlers = [
  /* ---------------------------------------------------------------- auth (spec) */
  http.get('/v1/auth/providers', () => json<AuthProviders>({ google: false, github: true, magicLink: false, devLogin: true })),

  http.post('/v1/auth/dev-login', async () => {
    await delay(500)
    localStorage.setItem(SESSION_KEY, '1')
    return noContent()
  }),

  // The real endpoint 302s to GitHub; the mock signs in and bounces back to redirect_to.
  http.get('/v1/auth/github/login', ({ request }) => {
    localStorage.setItem(SESSION_KEY, '1')
    const to = new URL(request.url).searchParams.get('redirect_to') ?? '/'
    return new HttpResponse(null, { status: 302, headers: { Location: to } })
  }),

  http.post('/v1/auth/logout', () => {
    localStorage.removeItem(SESSION_KEY)
    return noContent()
  }),

  http.get('/v1/me', authed(() => json<Me>(me))),
  http.get('/v1/orgs', authed(() => json(list(me.memberships)))),

  /* ---------------------------------------------------------------- repos & tickets (spec) */
  http.get('/v1/repos', authed(() => json<ListResponse<Repo>>(list(repos)))),

  http.get(
    '/v1/tickets',
    authed(({ request }) => {
      const q = new URL(request.url).searchParams
      const repoId = q.get('repoId')
      const stages = q.getAll('stage') as Stage[]
      const limit = Math.min(Number(q.get('limit') ?? 100), 500)
      const items = ticketSummaries()
        .filter((t) => (!repoId || t.repoId === repoId) && (stages.length === 0 || stages.includes(t.stage)))
        .sort((a, b) => b.updatedAt.localeCompare(a.updatedAt))
        .slice(0, limit)
      return json<ListResponse<TicketSummary>>(list(items))
    }),
  ),

  http.get(
    '/v1/tickets/:ticketId',
    authed(({ params }) => {
      const t = findTicket(String(params.ticketId))
      return t ? json(ticketDetail(t)) : notFound('Ticket not found')
    }),
  ),

  http.post(
    '/v1/tickets/:ticketId/actions',
    authed(async ({ params, request }) => {
      if (!findTicket(String(params.ticketId))) return notFound('Ticket not found')
      const body = (await request.json()) as TicketActionRequest
      if (!applyAction(String(params.ticketId), body.action))
        return error(409, 'conflict', `Can't ${body.action.replace('_', ' ')} a ticket in this stage`)
      return noContent(202)
    }),
  ),

  http.get(
    '/v1/tickets/:ticketId/runs',
    authed(({ params }) => {
      const t = findTicket(String(params.ticketId))
      return t ? json(list(ticketDetail(t).runs)) : notFound('Ticket not found')
    }),
  ),

  http.get(
    '/v1/tickets/:ticketId/gate-results',
    authed(({ params, request }) => {
      const t = findTicket(String(params.ticketId))
      if (!t) return notFound('Ticket not found')
      const runId = new URL(request.url).searchParams.get('runId')
      return json<ListResponse<GateResult>>(list(gateResultsFor(t).filter((g) => !runId || g.runId === runId)))
    }),
  ),

  http.get(
    '/v1/runs/:runId/turns',
    authed(({ params }) => {
      const turns = turnsFor(String(params.runId))
      return turns ? json(list(turns)) : notFound('Run not found')
    }),
  ),

  /* ---------------------------------------------------------------- PR inbox (spec) */
  http.get(
    '/v1/pull-requests',
    authed(({ request }) => {
      const state = (new URL(request.url).searchParams.get('state') ?? undefined) as PullRequestState | undefined
      const items = sortByBlastRadius(allPullRequests().filter((p) => !state || p.state === state)).map(withSummaries)
      return json<ListResponse<PullRequest>>(list(items))
    }),
  ),

  /* ---------------------------------------------------------------- artifacts, review, evidence, metrics, config (spec) */
  http.get(
    '/v1/tickets/:ticketId/artifacts',
    authed(({ params }) => {
      const t = findTicket(String(params.ticketId))
      return t ? json<ListResponse<Artifact>>(list(artifactsFor(t))) : notFound('Ticket not found')
    }),
  ),

  http.get(
    '/v1/tickets/:ticketId/review',
    authed(({ params }) => {
      const t = findTicket(String(params.ticketId))
      const review = t && reviewFor(t)
      return review ? json(review) : notFound('No review yet')
    }),
  ),

  http.get(
    '/v1/pull-requests/:id/evidence',
    authed(({ params }) => {
      const ev = evidenceFor(String(params.id))
      return ev ? json(ev) : notFound('Pull request not found')
    }),
  ),

  http.post(
    '/v1/pull-requests/:id/review',
    authed(async ({ params, request }) => {
      if (!evidenceFor(String(params.id))) return notFound('Pull request not found')
      const body = (await request.json()) as PullRequestReviewRequest
      prDecisions.set(String(params.id), body.action === 'approve' ? 'approved' : 'changes_requested')
      return json(evidenceFor(String(params.id)))
    }),
  ),

  http.get(
    '/v1/metrics',
    authed(({ request }) => {
      const q = new URL(request.url).searchParams
      return json(buildMetrics((q.get('window') ?? '30d') as MetricsWindow, q.get('repoId')))
    }),
  ),

  http.get(
    '/v1/repos/:repoId/config',
    authed(({ params }) => {
      const cfg = factoryConfigFor(String(params.repoId))
      return cfg ? json(cfg) : notFound('No .alpine/factory.yaml on the default branch')
    }),
  ),

  /* ---------------------------------------------------------------- SSE (spec) */
  sse<BoardEventMap>('/v1/events', ({ client, request }) => {
    const repoId = new URL(request.url).searchParams.get('repoId')
    const unsubscribe = subscribe((event) => {
      if (repoId && event.repoId !== repoId) return
      client.send({ event: event.type, data: event })
    })
    request.signal.addEventListener('abort', unsubscribe)
  }),
]

/** Exposed for tests/debugging. */
export const __mockState = { tickets }

/** The spec's PR list carries gate/diff summaries and the human decision (mirrors the API). */
function withSummaries(pr: PullRequest): PullRequest {
  const ev = evidenceFor(pr.id)
  if (!ev) return pr
  return {
    ...pr,
    gateSummary: ev.gateSummary,
    diffSummary: { additions: ev.additions, deletions: ev.deletions, filesChanged: ev.files.length },
    humanDecision: ev.humanDecision,
  }
}
