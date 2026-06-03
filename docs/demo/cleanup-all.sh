#!/usr/bin/env bash
# Copyright 2026 Hedgehog
# SPDX-License-Identifier: Apache-2.0
#
# One-command teardown for both demos. GitOps goes first so Argo CD's
# Application controller doesn't try to reconcile mid-teardown.

set -euo pipefail
SCRIPT_DIR="$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )"
"${SCRIPT_DIR}/gitops/cleanup.sh"
"${SCRIPT_DIR}/o11y/cleanup.sh"
