#!/usr/bin/env bash
# Copyright 2024 Hedgehog
# SPDX-License-Identifier: Apache-2.0

set -e

echo "Deleting all VPCs..."
kubectl delete $(kubectl get externalpeering -o name) 2> /dev/null || true
kubectl delete $(kubectl get vpcpeerings -o name) 2> /dev/null || true
kubectl delete $(kubectl get vpcattachments -o name) 2> /dev/null || true
kubectl delete $(kubectl get vpcs -o name) 2> /dev/null || true