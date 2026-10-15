import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, Outlet, useNavigate } from '@tanstack/react-router'
import { ChartColumn, GitPullRequest, LayoutGrid, LogOut, Moon, PanelLeftClose, PanelLeftOpen, Search, Settings2, Sun } from 'lucide-react'
import { useEffect, useState, type ReactNode } from 'react'
import { api } from '@/api/client'
import { apiConfig } from '@/api/config'
import { meQuery, pullRequestsQuery } from '@/api/queries'
import type { Role } from '@/api/types'
import { Button } from '@/components/ui/button'
import { Kbd } from '@/components/ui/kbd'
import { ToastProvider } from '@/components/ui/toast'
import { useTheme } from '@/design/theme'
import { useHotkeys } from '@/hooks/use-hotkeys'
import { sentence } from '@/lib/format'
import { cn } from '@/lib/utils'
import { CommandPaletteProvider, useCommandPalette } from './command-palette'
import { AlpineMark } from './logo'

const ROLE_LABEL: Record<Role, string> = {
  owner: 'Owner',
  admin: 'Admin',
  factory_owner: 'Factory owner',
  member: 'Member',
  viewer: 'Viewer',
}

type NavTo = '/board' | '/inbox' | '/metrics' | '/repos'

const COLLAPSE_KEY = 'alpine.sidebar'
/** Below this width the sidebar defaults to an icon rail so the board's columns fit. */
const RAIL_QUERY = '(max-width: 1599px)'

/** Sidebar mode: the user's explicit choice wins; otherwise follow the viewport width. */
function useSidebarCollapsed() {
  const [choice, setChoice] = useState<'rail' | 'full' | null>(() => {
    const v = localStorage.getItem(COLLAPSE_KEY)
    return v === 'rail' || v === 'full' ? v : null
  })
  const [narrow, setNarrow] = useState(() => window.matchMedia(RAIL_QUERY).matches)
  useEffect(() => {
    const mq = window.matchMedia(RAIL_QUERY)
    const on = () => setNarrow(mq.matches)
    mq.addEventListener('change', on)
    return () => mq.removeEventListener('change', on)
  }, [])
  const collapsed = choice ? choice === 'rail' : narrow
  const toggle = () => {
    const next = collapsed ? 'full' : 'rail'
    localStorage.setItem(COLLAPSE_KEY, next)
    setChoice(next)
  }
  return { collapsed, toggle }
}

function NavItem({ to, icon, label, count, hint, rail }: { to: NavTo; icon: ReactNode; label: string; count?: number; hint: string; rail: boolean }) {
  return (
    <Link
      to={to}
      aria-label={rail ? `${label}${count ? `, ${count} open` : ''}` : undefined}
      title={`${label} (${hint})`}
      className={cn(
        'text-muted-foreground hover:bg-accent hover:text-foreground data-[status=active]:bg-accent data-[status=active]:text-foreground relative flex h-8 items-center gap-2 rounded-md text-sm transition-colors duration-150 [&_svg]:size-4',
        rail ? 'w-8 justify-center' : 'px-2',
      )}
    >
      {icon}
      {!rail && <span className="flex-1 truncate">{label}</span>}
      {count !== undefined && count > 0 && (
        rail ? (
          <span className="bg-brand absolute top-1 right-1 size-1.5 rounded-full" aria-hidden />
        ) : (
          <span className="tnum text-muted-foreground text-xs" aria-label={`${count} open`}>{count}</span>
        )
      )}
    </Link>
  )
}

function SearchButton({ rail }: { rail: boolean }) {
  const palette = useCommandPalette()
  if (rail) {
    return (
      <Button variant="ghost" size="icon" onClick={palette.open} aria-label="Open command palette" title="Search (⌘K)">
        <Search />
      </Button>
    )
  }
  return (
    <button
      type="button"
      onClick={palette.open}
      className="border-input bg-card text-muted-foreground hover:bg-accent mx-2 flex h-8 items-center gap-2 rounded-md border px-2 text-sm transition-colors duration-150"
      aria-label="Open command palette"
    >
      <Search className="size-4" />
      <span className="flex-1 text-left">Search</span>
      <Kbd>⌘K</Kbd>
    </button>
  )
}

export function AppShell() {
  const { data: me } = useQuery(meQuery)
  const { data: prs } = useQuery(pullRequestsQuery('open'))
  const qc = useQueryClient()
  const navigate = useNavigate()
  const theme = useTheme()
  const sidebar = useSidebarCollapsed()
  const rail = sidebar.collapsed
  useHotkeys({ '[': sidebar.toggle })

  const signOut = async () => {
    await api.logout()
    qc.clear()
    await navigate({ to: '/login', search: { redirect: undefined } })
  }

  const displayName = me?.user.name || me?.user.githubLogin || ''
  const initials = displayName.split(/\s+/).map((p) => p[0]).join('').slice(0, 2).toUpperCase()

  return (
    <ToastProvider>
      <CommandPaletteProvider>
        <a href="#main" className="bg-primary text-primary-foreground sr-only z-50 rounded-md px-3 py-2 focus:not-sr-only focus:fixed focus:top-2 focus:left-2">
          Skip to content
        </a>
        <div className="flex min-h-screen">
          <aside
            className={cn('bg-muted/40 sticky top-0 flex h-screen shrink-0 flex-col border-r transition-[width] duration-150', rail ? 'w-14 items-center' : 'w-56')}
            aria-label="Primary"
          >
            <div className={cn('flex h-12 items-center gap-2', rail ? 'justify-center' : 'px-3')}>
              <AlpineMark />
              {!rail && (
                <div className="min-w-0 leading-tight">
                  <div className="truncate text-sm font-semibold">{me?.activeOrg?.name ?? 'Alpine'}</div>
                  {me?.activeOrg && <div className="text-muted-foreground text-xs">{sentence(me.activeOrg.plan)} plan</div>}
                </div>
              )}
            </div>
            <SearchButton rail={rail} />
            <nav className={cn('mt-3 flex flex-col gap-1', rail ? 'items-center' : 'px-2')} aria-label="Main">
              <NavItem rail={rail} to="/board" icon={<LayoutGrid />} label="Pipeline" hint="G then B" />
              <NavItem rail={rail} to="/inbox" icon={<GitPullRequest />} label="PR inbox" count={prs?.items.length} hint="G then I" />
              <NavItem rail={rail} to="/metrics" icon={<ChartColumn />} label="Metrics" hint="G then M" />
              <NavItem rail={rail} to="/repos" icon={<Settings2 />} label="Factory config" hint="G then C" />
            </nav>
            <div className={cn('mt-auto flex flex-col gap-2 p-2', rail && 'items-center')}>
              {apiConfig.mode === 'mock' && !rail && (
                <p className="text-muted-foreground px-2 text-xs">Mock API · simulated data</p>
              )}
              <div className={cn('flex gap-1 border-t pt-2', rail ? 'flex-col items-center' : 'items-center')}>
                <div
                  className="bg-secondary text-secondary-foreground flex size-8 shrink-0 items-center justify-center rounded-full text-xs font-medium"
                  title={rail ? `${displayName} · ${me?.role ? ROLE_LABEL[me.role] : ''}` : undefined}
                  aria-hidden
                >
                  {initials}
                </div>
                {!rail && (
                  <div className="ml-1 min-w-0 flex-1 leading-tight">
                    <div className="truncate text-sm">{displayName}</div>
                    <div className="text-muted-foreground truncate text-xs">{me?.role ? ROLE_LABEL[me.role] : null}</div>
                  </div>
                )}
                <Button variant="ghost" size="icon" onClick={theme.toggle} aria-label={`Switch to ${theme.resolved === 'dark' ? 'light' : 'dark'} theme`}>
                  {theme.resolved === 'dark' ? <Sun /> : <Moon />}
                </Button>
                <Button variant="ghost" size="icon" onClick={signOut} aria-label="Sign out">
                  <LogOut />
                </Button>
                <Button variant="ghost" size="icon" onClick={sidebar.toggle} aria-label={rail ? 'Expand sidebar' : 'Collapse sidebar'} title={`${rail ? 'Expand' : 'Collapse'} sidebar ([)`}>
                  {rail ? <PanelLeftOpen /> : <PanelLeftClose />}
                </Button>
              </div>
            </div>
          </aside>
          <main id="main" className="flex min-w-0 flex-1 flex-col">
            <Outlet />
          </main>
        </div>
      </CommandPaletteProvider>
    </ToastProvider>
  )
}
