import { QueryClient } from '@tanstack/react-query'
import { lazy, Suspense } from 'react'
import {
  createRootRouteWithContext,
  createRoute,
  createRouter,
  Outlet,
  redirect,
} from '@tanstack/react-router'
import { ApiRequestError } from '@/api/client'
import { meQuery, reposQuery } from '@/api/queries'
import type { Metrics } from '@/api/types'
import { AppShell } from '@/components/app/app-shell'
import { BoardPage } from '@/pages/board'
import { InboxPage } from '@/pages/inbox'
import { LoginPage } from '@/pages/login'
import { NotFoundPage } from '@/pages/not-found'
import { RepoConfigPage } from '@/pages/repo-config'
import { TicketPage } from '@/pages/ticket'

export interface RouterContext {
  queryClient: QueryClient
}

const rootRoute = createRootRouteWithContext<RouterContext>()({
  component: Outlet,
  notFoundComponent: NotFoundPage,
})

const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/login',
  validateSearch: (s: Record<string, unknown>): { redirect?: string } => ({
    redirect: typeof s.redirect === 'string' ? s.redirect : undefined,
  }),
  component: function LoginRoute() {
    const { redirect } = loginRoute.useSearch()
    return <LoginPage redirectTo={redirect ?? '/board'} />
  },
})

/** Authenticated shell. Any 401 from GET /v1/me sends the user to /login. */
const appRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: 'app',
  beforeLoad: async ({ context, location }) => {
    try {
      const me = await context.queryClient.ensureQueryData(meQuery)
      return { me }
    } catch (err) {
      if (err instanceof ApiRequestError && err.status === 401) {
        throw redirect({ to: '/login', search: { redirect: location.href } })
      }
      throw err
    }
  },
  component: AppShell,
})

const indexRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/',
  beforeLoad: () => {
    throw redirect({ to: '/board' })
  },
})

const boardRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/board',
  validateSearch: (s: Record<string, unknown>): { repo?: string } => ({
    repo: typeof s.repo === 'string' ? s.repo : undefined,
  }),
  component: function BoardRoute() {
    const { repo } = boardRoute.useSearch()
    const navigate = boardRoute.useNavigate()
    return <BoardPage repoId={repo} onRepoChange={(r) => navigate({ search: { repo: r } })} />
  },
})

const ticketRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/tickets/$ticketId',
  component: function TicketRoute() {
    const { ticketId } = ticketRoute.useParams()
    return <TicketPage ticketId={ticketId} />
  },
})

const inboxRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/inbox',
  component: InboxPage,
})

// Metrics pulls in the charting library; load it on demand.
const MetricsPage = lazy(() => import('@/pages/metrics').then((m) => ({ default: m.MetricsPage })))

const WINDOWS: Metrics['window'][] = ['7d', '30d', '90d']
const metricsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/metrics',
  validateSearch: (s: Record<string, unknown>): { window?: Metrics['window']; repo?: string } => ({
    window: WINDOWS.includes(s.window as Metrics['window']) ? (s.window as Metrics['window']) : undefined,
    repo: typeof s.repo === 'string' ? s.repo : undefined,
  }),
  component: function MetricsRoute() {
    const search = metricsRoute.useSearch()
    const navigate = metricsRoute.useNavigate()
    return (
      <Suspense fallback={null}>
        <MetricsPage
          window={search.window ?? '30d'}
          repoId={search.repo}
          onChange={(next) => navigate({ search: (prev) => ({ ...prev, ...next }) })}
        />
      </Suspense>
    )
  },
})

const reposIndexRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/repos',
  beforeLoad: async ({ context }) => {
    const repos = await context.queryClient.ensureQueryData(reposQuery)
    const first = repos.items[0]
    if (first) throw redirect({ to: '/repos/$repoId/config', params: { repoId: first.id } })
  },
  component: () => <p className="text-muted-foreground p-6 text-sm">No repositories installed yet.</p>,
})

const repoConfigRoute = createRoute({
  getParentRoute: () => appRoute,
  path: '/repos/$repoId/config',
  component: function RepoConfigRoute() {
    const { repoId } = repoConfigRoute.useParams()
    return <RepoConfigPage repoId={repoId} />
  },
})

const routeTree = rootRoute.addChildren([
  loginRoute,
  appRoute.addChildren([
    indexRoute,
    boardRoute,
    ticketRoute,
    inboxRoute,
    metricsRoute,
    reposIndexRoute,
    repoConfigRoute,
  ]),
])

export function createAppRouter(queryClient: QueryClient) {
  return createRouter({
    routeTree,
    context: { queryClient },
    defaultPreload: 'intent',
    defaultPreloadStaleTime: 0,
    scrollRestoration: true,
  })
}

declare module '@tanstack/react-router' {
  interface Register {
    router: ReturnType<typeof createAppRouter>
  }
}
