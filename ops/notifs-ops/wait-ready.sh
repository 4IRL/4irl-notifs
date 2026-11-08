#!/usr/bin/env bash
# Polls delivery-api /readyz until it answers HTTP 200 with status ok.
# usage: wait-ready.sh <readyz-url> <timeout-secs> [interval-secs]   (interval defaults to 10)
# Env: NOTIFS_OPS_ACCESS_CLIENT_ID, NOTIFS_OPS_ACCESS_CLIENT_SECRET (Cloudflare Access service token).
# Exit: 0 ready, 1 timeout, 2 usage error or access error (3xx/403). Redirects are never followed.
set -euo pipefail
umask 077

if [ "$#" -lt 2 ] || [ "$#" -gt 3 ]; then
  echo "usage: wait-ready.sh <readyz-url> <timeout-secs> [interval-secs]" >&2
  exit 2
fi
url="$1"
timeout_secs="$2"
interval="${3:-10}"

if [ -z "${NOTIFS_OPS_ACCESS_CLIENT_ID:-}" ] || [ -z "${NOTIFS_OPS_ACCESS_CLIENT_SECRET:-}" ]; then
  echo "wait-ready: NOTIFS_OPS_ACCESS_CLIENT_ID and NOTIFS_OPS_ACCESS_CLIENT_SECRET are required" >&2
  exit 2
fi
if [[ ! "$timeout_secs" =~ ^[0-9]+$ ]] || [[ ! "$interval" =~ ^[0-9]+([.][0-9]+)?$ ]]; then
  echo "wait-ready: timeout and interval must be numbers" >&2
  exit 2
fi

workdir="$(mktemp -d)"
trap 'rm -rf -- "$workdir"' EXIT
config="$workdir/curl.conf"
body="$workdir/body"
printf 'header = "CF-Access-Client-Id: %s"\nheader = "CF-Access-Client-Secret: %s"\n' \
  "$NOTIFS_OPS_ACCESS_CLIENT_ID" "$NOTIFS_OPS_ACCESS_CLIENT_SECRET" >"$config"

start="$(date +%s)"
attempt=0
while true; do
  attempt=$((attempt + 1))
  code="000"
  if code="$(curl -sS -K "$config" --max-time 15 -o "$body" -w '%{http_code}' "$url" 2>/dev/null)"; then
    case "$code" in
      200)
        if grep -Eq '"status"[[:space:]]*:[[:space:]]*"ok"' "$body"; then
          echo "wait-ready: ready after ${attempt} attempt(s)"
          exit 0
        fi
        echo "wait-ready: attempt ${attempt}: HTTP 200 but status not ok"
        ;;
      3??|403)
        echo "wait-ready: attempt ${attempt}: HTTP ${code} (Cloudflare Access refused the service token); not retrying" >&2
        exit 2
        ;;
      *)
        echo "wait-ready: attempt ${attempt}: HTTP ${code}"
        ;;
    esac
  else
    echo "wait-ready: attempt ${attempt}: curl failed"
  fi
  now="$(date +%s)"
  if [ $((now - start)) -ge "$timeout_secs" ]; then
    echo "wait-ready: not ready after ${timeout_secs}s" >&2
    exit 1
  fi
  sleep "$interval"
done
