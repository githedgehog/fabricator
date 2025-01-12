#!/bin/bash
# Copyright 2024 Hedgehog
# SPDX-License-Identifier: Apache-2.0

# show-tech.sh: Collect diagnostics from a Flatcar Linux server.

# Set the output file
OUTPUT_FILE="/tmp/show-tech.log"

# Clear the log file
: > "$OUTPUT_FILE"

echo "=== System Information ===" >> "$OUTPUT_FILE"
uname -a >> "$OUTPUT_FILE"
cat /etc/os-release >> "$OUTPUT_FILE"

echo -e "\n=== Network Configuration ===" >> "$OUTPUT_FILE"
ip addr show >> "$OUTPUT_FILE"
ip route show >> "$OUTPUT_FILE"
cat /proc/net/bonding/* >> "$OUTPUT_FILE" 2>/dev/null

echo -e "\n=== VLAN Configuration ===" >> "$OUTPUT_FILE"
ip -d link show type vlan >> "$OUTPUT_FILE"

echo "networkctl LLDP Data:" >> $OUTPUT_FILE
networkctl lldp >> $OUTPUT_FILE

echo "Diagnostics collected to $OUTPUT_FILE"
