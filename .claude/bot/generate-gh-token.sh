#!/bin/bash
# Generates a short-lived GitHub App installation token for gh CLI / git push.
# Tokens expire after 1 hour. Safe to call repeatedly — cheap and stateless.
#
# This script is committed to a PUBLIC repo, so it carries NO App-specific
# identifiers. The App ID, Installation ID, and private-key path are read from a
# gitignored sibling config file, .claude/bot/bot.env (copy bot.env.example and
# fill it in), or from the environment ($BOT_APP_ID / $BOT_INSTALL_ID /
# $BOT_PEM_FILE). The private key itself must live OUTSIDE the repo (e.g.
# ~/.claude/) and is never committed.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENV_FILE="$SCRIPT_DIR/bot.env"
if [ -f "$ENV_FILE" ]; then
  # shellcheck disable=SC1090
  . "$ENV_FILE"
fi

APP_ID="${BOT_APP_ID:-}"
INSTALL_ID="${BOT_INSTALL_ID:-}"
PEM_FILE="${BOT_PEM_FILE:-}"

if [ -z "$APP_ID" ] || [ -z "$INSTALL_ID" ] || [ -z "$PEM_FILE" ]; then
  echo "Error: missing bot config. Copy .claude/bot/bot.env.example to" >&2
  echo "       .claude/bot/bot.env and fill in BOT_APP_ID / BOT_INSTALL_ID /" >&2
  echo "       BOT_PEM_FILE (or set them in the environment)." >&2
  exit 2
fi

if [ ! -r "$PEM_FILE" ]; then
  echo "Error: private key not found or not readable: $PEM_FILE" >&2
  exit 3
fi

# Generate JWT
NOW=$(date +%s)
IAT=$((NOW - 60))
EXP=$((NOW + 600))

HEADER=$(printf '{"alg":"RS256","typ":"JWT"}' | openssl base64 -e -A | tr '+/' '-_' | tr -d '=')
PAYLOAD=$(printf '{"iat":%d,"exp":%d,"iss":"%s"}' "$IAT" "$EXP" "$APP_ID" | openssl base64 -e -A | tr '+/' '-_' | tr -d '=')
SIGNATURE=$(printf '%s.%s' "$HEADER" "$PAYLOAD" | openssl dgst -sha256 -sign "$PEM_FILE" -binary | openssl base64 -e -A | tr '+/' '-_' | tr -d '=')
JWT="${HEADER}.${PAYLOAD}.${SIGNATURE}"

# Exchange JWT for installation token
TOKEN=$(curl -s -X POST \
  -H "Authorization: Bearer $JWT" \
  -H "Accept: application/vnd.github+json" \
  "https://api.github.com/app/installations/$INSTALL_ID/access_tokens" \
  | jq -r '.token')

if [ "$TOKEN" = "null" ] || [ -z "$TOKEN" ]; then
  echo "Failed to generate installation token" >&2
  exit 1
fi

echo "$TOKEN"
