#!/bin/bash
# Copyright 2025 Hedgehog
# SPDX-License-Identifier: Apache-2.0

# show-tech.sh: Collect diagnostics from the runner host environment.
set +e

{
    echo "=== Runner Environment Diagnostics ==="
    echo "Timestamp: $(date -Iseconds)"
    echo "Hostname: $(hostname)"

    echo -e "\n=== System Resources ==="
    echo "Memory:"
    free -h
    echo -e "\nCPU:"
    nproc
    lscpu | grep -E "^CPU\(s\)|Model name|Thread|Core"
    echo -e "\nLoad Average:"
    uptime

    echo -e "\n=== cgroup Memory Statistics ==="
    CGROUPPATH=$(cat /proc/self/cgroup 2>/dev/null | grep '^0:' | cut -d: -f3)
    if [ -n "$CGROUPPATH" ] && [ -f "/sys/fs/cgroup${CGROUPPATH}/memory.stat" ]; then
        echo "cgroup path: $CGROUPPATH"
        cat "/sys/fs/cgroup${CGROUPPATH}/memory.stat" 2>/dev/null | head -15
    elif [ -f /sys/fs/cgroup/memory/memory.stat ]; then
        echo "cgroup v1 memory stats:"
        cat /sys/fs/cgroup/memory/memory.stat 2>/dev/null | head -15
    elif [ -f /sys/fs/cgroup/memory.stat ]; then
        echo "cgroup v2 memory stats:"
        cat /sys/fs/cgroup/memory.stat 2>/dev/null | head -15
    else
        echo "No cgroup memory stats available"
    fi

    echo -e "\n=== Pressure Stall Information ==="
    if [ -f /proc/pressure/memory ]; then
        echo "Memory Pressure:"
        cat /proc/pressure/memory
        echo -e "\nCPU Pressure:"
        cat /proc/pressure/cpu
        echo -e "\nI/O Pressure:"
        cat /proc/pressure/io
    else
        echo "PSI not available"
    fi

    echo -e "\n=== Detailed Memory Info ==="
    cat /proc/meminfo

    echo -e "\n=== Recent OOM Events ==="
    dmesg -T 2>/dev/null | grep -i "oom\|out of memory\|killed process" | tail -50 || \
        echo "No OOM events detected (or dmesg not accessible)"

    echo -e "\n=== Top Memory Consumers ==="
    ps aux --sort=-%mem | head -30

    echo -e "\n=== Top CPU Consumers ==="
    ps aux --sort=-%cpu | head -30

    echo -e "\n=== QEMU/KVM Processes ==="
    ps aux | grep -E "[q]emu" || echo "No QEMU processes running"

    echo -e "\n=== Disk Usage ==="
    df -h

    echo -e "\n=== I/O Statistics ==="
    iostat -x 1 3 2>/dev/null || echo "iostat not available"

    echo -e "\n=== Network Interface Stats ==="
    if command -v ip >/dev/null 2>&1; then
        ip -s link show 2>/dev/null || echo "ip link failed"
    else
        echo "ip command not available"
        echo -e "\n=== /sys/class/net (fallback) ==="
        ls /sys/class/net 2>/dev/null || echo "cannot list /sys/class/net"
    fi

    echo -e "\n=== VLAB Bridge/Tap Diagnostics ==="
    if command -v ip >/dev/null 2>&1; then
        if ip -d link show hhbr >/dev/null 2>&1; then
            ip -d link show hhbr
        else
            echo "hhbr bridge not found"
        fi
    else
        echo "ip command not available"
    fi

    if command -v bridge >/dev/null 2>&1; then
        bridge link 2>/dev/null || echo "bridge link failed"
        bridge fdb show br hhbr 2>/dev/null || echo "bridge fdb show failed"
        bridge vlan show 2>/dev/null || echo "bridge vlan show failed"
    else
        echo "bridge command not available"
    fi

    if command -v ip >/dev/null 2>&1; then
        if ip -d link show hhtap* >/dev/null 2>&1; then
            ip -d link show hhtap*
        else
            echo "no hhtap interfaces present"
        fi
    else
        echo "ip command not available"
    fi

    echo -e "\n=== VLAB sysfs Fallback (no ip/bridge) ==="
    if [ -d /sys/class/net ]; then
        ls /sys/class/net 2>/dev/null || echo "cannot list /sys/class/net"
    fi
    if [ -d /sys/class/net/hhbr ]; then
        echo "hhbr exists"
        ls /sys/class/net/hhbr/brif 2>/dev/null || echo "hhbr has no ports"
    else
        echo "hhbr missing"
    fi
    if [ -d /proc/net/bridge ]; then
        echo -e "\n=== /proc/net/bridge (fallback) ==="
        cat /proc/net/bridge/bridge 2>/dev/null || echo "cannot read /proc/net/bridge/bridge"
        cat /proc/net/bridge/brif 2>/dev/null || echo "cannot read /proc/net/bridge/brif"
        cat /proc/net/bridge/brforward 2>/dev/null || echo "cannot read /proc/net/bridge/brforward"
    fi
    for tap in /sys/class/net/hhtap*; do
        [ -e "$tap" ] || continue
        iface=$(basename "$tap")
        echo -e "\n--- $iface ---"
        cat "$tap/address" 2>/dev/null
        cat "$tap/operstate" 2>/dev/null
        cat "$tap/statistics/rx_packets" 2>/dev/null
        cat "$tap/statistics/tx_packets" 2>/dev/null
    done
    if [ -d /sys/class/net/hhbr/brif ]; then
        for port in /sys/class/net/hhbr/brif/*; do
            [ -e "$port" ] || continue
            state_file="$port/brport/state"
            if [ -f "$state_file" ]; then
                echo "$(basename "$port") state=$(cat "$state_file")"
            fi
        done
    fi

    echo -e "\n=== Host Networking Summary ==="
    if command -v ip >/dev/null 2>&1; then
        ip addr 2>/dev/null || echo "ip addr failed"
        ip route 2>/dev/null || echo "ip route failed"
        ip neigh 2>/dev/null || echo "ip neigh failed"
    else
        echo "ip command not available"
        echo -e "\n=== /proc/net/dev (fallback) ==="
        cat /proc/net/dev 2>/dev/null || echo "cannot read /proc/net/dev"
        echo -e "\n=== /proc/net/route (fallback) ==="
        cat /proc/net/route 2>/dev/null || echo "cannot read /proc/net/route"
        echo -e "\n=== /proc/net/arp (fallback) ==="
        cat /proc/net/arp 2>/dev/null || echo "cannot read /proc/net/arp"
    fi

    echo -e "\n=== QEMU netns Check ==="
    if command -v pgrep >/dev/null 2>&1; then
        QPID=$(pgrep -f 'qemu-system-x86_64' | head -n 1)
    else
        QPID=$(for p in /proc/[0-9]*; do
            cmd=$(tr '\0' ' ' < "$p/cmdline" 2>/dev/null)
            case "$cmd" in
                *qemu-system-x86_64*) echo "${p##*/}"; break ;;
            esac
        done)
    fi
    echo "qpid=${QPID:-none}"
    readlink /proc/1/ns/net 2>/dev/null || echo "cannot read /proc/1/ns/net"
    if [ -n "$QPID" ]; then
        readlink "/proc/$QPID/ns/net" 2>/dev/null || \
            (command -v sudo >/dev/null 2>&1 && sudo -n readlink "/proc/$QPID/ns/net" 2>/dev/null) || \
            echo "cannot read /proc/$QPID/ns/net"
    fi

    echo -e "\n=== Runner Devices ==="
    ls -l /dev/kvm /dev/net/tun 2>/dev/null || echo "/dev/kvm or /dev/net/tun missing"
    stat /dev/kvm /dev/net/tun 2>/dev/null || true

    echo -e "\n=== UDP Sockets (QEMU netdev socket links) ==="
    cat /proc/net/udp 2>/dev/null || echo "cannot read /proc/net/udp"

    echo -e "\n=== Bridge Netfilter Sysctls ==="
    for key in net.bridge.bridge-nf-call-iptables net.bridge.bridge-nf-call-ip6tables; do
        if sysctl -n "$key" >/dev/null 2>&1; then
            echo "$key=$(sysctl -n "$key")"
        elif [ -f "/proc/sys/${key//./\/}" ]; then
            echo "$key=$(cat "/proc/sys/${key//./\/}")"
        else
            echo "$key=unavailable"
        fi
    done

    echo -e "\n=== nftables Ruleset (if permitted) ==="
    if command -v nft >/dev/null 2>&1; then
        if [ "$(id -u)" -eq 0 ]; then
            nft list ruleset 2>/dev/null || echo "nft list ruleset failed"
        elif command -v sudo >/dev/null 2>&1; then
            sudo -n nft list ruleset 2>/dev/null || echo "nft list ruleset requires sudo"
        else
            echo "nft list ruleset requires sudo"
        fi
    else
        echo "nft command not available"
    fi

} 2>&1
