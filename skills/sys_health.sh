#!/usr/bin/env bash
set -e

echo "=== MicroHarness System Health Snapshot ==="
echo "Date: $(date -u)"
echo ""
echo "--- CPU Load & Uptime ---"
uptime
echo ""
echo "--- Memory Usage ---"
free -h
echo ""
echo "--- Disk Usage ---"
df -h /
echo ""
echo "--- Failed Systemd Services (if any) ---"
if command -v systemctl >/dev/null 2>&1; then
    systemctl --failed --no-pager || true
else
    echo "systemctl not found"
fi
