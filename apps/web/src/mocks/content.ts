/**
 * Realistic ticket bodies, acceptance criteria and touched files for the mock tickets.
 * Index-aligned with TITLES in db.ts.
 */
export interface TicketContent {
  body: string
  ac: string[]
  files: string[]
  route: string
}

export const CONTENT: TicketContent[] = [
  {
    body: 'Removing an applied coupon recomputes the order summary from the cached quote, so `canSubmit` stays false and "Place order" remains disabled until reload. Reproduced on Chromium and WebKit with seed guest-cart-03.',
    ac: ['Removing a coupon re-quotes the cart from current line items.', '"Place order" is enabled whenever the payable total is above 0.', 'e2e covers apply → remove coupon → place order.'],
    files: ['src/features/checkout/CouponField.tsx', 'src/features/checkout/useOrderSummary.ts', 'e2e/checkout/coupon.spec.ts'],
    route: '/checkout',
  },
  {
    body: 'Sentry: 412 events / 96 users in 24h since release 2026.10.3. `cart.total` is read before the cart query resolves when the page is opened from a saved-cart email link.',
    ac: ['Checkout renders a skeleton until the cart query resolves.', 'No TypeError when opening /checkout?cart=<id> cold.', 'Regression test for the deep-link path.'],
    files: ['src/features/checkout/CheckoutPage.tsx', 'src/features/checkout/CheckoutPage.test.tsx'],
    route: '/checkout',
  },
  {
    body: 'Cumulative layout shift of 0.31 on category pages: product cards have no intrinsic size until the image loads, so the grid reflows on slow connections.',
    ac: ['Product images reserve a 4:5 aspect ratio before load.', 'CLS on /c/shoes is below 0.05 in the Playwright perf trace.'],
    files: ['src/features/catalog/ProductCard.tsx', 'src/features/catalog/ProductGrid.module.css'],
    route: '/c/shoes',
  },
  {
    body: '`GET /v1/orders?page=2` returns page 3 because the cursor is computed from `offset + limit` after the offset has already been advanced.',
    ac: ['Paging through 3 pages of orders returns each order exactly once.', 'Contract test pins cursor semantics.'],
    files: ['src/server/orders/list.ts', 'src/server/orders/list.test.ts'],
    route: '/account/orders',
  },
  {
    body: 'Refund webhooks retried by the card network reuse the idempotency key of the first attempt but with a different amount after a partial refund, causing a 409 and a stuck refund.',
    ac: ['Idempotency key includes refund id and amount.', 'Replayed webhook with identical payload is a no-op.', 'Different amount for same refund id is rejected and alerted.'],
    files: ['internal/refunds/webhook.go', 'internal/refunds/idempotency.go', 'internal/refunds/webhook_test.go'],
    route: '/merchant/refunds',
  },
  {
    body: 'In dark surfaces the Button component applies the `md` padding token regardless of `size="sm"`, because the dark variant class is ordered after the size class.',
    ac: ['`size="sm"` renders 28px tall on all surfaces.', 'Visual snapshot of Button matrix updated intentionally.'],
    files: ['src/design-system/Button/Button.tsx', 'src/design-system/Button/button.css'],
    route: '/design-system/button',
  },
  {
    body: 'Prices in KES render as "KSh 1,250.5" on the PDP because the formatter uses `maximumFractionDigits` without `minimumFractionDigits`.',
    ac: ['All currencies render two fraction digits on PDP and cart.', 'Unit tests cover KES, USD and UGX.'],
    files: ['src/lib/money/formatPrice.ts', 'src/lib/money/formatPrice.test.ts'],
    route: '/p/trail-runner-2',
  },
  {
    body: 'Settlement report query scans `payments` sequentially (p95 4.8s). Needs a composite index on `(merchant_id, created_at)`; migration must be online.',
    ac: ['Migration creates the index CONCURRENTLY.', 'p95 for the settlement report under 300ms on the seeded dataset.'],
    files: ['migrations/20261014_payments_merchant_created_idx.sql', 'internal/settlements/report.go'],
    route: '/merchant/settlements',
  },
  {
    body: 'Staging session cookie is missing `SameSite=Lax`, so the embedded checkout in the partner iframe loses the session on Safari.',
    ac: ['Session cookie sets SameSite=Lax and Secure in all non-local envs.', 'Integration test asserts cookie attributes.'],
    files: ['src/server/auth/session.ts', 'src/server/auth/session.test.ts'],
    route: '/login',
  },
  {
    body: 'Search suggestions re-render with stale results when responses arrive out of order on slow connections.',
    ac: ['Only the response for the latest query is rendered.', 'e2e with throttled network shows no flicker.'],
    files: ['src/features/search/useSuggestions.ts', 'e2e/search/suggestions.spec.ts'],
    route: '/search',
  },
  {
    body: 'Tag uses `space-1.5` (6px) for horizontal padding where the spacing scale specifies `space-2` (8px).',
    ac: ['Tag padding matches the spacing scale.', 'No other component diffs in the visual run.'],
    files: ['src/design-system/Tag/Tag.tsx'],
    route: '/design-system/tag',
  },
  {
    body: 'The API rate limiter increments the bucket for CORS preflight requests, so browsers on the partner dashboard hit 429 at half the documented limit.',
    ac: ['OPTIONS requests do not consume rate-limit tokens.', 'Middleware test covers preflight and normal requests.'],
    files: ['src/server/middleware/rateLimit.ts', 'src/server/middleware/rateLimit.test.ts'],
    route: '/api',
  },
  {
    body: 'Wishlist empty state still says "Save items for later with the heart icon"; the icon was replaced by a bookmark in 2026.09.',
    ac: ['Copy matches the current save affordance.', 'Copy lives in the i18n catalogue (en, sw).'],
    files: ['src/features/wishlist/EmptyWishlist.tsx', 'src/i18n/en.json', 'src/i18n/sw.json'],
    route: '/wishlist',
  },
  {
    body: 'Payout CSV columns are emitted in map iteration order, so finance imports break randomly.',
    ac: ['Columns follow the documented order.', 'Golden-file test for the CSV header.'],
    files: ['internal/payouts/csv.go', 'internal/payouts/csv_test.go'],
    route: '/merchant/payouts',
  },
  {
    body: 'Shift+Tab from the first focusable element in Modal moves focus to the page behind it.',
    ac: ['Focus cycles within the modal in both directions.', 'axe check passes on the modal story.'],
    files: ['src/design-system/Modal/useFocusTrap.ts', 'src/design-system/Modal/Modal.test.tsx'],
    route: '/design-system/modal',
  },
  {
    body: 'Settlements endpoint runs one query per settlement to load the payout batch (N+1); 220 queries for a typical merchant page.',
    ac: ['Endpoint issues at most 3 queries per page.', 'Query-count assertion in the handler test.'],
    files: ['internal/settlements/handler.go', 'internal/settlements/repo.go', 'internal/settlements/handler_test.go'],
    route: '/merchant/settlements',
  },
  {
    body: '`ledger_entries.amount` is `numeric(12,2)`; FX conversions need 4 decimal places. Requires backfill and dual-write window.',
    ac: ['Online migration with backfill in batches of 10k.', 'Ledger balances identical before/after on seeded data.'],
    files: ['migrations/20261012_ledger_amount_precision.sql', 'internal/ledger/entry.go'],
    route: '/merchant/ledger',
  },
  {
    body: 'Footer links keep the previous locale prefix after switching language, e.g. /sw/en/help.',
    ac: ['Footer links are built from the active locale.', 'e2e switches en → sw and follows every footer link.'],
    files: ['src/features/layout/Footer.tsx', 'e2e/layout/footer-locale.spec.ts'],
    route: '/help',
  },
  {
    body: 'Toasts render at z-index 40, below the cookie banner (50), and overlap it on mobile.',
    ac: ['Toasts stack above the cookie banner with a 12px offset.', 'Visual diff limited to toast region.'],
    files: ['src/design-system/Toast/ToastViewport.tsx', 'src/design-system/tokens/zIndex.ts'],
    route: '/cart',
  },
  {
    body: 'Webhook signature verification compares HMACs with `==`, which is not constant-time.',
    ac: ['Use hmac.Equal for signature comparison.', 'Test covers valid, invalid and truncated signatures.'],
    files: ['internal/webhooks/verify.go', 'internal/webhooks/verify_test.go'],
    route: '/api/webhooks',
  },
  {
    body: 'Address autocomplete overwrites the apartment/suite field with an empty string when a suggestion is chosen.',
    ac: ['Apartment field is preserved when selecting a suggestion.', 'e2e covers manual apartment entry + autocomplete.'],
    files: ['src/features/checkout/AddressForm.tsx', 'e2e/checkout/address.spec.ts'],
    route: '/checkout/address',
  },
  {
    body: 'When the card network times out, three layers retry independently (client, gateway, worker), producing up to 27 attempts.',
    ac: ['Only the worker retries, with jittered backoff capped at 3 attempts.', 'Load test shows no retry amplification.'],
    files: ['internal/cards/client.go', 'internal/cards/retry.go', 'internal/worker/charge.go'],
    route: '/merchant/payments',
  },
  {
    body: 'Skeleton loaders on the cart drawer never clear when the API responds 304 Not Modified.',
    ac: ['304 responses resolve with cached data and clear the skeleton.'],
    files: ['src/lib/http/fetchJson.ts', 'src/lib/http/fetchJson.test.ts'],
    route: '/cart',
  },
  {
    body: 'Preorder products show "0 left" because the inventory badge does not check `availability = preorder`.',
    ac: ['Preorder items show "Preorder · ships Nov 12".', 'Badge unit tests cover in-stock, low, out, preorder.'],
    files: ['src/features/catalog/InventoryBadge.tsx', 'src/features/catalog/InventoryBadge.test.tsx'],
    route: '/p/alpine-shell-jacket',
  },
  {
    body: 'guest-checkout.spec.ts fails 1 in 6 runs on CI: the payment iframe sometimes loads after the test clicks "Pay".',
    ac: ['Test waits on the iframe ready message, not a timeout.', '50 consecutive green runs in the gate sandbox.'],
    files: ['e2e/checkout/guest-checkout.spec.ts', 'e2e/support/paymentFrame.ts'],
    route: '/checkout',
  },
  {
    body: 'Admin products table resets the sort to "updated" after an inline price edit because the edit invalidates the whole query key.',
    ac: ['Sort and filters persist after inline edit.', 'Only the edited row refetches.'],
    files: ['src/features/admin/ProductsTable.tsx', 'src/features/admin/useProducts.ts'],
    route: '/admin/products',
  },
]
