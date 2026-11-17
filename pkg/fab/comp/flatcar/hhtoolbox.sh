#!/usr/bin/env bash
# Copyright 2026 Hedgehog
# SPDX-License-Identifier: Apache-2.0

# Hedgehog Toolbox: debug tools in a container with full access to the host, Flatcar's toolbox hands over to it.
# Accepts the -q and -E/--setenv flags of Flatcar's toolbox, runs bash if no command given.

set -euo pipefail

# shellcheck source=/dev/null
source /etc/hedgehog/toolbox.env
image=${HHTOOLBOX_IMAGE:-$TOOLBOX_IMAGE}

quiet=false
envs=()
while [ $# -gt 0 ]; do
  case $1 in
  -q | --quiet) quiet=true ;;
  -E | --setenv)
    envs+=(-e "${2:?$1 requires VAR=VALUE}")
    shift
    ;;
  --setenv=*) envs+=(-e "${1#--setenv=}") ;;
  --)
    shift
    break
    ;;
  -*)
    echo "toolbox: unsupported flag $1" >&2
    exit 2
    ;;
  *) break ;;
  esac
  shift
done

if ! docker image inspect "$image" >/dev/null 2>&1; then
  pull=()
  if $quiet; then
    pull=(-q)
  else
    echo "Pulling Hedgehog Toolbox from $image" >&2
  fi
  DOCKER_CONFIG=/etc/hedgehog/docker docker pull "${pull[@]}" "$image" >&2
fi

if ! $quiet; then
  version=${image##*/}
  echo "Hedgehog Toolbox ${version#*:} (privileged, host net+pid, host / at /media/root)" >&2
fi

tty=()
if [ -t 0 ] && [ -t 1 ]; then
  tty=(-t)
fi

exec docker run --rm -i "${tty[@]}" --privileged --network host --pid host \
  -v /dev:/dev -v /sys:/sys -v /:/media/root "${envs[@]}" "$image" "${@:-bash}"
