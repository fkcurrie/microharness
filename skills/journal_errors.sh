#!/usr/bin/env bash
set -e

SINCE="${1:-1h}"
echo "=== System Journal Logs (Errors & Warnings past $SINCE) ==="
journalctl -q -p 3..4 --since "$SINCE ago" --no-pager -n 50 || echo "No journalctl logs available."
