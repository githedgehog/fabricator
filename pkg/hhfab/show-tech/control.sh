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

echo -e "\n=== K3s Version ===" >> "$OUTPUT_FILE"
k3s --version >> "$OUTPUT_FILE" 2>/dev/null

echo -e "\n=== Kubernetes Nodes ===" >> "$OUTPUT_FILE"
$KUBECTL get nodes -o wide >> "$OUTPUT_FILE" 2>/dev/null

echo -e "\n=== Kubernetes Pods ===" >> "$OUTPUT_FILE"
$KUBECTL get pods -A -o wide >> "$OUTPUT_FILE" 2>/dev/null

echo -e "\n=== Kubernetes Events ===" >> "$OUTPUT_FILE"
$KUBECTL get events -A >> "$OUTPUT_FILE" 2>/dev/null

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

echo "Diagnostics collected to $OUTPUT_FILE"
