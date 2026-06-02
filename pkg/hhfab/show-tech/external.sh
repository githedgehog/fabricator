#!/bin/bash
# Copyright 2025 Hedgehog
# SPDX-License-Identifier: Apache-2.0

# show-tech.sh: Collect diagnostics from a VLAB virtual external node.
# It is a Flatcar server VM that runs FRR as a docker container ("frr",
# quay.io/frrouting/frr) via frr.service. There is no host vtysh, so run it
# inside the container. This captures the BGP speaker state that the plain
# server show-tech omits. Best-effort: every command is allowed to fail.
set +e

# FRR has no host vtysh wrapper; exec into the frr container.
vtysh() { docker exec frr vtysh "$@"; }

OUTPUT_FILE="/tmp/show-tech.log"

: > "$OUTPUT_FILE"

# ---------------------------
# Basic System / Network
# ---------------------------
{
  echo "=== System Information ==="
  uname -a
  cat /etc/os-release
  echo -e "\n=== Interfaces (ip -br addr) ==="
  ip -br addr
  echo -e "\n=== Routes (all tables) ==="
  ip route show table all
  echo -e "\n=== Neighbors (ARP/ND) ==="
  ip neigh
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# FRR container + service
# ---------------------------
{
  echo -e "\n=== docker ps -a ==="
  docker ps -a
  echo -e "\n=== frr.service / frr-reload.service status ==="
  systemctl status --no-pager frr.service frr-reload.service
  echo -e "\n=== journalctl -u frr.service (last 300) ==="
  journalctl -u frr.service --no-pager -n 300
  echo -e "\n=== docker logs frr (last 300) ==="
  docker logs --tail 300 frr
  echo -e "\n=== /var/run/frr/frr.log (last 300) ==="
  tail -n 300 /var/run/frr/frr.log
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# BGP state (via the vtysh wrapper -> docker exec frr vtysh)
# ---------------------------
{
  echo -e "\n=== show bgp summary ==="
  vtysh -c "show bgp summary"
  echo -e "\n=== show bgp vrf all summary ==="
  vtysh -c "show bgp vrf all summary"
  echo -e "\n=== show bgp vrf all neighbors ==="
  vtysh -c "show bgp vrf all neighbors"
  echo -e "\n=== running-config ==="
  vtysh -c "show running-config"
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# Listeners: is anything answering BGP on :179?
# ---------------------------
{
  echo -e "\n=== ss -tlnp ==="
  ss -tlnp
  echo -e "\n=== :179 listeners ==="
  ss -tlnp | grep -E ':179( |$)' || echo "(nothing listening on :179)"
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# FRR config files
# ---------------------------
{
  for f in /etc/frr/frr.conf /etc/frr/daemons /etc/frr/vtysh.conf; do
    echo -e "\n=== $f ==="
    cat "$f"
  done
} >> "$OUTPUT_FILE" 2>&1
