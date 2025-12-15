#!/bin/bash
# Copyright 2025 Hedgehog
# SPDX-License-Identifier: Apache-2.0

# show-tech.sh: Collect diagnostics from a Gateway node (OS level + FRR/vtysh).
set +e

OUTPUT_FILE="/tmp/show-tech.log"

: > "$OUTPUT_FILE"

# Find the running FRR container ID
FRR_CONTAINER_ID=$(sudo crictl --runtime-endpoint unix:///run/k3s/containerd/containerd.sock ps \
    | grep ' frr ' \
    | awk '{print $1}')

# Helper for running vtysh commands inside the FRR container
run_vtysh_cmd() {
    echo -e "\n=== Executing: vtysh -c '$1' ===" >> "$OUTPUT_FILE"
    sudo crictl --runtime-endpoint unix:///run/k3s/containerd/containerd.sock exec -it "$FRR_CONTAINER_ID" vtysh -c "$1" >> "$OUTPUT_FILE" 2>&1
}

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
# FRR / vtysh Diagnostics
# ---------------------------
{
    echo -e "\n=== FRR (vtysh) Diagnostics ==="

    run_vtysh_cmd "show version"
    run_vtysh_cmd "show running-config"
    run_vtysh_cmd "show bgp summary"
    run_vtysh_cmd "show bgp ipv4 unicast summary"
    run_vtysh_cmd "show bgp l2vpn evpn summary"
    run_vtysh_cmd "show bgp l2vpn evpn route"
    run_vtysh_cmd "show bgp neighbor"
    run_vtysh_cmd "show bgp vrf all summary"
    run_vtysh_cmd "show bgp vrf all neighbor"
    run_vtysh_cmd "show ip route"
    run_vtysh_cmd "show ip route vrf all"
    run_vtysh_cmd "show interface"
    run_vtysh_cmd "show logging"
    run_vtysh_cmd "show protocols"
    run_vtysh_cmd "show zebra status"
    run_vtysh_cmd "show memory"
    run_vtysh_cmd "show thread cpu"
    run_vtysh_cmd "show ip bgp"
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# Resource Pressure & Container Runtime
# ---------------------------
{
    echo -e "\n=== Kubelet Status ==="
    systemctl status k3s-agent --no-pager -l

    echo -e "\n=== Kubelet Logs (last 200 lines) ==="
    journalctl -u k3s-agent --no-pager -n 200

    echo -e "\n=== Container Runtime Stats ==="
    sudo crictl --runtime-endpoint unix:///run/k3s/containerd/containerd.sock stats --no-stream

    echo -e "\n=== Memory Pressure (PSI) ==="
    cat /proc/pressure/memory 2>/dev/null || echo "PSI not available"

    echo -e "\n=== CPU Pressure (PSI) ==="
    cat /proc/pressure/cpu 2>/dev/null || echo "PSI not available"

    echo -e "\n=== Detailed Memory Info ==="
    cat /proc/meminfo | grep -E "MemTotal|MemFree|MemAvailable|Cached|Slab|PageTables|Committed"

    echo -e "\n=== VM Stats ==="
    vmstat 1 5
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

# ---------------------------
# Extract Errors to Separate File
# ---------------------------
ERROR_FILE="/tmp/show-tech-errors.log"
: > "$ERROR_FILE"

{
    echo "ERROR AND WARNING SUMMARY"
    echo "========================="
    echo ""
    echo "Extracted from: $(hostname) at $(date)"
    echo ""

    # Extract errors and warnings from main log
    echo "=== ERRORS AND WARNINGS FROM SHOW-TECH ==="
    grep -E "ERR|FAIL|WARN|ERROR|WARNING|error|fail|failed|Failed|down|Down|Idle|Active \(Connect\)" "$OUTPUT_FILE" | head -500

    echo ""
    echo "=== SUMMARY ==="

    # Count occurrences
    err_count=$(grep -c -E "ERR|error|Error" "$OUTPUT_FILE" 2>/dev/null || echo 0)
    fail_count=$(grep -c -E "FAIL|fail|Failed" "$OUTPUT_FILE" 2>/dev/null || echo 0)
    warn_count=$(grep -c -E "WARN|warning|Warning" "$OUTPUT_FILE" 2>/dev/null || echo 0)

    echo "Total ERR messages: $err_count"
    echo "Total FAIL messages: $fail_count"
    echo "Total WARN messages: $warn_count"

    echo ""
    echo "=== BGP SESSION ISSUES ==="
    grep -E "Idle|Active \(Connect\)|down|notification|BGP.*error|neighbor.*down" "$OUTPUT_FILE" 2>/dev/null | head -30 || echo "No BGP session issues detected"

    echo ""
    echo "=== INTERFACE ISSUES ==="
    grep -E "interface.*down|link.*down|no carrier" "$OUTPUT_FILE" 2>/dev/null | head -20 || echo "No interface issues detected"

    echo ""
    echo "=== FRR ERRORS ==="
    grep -E "zebra.*error|bgpd.*error|frr.*error|failed to|cannot" "$OUTPUT_FILE" 2>/dev/null | head -30 || echo "No FRR errors detected"

    echo ""
    echo "=== MOST COMMON ERROR PATTERNS ==="

    # Find most common error patterns (top 10)
    grep -E "ERR|FAIL|error|fail|Error|Failed|Idle|down" "$OUTPUT_FILE" 2>/dev/null | \
        sed 's/[0-9][0-9]:[0-9][0-9]:[0-9][0-9]/TIME/g' | \
        sed 's/[0-9]\{1,3\}\.[0-9]\{1,3\}\.[0-9]\{1,3\}\.[0-9]\{1,3\}/IP/g' | \
        sed 's/neighbor [0-9a-f:.]*/neighbor PEER/g' | \
        sed 's/AS [0-9]*/AS ASN/g' | \
        sort | uniq -c | sort -rn | head -10 || echo "No patterns found"

} > "$ERROR_FILE" 2>&1

echo "Diagnostics collected to $OUTPUT_FILE"
echo "Errors extracted to $ERROR_FILE"
