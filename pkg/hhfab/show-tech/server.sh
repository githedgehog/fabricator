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

echo -e "\n=== General Networkctl Status ===" >> "$OUTPUT_FILE"
networkctl status >> "$OUTPUT_FILE"

echo -e "\n=== VLAN Configuration ===" >> "$OUTPUT_FILE"
ip -d link show type vlan >> "$OUTPUT_FILE"

echo -e "\n=== IP Configuration ===" >> "$OUTPUT_FILE"
ip addr show >> "$OUTPUT_FILE"
ip route show >> "$OUTPUT_FILE"

echo -e "\n=== Detailed Link Information ===" >> "$OUTPUT_FILE"
networkctl list >> "$OUTPUT_FILE"

echo -e "\n=== Device Details ===" >> "$OUTPUT_FILE"
ip -d link show >> "$OUTPUT_FILE"

echo -e "\n=== Bonding Configuration ===" >> "$OUTPUT_FILE"
cat /proc/net/bonding/* >> "$OUTPUT_FILE" 2>/dev/null || echo "No bonding configuration found." >> "$OUTPUT_FILE"

echo -e "\n=== MTU Configuration ===" >> "$OUTPUT_FILE"
ip link show | grep mtu >> "$OUTPUT_FILE"

echo "networkctl LLDP Data:" >> $OUTPUT_FILE
networkctl lldp >> $OUTPUT_FILE

echo -e "\n=== DHCP Leases ===" >> "$OUTPUT_FILE"
cat /run/systemd/netif/leases/* >> "$OUTPUT_FILE" 2>/dev/null || echo "No DHCP leases found." >> "$OUTPUT_FILE"

echo -e "\n=== NIC Information ===" >> "$OUTPUT_FILE"
for nic in $(ls /sys/class/net | grep -E '^enp|^eth'); do
  echo -e "\n--- $nic ---" >> "$OUTPUT_FILE"
  ethtool -k "$nic" >> "$OUTPUT_FILE" 2>/dev/null
done

echo -e "\n=== Network Configuration Files ===" >> "$OUTPUT_FILE"
find /etc/systemd/network -type f -exec echo -e "\nFile: {}" \; -exec cat {} \; >> "$OUTPUT_FILE"

echo -e "\n=== systemd-timesyncd Service Status ===" >> "$OUTPUT_FILE"
systemctl status systemd-timesyncd >> "$OUTPUT_FILE"

echo -e "\n=== Timesync Status ===" >> "$OUTPUT_FILE"
timedatectl show-timesync >> "$OUTPUT_FILE"

echo -e "\n=== Current Time Settings ===" >> "$OUTPUT_FILE"
timedatectl >> "$OUTPUT_FILE"

echo -e "\n=== systemd-networkd logs ===" >> "$OUTPUT_FILE"
journalctl -u systemd-networkd >> "$OUTPUT_FILE" 2>/dev/null

echo -e "\n=== kernel logs ===" >> "$OUTPUT_FILE"
journalctl -k >> "$OUTPUT_FILE" 2>/dev/null

echo -e "\n=== Kernel Network Logs ===" >> "$OUTPUT_FILE"
dmesg | grep -i "network\|bond\|vlan" >> "$OUTPUT_FILE"

echo -e "\n=== SSH Logs ===" >> "$OUTPUT_FILE"
journalctl -u sshd >> "$OUTPUT_FILE" 2>/dev/null

echo "Diagnostics collected to $OUTPUT_FILE"
