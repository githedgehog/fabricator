#!/bin/bash
# Copyright 2025 Hedgehog
# SPDX-License-Identifier: Apache-2.0

# show-tech.sh: Collect diagnostics from a Gateway node (OS level + FRR/vtysh).
set +e

OUTPUT_FILE="/tmp/show-tech.log"

: > "$OUTPUT_FILE"

# Suppress crictl config file warnings by pointing to /dev/null
export CRI_CONFIG_FILE=/dev/null

# Find the running FRR container ID
FRR_CONTAINER_ID=$(sudo -E crictl --runtime-endpoint unix:///run/k3s/containerd/containerd.sock ps \
    | grep ' frr ' \
    | awk '{print $1}')

# Helper for running vtysh commands inside the FRR container
run_vtysh_cmd() {
    echo -e "\n=== Executing: vtysh -c '$1' ===" >> "$OUTPUT_FILE"
    sudo -E crictl --runtime-endpoint unix:///run/k3s/containerd/containerd.sock exec "$FRR_CONTAINER_ID" vtysh -c "$1" >> "$OUTPUT_FILE" 2>&1
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
