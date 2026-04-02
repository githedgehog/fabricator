#!/bin/bash
# Copyright 2025 Hedgehog
# SPDX-License-Identifier: Apache-2.0

# show-tech.sh: Collect diagnostics from a DS2000 hardware external SONiC switch.
set +e

OUTPUT_FILE="/tmp/show-tech.log"

: > "$OUTPUT_FILE"

# Helper function for sonic-cli commands
run_sonic_cmd() {
    echo -e "\n=== Executing: sonic-cli -c '$1' ===" >> "$OUTPUT_FILE"
    sonic-cli -c "$1 | no-more" >> "$OUTPUT_FILE" 2>/dev/null
}

# ---------------------------
# Basic System Information
# ---------------------------
{
    echo "=== System Information ==="
    run_sonic_cmd "show version"
    run_sonic_cmd "show uptime"
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# VRF Configuration
# ---------------------------
{
    echo -e "\n=== VRF Information ==="
    run_sonic_cmd "show ip vrf"
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# Interface Status
# ---------------------------
{
    echo -e "\n=== Interface Information ==="
    run_sonic_cmd "show interface status"
    run_sonic_cmd "show interface status err-disabled"
    run_sonic_cmd "show interface description"
    run_sonic_cmd "show interface counters"
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# Routing Tables (all VRFs)
# ---------------------------
{
    echo -e "\n=== Routing Tables ==="
    run_sonic_cmd "show ip route"
    run_sonic_cmd "show ip route vrf all"
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# ARP Tables (all VRFs)
# ---------------------------
{
    echo -e "\n=== ARP Tables ==="
    run_sonic_cmd "show ip arp"
    run_sonic_cmd "show ip arp vrf all"
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# BGP (all VRFs)
# ---------------------------
{
    echo -e "\n=== BGP ==="
    run_sonic_cmd "show ip bgp summary"
    run_sonic_cmd "show ip bgp vrf all summary"
    run_sonic_cmd "show ip bgp neighbors"
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# NAT
# ---------------------------
{
    echo -e "\n=== NAT ==="
    run_sonic_cmd "show ip nat"
    run_sonic_cmd "show ip nat translations"
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# Running Configuration
# ---------------------------
{
    echo -e "\n=== Running Configuration ==="
    run_sonic_cmd "show running-configuration"
} >> "$OUTPUT_FILE" 2>&1

echo "Diagnostics collected to $OUTPUT_FILE"
