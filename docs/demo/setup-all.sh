#!/usr/bin/env bash
# Copyright 2026 Hedgehog
# SPDX-License-Identifier: Apache-2.0
#
# One-command install for both demos. The two demos are independent, so
# either order works; we run o11y first because the GitOps demo's
# Argo CD pulls take a bit longer than VM/VL/Grafana.

set -euo pipefail
SCRIPT_DIR="$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )"
"${SCRIPT_DIR}/o11y/setup.sh"
"${SCRIPT_DIR}/gitops/setup.sh"
