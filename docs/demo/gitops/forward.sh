#!/usr/bin/env bash
# Copyright 2026 Hedgehog
# SPDX-License-Identifier: Apache-2.0
#
# Convenience: kubectl port-forward for every NodePort-exposed Service
# in the GitOps demo, mapped to the same local port. Useful when the
# cluster's NodePort path is unreachable (e.g. behind QEMU SLIRP that
# doesn't have hostfwd set up for these ports) or when you'd rather not
# expose NodePorts externally.
#
# Set ADDRESS=0.0.0.0 to bind the local ports on every interface (LAN);
# default is localhost-only.

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
  # `xargs -r` is GNU-only; use a bash array test instead so this is
  # portable to BSD/macOS xargs.
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
  || { echo "kubectl cannot reach the cluster (check KUBECONFIG: ${KUBECONFIG:-~/.kube/config})" >&2; exit 1; }

info "Forwarding GitOps NodePorts (address=${ADDRESS}; Ctrl-C to stop)"

# svc/argocd-server — NodePort 31900, port 80, targetPort http (8080).
kubectl -n "${NAMESPACE}" port-forward --address "${ADDRESS}" svc/argocd-server 31900:80 &

# svc/gitea-http — NodePort 31901, port 3000.
kubectl -n "${NAMESPACE}" port-forward --address "${ADDRESS}" svc/gitea-http 31901:3000 &

cat <<EOF

  Argo CD:  http://${ADDRESS}:31900    (anonymous, read-only)
  Gitea:    http://${ADDRESS}:31901    (anonymous browse; admin/admin to edit)

EOF

wait
