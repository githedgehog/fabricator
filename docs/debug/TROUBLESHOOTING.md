# Fabricator Troubleshooting Guide

**Version:** 1.0
**Date:** 2025-11-08
**Issue:** #8

## Overview

This guide provides specific solutions to common problems encountered when using Hedgehog Fabricator. Each issue includes symptoms, diagnosis steps, and tested solutions.

## Table of Contents

1. [Build Issues](#build-issues)
2. [Installation Issues](#installation-issues)
3. [VLAB Issues](#vlab-issues)
4. [Network Issues](#network-issues)
5. [Kubernetes Issues](#kubernetes-issues)
6. [Performance Issues](#performance-issues)

---

## Build Issues

### Issue: Build hangs during artifact download

**Symptoms:**
- Build process stops responding
- No output for extended period
- Last message shows artifact download starting

**Diagnosis:**
```bash
# Check network connectivity
ping -c 3 ghcr.io

# Check firewall/proxy settings
env | grep -i proxy

# Test registry access
curl -I https://ghcr.io/v2/
```

**Solutions:**

1. **Check Docker authentication:**
   ```bash
   # Re-login to registry
   docker login ghcr.io
   # Enter your GitHub username and token
   ```

2. **Clear cache and retry:**
   ```bash
   rm -rf ~/.hhfab-cache/v1/*
   hhfab --verbose build --mode iso
   ```

3. **Check network/proxy:**
   ```bash
   # If behind proxy, configure Docker
   sudo mkdir -p /etc/systemd/system/docker.service.d
   sudo cat > /etc/systemd/system/docker.service.d/http-proxy.conf <<EOF
   [Service]
   Environment="HTTP_PROXY=http://proxy.example.com:8080"
   Environment="HTTPS_PROXY=http://proxy.example.com:8080"
   Environment="NO_PROXY=localhost,127.0.0.1"
   EOF
   sudo systemctl daemon-reload
   sudo systemctl restart docker
   ```

---

### Issue: Build fails with "disk space" error

**Symptoms:**
- Build terminates with "no space left on device"
- Large ISO/IMG files fail to create

**Diagnosis:**
```bash
# Check available disk space
df -h

# Check cache size
du -sh ~/.hhfab-cache/

# Check workdir size
du -sh workdir/
```

**Solutions:**

1. **Clean up old artifacts:**
   ```bash
   # Remove old result files
   rm -rf workdir/result/*

   # Clear cache
   rm -rf ~/.hhfab-cache/v1/*

   # Clean Docker images
   docker system prune -a
   ```

2. **Move cache to larger disk:**
   ```bash
   # Move cache
   mkdir -p /mnt/large-disk/hhfab-cache
   mv ~/.hhfab-cache/* /mnt/large-disk/hhfab-cache/
   ln -s /mnt/large-disk/hhfab-cache ~/.hhfab-cache

   # Or set environment variable
   export HHFAB_CACHE_DIR=/mnt/large-disk/hhfab-cache
   ```

---

### Issue: Build fails with "permission denied"

**Symptoms:**
- Build fails accessing cache directory
- Errors mention file permissions

**Diagnosis:**
```bash
# Check cache permissions
ls -la ~/.hhfab-cache/

# Check workdir permissions
ls -la workdir/
```

**Solutions:**

1. **Fix cache permissions:**
   ```bash
   # Fix ownership
   sudo chown -R $USER:$USER ~/.hhfab-cache/

   # Fix permissions
   chmod -R 755 ~/.hhfab-cache/
   ```

2. **Recreate cache directory:**
   ```bash
   rm -rf ~/.hhfab-cache/
   mkdir -p ~/.hhfab-cache/v1/
   hhfab build --mode iso
   ```

---

### Issue: Build creates corrupted ISO

**Symptoms:**
- ISO file created but won't boot
- Checksum verification fails
- ISO appears truncated

**Diagnosis:**
```bash
# Check ISO file integrity
file workdir/result/control--*.iso

# Check size
ls -lh workdir/result/control--*.iso
# Should be ~9-10 GB for standard, ~12-15 GB for airgap

# Try mounting ISO
sudo mount -o loop workdir/result/control--ctrl-1--install-usb.iso /mnt
ls -la /mnt
sudo umount /mnt
```

**Solutions:**

1. **Rebuild with clean cache:**
   ```bash
   rm -rf ~/.hhfab-cache/v1/*
   rm -rf workdir/result/*
   hhfab --verbose build --mode iso 2>&1 | tee build.log
   ```

2. **Verify download integrity:**
   ```bash
   # Re-download specific artifact
   rm -rf ~/.hhfab-cache/v1/fabricator_k3s-airgap@*
   hhfab build --mode iso
   ```

---

## Installation Issues

### Issue: Installation fails with "network not ready"

**Symptoms:**
- `hhfab-install.service` fails
- Logs show network timeouts
- Installation retries and fails

**Diagnosis:**
```bash
# SSH to node
ssh core@<node-ip>

# Check network configuration
ip addr show
ip route show

# Check logs
journalctl -u hhfab-install.service | grep -i network
```

**Solutions:**

1. **Verify Ignition applied network config:**
   ```bash
   # Check systemd network files
   ls -la /etc/systemd/network/

   # Check specific interface config
   cat /etc/systemd/network/10-eth0.network

   # Restart networking
   sudo systemctl restart systemd-networkd
   sudo systemctl restart systemd-resolved
   ```

2. **Manually configure network:**
   ```bash
   # Create network config
   sudo cat > /etc/systemd/network/10-eth0.network <<EOF
   [Match]
   Name=eth0

   [Network]
   Address=10.10.10.10/24
   Gateway=10.10.10.1
   DNS=8.8.8.8
   EOF

   # Apply
   sudo systemctl restart systemd-networkd

   # Retry installation
   sudo systemctl restart hhfab-install.service
   ```

---

### Issue: K3s fails to start during installation

**Symptoms:**
- Installation hangs at K3s step
- `k3s.service` fails to start
- Timeout waiting for K3s

**Diagnosis:**
```bash
# Check K3s service status
sudo systemctl status k3s

# Check logs
sudo journalctl -u k3s | tail -100

# Check K3s config
cat /etc/rancher/k3s/config.yaml
```

**Solutions:**

1. **Check K3s configuration:**
   ```bash
   # Verify config syntax
   cat /etc/rancher/k3s/config.yaml

   # Common issues:
   # - Invalid IP addresses
   # - Incorrect TLS SANs
   # - Missing node-ip

   # Fix and restart
   sudo systemctl restart k3s
   ```

2. **Clean and reinstall K3s:**
   ```bash
   # Stop K3s
   sudo systemctl stop k3s

   # Remove K3s data
   sudo rm -rf /var/lib/rancher/k3s

   # Reinstall
   cd /opt/hedgehog/control--ctrl-1--install
   sudo INSTALL_K3S_SKIP_DOWNLOAD=true ./k3s-install.sh

   # Verify
   sudo systemctl status k3s
   kubectl get nodes
   ```

---

### Issue: Zot registry fails to start

**Symptoms:**
- Zot deployment not ready
- Installation times out waiting for Zot
- Pods in CrashLoopBackOff

**Diagnosis:**
```bash
# Check Zot pods
kubectl get pods -n zot

# Check pod logs
kubectl logs -n zot deployment/zot

# Check pod events
kubectl describe pod -n zot <pod-name>
```

**Solutions:**

1. **Check persistent volume:**
   ```bash
   # Check PVC status
   kubectl get pvc -n zot

   # Check storage class
   kubectl get storageclass

   # If PVC pending, check for storage issues
   kubectl describe pvc -n zot zot-data
   ```

2. **Restart Zot deployment:**
   ```bash
   # Delete pod to recreate
   kubectl delete pod -n zot -l app=zot

   # Wait for new pod
   kubectl wait --for=condition=ready pod -n zot -l app=zot --timeout=300s
   ```

---

### Issue: Certificate manager webhook not ready

**Symptoms:**
- Installation hangs at cert-manager step
- Webhook pod not responding
- Timeout errors

**Diagnosis:**
```bash
# Check cert-manager pods
kubectl get pods -n cert-manager

# Check webhook specifically
kubectl get pods -n cert-manager -l app=webhook

# Check logs
kubectl logs -n cert-manager deployment/cert-manager-webhook
```

**Solutions:**

1. **Wait longer (cert-manager can be slow):**
   ```bash
   # Manually wait for webhook
   kubectl wait --for=condition=available deployment/cert-manager-webhook \
     -n cert-manager --timeout=600s
   ```

2. **Restart cert-manager:**
   ```bash
   # Delete webhook pod
   kubectl delete pod -n cert-manager -l app=webhook

   # Wait for recreation
   kubectl wait --for=condition=available deployment/cert-manager-webhook \
     -n cert-manager --timeout=300s
   ```

---

## VLAB Issues

### Issue: VLAB fails to start VMs

**Symptoms:**
- `hhfab vlab up` fails
- No QEMU processes running
- Error messages about libvirt

**Diagnosis:**
```bash
# Check libvirt is running
sudo systemctl status libvirtd

# Check for existing VMs
virsh list --all

# Check network bridges
ip link show
brctl show
```

**Solutions:**

1. **Clean up stale resources:**
   ```bash
   # Kill stale processes
   hhfab vlab down --force

   # Kill any remaining QEMU
   sudo pkill -9 qemu

   # Remove stale VMs
   for vm in $(virsh list --all --name); do
     virsh destroy $vm
     virsh undefine $vm
   done

   # Retry
   hhfab vlab up --mode iso --recreate
   ```

2. **Restart libvirt:**
   ```bash
   sudo systemctl restart libvirtd
   hhfab vlab up --mode iso
   ```

---

### Issue: VLAB VMs won't boot

**Symptoms:**
- VMs created but don't boot
- Console shows no output
- Boot hangs

**Diagnosis:**
```bash
# Check VM status
virsh list --all

# Check VM console
virsh console control-1
# (Ctrl+] then Ctrl+5 to disconnect)

# Check VM logs
tail -f /var/log/libvirt/qemu/control-1.log
```

**Solutions:**

1. **Verify boot configuration:**
   ```bash
   # Check VM XML
   virsh dumpxml control-1 | grep -A 10 "<os>"

   # Ensure UEFI boot configured
   # Should have <loader> element pointing to OVMF
   ```

2. **Recreate VMs:**
   ```bash
   # Destroy and undefine
   virsh destroy control-1
   virsh undefine control-1

   # Rebuild VLAB
   hhfab vlab up --mode iso --recreate
   ```

---

### Issue: VLAB network not working

**Symptoms:**
- VMs can't communicate
- No network connectivity
- SSH fails

**Diagnosis:**
```bash
# Check bridges
brctl show

# Check IP forwarding
cat /proc/sys/net/ipv4/ip_forward
# Should be 1

# Check iptables
sudo iptables -L -n -v
```

**Solutions:**

1. **Enable IP forwarding:**
   ```bash
   # Enable temporarily
   sudo sysctl -w net.ipv4.ip_forward=1

   # Enable permanently
   echo "net.ipv4.ip_forward=1" | sudo tee -a /etc/sysctl.conf
   sudo sysctl -p
   ```

2. **Recreate network:**
   ```bash
   # Stop VLAB
   hhfab vlab down

   # Remove network configuration
   # (VLAB will recreate on next up)

   # Restart VLAB
   hhfab vlab up --mode iso
   ```

---

## Network Issues

### Issue: Nodes can't resolve DNS

**Symptoms:**
- `nslookup` fails
- Package downloads fail
- Kubectl commands timeout

**Diagnosis:**
```bash
# Check resolv.conf
cat /etc/resolv.conf

# Test DNS
nslookup google.com
dig google.com

# Test specific nameserver
nslookup google.com 8.8.8.8
```

**Solutions:**

1. **Configure DNS manually:**
   ```bash
   # Edit resolv.conf
   sudo cat > /etc/resolv.conf <<EOF
   nameserver 8.8.8.8
   nameserver 1.1.1.1
   EOF

   # Make immutable to prevent overwrites
   sudo chattr +i /etc/resolv.conf

   # Test
   nslookup google.com
   ```

2. **Fix systemd-resolved:**
   ```bash
   # Check resolved status
   sudo systemctl status systemd-resolved

   # Restart
   sudo systemctl restart systemd-resolved

   # Verify
   resolvectl status
   ```

---

### Issue: Control VIP not accessible

**Symptoms:**
- Can't access control services via VIP
- VIP not responding to ping
- Services only accessible via node IP

**Diagnosis:**
```bash
# Check VIP configuration
ip addr show | grep <vip>

# Check dummy interface
ip addr show dummy0

# Check routing
ip route show
```

**Solutions:**

1. **Verify VIP configured:**
   ```bash
   # Check if VIP assigned to dummy interface
   ip addr show dummy0

   # If missing, add manually
   sudo ip addr add <vip>/24 dev dummy0
   sudo ip link set dummy0 up
   ```

2. **Restart networking:**
   ```bash
   sudo systemctl restart systemd-networkd
   ip addr show dummy0
   ```

---

## Kubernetes Issues

### Issue: Pods stuck in Pending state

**Symptoms:**
- Pods don't start
- Status shows "Pending"
- No progress

**Diagnosis:**
```bash
# Describe pod to see events
kubectl describe pod <pod-name> -n <namespace>

# Check node resources
kubectl top nodes

# Check events
kubectl get events -n <namespace> --sort-by='.lastTimestamp'
```

**Solutions:**

1. **Resource constraints:**
   ```bash
   # If insufficient resources, clean up
   kubectl delete pod <unused-pod> -n <namespace>

   # Or scale down deployments
   kubectl scale deployment <name> --replicas=0 -n <namespace>
   ```

2. **PersistentVolumeClaim issues:**
   ```bash
   # Check PVC status
   kubectl get pvc -n <namespace>

   # If pending, check storage class
   kubectl get storageclass

   # Describe PVC for details
   kubectl describe pvc <pvc-name> -n <namespace>
   ```

---

### Issue: Service endpoints not ready

**Symptoms:**
- Service exists but has no endpoints
- Connections refused
- Pods running but service not working

**Diagnosis:**
```bash
# Check service
kubectl get svc -n <namespace>

# Check endpoints
kubectl get endpoints -n <namespace>

# Check if endpoints match pods
kubectl get pods -n <namespace> -o wide
```

**Solutions:**

1. **Verify pod labels match service selector:**
   ```bash
   # Check service selector
   kubectl get svc <service-name> -n <namespace> -o yaml | grep -A 5 selector

   # Check pod labels
   kubectl get pods -n <namespace> --show-labels

   # Ensure labels match
   ```

2. **Restart pods:**
   ```bash
   # Delete pods to recreate
   kubectl delete pod -n <namespace> -l app=<app-name>

   # Wait for recreation
   kubectl wait --for=condition=ready pod -n <namespace> -l app=<app-name>
   ```

---

### Issue: ImagePullBackOff errors

**Symptoms:**
- Pods fail to start
- Status shows "ImagePullBackOff" or "ErrImagePull"
- Image pull errors in events

**Diagnosis:**
```bash
# Describe pod
kubectl describe pod <pod-name> -n <namespace>

# Check for image pull errors in events
kubectl get events -n <namespace> | grep -i pull
```

**Solutions:**

1. **Check image exists:**
   ```bash
   # Verify image in registry
   docker pull <image>:<tag>

   # Or use crictl on node
   sudo crictl pull <image>:<tag>
   ```

2. **Fix image pull secrets:**
   ```bash
   # Create secret if needed
   kubectl create secret docker-registry regcred \
     --docker-server=<registry> \
     --docker-username=<username> \
     --docker-password=<password> \
     -n <namespace>

   # Patch deployment to use secret
   kubectl patch deployment <name> \
     -n <namespace> \
     -p '{"spec":{"template":{"spec":{"imagePullSecrets":[{"name":"regcred"}]}}}}'
   ```

---

## Performance Issues

### Issue: Build is very slow

**Symptoms:**
- Build takes over 30 minutes
- Downloads are slow
- CPU at 100%

**Diagnosis:**
```bash
# Check network speed
wget --output-document=/dev/null http://speedtest.wdc01.softlayer.com/downloads/test100.zip

# Check CPU usage
top

# Check disk I/O
iostat -x 1 10
```

**Solutions:**

1. **Use local registry:**
   ```bash
   # Set up Zot locally (see README.md)

   # Configure hhfab to use local registry
   hhfab init --dev --registry-repo 127.0.0.1:30000

   # Subsequent builds will be much faster
   ```

2. **Move cache to SSD:**
   ```bash
   # Move cache to faster disk
   export HHFAB_CACHE_DIR=/mnt/ssd/hhfab-cache
   hhfab build --mode iso
   ```

3. **Use build cache:**
   ```bash
   # Don't clear cache between builds
   # Only rebuild when config changes

   # Builds with cache: ~2-3 minutes
   # Builds without: ~10-15 minutes
   ```

---

### Issue: VLAB VMs are slow

**Symptoms:**
- VMs respond slowly
- SSH sessions lag
- Installation takes very long

**Diagnosis:**
```bash
# Check host resources
top
free -h

# Check VM CPU allocation
virsh vcpuinfo control-1

# Check VM memory
virsh dominfo control-1 | grep memory
```

**Solutions:**

1. **Increase VM resources:**
   ```bash
   # Edit VLAB config to increase CPU/RAM
   # In vlab configuration, adjust:
   # - CPU cores
   # - Memory allocation
   ```

2. **Reduce concurrent VMs:**
   ```bash
   # Build only controls
   hhfab build --controls --no-gateways

   # Or run VLAB with fewer switches
   hhfab vlab gen --fabric-mode minimal
   ```

---

## Getting More Help

If issues persist after trying these solutions:

1. **Collect debug information:**
   ```bash
   ./hack/debug/collect-debug-info.sh > debug-info.txt
   ```

2. **Enable verbose logging:**
   ```bash
   hhfab --verbose <command> 2>&1 | tee verbose-output.log
   ```

3. **File a GitHub issue** with:
   - Problem description
   - Steps to reproduce
   - Debug information from step 1
   - Verbose output from step 2
   - Environment details (OS, versions, etc.)

---

## Related Documentation

- [Debug Guide](DEBUG_GUIDE.md) - Comprehensive debugging instructions
- [Usage Guide](../usage/USAGE_GUIDE.md) - Common usage patterns
- [README](../../README.md) - Project overview and setup

---

**Last Updated:** 2025-11-08
**Maintained By:** Hedgehog Fabricator Team
**Issue:** #8
