/**
 * Alpine API types for apps/web. This is the only module that declares API shapes.
 *
 * 1. Spec types are re-exported from @alpine/api-client, which is generated from
 *    api/openapi.yaml on jeff/core. Don't redeclare them.
 * 2. Artifacts, review, PR evidence/review, metrics and factory config were proposed here
 *    first; they are now in the spec and re-exported like everything else.
 * 3. View helpers are UI-only mappings, such as spec stages to the 8 board columns.
 */
import type {
  AgentRun,
  AgentTurn,
  ApiError,
  AuthProviders,
  BlastRadius,
  BoardEvent,
  BoardEventType,
  GateFinding,
  GateResult,
  GateStatus,
  Limits,
  Me,
  Org,
  OrgMembership,
  Priority,
  PullRequest,
  PullRequestState,
  Repo,
  Role,
  Size,
  Stage,
  StageTransition,
  TicketAction,
  TicketDetail,
  TicketSummary,
  TicketType,
  User,
  components,
} from '@alpine/api-client'

export type {
  AgentRun,
  AgentTurn,
  ApiError,
  AuthProviders,
  BlastRadius,
  BoardEvent,
  BoardEventType,
  GateFinding,
  GateResult,
  GateStatus,
  Limits,
  Me,
  Org,
  OrgMembership,
  Priority,
  PullRequest,
  PullRequestState,
  Repo,
  Role,
  Size,
  Stage,
  StageTransition,
  TicketAction,
  TicketDetail,
  TicketSummary,
  TicketType,
  User,
}
export type RunKind = components['schemas']['RunKind']
export type RunStatus = components['schemas']['RunStatus']
export type TicketActionRequest = components['schemas']['TicketActionRequest']
export type SourceKind = NonNullable<TicketSummary['source']>
export { STAGES as SPEC_STAGES } from '@alpine/api-client'

export interface ListResponse<T> {
  items: T[]
}

/* ------------------------------------------------------------------ view helpers */

/** The 8 board columns. Several spec stages collapse into one column. */
export const BOARD_COLUMNS = [
  'triage',
  'refinement',
  'prioritize',
  'implement',
  'gates',
  'review',
  'merge',
  'escalated',
] as const
export type BoardColumn = (typeof BOARD_COLUMNS)[number]

/** Column for each spec stage; `null` = not shown on the board (closed, duplicate). */
export const COLUMN_FOR_STAGE: Record<Stage, BoardColumn | null> = {
  intake: 'triage',
  triage: 'triage',
  refinement: 'refinement',
  prioritize: 'prioritize',
  needs_human: 'prioritize',
  implement: 'implement',
  gates: 'gates',
  review: 'review',
  merge_policy: 'merge',
  pr_open: 'merge',
  done: 'merge',
  escalated: 'escalated',
  closed: null,
  duplicate: null,
}

export const STAGE_LABEL: Record<Stage, string> = {
  intake: 'Intake',
  triage: 'Triage',
  refinement: 'Refinement',
  prioritize: 'Prioritize',
  needs_human: 'Needs human',
  implement: 'Implement',
  gates: 'Gates',
  review: 'Review',
  merge_policy: 'Merge policy',
  pr_open: 'PR open',
  done: 'Done',
  escalated: 'Escalated',
  closed: 'Closed',
  duplicate: 'Duplicate',
}

/** "BR1".."BR4" label for the spec's integer blast radius. */
export const brLabel = (br: BlastRadius) => `BR${br}` as `BR${1 | 2 | 3 | 4}`

/* ------------------------------------------------------------------ PR evidence, review, metrics, config */
// These used to be @proposed here; they are now in api/openapi.yaml and come from the generated client.
export type {
  Artifact,
  Review,
  ReviewFinding,
  ReviewVerdict,
  VisualDiff,
  PullRequestFile,
  HumanDecision,
  PullRequestEvidence,
  PullRequestReviewRequest,
  MetricsWindow,
  Metrics,
  FactoryConfigSpec,
  FactoryConfig,
  GateSummary,
  DiffSummary,
} from '@alpine/api-client'
