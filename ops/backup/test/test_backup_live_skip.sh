#!/usr/bin/env bash
# The skip/require contract of test_backup_live.sh, with no docker or restic needed: the live test is run with a PATH
# that holds only the stubs this file creates.
#   docker or restic absent (or the docker daemon unreachable) -> exit 0 with a SKIP banner
#   REQUIRE_DOCKER=1                                           -> exit 1
#   NBLIVE_SKIP=1                                              -> exit 0 with a SKIP banner; refused (exit 1) with REQUIRE_DOCKER=1
# Not run by `make backup-test` (run.sh skips test_backup_live*.sh); `make backup-test-live` runs it before the live test.
. "$(dirname "$0")/lib.sh"
t_init

live="$(cd "$(dirname "$0")" && pwd)/test_backup_live.sh"

# Each case gets its own PATH directory holding only /bin/sh stubs (a stub must not need `env bash`).
mkbin() { # <dir> <tool:exitcode>...
  local d="$1" spec
  shift
  mkdir -p "$d"
  for spec in "$@"; do
    printf '#!/bin/sh\nexit %s\n' "${spec#*:}" > "$d/${spec%%:*}"
    chmod +x "$d/${spec%%:*}"
  done
}

live_run() { # <path-dir> [VAR=val...]
  local d="$1"
  shift
  run_cmd env -i PATH="$d" "$@" "$BASH" "$live"
}

mkbin "$T_TMP/none"
mkbin "$T_TMP/nodaemon" docker:1 restic:0 flock:0 timeout:0
mkbin "$T_TMP/norestic" docker:0 flock:0 timeout:0

live_run "$T_TMP/none"
expect_rc "no docker, no restic: skips with exit 0" 0
expect_out_has "no docker: SKIP banner" "SKIP"
expect_out_has "no docker: banner names docker" "docker"

live_run "$T_TMP/none" REQUIRE_DOCKER=1
expect_rc "REQUIRE_DOCKER=1 with no docker: exit 1" 1
expect_out_lacks "REQUIRE_DOCKER=1 does not print a skip banner" "SKIP"

live_run "$T_TMP/nodaemon"
expect_rc "docker daemon unreachable: skips with exit 0" 0
expect_out_has "unreachable daemon: banner names the daemon" "docker daemon"

live_run "$T_TMP/nodaemon" REQUIRE_DOCKER=1
expect_rc "docker daemon unreachable with REQUIRE_DOCKER=1: exit 1" 1

mkbin "$T_TMP/norestic" docker:0 flock:0 timeout:0
live_run "$T_TMP/norestic"
expect_rc "restic absent: skips with exit 0" 0
expect_out_has "restic absent: banner names restic" "restic"

live_run "$T_TMP/norestic" REQUIRE_DOCKER=1
expect_rc "restic absent with REQUIRE_DOCKER=1: exit 1" 1

live_run "$T_TMP/none" NBLIVE_SKIP=1
expect_rc "NBLIVE_SKIP=1: exit 0" 0
expect_out_has "NBLIVE_SKIP=1: SKIP banner" "SKIP"

live_run "$T_TMP/none" NBLIVE_SKIP=1 REQUIRE_DOCKER=1
expect_rc "NBLIVE_SKIP=1 with REQUIRE_DOCKER=1 is refused: exit 1" 1

t_finish
