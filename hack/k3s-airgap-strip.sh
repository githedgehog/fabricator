#!/bin/bash
# Copyright 2026 Hedgehog
# SPDX-License-Identifier: Apache-2.0

# Strip images matching a pattern from a k3s airgap tarball.
#
# k3s ships docker.io/rancher/mirrored-library-traefik in its airgap bundle,
# but we start k3s with --disable=servicelb,traefik (see pkg/fab/recipe/control_install.go),
# so the image never runs and only contributes to our CVE alert noise.
#
# The tarball is a hybrid OCI image layout (index.json, oci-layout, blobs/)
# plus a docker-save shim (manifest.json). Both indexes need to be filtered
# so that containerd (OCI) and docker load (docker-save) ignore the image.
# Layer blobs are kept (a few MB of bloat) so the rewrite stays atomic.
#
# Usage:
#   hack/k3s-airgap-strip.sh INPUT.tar.gz OUTPUT.tar.gz [PATTERN]
# PATTERN is a regex passed to jq's test(); default: 'traefik'.

set -euo pipefail

if [[ $# -lt 2 ]]; then
    echo "Usage: $0 INPUT.tar.gz OUTPUT.tar.gz [PATTERN]" >&2
    exit 1
fi

INPUT="$1"
OUTPUT="$2"
PATTERN="${3:-traefik}"

if [[ ! -f "$INPUT" ]]; then
    echo "ERROR: input file '$INPUT' not found" >&2
    exit 1
fi

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

echo "==> Extracting $INPUT"
mkdir -p "$WORK/extract"
tar -xzf "$INPUT" -C "$WORK/extract"

if [[ ! -f "$WORK/extract/manifest.json" ]]; then
    echo "ERROR: manifest.json not found at top of tarball; not a docker-save archive" >&2
    exit 2
fi
if [[ ! -f "$WORK/extract/index.json" ]]; then
    echo "ERROR: index.json not found at top of tarball; not an OCI layout" >&2
    exit 2
fi

echo "==> Source images:"
jq -r '.[].RepoTags[]?' "$WORK/extract/manifest.json" | sort

DROPPED=$(jq -r --arg p "$PATTERN" \
    '[.[] | select(.RepoTags | any(test($p))) | .RepoTags[]?] | .[]' \
    "$WORK/extract/manifest.json")
if [[ -z "$DROPPED" ]]; then
    echo "ERROR: no images matched pattern '$PATTERN'" >&2
    exit 3
fi
echo "==> Dropping (matched '$PATTERN'):"
echo "$DROPPED" | sed 's/^/    /'

jq --arg p "$PATTERN" '[.[] | select(.RepoTags | any(test($p)) | not)]' \
    "$WORK/extract/manifest.json" > "$WORK/extract/manifest.json.new"
mv "$WORK/extract/manifest.json.new" "$WORK/extract/manifest.json"

# OCI index uses annotations["io.containerd.image.name"] for the image ref.
jq --arg p "$PATTERN" \
    '.manifests |= [.[] | select((.annotations["io.containerd.image.name"] // "") | test($p) | not)]' \
    "$WORK/extract/index.json" > "$WORK/extract/index.json.new"
mv "$WORK/extract/index.json.new" "$WORK/extract/index.json"

if [[ -f "$WORK/extract/repositories" ]]; then
    jq --arg p "$PATTERN" 'with_entries(select(.key | test($p) | not))' \
        "$WORK/extract/repositories" > "$WORK/extract/repositories.new"
    mv "$WORK/extract/repositories.new" "$WORK/extract/repositories"
fi

echo "==> Repacking to $OUTPUT"
# Match upstream layout: top-level entries with no leading "./".
tar --sort=name -C "$WORK/extract" -czf "$OUTPUT" \
    $(cd "$WORK/extract" && ls -A)

echo "==> Output images (docker-save view):"
tar -xzOf "$OUTPUT" manifest.json | jq -r '.[].RepoTags[]?' | sort
echo "==> Output images (OCI view):"
tar -xzOf "$OUTPUT" index.json | \
    jq -r '.manifests[].annotations["io.containerd.image.name"]' | sort
