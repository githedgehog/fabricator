# LGTM Charts Setup for Local Registry

## Overview

The LGTM stack (Loki, Grafana, Tempo, Mimir) uses official Helm charts from Grafana Labs. For airgap installations and local development, these charts need to be manually pulled and pushed to your local registry.

## Chart Versions

Current versions (defined in `pkg/fab/versions.go`):

- **Grafana**: 10.0.0
- **Loki**: 6.42.0
- **Tempo**: 1.23.3
- **Mimir**: 5.8.0

## Quick Setup

### 1. Add Grafana Helm Repository

```bash
helm repo add grafana https://grafana.github.io/helm-charts
helm repo update
```

### 2. Pull Official Charts

```bash
helm pull grafana/grafana --version 10.0.0
helm pull grafana/loki --version 6.42.0
helm pull grafana/tempo --version 1.23.3
helm pull grafana/mimir-distributed --version 5.8.0
```

### 3. Push to Local Registry

```bash
helm push --plain-http grafana-10.0.0.tgz oci://127.0.0.1:30000/githedgehog/lgtm/charts
helm push --plain-http loki-6.42.0.tgz oci://127.0.0.1:30000/githedgehog/lgtm/charts
helm push --plain-http tempo-1.23.3.tgz oci://127.0.0.1:30000/githedgehog/lgtm/charts
helm push --plain-http mimir-distributed-5.8.0.tgz oci://127.0.0.1:30000/githedgehog/lgtm/charts
```

### 4. Verify Charts

```bash
curl http://127.0.0.1:30000/v2/githedgehog/lgtm/charts/grafana/tags/list
curl http://127.0.0.1:30000/v2/githedgehog/lgtm/charts/loki/tags/list
curl http://127.0.0.1:30000/v2/githedgehog/lgtm/charts/tempo/tags/list
curl http://127.0.0.1:30000/v2/githedgehog/lgtm/charts/mimir-distributed/tags/list
```

## All-in-One Script

```bash
#!/bin/bash
set -e

# Add Grafana repo
helm repo add grafana https://grafana.github.io/helm-charts
helm repo update

# Pull charts
helm pull grafana/grafana --version 10.0.0
helm pull grafana/loki --version 6.42.0
helm pull grafana/tempo --version 1.23.3
helm pull grafana/mimir-distributed --version 5.8.0

# Push to local registry
helm push --plain-http grafana-10.0.0.tgz oci://127.0.0.1:30000/githedgehog/lgtm/charts
helm push --plain-http loki-6.42.0.tgz oci://127.0.0.1:30000/githedgehog/lgtm/charts
helm push --plain-http tempo-1.23.3.tgz oci://127.0.0.1:30000/githedgehog/lgtm/charts
helm push --plain-http mimir-distributed-5.8.0.tgz oci://127.0.0.1:30000/githedgehog/lgtm/charts

# Cleanup
rm -f grafana-10.0.0.tgz loki-6.42.0.tgz tempo-1.23.3.tgz mimir-distributed-5.8.0.tgz

echo "✓ All LGTM charts pushed to local registry"
```

## Updating Chart Versions

When updating to newer chart versions:

1. Update versions in `pkg/fab/versions.go`:
   ```go
   LGTM: fabapi.LGTMVersions{
       Grafana: "10.0.0",  // Update this
       Loki:    "6.42.0",  // Update this
       Tempo:   "1.23.3",  // Update this
       Mimir:   "5.8.0",   // Update this
   },
   ```

2. Pull and push the new versions using the commands above

3. Rebuild and push fabricator:
   ```bash
   just lint && just hhfab-build
   just oci=http push
   ```

## Registry Path Convention

Charts are stored in the registry with the following path pattern:
```
127.0.0.1:30000/githedgehog/lgtm/charts/{chart-name}:{version}
```

Examples:
- `127.0.0.1:30000/githedgehog/lgtm/charts/grafana:10.0.0`
- `127.0.0.1:30000/githedgehog/lgtm/charts/loki:6.42.0`
- `127.0.0.1:30000/githedgehog/lgtm/charts/tempo:1.23.3`
- `127.0.0.1:30000/githedgehog/lgtm/charts/mimir-distributed:5.8.0`

Note: Mimir uses the chart name `mimir-distributed` (not just `mimir`)

## Troubleshooting

### Chart Not Found Error

If you see errors like:
```
repository name not known to registry
```

This means the charts haven't been pushed to the local registry yet. Run the setup script above.

### Version Mismatch

If the versions in `pkg/fab/versions.go` don't match what's in the registry, you'll get download errors. Ensure the versions match exactly.

## Future Work

TODO: Automate this process in the build pipeline, similar to how cert-manager charts are handled in `pkg/fab/comp/certmanager/`.
