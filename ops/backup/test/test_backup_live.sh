#!/usr/bin/env bash
# Live round trip for ops/backup: real Postgres, real sqlite (WAL mode, in a docker volume) and a real restic repository
# (a local directory standing in for R2). Runs the real backup.sh twice (so forget runs), then the real restore.sh
# `db --into delivery_restore` and `ntfy-auth --out`, and compares row checksums and auth.db content.
#
# Not part of `make backup-test` (run.sh skips test_backup_live*.sh); run it with `make backup-test-live`.
#   REQUIRE_DOCKER=1  a missing docker daemon or restic is a failure (CI), not a skip
#   NBLIVE_SKIP=1     opt out (exit 0 with a banner); refused together with REQUIRE_DOCKER=1
#
# Coverage note (accepted): two snapshots prune nothing and curl is stubbed, so the retention keep values and the real
# heartbeat pings are covered only by the stubbed unit tests (test_backup.sh); the heartbeat is first exercised for real
# against the VPS.
#
# The scripts under test are copied to a temp dir and the runtime constants in the copy of backup.sh (container and
# volume names, and the ntfy uid:gid) are rewritten, so the test never touches a real 4irl-notifs container or volume.
# The ntfy uid:gid is rewritten to the CURRENT user (1000:1000 on most dev machines, 1001 on a CI runner) because the
# test runs unprivileged and cannot chown a directory to another uid; the assertion that matters is unchanged: nothing
# in the ntfy volume may be owned by anyone else (root) after a backup.

# ---- prerequisites (before anything else, so the skip path needs no tools) ----
live_prereq() {
  local missing="" t
  if [ "${NBLIVE_SKIP:-}" = "1" ]; then
    if [ "${REQUIRE_DOCKER:-}" = "1" ]; then
      echo "FAIL: NBLIVE_SKIP=1 conflicts with REQUIRE_DOCKER=1; refusing to skip a required live test"
      exit 1
    fi
    echo "=== SKIP: ops/backup live round trip (NBLIVE_SKIP=1) ==="
    exit 0
  fi
  for t in docker restic flock timeout; do
    if ! command -v "$t" > /dev/null 2>&1; then missing="${missing:+$missing }$t"; fi
  done
  if [ -z "$missing" ] && ! docker info > /dev/null 2>&1; then missing="a reachable docker daemon"; fi
  if [ -n "$missing" ]; then
    if [ "${REQUIRE_DOCKER:-}" = "1" ]; then
      echo "FAIL: ops/backup live round trip needs: $missing (REQUIRE_DOCKER=1)"
      exit 1
    fi
    echo "=== SKIP: ops/backup live round trip: missing $missing (set REQUIRE_DOCKER=1 to fail instead) ==="
    exit 0
  fi
}
live_prereq

set -uo pipefail
here="$(cd "${BASH_SOURCE[0]%/*}" && pwd)"
src_dir="$(cd "$here/.." && pwd)"
# shellcheck source=lib.sh
. "$here/lib.sh"

t_init

SUFFIX="$$-$RANDOM"
PG="nbtest-pg-$SUFFIX"
NTFYVOL="nbtest-ntfy-$SUFFIX"
UIDGID="$(id -u):$(id -g)"
BK="$T_TMP/bk"
WORKROOT="$T_TMP/work"
STATE="$T_TMP/status"
LOGS="$T_TMP/logs"
REPO="$T_TMP/repo"
SECRETS="$T_TMP/secrets"
ENVF="$T_TMP/backup.env"
RESTORED_AUTH="$T_TMP/restored-auth"
RPW="nbtest-restic-password-$SUFFIX"

live_cleanup() {
  docker rm -fv "$PG" > /dev/null 2>&1 || true
  docker volume rm -f "$NTFYVOL" > /dev/null 2>&1 || true
  return 0
}
trap 'live_cleanup; t_cleanup' EXIT

SQLITE_IMAGE="$(grep -m1 '^SQLITE_IMAGE=' "$src_dir/backup.sh")"
SQLITE_IMAGE="${SQLITE_IMAGE#SQLITE_IMAGE=}"
SQLITE_IMAGE="${SQLITE_IMAGE//\'/}"

# ---- the scripts under test, with isolated runtime constants ----
mkdir -p "$BK" "$WORKROOT" "$STATE" "$LOGS" "$SECRETS"
cp -- "$src_dir/backup.sh" "$src_dir/restore.sh" "$src_dir/window.env" "$BK/"
sed -i -e "s|^PG_CONTAINER=.*|PG_CONTAINER=$PG|" -e "s|^NTFY_VOLUME=.*|NTFY_VOLUME=$NTFYVOL|" \
  -e "s|^NTFY_UID_GID=.*|NTFY_UID_GID=$UIDGID|" "$BK/backup.sh"
: > "$WORKROOT/.notifs-backup-root"
# A well-formed heartbeat key (dak_ plus 43 characters, no newline); curl is stubbed below.
printf '%s' 'dak_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA' > "$SECRETS/NOTIFS_OPS_DELIVERY_KEY"
t_stub curl 0 200

printf 'RESTIC_REPOSITORY=%s\nRESTIC_PASSWORD=%s\nAWS_ACCESS_KEY_ID=nbtest\nAWS_SECRET_ACCESS_KEY=nbtest-secret\nAWS_DEFAULT_REGION=auto\nDISCORD_WEBHOOK=https://hook.invalid/test\n' \
  "$REPO" "$RPW" > "$ENVF"
chmod 0600 "$ENVF"

restic_cmd() { env RESTIC_REPOSITORY="$REPO" RESTIC_PASSWORD="$RPW" restic "$@"; }
sq_run() { docker run --rm --network none --user "$UIDGID" -v "$NTFYVOL:/d" "$SQLITE_IMAGE" sqlite3 "$@"; }
psql_q() { docker exec "$PG" psql -U delivery -X -At -d "$1" -c "$2"; }

# checksums <database>: one "table md5" line per table, over the whole row text ordered by id.
checksums() {
  local db="$1" tbl
  for tbl in destinations routes heartbeat_checks messages; do
    printf '%s %s\n' "$tbl" "$(psql_q "$db" "SELECT md5(coalesce(string_agg(t::text, '|' ORDER BY id), '')) FROM $tbl t")"
  done
}

env_run() {
  env NOTIFS_BACKUP_TESTING=1 NOTIFS_BACKUP_ENV_FILE="$ENVF" NOTIFS_BACKUP_ENV_OWNER="$UIDGID" \
    NOTIFS_BACKUP_ROOT="$WORKROOT" NOTIFS_BACKUP_STATE_DIR="$STATE" NOTIFS_BACKUP_LOG_DIR="$LOGS" \
    NOTIFS_BACKUP_LOCK="$T_TMP/lock" NOTIFS_BACKUP_HEARTBEAT_KEY_FILE="$SECRETS/NOTIFS_OPS_DELIVERY_KEY" "$@"
}

# ---- throwaway Postgres with seeded data ----
docker run -d --name "$PG" -e POSTGRES_USER=delivery -e POSTGRES_DB=delivery -e POSTGRES_PASSWORD=nbtest \
  --health-cmd 'pg_isready -h 127.0.0.1 -U delivery -d delivery' --health-interval 1s --health-retries 60 \
  postgres:17.11-alpine3.23 > /dev/null 2>&1
for _ in $(seq 1 90); do
  [ "$(docker inspect -f '{{.State.Health.Status}}' "$PG" 2> /dev/null)" = "healthy" ] && break
  sleep 1
done
out=""
expect_eq "postgres container becomes healthy" "$(docker inspect -f '{{.State.Health.Status}}' "$PG" 2> /dev/null)" "healthy"

cat > "$T_TMP/seed.sql" << 'EOF'
CREATE TABLE destinations (id serial PRIMARY KEY, name text NOT NULL, kind text NOT NULL, sealed_secrets bytea);
CREATE TABLE routes (id serial PRIMARY KEY, destination_id int REFERENCES destinations(id), pattern text NOT NULL);
CREATE TABLE heartbeat_checks (id serial PRIMARY KEY, app_id text NOT NULL, name text NOT NULL, period_secs int NOT NULL);
CREATE TABLE messages (id serial PRIMARY KEY, destination_id int REFERENCES destinations(id), body jsonb NOT NULL, created_at timestamptz NOT NULL);
INSERT INTO destinations (name, kind, sealed_secrets) SELECT 'dest-' || i, 'webhook', decode(md5(i::text), 'hex') FROM generate_series(1, 20) i;
INSERT INTO routes (destination_id, pattern) SELECT 1 + (i % 20), 'topic.' || i FROM generate_series(1, 40) i;
INSERT INTO heartbeat_checks (app_id, name, period_secs) SELECT 'app' || (i % 3), 'check-' || i, 3600 * i FROM generate_series(1, 7) i;
INSERT INTO messages (destination_id, body, created_at)
  SELECT 1 + (i % 20), jsonb_build_object('n', i, 'text', 'message ' || i), timestamptz '2026-01-01 00:00:00+00' + i * interval '1 minute'
  FROM generate_series(1, 500) i;
EOF
seed_rc=0
docker exec -i "$PG" psql -U delivery -d delivery -X -v ON_ERROR_STOP=1 < "$T_TMP/seed.sql" > /dev/null 2>&1 || seed_rc=$?
out=""
expect_eq "postgres seed applied" "$seed_rc" "0"
SEEDED="$(checksums delivery)"
expect_eq "seeded messages row count" "$(psql_q delivery 'SELECT count(*) FROM messages')" "500"

# ---- fake ntfy volume: WAL-mode auth.db owned by the ntfy uid ----
docker volume create "$NTFYVOL" > /dev/null
docker run --rm --network none --user 0:0 -v "$NTFYVOL:/d" "$SQLITE_IMAGE" chown "$UIDGID" /d > /dev/null 2>&1
sq_run /d/auth.db "PRAGMA journal_mode=WAL; CREATE TABLE user (user text PRIMARY KEY, pass text NOT NULL, role text NOT NULL); INSERT INTO user VALUES ('alice','hash-a','user'),('bob','hash-b','user'),('admin','hash-x','admin');" > /dev/null 2>&1
AUTH_SEEDED="$(sq_run /d/auth.db 'SELECT * FROM user ORDER BY user' 2> /dev/null)"
expect_eq "auth.db seeded" "$(printf '%s' "$AUTH_SEEDED" | wc -l | tr -d ' ')" "2"

# ---- restic repository ----
restic_cmd init > "$T_TMP/restic-init.txt" 2>&1
init_rc=$?
out="$(cat "$T_TMP/restic-init.txt")"
expect_eq "restic init" "$init_rc" "0"

# ---- two real backups (the second one runs forget with a guard that needs both snapshots) ----
for n in 1 2; do
  run_cmd env_run bash "$BK/backup.sh" manual
  expect_rc "backup run $n exits 0" 0
  expect_out_has "backup run $n reports a snapshot" "restic_backup: snapshot "
done
expect_out_has "backup run 2 forget ran" "phase=restic_forget end rc=0"
expect_eq "two snapshots in the repository" "$(restic_cmd snapshots --json 2> /dev/null | grep -o '"short_id"' | wc -l | tr -d ' ')" "2"
expect_eq "last-run.tsv says ok" "$(cut -f3 "$STATE/last-run.tsv" 2> /dev/null)" "ok"
expect_calls_has "heartbeat ok ping was attempted (curl stubbed)" "/v1/heartbeats/notifs_ops/nightly_backup"
if [ -e "$WORKROOT/staging" ]; then _fail "staging dir removed after the snapshot" "$WORKROOT/staging still exists"; else _pass; fi

# Nothing from secrets-persistent or the master key reaches the repository.
files="$(restic_cmd ls latest --host 4irl-notifs --tag notifs 2> /dev/null)"
out=""
if grep -qE 'secrets-persistent|DELIVERY_MASTER_KEY|NOTIFS_OPS_DELIVERY_KEY' <<< "$files"; then _fail "snapshot holds no secrets-persistent files" "$files"; else _pass; fi

# No root-owned file appeared in the ntfy volume (a root-run sqlite would leave a root-owned -shm/-wal).
stray="$(docker run --rm --network none --user 0:0 -v "$NTFYVOL:/d:ro" "$SQLITE_IMAGE" find /d '!' -uid "${UIDGID%%:*}" 2> /dev/null)"
out="$stray"
expect_eq "ntfy volume holds only files of the ntfy uid" "$stray" ""

# ---- change the live data after the backup: the restore must return the snapshot, not the live state ----
psql_q delivery 'DELETE FROM messages WHERE id % 2 = 0' > /dev/null
LIVE_AFTER="$(checksums delivery)"
if [ "$LIVE_AFTER" = "$SEEDED" ]; then _fail "live data changed after the backup"; else _pass; fi

# ---- restore the database into a new database and compare ----
run_cmd env_run bash "$BK/restore.sh" db --into delivery_restore
expect_rc "restore.sh db --into delivery_restore exits 0" 0
expect_out_has "restore reports row counts" "row counts"
RESTORED="$(checksums delivery_restore)"
out=""
expect_eq "restored rows checksum-equal the seeded rows" "$RESTORED" "$SEEDED"
expect_eq "restored messages row count" "$(psql_q delivery_restore 'SELECT count(*) FROM messages')" "500"

# ---- restore auth.db to a new directory and compare ----
run_cmd env_run bash "$BK/restore.sh" ntfy-auth --out "$RESTORED_AUTH"
expect_rc "restore.sh ntfy-auth --out exits 0" 0
expect_file "restored auth.db exists" "$RESTORED_AUTH/auth.db"
AUTH_RESTORED="$(docker run --rm --network none --user "$UIDGID" -v "$RESTORED_AUTH:/r:ro" "$SQLITE_IMAGE" sqlite3 'file:/r/auth.db?mode=ro&immutable=1' 'SELECT * FROM user ORDER BY user' 2> /dev/null)"
out=""
expect_eq "restored auth.db content equals the seeded content" "$AUTH_RESTORED" "$AUTH_SEEDED"

t_finish
