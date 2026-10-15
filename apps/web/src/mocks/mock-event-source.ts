/**
 * In-page board stream used when the MSW service worker is unavailable (e.g. no secure
 * context). Feeds the same simulator the MSW `sse()` handler on /v1/events uses.
 */
import type { BoardEvent } from '@/api/types'
import { subscribe } from './db'

/** Same contract as `subscribeBoardEvents` from @alpine/api-client: returns an unsubscribe fn. */
export function subscribeInline(repoId: string | undefined, onEvent: (e: BoardEvent) => void): () => void {
  return subscribe((event) => {
    if (repoId && event.repoId !== repoId) return
    // Round-trip through JSON so consumers see the same shape as an SSE frame.
    onEvent(JSON.parse(JSON.stringify(event)) as BoardEvent)
  })
}
