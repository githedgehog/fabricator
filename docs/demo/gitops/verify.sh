#!/usr/bin/env bash
# Copyright 2026 Hedgehog
# SPDX-License-Identifier: Apache-2.0
#
# Re-runnable health check for the GitOps demo. The same probes setup.sh
# runs at the end of an install, extracted so you can call them any time
# (e.g. after a pod restart or to confirm a webhook still fires).
#
# Exit code: 0 if every probe passes; non-zero with the count of failed
# probes otherwise (so it composes with `set -e` and CI).

set -uo pipefail

NAMESPACE="demo"

if [[ -t 1 ]]; then
  C_GREEN=$'\033[0;32m' C_YELLOW=$'\033[0;33m' C_NC=$'\033[0m'
else
  C_GREEN="" C_YELLOW="" C_NC=""
fi
info() { printf "%s==>%s %s\n" "${C_GREEN}" "${C_NC}" "$*"; }
warn() { printf "%swarn:%s %s\n" "${C_YELLOW}" "${C_NC}" "$*" >&2; }

command -v kubectl >/dev/null || { warn "kubectl not found in PATH"; exit 1; }
kubectl cluster-info >/dev/null 2>&1 \
  || { warn "kubectl cannot reach the cluster (check KUBECONFIG)"; exit 1; }

poll_until() {
  local timeout="$1" probe="$2"
  local start=$SECONDS
  while (( SECONDS - start < timeout )); do
    if eval "${probe}" >/dev/null 2>&1; then return 0; fi
    sleep 2
  done
  return 1
}

FAIL=0
info "Verifying gitops demo"

# Gitea API
if poll_until 60 'kubectl -n '"${NAMESPACE}"' exec deploy/gitea -c gitea -- curl -sf http://localhost:3000/api/v1/version | grep -q version'; then
  info "  Gitea: API responding"
else
  warn "  Gitea: API not responding"
  FAIL=$((FAIL+1))
fi

# Argo CD server health — hit it from the Gitea pod (argocd-server's image
# has neither curl nor wget). Service routes traffic by selector so we can
# probe argocd-server.<ns>.svc:80 even before kube-proxy's Endpoints catch up.
if poll_until 60 'kubectl -n '"${NAMESPACE}"' exec deploy/gitea -c gitea -- curl -sf -m 3 http://argocd-server:80/healthz | grep -q ok'; then
  info "  Argo CD: server healthy"
else
  warn "  Argo CD: server not healthy"
  FAIL=$((FAIL+1))
fi

# Application Synced
if poll_until 180 'kubectl -n '"${NAMESPACE}"' get application/fabric-config -o jsonpath="{.status.sync.status}" | grep -q Synced'; then
  info "  Argo CD Application: Synced"
else
  warn "  Argo CD Application: not Synced"
  FAIL=$((FAIL+1))
fi

# Application Healthy
if poll_until 60 'kubectl -n '"${NAMESPACE}"' get application/fabric-config -o jsonpath="{.status.health.status}" | grep -q Healthy'; then
  info "  Argo CD Application: Healthy"
else
  warn "  Argo CD Application: not Healthy"
  FAIL=$((FAIL+1))
fi

# Webhook registered in Gitea pointing at argocd-server
WEBHOOK_OK=$(kubectl -n "${NAMESPACE}" exec deploy/gitea -c gitea -- \
  curl -sf -u admin:admin http://localhost:3000/api/v1/repos/admin/fabric-config/hooks 2>/dev/null \
  | python3 -c '
import json, sys
try:
    hooks = json.load(sys.stdin)
except Exception:
    print("none")
    sys.exit()
for h in hooks:
    if "argocd-server" in h.get("config", {}).get("url", ""):
        print("ok")
        sys.exit()
print("none")
' 2>/dev/null || echo "none")
if [ "${WEBHOOK_OK}" = "ok" ]; then
  info "  Gitea webhook: registered against argocd-server"
else
  warn "  Gitea webhook: missing or doesn't point at argocd-server"
  FAIL=$((FAIL+1))
fi

exit "${FAIL}"
