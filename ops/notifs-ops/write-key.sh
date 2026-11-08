#!/usr/bin/env bash
# Atomically writes the notifs_ops delivery key to the destination path given as the only argument.
# The key arrives on stdin only: exactly KEY or KEY plus one trailing LF, KEY matching
# ^dak_[A-Za-z0-9_-]{43}$. The file holds the bare 47-character key, no newline, mode 0600.
# The key is never printed and never accepted as an argument.
set -euo pipefail
umask 077

if [ "$#" -ne 1 ]; then
  echo "usage: write-key.sh <destination-path> (key on stdin)" >&2
  exit 2
fi
dest="$1"
dest_dir="$(dirname -- "$dest")"

if [ ! -d "$dest_dir" ]; then
  echo "write-key: destination directory does not exist" >&2
  exit 1
fi

# Sentinel keeps trailing newlines that command substitution would strip.
input="$(cat; printf x)"
input="${input%x}"

key="$input"
if [ "${key: -1}" = $'\n' ]; then
  key="${key%$'\n'}"
fi

if [[ ! "$key" =~ ^dak_[A-Za-z0-9_-]{43}$ ]]; then
  echo "write-key: stdin is not a valid key (expected dak_ plus 43 characters, at most one trailing newline)" >&2
  exit 1
fi

tmp=""
cleanup() {
  if [ -n "$tmp" ]; then
    rm -f -- "$tmp"
  fi
}
trap cleanup EXIT

tmp="$(mktemp "${dest_dir}/.notifs-ops-key.XXXXXX")"
chmod 600 -- "$tmp"
printf '%s' "$key" >"$tmp"

if ! mv -f -- "$tmp" "$dest"; then
  echo "write-key: could not move the key into place" >&2
  exit 1
fi
tmp=""
