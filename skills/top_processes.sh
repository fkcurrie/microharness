#!/usr/bin/env bash
set -e

echo "=== Top Memory & CPU Consuming Processes ==="
echo ""
echo "--- Top 5 CPU Consuming Processes ---"
ps aux --sort=-%cpu | head -n 6
echo ""
echo "--- Top 5 Memory Consuming Processes ---"
ps aux --sort=-%mem | head -n 6
