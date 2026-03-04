#!/bin/bash
# Copyright 2025 Hedgehog
# SPDX-License-Identifier: Apache-2.0

# Centralized Trivy version configuration
# Update this single file when bumping Trivy version
# Can be overridden via environment variable: TRIVY_VERSION=x.y.z ./script.sh

export TRIVY_VERSION="${TRIVY_VERSION:-0.69.3}"
