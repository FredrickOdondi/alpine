import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate } from '@tanstack/react-router'
import { ChevronRight, CircleCheck, CircleDot, CircleX, GitPullRequest, MessageSquareReply, ShieldAlert } from 'lucide-react'
import { useEffect, useRef, useState, type KeyboardEvent, type MouseEvent } from 'react'
import { api } from '@/api/client'
import { useReviewPullRequest } from '@/api/mutations'
import { evidenceQuery, meQuery, pullRequestsQuery, queryKeys } from '@/api/queries'
import { brLabel, type GateSummary, type PullRequest, type PullRequestEvidence } from '@/api/types'
import { BrBadge, PriorityBadge } from '@/components/app/badges'
import { EmptyState, ErrorState, PageHeader } from '@/components/app/page-header'
import { VisualDiffSlot, VisualDiffViewer } from '@/components/app/visual-diff'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/components/ui/dialog'
import { Popover, PopoverAnchor, PopoverContent } from '@/components/ui/popover'
import { Segmented, SegmentedItem } from '@/components/ui/segmented'
import { Skeleton } from '@/components/ui/skeleton'
import { useToast } from '@/components/ui/toast'
import { Tooltip } from '@/components/ui/tooltip'
import { useListNavigation } from '@/hooks/use-hotkeys'
import { formatUsd, timeAgo } from '@/lib/format'
import { middleTruncatePath } from '@/lib/path'
import { cn } from '@/lib/utils'

const COLS = 9
const UNDO_MS = 6000

export function InboxPage() {
  const [state, setState] = useState<'open' | 'merged'>('open')
  const [expanded, setExpanded] = useState<string | null>(null)
  const prs = useQuery(pullRequestsQuery(state))
  useListNavigation()
  const items = prs.data?.items ?? []

  return (
    <div className="flex min-h-screen flex-col">
      <PageHeader
        title="PR inbox"
        meta="Sorted by blast radius, then priority, then age"
        actions={
          <Segmented
            value={state}
            onValueChange={(v: 'open' | 'merged') => {
              setState(v)
              setExpanded(null)
            }}
            aria-label="Pull request state"
          >
            <SegmentedItem value="open">Open</SegmentedItem>
            <SegmentedItem value="merged">Merged</SegmentedItem>
          </Segmented>
        }
      />
      {prs.error ? (
        <ErrorState error={prs.error} title="Couldn’t load pull requests" onRetry={() => void prs.refetch()} />
      ) : prs.data && items.length === 0 ? (
        <EmptyState
          icon={<GitPullRequest />}
          title={state === 'open' ? 'Inbox zero' : 'Nothing merged yet'}
          body={state === 'open' ? 'No agent PRs are waiting on you. Tickets land here when the reviewer approves.' : 'Merged agent PRs appear here once a human merges them on GitHub.'}
          action={<Button variant="outline" size="sm" asChild><Link to="/board">Open the pipeline</Link></Button>}
        />
      ) : (
        <div className="relative overflow-x-auto">
          <table className="tnum w-full min-w-[1040px] table-fixed text-sm">
            <caption className="sr-only">
              {state === 'open' ? 'Open' : 'Merged'} agent pull requests, sorted by blast radius. Press A to approve merge, R to request changes, O to open the ticket, X to expand.
            </caption>
            <colgroup>
              <col className="w-10" />
              <col className="w-16" />
              <col />
              <col className="w-24" />
              <col className="w-28" />
              <col className="w-24" />
              <col className="w-16" />
              <col className="w-20" />
              <col className="w-56" />
            </colgroup>
            <thead>
              <tr className="text-muted-foreground h-8 border-b text-xs">
                <th scope="col"><span className="sr-only">Expand</span></th>
                <th scope="col" className="text-left font-medium">Radius</th>
                <th scope="col" className="px-3 text-left font-medium">Pull request</th>
                <th scope="col" className="px-3 text-left font-medium">Gates</th>
                <th scope="col" className="px-3 text-left font-medium">Reviewer</th>
                <th scope="col" className="px-3 text-right font-medium">Diff</th>
                <th scope="col" className="px-3 text-right font-medium">Turns</th>
                <th scope="col" className="px-3 text-right font-medium">Cost</th>
                <th scope="col" className="px-4 text-right font-medium">{state === 'open' ? 'Decision' : 'Merged'}</th>
              </tr>
            </thead>
            <tbody data-nav-list>
              {prs.isPending
                ? Array.from({ length: 6 }, (_, i) => (
                    <tr key={i} className="h-12 border-b" aria-hidden>
                      <td /><td><Skeleton className="h-5 w-9" /></td><td className="px-3"><Skeleton className="h-4 w-3/4" /></td>
                      <td className="px-3"><Skeleton className="h-4 w-10" /></td><td colSpan={5} />
                    </tr>
                  ))
                : items.map((pr) => (
                    <PrRows
                      key={pr.id}
                      pr={pr}
                      expanded={expanded === pr.id}
                      onToggle={() => setExpanded((e) => (e === pr.id ? null : pr.id))}
                    />
                  ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

/* ------------------------------------------------------------------------------- decision flow */

/**
 * Approval friction scales with blast radius:
 * BR1–2 approve immediately with a 6s undo window, BR3 asks in a popover with the changed paths,
 * BR4 needs the repository name typed into a confirm dialog.
 */
function useDecision(pr: PullRequest) {
  const review = useReviewPullRequest(pr.id)
  const qc = useQueryClient()
  const toast = useToast()
  const [staged, setStaged] = useState(false)
  const [confirm, setConfirm] = useState<null | 'popover' | 'typed'>(null)
  const [decidedAt, setDecidedAt] = useState<string | null>(null)
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)

  // Leaving the page inside the undo window commits the approval rather than dropping it.
  useEffect(
    () => () => {
      if (timer.current) {
        clearTimeout(timer.current)
        void api.reviewPullRequest(pr.id, { action: 'approve' }).then(() => qc.invalidateQueries({ queryKey: queryKeys.evidence(pr.id) }))
      }
    },
    [pr.id, qc],
  )

  const commitApprove = () => {
    setDecidedAt(new Date().toISOString())
    review.mutate({ action: 'approve' })
  }

  const approveWithUndo = () => {
    setStaged(true)
    setDecidedAt(new Date().toISOString())
    timer.current = setTimeout(() => {
      timer.current = null
      setStaged(false)
      review.mutate({ action: 'approve' })
    }, UNDO_MS)
    toast({
      message: <>Approved merge of <span className="font-mono">#{pr.number}</span></>,
      duration: UNDO_MS,
      action: {
        label: 'Undo',
        onClick: () => {
          if (timer.current) clearTimeout(timer.current)
          timer.current = null
          setStaged(false)
          setDecidedAt(null)
        },
      },
    })
  }

  const approve = () => {
    if (pr.blastRadius >= 4) setConfirm('typed')
    else if (pr.blastRadius === 3) setConfirm('popover')
    else approveWithUndo()
  }
  const requestChanges = () => {
    setDecidedAt(new Date().toISOString())
    review.mutate({ action: 'request_changes' })
  }

  const pending = review.isPending && review.variables ? (review.variables.action === 'approve' ? 'approved' : 'changes_requested') : null
  return {
    staged: staged ? ('approved' as const) : pending,
    decidedAt,
    approve,
    requestChanges,
    commitApprove,
    confirm,
    setConfirm,
    error: review.error,
  }
}

type Decision = ReturnType<typeof useDecision>

/* ------------------------------------------------------------------------------- rows */

function PrRows({ pr, expanded, onToggle }: { pr: PullRequest; expanded: boolean; onToggle: () => void }) {
  // Gate and diff summaries come with the PR list (spec: PullRequest.gateSummary/diffSummary);
  // the evidence endpoint fills the review verdict, the human decision and any missing summary.
  const evidence = useQuery(evidenceQuery(pr.id))
  const ev = evidence.data
  const gateSummary = pr.gateSummary ?? ev?.gateSummary
  const diff = pr.diffSummary ?? ev
  const decision = useDecision(pr)
  const navigate = useNavigate()
  const detailsId = `pr-details-${pr.id}`
  const title = pr.ticketTitle ?? `PR #${pr.number}`
  const open = pr.state === 'open'

  const onKeyDown = (e: KeyboardEvent<HTMLTableRowElement>) => {
    if (e.metaKey || e.ctrlKey || e.altKey || (e.target as HTMLElement).closest('input, textarea')) return
    const key = e.key.toLowerCase()
    if (key === 'a' && open && !(ev?.humanDecision ?? pr.humanDecision)) decision.approve()
    else if (key === 'r' && open && !(ev?.humanDecision ?? pr.humanDecision)) decision.requestChanges()
    else if (key === 'o') void navigate({ to: '/tickets/$ticketId', params: { ticketId: pr.ticketId } })
    else if (key === 'x') onToggle()
    else return
    e.preventDefault()
    e.stopPropagation()
  }
  // Clicking anywhere on the row toggles, except on controls or while selecting text.
  const onRowClick = (e: MouseEvent) => {
    if ((e.target as HTMLElement).closest('button, a, input') || window.getSelection()?.toString()) return
    onToggle()
  }

  return (
    <>
      <tr
        onKeyDown={onKeyDown}
        onClick={onRowClick}
        className={cn('hover:bg-accent/60 h-12 cursor-default border-b transition-colors duration-150', expanded && 'bg-muted/40 border-b-0')}
      >
        <td className="pl-3">
          <button
            type="button"
            data-nav-item
            onClick={onToggle}
            aria-expanded={expanded}
            aria-controls={expanded ? detailsId : undefined}
            aria-label={`${expanded ? 'Collapse' : 'Expand'} ${title}`}
            className="text-muted-foreground hover:text-foreground hover:bg-accent flex size-7 items-center justify-center rounded-md"
          >
            <ChevronRight className={cn('size-4 transition-transform duration-150', expanded && 'rotate-90')} />
          </button>
        </td>
        <td><BrBadge br={pr.blastRadius} /></td>
        <td className="px-3">
          <p className="truncate font-medium">{title}</p>
          <p className="text-muted-foreground flex items-center gap-1 truncate text-xs">
            <span className="font-mono">#{pr.number}</span>·<span className="font-mono">{pr.ticketKey}</span>·<span className="truncate">{pr.repoFullName}</span>
            {pr.priority && <>·<PriorityBadge p={pr.priority} /></>}·<span className="shrink-0">{timeAgo(pr.openedAt)}</span>
          </p>
        </td>
        <td className="px-3"><GatesCell summary={gateSummary} loading={!gateSummary && evidence.isPending} /></td>
        <td className="px-3"><VerdictCell verdict={ev?.reviewVerdict ?? null} loading={evidence.isPending} /></td>
        <td className="px-3 text-right text-xs">
          {diff ? <><span className="text-ok">+{diff.additions}</span> <span className="text-fail">−{diff.deletions}</span></> : evidence.isPending ? <Skeleton className="ml-auto h-4 w-14" /> : '—'}
        </td>
        <td className="text-muted-foreground px-3 text-right text-xs">{pr.turnsUsed}</td>
        <td className="text-muted-foreground px-3 text-right text-xs">{formatUsd(pr.costUsd)}</td>
        <td className="px-4">
          <div className="flex justify-end">
            {pr.state === 'merged' ? (
              <span className="text-muted-foreground text-xs">{pr.mergedAt ? timeAgo(pr.mergedAt) : 'Merged'}</span>
            ) : pr.state === 'closed' ? (
              <span className="text-muted-foreground text-xs">Closed</span>
            ) : (
              <DecisionCell pr={pr} ev={ev} decision={decision} />
            )}
          </div>
        </td>
      </tr>
      {expanded && (
        <tr id={detailsId} className="bg-muted/40 border-b">
          <td colSpan={COLS} className="px-4 pt-2 pb-6 pl-14">
            <div className="animate-in fade-in slide-in-from-top-1 grid gap-6 duration-150 xl:grid-cols-[minmax(0,1fr)_320px]">
              <div className="space-y-4">
                {evidence.isPending ? (
                  <Skeleton className="aspect-[16/7] w-full" />
                ) : ev && ev.visualDiffs.length > 0 ? (
                  ev.visualDiffs.map((d) => <VisualDiffViewer key={d.route} diff={d} />)
                ) : (
                  <VisualDiffSlot reason={`No UI routes affected (${brLabel(pr.blastRadius)})`} />
                )}
              </div>
              <div className="space-y-4">
                <section className="space-y-2" aria-labelledby={`${detailsId}-files`}>
                  <h2 id={`${detailsId}-files`} className="text-sm font-semibold">Changed files</h2>
                  {ev ? <FileList files={ev.files} /> : <p className="text-muted-foreground text-xs">{evidence.isPending ? 'Loading…' : 'File list unavailable.'}</p>}
                </section>
                <dl className="text-muted-foreground grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs">
                  <dt>Branch</dt>
                  <dd className="truncate font-mono select-text" title={pr.headBranch}>{pr.headBranch}</dd>
                  {pr.headSha && (
                    <>
                      <dt>Head</dt>
                      <dd className="font-mono">{pr.headSha}</dd>
                    </>
                  )}
                </dl>
                <div className="flex flex-wrap gap-2">
                  <Button variant="outline" size="sm" asChild><Link to="/tickets/$ticketId" params={{ ticketId: pr.ticketId }}>Open ticket</Link></Button>
                  <Button variant="outline" size="sm" asChild><a href={pr.url} target="_blank" rel="noreferrer">View on GitHub</a></Button>
                </div>
              </div>
            </div>
          </td>
        </tr>
      )}
    </>
  )
}

function FileList({ files }: { files: PullRequestEvidence['files'] }) {
  return (
    <ul className="bg-card divide-y rounded-md border text-xs">
      {files.map((f) => (
        <li key={f.path} className="flex items-center gap-2 px-3 py-2">
          <span className="min-w-0 flex-1 truncate font-mono" title={f.path}>{middleTruncatePath(f.path)}</span>
          {f.status === 'added' && <Badge variant="outline">New</Badge>}
          <span className="tnum shrink-0"><span className="text-ok">+{f.additions}</span> <span className="text-fail">−{f.deletions}</span></span>
        </li>
      ))}
    </ul>
  )
}

function GatesCell({ summary, loading }: { summary: GateSummary | null | undefined; loading: boolean }) {
  if (loading) return <Skeleton className="h-4 w-12" />
  if (!summary || summary.total === 0) return <span className="text-muted-foreground text-xs">—</span>
  const g = summary
  return (
    <span className="flex items-center gap-2 text-xs whitespace-nowrap" title={`${g.passed} of ${g.total} gates passed, ${g.warned} warnings`}>
      <span className={g.failed === 0 ? 'text-foreground' : 'text-fail'}>{g.passed}/{g.total}</span>
      {g.warned > 0 && <span className="text-warn">{g.warned} warn</span>}
    </span>
  )
}

/** Reviewer verdict as a status (icon + past-tense word), never an imperative that looks clickable. */
function VerdictCell({ verdict, loading }: { verdict: PullRequestEvidence['reviewVerdict']; loading: boolean }) {
  if (loading) return <Skeleton className="h-4 w-16" />
  if (!verdict) return <span className="text-muted-foreground text-xs">—</span>
  const map = {
    approve: { icon: <CircleCheck className="text-ok size-4" aria-hidden />, label: 'Approved' },
    request_changes: { icon: <CircleDot className="text-warn size-4" aria-hidden />, label: 'Changes' },
    escalate: { icon: <CircleX className="text-fail size-4" aria-hidden />, label: 'Escalated' },
  }[verdict]
  return (
    <span className="text-muted-foreground flex items-center gap-1.5 text-xs">
      {map.icon}
      {map.label}
    </span>
  )
}

function DecisionCell({ pr, ev, decision }: { pr: PullRequest; ev: PullRequestEvidence | null | undefined; decision: Decision }) {
  const me = useQuery(meQuery).data
  const shown = decision.staged ?? ev?.humanDecision ?? pr.humanDecision ?? null
  const initials = (me?.user.name || me?.user.githubLogin || '').split(/\s+/).map((p) => p[0]).join('').slice(0, 2).toUpperCase()

  if (shown) {
    return (
      <span className="flex items-center gap-2">
        {shown === 'approved' ? (
          <Badge variant="ok"><CircleCheck />Approved</Badge>
        ) : (
          <Badge variant="warn"><MessageSquareReply />Changes requested</Badge>
        )}
        <span className="text-muted-foreground text-xs" title={me ? `By ${me.user.name ?? me.user.githubLogin}` : undefined}>
          {initials}
          {decision.decidedAt && ` · ${timeAgo(decision.decidedAt)}`}
        </span>
      </span>
    )
  }

  return (
    <span className="flex items-center gap-1">
      <Popover open={decision.confirm === 'popover'} onOpenChange={(o) => !o && decision.setConfirm(null)}>
        <PopoverAnchor asChild>
          <Button size="sm" variant="outline" onClick={decision.approve} aria-label={`Approve merge of PR #${pr.number} (${brLabel(pr.blastRadius)})`}>
            Approve merge
          </Button>
        </PopoverAnchor>
        <PopoverContent aria-label={`Confirm merge approval for #${pr.number}`} className="space-y-3">
          <div className="space-y-1">
            <p className="font-semibold">Approve a {brLabel(pr.blastRadius)} change?</p>
            <p className="text-muted-foreground text-xs">API and service logic. Check these paths before merging.</p>
          </div>
          {ev && <FileList files={ev.files} />}
          {ev && (
            <p className="text-muted-foreground text-xs">
              Gates {ev.gateSummary.passed}/{ev.gateSummary.total} passed{ev.gateSummary.warned ? `, ${ev.gateSummary.warned} warning` : ''} · reviewer {ev.reviewVerdict === 'approve' ? 'approved' : (ev.reviewVerdict ?? 'pending')}
            </p>
          )}
          <div className="flex justify-end gap-2">
            <Button size="sm" variant="ghost" onClick={() => decision.setConfirm(null)}>Cancel</Button>
            <Button
              size="sm"
              onClick={() => {
                decision.setConfirm(null)
                decision.commitApprove()
              }}
            >
              Approve merge
            </Button>
          </div>
        </PopoverContent>
      </Popover>
      <Tooltip content="Request changes (R)">
        <Button size="icon-sm" variant="ghost" onClick={decision.requestChanges} aria-label={`Request changes on PR #${pr.number}`}>
          <MessageSquareReply />
        </Button>
      </Tooltip>
      <TypedConfirm pr={pr} ev={ev} open={decision.confirm === 'typed'} onClose={() => decision.setConfirm(null)} onConfirm={decision.commitApprove} />
    </span>
  )
}

/** BR4 (data/infra): type the repository name to confirm. */
function TypedConfirm({
  pr,
  ev,
  open,
  onClose,
  onConfirm,
}: {
  pr: PullRequest
  ev: PullRequestEvidence | null | undefined
  open: boolean
  onClose: () => void
  onConfirm: () => void
}) {
  const [typed, setTyped] = useState('')
  const matches = typed.trim() === pr.repoFullName
  const close = () => {
    setTyped('')
    onClose()
  }
  return (
    <Dialog open={open} onOpenChange={(o) => !o && close()}>
      <DialogContent className="max-w-lg">
        <form
          className="space-y-4 p-4"
          onSubmit={(e) => {
            e.preventDefault()
            if (!matches) return
            close()
            onConfirm()
          }}
        >
          <div className="flex gap-3">
            <ShieldAlert className="text-fail mt-0.5 size-5 shrink-0" aria-hidden />
            <div className="space-y-1">
              <DialogTitle className="text-base font-semibold">Approve a {brLabel(pr.blastRadius)} data or infrastructure change</DialogTitle>
              <DialogDescription className="text-muted-foreground">
                <span className="font-mono">#{pr.number}</span> {pr.ticketTitle}. This touches schema, data or infrastructure and can’t be rolled back by reverting the UI.
              </DialogDescription>
            </div>
          </div>
          {ev && <FileList files={ev.files} />}
          <label className="block space-y-1.5">
            <span className="text-xs">
              Type <span className="font-mono font-medium select-all">{pr.repoFullName}</span> to confirm
            </span>
            <input
              autoFocus
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              spellCheck={false}
              autoComplete="off"
              className="border-input bg-card block h-8 w-full rounded-md border px-2 font-mono text-sm"
            />
          </label>
          <div className="flex justify-end gap-2">
            <Button type="button" size="sm" variant="ghost" onClick={close}>Cancel</Button>
            <Button type="submit" size="sm" variant="destructive" disabled={!matches}>Approve merge</Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  )
}
