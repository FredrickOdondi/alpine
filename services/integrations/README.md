# services/integrations

Go module `github.com/alpine-dev/alpine/services/integrations` (registered in the repo-root `go.work`).
Webhook **verification + normalization** for intake sources and the **outbound GitHub App client**
(see `docs/architecture.md` §4.2 and §9.1).

Scope boundary: the HTTP webhook receiver and the `inbox_events` table belong to `services/api`.
This module gives the receiver what it needs — an `intake.Verifier` and an `intake.Normalizer` per
source — and gives the orchestrator a least-privilege GitHub client.

```
internal/hmacsig/     constant-time hex HMAC-SHA256 verify (shared by GitHub + Sentry)
intakemeta/           names of the TicketIntake.Metadata keys this module sets
github/               GitHub App client: JWT, installation tokens (cache), branches, API push,
                      PRs + labels, check runs, comments
github/webhook/       intake.Verifier (X-Hub-Signature-256) + intake.Normalizer (issues, issue_comment)
sentry/               intake.Verifier (Sentry-Hook-Signature) + intake.Normalizer (issue, error, event_alert)
*/testdata/           recorded-style webhook fixtures (no secrets)
```

The packages are intentionally *not* under `internal/` so `services/api` (a separate module in the
workspace) can import the verifiers/normalizers.

## Intake contract (packages/shared/intake)

This module implements Jeff's contract from `packages/shared/intake` (merged from `jeff/core`):
`intake.Verifier` (`Source()`, `Verify(http.Header, []byte) error`) and `intake.Normalizer`
(`Source()`, `Normalize(ctx, eventType, payload) ([]intake.TicketIntake, error)`).

```go
reg := intake.Registry{
	intake.SourceGitHub: webhook.Normalizer{},
	intake.SourceSentry: sentry.Normalizer{},
}
// receiver (services/api): webhook.Verifier{Secret: s}.Verify(r.Header, body) -> 401 on error
// (errors match intake.ErrInvalidSignature and a specific reason), then store the inbox row.
// inbox processor: out, err := reg.Normalize(ctx, row.Source, row.EventType, row.Payload)
// errors.Is(err, intake.ErrIgnored) -> mark processed; ErrMalformedPayload -> surface.
```

Normalizers are pure (no I/O) and return exactly one `TicketIntake` on success. They do not know
the delivery id (`DeliveryID` is left for the processor to fill from the inbox row).

- `DedupeKey` — grouping key (GitHub: same as `ExternalID`; Sentry: see below).
- `Severity` — `intake.Severity` (`fatal|error|warning|info|debug`, empty if unknown).
- `Raw` — a private copy of the verbatim webhook payload (untrusted; PII-scrub before LLM use).
- `SentryProject` — Sentry project slug/id for repo mapping.
- The deprecated `Fingerprint` and `Level` fields are **not** populated.

Everything without a field goes into `Metadata` under the keys in `intakemeta` (`event_type`,
`source_action`, `kind`, provider ids, `github_display_ref`, and `alt_dedupe_key` — the shared
contract has no field for a secondary dedupe key). All text fields are **untrusted**
(architecture §9.2).

## GitHub event normalization

| Event / action | `Action` | `Metadata[kind]` |
| --- | --- | --- |
| `issues` opened | create | `issue` |
| `issues` edited, labeled, unlabeled, transferred | update | `issue` |
| `issues` closed, deleted | close | `issue` |
| `issues` reopened | reopen | `issue` |
| `issue_comment` created, edited (on an issue) | comment | `comment` |
| `issue_comment` created, edited (on a PR) | comment | `pull_request_comment` (+ `github_pull_number`) |

Ignored (`intake.ErrIgnored`): other actions (assigned, milestoned, pinned, …), deleted comments,
**comments by bot accounts** (`type: Bot` or `*[bot]` login — prevents Alpine reacting to its own
comments), and every other event (ping, pull_request, …).

`ExternalID` is `gh:<repo_id>#<number>` (`webhook.ExternalID`), built from the numeric repository
id so it survives repo renames and owner transfers; issue events and their comments share it.
`owner/repo#number` is kept only for display in `Metadata[github_display_ref]`, and the numeric
issue id in `Metadata[github_issue_id]`. Payloads without `repository.id` are rejected as malformed.
`Repo` carries provider, installation id, repo id, full name and default branch.
`DedupeKey` equals `ExternalID`. `Severity` comes from labels (case-insensitive, highest wins):
`critical`/`P0`→fatal, `high`/`P1`→error, `medium`/`P2`→warning, `low`/`P3`→info, each optionally
prefixed `severity:`/`sev:`/`priority:` (`:` or `/`); with a prefix the intake vocabulary itself
(`severity:fatal|error|warning|info|debug`) is accepted too. `Raw` is the whole payload. Bot-authored issues are kept but flagged
`Metadata[author_is_bot]=true`.

## Sentry normalization and dedupe key

| Resource / action | `Action` | `Metadata[kind]` |
| --- | --- | --- |
| `issue` created | create | `error_group` |
| `issue` assigned | update | `error_group` |
| `issue` resolved, archived, ignored | close | `error_group` |
| `issue` unresolved | reopen | `error_group` |
| `error` created | create | `error_event` |
| `event_alert` triggered | create | `error_event` (+ `rule:<name>` label) |

Other resources (installation, metric_alert, comment, …) and actions are `intake.ErrIgnored`.

- `ExternalID` is the **Sentry issue id** (group id) for every resource, so an issue webhook and
  all error events of that group link to one ticket; the event id is used only if Sentry omits the
  group. `Metadata[sentry_event_id]` keeps the event id.
- `Severity`, `Culprit`, `Release` and `StackFrames` (≤ 10, innermost first, in-app frames preferred,
  `function (file:line)`) are filled for triage's fingerprint + stack-frame match.
- `Repo` is left empty: mapping a Sentry project to a repository is source configuration; the
  processor uses `SentryProject` and `Metadata[sentry_installation]` (integration installation uuid).
- The rendered `Body` omits user, request and tag data (PII); the full payload is in `Raw`.

**Dedupe key** — written to `TicketIntake.DedupeKey`, in priority order:

1. **Custom fingerprint** — `sentry:fp:<project_id>:<sha256(project_id ‖ NUL ‖ fp[0] ‖ NUL ‖ fp[1] …)>`
   when the event's `fingerprint` is non-empty and contains **no** `{{ … }}` template variables
   (e.g. `{{ default }}`, `{{ transaction }}`), which Sentry expands server-side and therefore are not
   a stable identity on their own. Project-scoped because fingerprints are per project;
   NUL-delimited so `["ab"]` ≠ `["a","b"]`. `sentry:issue:<id>` is added as
   `Metadata[alt_dedupe_key]`.
2. **Issue / group id** — `sentry:issue:<id>` from `issue.id` (issue webhooks), `issue_id`, `group_id`
   or `groupID`. For default grouping the Sentry issue *is* the dedupe unit.
3. **Event id** — `sentry:event:<event_id>` as a last resort (no dedupe).

Issue webhooks carry no fingerprint, so they always use form 2; they still meet custom-fingerprint
events through the alt key (and the shared `ExternalID`). The custom-fingerprint key survives
Sentry issue merges/deletes where the group id changes. Callers must scope keys by `org_id`.
`Severity` is the Sentry level as-is (`fatal`, `error`, `warning`, `info`, `debug`; `sample`→debug);
when an issue has no level its `priority` is used (`high`→error, `medium`→warning, `low`→info).

## Signature verification

- GitHub: `X-Hub-Signature-256: sha256=<hex>` = HMAC-SHA256(webhook secret, raw body).
- Sentry: `Sentry-Hook-Signature: <hex>` = HMAC-SHA256(client secret, raw body).

Both decode the hex and compare MACs with `hmac.Equal` (constant time); missing, malformed and
mismatching signatures are distinct errors, and `Verifier.Verify` wraps each with
`intake.ErrInvalidSignature`. An empty secret always fails closed. Receivers should cap the body
(`webhook.MaxPayloadBytes` = 25 MB, `sentry.MaxPayloadBytes` = 5 MB) and verify the **raw** bytes.

## GitHub App client

```go
app, _ := github.NewApp(github.AppConfig{AppID: id, BotLogin: "alpine-dev[bot]", PrivateKey: key /* github.ParsePrivateKeyPEM */})
c, _ := app.RepoClient(installationID, "acme", "shop", github.Merge(github.PermsPush(), github.PermsPullRequests()))
name, _ := github.NewBranchName(ticket.ID, ticket.Title) // alpine/01J9ZKX3M4Q7R8S9T0V1W2X3Y4-fix-checkout-total
br, _ := c.CreateBranchFrom(ctx, name, "main") // returns the existing branch on a retried activity
sha, _ := c.PushFiles(ctx, github.Push{Branch: name, Message: "fix: …", ExpectedHeadSHA: br.SHA, Changes: …})
pr, _ := c.CreatePullRequest(ctx, github.NewPullRequest{Title: …, Head: name, Base: "main", Body: …, Labels: []string{"alpine"}})
cm, _ := c.CreateIssueComment(ctx, 42, "triage:"+ticketID, "Triaged as …") // keyed: posted at most once
```

- **App JWT**: RS256, `iss` = App ID, `iat` = now − 60 s, `exp` = now + 9 min (GitHub max 10 min).
- **Installation tokens**: `POST /app/installations/{id}/access_tokens` with exactly one of
  `repositories: [name]` / `repository_ids: [id]` and an explicit `permissions` subset (requests
  without permissions, or asking for `workflows`/`administration`/`secrets`, are refused locally).
  Cached per (installation, repo, permission set); refreshed when < 5 min remain; concurrent callers
  share one refresh. `InvalidateToken` after a 401. Tokens are redacted from `fmt` output.
- **Push via API only** (no git binary): ref → parent commit → blobs → tree (`base_tree`, `sha:null`
  deletes) → commit → fast-forward ref update (`force:false`, so concurrent pushes fail with 422).
  Paths are validated (no absolute, `..`, `.git`).
- **Branch guard**: `CreateBranch`, `UpdateRef`, `PushFiles` only touch `alpine/*` by default
  (`ClientConfig.WritableBranchPrefixes`), per architecture §9.1.

### Branch names

Alpine branches are `alpine/<ticket_ulid>-<slug>`, one per ticket:

- **ULID:** 26 characters of Crockford base32 in canonical **uppercase** (`0-9A-HJKMNP-TV-Z`, no
  `I L O U`), first character `0`–`7` (fits 128 bits). Lowercase is **rejected** in branch names
  so a ticket can never end up with two branches differing only in case; `NewBranchName` accepts
  a lowercase ULID and uppercases it.
- **Slug:** 1–48 (`MaxSlugLen`) lowercase ASCII letters/digits in hyphen-separated words (no
  leading, trailing or double hyphens). It is cosmetic; the ULID is the identity. Max full name:
  82 bytes.
- `NewBranchName(ulid, title)` builds the name using `Slugify(title)`, which lowercases the title,
  folds common Latin accents (`Café` → `cafe`, `ß` → `ss`), turns everything else into
  separators, cuts at a word boundary near 48 characters, and falls back to `ticket` when nothing
  usable is left (e.g. a CJK-only title). `ParseBranchName` / `ValidateBranchName` /
  `ValidULID` / `ValidSlug` are exported for the orchestrator.
- **Enforced on every write:** `CreateBranch`, `CreateBranchFrom`, `UpdateRef`, `PushFiles` and the
  `CreatePullRequest` head reject any `alpine/…` name that does not match (`ErrInvalidBranchName`),
  even when `WritableBranchPrefixes` is widened. All written branch names, plus the base of
  `CreateBranchFrom`/`CreatePullRequest`, must also pass git's `check-ref-format` rules (no `..`,
  `//`, `@{`, control chars, space, `~^:?*[\`, components starting with `.` or ending `.lock`,
  ≤ 255 bytes) and be short names (not `refs/…`).
- PRs (create/update, title/body/state/base, add/replace/remove labels), check runs (create/update,
  output clamped to GitHub's 65 535-char limit), issue/PR comments (create/update).
- `BaseURL` is configurable (tests use `httptest`; GHES uses `https://host/api/v3`). Non-2xx
  responses become `*github.APIError` (`github.StatusCode(err)`, `IsRateLimited`, `IsAmbiguous`).

### Retries and rate limits (`RetryPolicy`)

| Failure | Idempotent calls (GET/PATCH/PUT/DELETE, blobs/trees/commits, token mint) | Creates (branch, PR, comment, check run) |
| --- | --- | --- |
| 5xx | retry, exponential backoff with jitter (Retry-After honoured) | re-check, then retry only if absent |
| Transport error / timeout (`TransportError`) | retry with backoff | re-check, then retry only if absent |
| 429, or 403 with `Retry-After` / `X-RateLimit-Remaining: 0` / "rate limit" message | retry | retry (GitHub did not apply it) |
| other 4xx | fail | fail (422 on branch/PR triggers a re-check) |

- Backoff: `BaseDelay` (500 ms) doubling per attempt, capped at `MaxDelay` (30 s), with equal jitter
  (wait ∈ [d/2, d)). `MaxAttempts` (4) counts the first attempt.
- Rate-limit waits: `Retry-After` (seconds or HTTP date); else, when `X-RateLimit-Remaining: 0`,
  until `X-RateLimit-Reset` + 1 s; else (secondary limit without headers) at least 1 minute per
  GitHub guidance. Any wait above `MaxWait` (2 min) fails fast with the rate-limit error so the
  Temporal activity can be rescheduled instead of blocking a worker.
- `Now`, `Sleep` and `Rand` are injectable on `AppConfig`/`ClientConfig` (tests never really sleep);
  the App passes them on to `RepoClient`. Each App-authenticated attempt signs a fresh JWT.

### Idempotency (no duplicate branches, PRs or comments)

- **Branches:** `CreateBranch`/`CreateBranchFrom` first `GET git/ref/heads/<branch>`; an existing
  branch is returned with `Created=false` (its current SHA, which may differ — e.g. after pushes).
  A 422 "Reference already exists" or an ambiguous failure is re-checked.
- **Pull requests:** `CreatePullRequest` requires an `alpine/<ulid>-<slug>` head, lists open PRs for that head
  (`head=owner:branch`) and, if one exists, PATCHes only differing title/body/base and returns it
  with `Reused=true`. Ambiguous failures and 422 "already exists" are re-checked the same way.
  Labels are added with the idempotent labels API. (Draft state is not changed on reuse.)
- **Comments:** with a non-empty key, the body ends with a hidden marker
  `<!-- alpine:idempotency-key=<key> -->` (key: 1–128 of `[A-Za-z0-9._:/@+=-]`, no `--`). Before
  posting, all comments are listed (paginated) and **our own App's** comment carrying the marker
  is returned (`Reused=true`). A comment counts as ours only if `user.type == "Bot"`,
  `user.login` equals `BotLogin` (case-insensitive, like GitHub logins) and, when the payload has
  `performed_via_github_app`, its `id` equals `AppID`. Markers in comments by people or by other
  bots are ignored, so nobody else can suppress Alpine's comment. If the listing hits the page cap the call fails with
  `ErrPageCapReached` instead of risking a duplicate. `UpdateIssueComment(id, key, body)` keeps
  the marker. With an empty key the comment is posted once and ambiguous errors are returned.
- **Bot identity (`AppID`, `BotLogin`):** set on `ClientConfig`; `App.RepoClient` fills them from
  `AppConfig.AppID` and `AppConfig.BotLogin`. `BotLogin` must look like `<app-slug>[bot]` and
  `AppID` must not be negative (checked by `NewApp`/`NewClient`). Both are **optional for the
  client as a whole but required for keyed comments**: `CreateIssueComment` with a key and
  `FindIssueCommentByKey` fail fast with `ErrBotIdentityRequired` (before any request) if either
  is missing, rather than fall back to trusting any bot. Unkeyed comments and all other calls work
  without them. Configure `BotLogin` from the App's settings page (slug) or `GET /app` at deploy
  time.
- **Check runs:** with an `ExternalID`, an existing run on `HeadSHA` with the same name and
  external id is returned; ambiguous creates are re-checked. With an `AppID` configured, only
  runs whose `app.id` matches are reused (another App's run could not be updated by us); without
  one, any matching run is accepted.

### Pagination

`ListPullRequests` (state/head/base), `ListIssueComments` and `ListCheckRunsForRef`
(check_name/status/filter) follow `Link: rel="next"` with `ListOptions{PerPage ≤ 100 (100),
MaxPages (10)}`. When more pages remain at the cap, the items fetched so far are returned together
with `ErrPageCapReached`. Next links must stay on the client's base URL, so the token is never
sent to another origin.

### Least-privilege GitHub App permissions

App-level ceiling (what the App registration requests). Each activity then mints a token with only
the subset it needs (`PermsPush`, `PermsPullRequests`, `PermsChecks`, `PermsIssueComments`).

| Permission | Level | Used for | Justification |
| --- | --- | --- | --- |
| Metadata | read | everything | Mandatory for every GitHub App; repo lookup. |
| Contents | read & write | `CreateBranch`, `PushFiles`, `UpdateRef`, clone via credential proxy | Git Data API (refs/blobs/trees/commits) requires contents:write. Writes are confined to `alpine/*` by the client and should be backed by a repo ruleset. |
| Pull requests | read & write | `CreatePullRequest`, `UpdatePullRequest`, PR labels, PR comments | Opening/editing PRs. GitHub accepts pull_requests:write for labels/comments on PRs, so the PR flow does not need issues:write. |
| Checks | read & write | `CreateCheckRun`, `UpdateCheckRun` | Publishing gate results as check runs; only Apps can create check runs. |
| Issues | read & write | issue intake webhooks, triage comments + labels on issues | Webhooks need issues:read; write is needed to comment back / label (MVP step 3). Could drop to **read** for read-only-intake design partners (MVP week 10). |
| Workflows | none | — | Agents must never modify CI (`.github/workflows/**`); also refused by the client. |
| Administration, Secrets | none | — | Never needed; refused by the client. |

Architecture §9.1 additionally lists *Commit statuses: read* and *Actions: read* (optional) for
reading customer CI; nothing in this module uses them yet.

Webhook subscriptions needed for this module: `issues`, `issue_comment` (plus `pull_request`,
`check_suite`, `installation` for other consumers per §4.2).

## Testing

```
cd services/integrations
go vet ./... && go test -cover ./...
```

No network: GitHub calls hit `net/http/httptest` servers asserting method, path, auth headers and
JSON bodies; RSA keys are generated per test run; webhook fixtures live in `testdata/`.

## Open items

- The secondary Sentry dedupe key lives in `Metadata[alt_dedupe_key]` until the shared contract
  grows a field for it.
- Re-check after an ambiguous create relies on GitHub's read-after-write consistency for list
  endpoints; a lagging list could in theory still allow one duplicate.
- Sentry `Sentry-Hook-Timestamp` is not covered by the signature, so no replay window is enforced;
  rely on inbox idempotency (`Request-ID`).
