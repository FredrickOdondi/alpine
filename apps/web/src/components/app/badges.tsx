import { Bug, CircleAlert, CircleCheck, CircleDashed, CircleMinus, CircleX, LoaderCircle, TriangleAlert } from 'lucide-react'
import { brLabel, type BlastRadius, type GateStatus, type Priority, type SourceKind, type Stage } from '@/api/types'
import { Badge } from '@/components/ui/badge'
import { brTone, gateTone, stageDotClass } from '@/design/tokens'
import { sentence } from '@/lib/format'
import { cn } from '@/lib/utils'
import { GitHubMark } from './github-mark'

export function BrBadge({ br, withLabel = false, className }: { br: BlastRadius | null | undefined; withLabel?: boolean; className?: string }) {
  if (!br) {
    return (
      <Badge variant="outline" className={cn('font-mono', className)} aria-label="Blast radius not estimated yet">
        BR–
      </Badge>
    )
  }
  return (
    <Badge className={cn(brTone[br].badge, 'font-mono', className)} title={`Blast radius ${brLabel(br)}: ${brTone[br].label}`}>
      {brLabel(br)}
      {withLabel && <span className="font-sans">· {brTone[br].label}</span>}
    </Badge>
  )
}

const PRIORITY_LABEL: Record<Priority, string> = {
  P0: 'Urgent: live production bug',
  P1: 'High',
  P2: 'Medium',
  P3: 'Low',
}

/** Priority as a compact signal-bar glyph + label (no extra colour except P0). */
export function PriorityBadge({ p, showLabel = true }: { p: Priority; showLabel?: boolean }) {
  const level = { P0: 4, P1: 3, P2: 2, P3: 1 }[p]
  return (
    <span className={cn('relative inline-flex items-center gap-1 font-mono text-xs', p === 'P0' ? 'text-fail' : 'text-muted-foreground')} title={PRIORITY_LABEL[p]}>
      <svg viewBox="0 0 12 12" className="size-3" aria-hidden>
        {[0, 1, 2, 3].map((i) => (
          <rect key={i} x={i * 3} y={9 - i * 2.5} width="2" height={3 + i * 2.5} rx="0.5" className={i < level ? 'fill-current' : 'fill-current opacity-25'} />
        ))}
      </svg>
      {showLabel && p}
      <span className="sr-only">{PRIORITY_LABEL[p]}</span>
    </span>
  )
}

export function SourceIcon({ kind, className }: { kind: SourceKind; className?: string }) {
  const cls = cn('size-3.5 text-muted-foreground', className)
  if (kind === 'github') return <GitHubMark className={cls} aria-label="GitHub issue" />
  return <Bug className={cls} role="img" aria-label={`${sentence(kind)} issue`} />
}

export function StageDot({ stage, className }: { stage: Stage; className?: string }) {
  return <span className={cn('inline-block size-2 shrink-0 rounded-full', stageDotClass(stage), className)} aria-hidden />
}

/** Gate status icon; `running` is UI-only (a gate with no result yet in the latest iteration). */
export function GateStatusIcon({ status, className }: { status: GateStatus | 'running'; className?: string }) {
  const cls = cn('size-4', status === 'running' ? 'text-muted-foreground' : gateTone[status], className)
  const label = { role: 'img', 'aria-label': `Gate ${status === 'pass' ? 'passed' : status === 'fail' ? 'failed' : status}` }
  switch (status) {
    case 'pass':
      return <CircleCheck className={cls} {...label} />
    case 'fail':
      return <CircleX className={cls} {...label} />
    case 'error':
      return <CircleAlert className={cls} {...label} />
    case 'warn':
      return <TriangleAlert className={cls} {...label} />
    case 'running':
      return <LoaderCircle className={cn(cls, 'animate-spin')} {...label} />
    case 'skipped':
      return <CircleMinus className={cls} {...label} />
    default:
      return <CircleDashed className={cls} />
  }
}
