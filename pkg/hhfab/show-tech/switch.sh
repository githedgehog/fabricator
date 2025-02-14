#!/bin/bash
# Copyright 2024 Hedgehog
# SPDX-License-Identifier: Apache-2.0

# show-tech.sh: Collect diagnostics from a SONiC switch.
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
# Interface Status
# ---------------------------
{
    echo -e "\n=== Interface Information ==="
    run_sonic_cmd "show interface status"
    run_sonic_cmd "show interface description"
    run_sonic_cmd "show interface counters"
    run_sonic_cmd "show lldp table"
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# VLAN and VXLAN Configuration
# ---------------------------
{
    echo -e "\n=== VLAN and VXLAN Configuration ==="
    run_sonic_cmd "show running-configuration"
    run_sonic_cmd "show vlan config"
    run_sonic_cmd "show vlan brief"
    run_sonic_cmd "show vxlan interface"
    run_sonic_cmd "show vxlan vlan-vni"
    run_sonic_cmd "show vxlan vrf-vni"
    run_sonic_cmd "show vxlan tunnel"
    run_sonic_cmd "show vxlan remote-vtep"
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# L2 Information
# ---------------------------
{
    echo -e "\n=== L2 Information ==="
    run_sonic_cmd "show mac address-table"
    run_sonic_cmd "show mclag brief"
    run_sonic_cmd "show mclag interface"
    run_sonic_cmd "show port-channel summary"
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# BGP and EVPN Status
# ---------------------------
{
    echo -e "\n=== BGP and EVPN Status ==="
    run_sonic_cmd "show ip bgp summary"
    run_sonic_cmd "show bgp l2vpn evpn summary"
    run_sonic_cmd "show bgp l2vpn evpn"
    
    echo -e "\n=== EVPN Information ==="
    run_sonic_cmd "show evpn vni"
    run_sonic_cmd "show evpn mac"
    run_sonic_cmd "show evpn arp-cache"
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# Platform Information
# ---------------------------
{
    echo -e "\n=== Platform Details ==="
    run_sonic_cmd "show platform environment"
    run_sonic_cmd "show platform fanstatus"
    run_sonic_cmd "show platform firmware"
    run_sonic_cmd "show platform i2c errors"
    run_sonic_cmd "show platform psusummary"
    run_sonic_cmd "show platform ssdhealth"
    run_sonic_cmd "show platform temperature"
    
    echo -e "\n=== Transceiver Information ==="
    run_sonic_cmd "show interface transceiver summary"
    run_sonic_cmd "show interface transceiver laser status"
    run_sonic_cmd "show interface transceiver wattage"
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# System Logs and Status
# ---------------------------
{
    echo -e "\n=== System Logs ==="
    run_sonic_cmd "show logging"
    
    echo -e "\n=== Hedgehog Agent Status ==="
    systemctl status hedgehog-agent
    
    echo -e "\n=== Hedgehog Agent Logs ==="
    cat /var/log/agent.log
    
    echo -e "\n=== Docker Status ==="
    docker ps
    
    echo -e "\n=== Docker Container Logs ==="
    CONTAINERS=$(docker ps --format "{{.Names}}")
    for CONTAINER in $CONTAINERS; do
        echo -e "\n--- Container: $CONTAINER ---"
        docker logs --tail 100 "$CONTAINER"
    done
} >> "$OUTPUT_FILE" 2>&1

echo "Diagnostics collected to $OUTPUT_FILE"
