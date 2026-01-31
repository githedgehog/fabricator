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
    run_sonic_cmd "show interface status err-disabled"
    run_sonic_cmd "show interface description"
    run_sonic_cmd "show interface counters"
    run_sonic_cmd "show lldp table"
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# Configuration
# ---------------------------
{
    echo -e "\n=== Running Configuration ==="
    run_sonic_cmd "show running-configuration"
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# VLAN and VXLAN Information
# ---------------------------
{
    echo -e "\n=== VLAN and VXLAN Information ==="
    run_sonic_cmd "show vlan config"
    run_sonic_cmd "show vlan brief"
    run_sonic_cmd "show vlan"
    run_sonic_cmd "show vxlan interface"
    run_sonic_cmd "show vxlan vlan-vni"
    run_sonic_cmd "show vxlan vrf-vni"
    run_sonic_cmd "show vxlan tunnel"
    run_sonic_cmd "show vxlan remote-vtep"
    run_sonic_cmd "show vxlan remote mac"
    run_sonic_cmd "show vxlan remote vni"
    run_sonic_cmd "show vxlan vlanvnimap"
    run_sonic_cmd "show vxlan vrfvnimap"
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# L2 Information
# ---------------------------
{
    echo -e "\n=== L2 Information ==="
    run_sonic_cmd "show mac address-table"
    run_sonic_cmd "show mac address-table count"
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
    run_sonic_cmd "show bgp l2vpn evpn route"

    echo -e "\n=== EVPN Information ==="
    run_sonic_cmd "show evpn vni"
    run_sonic_cmd "show evpn mac"
    run_sonic_cmd "show evpn es"
    run_sonic_cmd "show evpn mac vni all"
    run_sonic_cmd "show evpn vni detail"
    run_sonic_cmd "show evpn arp-cache"
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# Route Tables
# ---------------------------
{
    echo -e "\n=== Route Table (default VRF) ==="
    run_sonic_cmd "show ip route"
    echo -e "\n=== VRF List ==="
    run_sonic_cmd "show ip vrf"
    echo -e "\n=== Route Summary (all VRFs) ==="
    run_sonic_cmd "show ip route vrf all summary"
    echo -e "\n=== Routes (all VRFs) ==="
    run_sonic_cmd "show ip route vrf all"

    # --- Per-VRF route and ARP tables ---
    vrfs=$(sonic-cli -c "show ip vrf | no-more" | awk 'NR>2{print $1}')
    for vrf in $vrfs; do
        echo -e "\n=== Routes for VRF: $vrf ===" >> "$OUTPUT_FILE"
        run_sonic_cmd "show ip route vrf $vrf"
        echo -e "\n=== ARP for VRF: $vrf ===" >> "$OUTPUT_FILE"
        run_sonic_cmd "show ip arp vrf $vrf"
        echo -e "\n=== BGP IPv4 Unicast Summary for VRF: $vrf ===" >> "$OUTPUT_FILE"
        run_sonic_cmd "show bgp ipv4 unicast vrf $vrf summary"
    done
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
# Critical Resources
# ---------------------------
{
    echo -e "\n=== Critical Resource Monitoring ==="
    run_sonic_cmd "show crm resources all"
    run_sonic_cmd "show crm thresholds all"
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# Broadcom SDK Diagnostics
# ---------------------------
{
    echo -e "\n=== Broadcom Port Status ==="
    bcmcmd "ps"

    echo -e "\n=== Broadcom PHY Information ==="
    bcmcmd "phy info"

    echo -e "\n=== Broadcom L2 Table ==="
    bcmcmd "l2 show"

    echo -e "\n=== Broadcom L3 Interfaces ==="
    bcmcmd "l3 intf show"

    echo -e "\n=== Broadcom L3 ACLs ==="
    bcmcmd "l3 aacl show"

    echo -e "\n=== Broadcom L3 Route Table ==="
    bcmcmd "l3 route show"

    echo -e "\n=== Broadcom L3 ECMP Table ==="
    bcmcmd "l3 ecmp show"

    echo -e "\n=== Broadcom L3 Host Table ==="
    bcmcmd "l3 host show"

    echo -e "\n=== Broadcom VLAN Table ==="
    bcmcmd "vlan show"

    echo -e "\n=== Broadcom Trunk Table ==="
    bcmcmd "trunk show"
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# System Logs and Status
# ---------------------------
{
    echo -e "\n=== System Status ==="
    run_sonic_cmd "show system status brief"

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
