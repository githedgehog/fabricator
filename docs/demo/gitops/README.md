# GitOps Demo — Self-hosted Gitea + Argo CD

A lightweight GitOps stack the fabricator can run on its own control node.

## At a glance

`./setup.sh` deploys **Gitea + Argo CD** into the `demo` namespace,
pre-seeds the cluster's existing fabric CRs (VPC, VPCAttachment,
VPCPeering, ExternalPeering, GatewayPeering from `default`) into a
new Gitea repo, applies an Argo CD `Application` pointing at it, and
wires a Gitea→Argo CD webhook so commits reconcile in ~1–5 seconds.

What you can do with it once it's up:

- **Browse the cluster's fabric configuration** in Gitea — VPCs,
  attachments, peerings — as plain YAML files in one repo.
- **Edit a CR in the Gitea web editor**, click Commit changes, and
  watch Argo CD reconcile within seconds. The cluster updates from
  there; the fabricator's controllers propagate to switches.
- **Inspect live state, desired-vs-live diff, sync history, and the
  topology graph** in Argo CD's UI for the `fabric-config` Application.
- **Re-import current cluster state into git** by re-running
  `./setup.sh` (IMPORT MODE) — handy when someone `kubectl edit`s and
  you want git to match reality again. See the
  ["Re-running setup.sh re-imports"](#re-running-setupsh-re-imports)
  section for the IMPORT-MODE / CREATE-ONLY-MODE nonce dance.
- **Reach the UIs from your laptop without NodePorts** via
  `./forward.sh`, which runs `kubectl port-forward` to bind Argo CD on
  `localhost:31900` and Gitea on `localhost:31901`.
- **Drive the demo loop without manual clicks** via
  `./demo-loop.sh` — picks a VPC, changes one value through Gitea's API
  (same code path a webui edit takes), and times how long Argo CD + the
  cluster take to catch up. Pair with `--revert` to leave no trace.
- **Lifecycle helpers** under this same directory: `./verify.sh`
  re-runs the post-install health probes any time; `./cleanup.sh`
  tears the whole demo down. The peer [docs/demo/](../README.md)
  directory also has `setup-all.sh` / `verify-all.sh` / `forward-all.sh`
  / `cleanup-all.sh` that run both this demo and the o11y demo
  together.

> **Demo only.** Pre-seeded admin/admin password in Gitea, anonymous
> read-only Argo CD UI, cluster-admin for Argo CD's application-controller,
> no HA, no SSO. See the research notes for the production posture.

## Prerequisites

- `kubectl` in `$PATH`; `KUBECONFIG` pointing at a Hedgehog fabricator k3s cluster.
- Cluster has egress to public container registries (Gitea, Argo CD, `alpine/k8s` for the seed Job).
- ~1Gi spare RAM and ~2 cores headroom on the control node.
- ~6Gi free space on the storage class used by Gitea's PVC (default `local-path`).
- The `demo` namespace already exists (created by the fabricator).

## Install

```
export KUBECONFIG=<your-kubeconfig>
./setup.sh
```

Re-run safely — every step is idempotent (`kubectl apply` for declarative
resources; the seed Job is `IMPORT MODE` / `CREATE-ONLY MODE` aware — see below).

### Optional knobs

| Env var            | Default       | Notes                                       |
|--------------------|---------------|---------------------------------------------|
| `ARGOCD_NODEPORT`  | `31900`       | NodePort the Argo CD UI is exposed on      |
| `GITEA_NODEPORT`   | `31901`       | NodePort the Gitea UI is exposed on        |
| `STORAGE_CLASS`    | `local-path`  | StorageClass for Gitea's 5Gi PVC           |

## What gets deployed

| Kind          | Name                                  | Notes                                                              |
|---------------|---------------------------------------|--------------------------------------------------------------------|
| HelmChart     | `gitea`                               | Single-replica Gitea, SQLite, HTTP-only, NodePort `:31901`         |
| HelmChart     | `argo-cd`                             | Non-HA: 1× controller, server, repo-server, redis, applicationset  |
| Deployment    | `gitea`                               | Gitea pod (5Gi PVC)                                                 |
| StatefulSet   | `argocd-application-controller`       | Reconciles every Application against its source                    |
| Deployments   | `argocd-server`, `argocd-repo-server`, `argocd-redis`, `argocd-applicationset-controller` | The rest of Argo CD |
| Application   | `fabric-config`                       | Source: in-cluster Gitea repo; destination: `default` ns; auto-sync + prune |
| Job           | `gitops-demo-seed`                    | One-shot Gitea repo + manifest + webhook seeder                    |

Footprint (steady): ~640Mi requests / ~1.7Gi limits, ~300m CPU. Comfortable
on the control node alongside fabricator + o11y-demo.

## Accessing the UIs

```
Argo CD (GitOps UI):  http://<control-node-ip>:31900    (anonymous, read-only)
Gitea   (Git UI):     http://<control-node-ip>:31901    (anonymous browse; admin/admin to edit)
```

If those NodePorts aren't reachable from your machine (no hostfwd,
restrictive network, etc.), run **[`./forward.sh`](./forward.sh)** locally
— it `kubectl port-forward`s both Services to `http://localhost:31900`
(Argo CD) and `http://localhost:31901` (Gitea). Set
`ADDRESS=0.0.0.0 ./forward.sh` to bind on every interface and share with
the LAN.

The Argo CD UI loads straight into the Applications list — no login prompt
(anonymous user is enabled and bound to `role:readonly` so writes through the
UI are blocked, but every read action — diffs, history, logs — works).

**Gitea login credentials:** `admin` / `admin`. Login is only required to
**edit and commit** in the web editor; browsing the repo and history works
without login.

> Admin in Argo CD: the chart generates an initial admin password and stores
> it in the `argocd-initial-admin-secret` Secret. Get it with:
> ```bash
> kubectl -n demo get secret argocd-initial-admin-secret -o jsonpath='{.data.password}' | base64 -d
> ```
> You won't need it for the demo; just there if you want to sign in as
> admin (e.g. to click the **Sync** button manually).

## Demo flow

1. **Open Gitea** at `http://<node-ip>:31901/admin/fabric-config`. The repo
   has one folder per CR kind:

   ```
   fabric-config/
   ├── vpcs/vpc-01.yaml
   ├── vpcattachments/server-01--mclag--leaf-01--leaf-02--vpc-01--subnet-01.yaml
   ├── vpcpeerings/vpc-01--vpc-02.yaml
   ├── externalpeerings/external-1--vpc-1.yaml
   └── gatewaypeerings/vpc-02--vpc-03.yaml
   ```

2. **Open Argo CD** at `http://<node-ip>:31900` in another tab. Applications →
   `fabric-config`. You'll see the live cluster state visualised, the current
   sync status, and the sync history.

3. **Edit a YAML.** In Gitea, open e.g. `vpcs/vpc-01.yaml`, click the pencil,
   change a value (e.g. `spec.subnets.subnet-01.dhcp.range.end`), click
   **Commit changes**. Sign in as `admin / admin` if prompted.

4. **Watch the apply in Argo CD.** The Application transitions from
   `Synced` → briefly `OutOfSync` → back to `Synced`, and the new commit's
   short SHA appears at the top of the sync history. The whole cycle takes
   ~1–5 seconds via the Gitea→Argo CD webhook.

5. **Confirm in the cluster** if you want to be sure:
   ```bash
   kubectl get vpc/<name> -n default -o jsonpath='{.spec.subnets.subnet-01.dhcp.range.end}'
   ```

## What's in the repo and what's not

Pre-seeded from `default` namespace:

- `vpc.githedgehog.com/v1beta1`: `VPC`, `VPCAttachment`, `VPCPeering`, `ExternalPeering`
- `gateway.githedgehog.com/v1alpha1`: `GatewayPeering`

**Not** seeded — these are auto-generated topology / hardware definitions
and editing them would break the cluster:

- `Connection`, `Switch`, `SwitchGroup`, `SwitchProfile`, `IPv4Namespace`, `VLANNamespace`
- `External`, `ExternalAttachment`
- Everything in the `demo` namespace (the fabricator's own components)

### Re-running `setup.sh` re-imports

Each `./setup.sh` invocation passes a fresh nonce (`SETUP_RUN_ID`) to the
seed Job. The Job compares it against the previous value recorded in the
`gitops-demo-state` ConfigMap:

- **New nonce** (i.e. *you* re-ran `setup.sh`) → **IMPORT MODE**. The Job
  re-exports CRs from `default` and **overwrites** the matching files in
  git with current cluster state. New CRs that aren't yet in the repo
  also get added.
- **Same nonce** (a Job-pod retry within a single Job — e.g. the pod was
  evicted and K8s recreated it) → **CREATE-ONLY MODE**. The Job only adds
  files that don't yet exist; nothing is overwritten, so it can't clobber
  a webui edit that landed between attempts.

## How the webhook works

Argo CD's `argocd-server` exposes `/api/webhook`. The seed Job:

1. Reads the shared secret from `Secret/fabric-config-webhook-token` (whose
   `data.token` is a deterministic SHA256 of `kube-system`'s namespace UID,
   so re-runs are stable).
2. POSTs a Gitea webhook on the `push` event pointing at
   `http://argocd-server.demo.svc.cluster.local/api/webhook` with the secret.

The chart's HelmChart `valuesContent` block also injects the same secret
into `argocd-secret` under key `webhook.gitea.secret`. On every Gitea push,
Argo CD verifies the `X-Gitea-Signature` header against this secret and
triggers an immediate refresh of any Application whose source matches the
repo URL.

If the webhook ever misses, Argo CD's polling (`timeout.reconciliation=15s`)
re-discovers the new commit within 15 seconds.

## Resource footprint

| Component              | requests           | limits             | PVC  |
|------------------------|--------------------|--------------------|------|
| Gitea                  | 250m CPU, 256Mi    | 1 CPU, 1Gi         | 5Gi  |
| argocd-controller      | 100m CPU, 256Mi    | 500m CPU, 768Mi    | —    |
| argocd-server          | 50m CPU, 128Mi     | 300m CPU, 256Mi    | —    |
| argocd-repo-server     | 50m CPU, 128Mi     | 300m CPU, 256Mi    | —    |
| argocd-redis           | 50m CPU, 64Mi      | 200m CPU, 128Mi    | —    |
| argocd-applicationset  | 50m CPU, 64Mi      | 200m CPU, 128Mi    | —    |
| Seed Job (transient)   | 50m CPU, 64Mi      | 500m CPU, 256Mi    | —    |

## Limitations / known caveats

- **Demo only.** Single replica everywhere. No HA. No backups.
- **admin/admin** for Gitea is documented in plain text. Do not expose this
  NodePort outside the cluster's management network.
- **Argo CD's application-controller has cluster-admin** (default from the
  chart's `createClusterRoles: true`) so it can apply any CR. Production
  setups should narrow this with a custom Project + a tighter ClusterRole.
- **Mutating webhooks** on VPC/peering CRDs may rewrite some `spec` fields
  during apply (e.g. MAC normalization). Argo CD with `selfHeal=true` will
  see "drift" on the next reconcile and re-apply; harmless churn.
- **Repo is the source of truth.** If you `kubectl edit vpc/...` directly,
  Argo CD will revert it on the next reconcile (`prune: true`, `selfHeal: true`).
- **No TLS.** `server.insecure=true` — UI/API runs plain HTTP on the
  NodePort. Fine for the demo's internal-network usage.

## Uninstall

```bash
# Remove the Application first so Argo CD stops trying to sync as we tear down
kubectl -n demo delete application/fabric-config

# Remove HelmCharts (this also tears down the workloads and Services)
kubectl -n demo delete helmchart gitea argo-cd

# Drop the demo's ServiceAccount/CRB/secrets/configmaps/jobs (label-selected)
kubectl -n demo delete all,sa,cm,secret,job -l app.kubernetes.io/part-of=gitops-demo
kubectl delete clusterrolebinding gitops-demo-seeder --ignore-not-found
kubectl delete clusterrole gitops-demo-seeder --ignore-not-found

# Remove the Gitea PVC (data is gone)
kubectl -n demo delete pvc -l app.kubernetes.io/name=gitea

# Argo CD CRDs (chart keeps them on uninstall by default — drop only if
# nothing else uses them):
kubectl delete crd applications.argoproj.io applicationsets.argoproj.io appprojects.argoproj.io
```

## Chart versions (edit the script to bump)

| Chart      | Version  | Source                                           |
|------------|----------|--------------------------------------------------|
| `gitea`    | `12.6.0` | `https://dl.gitea.com/charts`                    |
| `argo-cd`  | `9.5.17` | `https://argoproj.github.io/argo-helm`           |
