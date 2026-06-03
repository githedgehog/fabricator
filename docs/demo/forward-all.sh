#!/usr/bin/env bash
# Copyright 2026 Hedgehog
# SPDX-License-Identifier: Apache-2.0
#
# Convenience: kubectl port-forward every NodePort-exposed Service from
# both demos, all in one foreground process. Useful for "I want all the
# UIs reachable from my laptop with one command" — pairs with the
# per-demo forward.sh scripts, which only forward a single demo.
#
# Set ADDRESS=0.0.0.0 to bind on every interface (LAN-wide); default is
# localhost-only.

set -euo pipefail

NAMESPACE="demo"
ADDRESS="${ADDRESS:-localhost}"

if [[ -t 1 ]]; then
  C_GREEN=$'\033[0;32m' C_NC=$'\033[0m'
else
  C_GREEN="" C_NC=""
fi
info() { printf "%s==>%s %s\n" "${C_GREEN}" "${C_NC}" "$*"; }

cleanup() {
  trap - SIGINT SIGTERM EXIT
  info "Stopping port-forwards"
  local pids=()
  mapfile -t pids < <(jobs -p)
  if [ "${#pids[@]}" -gt 0 ]; then
    kill "${pids[@]}" 2>/dev/null
  fi
  wait 2>/dev/null
  exit 0
}
trap cleanup SIGINT SIGTERM EXIT

command -v kubectl >/dev/null || { echo "kubectl not found in PATH" >&2; exit 1; }
kubectl cluster-info >/dev/null 2>&1 \
  || { echo "kubectl cannot reach the cluster (check KUBECONFIG)" >&2; exit 1; }

info "Forwarding all demo NodePorts (address=${ADDRESS}; Ctrl-C to stop)"

# o11y
kubectl -n "${NAMESPACE}" port-forward --address "${ADDRESS}" svc/grafana 31800:80 &
# gitops
kubectl -n "${NAMESPACE}" port-forward --address "${ADDRESS}" svc/argocd-server 31900:80 &
kubectl -n "${NAMESPACE}" port-forward --address "${ADDRESS}" svc/gitea-http   31901:3000 &

cat <<EOF

  Grafana:  http://${ADDRESS}:31800    (anonymous Editor, no login)
  Argo CD:  http://${ADDRESS}:31900    (anonymous, read-only)
  Gitea:    http://${ADDRESS}:31901    (anonymous browse; admin/admin to edit)

EOF

wait
