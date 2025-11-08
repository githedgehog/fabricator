#!/usr/bin/env bash
# Copyright 2024 Hedgehog
# SPDX-License-Identifier: Apache-2.0

# Collect Debug Information Script
# Collects system and environment information for debugging fabricator issues

set -euo pipefail

echo "=== Fabricator Debug Information Collection ==="
echo "Generated: $(date)"
echo ""

echo "=== System Information ==="
echo "Hostname: $(hostname)"
echo "OS: $(cat /etc/os-release | grep PRETTY_NAME | cut -d= -f2)"
echo "Kernel: $(uname -r)"
echo "Architecture: $(uname -m)"
echo ""

echo "=== Hardware Resources ==="
echo "CPU Cores: $(nproc)"
echo "Memory:"
free -h
echo ""
echo "Disk Space:"
df -h | grep -E '(Filesystem|/dev/|/mnt)'
echo ""

echo "=== Software Versions ==="
echo "Go: $(go version 2>/dev/null || echo 'not installed')"
echo "Docker: $(docker --version 2>/dev/null || echo 'not installed')"
echo "Just: $(just --version 2>/dev/null || echo 'not installed')"
echo "Kubectl: $(kubectl version --client --short 2>/dev/null || echo 'not installed')"
echo "Virsh: $(virsh --version 2>/dev/null || echo 'not installed')"
echo ""

echo "=== Docker Status ==="
if command -v docker &> /dev/null; then
    docker info 2>&1 | head -20 || echo "Docker not running"
else
    echo "Docker not installed"
fi
echo ""

echo "=== Fabricator Cache ==="
if [ -d "$HOME/.hhfab-cache" ]; then
    echo "Cache location: $HOME/.hhfab-cache"
    echo "Cache size: $(du -sh $HOME/.hhfab-cache 2>/dev/null | cut -f1)"
    echo "Cached artifacts:"
    ls -1 "$HOME/.hhfab-cache/v1/" 2>/dev/null | head -10 || echo "No artifacts cached"
else
    echo "No cache directory found"
fi
echo ""

echo "=== Network Configuration ==="
echo "Interfaces:"
ip -br addr show
echo ""
echo "Default route:"
ip route show default
echo ""
echo "DNS:"
cat /etc/resolv.conf | grep -v "^#"
echo ""

echo "=== Environment Variables ==="
echo "HHFAB_VERBOSE: ${HHFAB_VERBOSE:-not set}"
echo "HHFAB_CACHE_DIR: ${HHFAB_CACHE_DIR:-not set}"
echo "HHFAB_PREVIEW: ${HHFAB_PREVIEW:-not set}"
echo "HTTP_PROXY: ${HTTP_PROXY:-not set}"
echo "HTTPS_PROXY: ${HTTPS_PROXY:-not set}"
echo ""

echo "=== Registry Connectivity ==="
echo "Testing ghcr.io..."
if curl -s -I https://ghcr.io/v2/ > /dev/null 2>&1; then
    echo "  ✓ ghcr.io is reachable"
else
    echo "  ✗ ghcr.io is NOT reachable"
fi

echo "Testing local registry (127.0.0.1:30000)..."
if curl -s http://127.0.0.1:30000/v2/_catalog > /dev/null 2>&1; then
    echo "  ✓ Local registry is running"
else
    echo "  ✗ Local registry is NOT running"
fi
echo ""

if [ -d "workdir" ]; then
    echo "=== Build Artifacts ==="
    echo "Workdir exists: yes"
    if [ -d "workdir/result" ]; then
        echo "Result directory size: $(du -sh workdir/result 2>/dev/null | cut -f1)"
        echo "Artifacts:"
        ls -lh workdir/result/ 2>/dev/null | grep -E '\.(iso|img|tgz|ign)$' || echo "No artifacts found"
    else
        echo "No result directory"
    fi
    echo ""
fi

if command -v kubectl &> /dev/null && kubectl cluster-info &> /dev/null; then
    echo "=== Kubernetes Cluster Info ==="
    echo "Cluster:"
    kubectl cluster-info 2>/dev/null || echo "No cluster connection"
    echo ""
    echo "Nodes:"
    kubectl get nodes 2>/dev/null || echo "Cannot get nodes"
    echo ""
    echo "Pods (all namespaces):"
    kubectl get pods -A 2>/dev/null | head -20 || echo "Cannot get pods"
    echo ""
fi

if command -v virsh &> /dev/null; then
    echo "=== VLAB Status ==="
    echo "Libvirt VMs:"
    virsh list --all 2>/dev/null || echo "Cannot query libvirt"
    echo ""
    echo "Network bridges:"
    brctl show 2>/dev/null || echo "brctl not available"
    echo ""
fi

echo "=== Recent System Logs ==="
echo "Checking for hhfab-install service..."
if systemctl list-units --type=service | grep -q hhfab-install; then
    echo "Last 20 lines of hhfab-install.service:"
    journalctl -u hhfab-install.service -n 20 --no-pager 2>/dev/null || echo "Cannot read service logs"
else
    echo "hhfab-install.service not found (normal if not on installed node)"
fi
echo ""

echo "=== Debug Information Collection Complete ==="
echo ""
echo "To save this output to a file:"
echo "  $0 > debug-info.txt"
echo ""
echo "To share this information, review and redact any sensitive data,"
echo "then attach debug-info.txt to your GitHub issue."
