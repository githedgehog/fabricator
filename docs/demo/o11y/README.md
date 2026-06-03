# O11y Demo — Self-hosted Metrics & Logs Backend

A lightweight, self-hosted alternative to Grafana Cloud for the fabricator's
Alloy collectors that already run on switches, control nodes, and gateways.

## At a glance

`./setup.sh` deploys **VictoriaMetrics + VictoriaLogs + Grafana** into the
`demo` namespace (via the k3s helm-controller) and idempotently patches
`Fabricator/default` to add `local` Prometheus and Loki targets, so Alloy
starts pushing to the in-cluster services alongside any pre-existing
targets like `grafana_cloud`.

What you can do with it once it's up:

- **Browse pre-loaded Hedgehog dashboards** in Grafana — switch interface
  counters, fabric BGP, ASIC critical resources, platform sensors, agent
  stats, fabric logs (LogsQL-native), and Node Exporter.
- **Run ad-hoc PromQL or LogsQL queries** against switch metrics and logs
  in Grafana's Explore view (anonymous Editor unlocks Explore in OSS
  Grafana 12 — the curated dashboards stay tamper-proof via provisioning
  locks and an ephemeral Grafana DB).
- **Edit and re-deploy the bundled Fabric Logs dashboard** by editing
  [`fabric-logs.dashboard.json`](./fabric-logs.dashboard.json) and re-running
  `./setup.sh` — pushed as a ConfigMap, picked up by Grafana's file provider.
- **Reach Grafana from your laptop without a NodePort** via
  `./forward.sh`, which runs `kubectl port-forward` to bind Grafana on
  `localhost:31800`. Useful when the cluster's NodePort isn't externally
  reachable.
- **Lifecycle helpers** under this same directory: `./verify.sh`
  re-runs the post-install health probes any time; `./cleanup.sh` tears
  the whole demo down (including stripping the `local` Prometheus/Loki
  targets it added to the Fabricator CR). The peer
  [docs/demo/](../README.md) directory also has `setup-all.sh` /
  `verify-all.sh` / `forward-all.sh` / `cleanup-all.sh` that run both
  this demo and the GitOps demo together.

> **Demo only.** Single replica, no HA, data loss is acceptable. Not the right
> shape for production: airgap-friendly plugin install, HA replication of
> VM/VL, dashboard provisioning via ConfigMaps with versioned content.

## Prerequisites

- `kubectl` in `$PATH`, `KUBECONFIG` pointing at a Hedgehog fabricator k3s cluster
- Cluster has egress to:
  - public container registries (VM, VL, Grafana images)
  - `grafana.com` (the VL datasource plugin is fetched at pod start; dashboards are fetched at Helm install time)
- ~3Gi spare RAM and ~3 cores headroom on the target node
- ~55Gi free space on the storage class used by the VM/VL PVCs (default: `local-path`)

## Install

```
export KUBECONFIG=<your-kubeconfig>
./setup.sh
```

Re-run safely — `kubectl apply` reconciles the HelmChart objects and the
Fabricator patch is constant. The script ends with quick verification probes
against VictoriaMetrics, VictoriaLogs, and Grafana to confirm data is flowing.

### Optional knobs

| Env var            | Default       | Notes |
|--------------------|---------------|-------|
| `GRAFANA_NODEPORT` | `31800`       | NodePort the Grafana UI is exposed on |
| `STORAGE_CLASS`    | `local-path`  | StorageClass for the VM and VL PVCs   |

## What it deploys

| Kind          | Name              | Purpose                                                  |
|---------------|-------------------|----------------------------------------------------------|
| HelmChart     | `victoria-metrics` | VM single-node, 25Gi PVC, 7d retention                  |
| HelmChart     | `victoria-logs`    | VL single-node, 25Gi PVC, 7d retention, ≤80% disk usage |
| HelmChart     | `grafana`          | Stateless Grafana, NodePort `:31800`                    |
| StatefulSet   | `victoria-metrics` | VM pod                                                   |
| StatefulSet   | `victoria-logs`    | VL pod                                                   |
| Deployment    | `grafana`          | Grafana pod                                              |
| Service (NP)  | `grafana`          | NodePort access to the UI                                |
| Service       | `victoria-metrics` | ClusterIP `:8428`                                        |
| Service       | `victoria-logs`    | ClusterIP `:9428`                                        |
| PVC           | `*-victoria-…`     | 25Gi each (VM, VL); Grafana is ephemeral                 |

## Accessing Grafana

```
http://<control-node-ip>:31800
```

If that NodePort isn't reachable from your machine (no hostfwd, restrictive
network, etc.), run **[`./forward.sh`](./forward.sh)** locally — it
`kubectl port-forward`s Grafana to `http://localhost:31800`. Set
`ADDRESS=0.0.0.0 ./forward.sh` to bind on every interface and share the
port with the LAN.

Login is disabled by design. The anonymous user is given the **Editor** role
so the **Explore** view shows up in the left sidebar — you can run ad-hoc
PromQL against VictoriaMetrics or LogsQL against VictoriaLogs without
authoring a dashboard.

The curated dashboards are still safe: they're provisioned from disk with
`editable: false`, `disableDeletion: true`, `allowUiUpdates: false`, and the
Grafana API rejects save/delete on provisioned dashboards with HTTP 400. The
Grafana DB itself is ephemeral (`persistence.enabled: false`), so any new
dashboard a user creates is wiped on pod restart.

Why Editor and not Viewer? In OSS Grafana 12 the Viewer role lacks the
`datasources:explore` RBAC permission, and the legacy `viewers_can_explore`
flag was removed. Granting that permission via Grafana's RBAC provisioning
files is a Grafana Enterprise feature.

The pre-loaded
dashboards live under the `hedgehog` provider:

| Dashboard                                  | Datasource       | gnetId |
|--------------------------------------------|------------------|--------|
| Hedgehog Agent Stats                       | VictoriaMetrics  | 24389  |
| Hedgehog Switch Critical Resources         | VictoriaMetrics  | 24413  |
| Hedgehog Fabric                            | VictoriaMetrics  | 24414  |
| Hedgehog Switch Interface Counters         | VictoriaMetrics  | 24415  |
| Hedgehog Fabric Logs (local, bundled)      | VictoriaLogs     | —      |
| Hedgehog Fabric Platform Stats             | VictoriaMetrics  | 24417  |
| Hedgehog Node Exporter                     | VictoriaMetrics  | 24419  |

To add another dashboard from grafana.com, edit `setup.sh` (search for
`dashboards:`) and re-run. To edit the bundled `Hedgehog Fabric Logs`
dashboard (LogsQL queries, Switch dropdown), edit
[`fabric-logs.dashboard.json`](./fabric-logs.dashboard.json) and re-run —
the local dashboard ships via a separate `hedgehog-local` provider so the
grafana.com pulls aren't disturbed. The Fabric Logs dashboard is local
because grafana.com's 24416 is authored against Loki LogQL, which the
VictoriaLogs read API doesn't speak.

## What gets patched on the Fabricator CR

The script applies three JSON-merge patches to `Fabricator/default` in the
`fab` namespace, in order:

**1. Safety: refuse to proceed while *other* Prometheus/Loki targets
exist.** Before any HelmCharts or other patches are applied, the script
inspects `spec.config.observability.targets.{prometheus,loki}` and lists
every entry whose name isn't `local` (e.g. `grafana_cloud`). The demo
turns metrics up to a firehose (15s scrape, no relabel filter, no
`minimal` defaults — see patch 3) and any external sink would receive
the same flood — Grafana Cloud and similar typically bill per-sample,
so a real fabric can run to **\$1000s/month** unprompted.

The script therefore *fails closed*:

- Interactive: it prints the offending targets and asks `Delete the
  targets above and proceed? Anything other than 'yes' aborts:`. Any
  answer other than `yes`/`y` exits non-zero before anything is
  installed.
- Non-interactive (piped, called from `setup-all.sh`, CI): it exits
  non-zero **unless** `O11Y_DELETE_NONLOCAL=yes` is set, in which case
  it deletes without prompting.

Pyroscope targets are left alone (profiling volume is small).
`cleanup.sh` does **not** restore deleted targets — re-add them with
`kubectl edit fabricators.fabricator.githedgehog.com default -n fab` if
needed.

**2. Add `local` Prometheus + Loki targets** (the in-cluster Service URLs
that VM and VL listen on):

```yaml
spec:
  config:
    observability:
      targets:
        prometheus:
          local: { url: http://victoria-metrics.demo.svc.cluster.local:8428/api/v1/write }
        loki:
          local: { url: http://victoria-logs.demo.svc.cluster.local:9428/insert/loki/api/v1/push }
```

**3. Tune Alloy for a fuller demo signal:**

```yaml
spec:
  config:
    observability:
      defaults: null              # drop 'minimal' profile (sends more, by default)
    fabric:
      observability:
        agent: { metricsInterval: 15, metricsRelabel: null }
        unix:  { metricsInterval: 15, metricsRelabel: null }
    gateway:
      observability:
        dataplane: { metricsInterval: 15 }
        frr:       { metricsInterval: 15 }
        unix:      { metricsInterval: 15, metricsRelabel: null }
```

That's: every component's scrape interval drops from `60s` to `15s`, the
metric-name whitelist relabel (`keep` only e.g. `*_in_bits`, `*_status`,
…) is removed so the dashboards see the full surface, and the
`defaults: minimal` profile is removed so the per-component overrides
above actually take effect.

The fabricator controller then reconciles all of this onto the Alloy
configuration on switches, control nodes, and gateways on its next pass;
data starts appearing in Grafana shortly after.

> **Note:** `cleanup.sh` only restores the `local` targets to null. It
> does **not** put back any `grafana_cloud` targets it asked to delete,
> nor restore the original `metricsInterval`/`metricsRelabel`/`defaults`
> values. Re-apply them with `kubectl edit fabricators.fabricator.githedgehog.com default -n fab`
> if you need the originals back.

## Resource footprint

| Component       | requests           | limits             | PVC   | Retention | Disk cap |
|-----------------|--------------------|--------------------|-------|-----------|---------------------------------|
| VictoriaMetrics | 250m CPU, 256Mi    | 1 CPU, 1Gi         | 25Gi  | 7d        | `-storage.minFreeDiskSpaceBytes=5GiB` (read-only valve, not eviction) |
| VictoriaLogs    | 250m CPU, 256Mi    | 1 CPU, 1Gi         | 25Gi  | 7d        | `-retention.maxDiskUsagePercent=80`                                   |
| Grafana         | 100m CPU, 128Mi    | 500m CPU, 512Mi    | none  | n/a       | n/a (ephemeral; provisioning is the source of truth)                  |

Caveats worth knowing:

- **VictoriaMetrics OSS has no size-based eviction.** When `minFreeDiskSpaceBytes`
  is reached, VM flips the storage to read-only and rejects new writes — it
  does *not* delete old data. It self-recovers as time-based retention ages
  out month partitions, or when the PVC is grown.
- **VictoriaLogs always keeps the last 2 days** regardless of disk cap, so
  total usage can briefly exceed the configured percentage under a spike.
- **Grafana is AGPLv3.** Running the unmodified upstream image is fine; bundling
  or modifying it has redistribution obligations — see the research notes.

## Uninstall

```bash
# remove the stack
kubectl -n demo delete helmchart victoria-metrics victoria-logs grafana

# remove the PVCs (data is gone)
kubectl -n demo delete pvc -l app.kubernetes.io/name=victoria-metrics-single
kubectl -n demo delete pvc -l app.kubernetes.io/name=victoria-logs-single

# stop sending data to local backends — manually remove the targets:
kubectl -n demo edit fabricator default
#   delete spec.config.observability.targets.prometheus.local
#   delete spec.config.observability.targets.loki.local
```

The Fabricator patch is not auto-reversed; remove it explicitly if you don't
want Alloy to keep trying to push to the (now gone) services.

## Chart versions (edit the script to bump)

| Chart                     | Version  | App version |
|---------------------------|----------|-------------|
| `victoria-metrics-single` | `0.39.0` | `v1.144.0`  |
| `victoria-logs-single`    | `0.13.3` | `v1.50.0`   |
| `grafana`                 | `10.5.15`| `12.3.1`    |
