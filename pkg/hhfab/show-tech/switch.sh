#!/bin/bash
# Copyright 2024 Hedgehog
# SPDX-License-Identifier: Apache-2.0

# show-tech.sh: Collect diagnostics from a SONiC switch.

# Set the output file
OUTPUT_FILE="/tmp/show-tech.log"

# Clear the log file
: > "$OUTPUT_FILE"

echo "=== SONiC Version ===" >> "$OUTPUT_FILE"
show version >> "$OUTPUT_FILE" 2>/dev/null

echo -e "\n=== Interface Status ===" >> "$OUTPUT_FILE"
show interfaces status >> "$OUTPUT_FILE" 2>/dev/null

echo -e "\n=== Port Configuration ===" >> "$OUTPUT_FILE"
show runningconfiguration all >> "$OUTPUT_FILE" 2>/dev/null

echo -e "\n=== VLAN Configuration ===" >> "$OUTPUT_FILE"
show vlan brief >> "$OUTPUT_FILE" 2>/dev/null

echo -e "\n=== Routing Table ===" >> "$OUTPUT_FILE"
show ip route >> "$OUTPUT_FILE" 2>/dev/null

echo -e "\n=== ARP Table ===" >> "$OUTPUT_FILE"
show arp >> "$OUTPUT_FILE" 2>/dev/null

echo -e "\n=== Logs ===" >> "$OUTPUT_FILE"
show logging >> "$OUTPUT_FILE" 2>/dev/null

echo -e "\n=== Hedgehog agent status ===" >> "$OUTPUT_FILE"
systemctl status hedgehog-agent >> "$OUTPUT_FILE" 2>/dev/null

echo -e "\n=== Hedgehog agent logs ===" >> "$OUTPUT_FILE"
cat /var/log/agent.log >> "$OUTPUT_FILE" 2>/dev/null

echo "Diagnostics collected to $OUTPUT_FILE"

