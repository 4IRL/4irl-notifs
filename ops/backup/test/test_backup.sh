#!/usr/bin/env bash
# Offline tests for backup.sh: the framework (env, lock, logging, notify, state file, window, deadline, exit path) and
# the data phases. Ported from apps/platform/backup/test_backup.sh (GPropersi/apps).
# Framework cases run with run_case overriding the shipped PHASES with dummy phases; data-phase cases run the real ones.
# Every case runs backup.sh SOURCED in a fresh real bash (so set -e and the traps behave as in production) with the
# NOTIFS_BACKUP_* overrides pointing into a per-case temp dir, stubs for restic, docker and curl on PATH (failure
# injection through $CASE_DIR/fail_<key> files holding the exit code), and a stubbed clock (now_epoch, now_hhmm,
# window_file) injected through the BASH_SOURCE source guard. The lock is the real flock.
#
# No real credentials: every secret below is a fake marker string.
# SC2016: stub and case bodies are single-quoted on purpose. SC1091: lib.sh is sourced by relative path.
# SC2154: $out and $rc are set by run_cmd in lib.sh. SC2034: some fixture variables are only read by sub-functions.
# shellcheck disable=SC1091,SC2016,SC2034,SC2154
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=lib.sh
. "$here/lib.sh"
SCRIPT="$here/../backup.sh"
export SCRIPT

t_init

if command -v flock > /dev/null 2>&1; then _pass; else _fail "flock is available for the lock tests"; fi
HAVE_PY=1
if ! command -v python3 > /dev/null 2>&1; then
  HAVE_PY=0
  if [ "${REQUIRE_DOCKER:-}" = "1" ]; then _fail "python3 is available (REQUIRE_DOCKER=1)"; else echo "SKIP: python3 absent, JSON parser check skipped"; fi
fi

PW_SECRET='Pw+/=Zq9xSecretMarker'
AWS_SECRET='sk+/=testsecretmarker0002'
HOOK_SECRET='https://discord.example.test/api/webhooks/111/tokenmarker'
REPO_SECRET='s3:https://acct.example.test/bucket'
KEYID_SECRET='TESTKEYIDMARKER0001'

# ---- stubs ----

# curl: logs argv, copies the --data-binary @file payload and the -K config for inspection, and fails (exit 22,
# stderr from $CASE_DIR/curl.fail) when that file exists.
cat > "$STUB_BIN/curl" <<'EOF'
#!/usr/bin/env bash
printf 'curl' >> "$CALL_LOG"
for a in "$@"; do printf ' %s' "$a" >> "$CALL_LOG"; done
printf '\n' >> "$CALL_LOG"
prev=""
for a in "$@"; do
  case "$a" in @*) cp "${a#@}" "$CASE_DIR/payload.json" ;; esac
  if [ "$prev" = "-K" ]; then
    cp "$a" "$CASE_DIR/curl.cfg"
    if grep -q 'Authorization' "$a"; then cp "$a" "$CASE_DIR/hb.cfg"; fi
  fi
  if [ "$a" = "-w" ]; then want_code=1; fi
  prev="$a"
done
if [ -f "$CASE_DIR/curl.fail" ]; then cat "$CASE_DIR/curl.fail" >&2; exit 22; fi
# the heartbeat call asks for the status code with -w: answer from curl.http (default 200)
if [ "${want_code:-0}" = 1 ]; then printf '%s' "$(cat "$CASE_DIR/curl.http" 2> /dev/null || echo 200)"; fi
exit 0
EOF
chmod +x "$STUB_BIN/curl"

# restic: logs argv, records the password it saw, and fails with an error text that contains every secret when
# $CASE_DIR/fail_restic-backup exists (the file holds the exit code).
cat > "$STUB_BIN/restic" <<'EOF'
#!/usr/bin/env bash
printf 'restic' >> "$CALL_LOG"
for a in "$@"; do printf ' %s' "$a" >> "$CALL_LOG"; done
printf '\n' >> "$CALL_LOG"
printf '%s' "$RESTIC_PASSWORD" > "$CASE_DIR/restic-saw"
printf 'RESTIC_PASSWORD_FILE=[%s] AWS_SESSION_TOKEN=[%s] DOCKER_HOST=[%s] CACHE=[%s] FPS=[%s]\n' "${RESTIC_PASSWORD_FILE-}" "${AWS_SESSION_TOKEN-}" "${DOCKER_HOST-}" "${RESTIC_CACHE_DIR-}" "${RESTIC_PROGRESS_FPS-}" > "$CASE_DIR/restic-env"
sub="${1:-}"
case "$sub" in
  snapshots)
    # stateful: the first listing is the pre-backup one (snaps.pre, default none), later ones snaps.post
    n=$(cat "$CASE_DIR/snapcount" 2> /dev/null || echo 0)
    printf '%s' $((n + 1)) > "$CASE_DIR/snapcount"
    if [ "$n" -eq 0 ]; then f="$CASE_DIR/snaps.pre"; d='[]'; else f="$CASE_DIR/snaps.post"; d='[{"id":"abc12345deadbeef","short_id":"abc12345"}]'; fi
    if [ -f "$f" ]; then cat "$f"; else printf '%s\n' "$d"; fi
    ;;
  backup)
    # what is being uploaded, for the secrets-persistent assertions
    for a in "$@"; do last="$a"; done
    if [ -d "$last" ]; then (cd "$last" && find . -type f) > "$CASE_DIR/staged.txt"; fi
    case " $* " in *" --json "*)
      printf '{"message_type":"status","percent_done":0.5}\n'
      # no-summary: restic exits 0 but prints no summary line, so no snapshot id
      if [ ! -f "$CASE_DIR/no-summary" ]; then printf '{"message_type":"summary","snapshot_id":"abc12345"}\n'; fi ;;
    esac
    ;;
esac
if [ -f "$CASE_DIR/fail_restic-$sub" ]; then
  echo "Fatal: wrong password $RESTIC_PASSWORD for repo $RESTIC_REPOSITORY using key $AWS_SECRET_ACCESS_KEY id $AWS_ACCESS_KEY_ID" >&2
  exit "$(cat "$CASE_DIR/fail_restic-$sub")"
fi
exit 0
EOF
chmod +x "$STUB_BIN/restic"

# docker: logs argv and answers the data phases from per-case files: health.seq (one inspect answer per line, the last
# repeats; default healthy), toc (pg_restore --list), integrity (default ok), volumes (volume ls).
cat > "$STUB_BIN/docker" <<'EOF'
#!/usr/bin/env bash
printf 'docker' >> "$CALL_LOG"
for a in "$@"; do printf ' %s' "$a" >> "$CALL_LOG"; done
printf '\n' >> "$CALL_LOG"
# failure injection: $CASE_DIR/fail_docker-<key> holds the exit code (keys: pg_dump, pg_restore, sqlite-copy, volume)
dfail() {
  if [ -f "$CASE_DIR/fail_docker-$1" ]; then
    echo "docker: injected failure ($1)" >&2
    exit "$(cat "$CASE_DIR/fail_docker-$1")"
  fi
}
case "${1:-}" in
  inspect)
    n=$(cat "$CASE_DIR/healthn" 2> /dev/null || echo 0)
    printf '%s' $((n + 1)) > "$CASE_DIR/healthn"
    if [ -f "$CASE_DIR/health.seq" ]; then
      line="$(sed -n "$((n + 1))p" "$CASE_DIR/health.seq")"
      [ -n "$line" ] || line="$(tail -n 1 "$CASE_DIR/health.seq")"
      printf '%s\n' "$line"
    else
      echo healthy
    fi
    ;;
  exec)
    case " $* " in
      *" pg_dump "*)
        dfail pg_dump
        # dump.empty: pg_dump exits 0 with no output
        if [ ! -f "$CASE_DIR/dump.empty" ]; then echo PGDUMP-DATA; fi ;;
      *" pg_restore "*)
        dfail pg_restore
        if [ -f "$CASE_DIR/toc" ]; then cat "$CASE_DIR/toc"; else
          printf '200; 1259 16400 TABLE public destinations delivery\n201; 1259 16410 TABLE public messages delivery\n'
        fi ;;
    esac
    ;;
  run)
    out=""
    for a in "$@"; do case "$a" in *:/out) out="${a%:/out}" ;; esac; done
    case " $* " in
      *" .backup "* | *".backup /out/auth.db"*)
        dfail sqlite-copy
        # no-authdb: the copy exits 0 but leaves no auth.db behind
        if [ ! -f "$CASE_DIR/no-authdb" ]; then printf 'SQLITE' > "$out/auth.db"; fi ;;
    esac
    case "$*" in
      *"PRAGMA integrity_check"*) if [ -f "$CASE_DIR/integrity" ]; then cat "$CASE_DIR/integrity"; else echo ok; fi ;;
    esac
    ;;
  volume)
    dfail volume
    if [ -f "$CASE_DIR/volumes" ]; then cat "$CASE_DIR/volumes"; else
      printf '4irl-notifs_delivery-pgdata\n4irl-notifs_ntfy-auth\n4irl-notifs_ntfy-cache\nu4i_other\n'
    fi
    ;;
esac
exit 0
EOF
chmod +x "$STUB_BIN/docker"

# ---- per-case fixtures ----

ncase=0
mkenv() { # [skip-key...]: a complete valid backup.env minus the named keys
  local f="$C/backup.env" kv key skip
  : > "$f"
  for kv in \
    "RESTIC_REPOSITORY=$REPO_SECRET" \
    "RESTIC_PASSWORD=$PW_SECRET" \
    "AWS_ACCESS_KEY_ID=$KEYID_SECRET" \
    "AWS_SECRET_ACCESS_KEY=$AWS_SECRET" \
    "AWS_DEFAULT_REGION=auto" \
    "DISCORD_WEBHOOK=$HOOK_SECRET"; do
    key="${kv%%=*}"
    for skip in "$@"; do if [ "$key" = "$skip" ]; then continue 2; fi; done
    printf '%s\n' "$kv" >> "$f"
  done
  chmod 600 "$f"
}

new_case() {
  ncase=$((ncase + 1))
  C="$T_TMP/case$ncase"
  mkdir -p "$C"
  t_reset_calls
  export C CASE_DIR="$C" CLK="$C/clock" HHMM="$C/hhmm" WINF="$C/window.env"
  export NOTIFS_BACKUP_TESTING=1
  export NOTIFS_BACKUP_ENV_FILE="$C/backup.env"
  OWNER_NOW="$(id -u):$(id -g)"
  export NOTIFS_BACKUP_ENV_OWNER="$OWNER_NOW"
  export NOTIFS_BACKUP_ROOT="$C/root"
  export NOTIFS_BACKUP_STATE_DIR="$C/status"
  export NOTIFS_BACKUP_LOG_DIR="$C/logs"
  export NOTIFS_BACKUP_LOCK="$C/lock"
  unset NOTIFS_BACKUP_DEADLINE_SECS
  # the sentinel that lets prepare_staging wipe this (non-production) work root
  mkdir -p "$C/root"
  : > "$C/root/.notifs-backup-root"
  # data phases: a bare 47-character key (no newline, as write-key.sh writes it), fast pg_wait, default constants
  mkdir -p "$C/secrets-persistent"
  KEY_VALUE="dak_$(printf 'A%.0s' $(seq 1 43))"
  export KEY_VALUE
  printf '%s' "$KEY_VALUE" > "$C/secrets-persistent/NOTIFS_OPS_DELIVERY_KEY"
  export NOTIFS_BACKUP_HEARTBEAT_KEY_FILE="$C/secrets-persistent/NOTIFS_OPS_DELIVERY_KEY"
  export NOTIFS_BACKUP_PG_WAIT_SECS=2 NOTIFS_BACKUP_PG_POLL_SECS=1
  export ADV_TO=1000 HHMM_TO=04:00
  printf '1000' > "$CLK"
  printf '04:00' > "$HHMM"
  printf 'BACKUP_WINDOW_START=03:30\nBACKUP_WINDOW_END=05:00\nBACKUP_WINDOW_TZ=America/Los_Angeles\n' > "$WINF"
  mkenv
}

PRELUDE="$(
  cat <<'EOF'
now_epoch() { cat "$CLK"; }
now_hhmm() { cat "$HHMM"; }
window_file() { printf '%s' "$WINF"; }
ph_ok() { timed ph_ok 10 true; }
ph_adv() { printf '%s' "$ADV_TO" > "$CLK"; }
ph_never() { : > "$C/never-ran"; }
ph_slow() { timed ph_slow 60 sleep 30; }
ph_selfkill() { kill -TERM "$BASHPID"; sleep 2; }
ph_restic() { timed ph_restic 10 restic backup; }
ph_clamp() { timed ph_clamp 600 sleep 5; }
ph_noclamp() { timed ph_noclamp 60 true; }
ph_fastclamp() { timed ph_fastclamp 600 true; }
ph_hhmm() { printf '%s' "$HHMM_TO" > "$HHMM"; }
ph_dump() { printf '%s' "$RESTIC_PASSWORD" > "$C/pw"; }
ph_env() { printf 'cache=%s fps=%s' "$RESTIC_CACHE_DIR" "$RESTIC_PROGRESS_FPS" > "$C/phase-env"; }
EOF
)"
export PRELUDE

# run_case <body>: backup.sh sourced into a fresh bash with the dummy default PHASES=(ph_ok), then the body (usually
# "PHASES=(...); main nightly").
run_case() {
  rc=0
  out="$(env CASE_BODY="$1" bash -c '. "$SCRIPT"; eval "$PRELUDE"; PHASES=(ph_ok); eval "$CASE_BODY"' 2>&1)" || rc=$?
}

# run_bg <body>: the same in the background (job control on, so INT is not ignored); sets BGPID, output in $C/bg.out.
run_bg() {
  set -m
  env CASE_BODY="$1" bash -c '. "$SCRIPT"; eval "$PRELUDE"; PHASES=(ph_ok); eval "$CASE_BODY"' > "$C/bg.out" 2>&1 &
  BGPID=$!
  set +m
}

wait_for_text() { # <file> <fixed-string>
  local i
  for i in $(seq 1 100); do
    if grep -qF -- "$2" "$1" 2> /dev/null; then return 0; fi
    sleep 0.1
  done
  return 1
}

state() { awk -F'\t' -v n="$1" '{print $n}' "$C/status/last-run.tsv" 2> /dev/null; }
state_lines() { wc -l < "$C/status/last-run.tsv" 2> /dev/null | tr -d ' '; }
ncurl() { grep -c '^curl ' "$CALL_LOG" || true; }
payload() { cat "$C/payload.json" 2> /dev/null || true; }
logtext() { cat "$C"/logs/*.log 2> /dev/null || true; }
expect_absent() { if [ ! -e "$2" ]; then _pass; else _fail "$1" "unexpectedly exists: $2"; fi; }
expect_present() { if [ -e "$2" ]; then _pass; else _fail "$1" "missing: $2"; fi; }
expect_absent_or_empty() { if [ ! -s "$2" ]; then _pass; else _fail "$1" "not empty: $2"; fi; }
expect_text_has() { # <label> <text> <fixed>
  if grep -qF -- "$3" <<< "$2"; then _pass; else _fail "$1" "lacks: $3"; fi
}
expect_text_lacks() { # <label> <text> <fixed>
  if grep -qF -- "$3" <<< "$2"; then _fail "$1" "unexpectedly has: $3"; else _pass; fi
}

# ---------------------------------------------------------------------------------------------------------------
# 0. Source guard, usage, positive baseline
new_case
run_case 'echo sourced-ok'
expect_rc "sourcing runs no main" 0
expect_out_has "sourcing prints only the case output" "sourced-ok"
expect_out_lacks "sourcing starts no run" "run start"
expect_absent "sourcing writes no state" "$C/status/last-run.tsv"

run_case 'main'
expect_rc "no mode is a usage error" 2
run_case 'main bogus'
expect_rc "unknown mode is a usage error" 2

new_case
run_case 'PHASES=(ph_ok); main nightly'
expect_rc "clean nightly run with one phase" 0
expect_eq "state result ok" "$(state 3)" "ok"
expect_eq "state snapshot is - when empty" "$(state 4)" "-"
expect_eq "state failed_phase is - when empty" "$(state 5)" "-"
expect_eq "state is exactly one line" "$(state_lines)" "1"
if [[ "$(state 1)" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]] && [[ "$(state 2)" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]]; then _pass; else _fail "state times are ISO-8601 Z"; fi
expect_eq "one success notify" "$(ncurl)" "1"
expect_text_has "notice prefix and ok text" "$(payload)" "notifs-backup ok: nightly run ok in "
expect_text_has "notice carries snapshot and dump placeholders" "$(payload)" "snapshot -, dump -"
expect_out_has "phase start logged" "phase=ph_ok start timeout=10s"
expect_out_has "phase end logged with rc and duration" "phase=ph_ok end rc=0 dur="
expect_eq "dated log file holds the same run" "$(grep -c 'run start mode=nightly' <<< "$(logtext)")" "1"
expect_eq "log dir mode 0750" "$(stat -c %a "$C/logs")" "750"
expect_eq "log file mode 0640" "$(stat -c %a "$C"/logs/*.log)" "640"
expect_eq "state dir mode 0755" "$(stat -c %a "$C/status")" "755"
expect_eq "state file mode 0644" "$(stat -c %a "$C/status/last-run.tsv")" "644"
expect_absent "state is not under the 0700 work root" "$C/root/last-run.tsv"
expect_calls_has "curl is the webhook path" "curl -fsS --max-time 30"
expect_calls_lacks "webhook URL never on argv" "tokenmarker"
if grep -qF "tokenmarker" "$C/curl.cfg"; then _pass; else _fail "webhook URL reaches curl through the config file"; fi
expect_eq "curl config holds exactly one url line" "$(grep -c 'url = ' "$C/curl.cfg")" "1"

# the framework with no phases registered is env + lock + notice
new_case
run_case 'PHASES=(); main nightly'
expect_rc "a run with no phases registered succeeds" 0
expect_eq "no phases: one notify" "$(ncurl)" "1"

# phase_env exports the cache dir and progress rate for later phases, and scrubs the caller's variables
new_case
run_case 'PHASES=(ph_env); main nightly'
expect_rc "env exports run" 0
expect_eq "RESTIC_CACHE_DIR is under the work root, RESTIC_PROGRESS_FPS is set" "$(cat "$C/phase-env")" "cache=$C/root/restic-cache fps=0.0166"
new_case
rc=0
out="$(RESTIC_PASSWORD_FILE=/etc/passwd AWS_SESSION_TOKEN=tok DOCKER_HOST=tcp://evil:1 RESTIC_CACHE_DIR=/evil RCLONE_CONFIG=/evil env CASE_BODY='PHASES=(ph_restic); main nightly' bash -c '. "$SCRIPT"; eval "$PRELUDE"; eval "$CASE_BODY"' 2>&1)" || rc=$?
expect_rc "scrub: run with a hostile caller environment" 0
expect_eq "scrub: restic saw none of the caller's variables and the fixed cache dir" "$(cat "$C/restic-env")" "RESTIC_PASSWORD_FILE=[] AWS_SESSION_TOKEN=[] DOCKER_HOST=[] CACHE=[$C/root/restic-cache] FPS=[0.0166]"

# ---------------------------------------------------------------------------------------------------------------
# 1. Missing keys
new_case
mkenv RESTIC_PASSWORD
run_case 'PHASES=(ph_never); main nightly'
expect_rc "missing key exits 2" 2
expect_out_has "message names the missing key" "FATAL: missing required env var(s): RESTIC_PASSWORD"
expect_eq "state failed" "$(state 3)" "failed"
expect_eq "state failed_phase=env" "$(state 5)" "env"
expect_eq "state is exactly one line" "$(state_lines)" "1"
expect_eq "no webhook call" "$(ncurl)" "0"
expect_absent_or_empty "restic, docker and Discord are never contacted on an env failure" "$CALL_LOG"
expect_absent "no phase runs after an env failure" "$C/never-ran"

new_case
mkenv RESTIC_REPOSITORY DISCORD_WEBHOOK
run_case 'main nightly'
expect_rc "two missing keys exit 2" 2
expect_out_has "message lists every missing key" "FATAL: missing required env var(s): RESTIC_REPOSITORY DISCORD_WEBHOOK"

new_case
mkenv AWS_DEFAULT_REGION
run_case 'main nightly'
expect_rc "a missing AWS_DEFAULT_REGION exits 2 (all six keys required)" 2

new_case
mkenv RESTIC_PASSWORD
rc=0
out="$(RESTIC_PASSWORD=fromcaller env CASE_BODY='main nightly' bash -c '. "$SCRIPT"; eval "$PRELUDE"; eval "$CASE_BODY"' 2>&1)" || rc=$?
expect_rc "the caller's environment never supplies a key" 2
expect_out_has "caller-supplied key still reported missing" "RESTIC_PASSWORD"

new_case
run_case 'main nightly'
expect_rc "positive control: complete env accepted" 0
expect_eq "positive control: webhook called on success" "$(ncurl)" "1"

# ---------------------------------------------------------------------------------------------------------------
# 2. Literal reader
new_case
marker="$C/marker"
{
  printf '# comment line\n\n'
  printf 'RESTIC_REPOSITORY=%s\n' "$REPO_SECRET"
  printf 'RESTIC_PASSWORD=a=b/c+d$(touch %s)`touch %s.bt`\n' "$marker" "$marker"
  printf 'AWS_ACCESS_KEY_ID=%s\nAWS_SECRET_ACCESS_KEY=%s\nAWS_DEFAULT_REGION=auto\nDISCORD_WEBHOOK=%s\n' "$KEYID_SECRET" "$AWS_SECRET" "$HOOK_SECRET"
} > "$C/backup.env"
chmod 600 "$C/backup.env"
run_case 'PHASES=(ph_dump); main nightly'
expect_rc "literal reader accepts =, /, +, \$( and backticks in a value" 0
expect_absent "planted \$(...) substitution did not execute" "$marker"
expect_absent "planted backtick substitution did not execute" "$marker.bt"
expect_eq "value passes through byte-exact (split at first =)" "$(cat "$C/pw")" "a=b/c+d\$(touch $marker)\`touch $marker.bt\`"

new_case
ln -s "$C/real.env" "$C/linked.env"
mkenv
mv "$C/backup.env" "$C/real.env"
chmod 600 "$C/real.env"
export NOTIFS_BACKUP_ENV_FILE="$C/linked.env"
run_case 'main nightly'
expect_rc "symlinked env file refused" 2
expect_out_has "symlink: reason logged" "env file is a symlink"
expect_eq "symlink: failed_phase=env" "$(state 5)" "env"
expect_eq "symlink: no webhook" "$(ncurl)" "0"

new_case
chmod 640 "$C/backup.env"
run_case 'main nightly'
expect_rc "mode 0640 refused" 2
expect_eq "mode: failed_phase=env" "$(state 5)" "env"
chmod 600 "$C/backup.env"
run_case 'main nightly'
expect_rc "positive control: mode 0600 accepted" 0

new_case
rm -f "$C/backup.env"
run_case 'main nightly'
expect_rc "a missing env file exits 2" 2
expect_out_has "missing env file reason logged" "env file is missing or not a regular file"
expect_absent_or_empty "missing env file: nothing contacted" "$CALL_LOG"

new_case
if [ "$(id -u):$(id -g)" = "0:0" ]; then
  if [ "${REQUIRE_DOCKER:-}" = "1" ]; then _fail "unset-owner refusal is exercisable (running as 0:0 under REQUIRE_DOCKER=1)"; else echo "SKIP: running as 0:0, the unset-owner refusal cannot be exercised"; fi
else
  unset NOTIFS_BACKUP_ENV_OWNER
  run_case 'main nightly'
  expect_rc "owner unset: a file not owned by the 4irl-notifs user (or an unresolvable user) is refused" 2
  if id -u -- 4irl-notifs > /dev/null 2>&1; then
    expect_out_has "owner refusal names the 4irl-notifs uid:gid" "owner is not $(id -u -- 4irl-notifs):$(id -g -- 4irl-notifs)"
  else
    expect_out_has "an unresolvable 4irl-notifs user fails closed" "cannot resolve the uid:gid of user 4irl-notifs"
  fi
fi
new_case
unset NOTIFS_BACKUP_ENV_OWNER
run_case 'id() { return 1; }; main nightly'
expect_rc "owner unset and the user unresolvable exits 2 (fail closed)" 2
expect_out_has "unresolvable user is named" "cannot resolve the uid:gid of user 4irl-notifs"
for bad in "abc" "1:" "" "1:2:3" "root:root"; do
  new_case
  export NOTIFS_BACKUP_ENV_OWNER="$bad"
  run_case 'main nightly'
  expect_rc "malformed NOTIFS_BACKUP_ENV_OWNER '$bad' exits 2" 2
done
new_case
wrong_owner="$(($(id -u) + 1)):$(id -g)"
export NOTIFS_BACKUP_ENV_OWNER="$wrong_owner"
run_case 'main nightly'
expect_rc "owner set to a wrong uid:gid is refused" 2
expect_out_has "wrong-owner refusal names the owner check" "owner is not $NOTIFS_BACKUP_ENV_OWNER"
expect_eq "wrong owner: failed_phase=env" "$(state 5)" "env"
new_case
run_case 'main nightly'
expect_rc "owner set to the file's real uid:gid is accepted" 0

# ---------------------------------------------------------------------------------------------------------------
# 3. Grammar
bad_lines=('RESTIC_PASSWORD="quoted"' "RESTIC_PASSWORD='quoted'" 'NOT_A_KEY=1' 'RESTIC_PASSWORD=dup' 'novalueline' $'RESTIC_REPOSITORY=has-cr\r')
bad_labels=("leading double quote" "leading single quote" "unknown key" "duplicate key" "line without =" "CR in a value")
bad_msgs=("value for RESTIC_PASSWORD starts with a quote" "value for RESTIC_PASSWORD starts with a quote" "has an unknown key" "repeats key RESTIC_PASSWORD" "has no =" "contains a CR")
for i in "${!bad_lines[@]}"; do
  new_case
  mkenv
  if [ "${bad_labels[$i]}" = "leading double quote" ] || [ "${bad_labels[$i]}" = "leading single quote" ]; then mkenv RESTIC_PASSWORD; fi
  if [ "${bad_labels[$i]}" = "CR in a value" ]; then mkenv RESTIC_REPOSITORY; fi
  printf '%s\n' "${bad_lines[$i]}" >> "$C/backup.env"
  run_case 'main nightly'
  expect_rc "grammar: ${bad_labels[$i]} refused" 2
  expect_out_has "grammar: ${bad_labels[$i]} refused for the right reason" "FATAL: env file"
  expect_out_has "grammar: ${bad_labels[$i]} message" "${bad_msgs[$i]}"
  expect_eq "grammar: ${bad_labels[$i]} -> failed_phase=env" "$(state 5)" "env"
  expect_eq "grammar: ${bad_labels[$i]} -> no webhook" "$(ncurl)" "0"
  expect_absent_or_empty "grammar: ${bad_labels[$i]} -> nothing contacted" "$CALL_LOG"
done
new_case
{ printf '\n# a comment\n\n'; cat "$C/backup.env"; printf '\n# trailing comment\n'; } > "$C/with-extras.env"
mv "$C/with-extras.env" "$C/backup.env"
chmod 600 "$C/backup.env"
run_case 'main nightly'
expect_rc "positive control: blank and # lines skipped, same keys accepted" 0

# ---------------------------------------------------------------------------------------------------------------
# 4. Masking: every secret value (all keys but the region) is masked in logs, notice and argv
new_case
printf '1' > "$C/fail_restic-backup"
run_case 'PHASES=(ph_restic); main nightly'
expect_rc "failing restic phase exits 1" 1
expect_eq "positive control: the stub saw the real password" "$(cat "$C/restic-saw")" "$PW_SECRET"
expect_calls_has "positive control: restic was called" "restic backup"
expect_out_has "error text is kept, masked" "Fatal: wrong password ***"
expect_out_has "repository value is masked in the error text" "for repo *** using key ***"
expect_out_has "access key id is masked in the error text" "id ***"
for v in "$PW_SECRET" "$AWS_SECRET" "$REPO_SECRET" "$KEYID_SECRET" "$HOOK_SECRET" "tokenmarker"; do
  expect_out_lacks "no plain '$v' on stdout" "$v"
  expect_text_lacks "no plain '$v' in the log file" "$(logtext)" "$v"
  expect_text_lacks "no plain '$v' in the curl payload" "$(payload)" "$v"
  expect_calls_lacks "no plain '$v' in any recorded argv" "$v"
done
expect_text_has "masked error text reaches the Discord message" "$(payload)" "wrong password ***"
expect_text_has "failure message names the phase" "$(payload)" "notifs-backup failed: nightly run FAILED at phase ph_restic"
expect_eq "state failed_phase is the restic phase" "$(state 5)" "ph_restic"
expect_eq "state is exactly one line" "$(state_lines)" "1"
expect_eq "exactly one failure notify" "$(ncurl)" "1"

# the region is not a secret and is not masked
new_case
run_case 'mask_text "region auto here"'
expect_out_has "mask_text leaves a value that is not loaded alone" "region auto here"
new_case
run_case 'read_backup_env "$NOTIFS_BACKUP_ENV_FILE" "$NOTIFS_BACKUP_ENV_OWNER"; mask_text "region=$AWS_DEFAULT_REGION pw=$RESTIC_PASSWORD"'
expect_out_has "mask_text keeps AWS_DEFAULT_REGION and masks the password" "region=auto pw=***"

# ---------------------------------------------------------------------------------------------------------------
# 5. JSON body
new_case
if [ "$HAVE_PY" -eq 1 ]; then
  export HOOK="$HOOK_SECRET"
  export TXT=$'quote " backslash \\ newline\nline2 tab\tend @everyone ctrl\001x'
  run_case 'DISCORD_WEBHOOK="$HOOK"; WORK="$(mktemp -d)"; TIMED_LAST_FILE="$WORK/t"; notify ok "$TXT"'
  expect_rc "notify runs" 0
  rc=0
  out="$(python3 -I -c '
import json, os, sys
d = json.load(open(sys.argv[1]))
assert d["allowed_mentions"] == {"parse": []}, d
assert d["content"] == "notifs-backup ok: " + os.environ["TXT"], repr(d["content"])
print("json-ok")
' "$C/payload.json" 2>&1)" || rc=$?
  expect_rc "a real JSON parser accepts the body, allowed_mentions.parse empty, text round-trips" 0
  expect_out_has "json parser verdict" "json-ok"

  # the real notice of a failed run (quotes, newline, warnings) also parses, and is truncated to 1900 chars
  new_case
  printf '1' > "$C/fail_restic-backup"
  printf '02:00' > "$HHMM"
  run_case 'PHASES=(ph_restic); main nightly'
  rc=0
  out="$(python3 -I -c 'import json,sys; json.load(sys.stdin)' < "$C/payload.json" 2>&1)" || rc=$?
  expect_rc "the failed-run notice is valid JSON" 0
  new_case
  run_case 'WORK="$(mktemp -d)"; TIMED_LAST_FILE="$WORK/t"; notify ok "$(printf "x%.0s" $(seq 1 3000))"'
  rc=0
  out="$(python3 -I -c '
import json, sys
d = json.load(sys.stdin)
assert len(d["content"]) == 1903, len(d["content"])
print("len-ok")
' < "$C/payload.json" 2>&1)" || rc=$?
  expect_rc "the notice is truncated to 1900 chars plus an ellipsis" 0
fi

# notify text with & (patsub_replacement) and backslash, and a webhook value with backslash and quote
new_case
export HOOK2='https://h.example.test/a\b"c' TXT2='x & y \ z &'
run_case 'DISCORD_WEBHOOK="$HOOK2"; notify ok "$TXT2"'
expect_rc "notify with & and backslash runs" 0
expect_text_has "& and backslash are escaped in the payload" "$(payload)" 'notifs-backup ok: x & y \\ z &'
expect_text_has "webhook backslash and quote are escaped in the curl config" "$(cat "$C/curl.cfg" 2> /dev/null)" 'url = "https://h.example.test/a\\b\"c"'
expect_calls_lacks "webhook value never on argv" "h.example.test"

# ---------------------------------------------------------------------------------------------------------------
# 6. Webhook failure leaves the exit code alone
new_case
printf 'curl: (22) HTTP 404 for %s\n' "$HOOK_SECRET" > "$C/curl.fail"
run_case 'PHASES=(ph_ok); main nightly'
expect_rc "webhook failure does not change the exit code" 0
expect_eq "positive control: curl was called" "$(ncurl)" "1"
expect_eq "state still ok" "$(state 3)" "ok"
expect_text_has "failure logged with rc" "$(logtext)" "notify: webhook POST failed rc=22"
expect_text_has "curl error text logged, masked" "$(logtext)" "curl: (22) HTTP 404 for ***"
expect_text_lacks "webhook URL not logged in plain" "$(logtext)" "$HOOK_SECRET"
new_case
printf 'curl: (22) HTTP 404\n' > "$C/curl.fail"
printf '1' > "$C/fail_restic-backup"
run_case 'PHASES=(ph_restic); main nightly'
expect_rc "a failed run with a failed webhook still exits 1" 1

# ---------------------------------------------------------------------------------------------------------------
# 7. Lock
new_case
flock "$C/lock" sleep 30 &
holder=$!
for _ in $(seq 1 50); do flock -n "$C/lock" true 2> /dev/null || break; sleep 0.1; done
run_case 'PHASES=(ph_never); main nightly'
expect_rc "held lock exits 1" 1
expect_absent "lock: no state file is written (the holder reports its own result)" "$C/status/last-run.tsv"
expect_out_has "lock: the unchanged state file is logged" "last-run.tsv left unchanged (failed at phase lock"
expect_out_has "lock-held logged" "state lock-held"
expect_eq "lock: exactly one notify" "$(ncurl)" "1"
expect_text_has "lock: the failed notice names the lock phase" "$(payload)" "FAILED at phase lock"
expect_absent "no phase runs when the lock is held" "$C/never-ran"
pkill -P "$holder" 2> /dev/null
kill "$holder" 2> /dev/null
wait "$holder" 2> /dev/null
t_reset_calls
run_case 'PHASES=(ph_never); main nightly'
expect_rc "positive control: lock free again, run proceeds" 0
expect_present "positive control: phase ran once the lock was free" "$C/never-ran"

# a lock failure leaves an existing last-run.tsv byte-for-byte unchanged
new_case
mkdir -p "$C/status"
seeded=$'2020-01-01T00:00:00Z\t2020-01-01T00:01:00Z\tok\tdeadbeef\t-'
printf '%s\n' "$seeded" > "$C/status/last-run.tsv"
flock "$C/lock" sleep 30 &
holder=$!
for _ in $(seq 1 50); do flock -n "$C/lock" true 2> /dev/null || break; sleep 0.1; done
run_case 'PHASES=(ph_never); main nightly'
expect_rc "lock held with a prior state file exits 1" 1
expect_eq "lock: the prior last-run.tsv is unchanged" "$(cat "$C/status/last-run.tsv")" "$seeded"
expect_eq "lock: the prior last-run.tsv is still one line" "$(state_lines)" "1"
expect_eq "lock: the Discord notice is still sent" "$(ncurl)" "1"
pkill -P "$holder" 2> /dev/null
kill "$holder" 2> /dev/null
wait "$holder" 2> /dev/null

# ---------------------------------------------------------------------------------------------------------------
# 8. timed: timeout, rc passthrough, clamp
new_case
run_case 'RUN_START_EPOCH="$(now_epoch)"; rc=0; timed t1 1 sleep 5 || rc=$?; echo "rc=$rc"'
expect_out_has "timeout returns 124" "rc=124"
expect_out_has "TIMED OUT logged" "phase=t1 TIMED OUT after 1s"
run_case 'RUN_START_EPOCH="$(now_epoch)"; rc=0; timed t2 10 sh -c "exit 3" || rc=$?; echo "rc=$rc"'
expect_out_has "a command that exits 3 returns 3" "rc=3"
expect_out_lacks "no TIMED OUT for rc 3" "TIMED OUT"

# a phase timeout fails that phase
new_case
run_case 'ph_timeout() { timed ph_timeout 1 sleep 5; }; PHASES=(ph_timeout ph_never); main nightly'
expect_rc "a phase timeout fails the run with exit 1" 1
expect_out_has "phase timeout: TIMED OUT logged" "phase=ph_timeout TIMED OUT after 1s"
expect_eq "phase timeout: failed_phase" "$(state 5)" "ph_timeout"
expect_text_has "phase timeout: the notice says TIMED OUT" "$(payload)" "TIMED OUT"
expect_absent "phase timeout: later phases do not run" "$C/never-ran"

new_case
export ADV_TO=1099 NOTIFS_BACKUP_DEADLINE_SECS=100
run_case 'PHASES=(ph_adv ph_clamp); main nightly'
expect_rc "clamped phase fails the run with exit 1" 1
expect_out_has "timeout clamped to the 1s remaining, not the phase's 600s" "phase=ph_clamp start timeout=1s"
expect_out_has "clamped expiry logged as TIMED OUT" "phase=ph_clamp TIMED OUT after 1s"
expect_out_has "clamped expiry returns 124" "phase=ph_clamp end rc=124"
expect_eq "clamp: failed_phase" "$(state 5)" "ph_clamp"
new_case
export NOTIFS_BACKUP_DEADLINE_SECS=100
run_case 'PHASES=(ph_noclamp); main nightly'
expect_rc "positive control: a command inside the remaining time" 0
expect_out_has "positive control: not clamped (60s phase timeout < 100s remaining)" "phase=ph_noclamp start timeout=60s"
new_case
export ADV_TO=1099 NOTIFS_BACKUP_DEADLINE_SECS=100
run_case 'PHASES=(ph_adv ph_fastclamp); main nightly'
expect_rc "a fast command with the clamp engaged succeeds" 0
expect_out_has "fast command: clamp engaged (1s remaining, 600s phase timeout)" "phase=ph_fastclamp start timeout=1s"
expect_out_has "fast command ends rc=0" "phase=ph_fastclamp end rc=0"
expect_out_lacks "fast command: no TIMED OUT" "TIMED OUT"
new_case
run_case 'RUN_START_EPOCH=1000; DEADLINE_SECS=100; printf 1100 > "$CLK"; rc=0; timed late 10 touch "$C/ran" || rc=$?; echo "rc=$rc"'
expect_out_has "timed with no time left returns 124" "rc=124"
expect_absent "timed with no time left does not start the command" "$C/ran"
expect_out_has "timed with no time left says why" "total deadline of 100s already reached"

# ---------------------------------------------------------------------------------------------------------------
# 9. Total deadline and exempt calls
new_case
export ADV_TO=1100 NOTIFS_BACKUP_DEADLINE_SECS=100
run_case 'PHASES=(ph_adv ph_never); main nightly'
expect_rc "deadline reached: run exits 1" 1
expect_absent "the next phase is not started past the deadline" "$C/never-ran"
expect_eq "deadline: failed_phase is the phase it would have started" "$(state 5)" "ph_never"
expect_out_has "deadline exceeded message" "deadline exceeded"
expect_eq "deadline: exactly one failure notify" "$(ncurl)" "1"
expect_out_has "exempt: notify keeps its own 30s bound with the deadline exhausted" "phase=notify start timeout=30s"
expect_eq "exempt: state still written" "$(state 3)" "failed"
expect_text_has "deadline text in the Discord message" "$(payload)" "deadline exceeded"

new_case
export ADV_TO=1050 NOTIFS_BACKUP_DEADLINE_SECS=100
run_case 'PHASES=(ph_adv ph_never); main nightly'
expect_rc "positive control: phase ending before the deadline lets the next start" 0
expect_present "positive control: the next phase ran" "$C/never-ran"

for bad in 0 abc -5 "" 1.5; do
  new_case
  export NOTIFS_BACKUP_DEADLINE_SECS="$bad"
  run_case 'main nightly'
  expect_rc "NOTIFS_BACKUP_DEADLINE_SECS '$bad' exits 2" 2
done
new_case
export NOTIFS_BACKUP_DEADLINE_SECS=4200
run_case 'main nightly'
expect_rc "positive control: valid deadline value" 0
new_case
run_case 'main nightly'
expect_out_has "the default deadline is 4200s" "deadline=4200s"

# other override validation
new_case
export NOTIFS_BACKUP_ENV_FILE="relative/backup.env"
run_case 'main nightly'
expect_rc "relative path override exits 2" 2
new_case
export NOTIFS_BACKUP_LOCK=""
run_case 'main nightly'
expect_rc "empty path override exits 2" 2

# test overrides are honoured only with NOTIFS_BACKUP_TESTING=1 (fail closed)
new_case
unset NOTIFS_BACKUP_TESTING
export NOTIFS_BACKUP_ENV_FILE="relative/backup.env"
run_case 'id() { case "$1" in -u) echo 1001 ;; *) echo 1002 ;; esac; }; load_overrides; echo "ENV_FILE=$ENV_FILE ROOT=$BACKUP_ROOT LOCK=$LOCK_FILE STATE=$STATE_DIR LOG=$LOG_DIR OWNER=$ENV_OWNER"'
expect_out_has "without the flag the overrides are ignored (production env file)" "ENV_FILE=/home/4irl-notifs/secrets-persistent/backup.env"
expect_out_has "...and the production work root applies" "ROOT=/var/lib/notifs-backup/work"
expect_out_has "...and the production lock applies" "LOCK=/run/notifs-backup.lock"
expect_out_has "...and the production state dir applies" "STATE=/var/lib/notifs-backup/status"
expect_out_has "...and the production log dir applies" "LOG=/var/log/notifs-backup"
expect_out_has "...and the owner is the 4irl-notifs user's uid:gid" "OWNER=1001:1002"
expect_out_has "the ignored overrides are named in a warning" "ignoring test overrides without NOTIFS_BACKUP_TESTING=1"
new_case
export NOTIFS_BACKUP_TESTING=0
run_case 'resolve_env_owner() { ENV_OWNER=9:9; }; load_overrides; echo "ROOT=$BACKUP_ROOT"'
expect_out_has "a flag value other than 1 does not enable the overrides" "ROOT=/var/lib/notifs-backup/work"
new_case
unset NOTIFS_BACKUP_TESTING
export NOTIFS_BACKUP_DEADLINE_SECS=5
run_case 'resolve_env_owner() { ENV_OWNER=9:9; }; load_overrides; echo "DEADLINE=$DEADLINE_SECS"'
expect_out_has "the deadline override is ignored without the flag" "DEADLINE=4200"
new_case
run_case 'load_overrides; echo "ROOT=$BACKUP_ROOT"'
expect_out_has "positive control: with the flag the overrides apply" "ROOT=$C/root"

# prepare_staging refuses to rm -rf a non-production root without the sentinel
new_case
rm -f "$C/root/.notifs-backup-root"
mkdir -p "$C/root/staging"
: > "$C/root/staging/keep-me"
run_case 'load_overrides; r=0; prepare_staging || r=$?; echo "PREP_RC=$r"'
expect_out_has "no sentinel: prepare_staging fails" "PREP_RC=1"
expect_out_has "...naming the sentinel" ".notifs-backup-root"
if [ -f "$C/root/staging/keep-me" ]; then _pass; else _fail "no sentinel: staging was not wiped"; fi
new_case
rm -f "$C/root/.notifs-backup-root"
ln -s /dev/null "$C/root/.notifs-backup-root"
mkdir -p "$C/root/staging"
: > "$C/root/staging/keep-me"
run_case 'load_overrides; r=0; prepare_staging || r=$?; echo "PREP_RC=$r"'
expect_out_has "a symlink sentinel does not count" "PREP_RC=1"
if [ -f "$C/root/staging/keep-me" ]; then _pass; else _fail "symlink sentinel: staging was not wiped"; fi
new_case
mkdir -p "$C/root/staging"
: > "$C/root/staging/gone"
run_case 'load_overrides; r=0; prepare_staging || r=$?; echo "PREP_RC=$r"'
expect_out_has "positive control: with the sentinel prepare_staging succeeds" "PREP_RC=0"
if [ -f "$C/root/staging/gone" ]; then _fail "with the sentinel the old staging is wiped"; else _pass; fi
expect_eq "staging subdirs are created 0700" "$(stat -c %a "$C/root/staging" "$C/root/staging/postgres" "$C/root/staging/ntfy" | tr '\n' ' ')" "700 700 700 "
new_case
export NOTIFS_BACKUP_ROOT=/
run_case 'load_overrides; r=0; prepare_staging || r=$?; echo "PREP_RC=$r"'
expect_out_has "a root of / is refused" "PREP_RC=1"

# ---------------------------------------------------------------------------------------------------------------
# 10. Signals
new_case
run_bg 'PHASES=(ph_slow); main nightly'
if wait_for_text "$C/bg.out" "phase=ph_slow start"; then _pass; else _fail "TERM case: slow phase started"; fi
kill -TERM "$BGPID"
rc=0
wait "$BGPID" || rc=$?
out="$(cat "$C/bg.out")"
expect_rc "TERM exits 1" 1
expect_eq "TERM: state failed" "$(state 3)" "failed"
expect_eq "TERM: failed_phase is the phase reached" "$(state 5)" "ph_slow"
expect_eq "TERM: state is exactly one line" "$(state_lines)" "1"
expect_eq "TERM: exactly one notify" "$(ncurl)" "1"
expect_out_has "TERM logged" "signal TERM received during phase=ph_slow"
if flock -n "$C/lock" true; then _pass; else _fail "TERM: no lock left held"; fi

new_case
run_bg 'PHASES=(ph_slow); main nightly'
if wait_for_text "$C/bg.out" "phase=ph_slow start"; then _pass; else _fail "INT case: slow phase started"; fi
kill -INT "$BGPID"
rc=0
wait "$BGPID" || rc=$?
out="$(cat "$C/bg.out")"
expect_rc "INT exits 1" 1
expect_eq "INT: failed_phase is the phase reached" "$(state 5)" "ph_slow"
expect_eq "INT: exactly one notify" "$(ncurl)" "1"
if flock -n "$C/lock" true; then _pass; else _fail "INT: no lock left held"; fi

new_case
run_case 'PHASES=(ph_ok ph_selfkill ph_never); main nightly'
expect_rc "a run killed between phases exits 1" 1
expect_eq "killed run: state failed" "$(state 3)" "failed"
expect_eq "killed run: phase reached recorded" "$(state 5)" "ph_selfkill"
expect_absent "killed run: later phases did not run" "$C/never-ran"
expect_eq "killed run: one notify" "$(ncurl)" "1"

# ---------------------------------------------------------------------------------------------------------------
# 11. window.env and window warnings
wf="$here/../window.env"
expect_eq "window.env has exactly three lines" "$(wc -l < "$wf" | tr -d ' ')" "3"
expect_eq "window.env keys" "$(cut -d= -f1 "$wf" | tr '\n' ' ')" "BACKUP_WINDOW_START BACKUP_WINDOW_END BACKUP_WINDOW_TZ "
w_start="$(sed -n 's/^BACKUP_WINDOW_START=//p' "$wf")"
w_end="$(sed -n 's/^BACKUP_WINDOW_END=//p' "$wf")"
expect_eq "window.env START" "$w_start" "03:30"
expect_eq "window.env END" "$w_end" "05:00"
expect_eq "window.env TZ" "$(sed -n 's/^BACKUP_WINDOW_TZ=//p' "$wf")" "America/Los_Angeles"
new_case
out="$(bash -c '. "$SCRIPT"; window_file' 2>&1)"
expect_eq "window_file defaults to window.env beside backup.sh" "$out" "$(cd "$here/.." && pwd)/window.env"

new_case
export HHMM_TO=02:00
printf '02:00' > "$HHMM"
run_case 'PHASES=(ph_ok); main nightly'
expect_rc "nightly outside window is still ok" 0
expect_eq "outside window: state ok" "$(state 3)" "ok"
expect_text_has "outside window: warning in notify" "$(payload)" "started outside backup window"
new_case
run_case 'PHASES=(ph_ok); main nightly'
expect_text_lacks "inside window: no start warning" "$(payload)" "started outside backup window"
expect_text_lacks "inside window: no overran warning" "$(payload)" "overran backup window"
new_case
printf '02:00' > "$HHMM"
run_case 'PHASES=(ph_ok); main manual'
expect_rc "manual outside window ok" 0
expect_text_lacks "manual: no start warning" "$(payload)" "started outside backup window"
expect_eq "manual: positive control, notify was sent" "$(ncurl)" "1"
expect_text_has "manual: the notice names the mode" "$(payload)" "manual run ok"

new_case
export HHMM_TO=05:10
run_case 'PHASES=(ph_hhmm); main nightly'
expect_rc "overran nightly still ok" 0
expect_eq "overran: state ok" "$(state 3)" "ok"
expect_text_has "overran: warning in notify" "$(payload)" "overran backup window"
expect_text_lacks "overran: started inside, no start warning" "$(payload)" "started outside backup window"
for hh in 03:30 04:59; do
  new_case
  printf '%s' "$hh" > "$HHMM"
  run_case 'PHASES=(ph_ok); main nightly'
  expect_text_lacks "start at $hh (inside [03:30,05:00)): no start warning" "$(payload)" "started outside backup window"
  expect_eq "start at $hh: notify was sent" "$(ncurl)" "1"
done
for hh in 03:29 05:00; do
  new_case
  printf '%s' "$hh" > "$HHMM"
  run_case 'PHASES=(ph_ok); main nightly'
  expect_text_has "start at $hh (outside [03:30,05:00)): start warning" "$(payload)" "started outside backup window"
done
new_case
export HHMM_TO=05:00
run_case 'PHASES=(ph_hhmm); main nightly'
expect_text_has "ending exactly at END counts as overran" "$(payload)" "overran backup window"
new_case
export HHMM_TO=04:59
run_case 'PHASES=(ph_hhmm); main nightly'
expect_text_lacks "ending one minute before END: no overran warning" "$(payload)" "overran backup window"
new_case
export HHMM_TO=05:10
run_case 'PHASES=(ph_hhmm); main manual'
expect_text_lacks "manual ending after END: no overran warning" "$(payload)" "overran backup window"
expect_eq "manual overran control: notify was sent" "$(ncurl)" "1"

# a bad window file only disables the warnings
new_case
printf 'BACKUP_WINDOW_START=nonsense\n' > "$WINF"
printf '02:00' > "$HHMM"
run_case 'PHASES=(ph_ok); main nightly'
expect_rc "an invalid window file does not fail the run" 0
expect_out_has "invalid window file is logged" "window file values invalid; window warnings disabled"
expect_text_lacks "invalid window file: no warning in the notice" "$(payload)" "backup window"

# ---------------------------------------------------------------------------------------------------------------
# 12. Log pruning: newest 30 always kept, older ones only when also over 30 days old
new_case
mkdir -p "$C/logs"
for i in $(seq 1 40); do
  d="$(date -u -d "2020-01-01 +$i days" +%Y-%m-%d)"
  : > "$C/logs/$d.log"
  touch -d "$d" "$C/logs/$d.log"
done
run_case 'PHASES=(ph_ok); main nightly'
expect_rc "run with old logs present" 0
expect_eq "old logs pruned down to the newest 30 (including today's)" "$(find "$C/logs" -name '*.log' | wc -l | tr -d ' ')" "30"
new_case
mkdir -p "$C/logs"
for i in $(seq 1 40); do
  d="$(date -u -d "2020-01-01 +$i days" +%Y-%m-%d)"
  : > "$C/logs/$d.log"
done
run_case 'PHASES=(ph_ok); main nightly'
expect_eq "recent logs beyond 30 are kept (age rule)" "$(find "$C/logs" -name '*.log' | wc -l | tr -d ' ')" "41"

# ---------------------------------------------------------------------------------------------------------------
# 13. Data phases (the real PHASES list; stateful stubs for docker, restic and curl)

# run_data [body]: backup.sh sourced with its own PHASES, then the body (default "main nightly").
run_data() {
  rc=0
  out="$(env CASE_BODY="${1:-main nightly}" bash -c '. "$SCRIPT"; eval "$PRELUDE"; eval "$CASE_BODY"' 2>&1)" || rc=$?
}
hb_calls() { grep -c 'heartbeats' "$CALL_LOG" || true; }
# refs_secret_path <text>: true when the text names the secrets directory or a master key
refs_secret_path() { grep -qE 'secrets-persistent|DELIVERY_MASTER_KEY' <<< "$1"; }

# happy path
new_case
run_data
expect_rc "data run succeeds" 0
expect_eq "data run: state ok" "$(state 3)" "ok"
expect_eq "data run: snapshot id from the JSON summary line" "$(state 4)" "abc12345"
expect_text_has "ok notice carries snapshot" "$(payload)" "snapshot abc12345, dump "
expect_text_has "ok notice carries dump size" "$(payload)" "dump 12 bytes"
expect_calls_has "preflight restic cat config" "restic cat config"
expect_calls_has "backup argv: host, tags, json" "restic backup --json --host 4irl-notifs --tag notifs --tag nightly $C/root/staging"
expect_calls_has "forget argv: host, tag, group-by, exact keeps" "restic forget --prune --host 4irl-notifs --tag notifs --group-by host --keep-daily 7 --keep-weekly 4 --keep-monthly 6"
expect_calls_has "check runs" "restic check"
expect_calls_has "pg_dump argv" "docker exec 4irl-notifs-delivery-postgres pg_dump -U delivery -Fc -d delivery"
expect_calls_has "sqlite run: user 1000:1000" "--user 1000:1000"
expect_calls_has "sqlite run: no network" "--network none"
expect_calls_has "sqlite run: pinned image" "keinos/sqlite3:3.46.1@sha256:055d4be20d868f4077598cbbc33ce4ff39dbefbbe0b87e39ca7b70f9f51caf51 sqlite3 file:/src/auth.db?mode=ro .backup /out/auth.db"
expect_calls_has "heartbeat success URL" "-X POST -o /dev/null -w %{http_code} -K"
expect_calls_has "heartbeat success URL target" "http://127.0.0.1:8300/v1/heartbeats/notifs_ops/nightly_backup"
expect_calls_lacks "ok run: no /fail ping" "nightly_backup/fail"
expect_calls_lacks "dak_ key never on any argv" "dak_"
if grep -qF "Authorization: Bearer $KEY_VALUE" "$C/hb.cfg"; then _pass; else _fail "the -K config carries the dak_ key"; fi
expect_eq "staged tree has the dump and auth.db only" "$(sort "$C/staged.txt" | tr '\n' ' ')" "./ntfy/auth.db ./postgres/delivery.dump "
expect_absent "staging deleted after the run" "$C/root/staging"
# secrets-persistent: scoped to the restic backup argv and the staged tree, with positive controls
if refs_secret_path "$(grep '^restic backup' "$CALL_LOG")" || refs_secret_path "$(cat "$C/staged.txt")"; then
  _fail "no restic backup argument or staged path names secrets-persistent or DELIVERY_MASTER_KEY"
else _pass; fi
if refs_secret_path "$NOTIFS_BACKUP_HEARTBEAT_KEY_FILE" && refs_secret_path "restic backup /x/DELIVERY_MASTER_KEY"; then _pass; else _fail "positive control: the scan detects both secret names"; fi
expect_text_lacks "the key file path never reaches a restic call" "$(grep '^restic' "$CALL_LOG")" "NOTIFS_OPS_DELIVERY_KEY"

# pg_wait: starting then healthy passes; never healthy fails at the limit
new_case
printf 'starting\nstarting\nhealthy\n' > "$C/health.seq"
export NOTIFS_BACKUP_PG_WAIT_SECS=10
run_data
expect_rc "pg_wait passes after starting then healthy" 0
expect_out_has "pg_wait logs the state it saw" "pg_wait: 4irl-notifs-delivery-postgres state=starting"
new_case
printf 'starting\n' > "$C/health.seq"
run_data
expect_rc "pg_wait fails when never healthy" 1
expect_eq "pg_wait failure names the phase" "$(state 5)" "pg_wait"
expect_calls_lacks "no dump after a pg_wait failure" "pg_dump"
expect_calls_lacks "no restic backup after a pg_wait failure" "restic backup"
expect_eq "a failed run pings /fail" "$(grep -c 'nightly_backup/fail' "$CALL_LOG" || true)" "1"
expect_text_has "failed notice names the phase" "$(payload)" "FAILED at phase pg_wait"

# pg_verify: TOC without public.destinations
new_case
printf '201; 1259 16410 TABLE public messages delivery\n' > "$C/toc"
run_data
expect_rc "a TOC missing public.destinations fails" 1
expect_eq "pg_verify names the phase" "$(state 5)" "pg_verify"
expect_calls_lacks "nothing uploaded after pg_verify fails" "restic backup"
expect_out_has "pg_verify message" "dump TOC lacks TABLE public.destinations"

# ntfy_auth: integrity_check other than ok
new_case
printf '*** in database main ***\n' > "$C/integrity"
run_data
expect_rc "integrity_check other than ok fails" 1
expect_eq "ntfy_auth names the phase" "$(state 5)" "ntfy_auth"
expect_calls_lacks "nothing uploaded after ntfy_auth fails" "restic backup"

# restic exit 3: snapshot exists but the run fails
new_case
printf '3' > "$C/fail_restic-backup"
run_data
expect_rc "restic exit 3 fails the run" 1
expect_eq "exit 3 names restic_backup" "$(state 5)" "restic_backup"
expect_eq "exit 3 keeps the snapshot id that exists" "$(state 4)" "abc12345"
expect_calls_lacks "no forget after exit 3" "restic forget"
expect_absent "staging deleted after a failed backup" "$C/root/staging"
expect_text_lacks "restic error text is masked" "$(payload)" "$PW_SECRET"

# forget guard: the new id is not in the listing
new_case
printf '[{"id":"ffff0000aaaa","short_id":"ffff0000"}]\n' > "$C/snaps.post"
run_data
expect_rc "forget guard fails the run when the new id is missing" 1
expect_eq "guard names restic_forget" "$(state 5)" "restic_forget"
expect_calls_lacks "forget skipped by the guard" "restic forget"
# forget guard: id listed but the count did not grow
new_case
printf '[{"id":"abc12345deadbeef","short_id":"abc12345"}]\n' > "$C/snaps.pre"
run_data
expect_rc "forget guard fails when the count did not grow" 1
expect_calls_lacks "forget skipped when the count did not grow" "restic forget"

# volumes: an unexpected prefixed volume is warned about, rc unchanged
new_case
printf '4irl-notifs_delivery-pgdata\n4irl-notifs_ntfy-auth\n4irl-notifs_ntfy-cache\n4irl-notifs_newthing\nu4i_x\n' > "$C/volumes"
run_data
expect_rc "an unexpected volume does not fail the run" 0
expect_text_has "notice names the unbacked volume" "$(payload)" "not backed up: 4irl-notifs_newthing"
expect_text_lacks "skipped and backed-up volumes are not named" "$(payload)" "ntfy-cache"

# heartbeat: failure at phase lock pings nothing; a later failure pings /fail
new_case
run_data 'exec 8> "$C/lock"; flock -n 8; HEARTBEAT_ENABLED=1; main nightly'
expect_rc "lock held fails the run" 1
expect_absent "lock failure writes no state file" "$C/status/last-run.tsv"
expect_eq "lock failure sends no heartbeat call at all" "$(hb_calls)" "0"
expect_eq "lock failure still sends the Discord notice" "$(ncurl)" "1"
expect_text_has "lock failure notice" "$(payload)" "FAILED at phase lock"

# heartbeat problems never change the result
new_case
rm -f "$C/secrets-persistent/NOTIFS_OPS_DELIVERY_KEY"
run_data
expect_rc "missing key file: rc unchanged (ok run stays ok)" 0
expect_eq "missing key file: no heartbeat call" "$(hb_calls)" "0"
expect_text_has "missing key file: warning in the notice" "$(payload)" "warning: heartbeat: key file missing"
new_case
printf 'dak_short' > "$C/secrets-persistent/NOTIFS_OPS_DELIVERY_KEY"
run_data
expect_rc "bad key file: rc unchanged" 0
expect_eq "bad key file: no heartbeat call" "$(hb_calls)" "0"
expect_text_has "bad key file: warning" "$(payload)" "heartbeat: key file malformed"
new_case
printf '503' > "$C/curl.http"
run_data
expect_rc "503 answer: ok run stays ok" 0
expect_eq "503 answer: the ping was attempted once" "$(hb_calls)" "1"
expect_text_has "503 answer: warning in the notice" "$(payload)" "heartbeat: POST failed (rc=0 http=503)"
new_case
printf '503' > "$C/curl.http"
printf 'starting\n' > "$C/health.seq"
run_data
expect_rc "503 on a failed run: still failed" 1
expect_eq "503 on a failed run: failure phase kept" "$(state 5)" "pg_wait"
expect_text_has "503 on a failed run: both reported" "$(payload)" "heartbeat: POST failed"

# key file form: trailing LF or CRLF is malformed (the reader strips nothing)
new_case
printf '%s\n' "$KEY_VALUE" > "$C/secrets-persistent/NOTIFS_OPS_DELIVERY_KEY"
run_data
expect_rc "key plus LF: rc unchanged" 0
expect_eq "key plus LF: no ping" "$(hb_calls)" "0"
expect_text_has "key plus LF: warning" "$(payload)" "heartbeat: key file malformed"
new_case
printf '%s\r\n' "$KEY_VALUE" > "$C/secrets-persistent/NOTIFS_OPS_DELIVERY_KEY"
run_data
expect_rc "key plus CRLF: rc unchanged" 0
expect_eq "key plus CRLF: no ping" "$(hb_calls)" "0"
expect_text_has "key plus CRLF: warning" "$(payload)" "heartbeat: key file malformed"
new_case
run_data
expect_eq "bare 47-character key pings" "$(hb_calls)" "1"

# symlinked key file: refused with a warning, no ping
new_case
mv "$C/secrets-persistent/NOTIFS_OPS_DELIVERY_KEY" "$C/real-key"
ln -s "$C/real-key" "$C/secrets-persistent/NOTIFS_OPS_DELIVERY_KEY"
run_data
expect_rc "symlinked key file: rc unchanged" 0
expect_eq "symlinked key file: no ping" "$(hb_calls)" "0"
expect_text_has "symlinked key file: warning in the notice" "$(payload)" "heartbeat: key file missing or unreadable"

# failed run: the full /fail URL is posted and the OK URL never is
new_case
printf 'starting\n' > "$C/health.seq"
run_data
expect_rc "failed run for the /fail URL check" 1
expect_calls_has "failed run: the full /fail URL" "http://127.0.0.1:8300/v1/heartbeats/notifs_ops/nightly_backup/fail"
expect_eq "failed run: the OK URL is never posted" "$(grep -cE 'nightly_backup$' "$CALL_LOG" || true)" "0"

# restic timeout path: rc 124 from a restic call triggers `restic unlock`, the timed output reaches the notice
new_case
printf '124' > "$C/fail_restic-backup"
run_data
expect_rc "restic backup timeout fails the run" 1
expect_eq "timeout: failed phase is restic_backup" "$(state 5)" "restic_backup"
expect_calls_has "timeout: restic unlock is called" "restic unlock"
expect_text_has "timeout: the notice says TIMED OUT" "$(payload)" "TIMED OUT"
expect_text_has "timeout: the timed output is kept, masked, in the notice" "$(payload)" "Fatal: wrong password ***"
expect_calls_lacks "timeout: no forget after the timeout" "restic forget"
new_case
printf '124' > "$C/fail_restic-backup"
printf '1' > "$C/fail_restic-unlock"
run_data
expect_rc "timeout with a failing unlock: rc is still 1" 1
expect_eq "timeout with a failing unlock: failed phase unchanged" "$(state 5)" "restic_backup"
expect_text_has "timeout with a failing unlock: only logged" "$(logtext)" "restic_unlock failed; a stale repository lock may remain"
expect_text_has "timeout with a failing unlock: the notice keeps the backup's timed output" "$(payload)" "TIMED OUT"
expect_text_lacks "timeout with a failing unlock: the unlock error does not replace the detail" "$(payload)" "injected"
new_case
printf '2' > "$C/fail_restic-backup"
run_data
expect_calls_lacks "a non-timeout restic failure does not unlock" "restic unlock"

# data-phase failure branches: rc 1, the failed phase, the phase message, and nothing from a later phase
# expect_phase_fail <label> <phase> <message> [forbidden call]
expect_phase_fail() {
  expect_rc "$1: run fails with rc 1" 1
  expect_eq "$1: failed phase" "$(state 5)" "$2"
  expect_eq "$1: state failed" "$(state 3)" "failed"
  expect_text_has "$1: notice names the phase and message" "$(payload)" "FAILED at phase $2: $3"
  if [ -n "${4:-}" ]; then expect_calls_lacks "$1: no later phase ran ($4)" "$4"; fi
  expect_eq "$1: exactly one /fail ping" "$(grep -c 'nightly_backup/fail' "$CALL_LOG" || true)" "1"
}

new_case
printf '1' > "$C/fail_restic-cat"
run_data
expect_phase_fail "preflight repo unreachable" preflight "restic repository not reachable" "docker inspect"

new_case
rm -f "$C/root/.notifs-backup-root"
run_data
expect_phase_fail "preflight prepare_staging" preflight "cannot prepare staging" "docker inspect"
expect_out_has "preflight prepare_staging: the sentinel refusal is logged" "refusing to wipe"

new_case
printf '3' > "$C/fail_docker-pg_dump"
run_data
expect_phase_fail "pg_dump nonzero rc" pg_dump "pg_dump failed" "pg_restore"

new_case
: > "$C/dump.empty"
run_data
expect_phase_fail "pg_dump empty dump" pg_dump "pg_dump produced an empty dump" "pg_restore"

new_case
: > "$C/no-authdb"
run_data
expect_phase_fail "ntfy_auth no auth.db" ntfy_auth "sqlite .backup produced no auth.db" "restic backup"

new_case
printf '1' > "$C/fail_docker-sqlite-copy"
run_data
expect_phase_fail "ntfy_auth copy failed" ntfy_auth "ntfy auth.db copy failed" "restic backup"

# chown failure is tolerated under NOTIFS_BACKUP_TESTING=1 only; a PHASES entry flips the flag just before the phase
new_case
printf '#!/usr/bin/env bash\nexit 1\n' > "$STUB_BIN/chown"
chmod +x "$STUB_BIN/chown"
run_data
expect_rc "ntfy_auth chown failure is tolerated under NOTIFS_BACKUP_TESTING=1" 0
expect_out_has "ntfy_auth chown tolerance is logged" "chown failed (tolerated under NOTIFS_BACKUP_TESTING=1)"
new_case
printf '#!/usr/bin/env bash\nexit 1\n' > "$STUB_BIN/chown"
chmod +x "$STUB_BIN/chown"
run_data 'ph_strict() { NOTIFS_BACKUP_TESTING=0; }; PHASES=(phase_hb_arm phase_preflight phase_pg_wait phase_pg_dump phase_pg_verify ph_strict phase_ntfy_auth phase_restic_backup phase_restic_forget phase_restic_check phase_volumes); main nightly'
expect_phase_fail "ntfy_auth chown" ntfy_auth "cannot chown the ntfy copy dir" "restic backup"
rm -f "$STUB_BIN/chown"

new_case
printf '2' > "$C/fail_restic-backup"
run_data
expect_phase_fail "restic_backup rc other than 3" restic_backup "restic backup failed" "restic forget"

new_case
: > "$C/no-summary"
run_data
expect_phase_fail "restic_backup no snapshot id" restic_backup "restic backup printed no snapshot id" "restic forget"

new_case
printf '1' > "$C/fail_restic-forget"
run_data
expect_phase_fail "restic forget" restic_forget "restic forget failed" "restic check"

new_case
printf '1' > "$C/fail_restic-check"
run_data
expect_phase_fail "restic check" restic_check "restic check failed" "docker volume"

# volumes: a docker failure is a warning only
new_case
printf '1' > "$C/fail_docker-volume"
run_data
expect_rc "volumes: docker volume ls failure keeps rc 0" 0
expect_eq "volumes: state ok" "$(state 3)" "ok"
expect_text_has "volumes: the failure is a warning in the notice" "$(payload)" "volume check failed (docker volume ls)"
expect_calls_lacks "volumes: ok run pings no /fail" "nightly_backup/fail"

t_finish
