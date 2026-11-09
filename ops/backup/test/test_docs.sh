#!/usr/bin/env bash
# Static, grep-based test of ops/backup/RUNBOOK.md. check_runbook takes the runbook path as its argument so the test can
# aim it at fixtures: each of the seven rules is proven able to fail by a temp copy of the runbook with exactly one
# violation, and the real runbook must pass with a non-zero token count for every token class.
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
. "$HERE/lib.sh"

BACKUP_DIR="$(cd "$HERE/.." && pwd)"
REPO_ROOT="$(cd "$BACKUP_DIR/../.." && pwd)"
RUNBOOK="$BACKUP_DIR/RUNBOOK.md"

# check_runbook <file>: prints "TOKENS make=N restore=N install=N" and one "FAIL rule=<name>: <detail>" line per
# violation; exits non-zero on any violation or when a token class matched nothing.
check_runbook() {
  local file="$1" bad=0 tok target mode cmd n_make=0 n_restore=0 n_install=0
  local makefile="$REPO_ROOT/Makefile"
  local restore_modes install_cmds

  restore_modes="$(grep -E "^ *case \"[$]MODE\" in .*[)]" "$BACKUP_DIR/restore.sh" | head -1 | sed -E 's/.*in +//; s/\).*//; s/ \| / /g')"
  install_cmds="$(grep -E "^ *case \"[$]cmd\" in .*[)]" "$BACKUP_DIR/install-backup.sh" | head -1 | sed -E 's/.*in +//; s/\).*//; s/ \| / /g')"

  while IFS= read -r tok; do
    [ -n "$tok" ] || continue
    n_make=$((n_make + 1))
    target="${tok#make }"
    if ! grep -qE "^${target}:" "$makefile"; then
      echo "FAIL rule=make-target: no Makefile target '$target'"
      bad=1
    fi
  done < <(grep -oE 'make [a-z][a-z0-9-]*' "$file")

  while IFS= read -r tok; do
    [ -n "$tok" ] || continue
    n_restore=$((n_restore + 1))
    mode="${tok#restore.sh }"
    case " $restore_modes " in
      *" $mode "*) ;;
      *) echo "FAIL rule=restore-mode: unknown restore.sh mode '$mode'"; bad=1 ;;
    esac
  done < <(grep -oE 'restore\.sh [a-z][a-z0-9-]*' "$file")

  while IFS= read -r tok; do
    [ -n "$tok" ] || continue
    n_install=$((n_install + 1))
    cmd="${tok#install-backup.sh }"
    case " $install_cmds " in
      *" $cmd "*) ;;
      *) echo "FAIL rule=install-cmd: unknown install-backup.sh command '$cmd'"; bad=1 ;;
    esac
  done < <(grep -oE 'install-backup\.sh [a-z][a-z0-9-]*' "$file")

  if grep -qiE 'discord(app)?\.com/api/webhooks' "$file"; then
    echo "FAIL rule=webhook-url: a Discord webhook URL"
    bad=1
  fi
  while IFS= read -r tok; do
    [ -n "$tok" ] || continue
    if [ "$tok" != "<account-id>.r2.cloudflarestorage.com" ]; then
      echo "FAIL rule=r2-host: non-placeholder R2 host '$tok'"
      bad=1
    fi
  done < <(grep -oE '[^/[:space:]]*r2\.cloudflarestorage\.com' "$file")
  if grep -qE 'dak_' "$file"; then
    echo "FAIL rule=dak-key: a dak_ string"
    bad=1
  fi
  if grep -qE '(^|[^0-9a-fA-F])[0-9a-f]{32}([^0-9a-fA-F]|$)' "$file"; then
    echo "FAIL rule=account-id: a bare 32-hex id"
    bad=1
  fi

  echo "TOKENS make=$n_make restore=$n_restore install=$n_install"
  if [ "$n_make" -eq 0 ] || [ "$n_restore" -eq 0 ] || [ "$n_install" -eq 0 ]; then
    echo "FAIL rule=token-count: a token class matched nothing"
    bad=1
  fi
  return "$bad"
}

t_init

# The unmodified runbook passes and every token class is non-empty.
run_cmd check_runbook "$RUNBOOK"
expect_rc "real runbook passes" 0
expect_out_has "real runbook has make tokens" "TOKENS make="
n_make="$(sed -nE 's/.*TOKENS make=([0-9]+).*/\1/p' <<<"$out")"
n_restore="$(sed -nE 's/.*restore=([0-9]+).*/\1/p' <<<"$out")"
n_install="$(sed -nE 's/.*install=([0-9]+).*/\1/p' <<<"$out")"
if [ "${n_make:-0}" -gt 0 ]; then _pass; else _fail "make token count is zero"; fi
if [ "${n_restore:-0}" -gt 0 ]; then _pass; else _fail "restore.sh token count is zero"; fi
if [ "${n_install:-0}" -gt 0 ]; then _pass; else _fail "install-backup.sh token count is zero"; fi

# expect_violation <label> <rule> <line>: a copy of the runbook plus exactly one bad line must fail naming the rule.
expect_violation() {
  local label="$1" rule="$2" line="$3" fixture="$T_TMP/fixture.md"
  cp "$RUNBOOK" "$fixture"
  printf '\n%s\n' "$line" >> "$fixture"
  run_cmd check_runbook "$fixture"
  if [ "$rc" -ne 0 ]; then _pass; else _fail "$label: checker exited 0"; fi
  expect_out_has "$label names its rule" "rule=$rule"
}

expect_violation "nonexistent make target" make-target 'Run make no-such-target-xyz now.'
expect_violation "unknown restore mode" restore-mode 'Run restore.sh frobnicate now.'
expect_violation "unknown installer command" install-cmd 'Run install-backup.sh frobnicate now.'
expect_violation "webhook url" webhook-url 'Hook: https://discord.com/api/webhooks/123/abc'
expect_violation "real-looking r2 host" r2-host 'Repo: s3:https://abcdef.r2.cloudflarestorage.com/bucket'
expect_violation "dak string" dak-key 'Key: dak_abc'
expect_violation "32-hex account id" account-id 'Account: 0123456789abcdef0123456789abcdef'

# A token pattern that matches nothing must not pass.
printf 'Nothing to see here.\n' > "$T_TMP/empty.md"
run_cmd check_runbook "$T_TMP/empty.md"
if [ "$rc" -ne 0 ]; then _pass; else _fail "empty runbook passed"; fi
expect_out_has "empty runbook names token-count" "rule=token-count"

t_finish
