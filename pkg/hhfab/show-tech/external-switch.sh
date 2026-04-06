#!/bin/bash
# Copyright 2025 Hedgehog
# SPDX-License-Identifier: Apache-2.0

# show-tech.sh: Collect diagnostics from a DS2000 hardware external SONiC switch.
# Commands run in parallel (up to MAX_PARALLEL) to reduce collection time.
set +e

OUTPUT_FILE="/tmp/show-tech.log"
PARTS_DIR="/tmp/show-tech-parts"
MAX_PARALLEL=6

: > "$OUTPUT_FILE"
rm -rf "$PARTS_DIR"
mkdir -p "$PARTS_DIR"

PART_IDX=0

queue_sonic_cmd() {
    local cmd="$1"
    PART_IDX=$((PART_IDX + 1))
    local idx=$PART_IDX
    local part="$PARTS_DIR/$(printf '%03d' "$idx")"
    (
        echo -e "\n=== Executing: sonic-cli -c '$cmd' ==="
        sonic-cli -c "$cmd | no-more" 2>/dev/null
    ) > "$part" &

    while [ "$(jobs -rp | wc -l)" -ge "$MAX_PARALLEL" ]; do
        wait -n 2>/dev/null || sleep 0.1
    done
}

# ---------------------------
# Basic System Information
# ---------------------------
queue_sonic_cmd "show version"
queue_sonic_cmd "show uptime"

# ---------------------------
# VRF Configuration
# ---------------------------
queue_sonic_cmd "show ip vrf"

# ---------------------------
# Interface Status
# ---------------------------
queue_sonic_cmd "show interface status"
queue_sonic_cmd "show interface status err-disabled"
queue_sonic_cmd "show interface description"
queue_sonic_cmd "show interface counters"

# ---------------------------
# Routing Tables (all VRFs)
# ---------------------------
queue_sonic_cmd "show ip route"
queue_sonic_cmd "show ip route vrf all"

# ---------------------------
# ARP Tables (all VRFs)
# ---------------------------
queue_sonic_cmd "show ip arp"
queue_sonic_cmd "show ip arp vrf all"

# ---------------------------
# BGP (all VRFs)
# ---------------------------
queue_sonic_cmd "show ip bgp summary"
queue_sonic_cmd "show ip bgp vrf all summary"
queue_sonic_cmd "show ip bgp neighbors"

# ---------------------------
# NAT
# ---------------------------
queue_sonic_cmd "show ip nat"
queue_sonic_cmd "show ip nat translations"

# ---------------------------
# Running Configuration
# ---------------------------
queue_sonic_cmd "show running-configuration"

# Wait for all commands and concatenate in order
wait
cat "$PARTS_DIR"/* >> "$OUTPUT_FILE"

# Cleanup
rm -rf "$PARTS_DIR"

echo "Diagnostics collected to $OUTPUT_FILE"
