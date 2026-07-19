#!/bin/bash
# gh-app-push.sh — safely push a branch to https://github.com/4IRL/4irl-notifs.git as 4irl-notifs-claude[bot].
#
# Why this exists:
#   The calling environment may carry a stale/unrelated GH_TOKEN (e.g. a
#   personal PAT, or a different project's bot token). The conventional
#   one-liner
#       GH_TOKEN=$(token-gen) git ... "https://x-access-token:$GH_TOKEN@..."
#   has a bash evaluation bug: the parent shell expands $GH_TOKEN inside the
#   URL BEFORE the command-prefix assignment takes effect, so git would push
#   with whatever was already in the environment — not the fresh App token.
#   This script sidesteps the issue by storing the fresh token in a
#   differently-named local variable (APP_TOKEN) and unsetting GH_TOKEN so no
#   subprocess accidentally inherits a stale value.
#
#   The token is also never embedded in the remote URL. Instead it's handed to
#   git via GIT_ASKPASS (gh-app-askpass.sh), which reads it from APP_TOKEN.
#   This means the token never appears in anything git prints — push progress
#   lines, the upstream-tracking message, `git remote -v` — which matters
#   because whatever a command prints is visible to whatever ran it (e.g. an
#   agent's tool output), not just to whoever's at the keyboard.
#
# Usage:
#   .claude/bot/gh-app-push.sh [branch-name]
#
# Arguments:
#   branch-name  Optional. Defaults to the current branch. Must not be
#                main or master. Must not contain any force-push flag.
#
# Exit codes:
#   0  Push succeeded.
#   2  Bad arguments (too many, or detached HEAD with no branch given).
#   3  Refused: target is main/master or a force flag was supplied.
#   4  Token generator or askpass helper missing or not executable.
#   5  Token generator returned an invalid value.
#   other  git push failed (its own exit code is propagated).

set -euo pipefail

REPO_URL="https://github.com/4IRL/4irl-notifs.git"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TOKEN_GENERATOR="$SCRIPT_DIR/generate-gh-token.sh"
ASKPASS_HELPER="$SCRIPT_DIR/gh-app-askpass.sh"

if [ $# -gt 1 ]; then
  echo "Error: too many arguments. Usage: $0 [branch-name]" >&2
  exit 2
fi

BRANCH="${1:-$(git branch --show-current)}"

if [ -z "$BRANCH" ]; then
  echo "Error: no branch name given and git branch --show-current is empty (detached HEAD?)." >&2
  exit 2
fi

case "$BRANCH" in
  main|master)
    echo "Error: refusing to push to '$BRANCH'. Feature branches only." >&2
    exit 3
    ;;
  --force|-f|--force-with-lease|--force-with-lease=*|*\ --force*|*\ -f*)
    echo "Error: branch name looks like a force-push flag: '$BRANCH'. Refused." >&2
    exit 3
    ;;
esac

if [ ! -x "$TOKEN_GENERATOR" ]; then
  echo "Error: token generator not found or not executable: $TOKEN_GENERATOR" >&2
  exit 4
fi

if [ ! -x "$ASKPASS_HELPER" ]; then
  echo "Error: askpass helper not found or not executable: $ASKPASS_HELPER" >&2
  exit 4
fi

# Remove any stale GH_TOKEN from the environment so nothing downstream can
# accidentally use it. We deliberately do NOT name our own variable GH_TOKEN.
unset GH_TOKEN

APP_TOKEN=$("$TOKEN_GENERATOR")

if [ -z "$APP_TOKEN" ] || [ "${APP_TOKEN:0:4}" != "ghs_" ]; then
  echo "Error: token generator did not return a valid installation token (expected 'ghs_' prefix)." >&2
  exit 5
fi

echo "Pushing '$BRANCH' to $REPO_URL as 4irl-notifs-claude[bot]..."

# credential.helper= disables the osxkeychain helper so GIT_ASKPASS is the only
# credential source consulted. GIT_TERMINAL_PROMPT=0 prevents any interactive
# fallback if the token is rejected.
export APP_TOKEN
GIT_ASKPASS="$ASKPASS_HELPER" GIT_TERMINAL_PROMPT=0 git \
  -c credential.helper= \
  push -u "$REPO_URL" \
  "$BRANCH"
unset APP_TOKEN

# `git push -u` tries to persist upstream tracking in .git/config, but a
# sandboxed environment may block writes to that path. Re-run set-upstream-to
# so the caller gets a clear warning if it didn't stick.
if git branch --set-upstream-to="origin/${BRANCH}" "$BRANCH" >/dev/null 2>&1; then
  echo "Upstream tracking set: origin/${BRANCH}"
else
  echo "Warning: could not set upstream tracking — likely sandbox-blocked .git/config write." >&2
  echo "Re-run with dangerouslyDisableSandbox: true to persist it:" >&2
  echo "  git branch --set-upstream-to=origin/${BRANCH} ${BRANCH}" >&2
fi

echo "Push complete: $BRANCH"
