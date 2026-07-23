#!/usr/bin/env bash
set -e

TARGET_DIR="${1:-/var/log}"
echo "=== Disk Usage Analyzer for: $TARGET_DIR ==="
echo ""
echo "--- Top 10 Largest Directories / Files in $TARGET_DIR ---"
du -ah "$TARGET_DIR" 2>/dev/null | sort -rh | head -n 10
