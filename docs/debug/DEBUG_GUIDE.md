# Fabricator Debug Guide

**Version:** 1.0
**Date:** 2025-11-08
**Issue:** #8

## Overview

This guide provides comprehensive debugging instructions for Hedgehog Fabricator development. It covers common debugging scenarios, tools, techniques, and troubleshooting procedures.

## Table of Contents

1. [Debug Modes and Logging](#debug-modes-and-logging)
2. [Build Debug](#build-debug)
3. [VLAB Debug](#vlab-debug)
4. [Installation Debug](#installation-debug)
5. [Network Debug](#network-debug)
6. [Kubernetes Debug](#kubernetes-debug)
7. [Cache and Artifact Debug](#cache-and-artifact-debug)
8. [Debug Tools](#debug-tools)

---

## Debug Modes and Logging

### Verbose Mode

Enable verbose logging for detailed output:

```bash
# For hhfab commands
hhfab --verbose <command>
hhfab -v <command>

# Set via environment variable
export HHFAB_VERBOSE=true
hhfab <command>
```

### Brief Mode

Minimize output for clean logs:

```bash
hhfab --brief <command>
```

### Log Files

Default log locations:

| Component | Log Location | Description |
|-----------|-------------|-------------|
| hhfab | stdout/stderr | Main command output |
| hhfab-install | `/var/log/hhfab-install.log` | Installation logs on control nodes |
| systemd services | `journalctl -u <service>` | System service logs |
| K3s | `/var/log/k3s.log` | Kubernetes logs |
| fabricator controller | `kubectl logs -n fabricator-system` | Controller logs |

### View Installation Logs

On a control/gateway node after installation:

```bash
# View installation service logs
journalctl -u hhfab-install.service

# Follow live installation
journalctl -fu hhfab-install.service

# View specific time range
journalctl -u hhfab-install.service --since "10 minutes ago"

# Export logs to file
journalctl -u hhfab-install.service > install.log
```

---

## Build Debug

### Build Cache Debug

Check build cache status:

```bash
# View cache directory
ls -lah ~/.hhfab-cache/v1/

# Check cache size
du -sh ~/.hhfab-cache/

# Clear cache to force rebuild
rm -rf ~/.hhfab-cache/v1/*
```

### Build Hash Debug

Understand why builds are being skipped or rebuilt:

```bash
# View build hash files
find /path/to/workdir/result -name "*.inhash"

# Read hash file
cat /path/to/workdir/result/control--ctrl-1--install.inhash | base64 -d | xxd
```

The hash includes:
- Fabricator version
- `fab.yaml` content
- `include.yaml` content (wiring diagrams)
- Build mode

### Artifact Download Debug

Enable verbose logging to see download progress:

```bash
hhfab --verbose build --mode iso
```

Check for download errors:

```bash
# Test OCI registry connectivity
docker login ghcr.io

# Manually test artifact download
oras pull ghcr.io/githedgehog/fabricator/k3s-airgap:v1.34.1-k3s1
```

### Build Failure Debug

When build fails:

1. **Check verbose output:**
   ```bash
   hhfab --verbose build --mode iso 2>&1 | tee build-debug.log
   ```

2. **Verify prerequisites:**
   ```bash
   # Check Docker is running
   docker ps

   # Check disk space
   df -h

   # Check available memory
   free -h
   ```

3. **Check intermediate files:**
   ```bash
   ls -lah /path/to/workdir/result/
   ```

4. **Force clean rebuild:**
   ```bash
   # Remove result directory
   rm -rf /path/to/workdir/result

   # Clear cache
   rm -rf ~/.hhfab-cache/v1/*

   # Rebuild
   hhfab --verbose build --mode iso
   ```

---

## VLAB Debug

### VLAB Status Check

```bash
# Check VLAB status
hhfab vlab status

# Check VM processes
ps aux | grep qemu

# Check libvirt VMs (if using libvirt mode)
virsh list --all
```

### VLAB Network Debug

```bash
# Check network interfaces
ip link show

# Check bridges
brctl show

# Check routes
ip route

# Check iptables rules
sudo iptables -L -n -v
```

### VLAB Console Access

Access VM consoles for debugging:

```bash
# Connect to control node console
virsh console control-1

# Connect via serial (if configured)
screen /dev/pts/X

# Disconnect: Ctrl+] then Ctrl+5
```

### VLAB Log Files

```bash
# Check VLAB runner logs
hhfab --verbose vlab up --mode iso

# Check individual VM logs
tail -f /var/log/libvirt/qemu/control-1.log
```

---

## Installation Debug

### Pre-Installation Checks

Before troubleshooting failed installation:

```bash
# Verify network configuration
ip addr show
ip route show

# Verify DNS resolution
nslookup google.com

# Verify time sync
timedatectl status

# Check disk space
df -h
```

### Installation Service Debug

```bash
# Check service status
systemctl status hhfab-install.service

# View recent logs
journalctl -u hhfab-install.service -n 100

# Follow logs live
journalctl -fu hhfab-install.service

# Check for failures
journalctl -u hhfab-install.service -p err
```

### Installation Marker Files

```bash
# Check if installation completed
ls -la /opt/hedgehog/.install

# If exists and contains "complete", installation succeeded
cat /opt/hedgehog/.install
```

### Manual Installation Debug

Run installation steps manually:

```bash
# SSH to control node
ssh core@<control-ip>

# Navigate to installation directory
cd /opt/hedgehog/control--ctrl-1--install

# Run installer with verbose output
sudo ./hhfab-recipe install -v

# Check specific step output
sudo ./hhfab-recipe install -v 2>&1 | grep -A 10 "Step: Install K3s"
```

### K3s Installation Debug

```bash
# Check K3s service
systemctl status k3s

# Check K3s logs
journalctl -u k3s -n 100

# Check node status
kubectl get nodes

# Check pods
kubectl get pods -A
```

---

## Network Debug

### Interface Configuration Debug

```bash
# Show all interfaces
ip addr show

# Show specific interface
ip addr show eth0

# Check interface statistics
ip -s link show eth0

# Test connectivity
ping -c 3 <ip-address>
```

### DNS Debug

```bash
# Check DNS configuration
cat /etc/resolv.conf

# Test DNS resolution
nslookup google.com
dig google.com

# Test internal DNS
nslookup fabricator.fabricator-system.svc.cluster.local
```

### Firewall Debug

```bash
# Check iptables rules
sudo iptables -L -n -v

# Check for blocked ports
sudo iptables -L INPUT -n -v | grep DROP

# Temporarily disable firewall (for testing only)
sudo systemctl stop firewalld
```

---

## Kubernetes Debug

### Cluster Debug

```bash
# Check cluster info
kubectl cluster-info

# Check component status
kubectl get componentstatuses

# Check nodes
kubectl get nodes -o wide

# Check system pods
kubectl get pods -n kube-system
```

### Pod Debug

```bash
# Check pod status
kubectl get pods -A

# Describe pod (shows events)
kubectl describe pod <pod-name> -n <namespace>

# View pod logs
kubectl logs <pod-name> -n <namespace>

# View previous pod logs (if crashed)
kubectl logs <pod-name> -n <namespace> --previous

# Follow logs live
kubectl logs -f <pod-name> -n <namespace>

# Execute commands in pod
kubectl exec -it <pod-name> -n <namespace> -- /bin/bash
```

### Service Debug

```bash
# Check services
kubectl get svc -A

# Check endpoints
kubectl get endpoints -A

# Test service connectivity from pod
kubectl run -it --rm debug --image=alpine --restart=Never -- sh
# Inside pod:
wget -O- http://service-name.namespace.svc.cluster.local
```

### Events Debug

```bash
# View all events
kubectl get events -A --sort-by='.lastTimestamp'

# View events for specific namespace
kubectl get events -n fabricator-system

# Watch events live
kubectl get events -A --watch
```

---

## Cache and Artifact Debug

### Cache Directory Structure

```bash
# View cache structure
tree -L 2 ~/.hhfab-cache/v1/

# Check cache sizes
du -sh ~/.hhfab-cache/v1/*

# Find large artifacts
du -ah ~/.hhfab-cache/v1/ | sort -rh | head -20
```

### Artifact Verification

```bash
# Verify artifact checksums
cd ~/.hhfab-cache/v1/fabricator_k3s-airgap@v1.34.1-k3s1.oras/
sha256sum k3s k3s-install.sh k3s-airgap-images-amd64.tar.gz
```

### Registry Connection Debug

```bash
# Test registry connectivity
oras pull --insecure ghcr.io/githedgehog/fabricator/k3s-airgap:v1.34.1-k3s1

# Test with authentication
docker login ghcr.io
oras pull ghcr.io/githedgehog/fabricator/k3s-airgap:v1.34.1-k3s1
```

### Cache Cleanup

```bash
# Remove specific artifact from cache
rm -rf ~/.hhfab-cache/v1/fabricator_k3s-airgap@v1.34.1-k3s1.oras/

# Clear all cache
rm -rf ~/.hhfab-cache/v1/*

# Rebuild cache
hhfab build --mode iso
```

---

## Debug Tools

### Built-in Debug Commands

```bash
# Run any hhfab command with minimal Go flags (for development)
just run hhfab <args>

# Example: Test config loading
just run hhfab init --dev

# Run with specific log level
hhfab --verbose <command>
```

### Helper Scripts

Located in `hack/debug/`:

```bash
# Collect debug information
./hack/debug/collect-debug-info.sh

# Verify system requirements
./hack/debug/verify-system.sh

# Check VLAB health
./hack/debug/check-vlab-health.sh
```

See [Debug Scripts](#debug-scripts) section for script details.

### K9s (Kubernetes CLI)

Interactive Kubernetes debugging:

```bash
# Launch K9s
k9s

# Navigate:
# - 0-9: Switch contexts
# - : (colon): Command mode
# - /: Search
# - l: Logs
# - d: Describe
# - e: Edit
# - Ctrl+d: Delete
```

---

## Common Issues and Solutions

### Issue: Build hangs during artifact download

**Symptoms:**
- Build freezes at download step
- No progress output

**Diagnosis:**
```bash
# Check network connectivity
ping ghcr.io

# Check Docker authentication
cat ~/.docker/config.json
```

**Solution:**
```bash
# Re-authenticate with registry
docker login ghcr.io

# Retry build with verbose output
hhfab --verbose build --mode iso
```

---

### Issue: Installation fails with "network not ready"

**Symptoms:**
- Installation logs show network timeout errors
- `hhfab-install.service` fails

**Diagnosis:**
```bash
journalctl -u hhfab-install.service | grep -i "network"
ip addr show
```

**Solution:**
```bash
# Verify Ignition applied network config
cat /run/ignition.json | jq '.storage.files[] | select(.path=="/etc/systemd/network/*")'

# Restart networking
systemctl restart systemd-networkd

# Retry installation
systemctl restart hhfab-install.service
```

---

### Issue: K3s fails to start

**Symptoms:**
- K3s service fails
- Pods don't start

**Diagnosis:**
```bash
systemctl status k3s
journalctl -u k3s | tail -50
```

**Solution:**
```bash
# Check K3s config
cat /etc/rancher/k3s/config.yaml

# Check K3s binary
which k3s
k3s --version

# Restart K3s
systemctl restart k3s

# Check node status
kubectl get nodes
```

---

### Issue: Cache corruption

**Symptoms:**
- Build fails with "unexpected EOF" or checksum errors
- Artifacts appear incomplete

**Diagnosis:**
```bash
# Check cache integrity
ls -lh ~/.hhfab-cache/v1/fabricator_*

# Verify file sizes match expected
```

**Solution:**
```bash
# Clear cache
rm -rf ~/.hhfab-cache/v1/*

# Rebuild
hhfab build --mode iso
```

---

## Advanced Debugging

### Enable Go Debug Logging

For developers debugging Go code:

```bash
# Run with Go debugging
GODEBUG=gctrace=1 hhfab build --mode iso

# Run with pprof profiling
go run -race ./cmd/hhfab build --mode iso
```

### Attach Debugger (Delve)

```bash
# Install delve
go install github.com/go-delve/delve/cmd/dlv@latest

# Debug hhfab
dlv debug ./cmd/hhfab -- build --mode iso

# Set breakpoint
(dlv) break pkg/hhfab/cmdbuild.go:50

# Continue execution
(dlv) continue
```

### Network Packet Capture

```bash
# Capture network traffic
sudo tcpdump -i eth0 -w debug.pcap

# Analyze with Wireshark
wireshark debug.pcap
```

---

## Getting Help

If you're stuck:

1. **Check logs** with verbose output
2. **Review** this debug guide and troubleshooting guide
3. **Search** existing GitHub issues
4. **File an issue** with:
   - Command that failed
   - Full verbose output
   - Environment details (OS, kernel, Docker version)
   - Output of `./hack/debug/collect-debug-info.sh`

---

## Related Documentation

- [Usage Guide](../usage/USAGE_GUIDE.md) - Common usage patterns
- [Troubleshooting Guide](../debug/TROUBLESHOOTING.md) - Specific problem solutions
- [README](../../README.md) - Project overview and setup
- [Build Guide](../../README.md#local-build-instructions) - Building Fabricator

---

**Last Updated:** 2025-11-08
**Maintained By:** Hedgehog Fabricator Team
**Issue:** #8
