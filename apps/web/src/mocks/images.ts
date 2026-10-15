/**
 * Generated SVG "screenshots" of the customer's app (Kilima storefront / merchant console), one
 * scene per ticket so each ticket's evidence shows *its* bug: the route is in the address bar,
 * the page is the page the bug is on, and the repro frame annotates the failing element.
 * Pure strings → data URIs, so the mocks ship without binary fixtures (videos are pre-rendered
 * from these by scripts/gen-repro-videos.mjs).
 */
export type ShotVariant = 'before' | 'after' | 'heatmap' | 'repro'

type Kind =
  | 'checkout-disabled' | 'checkout-error' | 'checkout-iframe' | 'grid' | 'orders' | 'admin-sort'
  | 'buttons' | 'tags' | 'price' | 'preorder' | 'login-cookie' | 'address' | 'search' | 'wishlist'
  | 'modal' | 'footer-404' | 'toast' | 'skeleton' | 'network' | 'merchant'

interface Scene {
  kind: Kind
  /** One-line description of what the repro frame shows. */
  bug: string
}

/** Scenes by CONTENT index (index-aligned with TITLES/CONTENT). */
const SCENES: Scene[] = [
  { kind: 'checkout-disabled', bug: 'Coupon removed, "Place order" still disabled' },
  { kind: 'checkout-error', bug: 'TypeError: cart.total is undefined (cold deep link)' },
  { kind: 'grid', bug: 'Cards reflow as images load (CLS 0.31)' },
  { kind: 'orders', bug: 'URL says page=2, table shows page 3' },
  { kind: 'merchant', bug: 'Refund retried with the first attempt’s idempotency key' },
  { kind: 'buttons', bug: 'size="sm" renders at md height on dark surfaces' },
  { kind: 'price', bug: 'KES price drops the trailing zero: KSh 1,250.5' },
  { kind: 'merchant', bug: 'Settlement report p95 4.8s (sequential scan)' },
  { kind: 'login-cookie', bug: 'Cookie alpine_session blocked: SameSite missing' },
  { kind: 'search', bug: 'Stale suggestions for “tra” shown after “trail run”' },
  { kind: 'tags', bug: 'Tag padding 6px, off the 4px spacing scale' },
  { kind: 'network', bug: 'OPTIONS preflight counted by the rate limiter → 429' },
  { kind: 'wishlist', bug: 'Copy points to a heart icon that no longer exists' },
  { kind: 'merchant', bug: 'Payout CSV columns in random order' },
  { kind: 'modal', bug: 'Shift+Tab moves focus behind the modal' },
  { kind: 'merchant', bug: '220 queries for one settlements page (N+1)' },
  { kind: 'merchant', bug: 'amount numeric(12,2) truncates FX conversions' },
  { kind: 'footer-404', bug: 'Footer link keeps the old locale: /sw/en/help → 404' },
  { kind: 'toast', bug: 'Toast renders under the cookie banner' },
  { kind: 'merchant', bug: 'Signature compared with == (timing leak)' },
  { kind: 'address', bug: 'Apartment field cleared after picking a suggestion' },
  { kind: 'merchant', bug: 'Three retry layers multiply into a retry storm' },
  { kind: 'skeleton', bug: 'Cart drawer skeleton never clears on 304' },
  { kind: 'preorder', bug: 'Preorder product shows “0 left”' },
  { kind: 'checkout-iframe', bug: '“Place order” enabled before the payment iframe loads' },
  { kind: 'admin-sort', bug: 'Sort resets to “Updated” after an inline price edit' },
]

const W = 640
const H = 400
const FONT = 'font-family="Inter, system-ui, -apple-system, Segoe UI, sans-serif"'
const INK = '#1f2328'
const MUTED = '#57606a'
const FAINT = '#8b949e'
const LINE = '#e6e8eb'
const BRAND = '#16433f'
const RED = '#c4321f'

const esc = (s: string) => s.replace(/[<>&"]/g, (c) => `&#${c.charCodeAt(0)};`)
const t = (x: number, y: number, s: string, size = 12, fill = INK, weight = 400, anchor = 'start') =>
  `<text x="${x}" y="${y}" font-size="${size}" font-weight="${weight}" fill="${fill}" text-anchor="${anchor}" ${FONT}>${esc(s)}</text>`
const r = (x: number, y: number, w: number, h: number, fill = '#fff', extra = '') =>
  `<rect x="${x}" y="${y}" width="${w}" height="${h}" fill="${fill}" ${extra}/>`
const box = (x: number, y: number, w: number, h: number, fill = '#fff', rx = 6) => r(x, y, w, h, fill, `rx="${rx}" stroke="${LINE}"`)
const btn = (x: number, y: number, w: number, h: number, label: string, on = true) =>
  `${r(x, y, w, h, on ? BRAND : '#c9ced3', 'rx="6"')}${t(x + w / 2, y + h / 2 + 4, label, 12, on ? '#fff' : '#6a737d', 600, 'middle')}`
const ring = (x: number, y: number, w: number, h: number, color = '#2f81f7') =>
  `<rect x="${x - 3}" y="${y - 3}" width="${w + 6}" height="${h + 6}" rx="8" fill="none" stroke="${color}" stroke-width="2.5"/>`

interface Rect { x: number; y: number; w: number; h: number }
interface Drawn { body: string; focus: Rect; url?: string }

function chrome(route: string, merchant: boolean, url?: string) {
  const host = route.startsWith('/design-system') ? 'design.kilima.dev' : route.startsWith('/api') ? 'api.kilima.shop' : merchant ? 'merchant.kilima.co' : 'kilima.shop'
  const nav = merchant ? ['Payments', 'Settlements', 'Payouts'] : ['Shop', 'Orders', 'Help']
  return `${r(0, 0, W, 28, '#eceef1')}
    <circle cx="14" cy="14" r="4" fill="#d0d4d9"/><circle cx="27" cy="14" r="4" fill="#d0d4d9"/><circle cx="40" cy="14" r="4" fill="#d0d4d9"/>
    ${r(64, 5, 470, 18, '#fff', 'rx="9"')}${t(76, 18, `${host}${(url ?? route).replace(/^\/design-system/, '/?path=/docs').replace(/^\/api/, '')}` || host, 10, MUTED)}
    ${r(0, 28, W, 36)}${r(0, 64, W, 1, LINE)}
    ${t(20, 51, merchant ? 'Kilima · Merchant' : 'Kilima', 14, BRAND, 700)}
    ${nav.map((l, i) => t(220 + i * 76, 51, l, 12, MUTED)).join('')}
    <circle cx="${W - 30}" cy="46" r="11" fill="${LINE}"/>${t(W - 30, 50, 'WN', 9, MUTED, 600, 'middle')}`
}

const CART: [string, string, string][] = [
  ['Trail Runner 2', 'Size 42 · Slate', 'KSh 8,450.00'],
  ['Merino crew socks', '2-pack · Charcoal', 'KSh 1,200.00'],
  ['Alpine Shell Jacket', 'M · Glacier', 'KSh 14,900.00'],
]

function cartRows(y0 = 116) {
  return CART.map(([n, sub, p], i) => {
    const y = y0 + i * 46
    return `${box(20, y, 380, 40)}${r(28, y + 6, 28, 28, ['#d9e6e3', '#ece5d8', '#dde3ee'][i], 'rx="4"')}
      ${t(66, y + 17, n, 12, INK, 500)}${t(66, y + 31, sub, 10, '#6a737d')}${t(388, y + 24, p, 12, INK, 500, 'end')}`
  }).join('')
}

function summary(opts: { coupon: string; total: string; button: boolean; label?: string }) {
  return `${box(416, 108, 204, 200, '#fff', 8)}${t(432, 134, 'Order summary', 12, INK, 600)}
    ${t(432, 160, 'Subtotal', 11, MUTED)}${t(604, 160, 'KSh 24,550.00', 11, INK, 400, 'end')}
    ${t(432, 180, 'Coupon', 11, MUTED)}${t(604, 180, opts.coupon, 11, INK, 400, 'end')}
    ${r(432, 194, 172, 1, LINE)}${t(432, 220, 'Total', 12, INK, 600)}${t(604, 220, opts.total, 12, INK, 600, 'end')}
    ${btn(432, 256, 172, 34, opts.label ?? 'Place order', opts.button)}`
}

function draw(kind: Kind, bug: boolean, route: string): Drawn {
  switch (kind) {
    case 'checkout-disabled':
      return {
        body: `${t(20, 96, 'Checkout', 20, INK, 600)}${cartRows()}${summary({ coupon: 'removed', total: 'KSh 24,550.00', button: !bug })}`,
        focus: { x: 432, y: 256, w: 172, h: 34 },
      }
    case 'checkout-error':
      return {
        body: `${t(20, 96, 'Checkout', 20, INK, 600)}${cartRows()}${
          bug
            ? `${box(416, 108, 204, 200, '#fff', 8)}${[134, 160, 180, 220].map((y) => r(432, y - 10, 150, 10, '#eef0f2', 'rx="3"')).join('')}${r(432, 256, 172, 34, '#eef0f2', 'rx="6"')}`
            : summary({ coupon: 'KSh −2,490.00', total: 'KSh 22,060.00', button: true })
        }`,
        focus: { x: 416, y: 108, w: 204, h: 200 },
      }
    case 'checkout-iframe':
      return {
        body: `${t(20, 96, 'Checkout · Guest', 20, INK, 600)}${box(20, 108, 380, 182, '#fff', 8)}${t(36, 132, 'Card details', 12, INK, 600)}
          ${bug ? `${r(36, 146, 348, 120, '#f3f4f6', 'rx="6"')}<circle cx="210" cy="206" r="12" fill="none" stroke="${FAINT}" stroke-width="2.5" stroke-dasharray="40 30"/>${t(210, 236, 'Loading secure payment form…', 10, MUTED, 400, 'middle')}`
              : `${box(36, 146, 348, 32)}${t(48, 166, '4242 4242 4242 4242', 12, INK)}${box(36, 188, 168, 32)}${t(48, 208, '12 / 28', 12, INK)}${box(216, 188, 168, 32)}${t(228, 208, '•••', 12, INK)}${t(36, 246, 'Secured by Kilima Pay', 10, MUTED)}`}
          ${summary({ coupon: '—', total: 'KSh 24,550.00', button: true })}`,
        focus: bug ? { x: 36, y: 146, w: 348, h: 120 } : { x: 432, y: 256, w: 172, h: 34 },
      }
    case 'grid': {
      const cards = Array.from({ length: 6 }, (_, i) => {
        const col = i % 3
        const row = Math.floor(i / 3)
        const shift = bug && (i === 1 || i === 4) ? 26 : 0
        const imgH = bug && i % 2 === 1 ? 54 : 80
        const x = 20 + col * 204
        const y = 108 + row * 142 + shift
        return `${box(x, y, 192, 130)}${r(x + 8, y + 8, 176, imgH, ['#dde3ee', '#d9e6e3', '#ece5d8'][col], 'rx="4"')}
          ${t(x + 8, y + imgH + 26, ['Trail Runner 2', 'Ridge GTX', 'Summit Low'][col], 12, INK, 500)}${t(x + 8, y + imgH + 42, 'KSh ' + ['8,450.00', '11,200.00', '9,990.00'][col], 11, MUTED)}`
      }).join('')
      return { body: `${t(20, 96, 'Trail & running shoes', 20, INK, 600)}${cards}`, focus: { x: 224, y: 108, w: 192, h: 300 } }
    }
    case 'orders': {
      const first = bug ? 1041 : 1031
      const rows = Array.from({ length: 5 }, (_, i) => {
        const y = 128 + i * 34
        return `${r(20, y, 600, 34)}${r(20, y + 33, 600, 1, LINE)}${t(32, y + 21, `#KL-${first + i}`, 12, INK, 500)}${t(200, y + 21, `Oct ${12 - i}, 2026`, 12, MUTED)}${t(360, y + 21, 'Delivered', 12, MUTED)}${t(604, y + 21, `KSh ${(8 + i * 3).toLocaleString()},450.00`, 12, INK, 400, 'end')}`
      }).join('')
      const pages = [1, 2, 3].map((p, i) => {
        const active = p === (bug ? 3 : 2)
        return `${r(256 + i * 44, 312, 36, 28, active ? BRAND : '#fff', `rx="6" stroke="${LINE}"`)}${t(274 + i * 44, 330, String(p), 12, active ? '#fff' : INK, 600, 'middle')}`
      }).join('')
      return { body: `${t(20, 96, 'Order history', 20, INK, 600)}${t(20, 116, 'Page 2 of 3', 11, MUTED)}${rows}${pages}`, focus: { x: 250, y: 306, w: 136, h: 40 }, url: '/account/orders?page=2' }
    }
    case 'admin-sort': {
      const header = bug ? 'Updated ▼' : 'Price ▲'
      const prices = bug ? ['9,990.00', '14,900.00', '1,200.00', '8,450.00', '11,200.00'] : ['1,200.00', '8,450.00', '9,990.00', '11,200.00', '14,900.00']
      const rows = prices.map((p, i) => {
        const y = 140 + i * 34
        const edited = p === '9,990.00'
        return `${r(20, y, 600, 34, edited ? '#fff8e6' : '#fff')}${r(20, y + 33, 600, 1, LINE)}${t(32, y + 21, ['Summit Low', 'Alpine Shell Jacket', 'Merino crew socks', 'Trail Runner 2', 'Ridge GTX'][prices.indexOf(p) % 5], 12, INK)}${t(604, y + 21, `KSh ${p}`, 12, INK, edited ? 600 : 400, 'end')}`
      }).join('')
      return {
        body: `${t(20, 96, 'Products', 20, INK, 600)}${r(20, 112, 600, 28, '#f6f7f8')}${t(32, 130, 'Product', 11, MUTED, 600)}${t(604, 130, header, 11, bug ? RED : MUTED, 600, 'end')}${rows}`,
        focus: { x: 500, y: 112, w: 120, h: 28 },
      }
    }
    case 'buttons': {
      const panel = (x: number, dark: boolean) => {
        const bg = dark ? '#16191d' : '#ffffff'
        const fg = dark ? '#e6e8eb' : INK
        const smH = dark && bug ? 32 : 24
        return `${r(x, 108, 290, 220, bg, `rx="8" stroke="${LINE}"`)}${t(x + 16, 132, dark ? 'Dark surface' : 'Light surface', 11, dark ? '#9aa4ae' : MUTED, 600)}
          ${[['sm', smH, 72], ['md', 32, 84], ['lg', 40, 96]].map(([l, h, w], i) => `${r(x + 16, 150 + i * 56, w as number, h as number, dark ? '#e6e8eb' : INK, 'rx="6"')}${t(x + 16 + (w as number) / 2, 150 + i * 56 + (h as number) / 2 + 4, `Button ${l}`, 11, dark ? '#16191d' : '#fff', 600, 'middle')}${t(x + 130, 150 + i * 56 + (h as number) / 2 + 4, `size="${l}" · ${h}px`, 10, fg)}`).join('')}`
      }
      return { body: `${t(20, 96, 'Button', 20, INK, 600)}${panel(20, false)}${panel(330, true)}`, focus: { x: 346, y: 150, w: 72, h: bug ? 32 : 24 } }
    }
    case 'tags': {
      const pad = bug ? 6 : 8
      const tags = ['New', 'Waterproof', 'Vegan', 'Trail']
      let x = 36
      const items = tags.map((l) => {
        const w = l.length * 6.6 + pad * 2
        const s = `${r(x, 160, w, 22, '#eef3f2', 'rx="4"')}${t(x + pad, 175, l, 11, BRAND, 500)}`
        x += w + 8
        return s
      }).join('')
      const grid = Array.from({ length: 30 }, (_, i) => r(36 + i * 8, 150, 1, 44, '#ffd7d2', 'opacity="0.6"')).join('')
      return {
        body: `${t(20, 96, 'Tag', 20, INK, 600)}${box(20, 120, 600, 200, '#fff', 8)}${t(36, 142, `padding-inline: ${pad}px ${bug ? '(space-1.5)' : '(space-2)'}`, 11, MUTED)}${grid}${items}`,
        focus: { x: 36, y: 160, w: x - 44, h: 22 },
      }
    }
    case 'price':
    case 'preorder': {
      const preorder = kind === 'preorder'
      const name = preorder ? 'Alpine Shell Jacket' : 'Trail Runner 2'
      const price = preorder ? 'KSh 14,900.00' : bug ? 'KSh 1,250.5' : 'KSh 1,250.50'
      const badge = preorder ? (bug ? ['0 left', '#fde8e6', RED] : ['Preorder · ships Nov 4', '#eef3f2', BRAND]) : null
      return {
        body: `${r(20, 84, 300, 300, preorder ? '#dde3ee' : '#d9e6e3', 'rx="8"')}${t(340, 112, name, 20, INK, 600)}${t(340, 150, price, 18, INK, 600)}
          ${badge ? `${r(340, 166, badge[0].length * 7 + 20, 24, badge[1], 'rx="12"')}${t(350, 182, badge[0], 11, badge[2], 600)}` : t(340, 182, 'Incl. VAT · Free delivery in Nairobi', 11, MUTED)}
          ${t(340, 222, 'Size', 11, MUTED, 600)}${['40', '41', '42', '43'].map((s, i) => `${box(340 + i * 48, 232, 40, 32)}${t(360 + i * 48, 252, s, 12, INK, 500, 'middle')}`).join('')}
          ${btn(340, 290, 260, 38, preorder ? 'Preorder' : 'Add to cart')}`,
        focus: preorder ? { x: 340, y: 166, w: (badge?.[0].length ?? 4) * 7 + 20, h: 24 } : { x: 340, y: 130, w: 150, h: 28 },
      }
    }
    case 'login-cookie':
      return {
        body: `${box(170, 84, 300, 196, '#fff', 8)}${t(320, 116, 'Sign in to Kilima', 16, INK, 600, 'middle')}${box(194, 136, 252, 32)}${t(206, 156, 'wanjiru@example.com', 12, INK)}${box(194, 178, 252, 32)}${t(206, 198, '••••••••••', 12, INK)}${btn(194, 226, 252, 34, 'Sign in')}
          ${r(0, 296, W, 104, '#202327')}${t(16, 316, 'Console', 11, '#c9d1d9', 600)}${t(80, 316, 'Application › Cookies', 11, '#8b949e')}
          ${bug ? `${r(12, 326, 616, 40, '#3b1d1a', 'rx="4"')}${t(24, 342, '⚠ Cookie “alpine_session” was blocked: SameSite attribute missing in a cross-site context.', 10, '#ffb4a8')}${t(24, 358, 'Partner checkout iframe · staging.kilima.shop', 10, '#c98d84')}`
            : `${r(12, 326, 616, 40, '#1d2b22', 'rx="4"')}${t(24, 342, '✓ alpine_session · Secure · HttpOnly · SameSite=Lax', 10, '#9be3b0')}${t(24, 358, 'Partner checkout iframe signed in', 10, '#7fb08f')}`}`,
        focus: { x: 12, y: 326, w: 616, h: 40 },
        url: '/login?next=/checkout',
      }
    case 'address':
      return {
        body: `${t(20, 96, 'Delivery address', 20, INK, 600)}${box(20, 108, 400, 260, '#fff', 8)}
          ${[['Street', '14 Riverside Drive'], ['Apartment, suite', bug ? '' : 'Apt 4B, Riverside Towers'], ['City', 'Nairobi'], ['Postal code', '00100']].map(([l, v], i) => `${t(36, 136 + i * 56, l, 11, MUTED, 600)}${box(36, 144 + i * 56, 368, 30)}${t(48, 163 + i * 56, v || 'Apartment, suite, floor', 12, v ? INK : FAINT)}`).join('')}
          ${box(436, 108, 184, 96, '#fff', 8)}${t(448, 130, 'Suggestions', 11, MUTED, 600)}${t(448, 152, '14 Riverside Drive, Nairobi', 11, INK)}${t(448, 174, '14 Riverside Lane, Nairobi', 11, MUTED)}`,
        focus: { x: 36, y: 200, w: 368, h: 30 },
      }
    case 'search': {
      const items = bug ? ['tracksuit', 'travel adapter', 'tray table'] : ['trail runner 2', 'trail running socks', 'trail running vest']
      return {
        body: `${t(20, 96, 'Search', 20, INK, 600)}${box(20, 108, 600, 36, '#fff', 8)}${t(36, 131, 'trail run', 13, INK)}
          ${box(20, 150, 600, 120, '#fff', 8)}${items.map((s, i) => `${t(36, 178 + i * 32, s, 12, bug ? MUTED : INK)}${i < 2 ? r(20, 190 + i * 32, 600, 1, LINE) : ''}`).join('')}
          ${t(20, 300, bug ? 'Suggestions for “tra” (response #1 arrived after #3)' : 'Suggestions for “trail run”', 11, MUTED)}`,
        focus: { x: 20, y: 150, w: 600, h: 120 },
      }
    }
    case 'wishlist':
      return {
        body: `${t(20, 96, 'Saved items', 20, INK, 600)}${box(120, 120, 400, 200, '#fff', 8)}${r(296, 150, 48, 48, '#eef3f2', 'rx="24"')}
          ${t(320, 232, 'Nothing saved yet', 14, INK, 600, 'middle')}${t(320, 256, bug ? 'Save items for later with the heart icon.' : 'Tap Save on any product to keep it here.', 12, MUTED, 400, 'middle')}${btn(250, 274, 140, 32, 'Browse shoes')}`,
        focus: { x: 180, y: 242, w: 280, h: 22 },
      }
    case 'modal':
      return {
        body: `${t(20, 96, 'Modal', 20, INK, 600)}${box(20, 108, 600, 260, '#fff', 8)}${r(0, 65, W, H - 65, '#000', 'opacity="0.35"')}
          ${r(170, 140, 300, 170, '#fff', 'rx="10"')}${t(190, 172, 'Remove coupon?', 15, INK, 600)}${t(190, 196, 'Your total will be recalculated.', 12, MUTED)}
          ${r(296, 262, 76, 32, '#fff', `rx="6" stroke="${LINE}"`)}${t(334, 282, 'Cancel', 12, INK, 500, 'middle')}${btn(380, 262, 74, 32, 'Remove')}
          ${bug ? ring(292, 38, 52, 20, '#2f81f7') : ring(296, 262, 76, 32, '#2f81f7')}`,
        focus: bug ? { x: 292, y: 38, w: 52, h: 20 } : { x: 296, y: 262, w: 76, h: 32 },
      }
    case 'footer-404':
      return {
        body: bug
          ? `${t(320, 190, '404', 44, '#d0d4d9', 700, 'middle')}${t(320, 224, 'Page not found', 16, INK, 600, 'middle')}${t(320, 248, 'kilima.shop/sw/en/help doesn’t exist.', 12, MUTED, 400, 'middle')}`
          : `${t(20, 96, 'Help centre', 20, INK, 600)}${['Delivery & returns', 'Payments & M-Pesa', 'Your account'].map((l, i) => `${box(20 + i * 204, 116, 192, 90)}${t(36 + i * 204, 146, l, 13, INK, 600)}${t(36 + i * 204, 168, '6 articles', 11, MUTED)}`).join('')}`,
        focus: bug ? { x: 200, y: 150, w: 240, h: 110 } : { x: 20, y: 116, w: 600, h: 90 },
        url: bug ? '/sw/en/help' : '/sw/help',
      }
    case 'toast':
      return {
        body: `${t(20, 96, 'Your cart', 20, INK, 600)}${cartRows()}${summary({ coupon: '—', total: 'KSh 24,550.00', button: true, label: 'Checkout' })}
          ${r(0, 330, W, 70, '#202327')}${t(20, 356, 'We use cookies to keep you signed in and remember your cart.', 11, '#e6e8eb')}${r(520, 344, 100, 28, '#fff', 'rx="6"')}${t(570, 362, 'Accept', 11, INK, 600, 'middle')}
          ${bug ? `${r(380, 336, 240, 40, '#16433f', 'rx="8" opacity="0.92"')}${t(396, 360, 'Added Merino crew socks', 12, '#fff', 500)}` : `${r(380, 282, 240, 40, BRAND, 'rx="8"')}${t(396, 306, 'Added Merino crew socks', 12, '#fff', 500)}`}`,
        focus: bug ? { x: 380, y: 336, w: 240, h: 40 } : { x: 380, y: 282, w: 240, h: 40 },
      }
    case 'skeleton':
      return {
        body: `${t(20, 96, 'Your cart', 20, INK, 600)}${cartRows()}${r(380, 65, 260, H - 65, '#fff', `stroke="${LINE}"`)}${t(396, 94, 'Cart · 3 items', 13, INK, 600)}
          ${bug ? [110, 160, 210].map((y) => `${r(396, y, 40, 40, '#eef0f2', 'rx="4"')}${r(446, y + 6, 150, 10, '#eef0f2', 'rx="3"')}${r(446, y + 24, 90, 10, '#eef0f2', 'rx="3"')}`).join('') + t(396, 280, 'GET /v1/cart → 304 Not Modified', 10, FAINT)
            : CART.map(([n, , p], i) => `${r(396, 110 + i * 50, 40, 40, ['#d9e6e3', '#ece5d8', '#dde3ee'][i], 'rx="4"')}${t(446, 126 + i * 50, n, 11, INK, 500)}${t(446, 142 + i * 50, p, 11, MUTED)}`).join('')}
          ${btn(396, 330, 228, 34, 'Checkout', !bug)}`,
        focus: { x: 380, y: 100, w: 260, h: 160 },
      }
    case 'network': {
      const rows: [string, string, string][] = bug
        ? [['OPTIONS', '/v1/cart', '204'], ['GET', '/v1/cart', '200'], ['OPTIONS', '/v1/cart/items', '429'], ['POST', '/v1/cart/items', '429']]
        : [['OPTIONS', '/v1/cart', '204'], ['GET', '/v1/cart', '200'], ['OPTIONS', '/v1/cart/items', '204'], ['POST', '/v1/cart/items', '201']]
      return {
        body: `${t(20, 96, 'Network', 20, INK, 600)}${box(20, 108, 600, 32 + rows.length * 30, '#fff', 8)}${t(36, 128, 'Method', 11, MUTED, 600)}${t(140, 128, 'Path', 11, MUTED, 600)}${t(604, 128, 'Status', 11, MUTED, 600, 'end')}
          ${rows.map(([m, p, s], i) => `${r(20, 140 + i * 30, 600, 1, LINE)}${t(36, 160 + i * 30, m, 12, INK, 500)}${t(140, 160 + i * 30, p, 12, INK)}${t(604, 160 + i * 30, s, 12, s === '429' ? RED : INK, 600, 'end')}`).join('')}
          ${t(20, 290, bug ? 'X-RateLimit-Remaining: 0 (preflights counted)' : 'X-RateLimit-Remaining: 58', 11, MUTED)}`,
        focus: { x: 20, y: 200, w: 600, h: 60 },
      }
    }
    default:
      return { body: `${t(20, 96, route, 20, INK, 600)}`, focus: { x: 20, y: 80, w: 300, h: 30 } }
  }
}

const sceneAt = (index: number) => SCENES[((index % SCENES.length) + SCENES.length) % SCENES.length]

/** Screenshot of ticket content `index` (index-aligned with CONTENT) on `route`. */
export function ticketShot(index: number, route: string, variant: ShotVariant): string {
  const scene = sceneAt(index)
  const merchant = route.startsWith('/merchant') || route.startsWith('/admin')
  const drawn = draw(scene.kind, variant !== 'after', route)
  const f = drawn.focus
  let svg: string
  if (variant === 'heatmap') {
    svg = `<svg xmlns="http://www.w3.org/2000/svg" width="${W}" height="${H}" viewBox="0 0 ${W} ${H}">
      ${r(0, 0, W, H, '#0f1214')}${r(0, 0, W, 65, '#ffffff', 'opacity="0.04"')}
      ${r(f.x - 4, f.y - 4, f.w + 8, f.h + 8, '#ff4d6d', 'rx="6" opacity="0.85"')}
      ${t(W - 16, H - 14, `${route} · changed region`, 11, '#ff9fb1', 500, 'end')}</svg>`
  } else {
    const annotate =
      variant === 'repro'
        ? `<rect x="${f.x - 5}" y="${f.y - 5}" width="${f.w + 10}" height="${f.h + 10}" rx="8" fill="none" stroke="${RED}" stroke-width="2" stroke-dasharray="6 4"/>
           ${r(12, H - 40, W - 24, 28, '#fff1f0', `rx="6" stroke="#f1b5ad"`)}${t(24, H - 21, `Repro · ${scene.bug}`, 11, '#a3261b', 600)}`
        : ''
    svg = `<svg xmlns="http://www.w3.org/2000/svg" width="${W}" height="${H}" viewBox="0 0 ${W} ${H}">
      ${r(0, 0, W, H, '#f6f7f8')}${chrome(route, merchant, drawn.url)}${drawn.body}${annotate}</svg>`
  }
  return `data:image/svg+xml;charset=utf-8,${encodeURIComponent(svg)}`
}

/** Centre of the element the repro interacts with (used to aim the cursor in repro videos). */
export function ticketFocus(index: number, route: string) {
  const f = draw(sceneAt(index).kind, true, route).focus
  return { x: f.x + f.w / 2, y: f.y + f.h / 2 }
}

export function ticketBug(index: number) {
  return sceneAt(index).bug
}
