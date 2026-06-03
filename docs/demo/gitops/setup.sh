#!/usr/bin/env bash
# Copyright 2026 Hedgehog
# SPDX-License-Identifier: Apache-2.0
#
# Stands up a lightweight, demo-grade GitOps stack on a Hedgehog fabricator
# k3s cluster: Gitea (git server + web UI) + Argo CD (GitOps engine + UI).
#
# Pre-seeds the Gitea repo with the cluster's existing VPC / peering /
# attachment CRs from the `default` namespace, creates an Argo CD Application
# pointing at that repo, and wires a Gitea webhook into Argo CD for ~5s
# reconcile latency. 15s polling kicks in if the webhook ever misses.
#
# See ./README.md for the full demo walkthrough.

set -euo pipefail

# --- pinned versions -----------------------------------------------------------
GITEA_CHART_REPO="https://dl.gitea.com/charts"
GITEA_CHART_VERSION="12.6.0"
ARGOCD_CHART_REPO="https://argoproj.github.io/argo-helm"
ARGOCD_CHART_VERSION="9.5.17"   # app v3.4.3

# Image used by the seed Job (has kubectl, curl, jq, yq, bash).
SEED_IMAGE="alpine/k8s:1.31.0"

# --- knobs ---------------------------------------------------------------------
NAMESPACE="demo"
DEFAULT_NAMESPACE="default"
ARGOCD_NODEPORT="${ARGOCD_NODEPORT:-31900}"
GITEA_NODEPORT="${GITEA_NODEPORT:-31901}"
STORAGE_CLASS="${STORAGE_CLASS:-local-path}"

SCRIPT_DIR="$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )"

# Per-invocation nonce. Each ./setup.sh run produces a fresh value, which is
# passed to the seed Job. Inside the Job, this is compared against the value
# recorded in the gitops-demo-state ConfigMap. New nonce → IMPORT MODE (re-pull
# cluster state into git, overwriting). Same nonce → CREATE-ONLY MODE (a Job
# pod retry; never clobber a user's webui edits).
SETUP_RUN_ID="$(date +%s)-${RANDOM}"

# --- pretty -------------------------------------------------------------------
if [[ -t 1 ]]; then
  C_GREEN=$'\033[0;32m' C_YELLOW=$'\033[0;33m' C_RED=$'\033[0;31m' C_NC=$'\033[0m'
else
  C_GREEN="" C_YELLOW="" C_RED="" C_NC=""
fi

info()  { printf "%s==>%s %s\n" "${C_GREEN}" "${C_NC}" "$*"; }
warn()  { printf "%swarn:%s %s\n" "${C_YELLOW}" "${C_NC}" "$*" >&2; }
die()   { printf "%serror:%s %s\n" "${C_RED}" "${C_NC}" "$*" >&2; exit 1; }

usage() {
  cat <<EOF
Usage: $(basename "$0") [--help]

Environment overrides:
  ARGOCD_NODEPORT   NodePort for Argo CD UI (default: 31900)
  GITEA_NODEPORT    NodePort for Gitea UI   (default: 31901)
  STORAGE_CLASS     StorageClass for the Gitea PVC (default: local-path)

KUBECONFIG must be set to a Hedgehog fabricator k3s cluster.
EOF
}

case "${1:-}" in
  -h|--help) usage; exit 0 ;;
esac

# --- preflight ----------------------------------------------------------------
info "Preflight checks"
command -v kubectl >/dev/null || die "kubectl not found in PATH"
kubectl cluster-info >/dev/null 2>&1 \
  || die "kubectl cannot reach the cluster (check KUBECONFIG: ${KUBECONFIG:-~/.kube/config})"
kubectl get crd helmcharts.helm.cattle.io >/dev/null 2>&1 \
  || die "helmcharts.helm.cattle.io CRD not found — is this a k3s cluster?"
# Create the demo namespace if missing (the fabricator doesn't ship it).
kubectl create ns "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f - >/dev/null

# Generate a deterministic webhook token so re-runs don't churn the Secret.
WEBHOOK_TOKEN_RAW="$(kubectl get ns kube-system -o jsonpath='{.metadata.uid}')-gitops-demo"
WEBHOOK_TOKEN="$(printf '%s' "${WEBHOOK_TOKEN_RAW}" | shasum -a 256 2>/dev/null | cut -d' ' -f1 \
  || printf '%s' "${WEBHOOK_TOKEN_RAW}" | sha256sum | cut -d' ' -f1)"

# --- HelmCharts + Secrets ------------------------------------------------------
info "Applying HelmCharts and shared secrets in namespace '${NAMESPACE}'"

kubectl apply -f - <<EOF
---
# --- Gitea --------------------------------------------------------------------
apiVersion: helm.cattle.io/v1
kind: HelmChart
metadata:
  name: gitea
  namespace: ${NAMESPACE}
spec:
  repo: ${GITEA_CHART_REPO}
  chart: gitea
  version: ${GITEA_CHART_VERSION}
  targetNamespace: ${NAMESPACE}
  createNamespace: false
  valuesContent: |
    fullnameOverride: gitea
    replicaCount: 1
    strategy:
      type: Recreate
    gitea:
      admin:
        username: admin
        password: admin
        email: admin@fabricator.local
        passwordMode: keepUpdated
      config:
        # Minimal DEV config (per the Gitea chart README): SQLite for the
        # main DB, in-memory cache/session/queue, no external dependencies.
        database:
          DB_TYPE: sqlite3
        session:
          PROVIDER: memory
        cache:
          ADAPTER: memory
        queue:
          TYPE: level
        server:
          DISABLE_SSH: true
          OFFLINE_MODE: true
          # Set ROOT_URL to the in-cluster service URL so the webhook
          # payload Gitea sends to Argo CD includes the same repoURL the
          # Argo CD Application uses. Otherwise the payload defaults to
          # http://git.example.com/... which doesn't match the
          # Application's repoURL, and Argo CD logs "Received push event"
          # but never actually refreshes — the apply only catches up
          # via the 15s polling fallback.
          ROOT_URL: http://gitea-http.demo.svc.cluster.local:3000/
        service:
          DISABLE_REGISTRATION: true
          REQUIRE_SIGNIN_VIEW: false
        webhook:
          # Gitea defaults to ALLOWED_HOST_LIST=external, which silently
          # refuses to deliver webhooks to private IPs — including the
          # in-cluster argocd-server.demo.svc.cluster.local Service.
          # That kills the demo loop's instant-reconcile path; allow all.
          ALLOWED_HOST_LIST: "*"
        security:
          # Allow admin/admin: default MIN_PASSWORD_LENGTH=8 + complexity rules
          # reject 'admin' on the password-update code path used by the chart's
          # init container on subsequent reconciles. Demo-only relaxation.
          MIN_PASSWORD_LENGTH: 5
          PASSWORD_COMPLEXITY: "off"
        repository:
          DEFAULT_BRANCH: main
    service:
      http:
        type: NodePort
        port: 3000
        nodePort: ${GITEA_NODEPORT}
        clusterIP: ""
    persistence:
      enabled: true
      size: 5Gi
      storageClass: ${STORAGE_CLASS}
    postgresql:
      enabled: false
    postgresql-ha:
      enabled: false
    redis-cluster:
      enabled: false
    valkey:
      enabled: false
    valkey-cluster:
      enabled: false
    resources:
      requests: { cpu: 250m, memory: 256Mi }
      limits:   { cpu: 1000m, memory: 1Gi }
---
# --- Argo CD ------------------------------------------------------------------
# Non-HA setup — chart defaults already match what we want (single replica
# per component, single Redis pod, dex/notifications/redis-ha all off or
# disable-able).  We further:
#  - serve plain HTTP on NodePort :31900 (server.insecure=true)
#  - enable anonymous read-only (configs.cm.users.anonymous + rbac.policy.default)
#  - poll the repo every 15s as a safety net for the webhook fast-path
#  - inject the Gitea webhook secret into argocd-secret under webhook.gitea.secret
apiVersion: helm.cattle.io/v1
kind: HelmChart
metadata:
  name: argo-cd
  namespace: ${NAMESPACE}
spec:
  repo: ${ARGOCD_CHART_REPO}
  chart: argo-cd
  version: ${ARGOCD_CHART_VERSION}
  targetNamespace: ${NAMESPACE}
  createNamespace: false
  valuesContent: |
    fullnameOverride: argocd
    crds:
      install: true
      keep: false
    global:
      domain: ""
    dex:
      enabled: false
    notifications:
      enabled: false
    redis-ha:
      enabled: false
    redis:
      resources:
        requests: { cpu: 50m,  memory: 64Mi }
        limits:   { cpu: 200m, memory: 128Mi }
    controller:
      replicas: 1
      resources:
        requests: { cpu: 100m, memory: 256Mi }
        limits:   { cpu: 500m, memory: 768Mi }
    repoServer:
      replicas: 1
      resources:
        requests: { cpu: 50m,  memory: 128Mi }
        limits:   { cpu: 300m, memory: 256Mi }
    server:
      replicas: 1
      service:
        type: NodePort
        nodePortHttp: ${ARGOCD_NODEPORT}
      resources:
        requests: { cpu: 50m,  memory: 128Mi }
        limits:   { cpu: 300m, memory: 256Mi }
    applicationSet:
      replicas: 1
      resources:
        requests: { cpu: 50m,  memory: 64Mi }
        limits:   { cpu: 200m, memory: 128Mi }
    configs:
      cm:
        # Anonymous user enabled — UI loads without a login screen.
        users.anonymous.enabled: "true"
        # Poll the git repo every 15s as the safety net for the webhook
        # fast-path (mirrors what we had with Flux). Default is 3m; default
        # jitter adds up to 60s on top — drop both so polling actually
        # fires at ~15s and not 15-75s.
        timeout.reconciliation: 15s
        timeout.reconciliation.jitter: 0s
      rbac:
        # Anonymous gets read-only. Admin user retains full power if
        # someone logs in with the chart-generated initial password.
        policy.default: "role:readonly"
      params:
        # Disable TLS — demo only; lets us talk plain HTTP via NodePort.
        server.insecure: "true"
      secret:
        # Inject the shared webhook secret into argocd-secret so Argo CD
        # can validate Gitea's X-Gitea-Signature header.
        extra:
          webhook.gitea.secret: "${WEBHOOK_TOKEN}"
---
# --- Webhook shared secret (also lives in argocd-secret via extra; kept here
# for the seed Job to read so it can configure Gitea with the same value) ----
apiVersion: v1
kind: Secret
metadata:
  name: fabric-config-webhook-token
  namespace: ${NAMESPACE}
  labels:
    app.kubernetes.io/part-of: gitops-demo
stringData:
  token: "${WEBHOOK_TOKEN}"
---
# --- Argo CD repository credentials for the in-cluster Gitea repo -------------
# Labelled secret-type=repository so Argo CD picks it up automatically.
apiVersion: v1
kind: Secret
metadata:
  name: argocd-repo-gitea
  namespace: ${NAMESPACE}
  labels:
    app.kubernetes.io/part-of: gitops-demo
    argocd.argoproj.io/secret-type: repository
stringData:
  type: git
  url: http://gitea-http.${NAMESPACE}.svc.cluster.local:3000/admin/fabric-config.git
  username: admin
  password: admin
EOF

# --- wait for HelmCharts to install --------------------------------------------
wait_for_helmchart() {
  local name="$1" kind="$2" obj="$3"
  for _ in $(seq 1 60); do
    kubectl -n "${NAMESPACE}" get "job/helm-install-${name}" >/dev/null 2>&1 && break
    sleep 1
  done
  kubectl -n "${NAMESPACE}" wait --for=condition=Complete --timeout=10m \
    "job/helm-install-${name}" >/dev/null 2>&1 || true

  for _ in $(seq 1 120); do
    kubectl -n "${NAMESPACE}" get "${kind}/${obj}" >/dev/null 2>&1 && break
    sleep 1
  done
  kubectl -n "${NAMESPACE}" rollout status "${kind}/${obj}" --timeout=10m || true
}

info "Waiting for HelmChart installers and workloads (up to 10 min each)"
wait_for_helmchart gitea   deployment  gitea
wait_for_helmchart argo-cd statefulset argocd-application-controller
kubectl -n "${NAMESPACE}" rollout status deployment/argocd-server      --timeout=5m || true
kubectl -n "${NAMESPACE}" rollout status deployment/argocd-repo-server --timeout=5m || true

# --- Argo CD Application (applied after the chart installs CRDs) ---------------
info "Applying Argo CD Application 'fabric-config'"
kubectl apply -f - <<EOF
---
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: fabric-config
  namespace: ${NAMESPACE}
  labels:
    app.kubernetes.io/part-of: gitops-demo
spec:
  project: default
  source:
    repoURL: http://gitea-http.${NAMESPACE}.svc.cluster.local:3000/admin/fabric-config.git
    targetRevision: main
    path: .
    directory:
      recurse: true
  destination:
    server: https://kubernetes.default.svc
    namespace: ${DEFAULT_NAMESPACE}
  syncPolicy:
    automated:
      prune: true
      selfHeal: true
    syncOptions:
      - CreateNamespace=false
      - ApplyOutOfSyncOnly=true
EOF

# --- seed Job: create the repo, push CRs, wire the webhook ---------------------
info "Applying seed Job (creates Gitea repo, pushes fabric CRs, wires webhook)"
kubectl create configmap gitops-demo-seed-script -n "${NAMESPACE}" \
  --from-file=seed.sh="${SCRIPT_DIR}/seed.sh" \
  --dry-run=client -o yaml | kubectl apply -f -

kubectl apply -f - <<EOF
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: gitops-demo-seeder
  namespace: ${NAMESPACE}
  labels:
    app.kubernetes.io/part-of: gitops-demo
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: gitops-demo-seeder
  labels:
    app.kubernetes.io/part-of: gitops-demo
rules:
  - apiGroups: ["vpc.githedgehog.com"]
    resources: ["vpcs","vpcattachments","vpcpeerings","externalpeerings"]
    verbs: ["get","list","watch"]
  - apiGroups: ["gateway.githedgehog.com"]
    resources: ["gatewaypeerings"]
    verbs: ["get","list","watch"]
  # The seed Job persists its last-handled SETUP_RUN_ID in a ConfigMap so that
  # pod retries within a single Job can detect "same run vs fresh run".
  - apiGroups: [""]
    resources: ["configmaps"]
    verbs: ["get","list","watch","create","update","patch"]
  # No 'secrets' rule on purpose: the webhook token is delivered to the seed
  # container via env.valueFrom.secretKeyRef (the kubelet reads the Secret
  # on the pod's behalf at start time), so the Job's SA never needs Secret
  # read permissions.
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: gitops-demo-seeder
  labels:
    app.kubernetes.io/part-of: gitops-demo
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: gitops-demo-seeder
subjects:
  - kind: ServiceAccount
    name: gitops-demo-seeder
    namespace: ${NAMESPACE}
EOF

# Replace any prior Job (Jobs are immutable once spec is set).
kubectl -n "${NAMESPACE}" delete job gitops-demo-seed --ignore-not-found

kubectl apply -f - <<EOF
---
apiVersion: batch/v1
kind: Job
metadata:
  name: gitops-demo-seed
  namespace: ${NAMESPACE}
  labels:
    app.kubernetes.io/part-of: gitops-demo
spec:
  backoffLimit: 3
  ttlSecondsAfterFinished: 600
  template:
    metadata:
      labels:
        app.kubernetes.io/part-of: gitops-demo
        app.kubernetes.io/component: seeder
    spec:
      restartPolicy: OnFailure
      serviceAccountName: gitops-demo-seeder
      containers:
        - name: seed
          image: ${SEED_IMAGE}
          imagePullPolicy: IfNotPresent
          command: ["/bin/bash","/scripts/seed.sh"]
          env:
            - name: SETUP_RUN_ID
              value: "${SETUP_RUN_ID}"
            # Injected by the kubelet at pod start — the seed Job's SA does
            # NOT need Secret read RBAC.
            - name: WEBHOOK_TOKEN
              valueFrom:
                secretKeyRef:
                  name: fabric-config-webhook-token
                  key: token
          resources:
            requests: { cpu: 50m, memory: 64Mi }
            limits:   { cpu: 500m, memory: 256Mi }
          volumeMounts:
            - { name: seed-script, mountPath: /scripts, readOnly: true }
      volumes:
        - name: seed-script
          configMap:
            name: gitops-demo-seed-script
            defaultMode: 0o555
EOF

info "Waiting for seed Job to finish (up to 5 min)"
kubectl -n "${NAMESPACE}" wait --for=condition=Complete --timeout=5m \
  job/gitops-demo-seed || {
  warn "seed Job did not complete cleanly; inspect:"
  warn "  kubectl -n ${NAMESPACE} logs job/gitops-demo-seed"
}

# --- verify -------------------------------------------------------------------
NODE_IP="$(kubectl get nodes -o jsonpath='{.items[0].status.addresses[?(@.type=="InternalIP")].address}' 2>/dev/null || true)"
[[ -z "${NODE_IP}" ]] && NODE_IP="<node-ip>"

info "Verifying"
poll() {
  local timeout="$1" probe="$2"
  local start=$SECONDS
  while (( SECONDS - start < timeout )); do
    if eval "${probe}" >/dev/null 2>&1; then return 0; fi
    sleep 2
  done
  return 1
}

if poll 60 'kubectl -n '"${NAMESPACE}"' exec deploy/gitea -c gitea -- curl -sf http://localhost:3000/api/v1/version | grep -q version'; then
  info "  Gitea: API responding"
else
  warn "  Gitea: API not responding"
fi
if poll 60 'kubectl -n '"${NAMESPACE}"' exec deploy/argocd-server -- wget -qO- --tries=1 --timeout=3 http://localhost:8080/healthz | grep -q ok'; then
  info "  Argo CD: server healthy"
else
  warn "  Argo CD: server not healthy (kubectl -n ${NAMESPACE} logs deploy/argocd-server)"
fi
if poll 180 'kubectl -n '"${NAMESPACE}"' get application/fabric-config -o jsonpath="{.status.sync.status}" | grep -q Synced'; then
  info "  Argo CD Application: Synced"
else
  warn "  Argo CD Application: not Synced (kubectl -n ${NAMESPACE} get application/fabric-config -o yaml | grep -A 4 conditions)"
fi
if poll 60 'kubectl -n '"${NAMESPACE}"' get application/fabric-config -o jsonpath="{.status.health.status}" | grep -q Healthy'; then
  info "  Argo CD Application: Healthy"
else
  warn "  Argo CD Application: not Healthy"
fi

# Argo CD initial admin password (printed for users who want to log in
# and use the write/Sync side of the UI). The chart creates this Secret
# after a successful install; if it hasn't appeared yet, fall back to a
# kubectl-one-liner so the user can fetch it themselves.
ARGOCD_ADMIN_PW="$(kubectl -n "${NAMESPACE}" get secret argocd-initial-admin-secret \
  -o jsonpath='{.data.password}' 2>/dev/null | base64 -d 2>/dev/null || true)"
if [[ -z "${ARGOCD_ADMIN_PW}" ]]; then
  ARGOCD_ADMIN_PW="<run: kubectl -n ${NAMESPACE} get secret argocd-initial-admin-secret -o jsonpath='{.data.password}' | base64 -d>"
fi

# Pick the first VPC in default ns to deep-link directly at its edit page
# in Gitea. Falls back to the repo tree if no VPC found.
FIRST_VPC="$(kubectl get vpc -n "${DEFAULT_NAMESPACE}" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)"
if [[ -n "${FIRST_VPC}" ]]; then
  GITEA_EDIT_URL="http://${NODE_IP}:${GITEA_NODEPORT}/admin/fabric-config/_edit/main/vpcs/${FIRST_VPC}.yaml"
else
  GITEA_EDIT_URL="http://${NODE_IP}:${GITEA_NODEPORT}/admin/fabric-config"
fi

cat <<EOF

${C_GREEN}Done.${C_NC}

Argo CD UI:     http://${NODE_IP}:${ARGOCD_NODEPORT}    (anonymous, read-only)
  fabric-config: http://${NODE_IP}:${ARGOCD_NODEPORT}/applications/${NAMESPACE}/fabric-config
Gitea UI:       http://${NODE_IP}:${GITEA_NODEPORT}    (anonymous browse; admin/admin to edit)
  edit a VPC:   ${GITEA_EDIT_URL}

Login (if you want write access in Argo CD):
  user: admin
  pass: ${ARGOCD_ADMIN_PW}

Demo flow:
  1. Open the "edit a VPC" link above (sign in admin/admin if prompted).
  2. Change a value (e.g. spec.subnets.subnet-01.dhcp.range.end), click Commit.
  3. Watch the new revision land at the Argo CD Application URL above
     within a few seconds (live diff + sync history visible).

Or drive the loop without manual clicks:
  ./demo-loop.sh             pick a VPC, change one value via Gitea API,
                              time how long Argo CD + cluster take to catch up.

Other lifecycle:
  ./verify.sh                re-run the health checks
  ./forward.sh               expose Argo CD + Gitea NodePorts on localhost
  ./cleanup.sh               tear everything down

EOF
