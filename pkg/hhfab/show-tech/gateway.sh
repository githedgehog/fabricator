#!/bin/bash
# Copyright 2025 Hedgehog
# SPDX-License-Identifier: Apache-2.0

# show-tech.sh: Collect diagnostics from a Gateway node (OS level only).
set +e

OUTPUT_FILE="/tmp/show-tech.log"

: > "$OUTPUT_FILE"

# ---------------------------
# Basic System Information
# ---------------------------
{
    echo "=== System Information ==="
    uname -a
    cat /etc/os-release

    echo -e "\n=== Uptime ==="
    uptime

    echo -e "\n=== Hostname ==="
    hostname

    echo -e "\n=== Date/Time ==="
    date
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# Network Configuration
# ---------------------------
{
    echo -e "\n=== Network Interfaces ==="
    ip addr show

    echo -e "\n=== Routing Table ==="
    ip route show

    echo -e "\n=== ARP Table ==="
    ip neigh show
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# Disk and Memory Usage
# ---------------------------
{
    echo -e "\n=== Disk Usage ==="
    df -h

    echo -e "\n=== Memory Usage ==="
    free -h

    echo -e "\n=== Top Memory Processes ==="
    ps aux --sort=-%mem | head -n 20
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# CPU Usage and Running Processes
# ---------------------------
{
    echo -e "\n=== Top CPU Processes ==="
    ps aux --sort=-%cpu | head -n 20

    echo -e "\n=== All Running Processes ==="
    ps aux
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# System Logs
# ---------------------------
{
    echo -e "\n=== systemd-networkd logs ==="
    journalctl -u systemd-networkd --no-pager --since "1 hour ago"

    echo -e "\n=== kernel logs (last hour) ==="
    journalctl -k --no-pager --since "1 hour ago"

    echo -e "\n=== Kernel Network Logs ==="
    dmesg | grep -i "network\|bond\|vlan"
} >> "$OUTPUT_FILE" 2>&1

echo "Diagnostics collected to $OUTPUT_FILE"
