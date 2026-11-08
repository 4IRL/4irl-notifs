#!/usr/bin/env bash
# Runner for the offline ops/backup bash tests: runs every test_*.sh in the test directory except the test_backup_live*.sh
# files (those need docker and restic; `make backup-test-live` runs them). Exits non-zero when any file fails.
#   run.sh [test-dir]      test-dir defaults to the directory of this script (a different one is used by the self-test)
set -uo pipefail

dir="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)}"
[ -d "$dir" ] || { echo "run.sh: no such directory: $dir" >&2; exit 2; }

files=()
while IFS= read -r f; do
  case "$(basename "$f")" in test_backup_live*.sh) continue ;; esac
  files+=("$f")
done < <(find "$dir" -maxdepth 1 -type f -name 'test_*.sh' | sort)

if [ "${#files[@]}" -eq 0 ]; then
  echo "run.sh: no test files found in $dir" >&2
  exit 2
fi

failed=0
for f in "${files[@]}"; do
  if ! bash "$f"; then
    failed=$((failed + 1))
    echo "FAILED FILE: $(basename "$f")"
  fi
done

echo "backup tests: ${#files[@]} files, $failed failed"
[ "$failed" -eq 0 ]
