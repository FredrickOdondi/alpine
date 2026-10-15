import { useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from './client'
import { queryKeys } from './queries'
import type {
  ListResponse,
  PullRequestEvidence,
  PullRequestReviewRequest,
  Stage,
  TicketAction,
  TicketDetail,
  TicketSummary,
} from './types'

/** Where a ticket goes right after a human action (the workflow confirms via SSE). */
const OPTIMISTIC_STAGE: Record<TicketAction, Stage> = {
  approve: 'implement',
  resume: 'implement',
  reject: 'closed',
  close: 'closed',
  mark_merged: 'done',
}

/** Human decision on a ticket (POST /v1/tickets/{id}/actions → Temporal signal), optimistic. */
export function useTicketAction(ticketId: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (input: { action: TicketAction; note?: string }) => api.postTicketAction(ticketId, input),
    onMutate: async ({ action }) => {
      await qc.cancelQueries({ queryKey: queryKeys.ticket(ticketId) })
      const previous = qc.getQueryData<TicketDetail>(queryKeys.ticket(ticketId))
      const stage = OPTIMISTIC_STAGE[action]
      if (previous) qc.setQueryData<TicketDetail>(queryKeys.ticket(ticketId), { ...previous, stage, escalationReason: null })
      const lists = qc.getQueriesData<ListResponse<TicketSummary>>({ queryKey: queryKeys.ticketsAll })
      qc.setQueriesData<ListResponse<TicketSummary>>({ queryKey: queryKeys.ticketsAll }, (page) =>
        page ? { items: page.items.map((t) => (t.id === ticketId ? { ...t, stage, escalationReason: null } : t)) } : page,
      )
      return { previous, lists }
    },
    onError: (_e, _v, ctx) => {
      if (ctx?.previous) qc.setQueryData(queryKeys.ticket(ticketId), ctx.previous)
      for (const [key, data] of ctx?.lists ?? []) qc.setQueryData(key, data)
    },
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: queryKeys.ticket(ticketId) })
      void qc.invalidateQueries({ queryKey: queryKeys.ticketsAll })
    },
  })
}

/** Approve / send back a PR from the inbox (POST /v1/pull-requests/{id}/review), optimistic. */
export function useReviewPullRequest(prId: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (input: PullRequestReviewRequest) => api.reviewPullRequest(prId, input),
    onMutate: async (input) => {
      await qc.cancelQueries({ queryKey: queryKeys.evidence(prId) })
      const previous = qc.getQueryData<PullRequestEvidence | null>(queryKeys.evidence(prId))
      const humanDecision = input.action === 'approve' ? 'approved' : 'changes_requested'
      qc.setQueryData<PullRequestEvidence | null>(queryKeys.evidence(prId), (ev) => (ev ? { ...ev, humanDecision } : ev))
      return { previous, humanDecision }
    },
    onError: (_e, _v, ctx) => {
      if (ctx) qc.setQueryData(queryKeys.evidence(prId), ctx.previous)
    },
    onSuccess: (evidence) => qc.setQueryData(queryKeys.evidence(prId), evidence),
  })
}
