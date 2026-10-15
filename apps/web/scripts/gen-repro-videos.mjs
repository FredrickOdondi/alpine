/**
 * Render one repro video + poster per storefront ticket into public/mock-artifacts/
 * (repro-<contentIndex>.webm / .jpg) from the per-ticket SVG scenes in src/mocks/images.ts.
 * Needs `pnpm dev` running and ffmpeg. Usage: node scripts/gen-repro-videos.mjs [baseUrl]
 * Frames: page in its broken state → cursor moves to the failing element → annotated failure.
 */
import { execFileSync } from 'node:child_process'
import { mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { chromium } from 'playwright-core'

const base = process.argv[2] ?? 'http://localhost:5173'
const outDir = new URL('../public/mock-artifacts/', import.meta.url).pathname
const browser = await chromium.launch({ executablePath: process.env.CHROME_PATH ?? '/usr/bin/google-chrome', args: ['--no-sandbox'] })
const page = await browser.newPage({ viewport: { width: 640, height: 400 } })
await page.goto(`${base}/login`)

const scenes = await page.evaluate(async () => {
  const db = await import('/src/mocks/db.ts')
  const { CONTENT } = await import('/src/mocks/content.ts')
  const img = await import('/src/mocks/images.ts')
  const seen = new Set()
  const out = []
  for (const t of db.tickets) {
    if (t.repoId !== db.STOREFRONT) continue
    const i = t.n % CONTENT.length
    if (seen.has(i)) continue
    seen.add(i)
    const route = CONTENT[i].route
    out.push({ i, before: img.ticketShot(i, route, 'before'), repro: img.ticketShot(i, route, 'repro'), focus: img.ticketFocus(i, route) })
  }
  return out
})

const FRAMES = 40
const BREAK = 24
for (const s of scenes) {
  const dir = await mkdtemp(join(tmpdir(), `alpine-repro-${s.i}-`))
  for (let f = 0; f < FRAMES; f++) {
    const p = Math.min(1, f / (BREAK - 4))
    const ease = 1 - (1 - p) ** 3
    const x = 90 + (s.focus.x - 90) * ease
    const y = 370 + (s.focus.y - 370) * ease
    const src = f < BREAK ? s.before : s.repro
    await page.setContent(`<body style="margin:0;overflow:hidden"><img src="${src}" width="640" height="400" style="display:block"/>
      <svg style="position:absolute;left:${x}px;top:${y}px" width="16" height="22" viewBox="0 0 16 22"><path d="M1 1 L1 17 L5 13 L8 20 L11 19 L8 12 L14 12 Z" fill="#111" stroke="#fff" stroke-width="1.2"/></svg></body>`)
    await page.screenshot({ path: join(dir, `f${String(f).padStart(3, '0')}.png`) })
    if (f === FRAMES - 1) await page.screenshot({ path: join(outDir, `repro-${s.i}.jpg`), type: 'jpeg', quality: 82 })
  }
  execFileSync('ffmpeg', ['-loglevel', 'error', '-y', '-framerate', '10', '-i', join(dir, 'f%03d.png'), '-c:v', 'libvpx-vp9', '-b:v', '90k', '-pix_fmt', 'yuv420p', join(outDir, `repro-${s.i}.webm`)])
  await rm(dir, { recursive: true, force: true })
  console.log(`repro-${s.i}`)
}
await browser.close()
