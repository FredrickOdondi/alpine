/** Runtime API configuration, resolved from Vite env at build time. */
export type ApiMode = 'mock' | 'live'

const rawMode = (import.meta.env.VITE_API_MODE ?? 'mock') as string

export const apiConfig = {
  mode: (rawMode === 'live' ? 'live' : 'mock') as ApiMode,
  /** In mock mode requests stay same-origin so MSW can intercept them. */
  baseUrl:
    rawMode === 'live' ? String(import.meta.env.VITE_API_BASE_URL ?? '').replace(/\/$/, '') : '',
}

/**
 * How mock traffic is served. `worker` = MSW service worker (default). `inline` = the
 * worker could not start (no service worker support / insecure origin), so the client
 * resolves the same MSW handlers in-page and SSE uses a mock EventSource.
 */
export const mockTransport: { kind: 'worker' | 'inline' } = { kind: 'worker' }
