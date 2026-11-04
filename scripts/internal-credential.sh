#!/bin/sh
# Print the provisioning-api internal credential for the integration harnesses.
# An already-set NOTIFS_INTERNAL_CREDENTIAL wins as is; otherwise read
# PROVISIONING_INTERNAL_CREDENTIAL from the running provisioning-api container's environment
# (container found through ${COMPOSE:-docker compose}). Empty either way exits 1 (fail closed).
# Prints only the credential on stdout, never anything else; diagnostics go to stderr.
set -eu

CRED="${NOTIFS_INTERNAL_CREDENTIAL:-}"
if [ -z "$CRED" ]; then
  # shellcheck disable=SC2086 # COMPOSE is a multi-word command prefix
  CID="$(${COMPOSE:-docker compose} ps -q provisioning-api)"
  if [ -n "$CID" ]; then
    CRED="$(docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "$CID" | sed -n 's/^PROVISIONING_INTERNAL_CREDENTIAL=//p')"
  fi
fi
if [ -z "$CRED" ]; then
  echo "no internal credential: run 'make local-up' first (or export NOTIFS_INTERNAL_CREDENTIAL)" >&2
  exit 1
fi
printf '%s\n' "$CRED"
