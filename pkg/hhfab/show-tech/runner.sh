#!/bin/bash
# Copyright 2025 Hedgehog
# SPDX-License-Identifier: Apache-2.0

# show-tech.sh: Collect diagnostics from the Runner Pod environment
set +e

{
    echo "=== Runner Environment Diagnostics ==="
    echo "Timestamp: $(date -Iseconds)"
    echo "Hostname: $(hostname)"

    # Basic System Info
    echo -e "\n=== System Resources ==="
    echo "Memory:"
    free -h
    echo -e "\nCPU:"
    nproc
    lscpu | grep -E "^CPU\(s\)|Model name|Thread|Core"
    echo -e "\nLoad Average:"
    uptime

    # cgroup Memory Information
    echo -e "\n=== cgroup Memory Statistics ==="
    CGROUPPATH=$(cat /proc/self/cgroup 2>/dev/null | grep '^0:' | cut -d: -f3)
    if [ -n "$CGROUPPATH" ] && [ -f "/sys/fs/cgroup${CGROUPPATH}/memory.stat" ]; then
        echo "cgroup path: $CGROUPPATH"
        echo "Memory usage breakdown:"
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

    # Pressure Stall Information
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

    # Detailed memory info
    echo -e "\n=== Detailed Memory Info ==="
    cat /proc/meminfo

    # OOM events
    echo -e "\n=== Recent OOM Events ==="
    dmesg -T 2>/dev/null | grep -i "oom\|out of memory\|killed process" | tail -50 || \
        echo "No OOM events detected (or dmesg not accessible)"

    # Process listing
    echo -e "\n=== Top Memory Consumers ==="
    ps aux --sort=-%mem | head -30

    echo -e "\n=== Top CPU Consumers ==="
    ps aux --sort=-%cpu | head -30

    echo -e "\n=== QEMU/KVM Processes ==="
    ps aux | grep -E "[q]emu" || echo "No QEMU processes running"

    # Disk usage
    echo -e "\n=== Disk Usage ==="
    df -h

    echo -e "\n=== I/O Statistics ==="
    iostat -x 1 3 2>/dev/null || echo "iostat not available"

    # Network stats
    echo -e "\n=== Network Interface Stats ==="
    ip -s link show 2>/dev/null || echo "ip command not available"

} 2>&1
