#!/usr/bin/env bash
# notifs-backup installer (run as root on the VPS by `make host-backup`). Usage:
#   install-backup.sh <install|status|verify|run-now|init-repo> [--source DIR]
#
# Ported from apps/platform/host/install-backup.sh (GPropersi/apps), reduced: this repo has a checkout, so the make target
# copies ops/backup to the host and passes it as --source (no embedded payloads). install and verify need --source.
#
# Single definitions (never redefined here): the restic version and archive hashes come from DIR/pins.env via read_pins;
# SRC_SQLITE_IMAGE, the Postgres container and the ntfy-auth volume are parsed from DIR/backup.sh.
# This script never creates or modifies backup.env (/home/4irl-notifs/secrets-persistent/backup.env, written by the prod
# deploy from GitHub secrets as the 4irl-notifs user); init-repo reads it.
#
# Exit codes: 0 ok, 1 failed (or a verify FAIL), 2 usage.
#
# Test overrides, honoured ONLY when NOTIFS_BACKUP_TESTING=1 (otherwise every NOTIFS_BACKUP_* / INSTALL_BACKUP_* variable
# is unset with a warning, fail closed):
#   INSTALL_BACKUP_ROOT    prefix for every host path (default empty = the real filesystem)
#   INSTALL_BACKUP_OWNER   numeric uid:gid files must have for verify (default 0:0)
#   INSTALL_BACKUP_ENV_OWNER   numeric uid:gid backup.env must have (default: the 4irl-notifs user's; unresolvable exits 2)
#   INSTALL_BACKUP_TOOLS   space-separated tool list for the precondition check
set -euo pipefail

ENV_KEYS=(RESTIC_REPOSITORY RESTIC_PASSWORD AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_DEFAULT_REGION DISCORD_WEBHOOK)
# Set by read_backup_env and read back through ${!k}, so shellcheck sees no direct use.
# shellcheck disable=SC2034
RESTIC_REPOSITORY=""
# shellcheck disable=SC2034
RESTIC_PASSWORD=""
# shellcheck disable=SC2034
AWS_ACCESS_KEY_ID=""
# shellcheck disable=SC2034
AWS_SECRET_ACCESS_KEY=""
# shellcheck disable=SC2034
AWS_DEFAULT_REGION=""
# shellcheck disable=SC2034
DISCORD_WEBHOOK=""
ENV_READ_ERROR=""

TIMER_UNIT=notifs-backup.timer
SERVICE_UNIT=notifs-backup.service
SCRIPT_PAYLOAD=(backup.sh restore.sh)
DATA_PAYLOAD=(window.env)
UNIT_PAYLOAD=("$SERVICE_UNIT" "$TIMER_UNIT")
# The unit's properties, passed unchanged to systemd-run by run-now (a static test compares each with the service file).
RUN_PROPS=(Type=oneshot User=root TimeoutStartSec=4500 Nice=10 IOSchedulingClass=idle MemoryHigh=256M MemoryMax=384M
  "After=network-online.target time-sync.target docker.service" Requires=docker.service Wants=network-online.target)
HB_KEY_RE='^dak_[A-Za-z0-9_-]{43}$'
MAX_AGE_SECS=129600

die() { echo "install-backup: $*" >&2; exit 1; }
say() { printf '%s\n' "$*"; }
usage() {
  echo "usage: install-backup.sh <install|status|verify|run-now|init-repo> [--source DIR]" >&2
  exit 2
}

# gate_overrides: fail closed. See the header.
gate_overrides() {
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

set_paths() {
  ROOT="${INSTALL_BACKUP_ROOT:-}"
  if [ -n "$ROOT" ] && [[ ! "$ROOT" =~ ^/[^[:space:]]*[^/[:space:]]$ ]]; then
    echo "install-backup: INSTALL_BACKUP_ROOT must be an absolute path without whitespace or a trailing slash" >&2
    exit 2
  fi
  OWNER="${INSTALL_BACKUP_OWNER:-0:0}"
  if [[ ! "$OWNER" =~ ^[0-9]+:[0-9]+$ ]]; then echo "install-backup: INSTALL_BACKUP_OWNER must be uid:gid" >&2; exit 2; fi
  # backup.env is written by the prod deploy as the 4irl-notifs user, so it must belong to that user (not OWNER, which
  # is root for the files this installer lays down). Fail closed when the user cannot be resolved.
  if [ -n "${INSTALL_BACKUP_ENV_OWNER+x}" ]; then
    ENV_OWNER="$INSTALL_BACKUP_ENV_OWNER"
  elif u="$(id -u -- 4irl-notifs 2> /dev/null)" && g="$(id -g -- 4irl-notifs 2> /dev/null)"; then
    ENV_OWNER="$u:$g"
  else
    ENV_OWNER=""
  fi
  if [[ ! "$ENV_OWNER" =~ ^[0-9]+:[0-9]+$ ]]; then
    echo "install-backup: cannot resolve the uid:gid of user 4irl-notifs (or INSTALL_BACKUP_ENV_OWNER is not uid:gid)" >&2
    exit 2
  fi
  TOOLS="${INSTALL_BACKUP_TOOLS:-docker curl flock timeout bunzip2 sha256sum systemctl}"
  OPT_DIR="$ROOT/opt/notifs-backup"
  BIN="$ROOT/usr/local/bin/restic"
  SHAFILE="$OPT_DIR/restic.sha256"
  ETC_DIR="$ROOT/etc/notifs-backup"
  ENV_FILE="$ROOT/home/4irl-notifs/secrets-persistent/backup.env"
  LIB_DIR="$ROOT/var/lib/notifs-backup"
  WORK_DIR="$LIB_DIR/work"
  STATUS_DIR="$LIB_DIR/status"
  LAST_RUN="$STATUS_DIR/last-run.tsv"
  LOG_DIR="$ROOT/var/log/notifs-backup"
  UNIT_DIR="$ROOT/etc/systemd/system"
  HB_KEY_FILE="$ROOT/home/4irl-notifs/secrets-persistent/NOTIFS_OPS_DELIVERY_KEY"
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

# BYTE-IDENTICAL with read_pins in fetch-pinned.sh: change both together (test_install_backup.sh fails when they differ).
# read_pins <file>: literal KEY=VALUE reader (never source). Sets the eight PIN_* globals; no quotes, no CR, no unknown or
# duplicate keys, all eight keys required, values shaped like versions and sha256 hex. Blank lines and # comments are skipped.
read_pins() {
  local file="$1" line key val seen=" " k
  local -a keys=(SHELLCHECK_VERSION SHELLCHECK_SHA256_AMD64 SHELLCHECK_SHA256_ARM64 RESTIC_VERSION RESTIC_SHA256_AMD64 RESTIC_SHA256_ARM64 CLOUDFLARED_VERSION CLOUDFLARED_SHA256_AMD64)
  [ -f "$file" ] && [ ! -L "$file" ] || { echo "pins: $file is not a regular file" >&2; return 1; }
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in
      '' | '#'*) continue ;;
    esac
    case "$line" in *$'\r'*) echo "pins: carriage return in $file" >&2; return 1 ;; esac
    case "$line" in *[\"\']*) echo "pins: quotes are not allowed in $file" >&2; return 1 ;; esac
    case "$line" in *=*) ;; *) echo "pins: malformed line in $file" >&2; return 1 ;; esac
    key="${line%%=*}"
    val="${line#*=}"
    case " ${keys[*]} " in *" $key "*) ;; *) echo "pins: unknown key $key" >&2; return 1 ;; esac
    case "$seen" in *" $key "*) echo "pins: duplicate key $key" >&2; return 1 ;; esac
    seen="$seen$key "
    case "$key" in
      *_VERSION) [[ "$val" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "pins: bad version for $key" >&2; return 1; } ;;
      *) [[ "$val" =~ ^[0-9a-f]{64}$ ]] || { echo "pins: bad sha256 for $key" >&2; return 1; } ;;
    esac
    printf -v "PIN_$key" '%s' "$val"
  done < "$file"
  for k in "${keys[@]}"; do
    case "$seen" in *" $k "*) ;; *) echo "pins: missing key $k" >&2; return 1 ;; esac
  done
}

# parse_const <NAME> <file>: the value of the single top-level NAME=value line (optionally single-quoted), no whitespace.
parse_const() {
  local name="$1" file="$2" n val
  n="$(grep -c "^${name}=" "$file" || true)"
  [ "$n" = "1" ] || return 1
  val="$(grep "^${name}=" "$file")"
  val="${val#*=}"
  case "$val" in \'*\') val="${val#\'}"; val="${val%\'}" ;; *) ;; esac
  case "$val" in '' | *[[:space:]\'\"]*) return 1 ;; *) ;; esac
  printf '%s' "$val"
}

# ---- source, arch, preconditions ----

SOURCE_DIR=""
SRC_PG_CONTAINER=""
SRC_NTFY_VOLUME=""
SRC_SQLITE_IMAGE=""
ARCH=""
PRE_FAILS=()

load_source() {
  local f
  [ -n "$SOURCE_DIR" ] && [ -d "$SOURCE_DIR" ] || die "--source DIR (the copied ops/backup directory) is required"
  for f in "${SCRIPT_PAYLOAD[@]}" "${DATA_PAYLOAD[@]}" "${UNIT_PAYLOAD[@]}" pins.env; do
    { [ -f "$SOURCE_DIR/$f" ] && [ ! -L "$SOURCE_DIR/$f" ]; } || die "source file missing: $SOURCE_DIR/$f"
  done
  read_pins "$SOURCE_DIR/pins.env" || die "bad pins.env in $SOURCE_DIR"
  SRC_PG_CONTAINER="$(parse_const PG_CONTAINER "$SOURCE_DIR/backup.sh")" || die "cannot parse a single PG_CONTAINER from backup.sh"
  SRC_NTFY_VOLUME="$(parse_const NTFY_VOLUME "$SOURCE_DIR/backup.sh")" || die "cannot parse a single NTFY_VOLUME from backup.sh"
  SRC_SQLITE_IMAGE="$(parse_const SQLITE_IMAGE "$SOURCE_DIR/backup.sh")" || die "cannot parse a single SQLITE_IMAGE from backup.sh"
  [[ "$SRC_SQLITE_IMAGE" =~ @sha256:[0-9a-f]{64}$ ]] || die "SQLITE_IMAGE in backup.sh is not pinned by @sha256 digest"
}

pre_fail() { PRE_FAILS+=("$1"); }

check_preconditions() {
  local t m state
  for t in $TOOLS; do
    command -v "$t" >/dev/null 2>&1 || pre_fail "missing tool: $t"
  done
  m="$(uname -m)"
  case "$m" in
    x86_64) ARCH=amd64 ;;
    aarch64) ARCH=arm64 ;;
    *) pre_fail "unsupported architecture $m (need x86_64 or aarch64)" ;;
  esac
  if command -v docker >/dev/null 2>&1; then
    state="$(timeout 20 docker inspect -f '{{.State.Running}}' "$SRC_PG_CONTAINER" 2>/dev/null)" || state=""
    [ "$state" = "true" ] || pre_fail "container $SRC_PG_CONTAINER is not running"
    timeout 20 docker volume inspect "$SRC_NTFY_VOLUME" >/dev/null 2>&1 || pre_fail "docker volume $SRC_NTFY_VOLUME does not exist"
  fi
  if [ "${#PRE_FAILS[@]}" -gt 0 ]; then
    for m in "${PRE_FAILS[@]}"; do say "FAIL: $m"; done
    die "preconditions failed (${#PRE_FAILS[@]}); nothing was changed"
  fi
}

# ---- placing files ----

UNITS_CHANGED=0
TIMER_CHANGED=0

# mode_of <path>: "uid:gid mode"
mode_of() { stat -c '%u:%g %a' -- "$1" 2>/dev/null || true; }

ensure_dir() {
  local dir="$1" mode="$2"
  if [ -d "$dir" ] && [ ! -L "$dir" ] && [ "$(mode_of "$dir")" = "$OWNER ${mode#0}" ]; then
    say "already: dir $dir"
  else
    install -d -o root -g root -m "$mode" -- "$dir"
    say "changed: dir $dir"
  fi
}

# place_file <src> <dest> <mode>: atomic (dest.new then mv); prints already:/changed:; returns 0 when it changed dest, 1 when not.
place_file() {
  local src="$1" dest="$2" mode="$3"
  if [ -f "$dest" ] && [ ! -L "$dest" ] && cmp -s -- "$src" "$dest" && [ "$(mode_of "$dest")" = "$OWNER ${mode#0}" ]; then
    say "already: $dest"
    return 1
  fi
  install -o root -g root -m "$mode" -- "$src" "$dest.new"
  mv -f -- "$dest.new" "$dest"
  say "changed: $dest"
  return 0
}

TMP_DIR=""
cleanup() { [ -n "$TMP_DIR" ] && rm -rf -- "$TMP_DIR"; return 0; }

# restic_current: the installed restic is the pinned version and matches the recorded hash.
restic_current() {
  local rec act ver
  [ -x "$BIN" ] && [ -f "$SHAFILE" ] || return 1
  read -r rec _ < "$SHAFILE" || return 1
  act="$(sha256sum -- "$BIN" 2>/dev/null)" || return 1
  act="${act%% *}"
  [ -n "$rec" ] && [ "$rec" = "$act" ] || return 1
  ver="$("$BIN" version 2>/dev/null || true)"
  [[ "$ver" == "restic ${PIN_RESTIC_VERSION} "* ]]
}

# fetch_restic: download the pinned archive into TMP_DIR, check its hash BEFORE anything is placed, leave TMP_DIR/restic.
fetch_restic() {
  local archive url want got
  archive="restic_${PIN_RESTIC_VERSION}_linux_${ARCH}.bz2"
  url="https://github.com/restic/restic/releases/download/v${PIN_RESTIC_VERSION}/${archive}"
  case "$ARCH" in amd64) want="$PIN_RESTIC_SHA256_AMD64" ;; *) want="$PIN_RESTIC_SHA256_ARM64" ;; esac
  say "fetching: $archive"
  curl --fail --silent --show-error --location --connect-timeout 20 --max-time 300 --retry 3 -o "$TMP_DIR/$archive" "$url" \
    || die "download of $url failed; nothing was changed"
  got="$(sha256sum -- "$TMP_DIR/$archive")" || die "cannot hash $archive; nothing was changed"
  got="${got%% *}"
  [ "$got" = "$want" ] || die "sha256 mismatch for $archive: got ${got:-none}, want $want; nothing was changed"
  bunzip2 -c -- "$TMP_DIR/$archive" > "$TMP_DIR/restic" || die "decompress of $archive failed; nothing was changed"
  [ -s "$TMP_DIR/restic" ] || die "decompressed restic is empty; nothing was changed"
}

cmd_install() {
  local f enabled=0 active=0 bin_hash
  load_source
  check_preconditions
  TMP_DIR="$(mktemp -d)"
  trap cleanup EXIT
  if ! restic_current; then fetch_restic; fi

  ensure_dir "$OPT_DIR" 0755
  ensure_dir "$ETC_DIR" 0700
  ensure_dir "$LIB_DIR" 0755
  ensure_dir "$WORK_DIR" 0700
  ensure_dir "$STATUS_DIR" 0755
  ensure_dir "$LOG_DIR" 0750
  ensure_dir "$UNIT_DIR" 0755
  ensure_dir "$(dirname "$BIN")" 0755

  for f in "${SCRIPT_PAYLOAD[@]}"; do place_file "$SOURCE_DIR/$f" "$OPT_DIR/$f" 0755 || true; done
  for f in "${DATA_PAYLOAD[@]}"; do place_file "$SOURCE_DIR/$f" "$OPT_DIR/$f" 0644 || true; done
  for f in "${UNIT_PAYLOAD[@]}"; do
    if place_file "$SOURCE_DIR/$f" "$UNIT_DIR/$f" 0644; then
      UNITS_CHANGED=1
      if [ "$f" = "$TIMER_UNIT" ]; then TIMER_CHANGED=1; fi
    fi
  done

  if [ -s "$TMP_DIR/restic" ]; then
    place_file "$TMP_DIR/restic" "$BIN" 0755 || true
  else
    say "already: $BIN (restic ${PIN_RESTIC_VERSION})"
  fi
  bin_hash="$(sha256sum -- "$BIN")" || die "cannot hash $BIN"
  printf '%s  %s\n' "${bin_hash%% *}" "/usr/local/bin/restic" > "$TMP_DIR/restic.sha256"
  place_file "$TMP_DIR/restic.sha256" "$SHAFILE" 0644 || true

  if [ "$UNITS_CHANGED" = 1 ]; then systemctl daemon-reload; say "changed: systemctl daemon-reload"; fi
  systemctl is-enabled "$TIMER_UNIT" >/dev/null 2>&1 && enabled=1
  systemctl is-active "$TIMER_UNIT" >/dev/null 2>&1 && active=1
  if [ "$enabled" = 0 ] || [ "$active" = 0 ]; then
    systemctl enable --now "$TIMER_UNIT"
    say "changed: $TIMER_UNIT enabled and started"
  elif [ "$TIMER_CHANGED" = 1 ]; then
    systemctl restart "$TIMER_UNIT"
    say "changed: $TIMER_UNIT restarted"
  else
    say "already: $TIMER_UNIT enabled and active"
  fi

  if docker image inspect "$SRC_SQLITE_IMAGE" >/dev/null 2>&1; then
    say "already: image $SRC_SQLITE_IMAGE"
  else
    timeout 600 docker pull "$SRC_SQLITE_IMAGE" >/dev/null || die "docker pull $SRC_SQLITE_IMAGE failed"
    say "changed: pulled image $SRC_SQLITE_IMAGE"
  fi

  if [ ! -e "$ENV_FILE" ]; then
    say "note: $ENV_FILE is missing; it is written by the prod deploy (GitHub secrets BACKUP_*, 4irl-notifs user, 0600, six keys); run a deploy, then init-repo"
  fi
  return 0
}

# ---- verify ----

FAILS=0
chk_ok() { say "ok: $1"; }
chk_fail() { say "FAIL: $1"; FAILS=$((FAILS + 1)); }
chk_warn() { say "WARN: $1"; }

# chk_file <installed> <source-or-empty> <mode>
chk_file() {
  local dest="$1" src="$2" mode="$3"
  if [ ! -f "$dest" ] || [ -L "$dest" ]; then chk_fail "$dest is missing"; return 0; fi
  if [ -n "$src" ] && ! cmp -s -- "$src" "$dest"; then chk_fail "$dest differs from the repo copy"; return 0; fi
  if [ "$(mode_of "$dest")" != "$OWNER ${mode#0}" ]; then chk_fail "$dest owner/mode is $(mode_of "$dest"), want $OWNER ${mode#0}"; return 0; fi
  chk_ok "$dest"
}

# verify_window: timer start + TimeoutStartSec + AccuracySec must end inside window.env.
verify_window() {
  local timer="$UNIT_DIR/$TIMER_UNIT" svc="$UNIT_DIR/$SERVICE_UNIT" win="$OPT_DIR/window.env"
  local oncal h m s tz timeout_s acc acc_s unit wstart wend wtz start_s end_s line
  oncal="$(sed -n 's/^OnCalendar=//p' "$timer" 2>/dev/null | head -n 1)"
  if [[ ! "$oncal" =~ ^\*-\*-\*[[:space:]]([0-9]{2}):([0-9]{2}):([0-9]{2})[[:space:]](.+)$ ]]; then
    chk_fail "window: cannot parse OnCalendar from $timer"; return 0
  fi
  h="${BASH_REMATCH[1]}"; m="${BASH_REMATCH[2]}"; s="${BASH_REMATCH[3]}"; tz="${BASH_REMATCH[4]}"
  timeout_s="$(sed -n 's/^TimeoutStartSec=//p' "$svc" 2>/dev/null | head -n 1)"
  acc="$(sed -n 's/^AccuracySec=//p' "$timer" 2>/dev/null | head -n 1)"
  if [[ ! "$timeout_s" =~ ^[0-9]+$ ]]; then chk_fail "window: cannot parse TimeoutStartSec from $svc"; return 0; fi
  if [[ ! "$acc" =~ ^([0-9]+)(s|min)$ ]]; then chk_fail "window: cannot parse AccuracySec from $timer"; return 0; fi
  acc_s="${BASH_REMATCH[1]}"; unit="${BASH_REMATCH[2]}"
  if [ "$unit" = "min" ]; then acc_s=$((10#$acc_s * 60)); fi
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in
      BACKUP_WINDOW_START=*) wstart="${line#*=}" ;;
      BACKUP_WINDOW_END=*) wend="${line#*=}" ;;
      BACKUP_WINDOW_TZ=*) wtz="${line#*=}" ;;
      *) ;;
    esac
  done < "$win"
  if [[ ! "${wend:-}" =~ ^([0-9]{2}):([0-9]{2})$ ]]; then chk_fail "window: cannot parse BACKUP_WINDOW_END"; return 0; fi
  end_s=$((10#${BASH_REMATCH[1]} * 3600 + 10#${BASH_REMATCH[2]} * 60))
  start_s=$((10#$h * 3600 + 10#$m * 60 + 10#$s))
  if [ "$tz" != "${wtz:-}" ]; then chk_fail "window: timer zone '$tz' differs from window zone '${wtz:-}'"; return 0; fi
  if [[ "${wstart:-}" =~ ^([0-9]{2}):([0-9]{2})$ ]] && [ "$start_s" -lt $((10#${BASH_REMATCH[1]} * 3600 + 10#${BASH_REMATCH[2]} * 60)) ]; then
    chk_fail "window: timer starts before the window start"; return 0
  fi
  if [ $((start_s + timeout_s + acc_s)) -le "$end_s" ]; then
    chk_ok "window: timer + TimeoutStartSec + AccuracySec ends by ${wend}"
  else
    chk_fail "window: timer + TimeoutStartSec + AccuracySec ($((start_s + timeout_s + acc_s))s) passes the window end ${wend}"
  fi
}

verify_last_run() {
  local s e res snap ph end_epoch now age
  if [ ! -f "$LAST_RUN" ]; then chk_fail "last-run.tsv is missing ($LAST_RUN)"; return 0; fi
  IFS=$'\t' read -r s e res snap ph < "$LAST_RUN" || true
  if [ "${res:-}" != "ok" ]; then chk_fail "last run result is '${res:-?}' (failed phase ${ph:--}), want ok"; return 0; fi
  if [[ ! "${e:-}" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]]; then chk_fail "last run end time is malformed"; return 0; fi
  end_epoch="$(date -u -d "$e" +%s 2>/dev/null)" || { chk_fail "last run end time is unparsable"; return 0; }
  now="$(date +%s)"
  age=$((now - end_epoch))
  if [ "$age" -gt "$MAX_AGE_SECS" ]; then chk_fail "last run ended $((age / 3600))h ago (limit 36h)"; return 0; fi
  chk_ok "last run ok, $((age / 3600))h ago, snapshot ${snap:--}"
}

cmd_verify() {
  local f k ver rec act next key
  load_source
  for f in "${SCRIPT_PAYLOAD[@]}"; do chk_file "$OPT_DIR/$f" "$SOURCE_DIR/$f" 0755; done
  for f in "${DATA_PAYLOAD[@]}"; do chk_file "$OPT_DIR/$f" "$SOURCE_DIR/$f" 0644; done
  for f in "${UNIT_PAYLOAD[@]}"; do chk_file "$UNIT_DIR/$f" "$SOURCE_DIR/$f" 0644; done
  chk_file "$SHAFILE" "" 0644

  if [ -x "$BIN" ] && [ "$(mode_of "$BIN")" = "$OWNER 755" ]; then
    ver="$("$BIN" version 2>/dev/null || true)"
    rec=""; [ -f "$SHAFILE" ] && read -r rec _ < "$SHAFILE" || true
    act="$(sha256sum -- "$BIN" 2>/dev/null || true)"; act="${act%% *}"
    if [[ "$ver" != "restic ${PIN_RESTIC_VERSION} "* ]]; then chk_fail "restic is not version ${PIN_RESTIC_VERSION}"
    elif [ -z "$rec" ] || [ "$rec" != "$act" ]; then chk_fail "restic binary hash differs from $SHAFILE"
    else chk_ok "restic ${PIN_RESTIC_VERSION} matches its recorded hash"; fi
  else
    chk_fail "$BIN is missing, not executable or has the wrong owner/mode"
  fi

  if read_backup_env "$ENV_FILE" "$ENV_OWNER"; then
    for k in "${ENV_KEYS[@]}"; do
      if [ -z "${!k}" ]; then chk_fail "backup.env has an empty or missing $k"; fi
    done
    chk_ok "backup.env read (keys checked)"
  else
    chk_fail "backup.env: $ENV_READ_ERROR"
  fi

  if systemctl is-enabled "$TIMER_UNIT" >/dev/null 2>&1; then chk_ok "$TIMER_UNIT enabled"; else chk_fail "$TIMER_UNIT is not enabled"; fi
  if systemctl is-active "$TIMER_UNIT" >/dev/null 2>&1; then chk_ok "$TIMER_UNIT active"; else chk_fail "$TIMER_UNIT is not active"; fi
  next="$(systemctl show "$TIMER_UNIT" -p NextElapseUSecRealtime --value 2>/dev/null || true)"
  if [ -n "$next" ] && [ "$next" != "n/a" ]; then chk_ok "next elapse: $next"; else chk_fail "$TIMER_UNIT has no next elapse"; fi
  verify_window
  verify_last_run

  if docker image inspect "$SRC_SQLITE_IMAGE" >/dev/null 2>&1; then chk_ok "image $SRC_SQLITE_IMAGE present"; else chk_fail "image $SRC_SQLITE_IMAGE is not present locally"; fi

  if [ -f "$HB_KEY_FILE" ]; then
    key="$(cat -- "$HB_KEY_FILE"; printf x)"; key="${key%x}"
    if [[ "$key" =~ $HB_KEY_RE ]]; then chk_ok "heartbeat key file well formed"; else chk_warn "heartbeat key file $HB_KEY_FILE is malformed (heartbeat pings will be skipped)"; fi
  else
    chk_warn "heartbeat key file $HB_KEY_FILE is missing (heartbeat pings will be skipped)"
  fi

  if [ "$FAILS" -gt 0 ]; then say "verify: $FAILS FAIL"; return 1; fi
  say "verify: ok"
}

# ---- status, run-now, init-repo ----

cmd_status() {
  set +e
  local f
  for f in "${SCRIPT_PAYLOAD[@]}" "${DATA_PAYLOAD[@]}"; do
    if [ -f "$OPT_DIR/$f" ]; then say "installed: $OPT_DIR/$f"; else say "missing: $OPT_DIR/$f"; fi
  done
  if [ -x "$BIN" ]; then say "restic: $("$BIN" version 2>/dev/null | head -n 1)"; else say "restic: not installed"; fi
  if [ -f "$ENV_FILE" ]; then say "backup.env: present"; else say "backup.env: missing"; fi
  say "timer enabled: $(systemctl is-enabled "$TIMER_UNIT" 2>/dev/null)"
  say "timer active: $(systemctl is-active "$TIMER_UNIT" 2>/dev/null)"
  say "next elapse: $(systemctl show "$TIMER_UNIT" -p NextElapseUSecRealtime --value 2>/dev/null)"
  if [ -f "$LAST_RUN" ]; then say "last-run: $(head -n 1 "$LAST_RUN")"; else say "last-run: none"; fi
  exit 0
}

cmd_run_now() {
  local -a props=()
  local p
  [ -x "$OPT_DIR/backup.sh" ] || die "$OPT_DIR/backup.sh is not installed; run install first"
  for p in "${RUN_PROPS[@]}"; do props+=(-p "$p"); done
  exec systemd-run --wait --pipe --collect --unit="notifs-backup-manual-$(date +%s)" "${props[@]}" "$OPT_DIR/backup.sh" manual
}

cmd_init_repo() {
  local rc=0 errf k
  [ -x "$BIN" ] || die "$BIN is not installed; run install first"
  read_backup_env "$ENV_FILE" "$ENV_OWNER" || die "backup.env: $ENV_READ_ERROR"
  for k in "${ENV_KEYS[@]}"; do
    [ -n "${!k}" ] || die "backup.env has an empty or missing $k"
    export "${k?}"
  done
  unset RESTIC_PASSWORD_FILE RESTIC_PASSWORD_COMMAND RESTIC_REPOSITORY_FILE AWS_SESSION_TOKEN AWS_PROFILE \
    AWS_SHARED_CREDENTIALS_FILE AWS_CONFIG_FILE
  mkdir -p -- "$WORK_DIR/restic-cache"
  export RESTIC_CACHE_DIR="$WORK_DIR/restic-cache"
  TMP_DIR="$(mktemp -d)"
  trap cleanup EXIT
  errf="$TMP_DIR/err"
  timeout 60 "$BIN" cat config >/dev/null 2>"$errf" || rc=$?
  if [ "$rc" -ne 10 ]; then
    if [ "$rc" -eq 0 ]; then die "refusing to init: the repository already exists (restic cat config succeeded)"; fi
    head -n 3 "$errf" >&2 || true
    die "refusing to init: restic cat config exited $rc, not 10 (repository missing)"
  fi
  timeout 120 "$BIN" init || die "restic init failed"
  say "changed: restic repository initialised"
}

main() {
  local cmd="${1:-}"
  [ -n "$cmd" ] || usage
  shift
  while [ $# -gt 0 ]; do
    case "$1" in
      --source) [ $# -ge 2 ] || usage; SOURCE_DIR="$2"; shift 2 ;;
      *) usage ;;
    esac
  done
  case "$cmd" in install | status | verify | run-now | init-repo) ;; *) usage ;; esac
  gate_overrides
  set_paths
  if [ "${NOTIFS_BACKUP_TESTING:-}" != "1" ] && [ "$(id -u)" -ne 0 ]; then die "run as root (make host-backup uses sudo)"; fi
  case "$cmd" in
    install) cmd_install ;;
    status) cmd_status ;;
    verify) cmd_verify ;;
    run-now) cmd_run_now ;;
    init-repo) cmd_init_repo ;;
    *) usage ;;
  esac
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then main "$@"; fi
