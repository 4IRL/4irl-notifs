#!/usr/bin/env bash
# fetch-pinned.sh: a hash match installs the binary, a mismatch exits non-zero and installs nothing (shellcheck and restic
# alike), bad pins are refused, and every pin key is defined exactly once under ops/backup and the workflows.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=lib.sh
. "$here/lib.sh"
t_init

fetch="$here/../fetch-pinned.sh"
root="$(cd "$here/../../.." && pwd)"

hash_a="$(printf 'a%.0s' {1..64})"
hash_b="$(printf 'b%.0s' {1..64})"
hash_c="$(printf 'c%.0s' {1..64})"

pins="$T_TMP/pins.env"
{
  echo "# test pins"
  echo "SHELLCHECK_VERSION=1.2.3"
  echo "SHELLCHECK_SHA256_AMD64=$hash_a"
  echo "SHELLCHECK_SHA256_ARM64=$hash_b"
  echo "RESTIC_VERSION=4.5.6"
  echo "RESTIC_SHA256_AMD64=$hash_a"
  echo "RESTIC_SHA256_ARM64=$hash_c"
  echo "CLOUDFLARED_VERSION=7.8.9"
  echo "CLOUDFLARED_SHA256_AMD64=$hash_b"
} > "$pins"

# Fake release archives. The curl stub copies the right one to the -o path; the sha256sum stub reports $STUB_SHA.
arch_dir="$T_TMP/archives"
mkdir -p "$arch_dir/shellcheck-v1.2.3"
printf '#!/bin/sh\necho fake-shellcheck\n' > "$arch_dir/shellcheck-v1.2.3/shellcheck"
tar -cJf "$arch_dir/sc.tar.xz" -C "$arch_dir" shellcheck-v1.2.3
printf '#!/bin/sh\necho fake-restic\n' | bzip2 -c > "$arch_dir/restic.bz2"
printf '#!/bin/sh\necho fake-cloudflared\n' > "$arch_dir/cloudflared"

# shellcheck disable=SC2016
{
  printf '#!/usr/bin/env bash\n'
  printf 'printf "curl" >> "$CALL_LOG"; for a in "$@"; do printf " %%s" "$a" >> "$CALL_LOG"; done; printf "\\n" >> "$CALL_LOG"\n'
  printf 'out=""; prev=""\n'
  printf 'for a in "$@"; do [ "$prev" = "-o" ] && out="$a"; prev="$a"; done\n'
  printf 'case "$*" in *shellcheck*) cp "%s/sc.tar.xz" "$out" ;; *cloudflared*) cp "%s/cloudflared" "$out" ;; *) cp "%s/restic.bz2" "$out" ;; esac\n' "$arch_dir" "$arch_dir" "$arch_dir"
} > "$STUB_BIN/curl"
chmod +x "$STUB_BIN/curl"
# shellcheck disable=SC2016
{
  printf '#!/usr/bin/env bash\n'
  printf 'printf "sha256sum" >> "$CALL_LOG"; for a in "$@"; do printf " %%s" "$a" >> "$CALL_LOG"; done; printf "\\n" >> "$CALL_LOG"\n'
  printf 'printf "%%s  %%s\\n" "$STUB_SHA" "${2:-$1}"\n'
} > "$STUB_BIN/sha256sum"
chmod +x "$STUB_BIN/sha256sum"

# --- match installs ---
dest="$T_TMP/dest-sc"
run_cmd env STUB_SHA="$hash_a" bash "$fetch" shellcheck --dest "$dest" --pins "$pins" --arch amd64
expect_rc "shellcheck: matching hash installs" 0
expect_file "shellcheck: binary placed" "$dest/shellcheck"
expect_calls_has "shellcheck: amd64 archive requested" "shellcheck-v1.2.3.linux.x86_64.tar.xz"
expect_calls_has "shellcheck: curl uses the pinned timeouts" "--connect-timeout 20 --max-time 300 --retry 3"
run_cmd "$dest/shellcheck"
expect_out_has "shellcheck: installed binary is the extracted one" "fake-shellcheck"
t_reset_calls
run_cmd env STUB_SHA="$hash_a" bash "$fetch" shellcheck --dest "$dest" --pins "$pins" --arch amd64
expect_rc "shellcheck: second run succeeds" 0
expect_out_has "shellcheck: second run is a no-op" "already:"
expect_calls_lacks "shellcheck: second run does not download" "curl"

dest="$T_TMP/dest-restic"
run_cmd env STUB_SHA="$hash_c" bash "$fetch" restic --dest "$dest" --pins "$pins" --arch arm64
expect_rc "restic: matching arm64 hash installs" 0
expect_calls_has "restic: arm64 archive requested" "restic_4.5.6_linux_arm64.bz2"
run_cmd "$dest/restic"
expect_out_has "restic: installed binary is the decompressed one" "fake-restic"

dest="$T_TMP/dest-cf"
run_cmd env STUB_SHA="$hash_b" bash "$fetch" cloudflared --dest "$dest" --pins "$pins" --arch amd64
expect_rc "cloudflared: matching hash installs" 0
expect_calls_has "cloudflared: the pinned tag and raw amd64 asset are requested" "https://github.com/cloudflare/cloudflared/releases/download/7.8.9/cloudflared-linux-amd64"
expect_calls_has "cloudflared: curl fails on HTTP errors and has a time bound" "--fail --silent --show-error --location --connect-timeout 20 --max-time 300"
expect_calls_lacks "cloudflared: never the moving latest URL" "latest"
run_cmd "$dest/cloudflared"
expect_out_has "cloudflared: installed binary is the downloaded one" "fake-cloudflared"
dest="$T_TMP/dest-cf-bad"
run_cmd env STUB_SHA="$hash_a" bash "$fetch" cloudflared --dest "$dest" --pins "$pins" --arch amd64
expect_rc "cloudflared: hash mismatch exits non-zero" 1
expect_out_has "cloudflared: mismatch is named" "sha256 mismatch"
expect_eq "cloudflared: nothing installed on mismatch" "$([ -e "$dest" ] && echo present || echo absent)" "absent"
run_cmd env STUB_SHA="$hash_b" bash "$fetch" cloudflared --dest "$T_TMP/x" --pins "$pins" --arch arm64
expect_rc "cloudflared: arm64 has no pin and is refused" 2

# --- mismatch refuses and installs nothing ---
for tool in shellcheck restic; do
  dest="$T_TMP/dest-bad-$tool"
  run_cmd env STUB_SHA="$hash_b" bash "$fetch" "$tool" --dest "$dest" --pins "$pins" --arch amd64
  expect_rc "$tool: hash mismatch exits non-zero" 1
  expect_out_has "$tool: mismatch is named" "sha256 mismatch"
  expect_eq "$tool: nothing installed on mismatch" "$([ -e "$dest" ] && echo present || echo absent)" "absent"
done

# A pre-existing binary survives a mismatching re-fetch of a different pin.
dest="$T_TMP/dest-keep"
mkdir -p "$dest"
printf 'old\n' > "$dest/restic"
run_cmd env STUB_SHA="$hash_b" bash "$fetch" restic --dest "$dest" --pins "$pins" --arch amd64
expect_rc "restic: mismatch over an existing binary fails" 1
expect_eq "restic: existing binary untouched" "$(cat "$dest/restic")" "old"

# --- bad input ---
run_cmd bash "$fetch" cosign --dest "$T_TMP/x" --pins "$pins" --arch amd64
expect_rc "unknown tool is a usage error" 2
run_cmd bash "$fetch" restic --dest "$T_TMP/x" --pins "$pins" --arch riscv
expect_rc "unknown arch is refused" 2

bad="$T_TMP/bad.env"
grep -v '^RESTIC_VERSION' "$pins" > "$bad"
run_cmd bash "$fetch" restic --dest "$T_TMP/x" --pins "$bad" --arch amd64
expect_rc "missing pin key is refused" 2
expect_out_has "missing key is named" "missing key RESTIC_VERSION"
{ cat "$pins"; echo "RESTIC_VERSION=4.5.7"; } > "$bad"
run_cmd bash "$fetch" restic --dest "$T_TMP/x" --pins "$bad" --arch amd64
expect_rc "duplicate pin key is refused" 2
{ cat "$pins"; echo "EXTRA=1"; } > "$bad"
run_cmd bash "$fetch" restic --dest "$T_TMP/x" --pins "$bad" --arch amd64
expect_rc "unknown pin key is refused" 2
sed 's/^RESTIC_VERSION=4.5.6/RESTIC_VERSION="4.5.6"/' "$pins" > "$bad"
run_cmd bash "$fetch" restic --dest "$T_TMP/x" --pins "$bad" --arch amd64
expect_rc "quoted pin value is refused" 2
sed 's/$/\r/' "$pins" > "$bad"
run_cmd bash "$fetch" restic --dest "$T_TMP/x" --pins "$bad" --arch amd64
expect_rc "CR in pins is refused" 2
sed "s/^RESTIC_SHA256_AMD64=.*/RESTIC_SHA256_AMD64=nothex/" "$pins" > "$bad"
run_cmd bash "$fetch" restic --dest "$T_TMP/x" --pins "$bad" --arch amd64
expect_rc "malformed hash is refused" 2

# --- the real pins file ---
for key in SHELLCHECK_VERSION SHELLCHECK_SHA256_AMD64 SHELLCHECK_SHA256_ARM64 RESTIC_VERSION RESTIC_SHA256_AMD64 RESTIC_SHA256_ARM64 CLOUDFLARED_VERSION CLOUDFLARED_SHA256_AMD64; do
  n="$( (grep -rhE "^${key}=" "$root/ops/backup" || true) | wc -l)"
  w="$( (grep -rhE "^[[:space:]]*${key}[[:space:]]*[:=]" "$root/.github/workflows" || true) | wc -l)"
  expect_eq "$key is defined exactly once under ops/backup" "$n" "1"
  expect_eq "$key is not defined in a workflow" "$w" "0"
done
expect_file "pins.env exists" "$root/ops/backup/pins.env"
run_cmd bash -c "grep -E '^[A-Z0-9_]+=' '$root/ops/backup/pins.env' | wc -l"
expect_eq "pins.env has exactly eight keys" "$out" "8"
# Every real hash literal appears only in pins.env.
while IFS='=' read -r key val; do
  case "$key" in *SHA256*)
    hits="$(grep -rlF -- "$val" "$root/ops/backup" "$root/.github" "$root/Makefile" 2>/dev/null | wc -l)"
    expect_eq "$key literal appears in exactly one file" "$hits" "1"
    ;;
  esac
done < <(grep -E '^[A-Z0-9_]+=' "$root/ops/backup/pins.env")

t_finish
