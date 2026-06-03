#!/usr/bin/env bash
# Copyright 2026 Hedgehog
# SPDX-License-Identifier: Apache-2.0
#
# Installs a lightweight self-hosted observability backend (VictoriaMetrics,
# VictoriaLogs, Grafana) into the `demo` namespace of a Hedgehog fabricator k3s
# cluster, and points the fabricator's Alloy collectors at it.
#
# See ./README.md for full documentation.

set -euo pipefail

# --- pinned versions -----------------------------------------------------------
VM_CHART_REPO="https://victoriametrics.github.io/helm-charts"
VM_CHART_VERSION="0.39.0"          # app v1.144.0
VL_CHART_REPO="https://victoriametrics.github.io/helm-charts"
VL_CHART_VERSION="0.13.3"          # app v1.50.0
GRAFANA_CHART_REPO="https://grafana.github.io/helm-charts"
GRAFANA_CHART_VERSION="10.5.15"    # app 12.3.1

# --- knobs ---------------------------------------------------------------------
NAMESPACE="demo"            # where this demo's resources go (created if missing)
FAB_NAMESPACE="fab"         # where the fabricator itself + the Fabricator CR live
FABRICATOR_NAME="default"
GRAFANA_NODEPORT="${GRAFANA_NODEPORT:-31800}"
STORAGE_CLASS="${STORAGE_CLASS:-local-path}"

SCRIPT_DIR="$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )"

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
  GRAFANA_NODEPORT   NodePort for the Grafana service       (default: 31800)
  STORAGE_CLASS      StorageClass for VM/VL PVCs            (default: local-path)

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

kubectl get ns "${FAB_NAMESPACE}" >/dev/null 2>&1 \
  || die "namespace '${FAB_NAMESPACE}' not found — is the fabricator installed?"
kubectl get fabricators.fabricator.githedgehog.com "${FABRICATOR_NAME}" -n "${FAB_NAMESPACE}" >/dev/null 2>&1 \
  || die "Fabricator/${FABRICATOR_NAME} not found in namespace '${FAB_NAMESPACE}'"

# Create the demo namespace if missing (the fabricator doesn't ship it).
kubectl create ns "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f - >/dev/null

# --- safety check: refuse to proceed while other Prometheus/Loki sinks exist ---
# This demo deliberately turns up the firehose: 15s scrape interval, all
# metricsRelabel filters removed, the 'minimal' defaults profile dropped.
# Every existing Prometheus/Loki target on the Fabricator CR receives the
# same flood — and external billing sinks (Grafana Cloud Pro, etc.) can
# easily run to $1000s/month at that volume from a real fabric. So before
# we touch anything else, we refuse to proceed while non-'local' targets
# still exist.
info "Checking for non-'local' Prometheus/Loki targets on the Fabricator CR"
OTHER_TARGETS="$(kubectl get fabricators.fabricator.githedgehog.com "${FABRICATOR_NAME}" \
  -n "${FAB_NAMESPACE}" -o json | python3 -c '
import json, sys
d = json.load(sys.stdin)
targets = d.get("spec", {}).get("config", {}).get("observability", {}).get("targets", {})
for kind in ("prometheus", "loki"):
    for name in sorted(targets.get(kind, {}).keys()):
        if name != "local":
            print(f"{kind}.{name}")
')"

if [[ -n "${OTHER_TARGETS}" ]]; then
  warn "This demo enables full-resolution metrics (15s interval, no relabel"
  warn "filter). Any external Prometheus/Loki target you keep will also receive"
  warn "the firehose — Grafana Cloud and similar will bill accordingly (often"
  warn "in the \$1000s/month at fabric scale)."
  echo ""
  info "Non-'local' Prometheus/Loki targets currently on the Fabricator CR:"
  printf '%s\n' "${OTHER_TARGETS}" | sed 's/^/    - /'
  echo ""

  CONFIRMED="no"
  if [[ "${O11Y_DELETE_NONLOCAL:-}" =~ ^(y|yes|true|1)$ ]]; then
    info "O11Y_DELETE_NONLOCAL is set — proceeding to delete the targets above"
    CONFIRMED="yes"
  elif [[ -t 0 ]]; then
    read -r -p "Delete the targets above and proceed? Anything other than 'yes' aborts: " ANSWER
    case "${ANSWER,,}" in
      y|yes) CONFIRMED="yes" ;;
    esac
  else
    die "non-interactive shell and O11Y_DELETE_NONLOCAL is unset — refusing to proceed; either run interactively and confirm, or set O11Y_DELETE_NONLOCAL=yes to auto-delete the non-'local' Prometheus/Loki targets"
  fi

  if [[ "${CONFIRMED}" != "yes" ]]; then
    die "user did not confirm deletion — aborting before any install side effects"
  fi

  DELETION_PATCH="$(printf '%s\n' "${OTHER_TARGETS}" | python3 -c '
import json, sys
patch = {"spec": {"config": {"observability": {"targets": {}}}}}
for line in sys.stdin.read().splitlines():
    line = line.strip()
    if not line: continue
    kind, name = line.split(".", 1)
    patch["spec"]["config"]["observability"]["targets"].setdefault(kind, {})[name] = None
print(json.dumps(patch))
')"
  kubectl patch fabricators.fabricator.githedgehog.com "${FABRICATOR_NAME}" \
    -n "${FAB_NAMESPACE}" --type=merge -p "${DELETION_PATCH}" >/dev/null
  info "Deleted: $(echo "${OTHER_TARGETS}" | tr '\n' ' ')"
fi

info "Applying local dashboard ConfigMap"
# The 'hedgehog-local' dashboard provider (added to the Grafana HelmChart
# values below) mounts this ConfigMap at /var/lib/grafana/dashboards/hedgehog-local.
# Use server-side-apply-style dry-run|apply for clean idempotent replace-on-change.
kubectl create configmap hedgehog-dashboards-local \
  -n "${NAMESPACE}" \
  --from-file=fabric-logs.json="${SCRIPT_DIR}/fabric-logs.dashboard.json" \
  --dry-run=client -o yaml | kubectl apply -f -

info "Applying HelmChart objects in namespace '${NAMESPACE}'"

# --- HelmCharts ---------------------------------------------------------------
# Service URLs the datasources / Alloy will use.
VM_URL_IN_NS="http://victoria-metrics:8428"
VL_URL_IN_NS="http://victoria-logs:9428"
VM_FQDN="http://victoria-metrics.${NAMESPACE}.svc.cluster.local:8428"
VL_FQDN="http://victoria-logs.${NAMESPACE}.svc.cluster.local:9428"

kubectl apply -f - <<EOF
---
apiVersion: helm.cattle.io/v1
kind: HelmChart
metadata:
  name: victoria-metrics
  namespace: ${NAMESPACE}
spec:
  repo: ${VM_CHART_REPO}
  chart: victoria-metrics-single
  version: ${VM_CHART_VERSION}
  targetNamespace: ${NAMESPACE}
  createNamespace: false
  valuesContent: |
    server:
      fullnameOverride: victoria-metrics
      replicaCount: 1
      retentionPeriod: "7d"
      persistentVolume:
        enabled: true
        size: 25Gi
        storageClassName: ${STORAGE_CLASS}
      resources:
        requests: { cpu: 250m, memory: 256Mi }
        limits:   { cpu: 1000m, memory: 1Gi }
      extraArgs:
        storage.minFreeDiskSpaceBytes: "5GiB"
---
apiVersion: helm.cattle.io/v1
kind: HelmChart
metadata:
  name: victoria-logs
  namespace: ${NAMESPACE}
spec:
  repo: ${VL_CHART_REPO}
  chart: victoria-logs-single
  version: ${VL_CHART_VERSION}
  targetNamespace: ${NAMESPACE}
  createNamespace: false
  valuesContent: |
    server:
      fullnameOverride: victoria-logs
      replicaCount: 1
      retentionPeriod: "7d"
      retentionMaxDiskUsagePercent: "80"
      persistentVolume:
        enabled: true
        size: 25Gi
        storageClassName: ${STORAGE_CLASS}
      resources:
        requests: { cpu: 250m, memory: 256Mi }
        limits:   { cpu: 1000m, memory: 1Gi }
---
apiVersion: helm.cattle.io/v1
kind: HelmChart
metadata:
  name: grafana
  namespace: ${NAMESPACE}
spec:
  repo: ${GRAFANA_CHART_REPO}
  chart: grafana
  version: ${GRAFANA_CHART_VERSION}
  targetNamespace: ${NAMESPACE}
  createNamespace: false
  valuesContent: |
    fullnameOverride: grafana
    replicas: 1
    persistence:
      enabled: false
    resources:
      requests: { cpu: 100m, memory: 128Mi }
      limits:   { cpu: 500m, memory: 512Mi }
    service:
      type: NodePort
      nodePort: ${GRAFANA_NODEPORT}
      port: 80
      targetPort: 3000
    plugins:
      - victoriametrics-logs-datasource
    grafana.ini:
      # Anonymous Editor — Editor is required for the 'Explore' nav item to
      # appear (Viewer lacks 'datasources:explore' in OSS Grafana 12, and the
      # RBAC role-assignment provisioning that would fix that is Enterprise-only;
      # the legacy 'users.viewers_can_explore' flag was removed in v12).
      # The curated dashboards stay safe — they're provisioned with
      # editable=false / disableDeletion=true / allowUiUpdates=false (below).
      # New dashboards a user creates are lost on pod restart (no PVC).
      auth.anonymous:
        enabled: true
        org_role: Editor
      auth:
        disable_login_form: true
        disable_signout_menu: true
      auth.basic:
        enabled: false
    datasources:
      datasources.yaml:
        apiVersion: 1
        datasources:
          - name: VictoriaMetrics
            type: prometheus
            access: proxy
            url: ${VM_URL_IN_NS}
            isDefault: true
            editable: false
          - name: VictoriaLogs
            type: victoriametrics-logs-datasource
            access: proxy
            url: ${VL_URL_IN_NS}
            editable: false
    dashboardProviders:
      dashboardproviders.yaml:
        apiVersion: 1
        providers:
          - name: hedgehog
            orgId: 1
            type: file
            disableDeletion: true
            allowUiUpdates: false
            editable: false
            options:
              path: /var/lib/grafana/dashboards/hedgehog
          # Second provider for hand-rolled, VL-native dashboards bundled
          # next to setup.sh (loaded from the 'hedgehog-dashboards-local' CM).
          - name: hedgehog-local
            orgId: 1
            type: file
            disableDeletion: true
            allowUiUpdates: false
            editable: false
            options:
              path: /var/lib/grafana/dashboards/hedgehog-local
    dashboardsConfigMaps:
      hedgehog-local: hedgehog-dashboards-local
    dashboards:
      hedgehog:
        agent-stats:
          gnetId: 24389
          revision: 1
          datasource: VictoriaMetrics
        critical-resources:
          gnetId: 24413
          revision: 1
          datasource: VictoriaMetrics
        fabric:
          gnetId: 24414
          revision: 1
          datasource: VictoriaMetrics
        interfaces:
          gnetId: 24415
          revision: 1
          datasource: VictoriaMetrics
        platform:
          gnetId: 24417
          revision: 1
          datasource: VictoriaMetrics
        node-exporter:
          gnetId: 24419
          revision: 1
          datasource: VictoriaMetrics
EOF

# --- patch Fabricator CR ------------------------------------------------------
# JSON merge patch deep-merges maps and preserves untouched keys, including any
# pre-existing target entries (e.g. 'grafana_cloud').
info "Patching Fabricator/${FABRICATOR_NAME} observability targets — adding 'local'"

kubectl patch fabricators.fabricator.githedgehog.com "${FABRICATOR_NAME}" -n "${FAB_NAMESPACE}" --type=merge \
  --patch-file=/dev/stdin <<EOF
spec:
  config:
    observability:
      targets:
        prometheus:
          local:
            url: ${VM_FQDN}/api/v1/write
        loki:
          local:
            url: ${VL_FQDN}/insert/loki/api/v1/push
EOF

# --- tune Alloy: 15s metrics, no relabel filter, drop 'defaults: minimal' ------
# Default fabricator config relabels switch metrics to keep only a small
# whitelist of metric names — fine for shipping but hides most of what a
# demo wants to look at. Drop the relabel rules, drop the 'minimal'
# defaults profile, and tighten the scrape interval to 15s.
info "Tuning Alloy config: metricsInterval=15, drop metricsRelabel, drop 'defaults: minimal'"
kubectl patch fabricators.fabricator.githedgehog.com "${FABRICATOR_NAME}" -n "${FAB_NAMESPACE}" --type=merge \
  --patch-file=/dev/stdin <<EOF
spec:
  config:
    observability:
      defaults: null
    fabric:
      observability:
        agent:
          metricsInterval: 15
          metricsRelabel: null
        unix:
          metricsInterval: 15
          metricsRelabel: null
    gateway:
      observability:
        dataplane:
          metricsInterval: 15
        frr:
          metricsInterval: 15
        unix:
          metricsInterval: 15
          metricsRelabel: null
EOF

# --- wait for rollout ---------------------------------------------------------
wait_for_helmchart() {
  local name="$1" kind="$2"
  # 1. wait for the helm-controller to create its installer Job, then for the Job to finish
  for _ in $(seq 1 60); do
    if kubectl -n "${NAMESPACE}" get "job/helm-install-${name}" >/dev/null 2>&1; then
      break
    fi
    sleep 1
  done
  kubectl -n "${NAMESPACE}" wait --for=condition=Complete --timeout=5m \
    "job/helm-install-${name}" >/dev/null 2>&1 || true

  # 2. wait for the chart's workload to appear, then become Ready
  for _ in $(seq 1 60); do
    if kubectl -n "${NAMESPACE}" get "${kind}/${name}" >/dev/null 2>&1; then
      break
    fi
    sleep 1
  done
  kubectl -n "${NAMESPACE}" rollout status "${kind}/${name}" --timeout=5m || true
}

info "Waiting for HelmChart installs and workloads to become Ready (up to 5 min each)"
wait_for_helmchart victoria-metrics statefulset
wait_for_helmchart victoria-logs   statefulset
wait_for_helmchart grafana         deployment

# --- verify data flow ---------------------------------------------------------
# Poll the in-cluster services. Alloy has to reconcile our new 'local' targets
# before metrics/logs land, so we give each probe up to 90s.
poll_until() {
  local timeout="$1" probe="$2"
  local start=$SECONDS
  while (( SECONDS - start < timeout )); do
    if eval "${probe}" >/dev/null 2>&1; then
      return 0
    fi
    sleep 3
  done
  return 1
}

info "Verifying data flow (up to 90s per backend)"

# VM: at least one 'up' series. The response shape is {"result":[{...}]}; an
# empty store returns {"result":[]}. grep for a non-empty result array.
if poll_until 90 'kubectl exec -n '"${NAMESPACE}"' deploy/grafana -- \
     curl -sf "http://victoria-metrics:8428/api/v1/query?query=count(up)" \
     | grep -q "\"result\":\[{"'; then
  info "  VictoriaMetrics: metrics ingestion confirmed"
else
  warn "  VictoriaMetrics: no series yet — Alloy may still be reconciling"
fi

# VL: at least one log stream visible. /select/logsql/streams with a
# wide-open query lists every known stream; a fresh store returns {"values":[]}.
if poll_until 90 'kubectl exec -n '"${NAMESPACE}"' deploy/grafana -- \
     curl -sf "http://victoria-logs:9428/select/logsql/streams?query=*&start=1h" \
     | grep -q "\"value\":"'; then
  info "  VictoriaLogs: log ingestion confirmed"
else
  warn "  VictoriaLogs: no streams yet — Alloy may still be reconciling"
fi

# Grafana: both datasources answer via the in-pod proxy path the UI uses.
GF="kubectl exec -n ${NAMESPACE} deploy/grafana -- curl -sf"
if ${GF} 'http://localhost:3000/api/health' | grep -q '"database": "ok"'; then
  info "  Grafana: health endpoint OK"
else
  warn "  Grafana: health endpoint not OK"
fi
for ds in VictoriaMetrics VictoriaLogs; do
  if ${GF} "http://localhost:3000/api/datasources/name/${ds}" | grep -q '"id":'; then
    info "  Grafana: datasource '${ds}' present"
  else
    warn "  Grafana: datasource '${ds}' missing"
  fi
done

# --- final URL ---------------------------------------------------------------
NODE_IP="$(kubectl get nodes -o jsonpath='{.items[0].status.addresses[?(@.type=="InternalIP")].address}' 2>/dev/null || true)"
[[ -z "${NODE_IP}" ]] && NODE_IP="<node-ip>"

cat <<EOF

${C_GREEN}Done.${C_NC}

Grafana home:      http://${NODE_IP}:${GRAFANA_NODEPORT}    (anonymous Editor, no login)
Switch counters:   http://${NODE_IP}:${GRAFANA_NODEPORT}/d/a5e5b12d-b340-4753-8f83-af8d54304822/hedgehog-switch-interface-counters
Switch fabric:     http://${NODE_IP}:${GRAFANA_NODEPORT}/d/ab831ceb-cf5c-474a-b7e9-83dcd075c218/hedgehog-fabric
Fabric logs:       http://${NODE_IP}:${GRAFANA_NODEPORT}/d/hh-fabric-logs/hedgehog-fabric-logs

VictoriaMetrics: ${VM_FQDN}     (in-cluster only)
VictoriaLogs:    ${VL_FQDN}     (in-cluster only)

Fabricator observability targets updated:
  - prometheus.local -> ${VM_FQDN}/api/v1/write
  - loki.local       -> ${VL_FQDN}/insert/loki/api/v1/push

The fabricator controller will reconcile these targets onto the Alloy collectors
on switches / gateway / control nodes on its next pass; metrics and logs will
start appearing in Grafana shortly after.

Other lifecycle:
  ./verify.sh          re-run the health checks
  ./forward.sh         expose the Grafana NodePort on localhost
  ./cleanup.sh         tear everything down (incl. Fabricator CR targets)

EOF
