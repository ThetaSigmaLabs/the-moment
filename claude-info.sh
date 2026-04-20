#!/usr/bin/env bash
# save as: scripts/tree.sh   (or just paste into a terminal in the project root)
# Prints a tree of the project, skipping heavy/irrelevant dirs, and flagging Go test files.

set -euo pipefail

echo "=== pwd ==="
pwd
echo

echo "=== git rev (if any) ==="
git rev-parse --short HEAD 2>/dev/null || echo "(not a git repo or no commits)"
echo

echo "=== top-level ==="
ls -la
echo

echo "=== file tree (depth 4, filtered) ==="
if command -v tree >/dev/null 2>&1; then
  tree -a -L 4 \
    -I '.git|node_modules|vendor|dist|build|.idea|.vscode|*.db|*.db-*|.DS_Store|coverage*' \
    --dirsfirst
else
  # Fallback if 'tree' isn't installed
  find . \
    -path ./.git -prune -o \
    -path ./node_modules -prune -o \
    -path ./vendor -prune -o \
    -path ./dist -prune -o \
    -path ./build -prune -o \
    -type f \
    \( -name '*.db' -o -name '*.db-*' -o -name '.DS_Store' \) -prune -o \
    -print | sort | sed 's|[^/]*/|  |g'
fi
echo

echo "=== .go files (with line counts) ==="
find . -type f -name '*.go' \
  -not -path './vendor/*' -not -path './.git/*' \
  -print0 | xargs -0 wc -l | sort -k1 -n

echo
echo "=== _test.go files ==="
find . -type f -name '*_test.go' -not -path './vendor/*' | sort

echo
echo "=== templates/ css/ js/ contents ==="
for d in templates css js static web; do
  if [ -d "$d" ]; then
    echo "-- $d --"
    find "$d" -type f | sort
  fi
done

echo
echo "=== go.mod head ==="
head -20 go.mod 2>/dev/null || echo "(no go.mod)"

echo
echo "=== README/docs ==="
ls -1 README* CHANGELOG* docs/ 2>/dev/null || true