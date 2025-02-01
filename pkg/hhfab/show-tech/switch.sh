#!/bin/bash
# Copyright 2024 Hedgehog
# SPDX-License-Identifier: Apache-2.0

# show-tech.sh: Collect diagnostics from a SONiC switch.

# Set the output file
OUTPUT_FILE="/tmp/show-tech.log"

# Clear the log file
: > "$OUTPUT_FILE"

run_cmd() {
    echo -e "\n=== Executing: $1 ===" | tee -a "$OUTPUT_FILE"
    eval "$1" >> "$OUTPUT_FILE" 2>/dev/null
}

run_sonic_cli_cmd() {
    echo -e "\n=== Executing in SONiC CLI: $1 ===" | tee -a "$OUTPUT_FILE"
    sonic-cli -c "$1" >> "$OUTPUT_FILE" 2>/dev/null
}

run_cmd "show version"
run_cmd "show platform summary"
run_cmd "uptime"

run_cmd "show interface status"
run_cmd "intfutil -c description"
run_cmd "show interface counters"
run_cmd "show lldp table"

run_cmd "show runningconfiguration all"
run_cmd "show vlan config"
run_cmd "show vlan brief"
run_cmd "show vxlan interface"
run_cmd "show vxlan vlanvnimap"
run_cmd "show vxlan vrfvnimap"
run_cmd "show vxlan tunnel"
run_cmd "show vxlan fdb"
run_cmd "show vxlan counters"

run_cmd "show vxlan remotevtep"
run_cmd "show vxlan remotevni"

echo -e "\n=== Fetching Remote VTEPs ===" | tee -a "$OUTPUT_FILE"
REMOTE_VTEPS=$(show vxlan remotevtep | awk 'NR>3 {print $2}' | grep -E '([0-9]{1,3}\.){3}[0-9]{1,3}')

for VTEP in $REMOTE_VTEPS; do
    run_cmd "show vxlan remotemac $VTEP"
done

run_sonic_cli_cmd "show mac address-table"

run_cmd "show mclag brief"
run_cmd "show mclag peer"
run_cmd "show mclag interfaces"

run_cmd "show lacp neighbor"
run_cmd "show port-channel summary"

echo -e "\n=== Fetching VRFs ===" | tee -a "$OUTPUT_FILE"
VRFS=$(show vrf | awk 'NR>2 {print $1}' | grep -v default)

for VRF in $VRFS; do
    run_cmd "show arp vrf $VRF"
done

run_cmd "vtysh -c 'show ip bgp summary'"
run_cmd "vtysh -c 'show bgp l2vpn evpn summary'"
run_cmd "vtysh -c 'show bgp l2vpn evpn route type 2'"
run_cmd "vtysh -c 'show bgp l2vpn evpn route type 3'"

run_cmd "vtysh -c 'show evpn vni'"
run_cmd "vtysh -c 'show evpn mac'"
run_cmd "vtysh -c 'show evpn next-hops'"
run_cmd "vtysh -c 'show evpn arp-cache'"

echo -e "\n=== Logs ===" >> "$OUTPUT_FILE"
show logging >> "$OUTPUT_FILE" 2>/dev/null

echo -e "\n=== Journal ===" >> "$OUTPUT_FILE"
journalctl >> "$OUTPUT_FILE" 2>/dev/null

echo -e "\n=== Hedgehog agent status ===" >> "$OUTPUT_FILE"
systemctl status hedgehog-agent >> "$OUTPUT_FILE" 2>/dev/null

echo -e "\n=== Hedgehog agent logs ===" >> "$OUTPUT_FILE"
cat /var/log/agent.log >> "$OUTPUT_FILE" 2>/dev/null

echo -e "\n=== Executing: docker ps ===" >> "$OUTPUT_FILE"
docker ps >> "$OUTPUT_FILE" 2>/dev/null

echo -e "\n=== Collecting Docker Logs ===" >> "$OUTPUT_FILE"

CONTAINERS=$(docker ps --format "{{.Names}}")

for CONTAINER in $CONTAINERS; do
    echo -e "\n=== Capturing logs for container: $CONTAINER ===" >> "$OUTPUT_FILE"
    docker logs --tail 100 "$CONTAINER" >> "$OUTPUT_FILE" 2>/dev/null
done
echo "Diagnostics collected to $OUTPUT_FILE"

