#!/bin/bash
# Copyright 2025 Hedgehog
# SPDX-License-Identifier: Apache-2.0

# show-tech.sh: Collect diagnostics from a Gateway node (OS level + FRR/vtysh).
set +e

OUTPUT_FILE="/tmp/show-tech.log"

: > "$OUTPUT_FILE"

# Suppress crictl config file warnings by pointing to /dev/null
export CRI_CONFIG_FILE=/dev/null

CRICTL="sudo -E crictl --runtime-endpoint unix:///run/k3s/containerd/containerd.sock"

# Find the running FRR container ID
FRR_CONTAINER_ID=$($CRICTL ps --name '^frr$' -q 2>>"$OUTPUT_FILE" | head -1)

# Find the running dataplane container ID
DATAPLANE_CONTAINER_ID=$($CRICTL ps -q --name dataplane 2>>"$OUTPUT_FILE" | head -1)

# Helper for running vtysh commands inside the FRR container
run_vtysh_cmd() {
    echo -e "\n=== Executing: vtysh -c '$1' ===" >> "$OUTPUT_FILE"
    $CRICTL exec "$FRR_CONTAINER_ID" vtysh -X /lib/libvtysh_hedgehog.so -c "$1" >> "$OUTPUT_FILE" 2>&1
}

# Helper for running dataplane-cli commands inside the dataplane container
run_dp_cmd() {
    echo -e "\n=== Executing: dataplane-cli -c '$1' ===" >> "$OUTPUT_FILE"
    $CRICTL exec "$DATAPLANE_CONTAINER_ID" /dataplane-cli -c "$1" >> "$OUTPUT_FILE" 2>&1
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

    echo -e "\n=== Link Status ==="
    ip link show
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
# k3s / containerd Forensics
# (runs regardless of FRR/dataplane pod state — captures init-container failures)
# ---------------------------
{
    echo -e "\n=== k3s / containerd forensics ==="

    echo -e "\n--- k3s-agent.service status ---"
    systemctl status k3s-agent.service --no-pager

    echo -e "\n--- containerd status ---"
    systemctl status containerd --no-pager 2>/dev/null || echo "containerd service not found (k3s embeds containerd)"

    echo -e "\n--- k3s-agent.service journal (since boot) ---"
    journalctl -u k3s-agent.service --no-pager --since "$(uptime -s)"

    echo -e "\n--- kubelet / containerd config (secrets redacted) ---"
    # These files can contain k3s join tokens and registry credentials; show-tech
    # is uploaded as CI artifacts, so redact common secret fields before printing.
    redact_secrets() {
        sed -E \
            -e 's/(^[[:space:]]*(token|password|passwd|secret|auth|authorization|bearer|username|user|apikey|api_key|access_key|private_key)[[:space:]]*[:=][[:space:]]*).*/\1<REDACTED>/i' \
            -e 's/(https?:\/\/)[^:@/[:space:]]+:[^@/[:space:]]+@/\1<REDACTED>:<REDACTED>@/g'
    }
    for f in \
        /etc/rancher/k3s/config.yaml \
        /etc/rancher/k3s/config.yaml.d/* \
        /var/lib/rancher/k3s/agent/etc/containerd/config.toml \
        /var/lib/rancher/k3s/agent/etc/containerd/config.toml.tmpl \
        /var/lib/rancher/k3s/agent/containerd/config.toml \
        /run/k3s/containerd/containerd.toml
    do
        if [ -f "$f" ]; then
            echo "--- $f ---"
            redact_secrets < "$f"
        fi
    done

    echo -e "\n--- crictl info ---"
    $CRICTL info 2>&1 | head -n 200

    echo -e "\n--- crictl pods (all) ---"
    $CRICTL pods

    echo -e "\n--- crictl ps -a (all containers, any state) ---"
    $CRICTL ps -a

    echo -e "\n--- per-pod describe (crictl inspectp) ---"
    for pid in $($CRICTL pods -q 2>/dev/null); do
        echo -e "\n--- crictl inspectp $pid ---"
        $CRICTL inspectp "$pid" 2>&1 | head -n 200
    done

    echo -e "\n--- per-container inspect + logs (all states, incl. exited init containers) ---"
    for cid in $($CRICTL ps -a -q 2>/dev/null); do
        echo -e "\n--- crictl inspect $cid ---"
        $CRICTL inspect "$cid" 2>&1 | head -n 300
        echo -e "\n--- crictl logs --tail 500 $cid ---"
        $CRICTL logs --tail 500 "$cid" 2>&1
    done

    echo -e "\n--- kubelet pod dirs (volume mounts and status) ---"
    if [ -d /var/lib/kubelet/pods ]; then
        sudo ls -la /var/lib/kubelet/pods 2>&1 | head -n 200
        sudo find /var/lib/kubelet/pods -maxdepth 3 -type f \( -name 'status' -o -name 'containerid' \) 2>/dev/null | head -n 50
    else
        echo "/var/lib/kubelet/pods not present"
    fi

    echo -e "\n--- network namespaces ---"
    ip netns list 2>&1

    echo -e "\n--- CNI config ---"
    ls -la /etc/cni/net.d/ 2>/dev/null || echo "/etc/cni/net.d/ not present"
    for f in /etc/cni/net.d/*; do
        [ -f "$f" ] && { echo "--- $f ---"; cat "$f"; }
    done

    echo -e "\n--- kernel dmesg (oom/errors/frr/netns) ---"
    dmesg -T 2>/dev/null | grep -iE 'oom|segfault|denied|frr|netns|cni|container|bpf' | tail -n 100
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
        $CRICTL logs "$FRR_CONTAINER_ID"
    else
        echo "FRR container not found — skipping container logs"
    fi
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# Dataplane Diagnostics
# ---------------------------
{
    echo -e "\n=== Dataplane Diagnostics ==="
    if [ -z "$DATAPLANE_CONTAINER_ID" ]; then
        echo "Dataplane container not found — skipping dataplane diagnostics"
    else
        run_dp_cmd "show tech"
    fi
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# Dataplane Container Logs
# ---------------------------
{
    echo -e "\n=== Dataplane Container Logs ==="
    if [ -n "$DATAPLANE_CONTAINER_ID" ]; then
        $CRICTL logs "$DATAPLANE_CONTAINER_ID"
    else
        echo "Dataplane container not found — skipping container logs"
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
