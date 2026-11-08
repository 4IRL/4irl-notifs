#!/usr/bin/env bash
# Download a pinned tool release (shellcheck, restic or cloudflared), verify the sha256 from pins.env, and install the binary.
# Shared by `make backup-lint` and lint.yml (shellcheck), unchanged by the restic CI step, and by notifs-ops-key.yml
# (cloudflared: a raw binary, amd64 only, so --arch arm64 is a usage error for it).
#
#   fetch-pinned.sh <shellcheck|restic|cloudflared> [--dest DIR] [--pins FILE] [--arch amd64|arm64]
#
# Defaults: --dest <repo>/.local/bin, --pins <this dir>/pins.env, --arch from `uname -m` (x86_64 or aarch64, anything else
# refuses). The archive (cloudflared: binary) hash is checked before anything is placed: a mismatch exits 1 and DEST is left untouched.
# Exit codes: 0 ok, 1 failed (download, hash, extract), 2 usage or bad pins.
# New in this repo (the pins file and the shared helper are not ported from the apps repo).
set -euo pipefail

usage() {
  echo "usage: fetch-pinned.sh <shellcheck|restic|cloudflared> [--dest DIR] [--pins FILE] [--arch amd64|arm64]" >&2
  exit 2
}

# BYTE-IDENTICAL with read_pins in install-backup.sh: change both together (test_install_backup.sh fails when they differ).
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

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
tool="${1:-}"
[ -n "$tool" ] || usage
shift
dest="$here/../../.local/bin"
pins="$here/pins.env"
arch=""
while [ $# -gt 0 ]; do
  case "$1" in
    --dest) [ $# -ge 2 ] || usage; dest="$2"; shift 2 ;;
    --pins) [ $# -ge 2 ] || usage; pins="$2"; shift 2 ;;
    --arch) [ $# -ge 2 ] || usage; arch="$2"; shift 2 ;;
    *) usage ;;
  esac
done
case "$tool" in shellcheck | restic | cloudflared) ;; *) usage ;; esac

if [ -z "$arch" ]; then
  case "$(uname -m)" in
    x86_64) arch=amd64 ;;
    aarch64) arch=arm64 ;;
    *) echo "fetch-pinned: unsupported architecture $(uname -m)" >&2; exit 2 ;;
  esac
fi
case "$arch" in amd64 | arm64) ;; *) echo "fetch-pinned: unsupported arch $arch" >&2; exit 2 ;; esac
if [ "$tool" = cloudflared ] && [ "$arch" != amd64 ]; then
  echo "fetch-pinned: cloudflared is pinned for amd64 only (no $arch pin)" >&2
  exit 2
fi

read_pins "$pins" || exit 2

up="$(printf '%s' "$tool" | tr '[:lower:]' '[:upper:]')"
arch_up="$(printf '%s' "$arch" | tr '[:lower:]' '[:upper:]')"
ver_var="PIN_${up}_VERSION"
sha_var="PIN_${up}_SHA256_${arch_up}"
version="${!ver_var}"
want="${!sha_var}"

case "$tool" in
  shellcheck)
    case "$arch" in amd64) sc_arch=x86_64 ;; *) sc_arch=aarch64 ;; esac
    archive="shellcheck-v${version}.linux.${sc_arch}.tar.xz"
    url="https://github.com/koalaman/shellcheck/releases/download/v${version}/${archive}"
    ;;
  restic)
    archive="restic_${version}_linux_${arch}.bz2"
    url="https://github.com/restic/restic/releases/download/v${version}/${archive}"
    ;;
  cloudflared)
    archive="cloudflared-linux-${arch}"
    url="https://github.com/cloudflare/cloudflared/releases/download/${version}/${archive}"
    ;;
esac

marker="$dest/.${tool}-${version}-${want}"
if [ -x "$dest/$tool" ] && [ -f "$marker" ]; then
  echo "already: $dest/$tool ($tool $version)"
  exit 0
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

echo "fetch-pinned: downloading $archive"
curl --fail --silent --show-error --location --connect-timeout 20 --max-time 300 --retry 3 -o "$work/$archive" "$url" \
  || { echo "fetch-pinned: download of $url failed" >&2; exit 1; }
got="$(sha256sum -- "$work/$archive" | awk '{print $1}')"
if [ "$got" != "$want" ]; then
  echo "fetch-pinned: sha256 mismatch for $archive: got ${got:-none}, want $want; nothing installed" >&2
  exit 1
fi

case "$tool" in
  shellcheck)
    tar -xJf "$work/$archive" -C "$work" --strip-components=1 "shellcheck-v${version}/shellcheck" \
      || { echo "fetch-pinned: extract failed" >&2; exit 1; }
    bin="$work/shellcheck"
    ;;
  restic)
    bunzip2 -c -- "$work/$archive" > "$work/restic" || { echo "fetch-pinned: decompress failed" >&2; exit 1; }
    bin="$work/restic"
    ;;
  cloudflared)
    bin="$work/$archive"
    ;;
esac
[ -s "$bin" ] || { echo "fetch-pinned: extracted binary is missing or empty" >&2; exit 1; }

mkdir -p "$dest"
rm -f "$dest/.${tool}-"*
install -m 0755 -- "$bin" "$dest/.$tool.new"
mv -f -- "$dest/.$tool.new" "$dest/$tool"
: > "$marker"
echo "changed: installed $dest/$tool ($tool $version)"
