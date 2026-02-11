#!/bin/bash
# Copyright 2024 Hedgehog
# SPDX-License-Identifier: Apache-2.0

# show-tech.sh: Collect diagnostics from a Control node.
set +e

OUTPUT_FILE="/tmp/show-tech.log"
KUBECTL="/opt/bin/kubectl"

: > "$OUTPUT_FILE"

# ---------------------------
# Basic System Information
# ---------------------------
{
    echo "=== System Information ==="
    uname -a
    cat /etc/os-release

    echo -e "\n=== K3s Version ==="
    /opt/bin/k3s --version
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# Network Configuration
# ---------------------------
{
    echo -e "\n=== Network Configuration ==="
    ip addr show
    ip route show
    ip neigh show
    ip link show

    echo -e "\n=== NIC Offload Settings ==="
    for iface in $(ls /sys/class/net/ | grep -v '^lo$'); do
        echo "--- $iface ---"
        ethtool -k "$iface" 2>/dev/null | grep -E 'offload|segmentation' || echo "ethtool not available"
    done

    echo -e "\n=== Switch connectivity from control node ==="
    for sw in $($KUBECTL get switches -o jsonpath='{.items[*].spec.ip}' 2>/dev/null | tr ' ' '\n' | cut -d/ -f1); do
        echo -n "Switch $sw: ping="
        ping -c1 -W2 "$sw" >/dev/null 2>&1 && echo -n "ok" || echo -n "fail"
        echo -n " arp="
        ip neigh show "$sw" 2>/dev/null | awk '{print $NF}' || echo -n "none"
        echo -n " ssh:22="
        nc -zw2 "$sw" 22 >/dev/null 2>&1 && echo "open" || echo "closed"
    done

    echo -e "\n=== Disk Usage ==="
    df -h
    
    echo -e "\n=== Running Processes ==="
    ps aux
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# Kubernetes Cluster Status
# ---------------------------
{
    echo -e "\n=== Kubernetes Nodes ==="
    $KUBECTL get nodes -o wide

    echo -e "\n=== Kubernetes Pods ==="
    $KUBECTL get pods -A -o wide

    echo -e "\n=== Kubernetes Events ==="
    $KUBECTL get events -A --sort-by='.metadata.creationTimestamp'
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# Detailed Pod Information
# ---------------------------
{
    echo -e "\n=== Describe All Kubernetes Pods ==="
    for ns in $($KUBECTL get ns -o jsonpath='{.items[*].metadata.name}'); do
        for pod in $($KUBECTL get pods -n $ns -o jsonpath='{.items[*].metadata.name}'); do
            echo -e "\n--- Namespace: $ns, Pod: $pod ---"
            $KUBECTL describe pod "$pod" -n "$ns"

            echo -e "\nLogs for Pod: $pod in Namespace: $ns"
            $KUBECTL logs "$pod" -n "$ns" --all-containers=true

            echo -e "\nPrevious Logs for Pod: $pod in Namespace: $ns (if available)"
            $KUBECTL logs "$pod" -n "$ns" --all-containers=true --previous
        done
    done
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# Githedgehog Resources
# ---------------------------
{
    echo -e "\n=== Listing API Resources ==="
    resources_githedgehog=$($KUBECTL api-resources --verbs=list --namespaced=true -o name | grep 'githedgehog.com')
    for resource in $resources_githedgehog; do
        echo -e "\n=== Executing: $KUBECTL get $resource -A ==="
        $KUBECTL get $resource -A
    done

    echo -e "\n=== Describing API Resources ==="
    for resource in $resources_githedgehog; do
        echo -e "\n=== Executing: $KUBECTL describe $resource -A ==="
        $KUBECTL describe $resource -A
    done
} >> "$OUTPUT_FILE" 2>&1
    
# ---------------------------
# System Logs
# ---------------------------
{
    echo -e "\n=== disable-e1000-offloads.service status ==="
    systemctl status disable-e1000-offloads.service --no-pager 2>&1 || true

    echo -e "\n=== k3s.service status ==="
    systemctl status k3s.service --no-pager

    echo -e "\n=== sshd status ==="
    systemctl status sshd --no-pager

    echo -e "\n=== k3s.service logs (last hour) ==="
    journalctl -u k3s.service --no-pager --since "1 hour ago"

    echo -e "\n=== systemd-networkd logs ==="
    journalctl -u systemd-networkd

    echo -e "\n=== kernel logs ==="
    journalctl -k

    echo -e "\n=== Kernel Network Logs ==="
    dmesg | grep -i "network\|bond\|vlan"
} >> "$OUTPUT_FILE" 2>&1

echo "Diagnostics collected to $OUTPUT_FILE"
