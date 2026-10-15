import { useQuery } from '@tanstack/react-query'
import { Link, useNavigate } from '@tanstack/react-router'
import { Inbox, RotateCw, Settings2 } from 'lucide-react'
import { useMemo, useRef, useState, type KeyboardEvent } from 'react'
import { reposQuery, ticketsQuery } from '@/api/queries'
import { useBoardStream, type FeedItem, type StreamStatus } from '@/api/stream'
import { COLUMN_FOR_STAGE, STAGE_LABEL, type BoardColumn, type Repo, type TicketSummary } from '@/api/types'
import { BrBadge, PriorityBadge, SourceIcon, StageDot } from '@/components/app/badges'
import { EmptyState, ErrorState, PageHeader } from '@/components/app/page-header'
import { Button } from '@/components/ui/button'
import { NativeSelect } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { stageTone } from '@/design/tokens'
import { useHotkeys } from '@/hooks/use-hotkeys'
import { formatUsd, timeAgo } from '@/lib/format'
import { cn } from '@/lib/utils'

/** Spec default for limits.maxTurns; the per-ticket value is on TicketDetail. */
const TURN_CAP = 100

/** Left-to-right flow. Escalated + needs-human are pulled out into the pinned "Needs you" lane. */
const FLOW: Exclude<BoardColumn, 'escalated'>[] = ['triage', 'refinement', 'prioritize', 'implement', 'gates', 'review', 'merge']
type LaneId = 'needs-you' | (typeof FLOW)[number]

const SUB_STAGE: Partial<Record<TicketSummary['stage'], string>> = {
  intake: 'New',
  merge_policy: 'Merge policy',
  pr_open: 'PR open',
  done: 'Merged',
}

const needsYou = (t: TicketSummary) => t.stage === 'escalated' || t.stage === 'needs_human'

export function BoardPage({ repoId, onRepoChange }: { repoId?: string; onRepoChange: (repo?: string) => void }) {
  const tickets = useQuery(ticketsQuery(repoId))
  const repos = useQuery(reposQuery)
  const { status, feed, fresh } = useBoardStream(repoId)
  const navigate = useNavigate()
  const boardRef = useRef<HTMLDivElement>(null)
  const [selected, setSelected] = useState<string | null>(null)
  const [expanded, setExpanded] = useState<ReadonlySet<LaneId>>(new Set())

  const lanes = useMemo(() => {
    const by = Object.fromEntries([['needs-you', []], ...FLOW.map((c) => [c, []])]) as unknown as Record<LaneId, TicketSummary[]>
    for (const t of tickets.data?.items ?? []) {
      if (needsYou(t)) {
        by['needs-you'].push(t)
        continue
      }
      const col = COLUMN_FOR_STAGE[t.stage]
      if (col && col !== 'escalated') by[col].push(t)
    }
    // Escalated first, then needs-human; everything else most recently updated first.
    by['needs-you'].sort((a, b) => (a.stage === b.stage ? b.updatedAt.localeCompare(a.updatedAt) : a.stage === 'escalated' ? -1 : 1))
    for (const c of FLOW) by[c].sort((a, b) => b.updatedAt.localeCompare(a.updatedAt))
    return by
  }, [tickets.data])

  const order: LaneId[] = ['needs-you', ...FLOW]
  const total = order.reduce((n, c) => n + lanes[c].length, 0)
  const escalated = lanes['needs-you'].filter((t) => t.stage === 'escalated').length
  const human = lanes['needs-you'].length - escalated
  const streamStatus: StreamStatus = tickets.error ? 'error' : status

  /* ------------------------------------------------ keyboard: roving tabindex, h/j/k/l */
  const grid = order.map((c) => lanes[c].map((t) => t.id)).filter((col) => col.length > 0)
  const flat = grid.flat()
  const current = selected && flat.includes(selected) ? selected : (flat[0] ?? null)
  const focusTicket = (id: string | undefined) => {
    if (!id) return
    setSelected(id)
    const el = boardRef.current?.querySelector<HTMLElement>(`[data-ticket-id="${id}"]`)
    el?.focus()
    el?.scrollIntoView({ block: 'nearest', inline: 'nearest' })
  }
  const move = (dx: number, dy: number) => {
    if (!current) return
    const ci = grid.findIndex((col) => col.includes(current))
    const ri = grid[ci].indexOf(current)
    if (dy) return focusTicket(grid[ci][Math.max(0, Math.min(grid[ci].length - 1, ri + dy))])
    const next = grid[Math.max(0, Math.min(grid.length - 1, ci + dx))]
    focusTicket(next[Math.min(ri, next.length - 1)])
  }
  const onBoardKey = (e: KeyboardEvent) => {
    if (e.metaKey || e.ctrlKey || e.altKey) return
    const map: Record<string, [number, number]> = {
      j: [0, 1], arrowdown: [0, 1], k: [0, -1], arrowup: [0, -1],
      l: [1, 0], arrowright: [1, 0], h: [-1, 0], arrowleft: [-1, 0],
    }
    const d = map[e.key.toLowerCase()]
    if (!d) return
    e.preventDefault()
    e.stopPropagation()
    move(d[0], d[1])
  }
  // From anywhere on the page, j/k/h/l jump into the board at the remembered card.
  useHotkeys({ j: () => focusTicket(current ?? undefined), k: () => focusTicket(current ?? undefined), h: () => focusTicket(current ?? undefined), l: () => focusTicket(current ?? undefined) })

  const toggleLane = (id: LaneId) =>
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })

  return (
    <div className="flex h-screen min-w-0 flex-col">
      <PageHeader
        title="Pipeline"
        meta={
          tickets.data && total > 0 ? (
            <span className="tnum">
              {total} tickets
              {escalated + human > 0 && (
                <>
                  {' · '}
                  <span className="text-foreground">{escalated + human} need you</span>
                </>
              )}
            </span>
          ) : undefined
        }
        actions={
          <>
            <LiveIndicator status={streamStatus} />
            <RepoFilter repos={repos.data?.items ?? []} value={repoId} onChange={onRepoChange} />
          </>
        }
      />
      {tickets.error ? (
        <ErrorState
          error={tickets.error}
          title="Couldn’t load the pipeline"
          onRetry={() => void tickets.refetch()}
        />
      ) : tickets.data && total === 0 ? (
        <EmptyState
          icon={<Inbox />}
          title="No tickets yet"
          body={
            <>
              Label a GitHub issue <code className="bg-muted rounded-sm px-1 font-mono text-xs">alpine</code> or connect Sentry. New tickets appear here within a minute.
            </>
          }
          action={
            <>
              <Button size="sm" asChild>
                <Link to="/repos">Connect a source</Link>
              </Button>
              <Button size="sm" variant="outline" onClick={() => void navigate({ to: '/repos' })}>
                <Settings2 /> Open factory config
              </Button>
            </>
          }
        />
      ) : (
        <div className="flex min-h-0 flex-1">
          {/* `relative` contains the sr-only text inside badges so it can't widen the document. */}
          <div
            ref={boardRef}
            role="group"
            aria-label="Pipeline board. Use H J K L or arrow keys to move between tickets."
            onKeyDown={onBoardKey}
            className="relative flex min-w-0 flex-1 overflow-x-auto"
          >
            {order.map((lane) => {
              const items = lanes[lane]
              const pinned = lane === 'needs-you'
              const label = pinned ? 'Needs you' : stageTone[lane].label
              const rail = !pinned && tickets.data && items.length === 0 && !expanded.has(lane)
              if (rail) return <Rail key={lane} label={label} dot={stageTone[lane].dot} onExpand={() => toggleLane(lane)} />
              return (
                <section
                  key={lane}
                  aria-labelledby={`col-${lane}`}
                  className={cn(
                    'flex min-w-0 flex-col border-r last:border-r-0',
                    pinned ? 'bg-muted/50 w-52 shrink-0' : 'min-w-[144px] flex-1 basis-0',
                  )}
                >
                  <header className="flex h-10 shrink-0 items-center gap-2 px-3" title={pinned ? 'Escalated or waiting on a human decision' : stageTone[lane].hint}>
                    {pinned ? (
                      <span className="bg-fail inline-block size-2 shrink-0 rounded-full" aria-hidden />
                    ) : (
                      <span className={cn('inline-block size-2 shrink-0 rounded-full', stageTone[lane].dot)} aria-hidden />
                    )}
                    <h2 id={`col-${lane}`} className="truncate text-xs font-semibold">{label}</h2>
                    <span className="tnum text-muted-foreground ml-auto text-xs">{tickets.data ? items.length : ''}</span>
                    {!pinned && items.length === 0 && (
                      <button type="button" onClick={() => toggleLane(lane)} className="text-muted-foreground hover:text-foreground text-xs">
                        Collapse
                      </button>
                    )}
                  </header>
                  <ul className="flex min-h-0 flex-1 flex-col gap-2 overflow-y-auto px-2 pb-3" aria-label={`${label} tickets`}>
                    {tickets.isPending
                      ? Array.from({ length: pinned ? 2 : 3 }, (_, i) => <CardSkeleton key={i} />)
                      : items.map((t) => (
                          <li key={t.id} className="@container">
                            <TicketCard
                              ticket={t}
                              fresh={fresh.has(t.id)}
                              selected={t.id === current}
                              pinned={pinned}
                              onFocus={() => setSelected(t.id)}
                            />
                          </li>
                        ))}
                    {pinned && tickets.data && items.length === 0 && (
                      <li className="text-muted-foreground px-1 py-2 text-xs">Nothing needs you. Escalations and approvals land here.</li>
                    )}
                    {!pinned && tickets.data && items.length === 0 && (
                      <li className="text-muted-foreground rounded-md border border-dashed px-3 py-4 text-center text-xs">Empty</li>
                    )}
                  </ul>
                </section>
              )
            })}
          </div>
          <ActivityFeed feed={feed} tickets={tickets.data?.items ?? []} />
        </div>
      )}
    </div>
  )
}

/** Collapsed empty column: 40px rail with a vertical label. Click to expand. */
function Rail({ label, dot, onExpand }: { label: string; dot: string; onExpand: () => void }) {
  return (
    <section className="flex w-10 shrink-0 flex-col border-r" aria-label={`${label}: no tickets`}>
      <button
        type="button"
        onClick={onExpand}
        title={`${label}: no tickets. Click to expand.`}
        className="hover:bg-accent text-muted-foreground hover:text-foreground flex flex-1 flex-col items-center gap-2 pt-4 transition-colors duration-150"
      >
        <span className={cn('inline-block size-2 shrink-0 rounded-full', dot)} aria-hidden />
        <span className="text-xs font-semibold [writing-mode:vertical-rl]">{label}</span>
        <span className="tnum text-xs">0</span>
      </button>
    </section>
  )
}

function CardSkeleton() {
  return (
    <li className="bg-card space-y-2 rounded-md border p-3">
      <Skeleton className="h-3 w-16" />
      <Skeleton className="h-4 w-full" />
      <Skeleton className="h-4 w-2/3" />
    </li>
  )
}

function TicketCard({
  ticket: t,
  fresh,
  selected,
  pinned,
  onFocus,
}: {
  ticket: TicketSummary
  fresh: boolean
  selected: boolean
  pinned: boolean
  onFocus: () => void
}) {
  const br = t.brFinal ?? t.brPredicted
  const turns = t.turnsUsed
  const capped = turns >= TURN_CAP
  const sub = pinned ? (t.stage === 'escalated' ? 'Escalated' : 'Needs human') : SUB_STAGE[t.stage]
  return (
    <Link
      to="/tickets/$ticketId"
      params={{ ticketId: t.id }}
      data-ticket-id={t.id}
      tabIndex={selected ? 0 : -1}
      onFocus={onFocus}
      className={cn(
        'bg-card hover:bg-accent/60 hover:border-border-strong block rounded-md border p-3 transition-[background-color,border-color] duration-[1200ms,150ms]',
        selected && 'border-border-strong shadow-[inset_2px_0_0_var(--brand)]',
        fresh && 'bg-brand-subtle border-brand/40 duration-150',
        pinned && t.stage === 'escalated' && 'shadow-[inset_2px_0_0_var(--fail)]',
        pinned && t.stage === 'needs_human' && 'shadow-[inset_2px_0_0_var(--warn)]',
      )}
    >
      <div className="text-muted-foreground flex h-4 items-center gap-1 text-xs">
        {t.source && <SourceIcon kind={t.source} className="size-3 shrink-0" />}
        <span className="shrink-0 font-mono" title={t.repoFullName}>{t.key}</span>
        <span className={cn('ml-auto truncate', pinned && (t.stage === 'escalated' ? 'text-fail' : 'text-warn'))}>{sub ?? timeAgo(t.updatedAt)}</span>
      </div>
      <p className="mt-1 line-clamp-2 text-sm">{t.title}</p>
      {pinned && t.escalationReason && <p className="text-muted-foreground mt-1 line-clamp-2 text-xs">{t.escalationReason}</p>}
      <div className="mt-2 flex items-center gap-2">
        {t.priority && <PriorityBadge p={t.priority} />}
        {br ? <BrBadge br={br} /> : null}
        {(turns > 0 || t.costUsd > 0) && (
          <span className="tnum text-muted-foreground ml-auto flex items-center gap-1 text-xs whitespace-nowrap">
            {turns > 0 && (
              <span className={cn('flex items-center gap-0.5', capped && 'text-fail')} title={`${turns} of ${TURN_CAP} turns`}>
                <RotateCw className="size-3" aria-hidden />
                {turns}
                <span className="hidden @[176px]:inline">/{TURN_CAP}</span>
                <span className="sr-only"> turns</span>
              </span>
            )}
            <span className="sr-only @[160px]:not-sr-only">{formatUsd(t.costUsd)}</span>
          </span>
        )}
      </div>
      {turns > 0 && COLUMN_FOR_STAGE[t.stage] !== 'merge' && (
        <div className="bg-muted mt-2 h-0.5 overflow-hidden rounded-full" aria-hidden>
          <div
            className={cn('h-full transition-[width] duration-200', capped ? 'bg-fail' : turns > 70 ? 'bg-warn' : 'bg-brand')}
            style={{ width: `${Math.min(100, (turns / TURN_CAP) * 100)}%` }}
          />
        </div>
      )}
    </Link>
  )
}

function LiveIndicator({ status }: { status: StreamStatus }) {
  const label = status === 'open' ? 'Live' : status === 'connecting' ? 'Connecting' : 'Offline'
  return (
    <span className="text-muted-foreground flex items-center gap-1.5 text-xs" role="status" aria-live="polite">
      <span className={cn('size-2 rounded-full', status === 'open' ? 'bg-ok' : status === 'error' ? 'bg-fail' : 'bg-warn')} aria-hidden />
      {label}
    </span>
  )
}

export function RepoFilter({ repos, value, onChange }: { repos: Repo[]; value?: string; onChange: (v?: string) => void }) {
  return (
    <NativeSelect value={value ?? ''} onChange={(e) => onChange(e.target.value || undefined)} aria-label="Filter by repository" className="w-48">
      <option value="">All repositories</option>
      {repos.map((r) => (
        <option key={r.id} value={r.id}>{r.fullName}</option>
      ))}
    </NativeSelect>
  )
}

function describe(item: FeedItem, ticket?: TicketSummary): string {
  const e = item.event
  if (e.type === 'ticket.created') return `New ticket from ${ticket?.source ?? 'intake'}`
  if (e.type === 'ticket.stage_changed') return `${e.previousStage ? STAGE_LABEL[e.previousStage] : 'New'} → ${STAGE_LABEL[e.stage]}`
  return ticket && ticket.turnsUsed > 0 ? `Turn ${ticket.turnsUsed} · ${formatUsd(ticket.costUsd)}` : `Updated in ${STAGE_LABEL[e.stage].toLowerCase()}`
}

/** Live activity: only on very wide screens (≥1680px), where it doesn't cost board columns. */
function ActivityFeed({ feed, tickets }: { feed: FeedItem[]; tickets: TicketSummary[] }) {
  const byId = new Map(tickets.map((t) => [t.id, t]))
  return (
    <aside className="hidden w-60 shrink-0 flex-col border-l min-[1680px]:flex" aria-label="Live activity">
      <h2 className="flex h-10 items-center px-3 text-xs font-semibold">Activity</h2>
      <ol className="flex-1 overflow-y-auto px-2 pb-3" aria-live="polite">
        {feed.length === 0 && <li className="text-muted-foreground px-2 text-xs">Waiting for the first pipeline event.</li>}
        {feed.map((item) => {
          const t = byId.get(item.event.ticketId)
          return (
            <li key={item.id} className="animate-in fade-in duration-200">
              <Link to="/tickets/$ticketId" params={{ ticketId: item.event.ticketId }} className="hover:bg-accent flex flex-col rounded-md px-2 py-1">
                <span className="flex items-center gap-2 text-xs">
                  <StageDot stage={item.event.stage} />
                  <span className="font-mono">{t?.key ?? item.event.ticketId.slice(0, 8)}</span>
                  <span className="text-muted-foreground ml-auto">{timeAgo(item.event.at)}</span>
                </span>
                <span className="text-muted-foreground truncate text-xs">{describe(item, t)}</span>
              </Link>
            </li>
          )
        })}
      </ol>
    </aside>
  )
}
