import { subscribeBoardEvents } from '@alpine/api-client'
import { useQueryClient, type QueryClient } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { apiConfig, mockTransport } from './config'
import { queryKeys } from './queries'
import type { BoardEvent, ListResponse, TicketSummary } from './types'

export type StreamStatus = 'connecting' | 'open' | 'error'

/**
 * Apply a board event. Spec events are lightweight (ids + stage), so the stage is patched into
 * cached lists right away for a live feel, then the affected queries are invalidated
 * (as the spec recommends) to pull counters, cost and new tickets.
 */
export function applyBoardEvent(qc: QueryClient, event: BoardEvent) {
  qc.setQueriesData<ListResponse<TicketSummary>>({ queryKey: queryKeys.ticketsAll }, (page) =>
    page
      ? { items: page.items.map((t) => (t.id === event.ticketId ? { ...t, stage: event.stage, updatedAt: event.at } : t)) }
      : page,
  )
  void qc.invalidateQueries({ queryKey: queryKeys.ticketsAll })
  void qc.invalidateQueries({ queryKey: queryKeys.ticket(event.ticketId) })
  if (event.stage === 'pr_open' || event.previousStage === 'pr_open') {
    void qc.invalidateQueries({ queryKey: queryKeys.pullRequestsAll })
  }
}

export interface FeedItem {
  id: string
  event: BoardEvent
}

async function subscribe(repoId: string | undefined, onEvent: (e: BoardEvent) => void, onError: () => void) {
  if (apiConfig.mode === 'mock' && mockTransport.kind === 'inline') {
    const { subscribeInline } = await import('../mocks/mock-event-source')
    return subscribeInline(repoId, onEvent)
  }
  // Generated helper: EventSource on GET /v1/events (MSW's sse() handler intercepts it in mock mode).
  return subscribeBoardEvents(onEvent, { baseUrl: apiConfig.baseUrl, repoId, onError })
}

/** Subscribe to the board SSE stream and keep the query cache live. */
export function useBoardStream(repoId?: string) {
  const qc = useQueryClient()
  const [status, setStatus] = useState<StreamStatus>('connecting')
  const [feed, setFeed] = useState<FeedItem[]>([])
  /** Ticket ids touched by a live event in the last ~2.5s (for a brief highlight). */
  const [fresh, setFresh] = useState<ReadonlySet<string>>(new Set())

  useEffect(() => {
    let unsubscribe: (() => void) | null = null
    let cancelled = false
    const onEvent = (event: BoardEvent) => {
      setStatus('open')
      applyBoardEvent(qc, event)
      setFeed((prev) => [{ id: `${event.ticketId}-${event.at}-${event.type}`, event }, ...prev].slice(0, 20))
      setFresh((prev) => new Set(prev).add(event.ticketId))
      setTimeout(() => {
        setFresh((prev) => {
          const next = new Set(prev)
          next.delete(event.ticketId)
          return next
        })
      }, 2500)
    }
    void subscribe(repoId, onEvent, () => setStatus('error')).then((unsub) => {
      if (cancelled) unsub()
      else {
        unsubscribe = unsub
        setStatus((s) => (s === 'connecting' ? 'open' : s))
      }
    })
    return () => {
      cancelled = true
      unsubscribe?.()
    }
  }, [qc, repoId])

  return { status, feed, fresh }
}
