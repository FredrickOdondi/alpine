/**
 * Semantic token maps for TypeScript. Classes reference CSS variables from tokens.css, so
 * they follow the active theme automatically. Keep every colour decision here, not in screens.
 */
import { COLUMN_FOR_STAGE, type BlastRadius, type BoardColumn, type GateStatus, type Stage } from '@/api/types'

/** Tone per board column. Spec stages map onto columns via COLUMN_FOR_STAGE. */
export const stageTone: Record<BoardColumn, { dot: string; label: string; hint: string }> = {
  triage: { dot: 'bg-stage-triage', label: 'Triage', hint: 'Classify, dedupe, already-fixed check' },
  refinement: { dot: 'bg-stage-refinement', label: 'Refinement', hint: 'Reproduce, estimate blast radius, write acceptance criteria' },
  prioritize: { dot: 'bg-stage-prioritize', label: 'Prioritize', hint: 'Ready for agent or needs a human' },
  implement: { dot: 'bg-stage-implement', label: 'Implement', hint: 'Implementer working in an isolated sandbox' },
  gates: { dot: 'bg-stage-gates', label: 'Gates', hint: 'Deterministic quality gates from the base branch' },
  review: { dot: 'bg-stage-review', label: 'Review', hint: 'Independent reviewer with fresh context' },
  merge: { dot: 'bg-stage-merge', label: 'Merge', hint: 'Merge policy applied; PR open for a human or merged' },
  escalated: { dot: 'bg-stage-escalated', label: 'Escalated', hint: 'Turn or cost cap hit, or reviewer escalation' },
}

/** Dot class for any spec stage (closed/duplicate are off-board and neutral). */
export const stageDotClass = (stage: Stage) => {
  const col = COLUMN_FOR_STAGE[stage]
  return col ? stageTone[col].dot : 'bg-muted-foreground/50'
}

/**
 * Blast radius as a single-hue intensity ramp (weight, not traffic-light colour), so BR1 never
 * reads as "passed" and BR4 never reads as "failed". Red stays reserved for failures.
 */
export const brTone: Record<BlastRadius, { badge: string; label: string; bar: string }> = {
  1: { badge: 'bg-transparent border border-border-strong text-muted-foreground', bar: 'bg-border-strong', label: 'Leaf UI' },
  2: { badge: 'bg-secondary text-foreground border border-border', bar: 'bg-muted-foreground/60', label: 'Design system' },
  3: { badge: 'bg-foreground/75 text-background', bar: 'bg-foreground/75', label: 'API / logic' },
  4: { badge: 'bg-foreground text-background shadow-[inset_2px_0_0_var(--fail)]', bar: 'bg-foreground', label: 'Data / infra' },
}

export const gateTone: Record<GateStatus, string> = {
  pass: 'text-ok',
  fail: 'text-fail',
  warn: 'text-warn',
  error: 'text-fail',
  skipped: 'text-muted-foreground',
}

/** Motion: subtle and purposeful. Use these, not ad-hoc durations. */
export const motion = {
  fast: 'duration-150 ease-[var(--ease-standard)]',
  base: 'duration-200 ease-[var(--ease-standard)]',
}
