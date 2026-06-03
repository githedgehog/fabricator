#!/usr/bin/env bash
# Copyright 2026 Hedgehog
# SPDX-License-Identifier: Apache-2.0
#
# Tears down everything the o11y demo's setup.sh creates:
#   - the three HelmCharts (gitea-style: deleting takes workloads + Services)
#   - the local-dashboards ConfigMap
#   - the VM and VL PVCs
#   - the `local` Prometheus/Loki targets we added to Fabricator/default
#     (set to null in a JSON-merge patch — that's how merge-patch deletes keys)
#
# Idempotent and exits non-zero only on real errors (not on "already absent").

set -euo pipefail

NAMESPACE="demo"
FAB_NAMESPACE="fab"
FABRICATOR_NAME="default"

if [[ -t 1 ]]; then
  C_GREEN=$'\033[0;32m' C_YELLOW=$'\033[0;33m' C_RED=$'\033[0;31m' C_NC=$'\033[0m'
else
  C_GREEN="" C_YELLOW="" C_RED="" C_NC=""
fi
info() { printf "%s==>%s %s\n" "${C_GREEN}" "${C_NC}" "$*"; }
warn() { printf "%swarn:%s %s\n" "${C_YELLOW}" "${C_NC}" "$*" >&2; }
die()  { printf "%serror:%s %s\n" "${C_RED}" "${C_NC}" "$*" >&2; exit 1; }

command -v kubectl >/dev/null || die "kubectl not found in PATH"
kubectl cluster-info >/dev/null 2>&1 \
  || die "kubectl cannot reach the cluster (check KUBECONFIG)"

info "Removing 'local' Prometheus/Loki targets from Fabricator/${FABRICATOR_NAME}"
if kubectl get fabricators.fabricator.githedgehog.com "${FABRICATOR_NAME}" -n "${FAB_NAMESPACE}" >/dev/null 2>&1; then
  kubectl patch fabricators.fabricator.githedgehog.com "${FABRICATOR_NAME}" -n "${FAB_NAMESPACE}" \
    --type=merge --patch-file=/dev/stdin <<EOF
spec:
  config:
    observability:
      targets:
        prometheus:
          local: null
        loki:
          local: null
EOF
else
  warn "  Fabricator CR not found — skipping patch"
fi

info "Deleting HelmCharts (workloads + Services come with them)"
kubectl -n "${NAMESPACE}" delete helmchart victoria-metrics victoria-logs grafana --ignore-not-found

info "Deleting the local dashboards ConfigMap"
kubectl -n "${NAMESPACE}" delete configmap hedgehog-dashboards-local --ignore-not-found

info "Deleting VM and VL PVCs"
kubectl -n "${NAMESPACE}" delete pvc -l app.kubernetes.io/name=victoria-metrics-single --ignore-not-found
kubectl -n "${NAMESPACE}" delete pvc -l app.kubernetes.io/name=victoria-logs-single --ignore-not-found

info "Done."
