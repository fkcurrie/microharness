#!/usr/bin/env bash
set -e

echo "=== Active Listening Network Ports ==="
if command -v ss >/dev/null 2>&1; then
    ss -tulpn
elif command -v netstat >/dev/null 2>&1; then
    netstat -tulpn
else
    echo "Neither ss nor netstat found."
fi
