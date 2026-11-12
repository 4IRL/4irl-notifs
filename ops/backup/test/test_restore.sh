#!/usr/bin/env bash
# Offline tests for restore.sh. Each case runs restore.sh as a child process under `env -i` (a clean environment with
# only the NOTIFS_BACKUP_* test overrides, NOTIFS_BACKUP_TESTING=1 and a PATH of stubs) against a restic stub that serves
# a fixture tree and a docker stub that answers per-case files. No real credentials: every secret is a marker string.
# SC2016: stub bodies are single-quoted on purpose. SC1091: lib.sh is sourced by relative path.
# SC2154: $out and $rc are set by run_cmd in lib.sh.
# shellcheck disable=SC1091,SC2016,SC2154
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=lib.sh
. "$here/lib.sh"
SCRIPT="$here/../restore.sh"

t_init

# restic: logs argv; `restore` materialises the fixture tree under --target at the snapshot's absolute staging path.
cat > "$STUB_BIN/restic" <<'EOF'
#!/usr/bin/env bash
printf 'restic' >> "$CALL_LOG"
for a in "$@"; do printf ' %s' "$a" >> "$CALL_LOG"; done
printf '\n' >> "$CALL_LOG"
sub="${1:-}"
if [ -f "$CASE_DIR/fail_restic-$sub" ]; then echo "Fatal: boom $RESTIC_PASSWORD" >&2; exit "$(cat "$CASE_DIR/fail_restic-$sub")"; fi
case "$sub" in
  snapshots) echo "ID        Time   Host" ;;
  restore)
    prev=""
    for a in "$@"; do
      if [ "$prev" = "--target" ]; then target="$a"; fi
      prev="$a"
    done
    d="$target/var/lib/notifs-backup/work/staging"
    mkdir -p "$d/postgres" "$d/ntfy"
    if [ -f "$CASE_DIR/fx-dump" ]; then cp "$CASE_DIR/fx-dump" "$d/postgres/delivery.dump"; fi
    if [ -f "$CASE_DIR/fx-auth" ]; then cp "$CASE_DIR/fx-auth" "$d/ntfy/auth.db"; fi
    ;;
esac
exit 0
EOF
chmod +x "$STUB_BIN/restic"

# docker: logs argv; answers the database list, CREATE, pg_restore, counts, volume inspect and the integrity run.
cat > "$STUB_BIN/docker" <<'EOF'
#!/usr/bin/env bash
printf 'docker' >> "$CALL_LOG"
for a in "$@"; do printf ' %s' "$a" >> "$CALL_LOG"; done
printf '\n' >> "$CALL_LOG"
all=" $* "
case "${1:-}" in
  volume) cat "$CASE_DIR/mountpoint" 2> /dev/null || true ;;
  run) cat "$CASE_DIR/integrity" 2> /dev/null || echo ok ;;
  exec)
    case "$all" in
      *pg_restore*) cat > /dev/null; if [ -f "$CASE_DIR/fail_pgrestore" ]; then echo "pg_restore: error: boom" >&2; exit 1; fi ;;
      *"FROM pg_database"*) cat "$CASE_DIR/dbexists" 2> /dev/null || true ;;
      *"CREATE DATABASE"*) if [ -f "$CASE_DIR/fail_create" ]; then exit 1; fi ;;
      *"count(*)"*) printf 'destinations|3\nroutes|5\nheartbeat_checks|2\nmessages|40\n' ;;
    esac
    ;;
esac
exit 0
EOF
chmod +x "$STUB_BIN/docker"

CASE_N=0
CASE_DIR=""
ENVF=""
LOCKF=""

# new_case: fresh per-case dir, env file (0600, current owner), fixtures; resets the call log.
new_case() {
  CASE_N=$((CASE_N + 1))
  CASE_DIR="$T_TMP/case$CASE_N"
  mkdir -p "$CASE_DIR/work"
  : > "$CASE_DIR/work/.notifs-backup-root"
  export CASE_DIR
  ENVF="$CASE_DIR/backup.env"
  LOCKF="$CASE_DIR/lock"
  (
    umask 077
    printf 'RESTIC_REPOSITORY=s3:https://acct.example.test/bucket\nRESTIC_PASSWORD=Pw+/=SecretMarker\nAWS_ACCESS_KEY_ID=KEYIDMARKER\nAWS_SECRET_ACCESS_KEY=sk+/=secretmarker\nAWS_DEFAULT_REGION=auto\nDISCORD_WEBHOOK=https://discord.example.test/api/webhooks/1/tok\n' > "$ENVF"
  )
  printf 'DUMPDATA' > "$CASE_DIR/fx-dump"
  printf 'AUTHDB' > "$CASE_DIR/fx-auth"
  t_reset_calls
}

# rr <restore.sh args...>: restore.sh as a child under env -i.
rr() {
  run_cmd env -i PATH="$PATH" CALL_LOG="$CALL_LOG" CASE_DIR="$CASE_DIR" NOTIFS_BACKUP_TESTING=1 \
    NOTIFS_BACKUP_ENV_FILE="$ENVF" NOTIFS_BACKUP_ENV_OWNER="$(id -u):$(id -g)" NOTIFS_BACKUP_ROOT="$CASE_DIR/work" \
    NOTIFS_BACKUP_LOCK="$LOCKF" bash "$SCRIPT" "$@"
}

# d_args <pattern>: the "-d <value>" values on the recorded docker lines that match the pattern.
d_values() { grep -E "$1" "$CALL_LOG" | grep -oE ' -d [^ ]+' | sed 's/^ -d //' || true; }

# ---- env owner resolution (no NOTIFS_BACKUP_ENV_OWNER): the 4irl-notifs user's uid:gid, fail closed ----
new_case
mkdir -p "$CASE_DIR/shimbin"
printf '#!/bin/sh\nexit 1\n' > "$CASE_DIR/shimbin/id"
chmod 755 "$CASE_DIR/shimbin/id"
run_cmd env -i PATH="$CASE_DIR/shimbin:$PATH" CALL_LOG="$CALL_LOG" CASE_DIR="$CASE_DIR" NOTIFS_BACKUP_TESTING=1 \
  NOTIFS_BACKUP_ENV_FILE="$ENVF" NOTIFS_BACKUP_ROOT="$CASE_DIR/work" NOTIFS_BACKUP_LOCK="$LOCKF" bash "$SCRIPT" list
expect_rc "unresolvable 4irl-notifs user exits 2" 2
expect_out_has "unresolvable user is named" "cannot resolve the uid:gid of user 4irl-notifs"
printf '#!/bin/sh\nif [ "$1" = "-u" ]; then echo 12345; else echo 12346; fi\n' > "$CASE_DIR/shimbin/id"
run_cmd env -i PATH="$CASE_DIR/shimbin:$PATH" CALL_LOG="$CALL_LOG" CASE_DIR="$CASE_DIR" NOTIFS_BACKUP_TESTING=1 \
  NOTIFS_BACKUP_ENV_FILE="$ENVF" NOTIFS_BACKUP_ROOT="$CASE_DIR/work" NOTIFS_BACKUP_LOCK="$LOCKF" bash "$SCRIPT" list
expect_rc "a resolved user whose uid:gid does not own backup.env is refused" 2
expect_out_has "default owner is the resolved uid:gid" "owner is not 12345:12346"

# ---- list ----
new_case
rr list
expect_rc "list exits 0" 0
expect_calls_has "list calls restic snapshots with the host and tag filters" "restic snapshots --host 4irl-notifs --tag notifs"
expect_calls_lacks "list never touches docker" "docker"

# ---- usage ----
new_case
rr
expect_rc "no mode is a refusal" 2
rr nonsense
expect_rc "unknown mode is a refusal" 2
rr db
expect_rc "db without --into is a refusal" 2
rr db --into abc --out /x
expect_rc "db with --out is a refusal" 2
rr ntfy-auth
expect_rc "ntfy-auth without --out is a refusal" 2

# ---- db refusals ----
for bad in 1abc Delivery a has-dash 'x;drop' abcdefghijklmnopqrstuvwxyz0123456; do
  new_case
  rr db --into "$bad"
  expect_rc "db --into '$bad' is refused" 2
  expect_calls_lacks "db --into '$bad' touched neither restic nor docker" "restic"
  expect_calls_lacks "db --into '$bad' touched no docker" "docker"
done
new_case
rr db --into delivery
expect_rc "db --into delivery is refused" 2
expect_out_has "the delivery refusal names the live database" "live database"
expect_calls_lacks "delivery refusal touched no docker" "docker"

new_case
printf 'rest1\n' > "$CASE_DIR/dbexists"
rr db --into rest1
expect_rc "db into an existing database is refused" 2
expect_out_has "the refusal names the existing database" "database rest1 already exists"
expect_calls_lacks "an existing database is never created over" "CREATE DATABASE"
expect_calls_lacks "an existing database is never pg_restored into" "pg_restore"

new_case
rr db --into rest1 --snapshot ../etc
expect_rc "a non-hex snapshot id is refused" 2

# ---- db positive control ----
new_case
rr db --into rest1
expect_rc "db restore exits 0" 0
expect_calls_has "the database is created with CREATE DATABASE" "CREATE DATABASE rest1"
expect_calls_has "pg_restore runs with the guarded flags into the new database" "pg_restore -U delivery --no-owner --role=delivery --exit-on-error -d rest1"
expect_calls_has "the default snapshot is latest" "restic restore latest --host 4irl-notifs --tag notifs"
expect_out_has "row counts of destinations are printed" "destinations|3"
expect_out_has "row counts of routes are printed" "routes|5"
expect_out_has "row counts of heartbeat_checks are printed" "heartbeat_checks|2"
expect_out_has "row counts of messages are printed" "messages|40"
nd="$(d_values '^docker exec' | wc -l | tr -d ' ')"
if [ "$nd" -ge 2 ]; then _pass; else _fail "positive control: the -d arguments are recorded" "only $nd recorded"; fi
bad_d="$(d_values '^docker exec' | grep -vxF rest1 || true)"
expect_eq "every recorded -d value equals the --into name" "$bad_d" ""
if grep -qE '(^| )-d delivery( |$)' "$CALL_LOG"; then _fail "no recorded argv has -d delivery"; else _pass; fi
if grep -qE 'DROP|ALTER' "$CALL_LOG"; then _fail "no recorded argv has DROP or ALTER"; else _pass; fi
if grep -E 'psql|pg_restore' "$CALL_LOG" | grep -vE 'template1| -d rest1' | grep -q .; then _fail "every psql/pg_restore targets template1 or the new database"; else _pass; fi

new_case
rr db --into rest1 --snapshot abc12345
expect_rc "db restore with --snapshot exits 0" 0
expect_calls_has "--snapshot is passed through to restic" "restic restore abc12345 --host 4irl-notifs --tag notifs"

# ---- db failures ----
new_case
touch "$CASE_DIR/fail_pgrestore"
rr db --into rest1
expect_rc "a pg_restore failure exits 1" 1
expect_out_has "the failure names the database to drop" "DROP DATABASE rest1"
expect_out_has "the failure says the database is half-restored" "half-restored"
expect_calls_lacks "the drop command is printed, never run against delivery" "DROP DATABASE delivery"

new_case
printf '1' > "$CASE_DIR/fail_restic-restore"
rr db --into rest1
expect_rc "a restic restore failure exits 1" 1
expect_calls_lacks "nothing is created when the dump cannot be fetched" "CREATE DATABASE"
expect_out_lacks "the repository password is masked in the failure output" "Pw+/=SecretMarker"

new_case
rm -f "$CASE_DIR/fx-dump"
rr db --into rest1
expect_rc "a snapshot without a dump exits 1" 1
expect_calls_lacks "no database is created without a dump" "CREATE DATABASE"

new_case
touch "$CASE_DIR/fail_create"
rr db --into rest1
expect_rc "a CREATE DATABASE failure exits 1" 1
expect_calls_lacks "pg_restore is not attempted after a failed create" "pg_restore"

# ---- lock ----
new_case
flock "$LOCKF" sleep 20 &
holder=$!
for _ in $(seq 1 50); do
  if ! flock -n "$LOCKF" true 2> /dev/null; then break; fi
  sleep 0.1
done
rr db --into rest1
expect_rc "a lock held by a backup is a refusal" 2
expect_out_has "the lock refusal is explained" "holds the lock"
expect_calls_lacks "nothing runs while the lock is held" "docker"
rr list
expect_rc "list also refuses while the lock is held" 2
kill "$holder" 2> /dev/null || true
wait "$holder" 2> /dev/null || true

# ---- env ----
new_case
rm -f "$ENVF"
rr list
expect_rc "a missing env file is a refusal" 2
new_case
chmod 0644 "$ENVF"
rr list
expect_rc "an env file with mode 0644 is a refusal" 2

# ---- ntfy-auth refusals ----
new_case
mkdir "$CASE_DIR/exists"
rr ntfy-auth --out "$CASE_DIR/exists"
expect_rc "an existing --out directory is refused" 2
expect_calls_lacks "an existing --out never reaches restic" "restic"

new_case
rr ntfy-auth --out relative/dir
expect_rc "a relative --out is refused" 2

new_case
rr ntfy-auth --out /var/lib/docker/volumes/4irl-notifs_ntfy-auth/_data/restored
expect_rc "--out inside the docker data root is refused" 2
expect_out_has "the volume-path refusal is explained" "docker"
expect_calls_lacks "the volume-path refusal fetched nothing" "restic"

new_case
mkdir -p "$CASE_DIR/vol/_data"
printf '%s\n' "$CASE_DIR/vol/_data" > "$CASE_DIR/mountpoint"
rr ntfy-auth --out "$CASE_DIR/vol/_data/sub"
expect_rc "--out inside the inspected volume mountpoint is refused" 2
expect_out_has "the mountpoint refusal names the volume" "4irl-notifs_ntfy-auth"

# ---- ntfy-auth positive control ----
new_case
rr ntfy-auth --out "$CASE_DIR/restored"
expect_rc "ntfy-auth restore exits 0" 0
expect_file "the restored auth.db exists" "$CASE_DIR/restored/auth.db"
expect_eq "the restored auth.db has the snapshot content" "$(cat "$CASE_DIR/restored/auth.db" 2> /dev/null)" "AUTHDB"
expect_eq "the restored auth.db is 0600" "$(stat -c %a "$CASE_DIR/restored/auth.db" 2> /dev/null)" "600"
expect_calls_has "the integrity check mounts the NEW directory read-only" "-v $CASE_DIR/restored:/restored:ro"
expect_calls_has "the default snapshot is latest" "restic restore latest"
if grep -E '^docker run' "$CALL_LOG" | grep -qF "4irl-notifs_ntfy-auth"; then _fail "docker run never mounts the live ntfy-auth volume"; else _pass; fi
if grep -E '^docker run' "$CALL_LOG" | grep -qE -- '-v [^ ]+:[^ ]+( |$)' ; then
  if grep -E '^docker run' "$CALL_LOG" | grep -oE -- '-v [^ ]+' | grep -vE ':ro$' | grep -q .; then _fail "every docker run mount is read-only"; else _pass; fi
else
  _fail "positive control: the docker run mount is recorded"
fi
expect_calls_has "the integrity check runs without network" "--network none"
expect_calls_has "the integrity check runs as root (the image's default user cannot open the root-only copy)" "--network none --user 0:0"

new_case
rr ntfy-auth --out "$CASE_DIR/restored" --snapshot abc12345
expect_rc "ntfy-auth with --snapshot exits 0" 0
expect_calls_has "--snapshot is passed through to restic" "restic restore abc12345"

# ---- ntfy-auth failures ----
new_case
printf 'malformed: not ok' > "$CASE_DIR/integrity"
rr ntfy-auth --out "$CASE_DIR/restored"
expect_rc "a failing integrity_check exits 1" 1
expect_out_has "the integrity failure is explained" "failed integrity_check"
if [ -e "$CASE_DIR/restored" ]; then _fail "the unverified copy is removed after a failed integrity_check"; else _pass; fi

new_case
rm -f "$CASE_DIR/fx-auth"
rr ntfy-auth --out "$CASE_DIR/restored"
expect_rc "a snapshot without auth.db exits 1" 1
if [ -e "$CASE_DIR/restored" ]; then _fail "no --out directory is left behind when the file is missing"; else _pass; fi

# ---- shared constants are parsed from backup.sh, never redefined ----
for name in PG_CONTAINER SQLITE_IMAGE RESTIC_HOST RESTIC_TAG NTFY_VOLUME; do
  n="$(grep -c "^${name}=" "$here/../restore.sh" || true)"
  expect_eq "restore.sh does not define $name" "$n" "0"
done

t_finish
