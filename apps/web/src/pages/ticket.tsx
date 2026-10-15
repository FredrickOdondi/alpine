import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { ArrowUpRight, ChevronLeft, ChevronRight, CircleAlert, Download, GitBranch, GitPullRequest, Play, X } from 'lucide-react'
import { useMemo, useState } from 'react'
import { ApiRequestError } from '@/api/client'
import { useTicketAction } from '@/api/mutations'
import { artifactsQuery, gateResultsQuery, reviewQuery, runTurnsQuery, ticketQuery } from '@/api/queries'
import {
  COLUMN_FOR_STAGE,
  STAGE_LABEL,
  type AgentRun,
  type Artifact,
  type GateResult,
  type PullRequest,
  type Review,
  type StageTransition,
  type TicketAction,
  type TicketDetail,
} from '@/api/types'
import { BrBadge, GateStatusIcon, PriorityBadge, SourceIcon, StageDot } from '@/components/app/badges'
import { ErrorState, InlineError, PageHeader } from '@/components/app/page-header'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogTitle } from '@/components/ui/dialog'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { stageTone } from '@/design/tokens'
import { useHotkeys } from '@/hooks/use-hotkeys'
import { formatDateTime, formatDuration, formatUsd, sentence } from '@/lib/format'
import { cn } from '@/lib/utils'
import { NotFoundPage } from './not-found'

const SOURCE_LABEL: Record<string, string> = { github: 'GitHub issue', sentry: 'Sentry issue', slack: 'Slack thread', linear: 'Linear issue', jira: 'Jira issue', widget: 'Bug widget' }

/** Split the refined ticket body into the problem statement and its acceptance-criteria list. */
function parseBody(body: string): { problem: string[]; criteria: string[] } {
  const [problemPart, acPart = ''] = body.split(/^##\s*Acceptance criteria\s*$/im)
  const problem = problemPart.split(/\n{2,}/).map((p) => p.trim()).filter(Boolean)
  const criteria = acPart.split('\n').map((l) => l.replace(/^\s*(?:[-*]|\d+\.)\s+/, '').trim()).filter(Boolean)
  return { problem, criteria }
}

/** "agent:refine" → "Refine agent", "user:tess" → "@tess", "system" → "System". */
function formatActor(actor: string): string {
  const [kind, name] = actor.split(':')
  if (kind === 'agent' && name) return `${name[0].toUpperCase()}${name.slice(1)} agent`
  if (kind === 'user' && name) return `@${name}`
  return 'System'
}

/** Render `code` spans in plain text bodies. */
function InlineCode({ text }: { text: string }) {
  return (
    <>
      {text.split(/(`[^`]+`)/).map((part, i) =>
        part.startsWith('`') && part.endsWith('`') ? (
          <code key={i} className="bg-muted rounded-sm px-1 font-mono text-xs">{part.slice(1, -1)}</code>
        ) : (
          part
        ),
      )}
    </>
  )
}

export function TicketPage({ ticketId }: { ticketId: string }) {
  const q = useQuery(ticketQuery(ticketId))
  const gates = useQuery(gateResultsQuery(ticketId))
  const artifacts = useQuery(artifactsQuery(ticketId))
  const review = useQuery(reviewQuery(ticketId))
  const t = q.data
  const pr = t?.pullRequests[0]
  const body = useMemo(() => (t ? parseBody(t.body) : null), [t])

  useHotkeys({ escape: () => history.back() })

  if (q.error instanceof ApiRequestError && q.error.status === 404) return <NotFoundPage />

  const column = t ? COLUMN_FOR_STAGE[t.stage] : null

  return (
    <div className="flex min-h-screen flex-col">
      <PageHeader
        heading={false}
        title={
          <nav aria-label="Breadcrumb" className="flex items-center gap-1">
            <Link to="/board" className="text-muted-foreground hover:text-foreground">Pipeline</Link>
            <ChevronRight className="text-muted-foreground size-3.5" aria-hidden />
            <span className="font-mono" aria-current="page">{t?.key ?? '…'}</span>
            {t && <span className="text-muted-foreground ml-1">· {t.repoFullName}</span>}
          </nav>
        }
        actions={
          pr && (
            <Button variant="outline" size="sm" asChild>
              <a href={pr.url} target="_blank" rel="noreferrer">
                <GitPullRequest /> PR #{pr.number}
              </a>
            </Button>
          )
        }
      />
      {q.error ? (
        <ErrorState error={q.error} onRetry={() => void q.refetch()} />
      ) : !t || !body ? (
        <TicketSkeleton />
      ) : (
        <div className="grid flex-1 lg:grid-cols-[minmax(0,1fr)_320px]">
          <article className="min-w-0 space-y-8 px-6 py-5 lg:border-r">
            <header className="space-y-3">
              <h1 className="text-xl font-semibold tracking-tight">{t.title}</h1>
              <div className="flex flex-wrap items-center gap-x-4 gap-y-2 text-xs">
                <span className="flex items-center gap-1.5">
                  <StageDot stage={t.stage} />
                  {column ? stageTone[column].label : STAGE_LABEL[t.stage]}
                  {column && stageTone[column].label !== STAGE_LABEL[t.stage] && <span className="text-muted-foreground">· {STAGE_LABEL[t.stage]}</span>}
                </span>
                {t.priority && <PriorityBadge p={t.priority} />}
                {t.size && <span className="text-muted-foreground" title="Size estimate">Size <span className="text-foreground font-mono">{t.size}</span></span>}
                <BrBadge br={t.brFinal ?? t.brPredicted} withLabel />
                {t.confidence != null && t.confidence < 0.9 && (
                  <span className="text-muted-foreground" title="Refinement agent’s confidence in its blast-radius estimate">
                    BR confidence <span className="tnum text-foreground">{Math.round(t.confidence * 100)}%</span>
                  </span>
                )}
              </div>
            </header>

            {(t.stage === 'needs_human' || t.stage === 'escalated') && <HumanActionCallout ticket={t} />}

            <Section title="Problem">
              <div className="max-w-prose space-y-3 leading-6">
                {body.problem.map((p) => <p key={p}><InlineCode text={p} /></p>)}
              </div>
            </Section>

            {body.criteria.length > 0 && (
              <Section title="Acceptance criteria">
                <ol className="max-w-prose space-y-1.5">
                  {body.criteria.map((ac, i) => (
                    <li key={ac} className="flex gap-3">
                      <span className="tnum text-muted-foreground w-4 shrink-0 text-right text-xs leading-5">{i + 1}</span>
                      <span><InlineCode text={ac} /></span>
                    </li>
                  ))}
                </ol>
              </Section>
            )}

            <Section title="Evidence" count={artifacts.data?.items.length}>
              {artifacts.isPending ? <Skeleton className="h-40 w-full" /> : <Evidence artifacts={artifacts.data?.items ?? []} />}
            </Section>

            <Section title="Gate results" count={t.gateIterations || undefined}>
              {gates.isPending ? (
                <Skeleton className="h-48 w-full" />
              ) : gates.error ? (
                <InlineError label="Couldn’t load gate results." onRetry={() => void gates.refetch()} />
              ) : (
                <Gates results={gates.data.items} running={t.stage === 'gates'} />
              )}
            </Section>

            {review.data && <ReviewSection review={review.data} />}
          </article>

          <aside className="space-y-6 px-5 py-5" aria-label="Ticket details">
            <Budget ticket={t} />
            <Runs runs={t.runs} />
            {pr && <LinkedPr pr={pr} />}
            <Sources ticket={t} />
            <History history={t.stageHistory} />
          </aside>
        </div>
      )}
    </div>
  )
}

function Section({ title, count, children }: { title: string; count?: number; children: React.ReactNode }) {
  const id = `section-${title.toLowerCase().replace(/[^a-z]+/g, '-')}`
  return (
    <section className="space-y-3" aria-labelledby={id}>
      <h2 id={id} className="flex items-center gap-2 text-sm font-semibold">
        {title}
        {count !== undefined && <span className="tnum text-muted-foreground font-normal">{count}</span>}
      </h2>
      {children}
    </section>
  )
}

const ACTIONS: Record<'needs_human' | 'escalated', { primary: [TicketAction, string]; secondary: [TicketAction, string] }> = {
  needs_human: { primary: ['approve', 'Approve for agent'], secondary: ['reject', 'Reject'] },
  escalated: { primary: ['resume', 'Resume with added context'], secondary: ['close', 'Close ticket'] },
}

function HumanActionCallout({ ticket: t }: { ticket: TicketDetail }) {
  const action = useTicketAction(t.id)
  const [note, setNote] = useState('')
  const escalated = t.stage === 'escalated'
  const { primary, secondary } = ACTIONS[escalated ? 'escalated' : 'needs_human']
  const run = (a: TicketAction) => action.mutate({ action: a, note: note.trim() || undefined })
  return (
    <div role="region" aria-label="Needs a human decision" className={cn('rounded-md border px-4 py-3', escalated ? 'border-fail/30 bg-fail-subtle' : 'bg-muted')}>
      <div className="flex items-start gap-3">
        <CircleAlert className={cn('mt-0.5 size-4 shrink-0', escalated ? 'text-fail' : 'text-muted-foreground')} aria-hidden />
        <div className="min-w-0 flex-1 space-y-2">
          <p className="font-medium">{escalated ? 'Escalated to a human' : 'Needs a human decision'}</p>
          {t.escalationReason && <p className="text-muted-foreground text-xs">{t.escalationReason}</p>}
          <label className="block">
            <span className="sr-only">Note for the agent</span>
            <textarea
              value={note}
              onChange={(e) => setNote(e.target.value)}
              rows={2}
              placeholder={escalated ? 'Context for the next attempt, e.g. which seed data the e2e test needs' : 'Optional note for the agent'}
              className="border-input bg-card placeholder:text-muted-foreground block w-full max-w-prose resize-y rounded-md border px-2.5 py-1.5 text-xs"
            />
          </label>
          <div className="flex flex-wrap gap-2">
            <Button size="sm" onClick={() => run(primary[0])} disabled={action.isPending}>{primary[1]}</Button>
            <Button size="sm" variant="outline" onClick={() => run(secondary[0])} disabled={action.isPending}>{secondary[1]}</Button>
          </div>
          {action.error && <p role="alert" className="text-fail text-xs">{action.error.message}. Your change was rolled back.</p>}
        </div>
      </div>
    </div>
  )
}

function ReviewSection({ review }: { review: Review }) {
  return (
    <Section title="Independent review">
      <div className="space-y-2">
        <div className="flex items-center gap-2 text-xs">
          <Badge variant={review.verdict === 'approve' ? 'ok' : review.verdict === 'escalate' ? 'fail' : 'warn'}>
            {{ approve: 'Approved', request_changes: 'Changes requested', escalate: 'Escalated' }[review.verdict]}
          </Badge>
          <span className="text-muted-foreground">by {review.model} · fresh context, no implementer transcript</span>
        </div>
        <ul className="divide-y rounded-md border">
          {review.findings.map((f) => (
            <li key={f.message} className="flex gap-3 px-3 py-2">
              <Badge variant={f.severity === 'blocker' || f.severity === 'major' ? 'fail' : f.severity === 'minor' ? 'warn' : 'neutral'} className="mt-px">{sentence(f.severity)}</Badge>
              <div className="min-w-0">
                <p>{f.message}</p>
                <p className="text-muted-foreground mt-0.5 text-xs">{f.category}{f.file && <> · <span className="font-mono">{f.file}</span></>}</p>
              </div>
            </li>
          ))}
        </ul>
      </div>
    </Section>
  )
}

const formatBytes = (n: number) => (n >= 1_000_000 ? `${(n / 1_000_000).toFixed(1)} MB` : `${Math.max(1, Math.round(n / 1000))} KB`)

function Evidence({ artifacts }: { artifacts: Artifact[] }) {
  const video = artifacts.find((a) => a.kind === 'video')
  const shots = artifacts.filter((a) => a.kind === 'screenshot' || a.kind === 'visual_diff')
  const files = artifacts.filter((a) => a.kind === 'trace' || a.kind === 'log')
  const [lightbox, setLightbox] = useState<number | null>(null)
  if (artifacts.length === 0) {
    return <p className="text-muted-foreground text-xs">The refinement agent attaches a repro video, screenshots and a trace once it reproduces the bug.</p>
  }
  return (
    <div className="space-y-3">
      {(video || shots.length > 0) && (
        <div className="grid gap-4 xl:grid-cols-[minmax(0,3fr)_minmax(0,2fr)]">
          {video && (
            <figure className="space-y-2">
              <video
                controls
                preload="none"
                poster={video.posterUrl ?? undefined}
                className="bg-muted aspect-[16/10] w-full rounded-md border"
                aria-label={`Repro recording, ${video.label}`}
              >
                <source src={video.url} type={video.contentType} />
              </video>
              <figcaption className="text-muted-foreground flex items-center gap-2 text-xs">
                <Play className="size-3 shrink-0" aria-hidden />
                <span className="truncate">
                  <span className="font-mono">{video.label}</span> · recorded {formatDateTime(video.createdAt)}
                </span>
              </figcaption>
            </figure>
          )}
          <div className="grid grid-cols-2 content-start gap-3">
            {shots.map((s, i) => (
              <figure key={s.id} className="space-y-1">
                <button
                  type="button"
                  onClick={() => setLightbox(i)}
                  className="hover:border-border-strong block w-full overflow-hidden rounded-md border transition-colors duration-150"
                  aria-label={`Open screenshot: ${s.label}`}
                >
                  <img src={s.url} alt="" className="aspect-[16/10] w-full object-cover" loading="lazy" />
                </button>
                <figcaption className="text-muted-foreground text-xs break-words">{s.label}</figcaption>
              </figure>
            ))}
          </div>
        </div>
      )}
      {files.length > 0 && (
        <ul className="flex flex-wrap gap-2">
          {files.map((f) => (
            <li key={f.id}>
              <Button variant="outline" size="sm" asChild>
                <a href={f.url} download>
                  <Download />
                  {f.label}
                  {f.sizeBytes ? <span className="tnum text-muted-foreground font-normal">{formatBytes(f.sizeBytes)}</span> : null}
                </a>
              </Button>
            </li>
          ))}
        </ul>
      )}
      <Lightbox shots={shots} index={lightbox} onIndex={setLightbox} />
    </div>
  )
}

/** Screenshot lightbox: Esc closes, ←/→ cycle. */
function Lightbox({ shots, index, onIndex }: { shots: Artifact[]; index: number | null; onIndex: (i: number | null) => void }) {
  const open = index !== null && shots[index] !== undefined
  const shot = open ? shots[index] : null
  const step = (d: number) => index !== null && onIndex((index + d + shots.length) % shots.length)
  return (
    <Dialog open={open} onOpenChange={(o) => !o && onIndex(null)}>
      <DialogContent
        className="top-[8%] max-w-4xl"
        onKeyDown={(e) => {
          if (e.key === 'ArrowRight') step(1)
          if (e.key === 'ArrowLeft') step(-1)
        }}
      >
        {shot && (
          <figure>
            <div className="flex h-12 items-center gap-2 border-b px-4">
              <DialogTitle className="min-w-0 flex-1 truncate text-sm font-semibold">{shot.label}</DialogTitle>
              <DialogDescription className="sr-only">Screenshot {index! + 1} of {shots.length}. Use the left and right arrow keys to switch.</DialogDescription>
              <span className="tnum text-muted-foreground text-xs">{index! + 1} / {shots.length}</span>
              <Button variant="ghost" size="icon-sm" onClick={() => step(-1)} aria-label="Previous screenshot"><ChevronLeft /></Button>
              <Button variant="ghost" size="icon-sm" onClick={() => step(1)} aria-label="Next screenshot"><ChevronRight /></Button>
              <DialogClose asChild>
                <Button variant="ghost" size="icon-sm" aria-label="Close"><X /></Button>
              </DialogClose>
            </div>
            <img src={shot.url} alt={shot.label} className="bg-muted block w-full" />
          </figure>
        )}
      </DialogContent>
    </Dialog>
  )
}

function Gates({ results, running }: { results: GateResult[]; running: boolean }) {
  const iterations = useMemo(() => [...new Set(results.map((r) => r.iteration))].sort((a, b) => a - b), [results])
  const [selected, setSelected] = useState<number | null>(null)
  const [open, setOpen] = useState<ReadonlySet<string>>(new Set())
  const current = selected ?? iterations.at(-1)
  if (results.length === 0) {
    return <p className="text-muted-foreground text-xs">Gates run after the implementer pushes its first commit. Config is read from the base branch.</p>
  }
  const rows = results.filter((r) => r.iteration === current)
  const latest = current === iterations.at(-1)
  const toggle = (id: string) =>
    setOpen((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  return (
    <div className="space-y-2">
      {iterations.length > 1 && (
        <div className="flex items-center gap-1" role="group" aria-label="Gate iteration">
          <span className="text-muted-foreground mr-1 text-xs">Iteration</span>
          {iterations.map((it) => {
            const failed = results.some((r) => r.iteration === it && (r.status === 'fail' || r.status === 'error'))
            return (
              <button
                key={it}
                type="button"
                onClick={() => setSelected(it)}
                aria-pressed={it === current}
                aria-label={`Iteration ${it}, ${failed ? 'failed' : 'passed'}`}
                className={cn(
                  'tnum flex h-7 items-center gap-1.5 rounded-md border px-2 text-xs transition-colors duration-150',
                  it === current ? 'bg-card border-input' : 'text-muted-foreground hover:bg-accent border-transparent',
                )}
              >
                <span className={cn('size-1.5 rounded-full', failed ? 'bg-fail' : 'bg-ok')} aria-hidden />
                {it}
              </button>
            )
          })}
        </div>
      )}
      <div className="rounded-md border">
        <Table className="table-fixed">
          <colgroup>
            <col className="w-10" />
            <col className="w-28" />
            <col />
            <col className="w-20" />
          </colgroup>
          <TableHeader>
            <TableRow>
              <TableHead><span className="sr-only">Status</span></TableHead>
              <TableHead>Gate</TableHead>
              <TableHead>Findings</TableHead>
              <TableHead className="text-right">Duration</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((g) => {
              const expanded = open.has(g.id)
              const shown = expanded ? g.findings : g.findings.slice(0, 1)
              return (
                <TableRow key={g.id} className="align-top">
                  <TableCell className="py-2 align-top"><GateStatusIcon status={g.status} /></TableCell>
                  <TableCell className="py-2 align-top font-mono text-xs leading-5">{g.gateId}</TableCell>
                  <TableCell className="py-2 align-top text-xs leading-5">
                    {g.findings.length === 0 ? (
                      <span className="text-muted-foreground">{g.status === 'skipped' ? 'Not applicable to this repo' : 'No findings'}</span>
                    ) : (
                      <ul className="space-y-2">
                        {shown.map((f, i) => (
                          <li key={i} className="space-y-0.5">
                            <p className={cn(!expanded && 'line-clamp-2')}>
                              {f.file && (
                                <span className="text-muted-foreground font-mono">
                                  {f.file}
                                  {f.line ? `:${f.line}` : ''}
                                </span>
                              )}{' '}
                              {f.message}
                            </p>
                            {expanded && (f.rule || f.suggestion) && (
                              <p className="text-muted-foreground">
                                {f.rule && <span className="font-mono">{f.rule}</span>}
                                {f.rule && f.suggestion && ' · '}
                                {f.suggestion}
                              </p>
                            )}
                          </li>
                        ))}
                      </ul>
                    )}
                    {(g.findings.length > 1 || g.findings.some((f) => f.rule || f.suggestion)) && (
                      <button type="button" onClick={() => toggle(g.id)} aria-expanded={expanded} className="text-brand mt-1 hover:underline">
                        {expanded ? 'Show less' : g.findings.length > 1 ? `Show all ${g.findings.length} findings` : 'Show rule and suggestion'}
                      </button>
                    )}
                  </TableCell>
                  <TableCell className="text-muted-foreground py-2 text-right align-top text-xs leading-5">{formatDuration(g.durationMs)}</TableCell>
                </TableRow>
              )
            })}
            {running && latest && (
              <TableRow>
                <TableCell><GateStatusIcon status="running" /></TableCell>
                <TableCell className="text-muted-foreground text-xs" colSpan={3}>Remaining gates running…</TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </div>
    </div>
  )
}

function Meter({ value, max, label }: { value: number; max: number; label: string }) {
  const pct = Math.min(100, (value / max) * 100)
  return (
    <div className="h-1 overflow-hidden rounded-full bg-muted" role="meter" aria-valuenow={value} aria-valuemin={0} aria-valuemax={max} aria-label={label}>
      <div className={cn('h-full', pct >= 100 ? 'bg-fail' : pct > 70 ? 'bg-warn' : 'bg-brand')} style={{ width: `${pct}%` }} />
    </div>
  )
}

function Budget({ ticket: t }: { ticket: TicketDetail }) {
  const rows = [
    { label: 'Turns', value: `${t.turnsUsed} / ${t.limits.maxTurns}`, used: t.turnsUsed, max: t.limits.maxTurns },
    { label: 'Cost', value: `${formatUsd(t.costUsd)} / ${formatUsd(t.limits.maxCostUsd)}`, used: t.costUsd, max: t.limits.maxCostUsd },
    { label: 'Gate iterations', value: `${t.gateIterations} / ${t.limits.maxGateIterations}`, used: t.gateIterations, max: t.limits.maxGateIterations },
  ]
  return (
    <div className="space-y-3">
      <SideTitle>Budget</SideTitle>
      <dl className="tnum space-y-3 text-xs">
        {rows.map((r) => (
          <div key={r.label} className="grid grid-cols-[1fr_auto] gap-x-2 gap-y-2">
            <dt className="text-muted-foreground">{r.label}</dt>
            <dd>{r.value}</dd>
            <dd className="col-span-2">
              <Meter value={r.used} max={r.max} label={`${r.label} used`} />
            </dd>
          </div>
        ))}
      </dl>
    </div>
  )
}

const RUN_STATUS_VARIANT = { succeeded: 'ok', running: 'info', failed: 'fail', escalated: 'fail', cancelled: 'neutral' } as const

function Runs({ runs }: { runs: AgentRun[] }) {
  const [open, setOpen] = useState<string | null>(null)
  return (
    <div className="space-y-2">
      <SideTitle>Agent runs</SideTitle>
      {runs.length === 0 ? (
        <p className="text-muted-foreground text-xs">Triage starts within a minute of intake.</p>
      ) : (
        <ul className="divide-y rounded-md border">
          {runs.map((r) => (
            <li key={r.id}>
              <button
                type="button"
                onClick={() => setOpen((o) => (o === r.id ? null : r.id))}
                aria-expanded={open === r.id}
                className="hover:bg-accent/60 w-full space-y-1 px-3 py-2 text-left transition-colors duration-150"
              >
                <div className="flex items-center gap-2">
                  <ChevronRight className={cn('text-muted-foreground size-3.5 transition-transform duration-150', open === r.id && 'rotate-90')} aria-hidden />
                  <span className="font-medium">{sentence(r.kind)}</span>
                  <Badge variant={RUN_STATUS_VARIANT[r.status]}>{sentence(r.status)}</Badge>
                  <span className="tnum text-muted-foreground ml-auto text-xs">{formatUsd(r.costUsd)}</span>
                </div>
                <div className="tnum text-muted-foreground flex gap-3 pl-6 text-xs">
                  <span>{r.turns} turns</span>
                  {r.gateIterations > 0 && <span>{r.gateIterations} gate runs</span>}
                  {r.endedAt && <span>{formatDuration(new Date(r.endedAt).getTime() - new Date(r.startedAt).getTime())}</span>}
                  {r.model && <span className="truncate font-mono">{r.model}</span>}
                </div>
              </button>
              {open === r.id && <RunTurns runId={r.id} />}
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

function RunTurns({ runId }: { runId: string }) {
  const turns = useQuery(runTurnsQuery(runId))
  if (turns.isPending) return <div className="space-y-1 px-3 pb-2"><Skeleton className="h-3 w-full" /><Skeleton className="h-3 w-2/3" /></div>
  if (turns.error) return <p className="text-fail px-3 pb-2 text-xs">Couldn't load turns.</p>
  const items = turns.data.items
  return (
    <ol className="max-h-56 overflow-y-auto px-3 pb-2" aria-label="Turns">
      {items.map((turn) => (
        <li key={turn.id} className="tnum text-muted-foreground flex items-center gap-2 py-0.5 text-xs">
          <span className="w-6 shrink-0 text-right">{turn.n}</span>
          <span className="text-foreground shrink-0 font-mono">{turn.toolCalls[0]?.name ?? 'reply'}</span>
          <span className="min-w-0 flex-1 truncate font-mono" title={turn.toolCalls[0]?.summary}>{turn.toolCalls[0]?.summary}</span>
          <span className="shrink-0">{Math.round((turn.tokensIn + turn.tokensOut) / 1000)}k</span>
        </li>
      ))}
      {items.length === 0 && <li className="text-muted-foreground py-1 text-xs">No turns recorded yet.</li>}
    </ol>
  )
}

function LinkedPr({ pr }: { pr: PullRequest }) {
  return (
    <div className="space-y-2">
      <SideTitle>Pull request</SideTitle>
      <a href={pr.url} target="_blank" rel="noreferrer" className="hover:border-border-strong block space-y-1.5 rounded-md border px-3 py-2 transition-colors duration-150">
        <div className="flex items-center gap-2">
          <GitPullRequest className={cn('size-4', pr.state === 'merged' ? 'text-muted-foreground' : 'text-ok')} aria-hidden />
          <span className="font-medium">#{pr.number}</span>
          <Badge variant={pr.state === 'merged' ? 'neutral' : 'ok'}>{sentence(pr.state)}</Badge>
          <BrBadge br={pr.blastRadius} />
          <ArrowUpRight className="text-muted-foreground ml-auto size-3.5" aria-hidden />
        </div>
        <p className="text-muted-foreground flex items-center gap-1 truncate font-mono text-xs"><GitBranch className="size-3 shrink-0" aria-hidden /><span className="truncate">{pr.headBranch}</span></p>
        {pr.headSha && <p className="text-muted-foreground font-mono text-xs">head {pr.headSha}</p>}
      </a>
      {pr.state === 'open' && (
        <Link to="/inbox" className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1 text-xs">Review in PR inbox <ChevronRight className="size-3" aria-hidden /></Link>
      )}
    </div>
  )
}

function Sources({ ticket: t }: { ticket: TicketDetail }) {
  if (!t.source) return null
  const label = SOURCE_LABEL[t.source] ?? t.source
  return (
    <div className="space-y-2">
      <SideTitle>Source</SideTitle>
      {t.sourceUrl ? (
        <a href={t.sourceUrl} target="_blank" rel="noreferrer" className="text-muted-foreground hover:text-foreground flex items-center gap-2 text-xs">
          <SourceIcon kind={t.source} /> <span>{label}</span>
          <ArrowUpRight className="ml-auto size-3" aria-hidden />
        </a>
      ) : (
        <p className="text-muted-foreground flex items-center gap-2 text-xs"><SourceIcon kind={t.source} /> {label}</p>
      )}
    </div>
  )
}

function History({ history }: { history: StageTransition[] }) {
  return (
    <div className="space-y-2">
      <SideTitle>Stage history</SideTitle>
      <ol className="relative space-y-3 pl-4 before:absolute before:top-1 before:bottom-1 before:left-[3px] before:w-px before:bg-border">
        {[...history].reverse().map((h) => (
          <li key={h.id} className="relative">
            <StageDot stage={h.toStage} className="ring-background absolute top-1.5 -left-4 ring-2" />
            <div className="flex items-baseline justify-between gap-2 text-xs">
              <span>{STAGE_LABEL[h.toStage]}</span>
              <time className="tnum text-muted-foreground shrink-0 text-xs" dateTime={h.at}>{formatDateTime(h.at)}</time>
            </div>
            <p className="text-muted-foreground text-xs">{formatActor(h.actor)}{h.reason && ` · ${h.reason}`}</p>
          </li>
        ))}
      </ol>
    </div>
  )
}

function SideTitle({ children }: { children: React.ReactNode }) {
  return <h2 className="text-sm font-semibold">{children}</h2>
}

function TicketSkeleton() {
  return (
    <div className="grid flex-1 lg:grid-cols-[minmax(0,1fr)_320px]" aria-busy="true" aria-label="Loading ticket">
      <div className="space-y-4 px-6 py-5 lg:border-r">
        <Skeleton className="h-6 w-2/3" />
        <Skeleton className="h-4 w-1/2" />
        <Skeleton className="mt-6 h-20 w-full" />
        <Skeleton className="h-40 w-full" />
      </div>
      <div className="space-y-4 px-5 py-5">
        <Skeleton className="h-16 w-full" />
        <Skeleton className="h-32 w-full" />
      </div>
    </div>
  )
}
