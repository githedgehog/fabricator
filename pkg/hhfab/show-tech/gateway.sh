#!/bin/bash
# Copyright 2025 Hedgehog
# SPDX-License-Identifier: Apache-2.0

# show-tech.sh: Collect diagnostics from a Gateway node (OS level + FRR/vtysh + dataplane).
set +e

OUTPUT_FILE="/tmp/show-tech.log"

: > "$OUTPUT_FILE"

# Find the running FRR container ID
FRR_CONTAINER_ID=$(sudo crictl --runtime-endpoint unix:///run/k3s/containerd/containerd.sock ps \
    | grep ' frr ' \
    | awk '{print $1}')

# Find the running dataplane container ID
DATAPLANE_CONTAINER_ID=$(sudo crictl --runtime-endpoint unix:///run/k3s/containerd/containerd.sock ps \
    | grep ' dataplane ' \
    | awk '{print $1}')

# Helper for running vtysh commands inside the FRR container
run_vtysh_cmd() {
    echo -e "\n=== Executing: vtysh -c '$1' ===" >> "$OUTPUT_FILE"
    sudo crictl --runtime-endpoint unix:///run/k3s/containerd/containerd.sock exec -it "$FRR_CONTAINER_ID" vtysh -c "$1" >> "$OUTPUT_FILE" 2>&1
}

# Helper for running dataplane-cli show commands inside the dataplane container
run_dataplane_show() {
    local CMD="$1"
    echo -e "\n=== Executing: /dataplane-cli -c 'connect /tmp/dataplane_ctl.sock; $CMD' ===" >> "$OUTPUT_FILE"
    sudo crictl --runtime-endpoint unix:///run/k3s/containerd/containerd.sock exec -it "$DATAPLANE_CONTAINER_ID" /dataplane-cli -c "connect /tmp/dataplane_ctl.sock; $CMD" >> "$OUTPUT_FILE" 2>&1
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
# Dataplane CLI Diagnostics
# ---------------------------
{
    echo -e "\n=== Dataplane CLI Diagnostics ==="

    # List of all show commands to collect
    run_dataplane_show "show adjacency-table"
    run_dataplane_show "show dpdk port"
    run_dataplane_show "show dpdk port stats"
    run_dataplane_show "show evpn rmac-store"
    run_dataplane_show "show evpn vrfs"
    run_dataplane_show "show evpn vtep"
    run_dataplane_show "show interface"
    run_dataplane_show "show interface address"
    run_dataplane_show "show ip fib"
    run_dataplane_show "show ip fib group"
    run_dataplane_show "show ip next-hop"
    run_dataplane_show "show ip route"
    run_dataplane_show "show ip route summary"
    run_dataplane_show "show ipv6 fib"
    run_dataplane_show "show ipv6 fib group"
    run_dataplane_show "show ipv6 next-hop"
    run_dataplane_show "show ipv6 route"
    run_dataplane_show "show ipv6 route summary"
    run_dataplane_show "show kernel interfaces"
    run_dataplane_show "show nat port-usage"
    run_dataplane_show "show nat rules"
    run_dataplane_show "show pipeline"
    run_dataplane_show "show pipeline stages"
    run_dataplane_show "show pipeline stats"
    run_dataplane_show "show router cpi stats"
    run_dataplane_show "show router events"
    run_dataplane_show "show router frrmi stats"
    run_dataplane_show "show router frrmi last-config"
    run_dataplane_show "show tracing tag-groups"
    run_dataplane_show "show tracing targets"
    run_dataplane_show "show vpc"
    run_dataplane_show "show vpc peering interfaces"
    run_dataplane_show "show vpc peering policies"
    run_dataplane_show "show vrf"
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
