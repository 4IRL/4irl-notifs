#!/usr/bin/env bash
# Self-test for lib.sh and run.sh: the stub harness and expect_* helpers behave as documented, and the runner fails
# (non-zero) when any test case fails, ignores the live tests, and refuses an empty directory.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=lib.sh
. "$here/lib.sh"
t_init

t_stub fakecmd 0 "hello"
run_cmd fakecmd one "two words"
expect_rc "stub exits 0" 0
expect_out_has "stub prints its stdout" "hello"
expect_out_lacks "stub output lacks unrelated text" "goodbye"
expect_calls_has "stub logs argv" "fakecmd one two words"
expect_calls_lacks "stub log lacks unrelated call" "othercmd"

t_stub failcmd 7
run_cmd failcmd
expect_rc "stub exit code is configurable" 7

t_reset_calls
expect_eq "reset empties the call log" "$(t_calls)" ""
expect_file "temp root exists" "$T_TMP"
case "$PATH" in
  "$STUB_BIN":*) expect_eq "stub bin is first on PATH" 1 1 ;;
  *) expect_eq "stub bin is first on PATH" 0 1 ;;
esac

# A failing expectation must be counted as a failure (checked on a copy of the counters).
saved_passed="$passed"
run_cmd failcmd
failed_before="$failed"
expect_rc "deliberate mismatch" 0 > /dev/null
expect_eq "mismatch increments failed" "$failed" "$((failed_before + 1))"
failed=$((failed - 1))
passed="$saved_passed"
passed=$((passed + 1))

# The runner exits non-zero when any test case fails, zero when all pass, and skips test_backup_live*.sh.
fx="$T_TMP/runner-fixtures"
mkdir -p "$fx/pass" "$fx/fail" "$fx/empty" "$fx/live"
printf '#!/usr/bin/env bash\n. "%s/lib.sh"\nt_init\nexpect_eq ok 1 1\nt_finish\n' "$here" > "$fx/pass/test_ok.sh"
printf '#!/usr/bin/env bash\n. "%s/lib.sh"\nt_init\nexpect_eq bad 1 2\nt_finish\n' "$here" > "$fx/fail/test_bad.sh"
cp "$fx/pass/test_ok.sh" "$fx/fail/test_ok.sh"
cp "$fx/pass/test_ok.sh" "$fx/live/test_ok.sh"
printf '#!/usr/bin/env bash\nexit 1\n' > "$fx/live/test_backup_live.sh"
printf '#!/usr/bin/env bash\nexit 1\n' > "$fx/live/test_backup_live_skip.sh"

run_cmd bash "$here/run.sh" "$fx/pass"
expect_rc "runner passes when every case passes" 0
run_cmd bash "$here/run.sh" "$fx/fail"
expect_rc "runner exits non-zero when a case fails" 1
expect_out_has "runner names the failing file" "FAILED FILE: test_bad.sh"
run_cmd bash "$here/run.sh" "$fx/live"
expect_rc "runner skips the live tests" 0
run_cmd bash "$here/run.sh" "$fx/empty"
expect_rc "runner refuses a directory with no tests" 2

t_finish
