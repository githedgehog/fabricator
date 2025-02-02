#!/bin/bash
# Copyright 2024 Hedgehog
# SPDX-License-Identifier: Apache-2.0

# show-tech.sh: Collect diagnostics from a Flatcar Linux server.
set +e

OUTPUT_FILE="/tmp/show-tech.log"

: > "$OUTPUT_FILE"

# ---------------------------
# Basic System Information
# ---------------------------
{
  echo "=== System Information ==="
  uname -a
  cat /etc/os-release
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# Network Status & Configuration
# ---------------------------
{
  echo -e "\n=== General networkctl Status ==="
  networkctl status

  echo -e "\n=== VLAN Configuration ==="
  ip -d link show type vlan

  echo -e "\n=== IP Configuration ==="
  ip addr show
  ip route show
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# Connectivity Tests
# ---------------------------
{
  DEFAULT_GW=$(ip route | awk '/^default/ {print $3}')
  echo -e "\n=== Ping Default Gateway ($DEFAULT_GW) ==="
  ping -c 1 "$DEFAULT_GW" 2>&1

  echo -e "\n=== Ping Other Servers ==="
  OWN_IP=$(ip route get 8.8.8.8 | awk '{for(i=1;i<=NF;i++) if($i=="src") {print $(i+1); exit}}')
  for i in {1..9}; do
    TARGET_IP="10.0.$i.2"
    if [ "$TARGET_IP" == "$OWN_IP" ]; then
      continue
    fi
    ping -c 1 "$TARGET_IP" 2>&1
  done
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# Detailed Network Diagnostics
# ---------------------------
{
  echo -e "\n=== Detailed Link Information (networkctl list) ==="
  networkctl list

  echo -e "\n=== Device Details (ip -d link show) ==="
  ip -d link show

  echo -e "\n=== Bonding Configuration ==="
  cat /proc/net/bonding/* 2>/dev/null || echo "No bonding configuration found."

  echo -e "\n=== MTU Configuration ==="
  ip link show | grep mtu

  echo -e "\n=== LLDP Data (networkctl lldp) ==="
  networkctl lldp

  echo -e "\n=== DHCP Leases ==="
  cat /run/systemd/netif/leases/* 2>/dev/null || echo "No DHCP leases found."
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# NIC & Interface Diagnostics
# ---------------------------
{
  echo -e "\n=== NIC Information (ethtool) ==="
  for nic in $(ls /sys/class/net | grep -E '^(enp|eth)'); do
    echo -e "\n--- $nic ---"
    ethtool -k "$nic" 2>/dev/null || echo "Could not retrieve ethtool data for $nic"
  done

  echo -e "\n=== Network Configuration Files ==="
  find /etc/systemd/network -type f -exec echo -e "\nFile: {}" \; -exec cat {} \;
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# Time Synchronization & Services
# ---------------------------
{
  echo -e "\n=== systemd-timesyncd Service Status ==="
  systemctl status systemd-timesyncd

  echo -e "\n=== Timesync Status ==="
  timedatectl show-timesync

  echo -e "\n=== Current Time Settings ==="
  timedatectl
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# Logs & Kernel Information
# ---------------------------
{
  echo -e "\n=== systemd-networkd logs ==="
  journalctl -u systemd-networkd 2>/dev/null

  echo -e "\n=== Kernel logs ==="
  journalctl -k 2>/dev/null

  echo -e "\n=== Kernel Network Logs (dmesg) ==="
  dmesg | grep -i "network\|bond\|vlan"
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# Additional Diagnostics
# ---------------------------
{
  echo -e "\n=== ARP Table ==="
  ip neigh show

  echo -e "\n=== Listening Ports (ss -tulnp) ==="
  ss -tulnp 2>&1

  echo -e "\n=== DNS Resolver Status ==="
  cat /etc/resolv.conf
  if command -v resolvectl &>/dev/null; then
    resolvectl status 2>&1
  fi

  echo -e "\n=== Kernel Modules Related to Networking ==="
  lsmod | grep -E 'bond|8021q|bridge'

  echo -e "\n=== Interface Statistics (ip -s link) ==="
  ip -s link

} >> "$OUTPUT_FILE" 2>&1

echo "Diagnostics collected to $OUTPUT_FILE"
