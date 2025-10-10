# LGTM Images Setup for Upstream Registry

## Overview

The LGTM observability stack requires container images for Grafana, Loki, Tempo, and supporting components. These images must be pulled from Docker Hub and pushed to your upstream registry (e.g., `localhost:30000`). The Zot registry running in control-1 is configured to sync from the upstream registry on-demand.

This document describes how to populate the upstream registry with the required LGTM container images.

## Required Images

The following container images are required for the LGTM stack:

| Component | Source Image | Version | Upstream Registry Path |
|-----------|-------------|---------|------------------------|
| Grafana | `docker.io/grafana/grafana` | 12.1.1 | `localhost:30000/githedgehog/lgtm/images/grafana` |
| Loki | `docker.io/grafana/loki` | 3.3.2 | `localhost:30000/githedgehog/lgtm/images/loki` |
| Tempo | `docker.io/grafana/tempo` | 2.8.2 | `localhost:30000/githedgehog/lgtm/images/tempo` |
| Busybox | `docker.io/library/busybox` | 1.31.1 | `localhost:30000/githedgehog/lgtm/images/busybox` |

**Note**: These versions are the defaults from the upstream Grafana Helm charts and match the versions specified in the Fabricator's LGTM component configuration.

## Quick Setup

The fastest way to set up LGTM images is to use the provided script:

```bash
cd pkg/fab/comp/lgtm
chmod +x setup-lgtm-images.sh
./setup-lgtm-images.sh
```

The script will:
1. Pull all required images from Docker Hub
2. Tag them for the upstream registry
3. Push them to the upstream registry at `localhost:30000`
4. Verify the images are available

**Note**: The upstream registry should be the same one you specified with `--registry-repo` during `bin/hhfab init`. The control-1 Zot registry will automatically sync these images on-demand.

### Custom Registry

To use a different upstream registry address, set the `REGISTRY` environment variable:

```bash
REGISTRY=registry.example.com:5000 ./setup-lgtm-images.sh
```

## Manual Setup

If you prefer to set up the images manually or need to customize the process:

### 1. Pull Images from Docker Hub

```bash
docker pull docker.io/grafana/grafana:12.1.1
docker pull docker.io/grafana/loki:3.3.2
docker pull docker.io/grafana/tempo:2.8.2
docker pull docker.io/library/busybox:1.31.1
```

### 2. Tag Images for Upstream Registry

```bash
REGISTRY="localhost:30000"
REGISTRY_REPO="${REGISTRY}/githedgehog/lgtm/images"

docker tag docker.io/grafana/grafana:12.1.1 ${REGISTRY_REPO}/grafana:12.1.1
docker tag docker.io/grafana/loki:3.3.2 ${REGISTRY_REPO}/loki:3.3.2
docker tag docker.io/grafana/tempo:2.8.2 ${REGISTRY_REPO}/tempo:2.8.2
docker tag docker.io/library/busybox:1.31.1 ${REGISTRY_REPO}/busybox:1.31.1
```

### 3. Push Images to Upstream Registry

```bash
docker push ${REGISTRY_REPO}/grafana:12.1.1
docker push ${REGISTRY_REPO}/loki:3.3.2
docker push ${REGISTRY_REPO}/tempo:2.8.2
docker push ${REGISTRY_REPO}/busybox:1.31.1
```

### 4. Verify Images

Check that the images are available in the upstream registry:

```bash
curl -s http://localhost:30000/v2/githedgehog/lgtm/images/grafana/tags/list | jq .
curl -s http://localhost:30000/v2/githedgehog/lgtm/images/loki/tags/list | jq .
curl -s http://localhost:30000/v2/githedgehog/lgtm/images/tempo/tags/list | jq .
curl -s http://localhost:30000/v2/githedgehog/lgtm/images/busybox/tags/list | jq .
```

Expected output for each command:
```json
{
  "name": "githedgehog/lgtm/images/<component>",
  "tags": [
    "<version>"
  ]
}
```

## Updating Helm Values

After setting up the images in the local registry, you need to update the LGTM Helm chart values to use the local registry images instead of pulling from Docker Hub.

### Option 1: Update lgtm.go (Recommended)

Modify `/home/pau/fabricator/pkg/fab/comp/lgtm/lgtm.go` to include image repository overrides in the Helm values for each component:

For Grafana (around line 68):
```yaml
image:
  repository: 127.0.0.1:30000/githedgehog/lgtm/images/grafana
  tag: "12.1.1"
```

For Loki (around line 110):
```yaml
loki:
  image:
    repository: 127.0.0.1:30000/githedgehog/lgtm/images/loki
    tag: "3.3.2"
```

For Tempo (around line 151):
```yaml
tempo:
  image:
    repository: 127.0.0.1:30000/githedgehog/lgtm/images/tempo
    tag: "2.8.2"
```

### Option 2: Manual Helm Value Override

If you need to override without changing the code, you can manually edit the HelmChart resources after they're created:

```bash
kubectl edit helmchart grafana -n lgtm
kubectl edit helmchart loki -n lgtm
kubectl edit helmchart tempo -n lgtm
```

## Troubleshooting

### Image Pull Failures

**Symptom**: Pods show `ImagePullBackOff` or `ErrImagePull`

**Check pod events**:
```bash
kubectl describe pod -n lgtm <pod-name>
```

**Common causes**:
1. Images not in local registry - run the setup script
2. Wrong image path - verify the registry repository path
3. Registry not accessible - check that `127.0.0.1:30000` is reachable from the cluster

**Verify registry is accessible**:
```bash
curl -s http://127.0.0.1:30000/v2/_catalog | jq .
```

### Registry Connection Issues

**Symptom**: `docker push` fails with connection refused

**Solutions**:
1. Check that the Zot registry pod is running:
   ```bash
   kubectl get pod -n kube-system -l app=registry
   ```

2. Verify the NodePort service is exposing port 30000:
   ```bash
   kubectl get svc -n kube-system registry -o yaml | grep nodePort
   ```

3. Check that the registry is listening on the expected port:
   ```bash
   curl -s http://127.0.0.1:30000/v2/ | jq .
   ```

### Version Mismatches

**Symptom**: Pods start but crash with compatibility errors

**Check versions**:
```bash
# Check what versions are configured in the Fabricator status
kubectl get fabricator default -n fab -o jsonpath='{.status.versions.lgtm}' | jq .

# Check what versions are in the registry
curl -s http://127.0.0.1:30000/v2/githedgehog/lgtm/images/grafana/tags/list | jq .
```

**Solution**: Ensure the image versions in the registry match the versions specified in `cfg.Status.Versions.LGTM.*`

### Busybox Image Issues

**Symptom**: Init containers fail with busybox image pull errors

**Note**: The busybox image is used by:
- Loki chart init containers
- Tempo chart init containers
- local-path-provisioner helper pods

**Solution**: Ensure busybox:1.31.1 is pushed to the registry and accessible. The local-path-provisioner may need to be configured to use the local registry image as well.

## Integration with VLAB

When using VLAB (Virtual Lab) for testing:

1. **Run the setup script before starting VLAB**:
   ```bash
   cd pkg/fab/comp/lgtm
   ./setup-lgtm-images.sh
   ```

2. **The registry persists across VLAB restarts** - you only need to run this once unless you need to update image versions

3. **Monitor image availability during VLAB startup**:
   ```bash
   bin/hhfab vlab ssh -n control-1
   kubectl get pods -n lgtm -w
   ```

## See Also

- [LGTM Charts Setup](./LGTM-CHARTS-SETUP.md) - Setting up LGTM Helm charts in the registry
- [Observability Node Implementation](../../../OBSERVABILITY-NODE-IMPLEMENTATION.md) - Overall observability node design
- [Fabricator LGTM Configuration](../../../../api/fabricator/v1beta1/fabricator_types.go) - API types for LGTM configuration
