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
FRR_CONTAINER_ID=$(sudo -E crictl --runtime-endpoint unix:///run/k3s/containerd/containerd.sock ps --name '^frr$' -q 2>>"$OUTPUT_FILE" | head -1)

# Find the running dataplane container ID
DATAPLANE_CONTAINER_ID=$(sudo -E crictl --runtime-endpoint unix:///run/k3s/containerd/containerd.sock ps -q --name '^dataplane$' 2>>"$OUTPUT_FILE" | head -1)

# Helper for running vtysh commands inside the FRR container
run_vtysh_cmd() {
    echo -e "\n=== Executing: vtysh -c '$1' ===" >> "$OUTPUT_FILE"
    sudo -E crictl --runtime-endpoint unix:///run/k3s/containerd/containerd.sock exec "$FRR_CONTAINER_ID" vtysh -X /lib/libvtysh_hedgehog.so -c "$1" >> "$OUTPUT_FILE" 2>&1
}

# Helper for running dataplane-cli commands inside the dataplane container
run_dp_cmd() {
    echo -e "\n=== Executing: dataplane-cli -c '$1' ===" >> "$OUTPUT_FILE"
    sudo -E crictl --runtime-endpoint unix:///run/k3s/containerd/containerd.sock exec "$DATAPLANE_CONTAINER_ID" \
        /dataplane-cli -c "$1" >> "$OUTPUT_FILE" 2>&1
}

# `crictl logs` only reads the current on-disk log file for a container. Kubelet
# rotates that file once it grows past its size threshold, renaming the full file
# to a sibling `<name>.<timestamp>[.gz]` and starting a fresh one - so on a long
# test run, `crictl logs` alone can silently miss everything before the last
# rotation. This reads the rotated siblings (oldest first) plus the current file,
# so the full run is covered. Falls back to `crictl logs` if the log path can't
# be resolved.
capture_container_logs() {
    local container_id="$1"
    local log_path
    log_path=$(sudo -E crictl --runtime-endpoint unix:///run/k3s/containerd/containerd.sock inspect -o json "$container_id" 2>/dev/null | jq -r '.status.logPath // empty')

    if [ -z "$log_path" ] || ! sudo test -e "$log_path"; then
        sudo -E crictl --runtime-endpoint unix:///run/k3s/containerd/containerd.sock logs "$container_id"
        return
    fi

    # rotated siblings are named <log_path>.<YYYYMMDD-HHMMSS>[.gz], so a lexical
    # sort is also chronological; print those first, then the current file.
    # Capped to the 20 most recent rotations: the file's own mtime is when
    # gzip compression finished, not when its content was written (observed
    # lagging the embedded rotation timestamp by hours), so it can't be used
    # to bound this by age. Capping by count instead keeps this sane on a
    # long-lived environment where the container may have been running for
    # days, while still covering well beyond any single CI job's runtime.
    while IFS= read -r rotated; do
        [ -n "$rotated" ] || continue
        if [[ "$rotated" == *.gz ]]; then
            sudo -E zcat "$rotated" 2>/dev/null
        else
            sudo -E cat "$rotated" 2>/dev/null
        fi
    done < <(sudo -E find "$(dirname "$log_path")" -maxdepth 1 -name "$(basename "$log_path").*" 2>/dev/null | sort | tail -n 20)
    sudo -E cat "$log_path" 2>/dev/null
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

    echo -e "\n=== VRF Devices ==="
    ip -br link show type vrf

    echo -e "\n=== Routing Tables (all, incl. per-VRF) ==="
    ip route show table all

    echo -e "\n=== Link Status ==="
    ip link show

    echo -e "\n=== Interface Statistics ==="
    ip -s link show

    # Without driver counters, RX-ring and queue drops on the dataplane uplink
    # are unobservable -- ip -s link alone cannot attribute them.
    echo -e "\n=== NIC Statistics (ethtool -S, nonzero only) ==="
    for nic in $(ls /sys/class/net | grep -E '^(enp|eth)'); do
        echo -e "\n--- $nic ---"
        nic_stats=$(ethtool -S "$nic" 2>/dev/null | awk -F: 'NF==2 && $2+0 != 0 {print}')
        if [ -n "$nic_stats" ]; then
            echo "$nic_stats"
        elif ethtool -S "$nic" &>/dev/null; then
            echo "(all counters zero)"
        else
            echo "Could not retrieve ethtool -S data for $nic"
        fi
    done

    echo -e "\n=== Protocol Counters (netstat -s) ==="
    if command -v netstat &>/dev/null; then
        netstat -s 2>/dev/null
    else
        echo "netstat unavailable; raw counters follow"
        cat /proc/net/snmp 2>/dev/null
    fi
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
    run_vtysh_cmd "show hedgehog plugin version"
    run_vtysh_cmd "show hedgehog rpc stats"
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# FRR Container Logs
# ---------------------------
{
    echo -e "\n=== FRR Container Logs ==="
    if [ -n "$FRR_CONTAINER_ID" ]; then
        capture_container_logs "$FRR_CONTAINER_ID"
    else
        echo "FRR container not found - skipping container logs"
    fi
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# Dataplane Diagnostics
# ---------------------------
{
    echo -e "\n=== Dataplane Diagnostics ==="
    if [ -z "$DATAPLANE_CONTAINER_ID" ]; then
        echo "Dataplane container not found - skipping dataplane diagnostics"
    else
        run_dp_cmd "show tech"
    fi
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# Dataplane Metrics
# ---------------------------
{
    echo -e "\n=== Dataplane Metrics ==="
    # "show tech" reports drop counters by reason, but aggregated across the whole gateway, so it
    # cannot say which VPC pair or which direction a drop belongs to. The dataplane's own metrics
    # endpoint carries the complement: per-VPC-pair, directional packet and drop counters
    # (vpc_pair_drops_packet_count{from=...,to=...}), with no reason attached. Attributing a
    # single-packet loss needs both halves, so collect both.
    #
    # The endpoint listens on the gateway's own loopback, so this scrape runs here on the host,
    # not inside the dataplane container. Read the address off the dataplane process's own cmdline
    # rather than hardcoding it, so a change to --metrics-address does not silently produce an
    # empty section.
    if [ -z "$DATAPLANE_CONTAINER_ID" ]; then
        echo "Dataplane container not found - skipping metrics scrape"
    else
        DATAPLANE_PID=$(sudo -E crictl --runtime-endpoint unix:///run/k3s/containerd/containerd.sock \
            inspect -o go-template --template '{{.info.pid}}' "$DATAPLANE_CONTAINER_ID" 2>>"$OUTPUT_FILE")
        METRICS_ADDR=""
        if [ -n "$DATAPLANE_PID" ] && [ -r "/proc/$DATAPLANE_PID/cmdline" ]; then
            METRICS_ADDR=$(tr '\0' '\n' < "/proc/$DATAPLANE_PID/cmdline" | grep -A1 -m1 -x -- '--metrics-address' | tail -1)
            if [ -z "$METRICS_ADDR" ]; then
                METRICS_ADDR=$(tr '\0' '\n' < "/proc/$DATAPLANE_PID/cmdline" | grep -m1 -oE '^--metrics-address=(.+)$' | cut -d= -f2-)
            fi
        fi
        if [ -z "$METRICS_ADDR" ]; then
            METRICS_ADDR="127.0.0.1:9442"
            echo "Could not read --metrics-address from the dataplane process, trying $METRICS_ADDR"
        fi
        echo "Scraping http://${METRICS_ADDR}/metrics"
        curl -s --fail --max-time 10 "http://${METRICS_ADDR}/metrics" || echo "Metrics scrape failed"
    fi
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# Dataplane Container Logs
# ---------------------------
{
    echo -e "\n=== Dataplane Container Logs ==="
    if [ -n "$DATAPLANE_CONTAINER_ID" ]; then
        capture_container_logs "$DATAPLANE_CONTAINER_ID"
    else
        echo "Dataplane container not found - skipping container logs"
    fi
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# System Logs
# ---------------------------
{
    echo -e "\n=== k3s-agent.service status ==="
    systemctl status k3s-agent.service --no-pager

    echo -e "\n=== sshd status ==="
    systemctl status sshd --no-pager

    echo -e "\n=== k3s-agent.service logs (last hour) ==="
    journalctl -u k3s-agent.service --no-pager --since "1 hour ago"

    echo -e "\n=== systemd-networkd logs ==="
    journalctl -u systemd-networkd --no-pager --since "1 hour ago"

    echo -e "\n=== kernel logs (last hour) ==="
    journalctl -k --no-pager --since "1 hour ago"

    echo -e "\n=== Kernel Network Logs ==="
    dmesg | grep -i "network\|bond\|vlan"
} >> "$OUTPUT_FILE" 2>&1

echo "Diagnostics collected to $OUTPUT_FILE"
