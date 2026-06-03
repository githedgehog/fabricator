# Hedgehog Fabricator Demos

Two self-contained, demo-grade walkthroughs that run on a fabricator
k3s cluster and showcase end-to-end observability and GitOps flows for
the fabric. Both install into a dedicated `demo` namespace (the
fabricator's own `fab` namespace stays untouched), use the k3s
helm-controller (no `helm` CLI required), and are idempotent.

## At a glance

| Demo | What it adds | UI URLs (NodePort) |
|---|---|---|
| [o11y/](./o11y/)     | VictoriaMetrics + VictoriaLogs + Grafana for the cluster's existing Alloy collectors. Pre-loaded Hedgehog dashboards. | Grafana `:31800`                  |
| [gitops/](./gitops/) | Gitea (Git server + UI) + Argo CD (engine + rich UI). The cluster's fabric CRs (VPCs, peerings, attachments) pre-seeded into a Git repo and live-reconciled via webhook. | Argo CD `:31900`, Gitea `:31901` |

## Quick start

```bash
export KUBECONFIG=<your-kubeconfig>

# install both
./setup-all.sh

# expose every UI on localhost (when the NodePorts aren't reachable
# externally — e.g. behind QEMU SLIRP hostfwd without those ports)
./forward-all.sh
```

When you're done:

```bash
./cleanup-all.sh
```

## Lifecycle scripts

All four are thin wrappers that call into the per-demo subdirectories.
You can also run just one demo by invoking its sub-script directly
(e.g. `./o11y/setup.sh`).

| Script             | What it does                                                                   |
|--------------------|--------------------------------------------------------------------------------|
| `setup-all.sh`     | Install o11y, then gitops. Each sub-script is itself idempotent.               |
| `cleanup-all.sh`   | Tear gitops down first (so Argo CD's controller doesn't fight teardown), then o11y. Removes HelmCharts, PVCs, label-selected leftovers, cluster-scoped RBAC. o11y also strips the `local` Prometheus/Loki targets it added to the Fabricator CR. |
| `verify-all.sh`    | Re-run both demos' post-install health checks any time. Exits non-zero with the count of failed probes. |
| `forward-all.sh`   | `kubectl port-forward` for every NodePort Service (Grafana, Argo CD, Gitea) in a single foreground process. `ADDRESS=0.0.0.0 ./forward-all.sh` binds on every interface. |

Each demo also has its own `setup.sh` / `cleanup.sh` / `verify.sh` /
`forward.sh` under `o11y/` and `gitops/` if you want finer control.

## Demo-grade caveats

- **Single replica everywhere.** No HA, no backups.
- **Pre-seeded credentials**: Grafana is anonymous-Editor (no login);
  Gitea admin/admin for the webui edit path; Argo CD anonymous read-only
  + chart-generated admin password for write operations. Don't expose
  the NodePorts outside a trusted network.
- **Cluster-admin RBAC on the GitOps applier.** Argo CD's
  application-controller has the chart's default cluster-admin
  ClusterRoleBinding so it can apply any CR.
- **`fab` namespace stays clean** — except for the Fabricator CR itself,
  which the o11y demo patches in place to add `local` Prometheus/Loki
  targets alongside any pre-existing ones (e.g. `grafana_cloud`). The
  o11y cleanup strips those targets.
