import { queryOptions } from '@tanstack/react-query'
import { ApiRequestError, api } from './client'
import type { MetricsWindow, PullRequestState } from './types'

export const queryKeys = {
  me: ['me'] as const,
  authProviders: ['auth-providers'] as const,
  repos: ['repos'] as const,
  ticketsAll: ['tickets'] as const,
  tickets: (repoId?: string) => ['tickets', { repoId: repoId ?? null }] as const,
  ticket: (id: string) => ['ticket', id] as const,
  gateResults: (id: string) => ['ticket', id, 'gate-results'] as const,
  artifacts: (id: string) => ['ticket', id, 'artifacts'] as const,
  review: (id: string) => ['ticket', id, 'review'] as const,
  runTurns: (runId: string) => ['run', runId, 'turns'] as const,
  pullRequestsAll: ['pull-requests'] as const,
  pullRequests: (state: PullRequestState) => ['pull-requests', state] as const,
  evidence: (id: string) => ['pr-evidence', id] as const,
  metrics: (window: MetricsWindow, repoId?: string) => ['metrics', window, repoId ?? null] as const,
  factoryConfig: (repoId: string) => ['factory-config', repoId] as const,
}

/** 404 on an optional/proposed resource means "nothing yet", not an error. */
const orNull = async <T,>(p: Promise<T>): Promise<T | null> => {
  try {
    return await p
  } catch (e) {
    if (e instanceof ApiRequestError && e.status === 404) return null
    throw e
  }
}

export const meQuery = queryOptions({ queryKey: queryKeys.me, queryFn: api.getMe, retry: false, staleTime: 5 * 60_000 })
export const authProvidersQuery = queryOptions({ queryKey: queryKeys.authProviders, queryFn: api.getAuthProviders, staleTime: Infinity })
export const reposQuery = queryOptions({ queryKey: queryKeys.repos, queryFn: api.listRepos, staleTime: 5 * 60_000 })

export const ticketsQuery = (repoId?: string) =>
  queryOptions({ queryKey: queryKeys.tickets(repoId), queryFn: () => api.listTickets({ repoId }) })
export const ticketQuery = (id: string) => queryOptions({ queryKey: queryKeys.ticket(id), queryFn: () => api.getTicket(id) })
export const gateResultsQuery = (id: string) =>
  queryOptions({ queryKey: queryKeys.gateResults(id), queryFn: () => api.listGateResults(id) })
export const artifactsQuery = (id: string) =>
  queryOptions({ queryKey: queryKeys.artifacts(id), queryFn: () => orNull(api.listArtifacts(id)) })
export const reviewQuery = (id: string) => queryOptions({ queryKey: queryKeys.review(id), queryFn: () => orNull(api.getReview(id)) })

export const runTurnsQuery = (runId: string) =>
  queryOptions({ queryKey: queryKeys.runTurns(runId), queryFn: () => api.listRunTurns(runId) })

export const pullRequestsQuery = (state: PullRequestState = 'open') =>
  queryOptions({ queryKey: queryKeys.pullRequests(state), queryFn: () => api.listPullRequests(state) })
export const evidenceQuery = (id: string) =>
  queryOptions({ queryKey: queryKeys.evidence(id), queryFn: () => orNull(api.getPullRequestEvidence(id)) })

export const metricsQuery = (window: MetricsWindow, repoId?: string) =>
  queryOptions({ queryKey: queryKeys.metrics(window, repoId), queryFn: () => api.getMetrics(window, repoId) })
export const factoryConfigQuery = (repoId: string) =>
  queryOptions({ queryKey: queryKeys.factoryConfig(repoId), queryFn: () => api.getFactoryConfig(repoId) })
