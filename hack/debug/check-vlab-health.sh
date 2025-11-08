#!/usr/bin/env bash
# Copyright 2024 Hedgehog
# SPDX-License-Identifier: Apache-2.0

# VLAB Health Check Script
# Checks health of running VLAB environment

set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo "=== VLAB Health Check ==="
echo "Timestamp: $(date)"
echo ""

echo "=== Libvirt Status ==="
if ! systemctl is-active --quiet libvirtd; then
    echo -e "${RED}✗${NC} libvirtd is not running"
    exit 1
else
    echo -e "${GREEN}✓${NC} libvirtd is running"
fi
echo ""

echo "=== Virtual Machines ==="
vms=$(virsh list --all --name 2>/dev/null || echo "")
if [ -z "$vms" ]; then
    echo -e "${YELLOW}!${NC} No VMs found"
else
    echo "Found VMs:"
    virsh list --all
fi
echo ""

echo "=== VM Connectivity ==="
running_vms=$(virsh list --name 2>/dev/null || echo "")
if [ -z "$running_vms" ]; then
    echo -e "${YELLOW}!${NC} No running VMs"
else
    for vm in $running_vms; do
        # Try to get IP
        ip=$(virsh domifaddr "$vm" 2>/dev/null | awk 'NR>2 {print $4}' | cut -d/ -f1 | head -1)
        if [ -n "$ip" ]; then
            # Try to ping
            if ping -c 1 -W 2 "$ip" > /dev/null 2>&1; then
                echo -e "${GREEN}✓${NC} $vm ($ip): reachable"
            else
                echo -e "${YELLOW}!${NC} $vm ($ip): not responding to ping"
            fi
        else
            echo -e "${YELLOW}!${NC} $vm: no IP address assigned"
        fi
    done
fi
echo ""

echo "=== Network Bridges ==="
if command -v brctl &> /dev/null; then
    brctl show
else
    echo -e "${YELLOW}!${NC} brctl not available"
fi
echo ""

echo "=== Kubernetes Cluster ==="
if command -v kubectl &> /dev/null; then
    if kubectl cluster-info &> /dev/null; then
        echo -e "${GREEN}✓${NC} Kubernetes cluster is accessible"
        echo ""
        echo "Nodes:"
        kubectl get nodes
        echo ""
        echo "Pods (fabricator-system):"
        kubectl get pods -n fabricator-system 2>/dev/null || echo "fabricator-system namespace not found"
        echo ""
        echo "Pods (kube-system):"
        kubectl get pods -n kube-system | head -10
    else
        echo -e "${YELLOW}!${NC} Cannot connect to Kubernetes cluster"
    fi
else
    echo -e "${YELLOW}!${NC} kubectl not available"
fi
echo ""

echo "=== Control Node Services ==="
control_vms=$(virsh list --name 2>/dev/null | grep -E 'control|ctrl' || echo "")
if [ -n "$control_vms" ]; then
    for vm in $control_vms; do
        echo "Checking $vm..."
        ip=$(virsh domifaddr "$vm" 2>/dev/null | awk 'NR>2 {print $4}' | cut -d/ -f1 | head -1)
        if [ -n "$ip" ]; then
            # Try SSH
            if ssh -o ConnectTimeout=5 -o StrictHostKeyChecking=no core@"$ip" "systemctl is-active k3s" &> /dev/null; then
                echo -e "  ${GREEN}✓${NC} K3s is running"
            else
                echo -e "  ${YELLOW}!${NC} K3s status unknown (SSH failed)"
            fi
        else
            echo -e "  ${YELLOW}!${NC} No IP address"
        fi
    done
else
    echo -e "${YELLOW}!${NC} No control VMs found"
fi
echo ""

echo "=== Resource Usage ==="
echo "Host CPU:"
top -bn1 | grep "Cpu(s)" | sed "s/.*, *\([0-9.]*\)%* id.*/\1/" | awk '{print 100 - $1"%"}'
echo ""
echo "Host Memory:"
free -h | grep Mem
echo ""
echo "Host Disk:"
df -h / | awk 'NR==2 {print "Used: " $3 " / " $2 " (" $5 ")"}'
echo ""

echo "=== VM Resource Allocation ==="
for vm in $running_vms; do
    echo "$vm:"
    virsh dominfo "$vm" | grep -E "(CPU|memory|Max memory)"
    echo ""
done

echo "=== Health Check Complete ==="
