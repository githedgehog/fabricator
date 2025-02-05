#!/usr/bin/env bash
# Copyright 2024 Hedgehog
# SPDX-License-Identifier: Apache-2.0


set -e

function wait_for_interface_state() {
    local iface=$1
    local state=$2  # "up" or "down"
    local max_attempts=30
    local attempt=0

    while [ $attempt -lt $max_attempts ]; do
        if [ "$state" = "up" ]; then
            sudo ip link show "$iface" | grep -q "state UP" && return 0
        else
            sudo ip link show "$iface" | grep -q "state DOWN" && return 0
        fi
        sleep 0.1
        attempt=$((attempt + 1))
    done

    echo "ERROR: Interface $iface did not reach state $state within $((max_attempts * 100))ms" >&2
    exit 1
}

function cleanup() {
    for i in {0..3}; do
        if ip link show "bond$i" &>/dev/null; then
            sudo ip link set "bond$i" down 2>/dev/null || true
            wait_for_interface_state "bond$i" "down"
            sudo ip link delete "bond$i" 2>/dev/null || true
        fi
    done

    for i in {1..9}; do
        if sudo ip link show "enp2s$i" &>/dev/null; then
            sudo ip link set "enp2s$i" down 2>/dev/null || true
            wait_for_interface_state "enp2s$i" "down"
            for j in {1000..1020}; do
                if sudo ip link show "enp2s$i.$j" &>/dev/null; then
                    sudo ip link set "enp2s$i.$j" down 2>/dev/null || true
                    wait_for_interface_state "enp2s$i.$j" "down"
                    sudo ip link delete "enp2s$i.$j" 2>/dev/null || true
                fi
            done
        fi
    done

    sleep 2
}

function setup_bond() {
    local bond_name=$1
    shift  # Remove first argument (bond_name)

    sudo ip link add "$bond_name" type bond miimon 100 mode 802.3ad 2>/dev/null || {
        echo "ERROR: Failed to create bond $bond_name" >&2
        exit 1
    }

    for iface in "$@"; do
        if ! sudo ip link show "$iface" &>/dev/null; then
            echo "ERROR: Interface $iface does not exist." >&2
            exit 1
        fi
        sudo ip link set "$iface" down 2>/dev/null
        wait_for_interface_state "$iface" "down"
        sudo ip link set "$iface" master "$bond_name" 2>/dev/null || {
            echo "ERROR: Failed to add $iface to bond $bond_name" >&2
            exit 1
        }
    done

    sudo ip link set "$bond_name" up 2>/dev/null
    wait_for_interface_state "$bond_name" "up" || {
        echo "ERROR: Bond $bond_name did not come up." >&2
        exit 1
    }
}

function setup_vlan() {
    local iface_name=$1
    local vlan_id=$2

    if ! ip link show "$iface_name" &>/dev/null; then
        echo "ERROR: Interface $iface_name does not exist." >&2
        exit 1
    fi

    sudo ip link set "$iface_name" up 2>/dev/null
    wait_for_interface_state "$iface_name" "up" || {
        echo "ERROR: Interface $iface_name did not come up." >&2
        exit 1
    }

    sudo ip link add link "$iface_name" name "$iface_name.$vlan_id" type vlan id "$vlan_id" 2>/dev/null || {
        echo "ERROR: Failed to create VLAN $vlan_id on $iface_name" >&2
        exit 1
    }

    sudo ip link set "$iface_name.$vlan_id" up 2>/dev/null
    wait_for_interface_state "$iface_name.$vlan_id" "up" || {
        echo "ERROR: VLAN $iface_name.$vlan_id did not come up." >&2
        exit 1
    }
}

function get_ip() {
    local iface_name=$1
    local ip=""
    local max_attempts=300 # 5 minutes
    local attempt=0
    while [ -z "$ip" ]; do
        attempt=$((attempt + 1))
        ip=$(ip address show "$iface_name" 2>/dev/null | awk '/inet / {print $2}')
        [ "$attempt" -ge "$max_attempts" ] && break
        sleep 1
    done
    if [ -z "$ip" ]; then
        echo "ERROR: Failed to get IP address for $iface_name" >&2
        exit 1
    fi
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
