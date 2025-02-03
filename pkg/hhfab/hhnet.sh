#!/usr/bin/env bash
# Copyright 2024 Hedgehog
# SPDX-License-Identifier: Apache-2.0


set -e

LOG_FILE="/var/log/hhnet.log"
sudo touch "$LOG_FILE"
sudo chmod 666 "$LOG_FILE"
exec >> "$LOG_FILE" 2>&1

function cleanup() {
    for i in {0..3}; do
        sudo ip l d "bond$i" 2> /dev/null || true
    done

    for i in {1..9}; do
        sudo ip l s "enp2s$i" down 2> /dev/null || true
        for j in {1000..1020}; do
            sudo ip l d "enp2s$i.$j" 2> /dev/null || true
        done
    done

    sleep 1
}

function wait_for_interface() {
    local iface_name=$1
    local timeout=30
    local elapsed=0
    local start_time=$(date +%s)
    while ! ip l show "$iface_name" | grep -q "UP"; do
        sleep 1
        elapsed=$(( $(date +%s) - start_time ))
        if [ "$elapsed" -ge "$timeout" ]; then
            echo "Timeout waiting for $iface_name to come up" >&2 | tee -a "$LOG_FILE"
            return 1
        fi
    done
    local end_time=$(date +%s)
    echo "$iface_name is up after $((end_time - start_time)) seconds" >> "$LOG_FILE"
}

function setup_bond() {
    local bond_name=$1
    local start_time=$(date +%s)

    sudo ip l a "$bond_name" type bond miimon 100 mode 802.3ad

    for iface in "${@:2}"; do
        # cannot enslave interface if it is up
        sudo ip l s "$iface" down 2> /dev/null || true
        sudo ip l s "$iface" master "$bond_name"
    done

    sudo ip l s "$bond_name" up
    wait_for_interface "$bond_name" || return 1

    local end_time=$(date +%s)
    echo "setup_bond completed in $((end_time - start_time)) seconds" >> "$LOG_FILE"
}

function setup_vlan() {
    local iface_name=$1
    local vlan_id=$2
    local start_time=$(date +%s)

    sudo ip l s "$iface_name" up
    sudo ip l a link "$iface_name" name "$iface_name.$vlan_id" type vlan id "$vlan_id"
    sudo ip l s "$iface_name.$vlan_id" up
    wait_for_interface "$iface_name.$vlan_id" || return 1

    local end_time=$(date +%s)
    echo "setup_vlan completed in $((end_time - start_time)) seconds" >> "$LOG_FILE"
}

function get_ip() {
    local iface_name=$1
    local ip=""
    local max_attempts=300 # 5 minutes
    local attempt=0
    local start_time=$(date +%s)

    while [ -z "$ip" ]; do
        attempt=$((attempt + 1))
        ip=$(ip a s "$iface_name" | awk '/inet / {print $2}')
        [ "$attempt" -ge "$max_attempts" ] && break
        sleep 1
    done
    if [ -z "$ip" ]; then
        echo "Failed to get IP address for $iface_name" >&2 | tee -a "$LOG_FILE"
        exit 1
    fi

    local end_time=$(date +%s)
    echo "get_ip for $iface_name completed in $((end_time - start_time)) seconds" >> "$LOG_FILE"

    echo "$ip"
}

# Usage:
# hhnet cleanup
# hhnet bond 1000 enp2s1 enp2s2 enp2s3 enp2s4
# hhnet vlan 1000 enp2s1

function usage() {
    echo "Usage: $0 <cleanup|bond|vlan> [<args> ...]" >&2
    echo " Cleanup all interfaces (enp2s1-9, bond0-3, vlans 1000-1020): " >&2
    echo "  hhnet cleanup" >&2
    echo " Setup bond from provided interfaces (at least one) and vlan on top of it" >&2
    echo "  hhnet bond 1000 enp2s1 enp2s2 enp2s3 enp2s4" >&2
    echo " Setup vlan on top of provided interface (exactly one)" >&2
    echo "  hhnet vlan 1000 enp2s1" >&2
}

if [ "$#" -lt 1 ]; then
    usage

    exit 1
elif [ "$1" == "cleanup" ]; then
    cleanup

    exit 0
elif [ "$1" == "bond" ]; then
    if [ "$#" -lt 3 ]; then
        echo "Usage: $0 bond <vlan_id> <iface1> [<iface2> ...]" >&2
        exit 1
    fi

    setup_bond bond0 "${@:3}"
    sleep 1
    setup_vlan bond0 "$2"
    get_ip bond0."$2"

    exit 0
elif [ "$1" == "vlan" ]; then
    if [ "$#" -ne 3 ]; then
        echo "Usage: $0 vlan <vlan_id> <iface1>" >&2
        exit 1
    fi

    setup_vlan "$3" "$2"
    get_ip "$3"."$2"

    exit 0
else
    usage

    exit 1
fi
