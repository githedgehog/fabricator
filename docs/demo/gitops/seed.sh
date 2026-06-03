#!/usr/bin/env bash
# Copyright 2026 Hedgehog
# SPDX-License-Identifier: Apache-2.0
#
# Runs inside the gitops-demo-seed Job (image: alpine/k8s).
#
# Idempotent. Re-running is a no-op when:
#   - the 'fabric-config' repo already exists in Gitea
#   - the files already match the cluster state
#   - the Gitea webhook to Argo CD is already present

set -euo pipefail

GITEA_URL="${GITEA_URL:-http://gitea-http.demo.svc.cluster.local:3000}"
GITEA_USER="${GITEA_USER:-admin}"
GITEA_PASS="${GITEA_PASS:-admin}"
REPO_NAME="${REPO_NAME:-fabric-config}"
DEMO_NAMESPACE="${DEMO_NAMESPACE:-demo}"
DEFAULT_NAMESPACE="${DEFAULT_NAMESPACE:-default}"
WORK="${WORK:-/tmp/seed}"
ARGOCD_WEBHOOK_URL="${ARGOCD_WEBHOOK_URL:-http://argocd-server.demo.svc.cluster.local/api/webhook}"
STATE_CM="${STATE_CM:-gitops-demo-state}"
SETUP_RUN_ID="${SETUP_RUN_ID:-unset}"
# WEBHOOK_TOKEN is injected via env.valueFrom.secretKeyRef in the Job spec
# (see setup.sh). Reading from env avoids granting the Job's SA permission
# to list/read Secrets at all.
WEBHOOK_TOKEN="${WEBHOOK_TOKEN:?WEBHOOK_TOKEN must be set (see setup.sh Job spec)}"

GITEA_API() {
  local method="$1" path="$2"; shift 2
  curl -sS -u "${GITEA_USER}:${GITEA_PASS}" -H 'Content-Type: application/json' \
    -X "${method}" "${GITEA_URL}/api/v1${path}" "$@"
}

GITEA_API_CODE() {
  local method="$1" path="$2"; shift 2
  # On transient connection failure, curl exits non-zero, which under
  # `set -e` propagates out of the calling `$(...)` and kills the script
  # (notably during the wait-for-Gitea-API loop). Trap that with `|| echo
  # 000` so callers see a sentinel HTTP code and can decide what to do.
  curl -sS -o /dev/null -w '%{http_code}' -u "${GITEA_USER}:${GITEA_PASS}" -H 'Content-Type: application/json' \
    -X "${method}" "${GITEA_URL}/api/v1${path}" "$@" || echo "000"
}

info()  { printf '==> %s\n' "$*"; }

# 0) Determine IMPORT_MODE -------------------------------------------------------
# Each ./setup.sh invocation passes a fresh SETUP_RUN_ID. We record the last
# successfully-handled run-id in a small ConfigMap.
#   - new run-id        => IMPORT_MODE=true   (overwrite files in git with cluster state)
#   - same run-id       => IMPORT_MODE=false  (create-only; this is a Job-pod retry)
# We bump the recorded run-id BEFORE doing any imports, so a mid-flight failure
# doesn't cause a retry to re-clobber files a user edited in the meantime.
STORED_RUN_ID="$(kubectl get configmap "${STATE_CM}" -n "${DEMO_NAMESPACE}" \
  -o jsonpath='{.data.last-setup-run-id}' 2>/dev/null || true)"
if [ "${SETUP_RUN_ID}" != "${STORED_RUN_ID}" ]; then
  IMPORT_MODE=true
  info "New setup.sh run (SETUP_RUN_ID=${SETUP_RUN_ID}, prev=${STORED_RUN_ID:-<none>}) — IMPORT MODE: existing files in git will be overwritten with current cluster state"
  # Persist the new run-id immediately so retries see same==same and stay safe.
  kubectl create configmap "${STATE_CM}" -n "${DEMO_NAMESPACE}" \
    --from-literal=last-setup-run-id="${SETUP_RUN_ID}" \
    --dry-run=client -o yaml | kubectl apply -f - >/dev/null
else
  IMPORT_MODE=false
  info "Job-pod retry of the same setup.sh run (SETUP_RUN_ID=${SETUP_RUN_ID}) — CREATE-ONLY MODE: existing files in git will not be touched"
fi

# 1) Wait for Gitea API ----------------------------------------------------------
info "Waiting for Gitea API"
for _ in $(seq 1 60); do
  if [ "$(GITEA_API_CODE GET /version)" = "200" ]; then
    info "Gitea is up: $(GITEA_API GET /version)"
    break
  fi
  sleep 2
done

# 2) Create repo if missing ------------------------------------------------------
info "Ensuring repo ${GITEA_USER}/${REPO_NAME} exists"
code="$(GITEA_API_CODE GET "/repos/${GITEA_USER}/${REPO_NAME}")"
if [ "${code}" = "404" ]; then
  GITEA_API POST '/user/repos' --data "$(jq -n \
    --arg name "${REPO_NAME}" \
    '{name:$name, default_branch:"main", auto_init:true, description:"Hedgehog fabric — managed by Argo CD"}')" > /dev/null
  info "Created repo ${REPO_NAME}"
else
  info "Repo already exists (HTTP ${code})"
fi

# 2a) Migration cleanup: an earlier Flux-era seed pushed a root
#     `kustomization.yaml` listing every per-resource file. Argo CD's
#     directory-recurse mode mis-parses that as an invalid Kustomization CR
#     and the Application is stuck OutOfSync. Remove the file on IMPORT runs.
if [ "${IMPORT_MODE}" = "true" ]; then
  km_code="$(GITEA_API_CODE GET "/repos/${GITEA_USER}/${REPO_NAME}/contents/kustomization.yaml")"
  if [ "${km_code}" = "200" ]; then
    km_sha="$(GITEA_API GET "/repos/${GITEA_USER}/${REPO_NAME}/contents/kustomization.yaml" | jq -r '.sha')"
    GITEA_API DELETE "/repos/${GITEA_USER}/${REPO_NAME}/contents/kustomization.yaml" --data "$(jq -n \
      --arg sha "${km_sha}" '{sha:$sha, message:"remove Flux-era kustomization.yaml", branch:"main"}')" >/dev/null
    info "Removed legacy kustomization.yaml from repo"
  fi
fi

# 3) Export CRs from default ns + write files in the repo ------------------------
mkdir -p "${WORK}"
EXPORTED=0

# Map: kind -> directory in repo
declare -A KIND_DIR=(
  [vpcs.vpc.githedgehog.com]=vpcs
  [vpcattachments.vpc.githedgehog.com]=vpcattachments
  [vpcpeerings.vpc.githedgehog.com]=vpcpeerings
  [externalpeerings.vpc.githedgehog.com]=externalpeerings
  [gatewaypeerings.gateway.githedgehog.com]=gatewaypeerings
)


for kind in "${!KIND_DIR[@]}"; do
  dir="${KIND_DIR[$kind]}"
  info "Exporting ${kind} from ns/${DEFAULT_NAMESPACE}"
  # `kubectl get <kind> -n default -o yaml` returns a List; split per-item.
  kubectl get "${kind}" -n "${DEFAULT_NAMESPACE}" -o json 2>/dev/null \
    | jq -r '.items[]? | .metadata.name' \
    | while read -r name; do
        [ -z "${name}" ] && continue
        # Per-item export, scrub mutating-webhook-injected metadata + status.
        kubectl get "${kind}" "${name}" -n "${DEFAULT_NAMESPACE}" -o yaml \
          | yq eval '
              del(.metadata.resourceVersion, .metadata.uid, .metadata.creationTimestamp,
                  .metadata.generation, .metadata.managedFields, .metadata.ownerReferences,
                  .status)
            ' - \
          > "${WORK}/${dir}__${name}.yaml"
        echo "${dir}/${name}.yaml"
      done >> "${WORK}/_index.txt" || true
done

# 4) Push (create-or-update) each file via Gitea API -----------------------------
push_file() {
  # IMPORT_MODE=true   create-or-update (this Job invocation came from a fresh
  #                    ./setup.sh run; overwrite git with current cluster state)
  # IMPORT_MODE=false  create-only (Job-pod retry; never overwrite a user edit)
  local repo_path="$1" local_file="$2"
  # `base64 -w0` is GNU-only; use `| tr -d '\n'` for portability (BusyBox
  # / BSD base64 don't accept -w).
  local content_b64; content_b64="$(base64 < "${local_file}" | tr -d '\n')"
  local existing_code
  existing_code="$(GITEA_API_CODE GET "/repos/${GITEA_USER}/${REPO_NAME}/contents/${repo_path}")"
  if [ "${existing_code}" = "200" ]; then
    if [ "${IMPORT_MODE}" != "true" ]; then
      return 0
    fi
    # Overwrite (only if content actually changed, to avoid no-op commits).
    local resp; resp="$(GITEA_API GET "/repos/${GITEA_USER}/${REPO_NAME}/contents/${repo_path}")"
    local existing_sha; existing_sha="$(echo "${resp}" | jq -r '.sha')"
    local current_b64;  current_b64="$(echo "${resp}"  | jq -r '.content' | tr -d '\n')"
    if [ "${current_b64}" = "${content_b64}" ]; then
      return 0
    fi
    GITEA_API PUT "/repos/${GITEA_USER}/${REPO_NAME}/contents/${repo_path}" --data "$(jq -n \
      --arg sha "${existing_sha}" --arg content "${content_b64}" --arg msg "re-import ${repo_path}" \
      '{sha:$sha, content:$content, message:$msg, branch:"main"}')" >/dev/null
    echo "  updated ${repo_path}"
  else
    GITEA_API POST "/repos/${GITEA_USER}/${REPO_NAME}/contents/${repo_path}" --data "$(jq -n \
      --arg content "${content_b64}" --arg msg "add ${repo_path}" \
      '{content:$content, message:$msg, branch:"main"}')" >/dev/null
    echo "  added ${repo_path}"
  fi
}

info "Pushing exported manifests to ${REPO_NAME}"
shopt -s nullglob
for f in "${WORK}"/*__*.yaml; do
  base="$(basename "${f}")"        # vpcs__vpc-1.yaml
  dir="${base%%__*}"
  name="${base#*__}"
  push_file "${dir}/${name}" "${f}"
  EXPORTED=$((EXPORTED+1))
done
shopt -u nullglob
info "Exported ${EXPORTED} manifest(s)"

# 5) (no root kustomization.yaml needed — Argo CD's Application uses
#    `directory.recurse=true` to discover every *.yaml under the repo. Skipping
#    the Flux-style root kustomization keeps the repo tree visually cleaner.)

# 6) Wire Gitea webhook → Argo CD /api/webhook -----------------------------------
info "Webhook target: ${ARGOCD_WEBHOOK_URL}"

EXISTING="$(GITEA_API GET "/repos/${GITEA_USER}/${REPO_NAME}/hooks" \
  | jq --arg url "${ARGOCD_WEBHOOK_URL}" '[.[] | select(.config.url==$url)] | length')"
if [ "${EXISTING}" = "0" ]; then
  # Gitea-native webhook. The 'secret' is used by Gitea to compute
  # X-Gitea-Signature; Argo CD verifies that header against the same value
  # in argocd-secret's webhook.gitea.secret key (set by setup.sh's HelmChart).
  GITEA_API POST "/repos/${GITEA_USER}/${REPO_NAME}/hooks" --data "$(jq -n \
    --arg url "${ARGOCD_WEBHOOK_URL}" --arg secret "${WEBHOOK_TOKEN}" \
    '{
      type: "gitea",
      active: true,
      events: ["push"],
      config: { url: $url, content_type: "json", secret: $secret }
    }')" >/dev/null
  info "Created Gitea webhook → Argo CD"
else
  info "Gitea webhook already exists"
fi

info "Seed complete."
