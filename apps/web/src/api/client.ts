/**
 * Alpine API access for the SPA.
 *
 * Every endpoint goes through the generated client (`createAlpineClient`, openapi-fetch) built
 * from api/openapi.yaml. In live mode, state-changing requests carry the session's CSRF token
 * (from GET /v1/me, or GET /v1/auth/csrf on first use) as X-CSRF-Token.
 */
import { createAlpineClient, type Middleware } from '@alpine/api-client'
import { apiConfig, mockTransport } from './config'
import type {
  AgentTurn,
  Artifact,
  AuthProviders,
  FactoryConfig,
  ListResponse,
  Me,
  Metrics,
  MetricsWindow,
  PullRequest,
  PullRequestEvidence,
  PullRequestReviewRequest,
  PullRequestState,
  Repo,
  Review,
  Stage,
  TicketActionRequest,
  TicketDetail,
  TicketSummary,
  GateResult,
} from './types'

export class ApiRequestError extends Error {
  readonly status: number
  readonly code: string
  constructor(status: number, code: string, message: string) {
    super(message)
    this.status = status
    this.code = code
  }
}

/** fetch that routes to in-page MSW handlers when the mock service worker couldn't start. */
async function transport(request: Request): Promise<Response> {
  if (apiConfig.mode === 'mock' && mockTransport.kind === 'inline') {
    const { resolveInline } = await import('../mocks/inline')
    return resolveInline(request)
  }
  return fetch(request)
}

const client = createAlpineClient({ baseUrl: apiConfig.baseUrl, fetch: transport })

/** Session CSRF token (spec: Me.csrfToken / GET /v1/auth/csrf), sent on unsafe methods. */
let csrfToken: string | undefined
const SAFE_METHODS = new Set(['GET', 'HEAD', 'OPTIONS'])

async function ensureCsrfToken(): Promise<string | undefined> {
  if (csrfToken === undefined) {
    const { data } = await client.GET('/v1/auth/csrf')
    csrfToken = data?.csrfToken
  }
  return csrfToken
}

const csrfMiddleware: Middleware = {
  async onRequest({ request, schemaPath }) {
    if (SAFE_METHODS.has(request.method) || schemaPath.startsWith('/v1/webhooks/')) return undefined
    const token = await ensureCsrfToken()
    if (token) request.headers.set('X-CSRF-Token', token)
    return request
  },
  onResponse({ response, schemaPath }) {
    // A new session (login/logout/org switch) rotates the token; re-read it on next use.
    if (schemaPath.startsWith('/v1/auth/') || response.status === 401) csrfToken = undefined
    return undefined
  },
}
client.use(csrfMiddleware)

async function toError(res: Response, body?: unknown): Promise<ApiRequestError> {
  let err = body as { code?: string; message?: string } | undefined
  if (err === undefined) {
    try {
      err = (await res.json()) as { code?: string; message?: string }
    } catch {
      err = undefined
    }
  }
  return new ApiRequestError(res.status, err?.code ?? 'http_error', err?.message ?? (res.statusText || `HTTP ${res.status}`))
}

/** Unwrap an openapi-fetch result into data or a thrown ApiRequestError. */
async function unwrap<T>(p: Promise<{ data?: T; error?: unknown; response: Response }>): Promise<T> {
  const { data, error, response } = await p
  if (!response.ok) throw await toError(response, error)
  return data as T
}

export function apiUrl(path: string, query?: Record<string, string | undefined>): string {
  const qs = new URLSearchParams()
  for (const [k, v] of Object.entries(query ?? {})) if (v) qs.set(k, v)
  return `${apiConfig.baseUrl}/v1${path}${qs.size ? `?${qs}` : ''}`
}

export const api = {
  /* auth (spec) */
  getAuthProviders: () => unwrap<AuthProviders>(client.GET('/v1/auth/providers')),
  getMe: async () => {
    const me = await unwrap<Me>(client.GET('/v1/me'))
    csrfToken = me.csrfToken
    return me
  },
  logout: () => unwrap(client.POST('/v1/auth/logout')),
  devLogin: () => unwrap(client.POST('/v1/auth/dev-login')),
  /** Full-page redirect into GitHub OAuth (spec: GET /v1/auth/github/login). `redirectTo` is an SPA path ("/…"). */
  startGithubLogin(redirectTo: string) {
    window.location.assign(apiUrl('/auth/github/login', { redirect_to: redirectTo }))
  },
  /** Full-page redirect into Sign in with Google (spec: GET /v1/auth/google/login). `redirectTo` is an SPA path ("/…"). */
  startGoogleLogin(redirectTo: string) {
    window.location.assign(apiUrl('/auth/google/login', { redirect_to: redirectTo }))
  },

  /* repos & tickets (spec) */
  listRepos: () => unwrap<ListResponse<Repo>>(client.GET('/v1/repos')),
  listTickets: (params: { repoId?: string; stage?: Stage[] } = {}) =>
    unwrap<ListResponse<TicketSummary>>(client.GET('/v1/tickets', { params: { query: { ...params, limit: 500 } } })),
  getTicket: (ticketId: string) => unwrap<TicketDetail>(client.GET('/v1/tickets/{ticketId}', { params: { path: { ticketId } } })),
  listGateResults: (ticketId: string) =>
    unwrap<ListResponse<GateResult>>(client.GET('/v1/tickets/{ticketId}/gate-results', { params: { path: { ticketId } } })),
  postTicketAction: (ticketId: string, body: TicketActionRequest) =>
    unwrap(client.POST('/v1/tickets/{ticketId}/actions', { params: { path: { ticketId } }, body })),
  listRunTurns: (runId: string) => unwrap<ListResponse<AgentTurn>>(client.GET('/v1/runs/{runId}/turns', { params: { path: { runId } } })),
  listPullRequests: (state: PullRequestState = 'open') =>
    unwrap<ListResponse<PullRequest>>(client.GET('/v1/pull-requests', { params: { query: { state } } })),
  eventsUrl: (repoId?: string) => apiUrl('/events', { repoId }),

  /* artifacts, review, PR evidence/review, metrics, factory config (spec) */
  listArtifacts: (ticketId: string) =>
    unwrap<ListResponse<Artifact>>(client.GET('/v1/tickets/{ticketId}/artifacts', { params: { path: { ticketId } } })),
  getReview: (ticketId: string) => unwrap<Review>(client.GET('/v1/tickets/{ticketId}/review', { params: { path: { ticketId } } })),
  getPullRequestEvidence: (pullRequestId: string) =>
    unwrap<PullRequestEvidence>(client.GET('/v1/pull-requests/{pullRequestId}/evidence', { params: { path: { pullRequestId } } })),
  reviewPullRequest: (pullRequestId: string, body: PullRequestReviewRequest) =>
    unwrap<PullRequestEvidence>(client.POST('/v1/pull-requests/{pullRequestId}/review', { params: { path: { pullRequestId } }, body })),
  getMetrics: (window: MetricsWindow, repoId?: string) =>
    unwrap<Metrics>(client.GET('/v1/metrics', { params: { query: { window, repoId } } })),
  getFactoryConfig: (repoId: string) => unwrap<FactoryConfig>(client.GET('/v1/repos/{repoId}/config', { params: { path: { repoId } } })),
}

export type AlpineApi = typeof api
