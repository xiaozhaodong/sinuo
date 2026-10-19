#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 || -z "$1" ]]; then
  printf '用法：bash scripts/release-notes.sh <版本号>\n' >&2
  exit 1
fi

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
awk -v version="$1" '
  /^## \[/ {
    if (found) exit
    heading = $0
    sub(/^## \[/, "", heading)
    sub(/\].*$/, "", heading)
    if (heading == version) { found = 1; print; next }
  }
  found { print; if ($0 ~ /[^[:space:]]/) content = 1 }
  END {
    if (!found || !content) {
      print "CHANGELOG.md 中没有版本 " version " 的非空更新说明" > "/dev/stderr"
      exit 1
    }
  }
' "$script_dir/../CHANGELOG.md"
