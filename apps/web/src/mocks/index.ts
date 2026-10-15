import { mockTransport } from '@/api/config'

/** Start the MSW worker; fall back to in-page handlers + mock EventSource if it can't start. */
export async function startMocks(): Promise<void> {
  try {
    if (!('serviceWorker' in navigator)) throw new Error('Service workers unavailable')
    const { worker } = await import('./browser')
    await worker.start({
      onUnhandledRequest: 'bypass',
      quiet: true,
      serviceWorker: { url: `${import.meta.env.BASE_URL}mockServiceWorker.js` },
    })
    mockTransport.kind = 'worker'
  } catch (err) {
    console.warn('[alpine] MSW worker unavailable, using inline mocks:', err)
    mockTransport.kind = 'inline'
  }
}
