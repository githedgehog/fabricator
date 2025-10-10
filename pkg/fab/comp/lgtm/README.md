# LGTM Stack Component

This component deploys the LGTM observability stack (Loki, Grafana, Tempo, Mimir) on observability FabNodes.

## Overview

The LGTM stack provides a complete observability solution for Hedgehog Fabric:

- **Grafana**: Visualization and dashboards
- **Loki**: Log aggregation and querying
- **Tempo**: Distributed tracing
- **Mimir**: Long-term metrics storage (optional)

## Architecture

### Deployment Model

- **Namespace**: `lgtm` (separate from `fab` namespace)
- **Target Nodes**: Only runs on FabNodes with `observability` role
- **Node Affinity**: Enforced via node selectors and tolerations
- **Internet Access**: Observability nodes have unrestricted internet access via usernet

### Components

1. **Grafana** (NodePort 31001)
   - Pre-configured datasources for Loki, Tempo, and Mimir
   - Default credentials: admin/admin
   - Persistent volume: 10Gi

2. **Loki** (SingleBinary mode)
   - Filesystem storage backend
   - Persistent volume: 20Gi
   - Service: `loki-gateway.lgtm.svc.cluster.local`

3. **Tempo**
   - Local storage for traces
   - Persistent volume: 20Gi
   - Service: `tempo.lgtm.svc.cluster.local:3100`

4. **Mimir** (optional)
   - Filesystem storage for metrics
   - Service: `mimir-query-frontend.lgtm.svc.cluster.local:8080`

## Configuration

### Fabricator Config

```yaml
apiVersion: fabricator.githedgehog.com/v1beta1
kind: Fabricator
metadata:
  name: default
  namespace: fab
spec:
  config:
    lgtm:
      enable: true
      grafana:
        enabled: true
      loki:
        enabled: true
      tempo:
        enabled: true
      mimir:
        enabled: false  # Optional

    observability:
      targets:
        logs:
          url: http://loki-gateway.lgtm.svc.cluster.local/loki/api/v1/push
        metrics:
          url: http://mimir-query-frontend.lgtm.svc.cluster.local:8080/api/v1/push
        traces:
          url: http://tempo.lgtm.svc.cluster.local:4317
```

### Observability Node

```yaml
apiVersion: fabricator.githedgehog.com/v1beta1
kind: FabNode
metadata:
  name: lgtm-01
  namespace: fab
spec:
  roles:
    - observability
  bootstrap:
    disk: /dev/vda
  management:
    interface: enp2s1
    ip: 172.30.0.20/21
  external:
    interface: enp2s0
    ip: dhcp
  dummy:
    ip: 172.30.90.20/31
```

## Helm Charts

The component uses official Grafana Helm charts:

- **Grafana**: v11.4.0 from `grafana/grafana`
- **Loki**: v6.24.0 from `grafana/loki`
- **Tempo**: v1.17.3 from `grafana/tempo`
- **Mimir**: v5.6.0 from `grafana/mimir-distributed`

Charts are pulled from official repos and pushed to the local Zot registry during build.

## Implementation Details

### Chart References

Charts are stored in the local registry under:
- `lgtm/charts/grafana`
- `lgtm/charts/loki`
- `lgtm/charts/tempo`
- `lgtm/charts/mimir`

### Node Scheduling

All LGTM pods use:

```yaml
nodeSelector:
  node-role.fabricator.githedgehog.com/observability: "true"

tolerations:
  - key: node-role.fabricator.githedgehog.com/observability
    operator: Exists
    effect: NoExecute
```

This ensures pods only run on observability nodes.

## Usage

### Access Grafana

1. **Via NodePort**: `http://<control-node-ip>:31001`
2. **Via kubectl port-forward**:
   ```bash
   kubectl port-forward -n lgtm svc/grafana 3000:80
   ```

### Query Logs (Loki)

```bash
# Via Grafana UI
# Or via LogCLI
logcli --addr=http://loki-gateway.lgtm.svc.cluster.local query '{job="varlogs"}'
```

### Query Traces (Tempo)

Access via Grafana's Tempo datasource or query directly:
```bash
curl http://tempo.lgtm.svc.cluster.local:3100/api/search
```

## Monitoring

Check component status:

```bash
# Check all LGTM pods
kubectl get pods -n lgtm

# Check Grafana
kubectl get pods -n lgtm -l app.kubernetes.io/name=grafana

# Check Loki
kubectl get pods -n lgtm -l app.kubernetes.io/name=loki

# Check Tempo
kubectl get pods -n lgtm -l app.kubernetes.io/name=tempo
```

## Troubleshooting

### Pods not scheduling

Check node labels and taints:
```bash
kubectl get nodes -l node-role.fabricator.githedgehog.com/observability=true
kubectl describe node lgtm-01
```

### Storage issues

Check PVCs:
```bash
kubectl get pvc -n lgtm
kubectl describe pvc -n lgtm
```

### Chart deployment failures

Check HelmChart CRDs:
```bash
kubectl get helmcharts -n fab
kubectl describe helmchart -n fab grafana
```

## Future Enhancements

- [ ] Add Prometheus for metrics (alternative to Mimir)
- [ ] Configure retention policies
- [ ] Add pre-built Grafana dashboards for Fabric monitoring
- [ ] Support multi-node LGTM deployment for HA
- [ ] Add authentication/authorization (OAuth, LDAP)
- [ ] Configure alerting rules
