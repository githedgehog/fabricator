#!/bin/bash
# Copyright 2024 Hedgehog
# SPDX-License-Identifier: Apache-2.0

# show-tech.sh: Collect diagnostics from a SONiC or Cumulus switch.
# Commands run in parallel (up to MAX_PARALLEL) to reduce collection time.
set +e

OUTPUT_FILE="/tmp/show-tech.log"
PARTS_DIR="/tmp/show-tech-parts"
MAX_PARALLEL=6

: > "$OUTPUT_FILE"
rm -rf "$PARTS_DIR"

# Counter for ordering output parts
PART_IDX=0

# ---------------------------
# Cumulus Linux
# ---------------------------
# Cumulus has no sonic-cli or bgp container. NVUE state comes from nv, FRR state
# from vtysh, which needs root, so it runs through passwordless sudo (enabled by
# the agent on Cumulus). Sections SONiC collects that have no Cumulus
# counterpart (QoS/PFC counters, err-disable timeline, Broadcom SDK) are left
# out.
#
# hhfab gives a switch 10 minutes in total, including the log download, so the
# nv and vtysh commands share a 6-minute budget. The first nv or vtysh command
# that times out skips the rest of its kind, so a hung nvued doesn't use up the
# budget the FRR state needs, or the other way round. The kernel, log and agent
# sections run after it and must all finish by 8 minutes, leaving 2 for the
# download. Commands past either deadline are skipped.
if ! command -v sonic-cli >/dev/null 2>&1 && command -v nv >/dev/null 2>&1; then
    CL_DEADLINE=$((SECONDS + 360))
    CL_END=$((SECONDS + 480))
    CL_HUNG=""
    export PATH="$PATH:/sbin:/usr/sbin"

    # cl_cmd LABEL CMD: run CMD with what is left of the budget, at most 120s.
    cl_cmd() {
        echo -e "\n=== [$1] Executing: $2 ==="
        local kind=""
        case "$2" in
            "nv "*) kind=nv ;;
            "sudo -n vtysh "*) kind=vtysh ;;
        esac
        if [ -n "$kind" ] && [[ " $CL_HUNG " == *" $kind "* ]]; then
            echo "Skipped: an earlier $kind command timed out"
            return
        fi
        local t=$((CL_DEADLINE - SECONDS))
        [ "$t" -gt 120 ] && t=120
        if [ "$t" -le 0 ]; then
            echo "Skipped: show-tech time budget exhausted"
            return
        fi
        timeout "$t" bash -c "$2" 2>&1
        if [ $? -eq 124 ] && [ -n "$kind" ]; then
            echo "Timed out after ${t}s, skipping the remaining $kind commands"
            CL_HUNG="$CL_HUNG $kind"
        fi
    }

    # cl_tail LABEL CMD: run CMD with what is left until CL_END, at most 30s.
    cl_tail() {
        echo -e "\n=== [$1] Executing: $2 ==="
        local t=$((CL_END - SECONDS))
        [ "$t" -gt 30 ] && t=30
        if [ "$t" -le 0 ]; then
            echo "Skipped: show-tech time budget exhausted"
            return
        fi
        timeout "$t" bash -c "$2" 2>&1
    }

    vtysh_cmd() {
        cl_cmd "$1" "sudo -n vtysh -c '$2'"
    }

    {
        cl_cmd "System" "nv show system"
        cl_cmd "System" "grep -E '^(PRETTY_NAME|VERSION)=' /etc/os-release"
        cl_cmd "System" "uptime"
        cl_cmd "System" "nv show system health"

        # Applied config, its revision history and what the agent last pushed.
        # The agent keeps the last diff of each apply as files; empty means the
        # apply changed nothing.
        cl_cmd "Config" "nv config show -r applied"
        cl_cmd "Config" "nv config history"
        cl_cmd "Config" "nv config revision"
        cl_cmd "Config" "nv config diff"
        for f in /etc/hedgehog/last-diff.yaml /etc/hedgehog/last-diff-applied.yaml; do
            cl_cmd "Config" "echo '--- $f'; cat $f"
        done
        vtysh_cmd "Config" "show running-config"

        cl_cmd "Interface" "nv show interface"
        cl_cmd "Interface" "nv show interface --view=lldp"
        cl_cmd "Interface" "nv show interface --view=counters"

        cl_cmd "VLAN/VXLAN" "nv show bridge domain br_default vlan"
        cl_cmd "VLAN/VXLAN" "nv show nve vxlan"
        cl_cmd "VLAN/VXLAN" "bridge -d vlan show"
        cl_cmd "VLAN/VXLAN" "ip -d link show type vxlan"

        cl_cmd "L2" "nv show bridge domain br_default mac-table"
        cl_cmd "L2" "for b in /proc/net/bonding/*; do [ -e \"\$b\" ] || continue; echo \"--- \$b\"; cat \"\$b\"; done"

        vtysh_cmd "BGP/EVPN" "show bgp vrf all summary"
        vtysh_cmd "BGP/EVPN" "show bgp l2vpn evpn summary"
        vtysh_cmd "BGP/EVPN" "show bgp vrf all neighbors"
        vtysh_cmd "BGP/EVPN" "show bgp l2vpn evpn"
        vtysh_cmd "BGP/EVPN" "show bgp l2vpn evpn route"
        vtysh_cmd "BGP/EVPN" "show route-map"
        vtysh_cmd "BFD" "show bfd peers"
        vtysh_cmd "BFD" "show bfd peers counters"
        vtysh_cmd "EVPN" "show evpn"
        vtysh_cmd "EVPN" "show evpn vni detail"
        vtysh_cmd "EVPN" "show evpn mac vni all detail"
        vtysh_cmd "EVPN" "show evpn es detail"
        vtysh_cmd "EVPN" "show evpn arp-cache vni all"

        vtysh_cmd "Routes" "show vrf vni"
        vtysh_cmd "Routes" "show ip route vrf all"
        vtysh_cmd "Routes" "show bgp vrf all ipv4 unicast"

        cl_cmd "Platform" "nv show platform"
        cl_cmd "Platform" "nv show platform environment"
        cl_cmd "Platform" "nv show platform transceiver"
        cl_cmd "Platform" "nv show interface --view=dom"

        # Kernel MAC/neighbor state and datapath counters, as on SONiC.
        cl_tail "Kernel" "bridge fdb show"
        cl_tail "Kernel" "ip neigh show"
        cl_tail "Kernel" "ip -s -s link show"
        cl_tail "Kernel" "tc -s qdisc show"
        cl_tail "Kernel" "cat /proc/net/softnet_stat"
        cl_tail "Kernel" "sudo -n ss -0 -a -e -m -p"
        cl_tail "Kernel" "nstat -as"
        cl_tail "Kernel" "for p in /sys/class/net/swp*; do [ -e \"\$p\" ] || continue; echo \"--- \${p##*/}\"; ethtool -S \"\${p##*/}\"; done"
        cl_tail "Kernel" "top -b -n 1 -w 200 | head -40"

        cl_tail "Agent" "systemctl status hedgehog-agent"
        cl_tail "Agent" "cat /var/log/agent.log"

        cl_tail "Logs" "sudo -n journalctl --no-pager -n 300 -u frr"
        cl_tail "Logs" "sudo -n journalctl --no-pager -n 300 -u nvued"
        cl_tail "Logs" "sudo -n journalctl --no-pager -n 300 -u switchd"
        cl_tail "Logs" "sudo -n tail -n 500 /var/log/syslog"
    } >> "$OUTPUT_FILE" 2>&1

    echo "Diagnostics collected to $OUTPUT_FILE"
    exit 0
fi

mkdir -p "$PARTS_DIR"

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
queue_sonic_cmd "BGP/EVPN" "show bgp ipv4 unicast summary"
queue_sonic_cmd "BGP/EVPN" "show bgp l2vpn evpn summary"
queue_sonic_cmd "BGP/EVPN" "show bgp l2vpn evpn neighbor"
queue_sonic_cmd "BGP/EVPN" "show bgp l2vpn evpn"
queue_sonic_cmd "BGP/EVPN" "show bgp l2vpn evpn route"
queue_sonic_cmd "BGP/EVPN" "show route-map"
# Gateway FRR configures a bfd block, so without this the gateway<->leaf
# liveness state can be neither confirmed nor excluded.
queue_sonic_cmd "BFD" "show bfd peers"
queue_sonic_cmd "BFD" "show bfd peers counters"
queue_sonic_cmd "EVPN" "show evpn vni"
queue_sonic_cmd "EVPN" "show evpn mac"
queue_sonic_cmd "EVPN" "show evpn es"
queue_sonic_cmd "EVPN" "show evpn mac vni all"
queue_sonic_cmd "EVPN" "show evpn vni detail"
queue_sonic_cmd "EVPN" "show evpn arp-cache"

# ---------------------------
# Kernel and zebra MAC/neighbor state
# ---------------------------
# A MAC can sit in the ASIC L2 table and the CLI MAC table while zebra's EVPN
# MAC table never learns it, so no type-2 route is originated. The kernel
# bridge FDB (what fdbsyncd programs and zebra reads) and zebra's own detail
# view tell those cases apart. stderr is kept: an unsupported command must not
# look like an empty table.
{
    echo -e "\n=== Kernel Bridge FDB ==="
    PATH="$PATH:/sbin:/usr/sbin" bridge fdb show 2>&1
    echo -e "\n=== Kernel Neighbors ==="
    ip neigh show 2>&1
    for cmd in "show evpn mac vni all detail" "show evpn es detail" "show evpn arp-cache vni all"; do
        echo -e "\n=== [zebra] vtysh -c '$cmd' ==="
        docker exec bgp vtysh -c "$cmd" 2>&1
    done
} >> "$OUTPUT_FILE" 2>&1

# ---------------------------
# Kernel datapath counters
# ---------------------------
# "show interface counters" only covers the ASIC, or its model on a virtual
# switch. There every frame also crosses the kernel: the VM NIC (ethN), a
# packet socket in syncd and the EthernetN tap, so packets dropped on the way
# never show up there. These tell where they die: NIC driver (ethtool -S),
# netdev and qdisc (ip -s link, tc -s qdisc), per-CPU backlog (softnet_stat,
# hex columns: processed, dropped, time squeezed) and socket buffers (packet
# sockets' skmem d<drops>, IP/UDP counters). GRO state is checked as the agent
# turns it off on virtual switch ports.
{
    echo -e "\n=== Kernel Interface Statistics ==="
    ip -s -s link show 2>&1
    echo -e "\n=== Kernel Qdisc Statistics ==="
    PATH="$PATH:/sbin:/usr/sbin" tc -s qdisc show 2>&1
    echo -e "\n=== Kernel Softnet Statistics ==="
    cat /proc/net/softnet_stat 2>&1
    echo -e "\n=== Kernel Packet Sockets ==="
    sudo -n ss -0 -a -e -m -p 2>&1
    echo -e "\n=== Kernel Network Counters ==="
    nstat -as 2>&1
    for path in /sys/class/net/eth[0-9]*; do
        [ -e "$path" ] || continue
        nic="${path##*/}"
        echo -e "\n--- ethtool $nic ---"
        PATH="$PATH:/sbin:/usr/sbin" ethtool -S "$nic" 2>&1
        PATH="$PATH:/sbin:/usr/sbin" ethtool -k "$nic" 2>&1 | grep -E 'receive-offload|segmentation-offload'
    done
    echo -e "\n=== Top Processes ==="
    top -b -n 1 -w 200 2>&1 | head -40
} >> "$OUTPUT_FILE" 2>&1

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
    queue_sonic_cmd "VRF:$vrf" "show bgp ipv4 unicast vrf $vrf"
done
wait

# Concatenate all parts in order
cat "$PARTS_DIR"/* >> "$OUTPUT_FILE"

# ---------------------------
# Broadcom SDK Diagnostics
# ---------------------------
# bcm_cmd LABEL CMD [FALLBACK_CMD ...]
# Tries each candidate in order and keeps the first one the SDK shell
# actually answers. bcmcmd exits 0 whether a command is accepted or
# rejected, so a rejection is detected from its output instead: "Usage ("
# (unknown subcommand) or "Unsupported command." (known but disabled
# subcommand). The command set differs by ASIC/SDK generation - DS5000
# accepts "l3 route/host/ecmp show", everything else needs
# "l3 defip/l3table/ecmp egress show" - so a rejected first candidate is
# expected on some platforms, not an error.
bcm_cmd() {
    local label="$1"; shift
    echo -e "\n=== $label ==="
    local cmd tmp
    tmp=$(mktemp) || { echo "bcmcmd not run: mktemp failed"; return; }
    for cmd in "$@"; do
        bcmcmd "$cmd" > "$tmp" 2>&1
        if ! grep -qE 'Usage \(|Unsupported command\.' "$tmp"; then
            cat "$tmp"
            rm -f "$tmp"
            return
        fi
    done
    rm -f "$tmp"
    echo "bcmcmd rejected every candidate on this platform: $(printf '"%s" ' "$@")"
}

if command -v bcmcmd >/dev/null 2>&1; then
    {
        bcm_cmd "Broadcom Port Status" "ps"
        bcm_cmd "Broadcom PHY Information" "phy info"
        bcm_cmd "Broadcom L2 Table" "l2 show"
        bcm_cmd "Broadcom L3 Interfaces" "l3 intf show"
        bcm_cmd "Broadcom L3 ACLs" "fp show"
        bcm_cmd "Broadcom L3 Route Table" "l3 route show" "l3 defip show"
        bcm_cmd "Broadcom L3 ECMP Table" "l3 ecmp show" "l3 ecmp egress show"
        bcm_cmd "Broadcom L3 Host Table" "l3 host show" "l3 l3table show"
        bcm_cmd "Broadcom VLAN Table" "vlan show"
        bcm_cmd "Broadcom Trunk Table" "trunk show"
    } >> "$OUTPUT_FILE" 2>&1
else
    echo -e "\n=== Broadcom ASIC Diagnostics ===\nbcmcmd not available on this platform" >> "$OUTPUT_FILE"
fi

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
