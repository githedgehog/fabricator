#!/bin/bash
# Copyright 2026 Hedgehog
# SPDX-License-Identifier: Apache-2.0

# Usage: hh-hugepages.sh <1G pages> <2M pages>
# Reserves the hugepages for the gateway dataplane before k3s starts and retries forever, as kubelet only advertises the
# hugepages available when it starts

set -uo pipefail

reserve() {
  local size=$1 want=$2
  local path=/sys/kernel/mm/hugepages/hugepages-${size}kB/nr_hugepages

  if [ ! -e "$path" ]; then
    if [ "$want" -eq 0 ]; then
      return 0
    fi
    echo "Hugepages ${size}kB aren't supported, want $want"
    return 1
  fi

  echo "$want" > "$path"
  local got
  got=$(cat "$path")
  echo "Hugepages ${size}kB: want $want, got $got"
  [ "$got" -eq "$want" ]
}

delay=1
until reserve 1048576 "$1" && reserve 2048 "$2"; do
  # free and defragment memory, so there are enough contiguous chunks for the pages
  sync
  echo 3 > /proc/sys/vm/drop_caches
  echo 1 > /proc/sys/vm/compact_memory

  sleep "$delay"
  if [ "$delay" -lt 60 ]; then
    delay=$((delay * 2))
  fi
done
