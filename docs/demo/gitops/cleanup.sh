#!/usr/bin/env bash
# Copyright 2026 Hedgehog
# SPDX-License-Identifier: Apache-2.0
#
# Tears down everything the GitOps demo's setup.sh creates:
#   - the Argo CD Application (finalizers stripped first so deletion
#     doesn't hang once Argo CD itself is gone)
#   - the gitea + argo-cd HelmCharts
#   - label-selected ServiceAccounts / ConfigMaps / Secrets / Jobs
#   - cluster-scoped RBAC the seed Job uses
#   - the Gitea PVC
#
# Idempotent. Exits non-zero only on real errors (not on "already absent").

set -euo pipefail

NAMESPACE="demo"

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

info "Stripping finalizers and deleting Argo CD Application"
# Strip first so the delete returns immediately even after we kill Argo CD
# (otherwise the resources-finalizer.argocd.argoproj.io would hang waiting
# for an application-controller that's already gone).
if kubectl -n "${NAMESPACE}" get application/fabric-config >/dev/null 2>&1; then
  kubectl -n "${NAMESPACE}" patch application/fabric-config --type=merge \
    -p '{"metadata":{"finalizers":[]}}' >/dev/null 2>&1 || true
  kubectl -n "${NAMESPACE}" delete application/fabric-config --ignore-not-found
fi

info "Deleting HelmCharts (workloads + Services come with them)"
kubectl -n "${NAMESPACE}" delete helmchart gitea argo-cd --ignore-not-found

info "Deleting label-selected leftovers (gitops-demo)"
kubectl -n "${NAMESPACE}" delete all,sa,cm,secret,job \
  -l app.kubernetes.io/part-of=gitops-demo --ignore-not-found 2>&1 | grep -v "No resources found" || true

info "Deleting the seed script ConfigMap (not labelled before the move)"
kubectl -n "${NAMESPACE}" delete configmap gitops-demo-seed-script gitops-demo-state --ignore-not-found

info "Deleting cluster-scoped RBAC"
kubectl delete clusterrole gitops-demo-seeder --ignore-not-found
kubectl delete clusterrolebinding gitops-demo-seeder --ignore-not-found

info "Deleting the Gitea PVC"
kubectl -n "${NAMESPACE}" delete pvc -l app.kubernetes.io/instance=gitea --ignore-not-found

info "Done."
