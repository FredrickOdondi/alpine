#!/usr/bin/env bash
# Posts a signed fake Sentry `issue.created` webhook to the local API (no Sentry involved).
# Routing uses the seeded sentry_installations / sentry_project_repos rows (`make seed`).
# Usage: scripts/send-test-sentry-webhook.sh [issue-number]
set -euo pipefail
N="${1:-1}"
API="${API_URL:-http://localhost:8080}"
SECRET="${SENTRY_CLIENT_SECRET:-dev-sentry-secret}"
INSTALLATION="${SEED_SENTRY_INSTALLATION:-dev-sentry-installation}"
PROJECT="${SEED_SENTRY_PROJECT:-web-frontend}"
ID=$((1000000000 + N))
BODY=$(cat <<JSON
{"action":"created","installation":{"uuid":"$INSTALLATION"},
 "data":{"issue":{"id":"$ID","shortId":"WEB-$N","title":"TypeError: Cannot read properties of undefined (reading 'total')",
  "culprit":"src/checkout/Cart.tsx in computeTotal","permalink":"https://example.sentry.io/issues/$ID/",
  "web_url":"https://example.sentry.io/issues/$ID/","level":"error","status":"unresolved","platform":"javascript",
  "project":{"id":"1","name":"$PROJECT","slug":"$PROJECT","platform":"javascript-react"},
  "metadata":{"type":"TypeError","value":"Cannot read properties of undefined (reading 'total')","filename":"src/checkout/Cart.tsx","function":"computeTotal"},
  "issueType":"error","issueCategory":"error","priority":"high","count":"3","userCount":3,
  "firstSeen":"2026-10-14T20:56:00Z","lastSeen":"2026-10-14T20:58:00Z"}},
 "actor":{"type":"application","id":"sentry","name":"Sentry"}}
JSON
)
SIG="$(printf '%s' "$BODY" | openssl dgst -sha256 -hmac "$SECRET" -r | cut -d' ' -f1)"
curl -sS -X POST "$API/v1/webhooks/sentry" \
  -H "Content-Type: application/json" -H "Sentry-Hook-Resource: issue" \
  -H "Sentry-Hook-Timestamp: $(date +%s)" \
  -H "Request-ID: $(uuidgen 2>/dev/null || cat /proc/sys/kernel/random/uuid)" \
  -H "Sentry-Hook-Signature: $SIG" --data-binary "$BODY"
echo
