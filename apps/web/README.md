# @alpine/web

The Alpine dashboard: a Vite + React + TypeScript single-page app (no Next.js).

## Stack

- **Vite 8**, **React 19**, **TypeScript 6** (strict)
- **TanStack Router** (code-based routes in `src/router.tsx`) and **TanStack Query** for server state
- **Tailwind CSS v4** with **shadcn/ui** components (Radix primitives) retuned to Alpine's tokens
- **MSW 2** for REST and SSE mocks; **Recharts** for metrics; **cmdk** for the command palette
- Icons: **lucide** only, at stroke width 1.75. Font: **Inter Variable**, self-hosted via `@fontsource-variable`

## Run it

From the repo root (pnpm workspace):

```bash
pnpm install
pnpm dev          # http://localhost:5173, against the mocks
pnpm lint
pnpm typecheck
pnpm build        # outputs apps/web/dist
```

Or run the same scripts inside `apps/web` (`pnpm dev`, `pnpm lint`, `pnpm typecheck`, `pnpm build`, `pnpm preview`).

Sign in with the **Sign in with GitHub** button. In mock mode the OAuth round-trip is simulated.

Keyboard: `?` opens the shortcuts sheet and `⌘K` / `Ctrl+K` opens the command palette. On the board,
`J`/`K` move within a column and `H`/`L` move between columns (one roving tab stop, so `Tab` enters and
leaves the board). In the PR inbox, `J`/`K` move between rows, `X` expands, `A` approves the merge,
`R` requests changes and `O` opens the ticket. `G` then `B`/`I`/`M`/`C` jumps to Pipeline, Inbox,
Metrics or Config. `[` collapses the sidebar. `Esc` leaves a ticket.

Approving a merge scales friction with blast radius: BR1–2 commit after a 6s undo toast, BR3 asks
in a popover that lists the files, and BR4 needs you to type the repository name.

## Screens

| Route | Screen |
| --- | --- |
| `/login` | GitHub OAuth sign-in |
| `/board` | Pipeline board: one column per stage (triage, refinement, prioritize, implement, gates, review, merge, escalated). Updates live over SSE |
| `/tickets/:id` | Ticket detail: stage history, agent runs (turns, tokens, cost), gate results per iteration, evidence (repro video, screenshots, traces), reviewer findings, linked PR, human actions |
| `/inbox` | PR inbox sorted by blast radius, then priority, then age. Rows expand to before/after visual diffs (side by side, swipe, heatmap) and changed files. Approve and send-back are optimistic |
| `/metrics` | Turns p50/p90, cost, gate pass rate (overall and per gate), top failure reasons |
| `/repos/:id/config` | `.alpine/factory.yaml`: structured overview plus the raw file |

## API client and mocks

All API shapes live in `src/api/types.ts`. Spec types are re-exported from `@alpine/api-client`, which is generated from `api/openapi.yaml` on jeff/core (merged into tess/web). Spec endpoints go through the generated openapi-fetch client in `src/api/client.ts`.

Spec endpoints used (camelCase, UUID ids, integer blast radius 1–4):

- Auth: `GET /v1/auth/providers`, `GET /v1/auth/github/login?redirect_to`, `POST /v1/auth/dev-login`, `POST /v1/auth/logout`, `GET /v1/me`.
- Repos and tickets: `GET /v1/repos`, `GET /v1/tickets?repoId&stage&limit`, `GET /v1/tickets/{id}`, `POST /v1/tickets/{id}/actions` (approve/reject/resume/close/mark_merged → 202), `GET /v1/tickets/{id}/gate-results`, `GET /v1/runs/{runId}/turns`.
- PRs: `GET /v1/pull-requests?state`.
- Live updates: `GET /v1/events?repoId` (SSE, `BoardEvent`).

`@proposed` endpoints are NOT in the spec yet. They are mocked here and should be added to `api/openapi.yaml`:

- `GET /v1/tickets/{id}/artifacts`: repro video, screenshots, traces.
- `GET /v1/tickets/{id}/review`: reviewer verdict and findings.
- `GET /v1/pull-requests/{id}/evidence`: diff stats, files, gate summary, visual diffs, human decision.
- `POST /v1/pull-requests/{id}/review`.
- `GET /v1/metrics?window&repoId`.
- `GET /v1/repos/{id}/config`: parsed `.alpine/factory.yaml`.

Mock implementation:

- `src/mocks/db.ts`: a seeded, deterministic world (Kilima Commerce; `kilima/storefront-web`, `kilima/payments-api`; 26 tickets across the spec stages) plus a simulator. The simulator advances one ticket every 2.5s and broadcasts spec `BoardEvent`s.
- `src/mocks/handlers.ts`: MSW handlers for every endpoint above, plus an `sse()` handler on `/v1/events`. Every non-auth endpoint returns 401 until you sign in (the mock session lives in localStorage).
- Evidence is per ticket. `src/mocks/images.ts` draws one scene per seeded bug (before, after, repro and
  heatmap variants), and each storefront ticket has its own repro video and poster frame in
  `public/mock-artifacts/repro-<n>.webm|jpg`. To regenerate them, run `pnpm dev` and then
  `node scripts/gen-repro-videos.mjs http://localhost:5173` (needs Chrome and ffmpeg).
- Fallback: if the service worker can't start, requests resolve in-page (`src/mocks/inline.ts`) and the board stream uses `subscribeInline` (`src/mocks/mock-event-source.ts`).
- Board columns: the 14 spec stages map onto the 8 board columns via `COLUMN_FOR_STAGE`:
  - intake goes to triage;
  - escalated and needs_human are pinned in a "Needs you" lane on the left;
  - merge_policy, pr_open and done go to merge;
  - closed and duplicate are hidden.

## Switching to the real API

1. Copy `.env.example` to `.env.local` and set:
   ```
   VITE_API_MODE=live
   VITE_API_BASE_URL=            # empty = same origin
   ```
   Leave the base URL empty and serve the SPA same-origin with the API, or add a Vite `server.proxy` for `/v1`. The session is an HTTP-only cookie, so cross-origin needs CORS with credentials.
2. In live mode MSW isn't loaded. "Sign in with GitHub" redirects to `GET /v1/auth/github/login`. When `/v1/auth/providers` reports `devLogin: true`, a "Continue with dev login" button also appears.
3. The board subscribes with `subscribeBoardEvents` from `@alpine/api-client`.
4. Screens that rely on `@proposed` endpoints show empty states (artifacts, review) or "—" (inbox gates/diff) until the server implements them. Metrics and config show an error state with retry.

## Design system

See [`src/design/README.md`](src/design/README.md). It covers tokens (light and dark), the type scale,
the 4px grid, the one accent colour, and semantic stage and blast-radius colours.

## Screenshots

`screenshots/before/` is the state reviewed in the design critique (commit `1d38d90`), and
`screenshots/after/` is the current build. Both are 1440×900, light and dark. To regenerate `after/`,
run `pnpm dev` and then `pnpm screenshots [baseUrl]` (needs Chrome at `/usr/bin/google-chrome` or
`CHROME_PATH`).
