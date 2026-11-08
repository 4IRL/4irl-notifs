#!/usr/bin/env bash
# notifs-backup runner (root, host systemd oneshot or run by hand). Usage: backup.sh nightly|manual
#
# Ported from apps/platform/backup/backup.sh (GPropersi/apps): the framework (env, lock, logging, notify, state file,
# window, deadline, single exit path) plus the data phases (preflight, pg_wait, pg_dump, pg_verify, ntfy_auth,
# restic_backup, restic_forget, restic_check, volumes) and the heartbeat ping sent from the exit path.
#
# Exit codes: 0 ok, 1 failed, 2 usage or env/override problem.
#
# Credentials: /etc/notifs-backup/backup.env is read by this script itself with a literal KEY=VALUE reader (never
# sourced; see read_backup_env). The systemd unit has no EnvironmentFile=, so nothing depends on the caller's environment.
#
# Test overrides (all optional, each read once at startup, default = production value, an unparsable value exits 2).
# They are honoured ONLY when NOTIFS_BACKUP_TESTING=1; otherwise every NOTIFS_BACKUP_* / INSTALL_BACKUP_* variable is
# ignored (unset, named in a warning) and the production values apply. A work root other than the production one must
# hold a .notifs-backup-root sentinel file before staging/ is wiped.
#   NOTIFS_BACKUP_ENV_FILE        /etc/notifs-backup/backup.env
#   NOTIFS_BACKUP_ENV_OWNER       0:0   (numeric uid:gid the env file must have; mode 0600 and no-symlink never relax)
#   NOTIFS_BACKUP_ROOT            /var/lib/notifs-backup/work   (staging/, restic-cache/, restore scratch)
#   NOTIFS_BACKUP_STATE_DIR       /var/lib/notifs-backup/status   (last-run.tsv)
#   NOTIFS_BACKUP_LOG_DIR         /var/log/notifs-backup
#   NOTIFS_BACKUP_LOCK            /run/notifs-backup.lock
#   NOTIFS_BACKUP_DEADLINE_SECS   4200  (positive integer)
#   NOTIFS_BACKUP_PG_CONTAINER / NOTIFS_BACKUP_NTFY_VOLUME / NOTIFS_BACKUP_SQLITE_IMAGE   names (no whitespace)
#   NOTIFS_BACKUP_PG_WAIT_SECS / NOTIFS_BACKUP_PG_POLL_SECS   120 / 2  (digits; poll at least 1)
#   NOTIFS_BACKUP_HEARTBEAT_KEY_FILE   /home/4irl-notifs/secrets-persistent/NOTIFS_OPS_DELIVERY_KEY
#
# The window (window.env beside this script) and the clock (now_epoch, now_iso, now_hhmm, window_file) are plain
# functions so a test can redefine them after sourcing this file; the BASH_SOURCE guard at the bottom keeps main from
# running on source, and a test may also replace PHASES with dummy phases.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

ENV_KEYS=(RESTIC_REPOSITORY RESTIC_PASSWORD AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_DEFAULT_REGION DISCORD_WEBHOOK)
RESTIC_REPOSITORY=""
RESTIC_PASSWORD=""
AWS_ACCESS_KEY_ID=""
AWS_SECRET_ACCESS_KEY=""
# Exported for restic and the S3 SDK (via ENV_KEYS) and set by read_backup_env, so shellcheck sees no direct use.
# shellcheck disable=SC2034
AWS_DEFAULT_REGION=""
DISCORD_WEBHOOK=""

# Production locations (the overrides in load_overrides default to these; prepare_staging compares against PROD_ROOT).
PROD_ROOT=/var/lib/notifs-backup/work
ROOT_SENTINEL=.notifs-backup-root

# Phases run in order after env and lock. Each entry is a function that returns 0 on success; it runs its external
# calls through timed. A function named phase_<name> has the label <name> (failed_phase, logs); a phase may refine
# CUR_PHASE and set PHASE_MSG for the failure text. run_phases calls each phase in a || context, so errexit is OFF inside
# a phase body: a phase must check the rc of every timed call and return non-zero itself on failure.
# phase_hb_arm only arms the heartbeat: the ping itself is sent from on_exit (heartbeat_ping) because it needs the final
# result, ok or failed, and must also run when a later phase failed. It is first so that a failure at preflight or any
# later phase pings /fail; a failure at env or lock (before it) never pings.
PHASES=(phase_hb_arm phase_preflight phase_pg_wait phase_pg_dump phase_pg_verify phase_ntfy_auth phase_restic_backup
  phase_restic_forget phase_restic_check phase_volumes)

# Runtime constants (defined here and nowhere else under ops/backup; the installer parses them from this file). Each is
# overridable only under NOTIFS_BACKUP_TESTING=1 (see load_overrides).
PG_CONTAINER=4irl-notifs-delivery-postgres
NTFY_VOLUME=4irl-notifs_ntfy-auth
PG_VOLUME=4irl-notifs_delivery-pgdata
SKIPPED_VOLUME=4irl-notifs_ntfy-cache
VOLUME_PREFIX=4irl-notifs_
# sqlite3 image for the online ntfy auth.db copy. keinos/sqlite3 3.46.1 (Alpine, sqlite3 CLI at /usr/bin/sqlite3, no
# entrypoint, so `sqlite3 ...` is the command): its sqlite3 supports the `.backup` dot command and `file:...?mode=ro`
# URI filenames (both checked by running it), it is small, and the tag is pinned by digest so a moved tag cannot change
# what runs as root on the VPS. The run is --network none and --user 1000:1000 (ntfy's uid), see phase_ntfy_auth.
SQLITE_IMAGE='keinos/sqlite3:3.46.1@sha256:055d4be20d868f4077598cbbc33ce4ff39dbefbbe0b87e39ca7b70f9f51caf51'
NTFY_UID_GID=1000:1000
RESTIC_HOST=4irl-notifs
RESTIC_TAG=notifs
PG_WAIT_SECS=120
PG_POLL_SECS=2
# Written bare (47 characters, no newline) by ops/notifs-ops/write-key.sh; read only for the heartbeat ping header.
HB_KEY_FILE=/home/4irl-notifs/secrets-persistent/NOTIFS_OPS_DELIVERY_KEY
HB_URL=http://127.0.0.1:8300/v1/heartbeats/notifs_ops/nightly_backup
HEARTBEAT_ENABLED=0
PRE_COUNT=0

NOTIFY_PHASE_SECS=30

# Caller-environment variables that could redirect or re-credential restic, rclone, docker or the S3 SDK: removed in phase_env.
SCRUB_ENV=(RESTIC_PASSWORD_FILE RESTIC_PASSWORD_COMMAND RESTIC_REPOSITORY_FILE RESTIC_FROM_REPOSITORY
  RESTIC_FROM_PASSWORD_FILE RESTIC_FROM_PASSWORD_COMMAND RESTIC_FROM_REPOSITORY_FILE RESTIC_KEY_HINT RESTIC_CACHE_DIR
  RESTIC_COMPRESSION RESTIC_PACK_SIZE RESTIC_READ_CONCURRENCY AWS_SESSION_TOKEN AWS_PROFILE AWS_SHARED_CREDENTIALS_FILE
  AWS_CONFIG_FILE AWS_ROLE_ARN AWS_WEB_IDENTITY_TOKEN_FILE AWS_REGION AWS_ENDPOINT_URL AWS_ENDPOINT_URL_S3
  DOCKER_HOST DOCKER_CONTEXT DOCKER_CONFIG)

PHASE_MSG=""
STAGING=""
BACKUP_DONE=0

RUN_MODE=""
RUN_START_EPOCH=""
RUN_START_ISO=""
DEADLINE_SECS=4200
CUR_PHASE="env"
FAIL_PHASE=""
FAIL_MSG=""
FAIL_DETAIL=""
SNAPSHOT_ID=""
DUMP_SIZE=""
NOTIFY_ENABLED=0
WARNINGS=()
WINDOW_OK=0
WINDOW_START=""
WINDOW_END=""
WINDOW_TZ=""
START_IN_WINDOW=-1
LOG_FILE=""
WORK=""
TIMED_LAST_FILE="/dev/null"
TIMED_PID=""
ENV_READ_ERROR=""

# ---- clock and window source (redefinable in tests) ----

now_epoch() { date +%s; }
now_iso() { date -u +%Y-%m-%dT%H:%M:%SZ; }
now_hhmm() { TZ="$WINDOW_TZ" date +%H:%M; }
window_file() { printf '%s/window.env' "$SCRIPT_DIR"; }

# ---- masking and logging ----

# mask_text <text>: replaces every secret value with *** (values loaded from backup.env, all but the region; empty values skipped).
mask_text() {
  local s="$1" v
  for v in "$RESTIC_REPOSITORY" "$RESTIC_PASSWORD" "$AWS_ACCESS_KEY_ID" "$AWS_SECRET_ACCESS_KEY" "$DISCORD_WEBHOOK"; do
    if [ -n "$v" ]; then s=${s//"$v"/***}; fi
  done
  # The heartbeat key is read from its own file, not backup.env, so mask by shape (dak_ plus 43 characters).
  s="$(printf '%s' "$s" | sed -E 's/dak_[A-Za-z0-9_-]{43}/***/g')"
  printf '%s' "$s"
}

# log <text>: timestamped, masked, to stdout (journald) and the dated file log.
log() {
  local line
  line="$(now_iso) $(mask_text "$*")"
  printf '%s\n' "$line"
  if [ -n "$LOG_FILE" ]; then printf '%s\n' "$line" >> "$LOG_FILE" 2>/dev/null || true; fi
}

init_logging() {
  local created=0
  if [ ! -d "$LOG_DIR" ]; then created=1; fi
  if (umask 027 && mkdir -p -- "$LOG_DIR") 2>/dev/null; then
    if [ "$created" -eq 1 ]; then chmod 0750 -- "$LOG_DIR" 2>/dev/null || true; fi
    LOG_FILE="$LOG_DIR/$(date -u +%Y-%m-%d).log"
    if ! (umask 027 && : >> "$LOG_FILE") 2>/dev/null; then
      LOG_FILE=""
    else
      chmod 0640 -- "$LOG_FILE" 2>/dev/null || true
    fi
  fi
  if [ -z "$LOG_FILE" ]; then echo "WARN: cannot write a file log under $LOG_DIR; logging to stdout only" >&2; fi
  return 0
}

# prune_logs: keep the newest 30 dated logs always; older ones go only when also more than 30 days old.
prune_logs() {
  local f n=0
  [ -d "$LOG_DIR" ] || return 0
  while IFS= read -r f; do
    n=$((n + 1))
    if [ "$n" -gt 30 ] && [ -n "$(find "$f" -maxdepth 0 -mtime +30 2>/dev/null)" ]; then
      rm -f -- "$f"
      log "prune: removed old log $f"
    fi
  done < <(find "$LOG_DIR" -maxdepth 1 -type f -name '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9].log' | sort -r)
  return 0
}

# ---- overrides ----

# ov_path <target-var> <OVERRIDE_NAME> <default>: absolute path without whitespace.
ov_path() {
  local name="$2" v
  if [ -n "${!name+x}" ]; then v="${!name}"; else v="$3"; fi
  if [[ "$v" != /* ]] || [[ "$v" == *[[:space:]]* ]]; then
    echo "FATAL: $name must be an absolute path without whitespace" >&2
    exit 2
  fi
  printf -v "$1" '%s' "$v"
}

# ov_word <target-var> <OVERRIDE_NAME>: when set, a name/image reference without whitespace or shell metacharacters.
ov_word() {
  local name="$2"
  if [ -z "${!name+x}" ]; then return 0; fi
  if [[ ! "${!name}" =~ ^[A-Za-z0-9][A-Za-z0-9_.:@/-]*$ ]]; then
    echo "FATAL: $name must be a plain name (letters, digits, _ . : @ / -)" >&2
    exit 2
  fi
  printf -v "$1" '%s' "${!name}"
}

# ov_int <target-var> <OVERRIDE_NAME> <min>: when set, a non-negative integer (at most 6 digits) of at least min.
ov_int() {
  local name="$2"
  if [ -z "${!name+x}" ]; then return 0; fi
  if [[ ! "${!name}" =~ ^[0-9]{1,6}$ ]] || [ "${!name}" -lt "$3" ]; then
    echo "FATAL: $name must be an integer of at least $3 (at most 6 digits)" >&2
    exit 2
  fi
  printf -v "$1" '%s' "${!name}"
}

# gate_test_overrides: fail closed. The NOTIFS_BACKUP_* / INSTALL_BACKUP_* overrides are test hooks (a different chown,
# work root, env file, ...); they are honoured only with NOTIFS_BACKUP_TESTING=1 and are otherwise unset (named in a
# warning, never their values), so a caller environment that leaks through sudo -E / env_keep cannot redirect a root run.
gate_test_overrides() {
  local v dropped=""
  if [ "${NOTIFS_BACKUP_TESTING:-}" = "1" ]; then return 0; fi
  for v in $(compgen -v); do
    case "$v" in
      NOTIFS_BACKUP_* | INSTALL_BACKUP_*) unset "$v"; dropped="$dropped $v" ;;
      *) ;;
    esac
  done
  if [ -n "$dropped" ]; then echo "WARN: ignoring test overrides without NOTIFS_BACKUP_TESTING=1:$dropped" >&2; fi
  return 0
}

load_overrides() {
  gate_test_overrides
  ov_path ENV_FILE NOTIFS_BACKUP_ENV_FILE /etc/notifs-backup/backup.env
  ov_path BACKUP_ROOT NOTIFS_BACKUP_ROOT "$PROD_ROOT"
  ov_path STATE_DIR NOTIFS_BACKUP_STATE_DIR /var/lib/notifs-backup/status
  ov_path LOG_DIR NOTIFS_BACKUP_LOG_DIR /var/log/notifs-backup
  ov_path LOCK_FILE NOTIFS_BACKUP_LOCK /run/notifs-backup.lock
  ov_path HB_KEY_FILE NOTIFS_BACKUP_HEARTBEAT_KEY_FILE "$HB_KEY_FILE"
  ov_word PG_CONTAINER NOTIFS_BACKUP_PG_CONTAINER
  ov_word NTFY_VOLUME NOTIFS_BACKUP_NTFY_VOLUME
  ov_word SQLITE_IMAGE NOTIFS_BACKUP_SQLITE_IMAGE
  ov_int PG_WAIT_SECS NOTIFS_BACKUP_PG_WAIT_SECS 0
  ov_int PG_POLL_SECS NOTIFS_BACKUP_PG_POLL_SECS 1
  # Honoured only when set; default 0:0. A set-but-malformed (or empty) value exits 2.
  ENV_OWNER="0:0"
  if [ -n "${NOTIFS_BACKUP_ENV_OWNER+x}" ]; then
    if [[ ! "$NOTIFS_BACKUP_ENV_OWNER" =~ ^[0-9]+:[0-9]+$ ]]; then
      echo "FATAL: NOTIFS_BACKUP_ENV_OWNER must match uid:gid (digits)" >&2
      exit 2
    fi
    ENV_OWNER="$NOTIFS_BACKUP_ENV_OWNER"
  fi
  if [ -n "${NOTIFS_BACKUP_DEADLINE_SECS+x}" ]; then
    if [[ ! "$NOTIFS_BACKUP_DEADLINE_SECS" =~ ^[1-9][0-9]{0,6}$ ]]; then
      echo "FATAL: NOTIFS_BACKUP_DEADLINE_SECS must be a positive integer (at most 7 digits)" >&2
      exit 2
    fi
    DEADLINE_SECS="$NOTIFS_BACKUP_DEADLINE_SECS"
  fi
}

# ---- literal env reader (BYTE-IDENTICAL in every script that carries a copy: backup.sh, restore.sh, install-backup.sh) ----
# BEGIN shared-env-reader
# read_backup_env <file> <expected uid:gid>: refuses (return 1, message in ENV_READ_ERROR, never a value) a symlink or
# non-regular file, a wrong owner, a mode other than 0600, a CR, a line without "=", an unknown or duplicate key and a
# value starting with a quote. Each line splits at the FIRST "="; the value is everything after it, with no quote,
# backslash, whitespace or comment processing. Blank lines and lines starting with "#" are skipped. Never sources.
read_backup_env() {
  local f="$1" want_owner="$2" line key val st n=0 seen=" " k
  ENV_READ_ERROR=""
  for k in RESTIC_REPOSITORY RESTIC_PASSWORD AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_DEFAULT_REGION DISCORD_WEBHOOK; do
    printf -v "$k" '%s' ""
  done
  if [ -L "$f" ]; then ENV_READ_ERROR="env file is a symlink"; return 1; fi
  if [ ! -f "$f" ]; then ENV_READ_ERROR="env file is missing or not a regular file"; return 1; fi
  if ! st="$(stat -c '%u:%g %a' -- "$f" 2>/dev/null)"; then ENV_READ_ERROR="cannot stat env file"; return 1; fi
  if [ "${st%% *}" != "$want_owner" ]; then ENV_READ_ERROR="env file owner is not $want_owner"; return 1; fi
  if [ "${st##* }" != "600" ]; then ENV_READ_ERROR="env file mode is not 0600"; return 1; fi
  while IFS= read -r line || [ -n "$line" ]; do
    n=$((n + 1))
    case "$line" in *$'\r'*) ENV_READ_ERROR="env file line $n contains a CR"; return 1 ;; esac
    case "$line" in '' | '#'*) continue ;; esac
    case "$line" in *=*) ;; *) ENV_READ_ERROR="env file line $n has no ="; return 1 ;; esac
    key="${line%%=*}"
    val="${line#*=}"
    case "$key" in
      RESTIC_REPOSITORY | RESTIC_PASSWORD | AWS_ACCESS_KEY_ID | AWS_SECRET_ACCESS_KEY | AWS_DEFAULT_REGION | DISCORD_WEBHOOK) ;;
      *) ENV_READ_ERROR="env file line $n has an unknown key"; return 1 ;;
    esac
    case "$seen" in *" $key "*) ENV_READ_ERROR="env file repeats key $key"; return 1 ;; esac
    case "$val" in '"'* | "'"*) ENV_READ_ERROR="env file value for $key starts with a quote"; return 1 ;; esac
    seen="$seen$key "
    printf -v "$key" '%s' "$val"
  done < "$f"
  return 0
}
# END shared-env-reader

# ---- JSON and notify ----

# json_escape <string>: JSON string body (no surrounding quotes), pure bash.
json_escape() {
  local s="$1" i c u
  s=${s//\\/\\\\}
  s=${s//\"/\\\"}
  s=${s//$'\n'/\\n}
  s=${s//$'\t'/\\t}
  s=${s//$'\r'/\\r}
  for i in {1..31}; do
    if [ "$i" -eq 9 ] || [ "$i" -eq 10 ] || [ "$i" -eq 13 ]; then continue; fi
    printf -v c '%b' "\\0$(printf '%03o' "$i")"
    printf -v u '\\u%04x' "$i"
    s=${s//"$c"/$u}
  done
  printf '%s' "$s"
}

# notify <ok|failed> <text>: Discord post. Never changes the run's exit code.
notify() {
  local status="$1" text="$2" payload cfg hook rc=0 err
  text="notifs-backup $status: $(mask_text "$text")"
  if [ "${#text}" -gt 1900 ]; then text="${text:0:1900}..."; fi
  if [ -z "$WORK" ] || [ ! -d "$WORK" ]; then
    WORK="$(mktemp -d)" || { log "notify: cannot create a work dir; webhook not sent"; return 0; }
    TIMED_LAST_FILE="$WORK/timed-last"
  fi
  payload="$WORK/payload.json"
  cfg="$WORK/curl.cfg"
  printf '{"content":"%s","allowed_mentions":{"parse":[]}}' "$(json_escape "$text")" > "$payload" \
    || { log "notify: cannot write the payload; webhook not sent"; return 0; }
  # The webhook URL is a secret: it goes through a 0600 curl config file, never argv (ps shows argv).
  hook=${DISCORD_WEBHOOK//\\/\\\\}
  hook=${hook//\"/\\\"}
  (umask 077 && printf 'url = "%s"\n' "$hook" > "$cfg") \
    || { log "notify: cannot write the curl config; webhook not sent"; return 0; }
  timed_fixed notify "$NOTIFY_PHASE_SECS" curl -fsS --max-time 30 -o /dev/null -K "$cfg" \
    -H 'Content-Type: application/json' --data-binary "@$payload" || rc=$?
  if [ "$rc" -ne 0 ]; then
    err="$(tr '\n' ' ' < "$TIMED_LAST_FILE" 2>/dev/null || true)"
    log "notify: webhook POST failed rc=$rc err=$(mask_text "$err")"
  fi
  return 0
}

# ---- timed ----

# timed <label> <secs> <cmd...>: runs the command under timeout, logs start/end (rc, duration), logs its masked
# output line by line (also kept in $TIMED_LAST_FILE), TIMED OUT at 124/137. The timeout is clamped to the time left
# before the total deadline; with none left the command is not started and the result is 124.
# timed_fixed is the same without the clamp (notify and unlock keep their own bound after the deadline).
timed() { _timed_run 1 "$@"; }
timed_fixed() { _timed_run 0 "$@"; }

# timed_io <label> <secs> <stdin-file|-> <stdout-file|-> <cmd...>: timed, with the command's stdin and stdout redirected
# on the host side (binary-safe, never through $()). "-" for stdin is /dev/null; "-" for stdout leaves it in the log.
timed_io() {
  local label="$1" secs="$2" in="$3" out="$4"
  shift 4
  # shellcheck disable=SC2016 # the body is a literal for sh -c
  timed "$label" "$secs" sh -c 'i="$1"; o="$2"; shift 2; if [ "$i" = - ]; then i=/dev/null; fi; exec < "$i"; if [ "$o" != - ]; then exec > "$o"; fi; exec "$@"' sh "$in" "$out" "$@"
}

_timed_run() {
  local clamp="$1" label="$2" secs="$3" t0 rc=0 remain line
  shift 3
  : > "$TIMED_LAST_FILE" 2>/dev/null || true
  if [ "$clamp" -eq 1 ] && [ -n "$RUN_START_EPOCH" ]; then
    remain=$((DEADLINE_SECS - ($(now_epoch) - RUN_START_EPOCH)))
    if [ "$remain" -le 0 ]; then
      log "phase=$label start timeout=0s"
      log "phase=$label TIMED OUT (total deadline of ${DEADLINE_SECS}s already reached)"
      log "phase=$label end rc=124 dur=0s"
      return 124
    fi
    if [ "$remain" -lt "$secs" ]; then secs="$remain"; fi
  fi
  t0="$(now_epoch)"
  # Background + wait so a trapped TERM/INT interrupts at once instead of waiting for a long foreground child. fd 9
  # (the lock) is closed in the child so an orphan can never keep the lock held.
  (
    set +e
    exec 9>&-
    timeout -s TERM -k 30 "$secs" "$@" 2>&1 | while IFS= read -r line || [ -n "$line" ]; do
      line="$(mask_text "$line")"
      log "$label: $line"
      printf '%s\n' "$line" >> "$TIMED_LAST_FILE" 2>/dev/null
    done
    exit "${PIPESTATUS[0]}"
  ) &
  TIMED_PID=$!
  # Logged after the fork and after TIMED_PID is set: a TERM seen once this line is out always finds the child.
  log "phase=$label start timeout=${secs}s"
  wait "$TIMED_PID" || rc=$?
  TIMED_PID=""
  if [ "$rc" -eq 124 ] || [ "$rc" -eq 137 ]; then log "phase=$label TIMED OUT after ${secs}s"; fi
  log "phase=$label end rc=$rc dur=$(($(now_epoch) - t0))s"
  return "$rc"
}

# ---- state file ----

# write_state <ok|failed>: last-run.tsv, atomically, in the 0755 status dir.
write_state() {
  local result="$1" end_iso
  end_iso="$(now_iso)"
  # The subshell is the left side of ||, so errexit is off inside it: every step checks itself and fails the subshell.
  (
    local tmp=""
    umask 022
    if [ ! -d "$STATE_DIR" ]; then
      mkdir -p -- "$STATE_DIR" || exit 1
      chmod 0755 -- "$STATE_DIR" || exit 1
    fi
    tmp="$(mktemp "$STATE_DIR/.last-run.XXXXXX")" || exit 1
    if ! { printf '%s\t%s\t%s\t%s\t%s\n' "$RUN_START_ISO" "$end_iso" "$result" "${SNAPSHOT_ID:--}" "${FAIL_PHASE:--}" > "$tmp" \
      && chmod 0644 -- "$tmp" && mv -f -- "$tmp" "$STATE_DIR/last-run.tsv"; }; then
      rm -f -- "$tmp"
      exit 1
    fi
  ) || log "WARN: could not write $STATE_DIR/last-run.tsv"
  return 0
}

# ---- window ----

# load_window: literal read of window.env (START, END, TZ); on any problem the window warnings are disabled, loudly.
load_window() {
  local f line key val seen=" " s="" e="" z=""
  WINDOW_OK=0
  f="$(window_file)"
  if [ ! -f "$f" ]; then log "WARN: window file $f missing; window warnings disabled"; return 0; fi
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in '' | '#'*) continue ;; esac
    case "$line" in *=*) ;; *) log "WARN: window file has a malformed line; window warnings disabled"; return 0 ;; esac
    key="${line%%=*}"
    val="${line#*=}"
    case "$seen" in *" $key "*) log "WARN: window file repeats $key; window warnings disabled"; return 0 ;; esac
    seen="$seen$key "
    case "$key" in
      BACKUP_WINDOW_START) s="$val" ;;
      BACKUP_WINDOW_END) e="$val" ;;
      BACKUP_WINDOW_TZ) z="$val" ;;
      *) log "WARN: window file has an unknown key; window warnings disabled"; return 0 ;;
    esac
  done < "$f"
  if [[ ! "$s" =~ ^([01][0-9]|2[0-3]):[0-5][0-9]$ ]] || [[ ! "$e" =~ ^([01][0-9]|2[0-3]):[0-5][0-9]$ ]] \
    || [[ ! "$z" =~ ^[A-Za-z0-9_/+-]+$ ]] || [[ ! "$s" < "$e" ]]; then
    log "WARN: window file values invalid; window warnings disabled"
    return 0
  fi
  WINDOW_START="$s"
  WINDOW_END="$e"
  WINDOW_TZ="$z"
  WINDOW_OK=1
  return 0
}

window_check_start() {
  local h
  if [ "$RUN_MODE" != "nightly" ] || [ "$WINDOW_OK" -ne 1 ]; then return 0; fi
  h="$(now_hhmm)"
  if [[ "$h" < "$WINDOW_START" ]] || [[ ! "$h" < "$WINDOW_END" ]]; then
    START_IN_WINDOW=0
    WARNINGS+=("started outside backup window")
    log "window: run started at $h, outside [$WINDOW_START,$WINDOW_END) $WINDOW_TZ"
  else
    START_IN_WINDOW=1
  fi
  return 0
}

window_check_end() {
  local h
  if [ "$RUN_MODE" != "nightly" ] || [ "$WINDOW_OK" -ne 1 ] || [ "$START_IN_WINDOW" -ne 1 ]; then return 0; fi
  h="$(now_hhmm)"
  if [[ ! "$h" < "$WINDOW_END" ]]; then
    WARNINGS+=("overran backup window")
    log "window: run ended at $h, after $WINDOW_END $WINDOW_TZ"
  fi
  return 0
}

# ---- exit path ----

die_phase() { # <phase> <exit code> <message>
  FAIL_PHASE="$1"
  FAIL_MSG="$3"
  exit "$2"
}

kill_timed_child() {
  if [ -n "${TIMED_PID:-}" ]; then
    if command -v pkill > /dev/null 2>&1; then pkill -TERM -P "$TIMED_PID" 2> /dev/null || true; fi
    kill -TERM "$TIMED_PID" 2> /dev/null || true
  fi
  return 0
}

on_signal() {
  trap - TERM INT
  log "signal $1 received during phase=${CUR_PHASE:-unknown}"
  kill_timed_child
  FAIL_PHASE="${CUR_PHASE:-unknown}"
  FAIL_MSG="terminated by SIG$1"
  exit 1
}

# on_exit: the single exit path (normal end, die_phase, signal, crash under set -e). Writes the state file (not for a
# lock failure, see below) and posts
# exactly one notify (only once the env phase passed), then cleans up. Exempt from the deadline clamp.
on_exit() {
  local rc=$? result=ok text dur w
  trap - EXIT TERM INT
  set +e # nothing in the exit path may abort it: state and notify are best effort, the exit code is rc
  if [ "$rc" -ne 0 ] || [ -n "$FAIL_PHASE" ]; then
    result=failed
    if [ -z "$FAIL_PHASE" ]; then
      FAIL_PHASE="${CUR_PHASE:-unknown}"
      FAIL_MSG="unexpected exit rc=$rc"
    fi
    if [ "$rc" -eq 0 ]; then rc=1; fi
  fi
  window_check_end
  # Dumps must not sit on disk once the snapshot exists (also after a later forget/check failure); before that the
  # staging dir is kept for diagnosis and wiped by the next run.
  if [ "$BACKUP_DONE" -eq 1 ] && [ -n "$STAGING" ]; then rm -rf -- "$STAGING"; fi
  dur=$(($(now_epoch) - RUN_START_EPOCH))
  log "run end mode=$RUN_MODE result=$result rc=$rc dur=${dur}s${FAIL_PHASE:+ failed_phase=$FAIL_PHASE}${FAIL_MSG:+ msg=$FAIL_MSG}"
  # A lock failure means another run holds the lock and reports its own result: its state file is left untouched.
  if [ "$FAIL_PHASE" = "lock" ]; then
    log "state: last-run.tsv left unchanged (failed at phase lock; the lock holder reports its own result)"
  else
    write_state "$result"
  fi
  # Before the notice, so the notice can carry a heartbeat warning. Never changes rc.
  heartbeat_ping "$result"
  if [ "$NOTIFY_ENABLED" -eq 1 ] && [ -n "$DISCORD_WEBHOOK" ]; then
    if [ "$result" = ok ]; then
      text="$RUN_MODE run ok in ${dur}s, snapshot ${SNAPSHOT_ID:--}, dump ${DUMP_SIZE:--}"
    else
      text="$RUN_MODE run FAILED at phase ${FAIL_PHASE}: ${FAIL_MSG}"
      if [ -n "$FAIL_DETAIL" ]; then text="$text"$'\n'"$FAIL_DETAIL"; fi
    fi
    if [ "${#WARNINGS[@]}" -gt 0 ]; then
      text="$text"$'\n'"warning:"
      for w in "${WARNINGS[@]}"; do text="$text $w;"; done
    fi
    notify "$result" "$text"
  fi
  if [ -n "$WORK" ] && [ -d "$WORK" ]; then rm -rf -- "$WORK"; fi
  exit "$rc"
}

# ---- framework phases ----

# phase_env: read and validate backup.env. Failure: state failed (failed_phase=env), exit 2, NO webhook call.
phase_env() {
  local k missing=""
  CUR_PHASE="env"
  log "phase=env start"
  # Whatever the caller's environment holds (a hand run without sudo) must not reach restic, docker or the S3 SDK.
  unset "${SCRUB_ENV[@]}"
  while IFS= read -r k; do
    case "$k" in RESTIC_* | AWS_* | RCLONE_*) unset "$k" ;; esac
  done < <(compgen -e)
  if ! read_backup_env "$ENV_FILE" "$ENV_OWNER"; then
    log "FATAL: $ENV_READ_ERROR"
    die_phase env 2 "$ENV_READ_ERROR"
  fi
  for k in "${ENV_KEYS[@]}"; do
    if [ -z "${!k}" ]; then missing="${missing:+$missing }$k"; fi
  done
  if [ -n "$missing" ]; then
    log "FATAL: missing required env var(s): $missing"
    die_phase env 2 "missing required env var(s): $missing"
  fi
  export RESTIC_REPOSITORY RESTIC_PASSWORD AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_DEFAULT_REGION
  # The unit runs with neither $HOME nor $XDG_CACHE_HOME, so restic would warn on every call and prune without a cache.
  # A fixed cache inside the root-only work dir (restic creates it 0700); the caller's RESTIC_CACHE_DIR was scrubbed.
  export RESTIC_CACHE_DIR="$BACKUP_ROOT/restic-cache"
  # restic --json status lines are logged one per line: ask for about one per minute.
  export RESTIC_PROGRESS_FPS=0.0166
  NOTIFY_ENABLED=1
  log "phase=env end rc=0"
}

# phase_lock: flock -n on the shared lock (fd 9, held until the process exits).
phase_lock() {
  CUR_PHASE=lock
  log "phase=lock start"
  # LOCK_FILE is assigned by ov_path (printf -v) in load_overrides.
  # shellcheck disable=SC2153
  if ! exec 9> "$LOCK_FILE"; then
    log "FATAL: cannot open lock file $LOCK_FILE"
    die_phase lock 1 "cannot open lock file"
  fi
  if ! flock -n 9; then
    log "state lock-held"
    die_phase lock 1 "another backup or restore holds the lock"
  fi
  log "phase=lock end rc=0"
}

# prepare_staging: wipe and recreate staging/ (root 0700). Called by the first data phase, under the lock.
prepare_staging() {
  if [ "$BACKUP_ROOT" = "/" ]; then log "FATAL: NOTIFS_BACKUP_ROOT must not be /"; return 1; fi
  # Fail closed: a recursive delete only under the production root or a root that carries the sentinel file.
  if [ "$BACKUP_ROOT" != "$PROD_ROOT" ] && { [ ! -f "$BACKUP_ROOT/$ROOT_SENTINEL" ] || [ -L "$BACKUP_ROOT/$ROOT_SENTINEL" ]; }; then
    log "FATAL: refusing to wipe $BACKUP_ROOT/staging: $BACKUP_ROOT is not $PROD_ROOT and has no $ROOT_SENTINEL sentinel file"
    return 1
  fi
  STAGING="$BACKUP_ROOT/staging"
  rm -rf -- "$STAGING" || return 1
  # A previous run killed mid ntfy_auth leaves its scratch copy of auth.db here.
  rm -rf -- "$BACKUP_ROOT"/ntfy-copy.* || return 1
  (umask 077 && mkdir -p -- "$STAGING/postgres" "$STAGING/ntfy") || return 1
  chmod 0700 -- "$BACKUP_ROOT" "$STAGING" "$STAGING/postgres" "$STAGING/ntfy" || return 1
  log "staging: wiped and recreated $STAGING"
}

# ---- data phases ----

# restic_timed <label> <secs> <stdout-file|-> <restic args...>: a restic call through timed_io; on a timeout it runs
# `restic unlock` (120s, fixed, exempt from the deadline) so a killed run leaves no stale repository lock, and keeps the
# timed output of the call that timed out for the failure notice.
restic_timed() {
  local label="$1" secs="$2" outf="$3" rc=0
  shift 3
  timed_io "$label" "$secs" - "$outf" restic "$@" || rc=$?
  if [ "$rc" -eq 124 ] || [ "$rc" -eq 137 ]; then
    cp -- "$TIMED_LAST_FILE" "$TIMED_LAST_FILE.keep" 2> /dev/null || true
    timed_fixed restic_unlock 120 restic unlock || log "restic_unlock failed; a stale repository lock may remain"
    mv -f -- "$TIMED_LAST_FILE.keep" "$TIMED_LAST_FILE" 2> /dev/null || true
  fi
  return "$rc"
}

# count_snapshots <restic snapshots --json file>: number of snapshots listed.
count_snapshots() {
  local n
  n="$(grep -o '"short_id"' "$1" 2> /dev/null | wc -l | tr -d ' ')" || n=0
  printf '%s' "${n:-0}"
}

phase_hb_arm() {
  HEARTBEAT_ENABLED=1
  log "heartbeat: armed (ping is sent from the exit path)"
}

phase_preflight() {
  if ! prepare_staging; then PHASE_MSG="cannot prepare staging"; return 1; fi
  local rc=0
  restic_timed preflight 60 /dev/null cat config || rc=$?
  if [ "$rc" -ne 0 ]; then PHASE_MSG="restic repository not reachable"; return "$rc"; fi
}

# phase_pg_wait: poll the container health until healthy (a deploy may be recreating it); logs each state change.
phase_pg_wait() {
  local start=$SECONDS state last="" raw
  while :; do
    raw="$(timeout 20 docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}no-healthcheck{{end}}' "$PG_CONTAINER" 2>&1)" || true
    state="$(head -n 1 <<< "$raw")"
    if [ -z "$state" ]; then state="no-answer"; fi
    if [ "$state" != "$last" ]; then log "pg_wait: $PG_CONTAINER state=$(mask_text "$state")"; last="$state"; fi
    if [ "$state" = "healthy" ]; then return 0; fi
    if [ $((SECONDS - start)) -ge "$PG_WAIT_SECS" ]; then
      PHASE_MSG="$PG_CONTAINER not healthy after ${PG_WAIT_SECS}s (last state: ${state:0:80})"
      return 1
    fi
    sleep "$PG_POLL_SECS"
  done
}

phase_pg_dump() {
  local dump="$STAGING/postgres/delivery.dump" rc=0 size
  timed_io pg_dump 600 - "$dump" docker exec "$PG_CONTAINER" pg_dump -U delivery -Fc -d delivery || rc=$?
  if [ "$rc" -ne 0 ]; then PHASE_MSG="pg_dump failed"; return "$rc"; fi
  if [ ! -s "$dump" ]; then PHASE_MSG="pg_dump produced an empty dump"; return 1; fi
  size="$(stat -c %s -- "$dump")"
  DUMP_SIZE="${size} bytes"
  log "pg_dump: dump is $DUMP_SIZE"
}

# phase_pg_verify: the dump must be readable by pg_restore and list the core tables. (pg_restore --list prints
# "TABLE public destinations <owner>", with a space where the schema-qualified name has a dot.)
phase_pg_verify() {
  local dump="$STAGING/postgres/delivery.dump" toc="$WORK/toc.txt" rc=0 t
  timed_io pg_verify 600 "$dump" "$toc" docker exec -i "$PG_CONTAINER" pg_restore --list || rc=$?
  if [ "$rc" -ne 0 ]; then PHASE_MSG="pg_restore --list failed"; return "$rc"; fi
  for t in destinations messages; do
    if ! grep -Eq "[[:space:]]TABLE public ${t}[[:space:]]" "$toc"; then
      PHASE_MSG="dump TOC lacks TABLE public.$t"
      return 1
    fi
  done
}

# phase_ntfy_auth: consistent online copy of auth.db with sqlite3 from the pinned image (no network, as ntfy's uid).
# --user matters: a root-run sqlite would leave a root-owned -shm/-wal that ntfy (uid 1000) could not open. The copy
# goes to a fresh dir owned by that uid, is integrity-checked there, then copied into staging as root 0600.
phase_ntfy_auth() {
  local out rc=0 res
  if ! out="$(mktemp -d "$BACKUP_ROOT/ntfy-copy.XXXXXX")"; then PHASE_MSG="cannot create the ntfy copy dir"; return 1; fi
  if ! chown "$NTFY_UID_GID" -- "$out"; then
    if [ "${NOTIFS_BACKUP_TESTING:-}" = "1" ]; then log "ntfy_auth: chown failed (tolerated under NOTIFS_BACKUP_TESTING=1)"
    else rm -rf -- "$out"; PHASE_MSG="cannot chown the ntfy copy dir"; return 1; fi
  fi
  timed ntfy_copy 120 docker run --rm --network none --user "$NTFY_UID_GID" -v "$NTFY_VOLUME:/src" -v "$out:/out" \
    "$SQLITE_IMAGE" sqlite3 'file:/src/auth.db?mode=ro' '.backup /out/auth.db' || rc=$?
  if [ "$rc" -eq 0 ] && [ ! -s "$out/auth.db" ]; then PHASE_MSG="sqlite .backup produced no auth.db"; rc=1; fi
  if [ "$rc" -eq 0 ]; then
    timed ntfy_integrity 60 docker run --rm --network none --user "$NTFY_UID_GID" -v "$out:/out" \
      "$SQLITE_IMAGE" sqlite3 'file:/out/auth.db?mode=ro' 'PRAGMA integrity_check' || rc=$?
    res="$(tr -d '\r\n' < "$TIMED_LAST_FILE" 2> /dev/null || true)"
    if [ "$rc" -eq 0 ] && [ "$res" != "ok" ]; then PHASE_MSG="auth.db integrity_check: ${res:0:80}"; rc=1; fi
  fi
  if [ "$rc" -eq 0 ]; then
    if cp -- "$out/auth.db" "$STAGING/ntfy/auth.db" && chmod 0600 -- "$STAGING/ntfy/auth.db"; then :
    else PHASE_MSG="cannot stage auth.db"; rc=1; fi
  elif [ -z "$PHASE_MSG" ]; then
    PHASE_MSG="ntfy auth.db copy failed"
  fi
  rm -rf -- "$out"
  return "$rc"
}

# phase_restic_backup: nothing from secrets-persistent/ is ever staged; only $STAGING is uploaded. From here on the
# staging dir is removed on every exit (BACKUP_DONE). restic exit 3 (some files unreadable) fails the run although a
# snapshot exists.
phase_restic_backup() {
  local rc=0 summary
  restic_timed restic_list_pre 120 "$WORK/snaps-pre.json" snapshots --json --host "$RESTIC_HOST" --tag "$RESTIC_TAG" || rc=$?
  if [ "$rc" -ne 0 ]; then PHASE_MSG="cannot list snapshots before the backup"; return "$rc"; fi
  PRE_COUNT="$(count_snapshots "$WORK/snaps-pre.json")"
  log "restic_backup: $PRE_COUNT snapshot(s) before this run"
  BACKUP_DONE=1
  restic_timed restic_backup 3600 - backup --json --host "$RESTIC_HOST" --tag "$RESTIC_TAG" --tag "$RUN_MODE" "$STAGING" || rc=$?
  summary="$(grep -F '"message_type":"summary"' "$TIMED_LAST_FILE" 2> /dev/null | tail -n 1 || true)"
  if [[ "$summary" =~ \"snapshot_id\":\"([0-9a-f]{8,64})\" ]]; then SNAPSHOT_ID="${BASH_REMATCH[1]}"; fi
  if [ "$rc" -eq 3 ]; then PHASE_MSG="restic backup exit 3 (some files unreadable${SNAPSHOT_ID:+, snapshot $SNAPSHOT_ID exists})"; return 3; fi
  if [ "$rc" -ne 0 ]; then PHASE_MSG="restic backup failed"; return "$rc"; fi
  if [ -z "$SNAPSHOT_ID" ]; then PHASE_MSG="restic backup printed no snapshot id"; return 1; fi
  log "restic_backup: snapshot $SNAPSHOT_ID"
}

# phase_restic_forget: the forget guard. Forget (which prunes) runs only when the new snapshot id is in the listing and
# the count grew by at least one; otherwise it is skipped and the run fails.
phase_restic_forget() {
  local rc=0 post
  if [ -z "$SNAPSHOT_ID" ]; then PHASE_MSG="forget guard: no snapshot id, forget skipped"; return 1; fi
  restic_timed restic_list_post 120 "$WORK/snaps-post.json" snapshots --json --host "$RESTIC_HOST" --tag "$RESTIC_TAG" || rc=$?
  if [ "$rc" -ne 0 ]; then PHASE_MSG="forget guard: cannot list snapshots, forget skipped"; return "$rc"; fi
  post="$(count_snapshots "$WORK/snaps-post.json")"
  if ! grep -qF "\"id\":\"$SNAPSHOT_ID" "$WORK/snaps-post.json" || [ "$post" -lt $((PRE_COUNT + 1)) ]; then
    log "forget guard: snapshot $SNAPSHOT_ID not confirmed (before=$PRE_COUNT after=$post); forget skipped"
    PHASE_MSG="forget guard: new snapshot not confirmed in the listing, forget skipped"
    return 1
  fi
  restic_timed restic_forget 1800 - forget --prune --host "$RESTIC_HOST" --tag "$RESTIC_TAG" --group-by host \
    --keep-daily 7 --keep-weekly 4 --keep-monthly 6 || rc=$?
  if [ "$rc" -ne 0 ]; then PHASE_MSG="restic forget failed"; return "$rc"; fi
}

phase_restic_check() {
  local rc=0
  restic_timed restic_check 600 - check || rc=$?
  if [ "$rc" -ne 0 ]; then PHASE_MSG="restic check failed"; return "$rc"; fi
}

# phase_volumes: warn-only. Any prefixed volume other than the backed-up and the intentionally skipped ones is named.
phase_volumes() {
  local list="$WORK/volumes.txt" v extra=""
  if ! timed_io volumes 30 - "$list" docker volume ls --format '{{.Name}}'; then
    WARNINGS+=("volume check failed (docker volume ls)")
    return 0
  fi
  while IFS= read -r v; do
    case "$v" in
      "$PG_VOLUME" | "$NTFY_VOLUME" | "$SKIPPED_VOLUME") ;;
      "$VOLUME_PREFIX"*) extra="${extra:+$extra, }$v" ;;
      *) ;;
    esac
  done < "$list"
  if [ -n "$extra" ]; then
    WARNINGS+=("not backed up: $extra")
    log "volumes: not backed up: $extra"
  fi
  return 0
}

# heartbeat_ping <ok|failed>: from on_exit, before the notice. ok -> POST HB_URL, failed -> POST HB_URL/fail. Skipped when
# not armed, when the env phase did not pass, and at phase lock (another run holds the lock and reports its own
# result). The key file content is read byte-exact (a trailing newline makes it malformed, matching write-key.sh's bare
# output) and reaches curl only through a 0600 -K config (the Authorization header), never argv. Any problem is a warning; rc is never touched.
heartbeat_ping() {
  local result="$1" url cfg key code rc=0 masked_code
  if [ "$HEARTBEAT_ENABLED" -ne 1 ] || [ "$NOTIFY_ENABLED" -ne 1 ]; then return 0; fi
  if [ "$FAIL_PHASE" = "lock" ] || [ "$FAIL_PHASE" = "env" ]; then
    log "heartbeat: not sent (failed at phase $FAIL_PHASE)"
    return 0
  fi
  if [ -z "$WORK" ] || [ ! -d "$WORK" ]; then log "heartbeat: no work dir; not sent"; return 0; fi
  if [ -L "$HB_KEY_FILE" ] || [ ! -f "$HB_KEY_FILE" ] || [ ! -r "$HB_KEY_FILE" ]; then
    log "heartbeat: key file missing or unreadable"
    WARNINGS+=("heartbeat: key file missing or unreadable, no ping sent")
    return 0
  fi
  key="$(cat -- "$HB_KEY_FILE"; printf x)"
  key="${key%x}"
  if [[ ! "$key" =~ ^dak_[A-Za-z0-9_-]{43}$ ]] || [ "${#key}" -ne 47 ]; then
    log "heartbeat: key file malformed (want exactly dak_ plus 43 characters, no newline)"
    WARNINGS+=("heartbeat: key file malformed, no ping sent")
    return 0
  fi
  url="$HB_URL"
  if [ "$result" != "ok" ]; then url="$HB_URL/fail"; fi
  cfg="$WORK/hb.cfg"
  (umask 077 && printf 'header = "Authorization: Bearer %s"\n' "$key" > "$cfg") \
    || { log "heartbeat: cannot write the curl config; not sent"; WARNINGS+=("heartbeat: not sent"); return 0; }
  timed_fixed heartbeat 20 curl -sS --max-time 15 -X POST -o /dev/null -w '%{http_code}' -K "$cfg" "$url" || rc=$?
  rm -f -- "$cfg"
  code="$(tr -d '\r\n' < "$TIMED_LAST_FILE" 2> /dev/null || true)"
  if [ "$rc" -ne 0 ] || [ "$code" != "200" ]; then
    masked_code="$(mask_text "$code")"
    log "heartbeat: POST ${url##*/} failed rc=$rc http=${masked_code:0:60}"
    WARNINGS+=("heartbeat: POST failed (rc=$rc http=${masked_code:0:20})")
    return 0
  fi
  log "heartbeat: POST ${url##*/} -> 200"
  return 0
}

run_phases() {
  local p rc label detail
  for p in "${PHASES[@]}"; do
    label="$p"
    if [[ "$p" == phase_* ]]; then label="${p#phase_}"; fi
    if [ $(($(now_epoch) - RUN_START_EPOCH)) -ge "$DEADLINE_SECS" ]; then
      log "phase=$label not started: total deadline of ${DEADLINE_SECS}s reached"
      die_phase "$label" 1 "deadline exceeded"
    fi
    CUR_PHASE="$label"
    PHASE_MSG=""
    rc=0
    "$p" || rc=$?
    if [ "$rc" -ne 0 ]; then
      detail="$(tail -n 3 "$TIMED_LAST_FILE" 2>/dev/null || true)"
      detail="$(mask_text "$detail")"
      FAIL_DETAIL="${detail:0:500}"
      if [ "$rc" -eq 124 ] || [ "$rc" -eq 137 ]; then die_phase "$CUR_PHASE" 1 "TIMED OUT rc=$rc"; fi
      die_phase "$CUR_PHASE" 1 "${PHASE_MSG:-phase failed} rc=$rc"
    fi
  done
}

usage() {
  echo "usage: backup.sh nightly|manual" >&2
}

main() {
  local mode="${1:-}"
  case "$mode" in
    nightly | manual) ;;
    *) usage; exit 2 ;;
  esac
  load_overrides
  umask 077 # dumps, staged files and the work dir are root-only
  RUN_MODE="$mode"
  RUN_START_EPOCH="$(now_epoch)"
  RUN_START_ISO="$(now_iso)"
  trap on_exit EXIT
  trap 'on_signal TERM' TERM
  trap 'on_signal INT' INT
  WORK="$(mktemp -d)"
  TIMED_LAST_FILE="$WORK/timed-last"
  init_logging
  prune_logs
  log "run start mode=$mode deadline=${DEADLINE_SECS}s"
  load_window
  window_check_start
  phase_env
  phase_lock
  run_phases
  exit 0
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  main "$@"
fi
