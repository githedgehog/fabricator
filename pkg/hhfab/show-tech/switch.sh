#!/bin/bash
# Copyright 2024 Hedgehog
# SPDX-License-Identifier: Apache-2.0

# show-tech.sh: Collect diagnostics from a SONiC switch.
# Commands run in parallel (up to MAX_PARALLEL) to reduce collection time.
set +e

OUTPUT_FILE="/tmp/show-tech.log"
PARTS_DIR="/tmp/show-tech-parts"
MAX_PARALLEL=6

: > "$OUTPUT_FILE"
rm -rf "$PARTS_DIR"
mkdir -p "$PARTS_DIR"

# Counter for ordering output parts
PART_IDX=0

# Queue a sonic-cli command to run in parallel.
# Output is captured to a numbered part file for ordered concatenation.
queue_sonic_cmd() {
    local label="$1"
    local cmd="$2"
    PART_IDX=$((PART_IDX + 1))
    local idx=$PART_IDX
    local part="$PARTS_DIR/$(printf '%03d' "$idx")"
    (
        echo -e "\n=== [$label] Executing: sonic-cli -c '$cmd' ==="
        sonic-cli -c "$cmd | no-more" 2>/dev/null
    ) > "$part" &

    # Enforce concurrency limit
    while [ "$(jobs -rp | wc -l)" -ge "$MAX_PARALLEL" ]; do
        wait -n 2>/dev/null || sleep 0.1
    done
}

# ---------------------------
# Basic System Information
# ---------------------------
queue_sonic_cmd "System" "show version"
queue_sonic_cmd "System" "show uptime"

# ---------------------------
# Interface Status
# ---------------------------
queue_sonic_cmd "Interface" "show interface status"
queue_sonic_cmd "Interface" "show interface status err-disabled"
queue_sonic_cmd "Interface" "show interface description"
queue_sonic_cmd "Interface" "show interface counters"
queue_sonic_cmd "Interface" "show lldp table"

# ---------------------------
# Configuration
# ---------------------------
queue_sonic_cmd "Config" "show running-configuration"

# ---------------------------
# VLAN and VXLAN Information
# ---------------------------
queue_sonic_cmd "VLAN/VXLAN" "show vlan config"
queue_sonic_cmd "VLAN/VXLAN" "show vlan brief"
queue_sonic_cmd "VLAN/VXLAN" "show vlan"
queue_sonic_cmd "VLAN/VXLAN" "show vxlan interface"
queue_sonic_cmd "VLAN/VXLAN" "show vxlan vlan-vni"
queue_sonic_cmd "VLAN/VXLAN" "show vxlan vrf-vni"
queue_sonic_cmd "VLAN/VXLAN" "show vxlan tunnel"
queue_sonic_cmd "VLAN/VXLAN" "show vxlan remote-vtep"
queue_sonic_cmd "VLAN/VXLAN" "show vxlan remote mac"
queue_sonic_cmd "VLAN/VXLAN" "show vxlan remote vni"
queue_sonic_cmd "VLAN/VXLAN" "show vxlan vlanvnimap"
queue_sonic_cmd "VLAN/VXLAN" "show vxlan vrfvnimap"

# ---------------------------
# L2 Information
# ---------------------------
queue_sonic_cmd "L2" "show mac address-table"
queue_sonic_cmd "L2" "show mclag brief"
queue_sonic_cmd "L2" "show mclag interface"
queue_sonic_cmd "L2" "show port-channel summary"

# teamd per-LAG state captures LACP partner info and member oper status,
# which "show port-channel summary" hides when a LAG is err-disabled.
# Enumerate via /sys/class/net rather than parsing sonic-cli output, which
# is empty on some SONiC builds.
{
    echo -e "\n=== Teamd LAG State ==="
    for pc in $(ls /sys/class/net/ 2>/dev/null | grep '^PortChannel' | sort); do
        echo -e "\n--- teamdctl $pc state ---"
        teamdctl "$pc" state 2>&1
    done
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# QoS / Queue Counters
# ---------------------------
# Direct view of the per-queue transmit/receive counters and PFC state that
# RoCE / DSCP-marking tests assert on. The agent serialises a filtered subset
# into kube state, so the raw view is needed when those tests fail.
queue_sonic_cmd "QoS" "show queue counters"
queue_sonic_cmd "QoS" "show priority-flow-control statistics"
queue_sonic_cmd "QoS" "show qos map dscp-tc"
queue_sonic_cmd "QoS" "show qos map tc-queue"
queue_sonic_cmd "QoS" "show qos map tc-pg"
queue_sonic_cmd "QoS" "show qos scheduler-policy"
queue_sonic_cmd "QoS" "show qos wred-policy"

# ---------------------------
# err-disable / link-flap timeline
# ---------------------------
# show interface status err-disabled only gives the latest event per port; this
# filtered syslog view gives the full sequence (with timestamps) so post-mortems
# can reconstruct flap/recovery against the test timeline. Scan only the current
# syslog so log rotation can't bury the most recent events under older ones.
{
    echo -e "\n=== Error-Disable Timeline ==="
    grep -hE "ERR_DISABLED|err-disable|err_disable|lag-status-down" /var/log/syslog 2>/dev/null | tail -200
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# BGP and EVPN Status
# ---------------------------
queue_sonic_cmd "BGP/EVPN" "show ip bgp summary"
queue_sonic_cmd "BGP/EVPN" "show bgp l2vpn evpn summary"
queue_sonic_cmd "BGP/EVPN" "show bgp l2vpn evpn neighbor"
queue_sonic_cmd "BGP/EVPN" "show bgp l2vpn evpn"
queue_sonic_cmd "BGP/EVPN" "show bgp l2vpn evpn route"
queue_sonic_cmd "BGP/EVPN" "show route-map"
queue_sonic_cmd "EVPN" "show evpn vni"
queue_sonic_cmd "EVPN" "show evpn mac"
queue_sonic_cmd "EVPN" "show evpn es"
queue_sonic_cmd "EVPN" "show evpn mac vni all"
queue_sonic_cmd "EVPN" "show evpn vni detail"
queue_sonic_cmd "EVPN" "show evpn arp-cache"

# ---------------------------
# Route Tables
# ---------------------------
queue_sonic_cmd "Routes" "show ip route"
queue_sonic_cmd "Routes" "show ip vrf"
queue_sonic_cmd "Routes" "show ip route vrf all"

# ---------------------------
# Platform Information
# ---------------------------
queue_sonic_cmd "Platform" "show platform environment"
queue_sonic_cmd "Platform" "show platform fanstatus"
queue_sonic_cmd "Platform" "show platform firmware"
queue_sonic_cmd "Platform" "show platform i2c errors"
queue_sonic_cmd "Platform" "show platform psusummary"
queue_sonic_cmd "Platform" "show platform ssdhealth"
queue_sonic_cmd "Platform" "show platform temperature"
queue_sonic_cmd "Platform" "show interface transceiver summary"
queue_sonic_cmd "Platform" "show interface transceiver laser status"
queue_sonic_cmd "Platform" "show interface transceiver wattage"

# ---------------------------
# System Status
# ---------------------------
queue_sonic_cmd "System" "show system status brief"
queue_sonic_cmd "System" "show system status"
queue_sonic_cmd "System" "show logging"

# Wait for all sonic-cli commands to complete
wait

# --- Per-VRF route and ARP tables (must run after VRF list is known) ---
vrfs=$(sonic-cli -c "show ip vrf | no-more" 2>/dev/null | awk 'NR>2{print $1}')
for vrf in $vrfs; do
    queue_sonic_cmd "VRF:$vrf" "show ip route vrf $vrf"
    queue_sonic_cmd "VRF:$vrf" "show ip arp vrf $vrf"
    queue_sonic_cmd "VRF:$vrf" "show bgp ipv4 unicast vrf $vrf summary"
done
wait

# Concatenate all parts in order
cat "$PARTS_DIR"/* >> "$OUTPUT_FILE"

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

# Cleanup
rm -rf "$PARTS_DIR"

echo "Diagnostics collected to $OUTPUT_FILE"
