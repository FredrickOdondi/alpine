/**
 * Capture every screen in light and dark themes into ./screenshots/after (1440x900).
 * Usage: pnpm dev (in another shell), then
 *   CHROME_PATH=/usr/bin/google-chrome pnpm screenshots [baseUrl]
 */
import { mkdir } from 'node:fs/promises'
import { chromium } from 'playwright-core'

const base = process.argv[2] ?? 'http://localhost:5173'
const out = new URL('../screenshots/after/', import.meta.url).pathname
await mkdir(out, { recursive: true })

const browser = await chromium.launch({
  executablePath: process.env.CHROME_PATH ?? '/usr/bin/google-chrome',
  args: ['--no-sandbox'],
})

for (const theme of ['light', 'dark']) {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 1 })
  await ctx.addInitScript((t) => localStorage.setItem('alpine.theme', t), theme)
  const page = await ctx.newPage()
  page.on('console', (m) => m.type() === 'error' && console.error('[console]', m.text()))
  page.on('pageerror', (e) => console.error('[pageerror]', e.message))
  const shot = async (name) => {
    await page.waitForTimeout(700)
    await page.screenshot({ path: `${out}${name}-${theme}.png` })
    console.log(`${name}-${theme}.png`)
  }

  await page.goto(`${base}/login`)
  await page.getByRole('button', { name: 'Sign in with GitHub' }).waitFor()
  await shot('01-login')

  await page.getByRole('button', { name: 'Sign in with GitHub' }).click()
  await page.waitForURL('**/board')
  await page.locator('[data-ticket-id]').first().waitFor()
  await page.waitForTimeout(5500) // let a couple of live SSE events arrive
  await shot('02-board')

  await page.goto(`${base}/tickets/7a1c0000-0000-4000-8000-000000000015`)
  await page.getByRole('heading', { level: 2 }).first().waitFor()
  await shot('03-ticket-pr-open')

  await page.goto(`${base}/tickets/7a1c0000-0000-4000-8000-000000000023`)
  await page.getByRole('region', { name: /human/i }).waitFor()
  await shot('04-ticket-escalated')

  await page.goto(`${base}/inbox`)
  await page.locator('[data-nav-item]').first().waitFor()
  await page.getByRole('button', { name: /Expand Modal focus trap/ }).click()
  await page.locator('img').first().waitFor()
  await shot('05-pr-inbox')
  // Confirmation scales with blast radius: BR3 popover, BR4 typed confirm.
  const row = (re) => page.getByRole('row').filter({ hasText: re })
  await row(/N\+1 query/).getByRole('button', { name: 'Approve merge' }).click()
  await page.getByRole('dialog').waitFor()
  await shot('05b-pr-inbox-br3-confirm')
  await page.keyboard.press('Escape')
  await row(/Migrate ledger_entries/).getByRole('button', { name: 'Approve merge' }).click()
  await page.getByRole('dialog').waitFor()
  await shot('05c-pr-inbox-br4-typed-confirm')
  await page.keyboard.press('Escape')

  await page.goto(`${base}/metrics`)
  await page.locator('.recharts-surface').first().waitFor()
  await shot('06-metrics')

  await page.goto(`${base}/repos/4e900000-0000-4000-8000-000000000001/config`)
  await page.getByText('Merge policy').waitFor()
  await shot('07-repo-config')
  await page.getByRole('radio', { name: 'factory.yaml' }).click()
  await shot('08-repo-config-yaml')

  await page.goto(`${base}/board`)
  await page.locator('[data-ticket-id]').first().waitFor()
  await page.keyboard.press('Control+k')
  await page.getByRole('dialog').waitFor()
  await page.keyboard.type('checkout')
  await shot('09-command-palette')
  await page.keyboard.press('Escape')
  await page.keyboard.press('Shift+Slash')
  await page.getByRole('dialog', { name: 'Keyboard shortcuts' }).waitFor()
  await shot('10-shortcuts')

  await ctx.close()
}
await browser.close()
