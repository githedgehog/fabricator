#!/usr/bin/env bash
# Copyright 2024 Hedgehog
# SPDX-License-Identifier: Apache-2.0


set -e

function cleanup() {
    mapfile -t bond_intfs < <(ip -brief link show type bond)

    for intf in "${bond_intfs[@]}"; do
            intf="${intf%% *}"
            sudo ip link del "$intf"
    done

    mapfile -t vlan_intfs < <(ip -brief link show type vlan)
    for intf in "${vlan_intfs[@]}"; do
            intf="${intf%@*}"
            sudo ip link set "$intf" down
            sudo ip link del "$intf"
    done
    sleep 1
}

function setup_bond() {
    local bond_name=$1
    local hash_policy=$2

    sudo ip l a "$bond_name" type bond miimon 100 mode 802.3ad xmit_hash_policy "$hash_policy"

    for iface in "${@:3}"; do
        # cannot enslave interface if it is up
        sudo ip l s "$iface" down 2> /dev/null || true
        sudo ip l s "$iface" master "$bond_name"
    done

    sudo ip l s "$bond_name" up
}

function setup_vlan() {
    local iface_name=$1
    local vlan_id=$2

    sudo ip l s "$iface_name" up
    sudo ip l a link "$iface_name" name "$iface_name.$vlan_id" type vlan id "$vlan_id"
    sudo ip l s "$iface_name.$vlan_id" up
}

function get_ip() {
    local iface_name=$1
    local ip=""
    local max_attempts=300 # 5 minutes
    local attempt=0

    while [ -z "$ip" ]; do
        attempt=$((attempt + 1))
        ip=$(ip a s "$iface_name" | awk '/inet / {print $2}')
        [ "$attempt" -ge "$max_attempts" ] && break
        sleep 1
    done
    if [ -z "$ip" ]; then
        echo "Failed to get IP address for $iface_name" >&2
        exit 1
    fi
    echo "$ip"
}

# Usage:
# hhnet cleanup
# hhnet bond 1000 layer2+3 enp2s1 enp2s2 enp2s3 enp2s4
# hhnet vlan 1000 enp2s1

function usage() {
    echo "Usage: $0 <cleanup|bond|vlan> [<args> ...]" >&2
    echo " Cleanup all interfaces (enp2s1-9, bond0-3, vlans 1000-1020): " >&2
    echo "  hhnet cleanup" >&2
    echo " Setup bond from provided interfaces (at least one) and vlan on top of it" >&2
    echo "  hhnet bond 1000 layer2+3 enp2s1 enp2s2 enp2s3 enp2s4" >&2
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
    if [ "$#" -lt 4 ]; then
        echo "Usage: $0 bond <vlan_id> <hash_policy> <iface1> [<iface2> ...]" >&2
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
