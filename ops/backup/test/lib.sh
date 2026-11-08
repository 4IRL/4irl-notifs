#!/usr/bin/env bash
# Shared helpers for ops/backup bash tests. Source it from a test_*.sh; do not run it.
# Ported from apps/platform/test/lib.sh (GPropersi/apps), trimmed to the backup suite.
#
#   . "$(dirname "$0")/lib.sh"
#   t_init                                   # temp root + stub-bin first on PATH + call log
#   t_stub restic 0 'some stdout'            # stub that logs argv and prints stdout
#   run_cmd some-command arg                 # captures rc and out
#   expect_rc "label" 0
#   t_finish                                 # prints the summary, exits non-zero on any failure

# A suite launched from a background job or a detached unit (the push gate) inherits INT, HUP and QUIT as ignored, and a
# signal ignored at shell entry can be neither trapped nor un-ignored. Re-exec once with the default dispositions so the
# result does not depend on the launcher.
if [ -z "${NOTIFS_BACKUP_TEST_SIGRESET:-}" ] && [ -f "$0" ]; then
  export NOTIFS_BACKUP_TEST_SIGRESET=1
  exec /usr/bin/env --default-signal=INT,HUP,QUIT "$BASH" "$0" "$@"
fi

# ENVU: prefix that unsets every runner knob, so an outer environment never leaks into a suite. Add a new knob here once.
# Used as: run_cmd "${ENVU[@]}" [env VAR=val] cmd...
# shellcheck disable=SC2034
ENVU=(env -u NOTIFS_BACKUP_TESTING -u REQUIRE_DOCKER)

passed=0
failed=0
rc=0
out=""

t_init() {
  T_TMP="$(mktemp -d)"
  STUB_BIN="$T_TMP/stub-bin"
  CALL_LOG="$T_TMP/calls.log"
  mkdir -p "$STUB_BIN"
  : > "$CALL_LOG"
  PATH="$STUB_BIN:$PATH"
  export T_TMP STUB_BIN CALL_LOG PATH
  trap 't_cleanup' EXIT
}

t_cleanup() {
  [ -n "${T_TMP:-}" ] && rm -rf "$T_TMP"
  return 0
}

# t_stub <name> [exit-code] [stdout]: a stub on PATH that appends "<name> <argv...>" to $CALL_LOG.
t_stub() {
  local name="$1" code="${2:-0}" text="${3:-}"
  # The stub body is written with single quotes on purpose: $CALL_LOG and "$@" expand when the stub runs.
  # shellcheck disable=SC2016
  {
    printf '#!/usr/bin/env bash\n'
    printf 'printf "%%s" %q >> "$CALL_LOG"\n' "$name"
    printf 'for a in "$@"; do printf " %%s" "$a" >> "$CALL_LOG"; done\n'
    printf 'printf "\\n" >> "$CALL_LOG"\n'
    if [ -n "$text" ]; then printf 'printf "%%s\\n" %q\n' "$text"; fi
    printf 'exit %q\n' "$code"
  } > "$STUB_BIN/$name"
  chmod +x "$STUB_BIN/$name"
}

t_calls() { cat "$CALL_LOG"; }
t_reset_calls() { : > "$CALL_LOG"; }

# run_cmd <cmd...>: run it, capture combined output in $out and status in $rc.
run_cmd() {
  rc=0
  out="$("$@" 2>&1)" || rc=$?
}

_pass() { passed=$((passed + 1)); }
_fail() {
  failed=$((failed + 1))
  echo "FAIL: $1"
  [ -n "${2:-}" ] && echo "  $2"
  [ -n "$out" ] && printf '  output: %s\n' "$out"
  return 0
}

# expect_rc <label> <want>: $rc equals want.
expect_rc() {
  if [ "$rc" -eq "$2" ]; then _pass; else _fail "$1" "rc=$rc, want $2"; fi
}

# expect_out_has <label> <fixed-string>: $out contains it.
expect_out_has() {
  if grep -qF -- "$2" <<<"$out"; then _pass; else _fail "$1" "output lacks: $2"; fi
}

# expect_out_lacks <label> <fixed-string>: $out does not contain it.
expect_out_lacks() {
  if grep -qF -- "$2" <<<"$out"; then _fail "$1" "output unexpectedly has: $2"; else _pass; fi
}

# expect_eq <label> <got> <want>
expect_eq() {
  if [ "$2" = "$3" ]; then _pass; else _fail "$1" "got '$2', want '$3'"; fi
}

# expect_calls_has / expect_calls_lacks <label> <fixed-string>: against the stub call log.
expect_calls_has() {
  if grep -qF -- "$2" "$CALL_LOG"; then _pass; else _fail "$1" "call log lacks: $2"; fi
}
expect_calls_lacks() {
  if grep -qF -- "$2" "$CALL_LOG"; then _fail "$1" "call log unexpectedly has: $2"; else _pass; fi
}

# expect_file <label> <path>: exists.
expect_file() {
  if [ -e "$2" ]; then _pass; else _fail "$1" "missing: $2"; fi
}

t_finish() {
  echo "$(basename "$0"): $passed passed, $failed failed"
  [ "$failed" -eq 0 ]
}
