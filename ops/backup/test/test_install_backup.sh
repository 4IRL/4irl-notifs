#!/usr/bin/env bash
# Offline tests for install-backup.sh, the pins/constants single-definition rules, the byte-identical shared readers and the
# `make host-backup*` contract (by dry run only: make -n, nothing executes). The installer runs as a child process with
# NOTIFS_BACKUP_TESTING=1, a fake root (INSTALL_BACKUP_ROOT) and PATH stubs for install, curl, bunzip2, sha256sum, systemctl,
# systemd-run, docker and uname, so no host file, service or network is touched. A failure outside this file often means a
# stub is missing from STUB_BIN: the installer then runs the real tool.
# SC2016: stub bodies are single-quoted on purpose. SC1091: lib.sh is sourced by relative path.
# SC2154: $out and $rc are set by run_cmd in lib.sh.
# shellcheck disable=SC1091,SC2016,SC2154
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=lib.sh
. "$here/lib.sh"
BK="$(cd "$here/.." && pwd)"
REPO="$(cd "$here/../../.." && pwd)"
INSTALLER="$BK/install-backup.sh"

t_init
OWNER="$(id -u):$(id -g)"
export SD="$T_TMP/sd"

pin() { sed -n "s/^$1=//p" "$BK/pins.env"; }
PIN_VER="$(pin RESTIC_VERSION)"
PIN_AMD="$(pin RESTIC_SHA256_AMD64)"
PIN_ARM="$(pin RESTIC_SHA256_ARM64)"
IMAGE="$(sed -n "s/^SQLITE_IMAGE='\(.*\)'$/\1/p" "$BK/backup.sh")"
ZERO64="0000000000000000000000000000000000000000000000000000000000000000"

mkstub() { # <name> : body from stdin; the stub logs its argv first
  local name="$1"
  {
    printf '#!/usr/bin/env bash\n'
    printf 'printf "%%s" %q >> "$CALL_LOG"\n' "$name"
    printf 'for a in "$@"; do printf " %%s" "$a" >> "$CALL_LOG"; done\nprintf "\\n" >> "$CALL_LOG"\n'
    cat
  } > "$STUB_BIN/$name"
  chmod +x "$STUB_BIN/$name"
}

CASE=0
EXTRA_ENV=()
mk_env() {
  local f
  CASE=$((CASE + 1))
  ROOT="$T_TMP/root$CASE"
  SRC="$T_TMP/src$CASE"
  rm -rf "$SD"
  mkdir -p "$ROOT" "$SRC" "$SD"
  for f in backup.sh restore.sh window.env notifs-backup.service notifs-backup.timer pins.env; do cp "$BK/$f" "$SRC/$f"; done
  printf '%s\n' "$PIN_AMD" > "$SD/sha_out"
  mkstub install <<'EOF'
mode=""; dirs=0; args=()
while [ $# -gt 0 ]; do
  case "$1" in -d) dirs=1; shift ;; -o | -g) shift 2 ;; -m) mode="$2"; shift 2 ;; --) shift ;; *) args+=("$1"); shift ;; esac
done
if [ "$dirs" = 1 ]; then
  for d in "${args[@]}"; do mkdir -p "$d"; chmod "$mode" "$d"; done
else
  cp "${args[0]}" "${args[1]}"; chmod "$mode" "${args[1]}"
fi
EOF
  mkstub curl <<'EOF'
exit 0
EOF
  mkstub sha256sum <<'EOF'
printf '%s  %s\n' "$(cat "$SD/sha_out")" "${2:--}"
EOF
  mkstub bunzip2 <<'EOF'
printf '#!/bin/sh\necho "restic 0.18.1 compiled with go1.24 on linux/amd64"\n'
EOF
  mkstub uname <<'EOF'
cat "$SD/uname_m" 2>/dev/null || echo x86_64
EOF
  mkstub systemd-run <<'EOF'
exit 0
EOF
  mkstub systemctl <<'EOF'
case "$1" in
  is-enabled) [ -e "$SD/enabled" ] ;;
  is-active) [ -e "$SD/active" ] ;;
  enable) touch "$SD/enabled" "$SD/active" ;;
  show) echo "Sun 2026-11-08 03:35:00 PST" ;;
  *) exit 0 ;;
esac
EOF
  mkstub docker <<'EOF'
case "$1" in
  inspect) [ -e "$SD/pg_down" ] && exit 1; echo true ;;
  volume) [ -e "$SD/vol_missing" ] && exit 1; exit 0 ;;
  image) [ -e "$SD/image_present" ] ;;
  pull) touch "$SD/image_present" ;;
  *) exit 0 ;;
esac
EOF
  t_reset_calls
  EXTRA_ENV=()
}

inst() {
  run_cmd "${ENVU[@]}" env NOTIFS_BACKUP_TESTING=1 INSTALL_BACKUP_ROOT="$ROOT" INSTALL_BACKUP_OWNER="$OWNER" INSTALL_BACKUP_ENV_OWNER="$OWNER" \
    ${EXTRA_ENV[@]+"${EXTRA_ENV[@]}"} bash "$INSTALLER" "$@"
}

ENV_FILE_PATH() { printf '%s' "$ROOT/home/4irl-notifs/secrets-persistent/backup.env"; }
write_env() {
  mkdir -p "$ROOT/home/4irl-notifs/secrets-persistent"
  printf 'RESTIC_REPOSITORY=s3:https://acct.r2.cloudflarestorage.com/b\nRESTIC_PASSWORD=pw\nAWS_ACCESS_KEY_ID=ak\nAWS_SECRET_ACCESS_KEY=sk\nAWS_DEFAULT_REGION=auto\nDISCORD_WEBHOOK=https://discord.example/hook\n' \
    > "$(ENV_FILE_PATH)"
  chmod 600 "$(ENV_FILE_PATH)"
}
write_last_run() { # <end offset for date -d> <result>
  mkdir -p "$ROOT/var/lib/notifs-backup/status"
  printf '%s\t%s\t%s\t%s\t%s\n' "$(date -u -d '-3 hours' +%Y-%m-%dT%H:%M:%SZ)" "$(date -u -d "$1" +%Y-%m-%dT%H:%M:%SZ)" "$2" abc12345 - \
    > "$ROOT/var/lib/notifs-backup/status/last-run.tsv"
}
write_hb_key() {
  mkdir -p "$ROOT/home/4irl-notifs/secrets-persistent"
  printf 'dak_%s' "$(head -c 43 /dev/zero | tr '\0' 'A')" > "$ROOT/home/4irl-notifs/secrets-persistent/NOTIFS_OPS_DELIVERY_KEY"
}
# good_state: a completed install plus env file, a fresh ok last-run and a well-formed heartbeat key.
good_state() {
  mk_env
  inst install --source "$SRC"
  write_env
  write_last_run '-2 hours' ok
  write_hb_key
}
OPT() { printf '%s' "$ROOT/opt/notifs-backup"; }

# ---- install: fresh host ----
mk_env
inst install --source "$SRC"
expect_rc "fresh install succeeds" 0
expect_eq "backup.sh mode" "$(stat -c '%a' "$(OPT)/backup.sh")" 755
expect_eq "restore.sh mode" "$(stat -c '%a' "$(OPT)/restore.sh")" 755
expect_eq "window.env mode" "$(stat -c '%a' "$(OPT)/window.env")" 644
expect_eq "restic mode" "$(stat -c '%a' "$ROOT/usr/local/bin/restic")" 755
expect_eq "unit mode" "$(stat -c '%a' "$ROOT/etc/systemd/system/notifs-backup.timer")" 644
expect_eq "work dir mode" "$(stat -c '%a' "$ROOT/var/lib/notifs-backup/work")" 700
expect_eq "log dir mode" "$(stat -c '%a' "$ROOT/var/log/notifs-backup")" 750
expect_file "restic.sha256 recorded" "$(OPT)/restic.sha256"
expect_calls_has "daemon-reload" "systemctl daemon-reload"
expect_calls_has "timer enabled now" "systemctl enable --now notifs-backup.timer"
expect_calls_has "installer pulls the image backup.sh names" "docker pull $IMAGE"
expect_calls_has "restic fetched with retries" "--connect-timeout 20 --max-time 300 --retry 3"
expect_calls_has "amd64 URL from pins.env" "https://github.com/restic/restic/releases/download/v$PIN_VER/restic_${PIN_VER}_linux_amd64.bz2"
expect_calls_has "archive hashed" "sha256sum -- "
if [ -e "$(ENV_FILE_PATH)" ]; then _fail "install never writes backup.env" "created $(ENV_FILE_PATH)"; else _pass; fi
expect_out_has "missing env is only a note" "note: "

# ---- install: second run changes nothing ----
write_env
t_reset_calls
inst install --source "$SRC"
expect_rc "second install succeeds" 0
nonalready="$(grep -vc '^already:' <<<"$out" || true)"
expect_eq "second install prints only already:" "$nonalready" 0
expect_calls_lacks "second install: no download" "curl "
expect_calls_lacks "second install: no pull" "docker pull"
expect_calls_lacks "second install: no enable" "systemctl enable"
expect_calls_lacks "second install: no reload" "daemon-reload"
expect_calls_lacks "second install: no file install" "install "

# ---- install: a changed timer is restarted ----
printf '# edited\n' >> "$SRC/notifs-backup.timer"
inst install --source "$SRC"
expect_rc "changed timer install" 0
expect_calls_has "changed timer restarted" "systemctl restart notifs-backup.timer"
expect_calls_has "changed unit reloads" "systemctl daemon-reload"

# ---- preconditions listed together ----
mk_env
touch "$SD/pg_down" "$SD/vol_missing"
EXTRA_ENV=(INSTALL_BACKUP_TOOLS="docker curl nonexistent-tool-xyz another-missing-tool")
inst install --source "$SRC"
expect_rc "failed preconditions exit 1" 1
expect_out_has "tool 1 listed" "missing tool: nonexistent-tool-xyz"
expect_out_has "tool 2 listed" "missing tool: another-missing-tool"
expect_out_has "postgres listed" "is not running"
expect_out_has "volume listed" "does not exist"
if [ -e "$ROOT/opt" ]; then _fail "preconditions change nothing" "$ROOT/opt exists"; else _pass; fi

# ---- checksum mismatch dies before any host change ----
mk_env
printf '%s\n' "$ZERO64" > "$SD/sha_out"
inst install --source "$SRC"
expect_rc "checksum mismatch exits 1" 1
expect_out_has "mismatch named" "sha256 mismatch"
expect_calls_lacks "mismatch: nothing installed" "install "
expect_calls_lacks "mismatch: no systemctl" "systemctl"
expect_calls_lacks "mismatch: no pull" "docker pull"
if [ -e "$ROOT/opt" ] || [ -e "$ROOT/usr" ] || [ -e "$ROOT/etc" ]; then _fail "mismatch changes no host path" "files under $ROOT"; else _pass; fi

# ---- unsupported architecture ----
mk_env
printf 'armv7l\n' > "$SD/uname_m"
inst install --source "$SRC"
expect_rc "unsupported uname -m refused" 1
expect_out_has "arch named" "unsupported architecture armv7l"
expect_calls_lacks "unsupported arch: no download" "curl "
if [ -e "$ROOT/opt" ] || [ -e "$ROOT/usr" ]; then _fail "unsupported arch changes nothing" "files under $ROOT"; else _pass; fi

# ---- arm64 pins: URL and hash ----
mk_env
printf 'aarch64\n' > "$SD/uname_m"
printf '%s\n' "$PIN_ARM" > "$SD/sha_out"
inst install --source "$SRC"
expect_rc "aarch64 install with the arm64 pin hash" 0
expect_calls_has "arm64 URL from pins.env" "restic_${PIN_VER}_linux_arm64.bz2"
mk_env
printf 'aarch64\n' > "$SD/uname_m"
printf '%s\n' "$PIN_AMD" > "$SD/sha_out"
inst install --source "$SRC"
expect_rc "aarch64 install with the amd64 hash is refused" 1

# ---- pins are read from the --source file, not a copy ----
mk_env
newhash="1111111111111111111111111111111111111111111111111111111111111111"
sed -i -e "s/^RESTIC_VERSION=.*/RESTIC_VERSION=9.9.9/" -e "s/^RESTIC_SHA256_AMD64=.*/RESTIC_SHA256_AMD64=$newhash/" "$SRC/pins.env"
printf '%s\n' "$newhash" > "$SD/sha_out"
inst install --source "$SRC"
expect_rc "install with changed pins.env" 0
expect_calls_has "changed pins.env changes the fetched URL" "v9.9.9/restic_9.9.9_linux_amd64.bz2"

# ---- verify ----
good_state
inst verify --source "$SRC"
expect_rc "verify passes on a good install" 0

# backup.env is judged against ENV_OWNER (the 4irl-notifs user), not OWNER: an env owner that differs fails verify.
EXTRA_ENV=(INSTALL_BACKUP_ENV_OWNER=12345:12345)
inst verify --source "$SRC"
expect_rc "verify fails when backup.env is not owned by the env owner" 1
expect_out_has "env owner mismatch is named" "owner is not 12345:12345"
EXTRA_ENV=(INSTALL_BACKUP_ENV_OWNER=abc)
inst verify --source "$SRC"
expect_rc "a malformed INSTALL_BACKUP_ENV_OWNER exits 2" 2
expect_out_has "malformed env owner message" "cannot resolve the uid:gid of user 4irl-notifs"
EXTRA_ENV=()

# No override and an unresolvable 4irl-notifs user: fail closed (exit 2), never fall back to another owner.
mkdir -p "$ROOT/shimbin"
printf '#!/bin/sh\nexit 1\n' > "$ROOT/shimbin/id"
chmod 755 "$ROOT/shimbin/id"
run_cmd "${ENVU[@]}" env PATH="$ROOT/shimbin:$PATH" NOTIFS_BACKUP_TESTING=1 INSTALL_BACKUP_ROOT="$ROOT" INSTALL_BACKUP_OWNER="$OWNER" \
  bash "$INSTALLER" verify --source "$SRC"
expect_rc "unresolvable 4irl-notifs user exits 2" 2
expect_out_has "unresolvable user is named" "cannot resolve the uid:gid of user 4irl-notifs"

# A resolvable user supplies the default env owner.
printf '#!/bin/sh\nif [ "$1" = "-u" ]; then echo 12345; else echo 12346; fi\n' > "$ROOT/shimbin/id"
run_cmd "${ENVU[@]}" env PATH="$ROOT/shimbin:$PATH" NOTIFS_BACKUP_TESTING=1 INSTALL_BACKUP_ROOT="$ROOT" INSTALL_BACKUP_OWNER="$OWNER" \
  bash "$INSTALLER" verify --source "$SRC"
expect_rc "default env owner comes from the 4irl-notifs uid:gid" 1
expect_out_has "default env owner is the resolved uid:gid" "owner is not 12345:12346"
rm -rf "$ROOT/shimbin"

good_state
printf '# drift\n' >> "$(OPT)/backup.sh"
inst verify --source "$SRC"
expect_rc "verify fails on payload drift" 1
expect_out_has "drift named" "differs from the repo copy"

good_state
chmod 0777 "$(OPT)/backup.sh"
inst verify --source "$SRC"
expect_rc "verify fails on a wrong mode" 1

good_state
write_last_run '-40 hours' ok
inst verify --source "$SRC"
expect_rc "verify fails on a stale last-run" 1

good_state
write_last_run '-2 hours' failed
inst verify --source "$SRC"
expect_rc "verify fails on a failed last-run" 1

good_state
rm -f "$ROOT/var/lib/notifs-backup/status/last-run.tsv"
inst verify --source "$SRC"
expect_rc "verify fails on a missing last-run" 1

good_state
rm -f "$SD/image_present"
inst verify --source "$SRC"
expect_rc "verify fails on a missing sqlite image" 1
expect_out_has "image named" "is not present locally"

good_state
rm -f "$SD/enabled"
inst verify --source "$SRC"
expect_rc "verify fails on a disabled timer" 1

good_state
sed -i 's/^TimeoutStartSec=.*/TimeoutStartSec=9000/' "$SRC/notifs-backup.service" "$ROOT/etc/systemd/system/notifs-backup.service"
inst verify --source "$SRC"
expect_rc "verify fails when the window arithmetic overruns" 1
expect_out_has "window named" "window:"

good_state
sed -i '/^AWS_ACCESS_KEY_ID=/d' "$(ENV_FILE_PATH)"
inst verify --source "$SRC"
expect_rc "verify fails on a backup.env missing a key" 1

good_state
chmod 0644 "$(ENV_FILE_PATH)"
inst verify --source "$SRC"
expect_rc "verify fails on a backup.env with the wrong mode" 1

good_state
rm -f "$ROOT/home/4irl-notifs/secrets-persistent/NOTIFS_OPS_DELIVERY_KEY"
inst verify --source "$SRC"
expect_rc "missing heartbeat key is only a WARN" 0
expect_out_has "WARN printed" "WARN: "

good_state
printf '\n' >> "$ROOT/home/4irl-notifs/secrets-persistent/NOTIFS_OPS_DELIVERY_KEY"
inst verify --source "$SRC"
expect_rc "heartbeat key with a trailing newline is only a WARN" 0
expect_out_has "malformed key WARN" "malformed"

# ---- init-repo: only on restic cat config rc 10 ----
good_state
cat > "$ROOT/usr/local/bin/restic" <<'EOF'
#!/usr/bin/env bash
echo "restic $*" >> "$CALL_LOG"
[ "$1" = cat ] && exit "${RESTIC_CAT_RC:-0}"
exit 0
EOF
chmod 755 "$ROOT/usr/local/bin/restic"
for crc in 0 1 11; do
  t_reset_calls
  EXTRA_ENV=("RESTIC_CAT_RC=$crc")
  inst init-repo
  expect_rc "init-repo refuses on rc $crc" 1
  expect_calls_lacks "no restic init on rc $crc" "restic init"
done
t_reset_calls
EXTRA_ENV=(RESTIC_CAT_RC=10)
inst init-repo
expect_rc "init-repo runs on rc 10" 0
expect_calls_has "restic init on rc 10" "restic init"
EXTRA_ENV=()

# ---- run-now ----
good_state
t_reset_calls
inst run-now
expect_rc "run-now succeeds" 0
expect_calls_has "run-now waits on the unit" "systemd-run --wait --pipe"
expect_calls_has "run-now runs manual mode" "/opt/notifs-backup/backup.sh manual"
props="$(bash -c '. "$1"; printf "%s\n" "${RUN_PROPS[@]}"' _ "$INSTALLER")"
svc="$BK/notifs-backup.service"
missing=""
while IFS= read -r p; do
  grep -qxF -- "$p" "$svc" || missing="$missing [$p]"
done <<<"$props"
expect_eq "every run-now property is a line of the unit" "$missing" ""
extra=""
while IFS= read -r line; do
  case "$line" in '' | '#'* | '['* | Description=* | ExecStart=*) continue ;; esac
  grep -qxF -- "$line" <<<"$props" || extra="$extra [$line]"
done < "$svc"
expect_eq "every unit property is passed by run-now" "$extra" ""
expect_calls_has "run-now passes the unit's timeout" "-p TimeoutStartSec=4500"

# ---- static: shared env reader and read_pins are byte-identical ----
reader() { sed -n '/^# BEGIN shared-env-reader$/,/^# END shared-env-reader$/p' "$1"; }
pinsfn() { sed -n '/^read_pins() {$/,/^}$/p' "$1"; }
r_backup="$(reader "$BK/backup.sh")"
expect_eq "env reader block is non-trivial" "$([ "$(wc -l <<<"$r_backup")" -gt 25 ] && echo yes || echo no)" yes
expect_eq "env reader identical in restore.sh" "$(reader "$BK/restore.sh")" "$r_backup"
expect_eq "env reader identical in install-backup.sh" "$(reader "$INSTALLER")" "$r_backup"
mutated="$T_TMP/mutated.sh"
sed 's/ENV_READ_ERROR="env file is a symlink"/ENV_READ_ERROR="changed"/' "$INSTALLER" > "$mutated"
if [ "$(reader "$mutated")" = "$r_backup" ]; then _fail "positive control: a mutated reader must differ"; else _pass; fi
p_fetch="$(pinsfn "$BK/fetch-pinned.sh")"
expect_eq "read_pins block is non-trivial" "$([ "$(wc -l <<<"$p_fetch")" -gt 15 ] && echo yes || echo no)" yes
expect_eq "read_pins identical in install-backup.sh" "$(pinsfn "$INSTALLER")" "$p_fetch"
sed 's/pins: unknown key/pins: changed/' "$INSTALLER" > "$mutated"
if [ "$(pinsfn "$mutated")" = "$p_fetch" ]; then _fail "positive control: a mutated read_pins must differ"; else _pass; fi

# ---- static: single definitions ----
# pin_literal_files <root>...: non-test files under the roots that carry a restic/shellcheck/cloudflared version or hash from pins.env.
pin_literal_files() {
  local -a lits=()
  local k v root
  for k in SHELLCHECK_VERSION SHELLCHECK_SHA256_AMD64 SHELLCHECK_SHA256_ARM64 RESTIC_VERSION RESTIC_SHA256_AMD64 RESTIC_SHA256_ARM64 CLOUDFLARED_VERSION CLOUDFLARED_SHA256_AMD64; do
    v="$(pin "$k")"
    lits+=(-e "$v")
  done
  for root in "$@"; do
    [ -e "$root" ] || continue
    grep -rlF "${lits[@]}" --exclude=pins.env --exclude-dir=test --exclude-dir=node_modules --exclude-dir=.local "$root" || true
  done
}
found="$(pin_literal_files "$BK" "$REPO/.github" "$REPO/Makefile" "$REPO/ops/notifs-ops")"
expect_eq "no restic, shellcheck or cloudflared pin literal outside pins.env" "$found" ""
plant="$T_TMP/plant"
mkdir -p "$plant"
printf 'RESTIC=%s\n' "$PIN_AMD" > "$plant/dup.txt"
planted="$(pin_literal_files "$plant")"
if [ -z "$planted" ]; then _fail "positive control: a planted pin literal must be found"; else _pass; fi
for k in SHELLCHECK_VERSION SHELLCHECK_SHA256_AMD64 SHELLCHECK_SHA256_ARM64 RESTIC_VERSION RESTIC_SHA256_AMD64 RESTIC_SHA256_ARM64 CLOUDFLARED_VERSION CLOUDFLARED_SHA256_AMD64; do
  expect_eq "pins.env defines $k once" "$(grep -c "^$k=" "$BK/pins.env")" 1
done

nontest_defs() { grep -rE "^$1=" --include='*.sh' --exclude-dir=test "$BK" || true; }
for k in SQLITE_IMAGE PG_CONTAINER NTFY_VOLUME; do
  expect_eq "$k defined exactly once under ops/backup" "$(nontest_defs "$k" | wc -l | tr -d ' ')" 1
  expect_eq "$k defined in backup.sh" "$(nontest_defs "$k" | grep -c 'backup\.sh:' || true)" 1
done
expect_eq "sqlite image repository only in backup.sh" "$(grep -rlF 'keinos/sqlite3' --include='*.sh' --exclude-dir=test "$BK" | sed 's|.*/||')" backup.sh
expect_eq "postgres container literal only in backup.sh" \
  "$(grep -rlF '4irl-notifs-delivery-postgres' --include='*.sh' --exclude-dir=test "$BK" | sed 's|.*/||')" backup.sh
expect_eq "ntfy-auth volume literal only in backup.sh" \
  "$(grep -rlF '4irl-notifs_ntfy-auth' --include='*.sh' --exclude-dir=test "$BK" | sed 's|.*/||')" backup.sh

# ---- make contract, by dry run ----
# make_contract_ok <makefile>: succeeds only when every host-backup / host-backup-status guarantee holds for that Makefile.
make_contract_ok() {
  local mf="$1" o
  mk() { make -n -C "$REPO" -f "$mf" "$@" 2>&1; }
  o="$(mk host-backup ARGS=install)" && return 1
  grep -qF HOST <<<"$o" || return 1
  grep -qF 'scp' <<<"$o" && return 1
  o="$(mk host-backup-status)" && return 1
  grep -qF HOST <<<"$o" || return 1
  o="$(HOST=envhost make -n -C "$REPO" -f "$mf" host-backup ARGS=install 2>&1)" && return 1
  o="$(mk host-backup HOST=h ARGS=bogus)" && return 1
  o="$(mk host-backup HOST=h ARGS=install)" || return 1
  grep -qF 'scp -r ops/backup' <<<"$o" || return 1
  grep -qF 'ssh -t h sudo bash' <<<"$o" || return 1
  grep -qF 'install-backup.sh install --source' <<<"$o" || return 1
  grep -qE "trap '[^']*rm -rf[^']*' EXIT" <<<"$o" || return 1
  o="$(mk host-backup-status HOST=h)" || return 1
  grep -qF sudo <<<"$o" && return 1
  grep -qE ' -t( |$)' <<<"$o" && return 1
  grep -qF 'last-run.tsv' <<<"$o" || return 1
  grep -qF 'list-timers notifs-backup.timer' <<<"$o" || return 1
  return 0
}
if make_contract_ok "$REPO/Makefile"; then _pass; else _fail "the real Makefile satisfies the host-backup contract"; fi
broken="$T_TMP/Makefile.notrap"
sed "s/trap '[^']*' EXIT && //" "$REPO/Makefile" > "$broken"
if make_contract_ok "$broken"; then _fail "positive control: a Makefile without the trap must fail the contract"; else _pass; fi
broken="$T_TMP/Makefile.notty"
sed 's/ssh -t /ssh /' "$REPO/Makefile" > "$broken"
if make_contract_ok "$broken"; then _fail "positive control: a Makefile without ssh -t must fail the contract"; else _pass; fi
broken="$T_TMP/Makefile.sudostatus"
sed "s|ssh \$(HOST) 'cat /var/lib|ssh -t \$(HOST) 'sudo cat /var/lib|" "$REPO/Makefile" > "$broken"
if make_contract_ok "$broken"; then _fail "positive control: a status target with sudo must fail the contract"; else _pass; fi

t_finish
