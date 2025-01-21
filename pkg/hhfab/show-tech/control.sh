#!/bin/bash
# Copyright 2024 Hedgehog
# SPDX-License-Identifier: Apache-2.0

# show-tech.sh: Collect diagnostics from a Control node.

# Set the output file
OUTPUT_FILE="/tmp/show-tech.log"

# Clear the log file
: > "$OUTPUT_FILE"

# Set the kubectl path
KUBECTL="/opt/bin/kubectl"

echo "=== System Information ===" >> "$OUTPUT_FILE"
uname -a >> "$OUTPUT_FILE"
cat /etc/os-release >> "$OUTPUT_FILE"

echo -e "\n=== Network Configuration ===" >> "$OUTPUT_FILE"
ip addr show >> "$OUTPUT_FILE"
ip route show >> "$OUTPUT_FILE"

echo -e "\n=== K3s Version ===" >> "$OUTPUT_FILE"
k3s --version >> "$OUTPUT_FILE" 2>/dev/null

echo -e "\n=== Kubernetes Nodes ===" >> "$OUTPUT_FILE"
$KUBECTL get nodes -o wide >> "$OUTPUT_FILE" 2>/dev/null

echo -e "\n=== Kubernetes Pods ===" >> "$OUTPUT_FILE"
$KUBECTL get pods -A -o wide >> "$OUTPUT_FILE" 2>/dev/null

echo -e "\n=== Kubernetes Events ===" >> "$OUTPUT_FILE"
$KUBECTL get events -A >> "$OUTPUT_FILE" 2>/dev/null

echo -e "\n=== Describe All Kubernetes Pods ===" >> "$OUTPUT_FILE"
for ns in $($KUBECTL get ns -o jsonpath='{.items[*].metadata.name}'); do
  for pod in $($KUBECTL get pods -n $ns -o jsonpath='{.items[*].metadata.name}'); do
    echo -e "\n--- Namespace: $ns, Pod: $pod ---" >> "$OUTPUT_FILE"
    $KUBECTL describe pod "$pod" -n "$ns" >> "$OUTPUT_FILE" 2>/dev/null

    echo -e "\nLogs for Pod: $pod in Namespace: $ns" >> "$OUTPUT_FILE"
    $KUBECTL logs "$pod" -n "$ns" --all-containers=true >> "$OUTPUT_FILE" 2>/dev/null

    echo -e "\nPrevious Logs for Pod: $pod in Namespace: $ns (if available)" >> "$OUTPUT_FILE"
    $KUBECTL logs "$pod" -n "$ns" --all-containers=true --previous >> "$OUTPUT_FILE" 2>/dev/null
  done
done

echo -e "\n=== Disk Usage ===" >> "$OUTPUT_FILE"
df -h >> "$OUTPUT_FILE"

echo -e "\n=== Running Processes ===" >> "$OUTPUT_FILE"
ps aux >> "$OUTPUT_FILE"

# Get custom resources

echo -e "\n=== Githedgehog.com Resources ===" >> "$OUTPUT_FILE"

crds_githedgehog=$($KUBECTL get crds -o custom-columns=":metadata.name" | grep 'githedgehog.com')

for crd in $crds_githedgehog; do
    echo -e "\n=== Instances of $crd ===" >> "$OUTPUT_FILE"
    $KUBECTL get $crd -A >> "$OUTPUT_FILE" 2>/dev/null
done

resources_githedgehog=$($KUBECTL api-resources --verbs=list --namespaced=true -o name | grep 'githedgehog.com')

for resource in $resources_githedgehog; do
    echo -e "\n=== Instances of $resource ===" >> "$OUTPUT_FILE"
    $KUBECTL get $resource -A >> "$OUTPUT_FILE" 2>/dev/null
done

resources_non_namespaced_githedgehog=$($KUBECTL api-resources --verbs=list --namespaced=false -o name | grep 'githedgehog.com')

for resource in $resources_non_namespaced_githedgehog; do
    echo -e "\n=== Instances of $resource (non-namespaced) ===" >> "$OUTPUT_FILE"
    $KUBECTL get $resource >> "$OUTPUT_FILE" 2>/dev/null
done

echo -e "\n=== systemd-networkd logs ===" >> "$OUTPUT_FILE"
journalctl -u systemd-networkd >> "$OUTPUT_FILE" 2>/dev/null

echo -e "\n=== kernel logs ===" >> "$OUTPUT_FILE"
journalctl -k >> "$OUTPUT_FILE" 2>/dev/null

echo -e "\n=== Kernel Network Logs ===" >> "$OUTPUT_FILE"
dmesg | grep -i "network\|bond\|vlan" >> "$OUTPUT_FILE"

echo "Diagnostics collected to $OUTPUT_FILE"
