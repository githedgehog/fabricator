#!/usr/bin/env bash
# Copyright 2024 Hedgehog
# SPDX-License-Identifier: Apache-2.0
#
# Deletes cancelled GitHub Actions workflow runs from the past N days,
# grouped by branch. For each branch, shows the most recent completed run
# as a sanity check before offering to delete the cancelled ones.
# By default only dependabot branches are included; use --all for all branches.
#
# Usage: ./hack/cleanup-cancelled-runs.sh [--all] [-y] [days]
#   --all  include non-dependabot branches (default: dependabot/* only)
#   -y     skip confirmation prompts and delete all
#   days   how far back to look (default: 7)

set -euo pipefail

REPO="githedgehog/fabricator"
ALL_BRANCHES=false
YES=false
DAYS=7

for arg in "$@"; do
    if [[ "${arg}" == "--all" ]]; then
        ALL_BRANCHES=true
    elif [[ "${arg}" == "-y" ]]; then
        YES=true
    elif [[ "${arg}" =~ ^[0-9]+$ ]]; then
        DAYS="${arg}"
    fi
done

# Cross-platform date calculation (GNU/Linux vs BSD/macOS)
if date --version >/dev/null 2>&1; then
    SINCE=$(date -d "${DAYS} days ago" --utc +"%Y-%m-%dT%H:%M:%SZ")
else
    SINCE=$(date -v-"${DAYS}"d -u +"%Y-%m-%dT%H:%M:%SZ")
fi

echo "Fetching cancelled runs from the past ${DAYS} days (since ${SINCE})..."

# Fetch all pages of cancelled runs, filter to the requested window.
# --paginate applies the jq filter per page; jq -s collects into one array.
RUNS=$(gh api --paginate \
    "repos/${REPO}/actions/runs?status=cancelled&per_page=100" \
    --jq ".workflow_runs[] | select(.created_at >= \"${SINCE}\") | {id: .id, branch: .head_branch, title: .display_title, created_at: .created_at}" \
    | jq -s '.')

if [[ "${ALL_BRANCHES}" == "false" ]]; then
    RUNS=$(echo "${RUNS}" | jq '[.[] | select(.branch | startswith("dependabot/"))]')
fi

TOTAL=$(echo "${RUNS}" | jq 'length')

if [[ "${TOTAL}" -eq 0 ]]; then
    echo "No cancelled runs found."
    [[ "${ALL_BRANCHES}" == "false" ]] && echo "Use --all to include non-dependabot branches."
    exit 0
fi

BRANCH_COUNT=$(echo "${RUNS}" | jq -r '[.[].branch] | unique | length')
SCOPE=$([[ "${ALL_BRANCHES}" == "true" ]] && echo "all branches" || echo "dependabot branches")
echo "Found ${TOTAL} cancelled run(s) across ${BRANCH_COUNT} ${SCOPE}."

BRANCHES=$(echo "${RUNS}" | jq -r '[.[].branch] | unique | .[]')
TOTAL_DELETED=0

while IFS= read -r branch; do
    BRANCH_RUNS=$(echo "${RUNS}" | jq --arg b "${branch}" \
        '[.[] | select(.branch == $b)] | sort_by(.created_at) | reverse')
    COUNT=$(echo "${BRANCH_RUNS}" | jq 'length')

    echo ""
    echo "  branch    : ${branch}"
    echo "  cancelled : ${COUNT}"
    echo "${BRANCH_RUNS}" | jq -r '.[] | "    - \(.created_at)  \(.title)"'

    # Fetch the most recent non-cancelled completed run for this branch
    LAST_RUN=$(gh api "repos/${REPO}/actions/runs?branch=${branch}&per_page=10" \
        --jq '[.workflow_runs[] | select(.status == "completed" and .conclusion != "cancelled")] | first // empty')

    if [[ -n "${LAST_RUN}" ]]; then
        LAST_DATE=$(echo "${LAST_RUN}" | jq -r '.created_at')
        LAST_CONCLUSION=$(echo "${LAST_RUN}" | jq -r '.conclusion')
        LAST_TITLE=$(echo "${LAST_RUN}" | jq -r '.display_title')
        echo "  last run  : [${LAST_CONCLUSION}] ${LAST_DATE}  ${LAST_TITLE}"
    else
        echo "  last run  : (none found — branch may never have run CI to completion)"
    fi

    if [[ "${YES}" == "false" ]]; then
        read -rp "  Delete ${COUNT} cancelled run(s)? [y/N] " confirm </dev/tty
    else
        confirm="y"
    fi
    if [[ "${confirm,,}" == "y" ]]; then
        while IFS= read -r id; do
            gh api -X DELETE "repos/${REPO}/actions/runs/${id}"
        done < <(echo "${BRANCH_RUNS}" | jq -r '.[].id')
        echo "  Deleted ${COUNT} run(s)."
        TOTAL_DELETED=$((TOTAL_DELETED + COUNT))
    fi
done <<< "${BRANCHES}"

echo ""
echo "Done. Deleted ${TOTAL_DELETED} of ${TOTAL} cancelled run(s)."
