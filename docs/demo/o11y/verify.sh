#!/usr/bin/env bash
# Copyright 2026 Hedgehog
# SPDX-License-Identifier: Apache-2.0
#
# Re-runnable health check for the o11y demo. The same probes setup.sh
# runs at the end of an install, extracted so you can call them any time
# (e.g. after a pod restart or kubectl-edit drift).
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
    sleep 3
  done
  return 1
}

FAIL=0
info "Verifying o11y demo (up to 90s per probe)"

# VM
if poll_until 90 'kubectl exec -n '"${NAMESPACE}"' deploy/grafana -- \
     curl -sf "http://victoria-metrics:8428/api/v1/query?query=count(up)" \
     | grep -q "\"result\":\[{"'; then
  info "  VictoriaMetrics: metrics ingestion confirmed"
else
  warn "  VictoriaMetrics: no series — Alloy may not be pushing"
  FAIL=$((FAIL+1))
fi

# VL
if poll_until 90 'kubectl exec -n '"${NAMESPACE}"' deploy/grafana -- \
     curl -sf "http://victoria-logs:9428/select/logsql/streams?query=*&start=1h" \
     | grep -q "\"value\":"'; then
  info "  VictoriaLogs: log ingestion confirmed"
else
  warn "  VictoriaLogs: no streams — Alloy may not be pushing"
  FAIL=$((FAIL+1))
fi

# Grafana health + datasources
GF="kubectl exec -n ${NAMESPACE} deploy/grafana -- curl -sf"
if ${GF} 'http://localhost:3000/api/health' | grep -q '"database": "ok"'; then
  info "  Grafana: health endpoint OK"
else
  warn "  Grafana: health endpoint not OK"
  FAIL=$((FAIL+1))
fi
for ds in VictoriaMetrics VictoriaLogs; do
  if ${GF} "http://localhost:3000/api/datasources/name/${ds}" | grep -q '"id":'; then
    info "  Grafana: datasource '${ds}' present"
  else
    warn "  Grafana: datasource '${ds}' missing"
    FAIL=$((FAIL+1))
  fi
done

exit "${FAIL}"
