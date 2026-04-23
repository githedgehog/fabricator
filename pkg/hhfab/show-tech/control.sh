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

    echo -e "\n=== NIC Statistics and Offload Settings ==="
    for iface in $(ls /sys/class/net/ | grep -v '^lo$'); do
        echo "--- $iface Statistics---"
        ethtool -S "$iface" 2>/dev/null
        echo "--- $iface Offload Settings---"
        ethtool -k "$iface" 2>/dev/null | grep -E 'offload|segmentation'
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
# Broad describe of every pod. Intentionally no `kubectl logs --all-containers`
# here: on a cluster with pods stuck in PodInitializing, the kubelet log stream
# blocks until containers start, which is the exact failure mode this script is
# meant to debug. Per-container log capture for unhealthy pods happens in the
# Not-Ready Pod Forensics section below, with bounded request timeouts.
{
    echo -e "\n=== Describe All Kubernetes Pods ==="
    for ns in $($KUBECTL get ns -o jsonpath='{.items[*].metadata.name}'); do
        for pod in $($KUBECTL get pods -n "$ns" -o jsonpath='{.items[*].metadata.name}'); do
            echo -e "\n--- Namespace: $ns, Pod: $pod ---"
            $KUBECTL describe pod "$pod" -n "$ns"
        done
    done
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# Not-Ready Pod Forensics (PodInitializing, CrashLoopBackOff, init container failures)
# ---------------------------
{
    echo -e "\n=== Not-Ready Pod Forensics ==="

    # kubectl JSONPath can't reliably filter on array elements, so derive the
    # not-ready set by streaming all pods (phase + per-container ready flags)
    # through awk. A pod is "not ready" if phase != Running, or if any (init
    # or regular) container is present but not ready.
    $KUBECTL get pods -A -o jsonpath='{range .items[*]}{.metadata.namespace}{"\t"}{.metadata.name}{"\t"}{.status.phase}{"\t"}{range .status.initContainerStatuses[*]}{.ready}{","}{end}{"\t"}{range .status.containerStatuses[*]}{.ready}{","}{end}{"\n"}{end}' \
        | awk -F'\t' '
            {
                ns=$1; name=$2; phase=$3; init_states=$4; main_states=$5
                if (ns == "" || name == "") { next }
                not_ready=0
                if (phase != "Running" && phase != "Succeeded") { not_ready=1 }
                split(init_states "," main_states, states, ",")
                for (i in states) {
                    if (states[i] != "" && states[i] != "true") { not_ready=1 }
                }
                if (not_ready) { print ns "\t" name }
            }
        ' | sort -u > /tmp/not-ready-pods.txt

    if [ ! -s /tmp/not-ready-pods.txt ]; then
        echo "No not-ready pods found."
    else
        echo "Not-ready pods:"
        cat /tmp/not-ready-pods.txt
        echo
        while IFS=$'\t' read -r ns pod; do
            [ -z "$ns" ] && continue
            echo -e "\n### [$ns/$pod] Full YAML ###"
            $KUBECTL get pod "$pod" -n "$ns" -o yaml

            echo -e "\n### [$ns/$pod] Init container statuses ###"
            $KUBECTL get pod "$pod" -n "$ns" -o jsonpath='{range .status.initContainerStatuses[*]}{"name="}{.name}{" ready="}{.ready}{" restartCount="}{.restartCount}{" state="}{.state}{"\n"}{end}'

            echo -e "\n### [$ns/$pod] Regular container statuses ###"
            $KUBECTL get pod "$pod" -n "$ns" -o jsonpath='{range .status.containerStatuses[*]}{"name="}{.name}{" ready="}{.ready}{" restartCount="}{.restartCount}{" state="}{.state}{"\n"}{end}'

            echo -e "\n### [$ns/$pod] Events ###"
            $KUBECTL get events -n "$ns" --field-selector "involvedObject.name=$pod" --sort-by='.metadata.creationTimestamp'

            # Logs per container (init + regular), current and previous, even if container hasn't started.
            # `--request-timeout=15s` bounds each call so a PodInitializing container can't stall the whole script.
            init_containers=$($KUBECTL get pod "$pod" -n "$ns" -o jsonpath='{.spec.initContainers[*].name}')
            main_containers=$($KUBECTL get pod "$pod" -n "$ns" -o jsonpath='{.spec.containers[*].name}')
            for c in $init_containers $main_containers; do
                echo -e "\n### [$ns/$pod] logs ($c, current) ###"
                $KUBECTL logs "$pod" -n "$ns" -c "$c" --request-timeout=15s 2>&1 | tail -n 500
                echo -e "\n### [$ns/$pod] logs ($c, previous) ###"
                $KUBECTL logs "$pod" -n "$ns" -c "$c" --previous --request-timeout=15s 2>&1 | tail -n 500
            done
        done < /tmp/not-ready-pods.txt
    fi
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# k3s Server / Containerd Forensics
# ---------------------------
# These config files can carry cluster tokens and registry credentials.
# Show-tech output is uploaded as CI artifacts, so redact common secret
# fields before printing. Keep the key name visible so reviewers can tell
# what was set, but replace the value with <REDACTED>.
redact_secrets() {
    sed -E \
        -e 's/(^[[:space:]]*(token|password|passwd|secret|auth|authorization|bearer|username|user|apikey|api_key|access_key|private_key)[[:space:]]*[:=][[:space:]]*).*/\1<REDACTED>/i' \
        -e 's/(https?:\/\/)[^:@/[:space:]]+:[^@/[:space:]]+@/\1<REDACTED>:<REDACTED>@/g'
}

{
    echo -e "\n=== k3s server / containerd forensics ==="

    echo -e "\n--- k3s config files (secrets redacted) ---"
    for f in \
        /etc/rancher/k3s/config.yaml \
        /etc/rancher/k3s/config.yaml.d/* \
        /var/lib/rancher/k3s/agent/etc/containerd/config.toml \
        /var/lib/rancher/k3s/agent/etc/containerd/config.toml.tmpl \
        /run/k3s/containerd/containerd.toml
    do
        if [ -f "$f" ]; then
            echo "--- $f ---"
            redact_secrets < "$f"
        fi
    done

    echo -e "\n--- manifests directory ---"
    ls -la /var/lib/rancher/k3s/server/manifests/ 2>/dev/null || echo "no manifests dir"

    echo -e "\n--- crictl info (control node) ---"
    export CRI_CONFIG_FILE=/dev/null
    CRICTL="sudo -E crictl --runtime-endpoint unix:///run/k3s/containerd/containerd.sock"
    $CRICTL info 2>&1 | head -n 200

    echo -e "\n--- crictl pods (all, control node) ---"
    $CRICTL pods

    echo -e "\n--- crictl ps -a (all, control node) ---"
    $CRICTL ps -a
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

    echo -e "\n=== Describing API Resources (secrets redacted) ==="
    # The Fabricator CR (and potentially other githedgehog.com CRDs) contains
    # inline credentials (Users[].Password, registry Password, etc.). Pipe the
    # describe output through the same redact_secrets filter used for k3s
    # config files to avoid leaking them into CI artifacts.
    for resource in $resources_githedgehog; do
        echo -e "\n=== Executing: $KUBECTL describe $resource -A ==="
        $KUBECTL describe $resource -A | redact_secrets
    done
} >> "$OUTPUT_FILE" 2>&1
    
# ---------------------------
# System Logs
# ---------------------------
{
    echo -e "\n=== e1000 offloads .link file ==="
    cat /etc/systemd/network/10-e1000-offloads.link 2>/dev/null || echo "Not found"

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
