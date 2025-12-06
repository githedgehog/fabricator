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

    echo -e "\n=== VRF-VNI Consistency Check ==="
    # Check that each VRF has a proper VNI mapping
    vrfs=$(sonic-cli -c "show ip vrf | no-more" | awk 'NR>2{print $1}')
    for vrf in $vrfs; do
        echo -e "\n--- VRF: $vrf ---" >> "$OUTPUT_FILE"
        echo "VNI mapping:" >> "$OUTPUT_FILE"
        sonic-cli -c "show vxlan vrfvnimap | grep $vrf" >> "$OUTPUT_FILE" 2>&1
        echo "BGP L2VPN EVPN VNI status:" >> "$OUTPUT_FILE"
        sonic-cli -c "show bgp l2vpn evpn vni | grep $vrf" >> "$OUTPUT_FILE" 2>&1
    done
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
# VRF Configuration and Status
# ---------------------------
{
    echo -e "\n=== VRF Configuration ==="
    run_sonic_cmd "show ip vrf"

    echo -e "\n=== VRF Interface Membership ==="
    # Show which interfaces belong to which VRF
    vrfs=$(sonic-cli -c "show ip vrf | no-more" | awk 'NR>2{print $1}')
    for vrf in $vrfs; do
        echo -e "\n--- VRF: $vrf ---" >> "$OUTPUT_FILE"
        sonic-cli -c "show running-configuration | grep -A2 \"ip vrf forwarding $vrf\"" >> "$OUTPUT_FILE" 2>&1
    done

    echo -e "\n=== VLAN Operational Status ==="
    # Check operational status of all VLANs
    ip -br link show type vlan >> "$OUTPUT_FILE" 2>&1

    echo -e "\n=== VLAN Interface Details ==="
    # Detailed VLAN interface status
    for vlan_intf in $(ip -br link show type vlan | awk '{print $1}'); do
        echo -e "\n--- Interface: $vlan_intf ---" >> "$OUTPUT_FILE"
        ip addr show dev "$vlan_intf" 2>&1 >> "$OUTPUT_FILE"
        echo "VRF binding:" >> "$OUTPUT_FILE"
        ip link show "$vlan_intf" 2>&1 | grep -i "vrf" >> "$OUTPUT_FILE"
    done

    echo -e "\n=== Anycast Gateway Reachability ==="
    # Test reachability to anycast gateway IPs configured on VLANs
    # Extract anycast-address from running config
    echo "Testing local anycast gateway IPs..." >> "$OUTPUT_FILE"
    sonic-cli -c "show running-configuration | grep \"ip anycast-address\"" 2>/dev/null | while read -r line; do
        gw_ip=$(echo "$line" | awk '{print $3}' | cut -d'/' -f1)
        if [ -n "$gw_ip" ]; then
            echo -e "\n--- Testing gateway: $gw_ip ---" >> "$OUTPUT_FILE"
            # Find which interface has this IP
            vlan_intf=$(ip -4 -br addr show | grep "$gw_ip" | awk '{print $1}')
            if [ -n "$vlan_intf" ]; then
                echo "Gateway $gw_ip is on interface $vlan_intf" >> "$OUTPUT_FILE"
                # Check if we can ARP resolve it from the switch itself
                ip neigh show dev "$vlan_intf" | grep "$gw_ip" >> "$OUTPUT_FILE" 2>&1
            else
                echo "WARNING: Gateway $gw_ip not found on any interface!" >> "$OUTPUT_FILE"
            fi
        fi
    done
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
        echo -e "\n=== BGP EVPN VNI for VRF: $vrf ===" >> "$OUTPUT_FILE"
        run_sonic_cmd "show bgp l2vpn evpn vni $vrf"
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
# Recent Configuration Changes
# ---------------------------
{
    echo -e "\n=== Recent VRF and VLAN Configuration Changes ==="
    echo "Last 500 lines from orchestration agents showing VRF/VLAN changes:"

    echo -e "\n--- Recent VRF Manager Events ---" >> "$OUTPUT_FILE"
    docker logs --tail 500 --timestamps swss 2>&1 | grep -E "vrfmgrd|VRF|VrfVvpc" | tail -200 >> "$OUTPUT_FILE"

    echo -e "\n--- Recent Interface Manager Events (VLANs) ---" >> "$OUTPUT_FILE"
    docker logs --tail 500 --timestamps swss 2>&1 | grep -E "intfmgrd.*Vlan|doIntfGeneralTask.*Vlan|sagStateDbUpdate.*Vlan" | tail -200 >> "$OUTPUT_FILE"

    echo -e "\n--- Recent Orchestration Agent Events (VRF/VLAN) ---" >> "$OUTPUT_FILE"
    docker logs --tail 500 --timestamps swss 2>&1 | grep -E "orchagent.*VRF|orchagent.*Vlan.*vrf|addRoute.*vrf|removeRoute.*vrf" | tail -200 >> "$OUTPUT_FILE"

    echo -e "\n--- Recent BGP FPM Events (Route changes) ---" >> "$OUTPUT_FILE"
    docker logs --tail 500 --timestamps bgp 2>&1 | grep -E "fpmsyncd.*VrfVvpc|fpmsyncd.*10.0.[0-9]" | tail -200 >> "$OUTPUT_FILE"

    echo -e "\n--- Recent VLAN Manager Events ---" >> "$OUTPUT_FILE"
    docker logs --tail 500 --timestamps swss 2>&1 | grep -E "vlanmgrd|doVlanTask|doVlanMember" | tail -200 >> "$OUTPUT_FILE"

    echo -e "\n--- Recent Interface Status Changes ---" >> "$OUTPUT_FILE"
    docker logs --tail 500 --timestamps eventd 2>&1 | grep -E "INTERFACE_OPER_STATUS.*Vlan" | tail -100 >> "$OUTPUT_FILE"
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

# ---------------------------
# Extract Errors to Separate File
# ---------------------------
ERROR_FILE="/tmp/show-tech-errors.log"
: > "$ERROR_FILE"

{
    echo "ERROR AND WARNING SUMMARY"
    echo "========================="
    echo ""
    echo "Extracted from: $(hostname) at $(date)"
    echo ""

    # Extract errors and warnings from main log
    echo "=== ERRORS AND WARNINGS FROM SHOW-TECH ==="
    grep -E "ERR|FAIL|WARN|ERROR|WARNING|error|fail" "$OUTPUT_FILE" | grep -v "err-disabled" | head -500

    echo ""
    echo "=== SUMMARY ==="

    # Count occurrences
    err_count=$(grep -c "ERR" "$OUTPUT_FILE" 2>/dev/null || echo 0)
    fail_count=$(grep -c "FAIL" "$OUTPUT_FILE" 2>/dev/null || echo 0)
    warn_count=$(grep -c "WARN" "$OUTPUT_FILE" 2>/dev/null || echo 0)

    echo "Total ERR messages: $err_count"
    echo "Total FAIL messages: $fail_count"
    echo "Total WARN messages: $warn_count"

    echo ""
    echo "=== MOST COMMON ERROR PATTERNS ==="

    # Find most common error patterns (top 10)
    grep -E "ERR|FAIL" "$OUTPUT_FILE" 2>/dev/null | \
        sed 's/[0-9][0-9]:[0-9][0-9]:[0-9][0-9]\.[0-9]*/TIME/g' | \
        sed 's/Vlan[0-9]*/VlanX/g' | \
        sed 's/[0-9]\{1,3\}\.[0-9]\{1,3\}\.[0-9]\{1,3\}\.[0-9]\{1,3\}/IP/g' | \
        sort | uniq -c | sort -rn | head -10 || echo "No patterns found"

} > "$ERROR_FILE" 2>&1

echo "Diagnostics collected to $OUTPUT_FILE"
echo "Errors extracted to $ERROR_FILE"
