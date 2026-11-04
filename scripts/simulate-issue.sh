#!/usr/bin/env bash
# Simulates the IssueOps GitHub Action locally: renders an issue-form body,
# converts it with the same parser the Action uses, and POSTs it to the factory.
# Usage: scripts/simulate-issue.sh [app-name] [vault|email] [org]
set -euo pipefail
cd "$(dirname "$0")/.."

NAME="${1:-acme-ci-bot-$RANDOM}"
STORAGE="${2:-vault}"
ORG="${3:-my-org}"
URL="${FACTORY_URL:-http://localhost:3000}"
TOKEN="${INGEST_TOKEN:-dev-ingest-token}"

ISSUE_BODY=$(cat <<EOF
### App name

${NAME}

### Description

CI bot that comments on pull requests (requested via IssueOps)

### Owner type

org

### Organization

${ORG}

### Homepage URL

https://github.com/${ORG}/${NAME}

### Webhook URL

https://hooks.example.com/${NAME}

### Permissions

contents: read
pull_requests: write
metadata: read

### Webhook events

- [X] push
- [X] pull_request
- [ ] issues
- [ ] issue_comment
- [ ] workflow_run
- [ ] release

### Secret delivery

${STORAGE}

### Delivery email

team-${NAME}@example.com
EOF
)

PAYLOAD=$(printf '%s' "$ISSUE_BODY" | node scripts/issue-to-request.js)
echo "→ POST $URL/api/ingest"
curl -sS -X POST "$URL/api/ingest" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d "$PAYLOAD"
echo
