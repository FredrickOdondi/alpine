#!/usr/bin/env bash
# Replays a REAL GitHub issue into the local API as a signed `issues.opened` webhook, using the
# gh CLI to fetch it (useful without a GitHub App webhook pointing at your machine).
# Usage: scripts/send-issue-webhook.sh owner/repo issue-number
set -euo pipefail
REPO="${1:?owner/repo}"; N="${2:?issue number}"
API="${API_URL:-http://localhost:8080}"
SECRET="${GITHUB_WEBHOOK_SECRET:-dev-webhook-secret}"
INSTALLATION="${SEED_GITHUB_INSTALLATION_ID:-42}"
REPO_JSON=$(gh api "repos/$REPO" --jq '{id, full_name, default_branch, private}')
ISSUE_JSON=$(gh api "repos/$REPO/issues/$N")
BODY=$(jq -cn --argjson repo "$REPO_JSON" --argjson issue "$ISSUE_JSON" --argjson inst "$INSTALLATION" \
  '{action:"opened", installation:{id:$inst}, repository:$repo, issue:$issue, sender:$issue.user}')
SIG="sha256=$(printf '%s' "$BODY" | openssl dgst -sha256 -hmac "$SECRET" -r | cut -d' ' -f1)"
curl -sS -X POST "$API/v1/webhooks/github" \
  -H "Content-Type: application/json" -H "X-GitHub-Event: issues" \
  -H "X-GitHub-Delivery: $(uuidgen 2>/dev/null || cat /proc/sys/kernel/random/uuid)" \
  -H "X-Hub-Signature-256: $SIG" --data-binary "$BODY"
echo
