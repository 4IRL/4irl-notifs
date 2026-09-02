#!/bin/bash
# Retired the isolated `4irl-notifs-claude` App: this repo now authenticates as
# the SHARED consolidated `gpropersi-claude` GitHub App (one App across all of
# GPropersi's/4IRL's bot repos), scoped here to 4irl-notifs's installation.
#
# This is a thin delegate to the single shared generator so the repo-local bot
# toolkit (gh-app-push.sh, etc.) keeps working unchanged — it still calls
# `.claude/bot/generate-gh-token.sh` and still gets a valid `ghs_` token, just
# minted from the shared App. GH_APP_REPO pins resolution to this repo regardless
# of the caller's CWD. Tokens expire after 1 hour. No App-specific IDs live here
# (safe for a public repo); the private key lives at ~/.claude/, outside the repo.
exec env GH_APP_REPO="4IRL/4irl-notifs" "$HOME/.claude/generate-gh-token.sh"
