#!/usr/bin/env bash
# notifs-backup restore (root, run by hand). Usage:
#   restore.sh list
#   restore.sh db --into <name> [--snapshot <id>]
#   restore.sh ntfy-auth --out <dir> [--snapshot <id>]
#
# Ported from apps/platform/backup/restore.sh (GPropersi/apps): the literal env reader, the shared lock and the
# guarded, never-in-place restore modes. Nothing here can write the live data: `db` restores into a NEW database whose
# name is never `delivery`, and `ntfy-auth` restores to a NEW directory outside every docker volume and never mounts the
# live ntfy-auth volume at all.
#
# Exit codes: 0 ok, 1 a step failed, 2 refusal (usage, bad name, existing target, env problem, lock held).
#
# The Postgres container, sqlite image and restic host/tag are runtime constants defined in backup.sh only; they are
# parsed from the backup.sh beside this script (read_const), never redefined here.
#
# Test overrides (honoured ONLY when NOTIFS_BACKUP_TESTING=1, otherwise every NOTIFS_BACKUP_* / INSTALL_BACKUP_* variable
# is unset with a warning):
#   NOTIFS_BACKUP_ENV_FILE / NOTIFS_BACKUP_ENV_OWNER / NOTIFS_BACKUP_ROOT / NOTIFS_BACKUP_LOCK
#
# SC2154 (file-wide): PG_CONTAINER, SQLITE_IMAGE, RESTIC_HOST, RESTIC_TAG and NTFY_VOLUME are assigned by read_const.
# shellcheck disable=SC2154
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

SCRUB_ENV=(RESTIC_PASSWORD_FILE RESTIC_PASSWORD_COMMAND RESTIC_REPOSITORY_FILE RESTIC_FROM_REPOSITORY
  RESTIC_FROM_PASSWORD_FILE RESTIC_FROM_PASSWORD_COMMAND RESTIC_FROM_REPOSITORY_FILE RESTIC_KEY_HINT RESTIC_CACHE_DIR
  RESTIC_COMPRESSION RESTIC_PACK_SIZE RESTIC_READ_CONCURRENCY AWS_SESSION_TOKEN AWS_PROFILE AWS_SHARED_CREDENTIALS_FILE
  AWS_CONFIG_FILE AWS_ROLE_ARN AWS_WEB_IDENTITY_TOKEN_FILE AWS_REGION AWS_ENDPOINT_URL AWS_ENDPOINT_URL_S3
  DOCKER_HOST DOCKER_CONTEXT DOCKER_CONFIG)

PROD_ROOT=/var/lib/notifs-backup/work
ENV_USER=site-ops
PROD_ENV_FILE=/home/4irl-notifs/secrets-persistent/backup.env
LIVE_DB=delivery
ENV_READ_ERROR=""
SCRATCH=""
LAST_FILE=""
# PG_CONTAINER, SQLITE_IMAGE, RESTIC_HOST, RESTIC_TAG and NTFY_VOLUME are assigned by read_const (printf -v) from
# backup.sh in main before any use; they are deliberately not initialised here so backup.sh stays their only definition
# (the file-wide SC2154 directive near the top covers their uses).

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

# ---- logging ----

now_epoch() { date +%s; }
now_iso() { date -u +%Y-%m-%dT%H:%M:%SZ; }

mask_text() {
  local s="$1" v
  for v in "$RESTIC_REPOSITORY" "$RESTIC_PASSWORD" "$AWS_ACCESS_KEY_ID" "$AWS_SECRET_ACCESS_KEY" "$DISCORD_WEBHOOK"; do
    if [ -n "$v" ]; then s=${s//"$v"/***}; fi
  done
  printf '%s' "$s"
}

log() { printf '%s %s\n' "$(now_iso)" "$(mask_text "$*")"; }

# refuse <message>: exit 2, nothing has been changed.
refuse() {
  log "REFUSED: $*"
  exit 2
}

# fail_step <message>: exit 1, a step failed after the refusal checks passed.
fail_step() {
  log "FAILED: $*"
  exit 1
}

cleanup() {
  if [ -n "$SCRATCH" ] && [ -d "$SCRATCH" ]; then rm -rf -- "$SCRATCH"; fi
  return 0
}

# ---- overrides and constants ----

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

ov_path() {
  local name="$2" v
  if [ -n "${!name+x}" ]; then v="${!name}"; else v="$3"; fi
  if [[ "$v" != /* ]] || [[ "$v" == *[[:space:]]* ]]; then
    echo "FATAL: $name must be an absolute path without whitespace" >&2
    exit 2
  fi
  printf -v "$1" '%s' "$v"
}

load_overrides() {
  gate_test_overrides
  ov_path ENV_FILE NOTIFS_BACKUP_ENV_FILE "$PROD_ENV_FILE"
  ov_path BACKUP_ROOT NOTIFS_BACKUP_ROOT "$PROD_ROOT"
  ov_path LOCK_FILE NOTIFS_BACKUP_LOCK /run/notifs-backup.lock
  # The prod deploy writes backup.env as the site-ops user; default owner is that user's uid:gid. Fail closed: an
  # unresolvable user exits 2 rather than accepting any other owner.
  if [ -n "${NOTIFS_BACKUP_ENV_OWNER+x}" ]; then
    if [[ ! "$NOTIFS_BACKUP_ENV_OWNER" =~ ^[0-9]+:[0-9]+$ ]]; then
      echo "FATAL: NOTIFS_BACKUP_ENV_OWNER must match uid:gid (digits)" >&2
      exit 2
    fi
    ENV_OWNER="$NOTIFS_BACKUP_ENV_OWNER"
  else
    local u g
    if ! u="$(id -u -- "$ENV_USER" 2> /dev/null)" || ! g="$(id -g -- "$ENV_USER" 2> /dev/null)"; then
      echo "FATAL: cannot resolve the uid:gid of user $ENV_USER (the owner backup.env must have)" >&2
      exit 2
    fi
    ENV_OWNER="$u:$g"
  fi
}

# read_const <NAME>: the literal value of NAME=... from the backup.sh beside this script (single quotes stripped).
# Fails closed: a missing, duplicated or odd-looking definition exits 2.
read_const() {
  local name="$1" line val n
  n="$(grep -c "^${name}=" "$SCRIPT_DIR/backup.sh" 2> /dev/null || true)"
  if [ "$n" != "1" ]; then refuse "cannot read $name from $SCRIPT_DIR/backup.sh (found $n definitions)"; fi
  line="$(grep -m1 "^${name}=" "$SCRIPT_DIR/backup.sh")"
  val="${line#*=}"
  case "$val" in "'"*"'") val="${val#\'}"; val="${val%\'}" ;; *) ;; esac
  if [[ ! "$val" =~ ^[A-Za-z0-9][A-Za-z0-9_.:@/-]*$ ]]; then refuse "$name in backup.sh is not a plain word"; fi
  printf -v "$name" '%s' "$val"
}

# run_step <label> <secs> <stdin-file|-> <stdout-file|-> <cmd...>: timeout-bounded call that logs start and end (rc,
# duration) and its masked stderr/stdout lines; stdout goes to the file when one is given (binary-safe).
run_step() {
  local label="$1" secs="$2" in="$3" outf="$4" t0 rc=0 errf line
  shift 4
  errf="$SCRATCH/step-err"
  t0="$(now_epoch)"
  log "phase=$label start timeout=${secs}s"
  if [ "$in" = "-" ]; then in=/dev/null; fi
  if [ "$outf" = "-" ]; then
    LAST_FILE="$SCRATCH/step-out"
    timeout -s TERM -k 30 "$secs" "$@" < "$in" > "$LAST_FILE" 2> "$errf" || rc=$?
  else
    LAST_FILE="$outf"
    timeout -s TERM -k 30 "$secs" "$@" < "$in" > "$outf" 2> "$errf" || rc=$?
  fi
  if [ "$outf" = "-" ]; then
    while IFS= read -r line || [ -n "$line" ]; do log "$label: $line"; done < "$LAST_FILE"
  fi
  while IFS= read -r line || [ -n "$line" ]; do log "$label: $line"; done < "$errf"
  if [ "$rc" -eq 124 ] || [ "$rc" -eq 137 ]; then log "phase=$label TIMED OUT after ${secs}s"; fi
  log "phase=$label end rc=$rc dur=$(($(now_epoch) - t0))s"
  return "$rc"
}

usage() {
  cat >&2 << 'EOF'
usage: restore.sh list
       restore.sh db --into <name> [--snapshot <id>]
       restore.sh ntfy-auth --out <dir> [--snapshot <id>]
EOF
  exit 2
}

# ---- argument parsing and validation (no I/O beyond reading backup.sh) ----

MODE=""
INTO=""
OUT=""
SNAPSHOT="latest"

parse_args() {
  MODE="${1:-}"
  if [ "$#" -gt 0 ]; then shift; fi
  case "$MODE" in list | db | ntfy-auth) ;; *) usage ;; esac
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --into) [ "$MODE" = db ] && [ "$#" -ge 2 ] || usage; INTO="$2"; shift 2 ;;
      --out) [ "$MODE" = ntfy-auth ] && [ "$#" -ge 2 ] || usage; OUT="$2"; shift 2 ;;
      --snapshot) [ "$MODE" != list ] && [ "$#" -ge 2 ] || usage; SNAPSHOT="$2"; shift 2 ;;
      *) usage ;;
    esac
  done
  if [ "$MODE" = db ] && [ -z "$INTO" ]; then usage; fi
  if [ "$MODE" = ntfy-auth ] && [ -z "$OUT" ]; then usage; fi
  if [[ ! "$SNAPSHOT" =~ ^(latest|[0-9a-f]{8,64})$ ]]; then refuse "--snapshot must be 'latest' or a hex snapshot id"; fi
  if [ "$MODE" = db ]; then
    if [[ ! "$INTO" =~ ^[a-z][a-z0-9_]{1,30}$ ]]; then refuse "--into name must match ^[a-z][a-z0-9_]{1,30}\$"; fi
    if [ "$INTO" = "$LIVE_DB" ]; then refuse "--into must not be the live database ($LIVE_DB)"; fi
  fi
  return 0
}

# check_out_dir: --out must be an absolute, not-yet-existing path outside every docker volume.
check_out_dir() {
  local norm mp
  if [[ "$OUT" != /* ]] || [[ "$OUT" == *[[:space:]]* ]]; then refuse "--out must be an absolute path without whitespace"; fi
  if [[ ! "$OUT" =~ ^/[A-Za-z0-9_./-]+$ ]]; then refuse "--out may only contain letters, digits, and _ . / - (':' and ',' would alter the docker volume spec)"; fi
  if [ -e "$OUT" ] || [ -L "$OUT" ]; then refuse "--out $OUT already exists"; fi
  norm="$(realpath -m -- "$OUT")"
  case "$norm/" in
    /var/lib/docker/*) refuse "--out $norm is inside the docker data root (volumes live there)" ;;
    *) ;;
  esac
  mp="$(timeout 20 docker volume inspect -f '{{.Mountpoint}}' "$NTFY_VOLUME" 2> /dev/null || true)"
  mp="$(head -n 1 <<< "$mp")"
  if [[ "$mp" == /* ]]; then
    case "$norm/" in
      "$mp"/*) refuse "--out $norm is inside the docker volume $NTFY_VOLUME ($mp)" ;;
      *) ;;
    esac
  fi
  if [ ! -d "$(dirname -- "$norm")" ]; then refuse "parent directory of --out does not exist"; fi
}

# ---- restic fetch ----

# fetch_file <include-glob> <label>: restores the one matching file from the snapshot into $SCRATCH/tree and sets
# FETCHED to its path. The snapshot holds the staging dir at its absolute path, so the match is on the tail.
FETCHED=""
fetch_file() {
  local glob="$1" label="$2" found
  rm -rf -- "${SCRATCH:?}/tree"
  run_step "restic_restore_$label" 1800 - - restic restore "$SNAPSHOT" --host "$RESTIC_HOST" --tag "$RESTIC_TAG" \
    --target "$SCRATCH/tree" --include "$glob" || fail_step "restic restore of $label failed"
  found="$(find "$SCRATCH/tree" -type f -path "$glob" 2> /dev/null | head -n 1 || true)"
  if [ -z "$found" ] || [ ! -s "$found" ]; then fail_step "snapshot $SNAPSHOT holds no $label"; fi
  FETCHED="$found"
}

# ---- modes ----

mode_list() {
  run_step list 120 - - restic snapshots --host "$RESTIC_HOST" --tag "$RESTIC_TAG" || fail_step "restic snapshots failed"
  log "list: ok"
}

mode_db() {
  local dump exists rc=0 counts
  # Existence and creation connect to the maintenance database (positional template1), never to $LIVE_DB; every -d below
  # is the new database.
  run_step db_exists 60 - - docker exec "$PG_CONTAINER" psql -U "$LIVE_DB" -X -Atc \
    "SELECT 1 FROM pg_database WHERE datname='$INTO'" template1 || fail_step "cannot query the database list"
  exists="$(tr -d '\r\n ' < "$LAST_FILE")"
  if [ -n "$exists" ]; then refuse "database $INTO already exists; pick another name or drop it yourself"; fi
  fetch_file '*/postgres/delivery.dump' dump
  dump="$FETCHED"
  run_step db_create 60 - - docker exec "$PG_CONTAINER" psql -U "$LIVE_DB" -X -v ON_ERROR_STOP=1 -c "CREATE DATABASE $INTO" template1 \
    || fail_step "CREATE DATABASE $INTO failed"
  run_step db_restore 1800 "$dump" - docker exec -i "$PG_CONTAINER" pg_restore -U "$LIVE_DB" --no-owner --role="$LIVE_DB" \
    --exit-on-error -d "$INTO" || rc=$?
  if [ "$rc" -ne 0 ]; then
    log "FAILED: pg_restore into $INTO failed (rc=$rc). The database $INTO is half-restored: drop it before retrying:"
    log "  docker exec $PG_CONTAINER psql -U $LIVE_DB -c 'DROP DATABASE $INTO' template1"
    exit 1
  fi
  run_step db_counts 60 - - docker exec "$PG_CONTAINER" psql -U "$LIVE_DB" -X -At -d "$INTO" -c \
    "SELECT 'destinations', count(*) FROM destinations UNION ALL SELECT 'routes', count(*) FROM routes UNION ALL SELECT 'heartbeat_checks', count(*) FROM heartbeat_checks UNION ALL SELECT 'messages', count(*) FROM messages" \
    || fail_step "restored into $INTO but the row-count query failed; drop $INTO before retrying"
  counts="$(tr '\n' ' ' < "$LAST_FILE")"
  log "db: restored snapshot $SNAPSHOT into database $INTO; row counts (name|count): $counts"
}

mode_ntfy_auth() {
  local src res rc=0
  fetch_file '*/ntfy/auth.db' auth
  src="$FETCHED"
  (umask 077 && mkdir -- "$OUT") || fail_step "cannot create $OUT"
  cp -- "$src" "$OUT/auth.db" || fail_step "cannot copy auth.db into $OUT"
  chmod 0600 -- "$OUT/auth.db"
  # The restored copy is checked from a read-only mount of the NEW directory; the live ntfy-auth volume is never mounted.
  # --user 0:0: the image's default user is not root, and the directory and file are root-only (umask 077), so the
  # default user could not open them (found by the live round trip, test_backup_live.sh).
  run_step auth_integrity 60 - - docker run --rm --network none --user 0:0 -v "$OUT:/restored:ro" "$SQLITE_IMAGE" sqlite3 \
    'file:/restored/auth.db?mode=ro&immutable=1' 'PRAGMA integrity_check' || rc=$?
  res="$(tr -d '\r\n' < "$LAST_FILE" 2> /dev/null || true)"
  if [ "$rc" -ne 0 ] || [ "$res" != "ok" ]; then
    rm -rf -- "$OUT"
    fail_step "restored auth.db failed integrity_check (${res:0:80}); $OUT was removed"
  fi
  log "ntfy-auth: restored snapshot $SNAPSHOT to $OUT/auth.db (integrity_check ok). It is a copy: swap it in yourself (see RUNBOOK)."
}

main() {
  local k
  parse_args "$@"
  load_overrides
  umask 077
  for k in PG_CONTAINER SQLITE_IMAGE RESTIC_HOST RESTIC_TAG NTFY_VOLUME; do read_const "$k"; done
  unset "${SCRUB_ENV[@]}"
  while IFS= read -r k; do
    case "$k" in RESTIC_* | AWS_* | RCLONE_*) unset "$k" ;; esac
  done < <(compgen -e)
  if ! read_backup_env "$ENV_FILE" "$ENV_OWNER"; then refuse "$ENV_READ_ERROR"; fi
  for k in "${ENV_KEYS[@]}"; do
    if [ -z "${!k}" ]; then refuse "missing required env var $k"; fi
  done
  export RESTIC_REPOSITORY RESTIC_PASSWORD AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_DEFAULT_REGION
  if ! exec 9> "$LOCK_FILE"; then refuse "cannot open lock file $LOCK_FILE"; fi
  if ! flock -n 9; then refuse "another backup or restore holds the lock"; fi
  mkdir -p -- "$BACKUP_ROOT" || refuse "cannot create $BACKUP_ROOT"
  SCRATCH="$(mktemp -d "$BACKUP_ROOT/restore.XXXXXX")" || fail_step "cannot create a scratch dir under $BACKUP_ROOT"
  trap cleanup EXIT
  export RESTIC_CACHE_DIR="$BACKUP_ROOT/restic-cache"
  log "restore start mode=$MODE snapshot=$SNAPSHOT"
  case "$MODE" in
    list) mode_list ;;
    db) mode_db ;;
    ntfy-auth) check_out_dir; mode_ntfy_auth ;;
    *) usage ;;
  esac
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
  main "$@"
fi
