/** In-page fallback: resolve a request against the same MSW handlers without a service worker. */
import { getResponse } from 'msw'
import { handlers } from './handlers'

export async function resolveInline(request: Request): Promise<Response> {
  const res = await getResponse(handlers, request)
  return res ?? new Response(JSON.stringify({ code: 'not_mocked', message: request.url }), { status: 501 })
}
