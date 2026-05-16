#!/usr/bin/env bash
# Creates octoprint-the-moment.zip ready to install via OctoPrint Plugin Manager.
# setup.py and octoprint_the_moment/ must be at the zip root — not inside a subfolder.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PLUGIN_DIR="$REPO_ROOT/octoprint-plugin"
OUT="$REPO_ROOT/.dev/octoprint-the-moment.zip"

rm -f "$OUT"

cd "$PLUGIN_DIR"
zip -r "$OUT" setup.py octoprint_the_moment \
    --exclude "**/__pycache__/*" \
    --exclude "**/*.pyc" \
    --exclude "**/.DS_Store"

echo "✅ Created: $OUT"
echo "   Install via OctoPrint → Settings → Plugin Manager → Upload"
