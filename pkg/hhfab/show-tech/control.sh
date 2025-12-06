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
# Resource Pressure & Timing
# ---------------------------
{
    echo -e "\n=== Memory Pressure (PSI) ==="
    cat /proc/pressure/memory 2>/dev/null || echo "PSI not available"

    echo -e "\n=== CPU Pressure (PSI) ==="
    cat /proc/pressure/cpu 2>/dev/null || echo "PSI not available"

    echo -e "\n=== VM Stats ==="
    vmstat 1 5

    echo -e "\n=== Detailed Memory Info ==="
    cat /proc/meminfo

    echo -e "\n=== Pod Resource Usage ==="
    $KUBECTL top pods -A 2>&1 || echo "Metrics not available"

    echo -e "\n=== Node Resource Usage ==="
    $KUBECTL top nodes 2>&1 || echo "Metrics not available"

    echo -e "\n=== Pod Ready/Unhealthy Events ==="
    $KUBECTL get events -A --sort-by='.lastTimestamp' | grep -E "Ready|Unhealthy|Failed|BackOff" | tail -100
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# K3s Control Plane Health
# ---------------------------
{
    echo -e "\n=== K3s Service Status ==="
    systemctl status k3s --no-pager -l

    echo -e "\n=== K3s Service Logs (last 500 lines) ==="
    journalctl -u k3s --no-pager -n 500

    echo -e "\n=== Containerd Service Status ==="
    systemctl status containerd --no-pager -l 2>&1 || echo "containerd service not found"

    echo -e "\n=== API Server Health Check ==="
    $KUBECTL get --raw /healthz 2>&1 || echo "API server health check failed"

    echo -e "\n=== API Server Readiness Check ==="
    $KUBECTL get --raw '/readyz?verbose=1' 2>&1 || echo "API server readiness check failed"

    echo -e "\n=== API Server Liveness Check ==="
    $KUBECTL get --raw '/livez?verbose=1' 2>&1 || echo "API server liveness check failed"

    echo -e "\n=== Control Plane Pods Status ==="
    $KUBECTL get pods -n kube-system -o wide 2>&1 || echo "Cannot get kube-system pods"

    echo -e "\n=== Recent Control Plane Pod Events ==="
    $KUBECTL get events -n kube-system --sort-by='.lastTimestamp' 2>&1 | tail -50 || echo "Cannot get kube-system events"
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
    grep -E "ERR|FAIL|WARN|ERROR|WARNING|error|fail|failed|Failed|CrashLoopBackOff|ImagePullBackOff|Pending|Evicted" "$OUTPUT_FILE" | head -500

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
    echo "=== POD ISSUES ==="
    grep -E "CrashLoopBackOff|ImagePullBackOff|Pending|Evicted|Error|OOMKilled|ContainerCannotRun" "$OUTPUT_FILE" 2>/dev/null | head -30 || echo "No pod issues detected"

    echo ""
    echo "=== KUBERNETES EVENTS ==="
    grep -E "Warning|FailedScheduling|FailedMount|BackOff|Unhealthy|FailedCreate" "$OUTPUT_FILE" 2>/dev/null | head -30 || echo "No warning events detected"

    echo ""
    echo "=== API SERVER HEALTH ISSUES ==="
    grep -E "healthz|readyz|livez.*failed|not ok" "$OUTPUT_FILE" 2>/dev/null | head -10 || echo "No API server health issues detected"

    echo ""
    echo "=== MOST COMMON ERROR PATTERNS ==="

    # Find most common error patterns (top 10)
    grep -E "ERR|FAIL|error|fail|Error|Failed" "$OUTPUT_FILE" 2>/dev/null | \
        sed 's/[0-9][0-9]:[0-9][0-9]:[0-9][0-9]/TIME/g' | \
        sed 's/[0-9]\{1,3\}\.[0-9]\{1,3\}\.[0-9]\{1,3\}\.[0-9]\{1,3\}/IP/g' | \
        sed 's/pod\/[a-z0-9-]*/pod\/POD/g' | \
        sed 's/[0-9a-f]\{8\}-[0-9a-f]\{4\}-[0-9a-f]\{4\}-[0-9a-f]\{4\}-[0-9a-f]\{12\}/UUID/g' | \
        sort | uniq -c | sort -rn | head -10 || echo "No patterns found"

} > "$ERROR_FILE" 2>&1

echo "Diagnostics collected to $OUTPUT_FILE"
echo "Errors extracted to $ERROR_FILE"
