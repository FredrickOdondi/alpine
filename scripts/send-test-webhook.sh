#!/usr/bin/env bash
# Posts a signed fake GitHub `issues.opened` webhook to the local API (no GitHub involved).
# Usage: scripts/send-test-webhook.sh [issue-number] [title]
set -euo pipefail
N="${1:-1}"
TITLE="${2:-Login button does nothing on Safari}"
API="${API_URL:-http://localhost:8080}"
SECRET="${GITHUB_WEBHOOK_SECRET:-dev-webhook-secret}"
INSTALLATION="${SEED_GITHUB_INSTALLATION_ID:-42}"
REPO_ID="${SEED_GITHUB_REPO_ID:-777}"
REPO="${SEED_GITHUB_REPO:-acme/widgets}"
BODY=$(cat <<JSON
{"action":"opened","installation":{"id":$INSTALLATION},
 "repository":{"id":$REPO_ID,"full_name":"$REPO","default_branch":"main"},
 "issue":{"id":$((900000 + N)),"number":$N,"title":"$TITLE","body":"Steps: open /login in Safari 17, click Sign in. Expected: login. Actual: nothing.",
  "html_url":"https://github.com/$REPO/issues/$N","user":{"login":"jane","type":"User"},"labels":[{"name":"bug"}]}}
JSON
)
SIG="sha256=$(printf '%s' "$BODY" | openssl dgst -sha256 -hmac "$SECRET" -r | cut -d' ' -f1)"
curl -sS -X POST "$API/v1/webhooks/github" \
  -H "Content-Type: application/json" -H "X-GitHub-Event: issues" \
  -H "X-GitHub-Delivery: $(uuidgen 2>/dev/null || cat /proc/sys/kernel/random/uuid)" \
  -H "X-Hub-Signature-256: $SIG" --data-binary "$BODY"
echo
