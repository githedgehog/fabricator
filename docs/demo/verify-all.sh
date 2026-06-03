#!/usr/bin/env bash
# Copyright 2026 Hedgehog
# SPDX-License-Identifier: Apache-2.0
#
# One-command health check for both demos. Each sub-verify.sh exits with
# the count of failed probes; we sum them and exit non-zero if anything
# was off.

set -uo pipefail
SCRIPT_DIR="$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )"

"${SCRIPT_DIR}/o11y/verify.sh"
O11Y=$?
echo ""
"${SCRIPT_DIR}/gitops/verify.sh"
GITOPS=$?

TOTAL=$(( O11Y + GITOPS ))
echo ""
if [ "${TOTAL}" -eq 0 ]; then
  echo "All checks passed."
else
  echo "${TOTAL} probe(s) failed (${O11Y} o11y + ${GITOPS} gitops)." >&2
fi
exit "${TOTAL}"
