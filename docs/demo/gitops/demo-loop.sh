#!/usr/bin/env bash
# Copyright 2026 Hedgehog
# SPDX-License-Identifier: Apache-2.0
#
# Drives the demo loop without manual webui clicks:
#  1. Picks a VPC from the `default` namespace.
#  2. Reads its YAML from the Gitea repo, modifies
#     spec.subnets.subnet-01.dhcp.range.end to a fresh value.
#  3. PUTs the new content via Gitea's API (same code path a webui edit
#     would take — produces a real commit and a real webhook).
#  4. Polls Argo CD's Application revision until it matches the commit,
#     then polls the cluster spec until it reflects the change.
#  5. Prints timestamps relative to the commit for each step.
#
# Usage:
#   ./demo-loop.sh             # change a value, watch it apply
#   ./demo-loop.sh --revert    # also revert via a follow-up commit
#
# Requires: KUBECONFIG pointing at the cluster the demo runs on.

set -euo pipefail

NAMESPACE="demo"
DEFAULT_NS="default"
REPO_OWNER="admin"
REPO_NAME="fabric-config"

if [[ -t 1 ]]; then
  C_GREEN=$'\033[0;32m' C_YELLOW=$'\033[0;33m' C_RED=$'\033[0;31m' C_NC=$'\033[0m'
else
  C_GREEN="" C_YELLOW="" C_RED="" C_NC=""
fi
info() { printf "%s==>%s %s\n" "${C_GREEN}" "${C_NC}" "$*"; }
warn() { printf "%swarn:%s %s\n" "${C_YELLOW}" "${C_NC}" "$*" >&2; }
die()  { printf "%serror:%s %s\n" "${C_RED}" "${C_NC}" "$*" >&2; exit 1; }

REVERT=0
case "${1:-}" in
  --revert) REVERT=1 ;;
  -h|--help) echo "Usage: $(basename "$0") [--revert]"; exit 0 ;;
esac

command -v kubectl >/dev/null || die "kubectl not found in PATH"
command -v python3 >/dev/null || die "python3 not found in PATH"

GITEA_EXEC=(kubectl -n "${NAMESPACE}" exec deploy/gitea -c gitea --)
GITEA_API() {
  "${GITEA_EXEC[@]}" curl -sf -u admin:admin -H 'Content-Type: application/json' "$@"
}

# 1. Pick a VPC in default ns
VPCS=$(kubectl get vpcs -n "${DEFAULT_NS}" -o jsonpath='{.items[*].metadata.name}' 2>/dev/null || true)
[ -z "${VPCS}" ] && die "no VPCs in ns/${DEFAULT_NS} — nothing to drive the loop with"
VPC=$(printf '%s\n' "${VPCS}" | tr ' ' '\n' | head -1)
info "Using VPC: ${VPC}"

# 2. Fetch current file content + sha from Gitea
RAW=$(GITEA_API "http://localhost:3000/api/v1/repos/${REPO_OWNER}/${REPO_NAME}/contents/vpcs/${VPC}.yaml")
SHA=$(echo "${RAW}" | python3 -c 'import json,sys;print(json.load(sys.stdin)["sha"])')
CONTENT=$(echo "${RAW}" | python3 -c '
import json, sys, base64
print(base64.b64decode(json.load(sys.stdin)["content"]).decode(), end="")
')

CUR=$(printf '%s' "${CONTENT}" | python3 -c '
import sys, re
c = sys.stdin.read()
m = re.search(r"subnet-01:[^a-zA-Z]*?dhcp:.*?end: (\d+\.\d+\.\d+\.\d+)", c, re.DOTALL)
print(m.group(1) if m else "")
')
[ -z "${CUR}" ] && die "couldn't find subnet-01.dhcp.range.end in ${VPC}.yaml"

# Pick a different value (cycle through a small range)
NEW="10.0.1.111"
[ "${CUR}" = "${NEW}" ] && NEW="10.0.1.222"

NEW_CONTENT=$(printf '%s' "${CONTENT}" | python3 -c "
import sys, re
c = sys.stdin.read()
c = re.sub(r'(subnet-01:[^a-zA-Z]*?dhcp:.*?end: )\d+\.\d+\.\d+\.\d+', r'\g<1>${NEW}', c, count=1, flags=re.DOTALL)
print(c, end='')
")
NEW_B64=$(printf '%s' "${NEW_CONTENT}" | base64 | tr -d '\n')
PAYLOAD=$(python3 -c "
import json
print(json.dumps({'sha':'${SHA}','content':'${NEW_B64}','message':'demo-loop: ${CUR} -> ${NEW}','branch':'main'}))
")

# 3. PUT to Gitea
START=$(date +%s)
T_REL() { printf "T+%ds" "$(( $(date +%s) - START ))"; }

COMMIT_RESP=$(GITEA_API -X PUT --data "${PAYLOAD}" \
  "http://localhost:3000/api/v1/repos/${REPO_OWNER}/${REPO_NAME}/contents/vpcs/${VPC}.yaml")
COMMIT_SHA=$(echo "${COMMIT_RESP}" | python3 -c 'import json,sys;print(json.load(sys.stdin)["commit"]["sha"])')
info "$(T_REL)  commit ${COMMIT_SHA:0:10}  (subnet-01 range.end: ${CUR} -> ${NEW})"

# 4. Watch Argo CD pick up the new revision
for _ in $(seq 1 30); do
  REV=$(kubectl -n "${NAMESPACE}" get application/fabric-config -o jsonpath='{.status.sync.revision}' 2>/dev/null || true)
  if [ "${REV}" = "${COMMIT_SHA}" ]; then
    info "$(T_REL)  argocd synced to ${REV:0:10}"
    break
  fi
  sleep 1
done

# 5. Watch the cluster spec
for _ in $(seq 1 30); do
  V=$(kubectl get "vpc/${VPC}" -n "${DEFAULT_NS}" -o jsonpath='{.spec.subnets.subnet-01.dhcp.range.end}' 2>/dev/null || true)
  if [ "${V}" = "${NEW}" ]; then
    info "$(T_REL)  cluster spec.subnets.subnet-01.dhcp.range.end = ${V}"
    break
  fi
  sleep 1
done

if [ "${REVERT}" = "1" ]; then
  info "Reverting..."
  # Re-fetch SHA (PUT updated it)
  RAW=$(GITEA_API "http://localhost:3000/api/v1/repos/${REPO_OWNER}/${REPO_NAME}/contents/vpcs/${VPC}.yaml")
  SHA=$(echo "${RAW}" | python3 -c 'import json,sys;print(json.load(sys.stdin)["sha"])')
  REVERTED=$(printf '%s' "${NEW_CONTENT}" | python3 -c "
import sys, re
c = sys.stdin.read()
c = re.sub(r'(subnet-01:[^a-zA-Z]*?dhcp:.*?end: )\d+\.\d+\.\d+\.\d+', r'\g<1>${CUR}', c, count=1, flags=re.DOTALL)
print(c, end='')
")
  R_B64=$(printf '%s' "${REVERTED}" | base64 | tr -d '\n')
  R_PAYLOAD=$(python3 -c "import json;print(json.dumps({'sha':'${SHA}','content':'${R_B64}','message':'demo-loop revert','branch':'main'}))")
  R_START=$(date +%s)
  R_RESP=$(GITEA_API -X PUT --data "${R_PAYLOAD}" \
    "http://localhost:3000/api/v1/repos/${REPO_OWNER}/${REPO_NAME}/contents/vpcs/${VPC}.yaml")
  R_SHA=$(echo "${R_RESP}" | python3 -c 'import json,sys;print(json.load(sys.stdin)["commit"]["sha"])')
  info "  revert commit ${R_SHA:0:10}"
  for _ in $(seq 1 30); do
    V=$(kubectl get "vpc/${VPC}" -n "${DEFAULT_NS}" -o jsonpath='{.spec.subnets.subnet-01.dhcp.range.end}' 2>/dev/null || true)
    if [ "${V}" = "${CUR}" ]; then
      info "  reverted in $(( $(date +%s) - R_START ))s"
      break
    fi
    sleep 1
  done
fi
