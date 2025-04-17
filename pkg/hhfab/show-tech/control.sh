#!/bin/bash
# Copyright 2024 Hedgehog
# SPDX-License-Identifier: Apache-2.0

# show-tech.sh: Collect diagnostics from a Control node.
set +e

OUTPUT_FILE="/tmp/show-tech.log"
KUBECTL="/opt/bin/kubectl"
FABRIC="/opt/bin/kubectl-fabric"

# Create a clean log file
: > "$OUTPUT_FILE"

# ---------------------------
# Basic System Information
# ---------------------------
{
    echo "=== System Information ==="
    uname -a
    cat /etc/os-release

    echo -e "\n=== K3s Version ==="
    k3s --version
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# Network Configuration
# ---------------------------
{
    echo -e "\n=== Network Configuration ==="
    ip addr show
    ip route show
    
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
    $KUBECTL get events -A
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# Fabric Inspection
# ---------------------------
{
    echo -e "\n=== Fabric Overview Inspection ==="
    $FABRIC inspect fabric

    echo -e "\n=== BGP Neighbors Inspection ==="
    $FABRIC inspect bgp

    echo -e "\n=== LLDP Neighbors Inspection ==="
    $FABRIC inspect lldp

    echo -e "\n=== Gathering Switch Information ==="
    for switch in $($KUBECTL get switches.wiring.githedgehog.com -o jsonpath='{.items[*].metadata.name}' 2>/dev/null); do
        echo -e "\n=== Switch Inspection: $switch ==="
        PATH="/opt/bin:$PATH" $FABRIC inspect switch --name=$switch
    done

    echo -e "\n=== Gathering VPC Information ==="
    for vpc in $($KUBECTL get vpcs.vpc.githedgehog.com -o jsonpath='{.items[*].metadata.name}' 2>/dev/null); do
        echo -e "\n=== VPC Inspection: $vpc ==="
        PATH="/opt/bin:$PATH" $FABRIC inspect vpc --name=$vpc
    done

    echo -e "\n=== Gathering Connection Information ==="
    for conn in $($KUBECTL get connections.wiring.githedgehog.com -o jsonpath='{.items[*].metadata.name}' 2>/dev/null); do
        echo -e "\n=== Connection Inspection: $conn ==="
        PATH="/opt/bin:$PATH" $FABRIC inspect connection --name=$conn
    done

    echo -e "\n=== Listing Management Connections ==="
    PATH="/opt/bin:$PATH" $FABRIC connection get management

    echo -e "\n=== Listing Fabric Connections ==="
    PATH="/opt/bin:$PATH" $FABRIC connection get fabric

    echo -e "\n=== Listing VPC Loopback Connections ==="
    PATH="/opt/bin:$PATH" $FABRIC connection get vpc-loopback

    echo -e "\n=== Gathering Server Information ==="
    for server in $($KUBECTL get servers.wiring.githedgehog.com -o jsonpath='{.items[*].metadata.name}' 2>/dev/null); do
        echo -e "\n=== Server Inspection: $server ==="
        PATH="/opt/bin:$PATH" $FABRIC inspect server --name=$server
    done
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
    echo -e "\n=== githedgehog.com Resources ==="
    
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
    echo -e "\n=== systemd-networkd logs ==="
    journalctl -u systemd-networkd

    echo -e "\n=== kernel logs ==="
    journalctl -k

    echo -e "\n=== Kernel Network Logs ==="
    dmesg | grep -i "network\|bond\|vlan"
} >> "$OUTPUT_FILE" 2>&1

echo "Diagnostics collected to $OUTPUT_FILE"
