# Fabricator Usage Guide

**Version:** 1.0
**Date:** 2025-11-08
**Issue:** #8

## Overview

This guide provides comprehensive usage instructions for Hedgehog Fabricator, covering common workflows, examples, and best practices for developers and operators.

## Table of Contents

1. [Quick Start](#quick-start)
2. [Project Initialization](#project-initialization)
3. [Configuration](#configuration)
4. [Building Images](#building-images)
5. [VLAB Operations](#vlab-operations)
6. [Testing](#testing)
7. [Common Workflows](#common-workflows)
8. [Best Practices](#best-practices)

---

## Quick Start

### Prerequisites

Ensure you have all required tools installed:

```bash
# Verify Go version (v1.24.0+)
go version

# Verify Docker (v17.03+)
docker --version

# Verify Just (v1.36.0+)
just --version

# Verify Zot is running (optional for local registry)
curl http://127.0.0.1:30000/v2/_catalog
```

### Clone and Build

```bash
# Clone the repository
git clone https://github.com/githedgehog/fabricator.git
cd fabricator

# Set up local registry (one-time setup)
# See README.md for Zot installation instructions

# Build all artifacts
just push

# Verify binaries created
ls -lh bin/
```

---

## Project Initialization

### Initialize New Fabric Configuration

```bash
# Initialize with defaults
hhfab init --dev

# Initialize with custom options
hhfab init \
  --dev \
  --registry-repo 127.0.0.1:30000 \
  --default-password-hash '$6$rounds=4096$...' \
  --default-authorized-keys "$(cat ~/.ssh/id_rsa.pub)"

# Initialize with TLS SANs
hhfab init \
  --dev \
  --tls-san "fabric.example.com" \
  --tls-san "10.10.10.10"
```

This creates:
- `fab.yaml` - Main configuration file
- `wiring/` - Directory for wiring diagrams

### Import Existing Configuration

```bash
# Import from host
hhfab import \
  --import-host-upstream https://existing-fabric.example.com:6443 \
  --config /path/to/config
```

---

## Configuration

### Edit Fabric Configuration

Edit `fab.yaml`:

```yaml
apiVersion: fabricator.githedgehog.com/v1beta1
kind: Fabricator
metadata:
  name: default
spec:
  config:
    # Registry configuration
    repo: ghcr.io
    prefix: githedgehog
    registryMode: proxy  # or airgap, upstream

    # Default credentials
    defaultUser: admin
    defaultPasswordHash: $6$rounds=4096$...
    defaultAuthorizedKeys:
      - ssh-rsa AAAAB3NzaC1...

    # Version overrides (optional)
    versions:
      platform:
        k3s: v1.34.1-k3s1
        zot: v2.1.9
```

### Create Wiring Diagrams

Create files in `wiring/` directory:

```bash
# Example: wiring/fabric.yaml
apiVersion: wiring.githedgehog.com/v1beta1
kind: Fabric
metadata:
  name: my-fabric
spec:
  spines:
    - name: spine-1
      asn: 65100
    - name: spine-2
      asn: 65100
  leaves:
    - name: leaf-1
      asn: 65101
    - name: leaf-2
      asn: 65102
```

### Validate Configuration

```bash
# Validate configuration files
hhfab config validate

# Show merged configuration
hhfab config show
```

---

## Building Images

### Build Controller ISO

Standard build for control nodes:

```bash
# Build ISO (default)
hhfab build --mode iso

# Build only controls (skip gateways)
hhfab build --controls --no-gateways

# Build with verbose output
hhfab --verbose build --mode iso
```

Output location: `workdir/result/control--<name>--install-usb.iso`

### Build USB Image

For USB installation media:

```bash
# Build bootable USB image
hhfab build --mode usb
```

Output: `workdir/result/control--<name>--install-usb.img`

To write to USB drive:

```bash
sudo dd if=workdir/result/control--ctrl-1--install-usb.img \
  of=/dev/sdX \
  bs=4M \
  status=progress \
  conv=fsync
```

### Build Manual Installation

Separate archive and ignition config:

```bash
# Build manual installation bundle
hhfab build --mode manual
```

Outputs:
- `control--<name>--install.tgz` - Installation archive
- `control--<name>--install.ign` - Ignition configuration

### Build with Custom Registry

```bash
# Use local registry
hhfab build \
  --mode iso \
  --config fab.yaml

# Specify registry in fab.yaml:
# spec.config.repo: 127.0.0.1:30000
# spec.config.prefix: githedgehog
```

### Build in Airgap Mode

For offline installations:

```yaml
# In fab.yaml
spec:
  config:
    registryMode: airgap
```

```bash
# Build with all artifacts embedded
hhfab build --mode iso
```

**Note:** Airgap builds are significantly larger (~12-15 GB vs ~9.5 GB)

---

## VLAB Operations

### Generate VLAB Configuration

```bash
# Generate basic VLAB
hhfab vlab gen

# Generate with specific options
hhfab vlab gen \
  --fabric-mode spine-leaf \
  --control-node-mgmt-link direct \
  --gateway
```

### Start VLAB

```bash
# Start VLAB with ISO mode
hhfab vlab up --mode iso

# Start with specific options
hhfab vlab up \
  --mode iso \
  --kill-stale \
  --recreate

# Wait for ready state
hhfab vlab up --mode iso --ready
```

### Monitor VLAB

```bash
# Check VLAB status
hhfab vlab status

# Check specific components
kubectl get nodes
kubectl get pods -A

# Access control node
ssh core@<control-ip>
```

### Stop VLAB

```bash
# Stop VLAB
hhfab vlab down

# Force cleanup
hhfab vlab down --force
```

### VLAB Console Access

```bash
# Connect to control node console
virsh console control-1

# Disconnect: Ctrl+] then Ctrl+5
```

---

## Testing

### Run Unit Tests

```bash
# Run all tests
just test

# Run specific package tests
go test ./pkg/hhfab/...

# Run with verbose output
go test -v ./pkg/...
```

### Run Integration Tests

```bash
# Run VLAB tests
hhfab vlab test

# Run with specific test filter
hhfab vlab test --regex "TestNetworking"

# Run extended tests
hhfab vlab test --extended
```

### Run Security Scans

```bash
# Run Trivy security scan
just security-scan

# Run control-only scan
just security-scan --control-only

# Run with strict mode
just security-scan --strict
```

---

## Common Workflows

### Workflow 1: Local Development Build

```bash
# 1. Make code changes
vim pkg/hhfab/cmdbuild.go

# 2. Build and push to local registry
just oci=http push

# 3. Verify binaries
ls -lh bin/hhfab

# 4. Test locally
bin/hhfab build --mode iso --registry-repo 127.0.0.1:30000
```

### Workflow 2: Test Configuration Changes

```bash
# 1. Update configuration
vim fab.yaml

# 2. Validate
hhfab config validate

# 3. Rebuild
hhfab build --mode iso

# 4. Test in VLAB
hhfab vlab up --mode iso --recreate
```

### Workflow 3: Upgrade Testing

```bash
# 1. Build new version
just push

# 2. Start VLAB with old version
hhfab vlab up --mode iso

# 3. Upgrade to new version
hhfab upgrade --version <new-version>

# 4. Verify upgrade
kubectl get nodes
kubectl get pods -A
```

### Workflow 4: Multi-Platform Build

```bash
# Build for all supported platforms
just build-multi

# Outputs in bin/:
# - hhfab-linux-amd64.tar.gz
# - hhfab-linux-arm64.tar.gz
# - hhfab-darwin-amd64.tar.gz
# - hhfab-darwin-arm64.tar.gz
```

### Workflow 5: Airgap Deployment

```bash
# 1. Configure airgap mode
cat > fab.yaml <<EOF
spec:
  config:
    registryMode: airgap
EOF

# 2. Build airgap ISO
hhfab build --mode iso

# 3. Transfer ISO to offline system
scp workdir/result/control--*.iso offline-system:/tmp/

# 4. Install on offline system
# Boot from ISO, installation proceeds without internet
```

---

## Best Practices

### 1. Use Version Control for Configuration

```bash
# Initialize git repository for config
git init
git add fab.yaml wiring/
git commit -m "Initial fabric configuration"
```

### 2. Use Local Registry for Development

```bash
# Always specify local registry
hhfab build --mode iso --registry-repo 127.0.0.1:30000

# Or set in fab.yaml
spec:
  config:
    repo: 127.0.0.1:30000
```

### 3. Enable Verbose Logging for Debugging

```bash
# Set environment variable
export HHFAB_VERBOSE=true

# Or use flag
hhfab --verbose <command>
```

### 4. Clean Cache Regularly

```bash
# Check cache size
du -sh ~/.hhfab-cache/

# Clear old artifacts
find ~/.hhfab-cache/v1/ -mtime +30 -delete

# Full cache clear
rm -rf ~/.hhfab-cache/v1/*
```

### 5. Use Build Cache for Speed

```bash
# First build (downloads all artifacts)
hhfab build --mode iso  # Takes ~10-15 minutes

# Subsequent builds (uses cache)
hhfab build --mode iso  # Takes ~2-3 minutes if no config changes
```

### 6. Test Before Deployment

```bash
# Always test in VLAB first
hhfab vlab gen
hhfab vlab up --mode iso
hhfab vlab test

# Verify before deploying to production
hhfab vlab status
kubectl get nodes
kubectl get pods -A
```

### 7. Document Custom Configurations

```bash
# Add comments to fab.yaml
apiVersion: fabricator.githedgehog.com/v1beta1
kind: Fabricator
metadata:
  name: default
spec:
  config:
    # Using airgap mode for datacenter deployment
    registryMode: airgap

    # Custom K3s version for stability
    versions:
      platform:
        k3s: v1.34.1-k3s1  # Tested and verified
```

### 8. Backup Configurations

```bash
# Backup before major changes
tar -czf fabric-config-$(date +%Y%m%d).tar.gz \
  fab.yaml wiring/ workdir/

# Restore if needed
tar -xzf fabric-config-20251108.tar.gz
```

### 9. Monitor Build Sizes

```bash
# Check ISO size before deployment
ls -lh workdir/result/*.iso

# Expected sizes:
# - Standard: ~9.5 GB
# - Airgap: ~12-15 GB

# If size is unexpected, check verbose build output
```

### 10. Use Just for Common Tasks

```bash
# List available commands
just

# Common tasks
just push          # Build and push all artifacts
just build         # Build only
just test          # Run tests
just lint          # Run linters
just all           # Generate, lint, test, build everything
```

---

## Environment Variables

Supported environment variables:

| Variable | Description | Default |
|----------|-------------|---------|
| `HHFAB_VERBOSE` | Enable verbose logging | `false` |
| `HHFAB_CACHE_DIR` | Cache directory location | `~/.hhfab-cache` |
| `HHFAB_PREVIEW` | Enable preview features | `false` |

Example usage:

```bash
# Set for current session
export HHFAB_VERBOSE=true
export HHFAB_CACHE_DIR=/mnt/large-disk/cache

# Run command with variables
HHFAB_VERBOSE=true hhfab build --mode iso
```

---

## Tips and Tricks

### Tip 1: Speed Up Builds

```bash
# Use local registry for faster artifact access
hhfab init --dev --registry-repo 127.0.0.1:30000

# Keep cache directory on fast SSD
export HHFAB_CACHE_DIR=/mnt/ssd/hhfab-cache
```

### Tip 2: Quick VLAB Iteration

```bash
# Generate once, reuse config
hhfab vlab gen
# ... make changes ...
hhfab vlab up --mode iso --recreate --kill-stale
```

### Tip 3: Parallel Development

```bash
# Use separate work directories for different configs
hhfab build --mode iso --config /path/to/config1
hhfab build --mode iso --config /path/to/config2
```

### Tip 4: Extract ISO Contents

```bash
# Mount ISO to inspect contents
sudo mount -o loop control--ctrl-1--install-usb.iso /mnt
ls -la /mnt
sudo umount /mnt
```

### Tip 5: Quick Config Validation

```bash
# Validate without building
hhfab config validate

# Show effective configuration
hhfab config show | less
```

---

## Troubleshooting Quick Reference

Common issues and quick fixes:

| Issue | Quick Fix |
|-------|-----------|
| Build cache errors | `rm -rf ~/.hhfab-cache/v1/*` |
| Registry auth fails | `docker login ghcr.io` |
| Build hangs | Check network, firewall, disk space |
| VLAB won't start | `hhfab vlab down --force && hhfab vlab up --mode iso --recreate` |
| K3s fails | `journalctl -u k3s \| tail -50` |

For detailed troubleshooting, see [Debug Guide](../debug/DEBUG_GUIDE.md) and [Troubleshooting Guide](../debug/TROUBLESHOOTING.md).

---

## Getting Help

- **Debug Guide:** [docs/debug/DEBUG_GUIDE.md](../debug/DEBUG_GUIDE.md)
- **Troubleshooting:** [docs/debug/TROUBLESHOOTING.md](../debug/TROUBLESHOOTING.md)
- **API Docs:** [docs/api.md](../api.md)
- **GitHub Issues:** [github.com/githedgehog/fabricator/issues](https://github.com/githedgehog/fabricator/issues)

---

## Related Documentation

- [README](../../README.md) - Project overview and setup
- [Debug Guide](../debug/DEBUG_GUIDE.md) - Comprehensive debugging instructions
- [Troubleshooting Guide](../debug/TROUBLESHOOTING.md) - Specific problem solutions
- [Trivy Scans](../../hack/README_Trivy_Scans.md) - Security scanning guide
- [Test Diagrams](../../hack/README_Test_Diagrams.md) - Testing documentation

---

**Last Updated:** 2025-11-08
**Maintained By:** Hedgehog Fabricator Team
**Issue:** #8
