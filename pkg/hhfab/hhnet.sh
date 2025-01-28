#!/usr/bin/env bash
# Copyright 2024 Hedgehog
# SPDX-License-Identifier: Apache-2.0

set -e

NETWORKD_PATH="/etc/systemd/network"

function cleanup() {
    for i in {0..3}; do
        sudo networkctl down "bond$i" 2> /dev/null || true
        sudo networkctl delete "bond$i" 2> /dev/null || true
    done

    for i in {1..9}; do
        sudo networkctl down "enp2s$i" 2> /dev/null || true
        for j in {1000..1020}; do
            sudo networkctl down "enp2s$i.$j" 2> /dev/null || true
            sudo networkctl delete "enp2s$i.$j" 2> /dev/null || true
        done
    done

    sudo rm -f "$NETWORKD_PATH"/10-bond*.network "$NETWORKD_PATH"/10-bond*.netdev
    sudo rm -f "$NETWORKD_PATH"/20-slave*.network
    sudo rm -f "$NETWORKD_PATH"/30-vlan*.network "$NETWORKD_PATH"/30-vlan*.netdev

    sudo systemctl restart systemd-networkd 2> /dev/null || true
    sleep 2
}

function setup_bond() {
    local bond_name=$1
    shift
    local interfaces=("$@")
    for iface in "${interfaces[@]}"; do
        if ! sudo networkctl status "$iface" &> /dev/null; then
            echo "Interface $iface not found"
            return 1
        fi
    done
    cat << EOF | sudo tee "$NETWORKD_PATH/10-$bond_name.netdev" > /dev/null
[NetDev]
Name=$bond_name
Kind=bond
[Bond]
Mode=802.3ad
LACPTransmitRate=fast
MIIMonitorSec=1s
EOF
    for iface in "${interfaces[@]}"; do
        cat << EOF | sudo tee "$NETWORKD_PATH/20-slave-$iface.network" > /dev/null
[Match]
Name=$iface
[Network]
Bond=$bond_name
[Link]
MTUBytes=9036
EOF
    done

    sudo systemctl restart systemd-networkd 2> /dev/null || true
    sleep 5
}

function setup_vlan() {
    local parent_iface=$1
    local vlan_id=$2

    if ! sudo networkctl status "$parent_iface" &> /dev/null; then
        echo "Parent interface $parent_iface not found"
        return 1
    fi

    sudo networkctl up "$parent_iface" 2> /dev/null || true
    sleep 2

    cat << EOF | sudo tee "$NETWORKD_PATH/30-vlan-$parent_iface-$vlan_id.netdev" > /dev/null
[NetDev]
Name=$parent_iface.$vlan_id
Kind=vlan

[VLAN]
Id=$vlan_id
EOF

    cat << EOF | sudo tee "$NETWORKD_PATH/30-vlan-$parent_iface-$vlan_id.network" > /dev/null
[Match]
Name=$parent_iface.$vlan_id

[Network]
DHCP=yes
EOF

    cat << EOF | sudo tee "$NETWORKD_PATH/30-$parent_iface.network" > /dev/null
[Match]
Name=$parent_iface

[Network]
VLAN=$parent_iface.$vlan_id

[Link]
MTUBytes=9036
EOF

    sudo systemctl restart systemd-networkd 2> /dev/null || true
    sleep 5
}

function wait_for_interface() {
    local iface_name=$1
    local max_attempts=60
    local attempt=0
    local retry_delay=1

    while true; do
        attempt=$((attempt + 1))

        local status
        status=$(sudo networkctl status "$iface_name" 2> /dev/null)

        if echo "$status" | grep -q "State: routable" 2> /dev/null; then
            return 0
        elif echo "$status" | grep -q "State: carrier" 2> /dev/null; then
            return 0
        elif echo "$status" | grep -q "State: degraded" 2> /dev/null; then
            return 0
        fi

        if [ "$attempt" -ge "$max_attempts" ]; then
            echo "Interface $iface_name failed to become ready after $max_attempts attempts"
            return 1
        fi

        sleep "$retry_delay"
    done
}

function get_ip() {
    local iface_name=$1
    local ip=""
    local max_attempts=60
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

function usage() {
    echo "Usage: $0 [command] [...]" >&2
    echo "  Cleanup all interfaces (enp2s1-9, bond0-3, vlans 1000-1020): " >&2
    echo "    hhnet cleanup" >&2
    echo "  Setup bond from provided interfaces (at least one) and vlan on top of it" >&2
    echo "    hhnet bond 1000 enp2s1 enp2s2 enp2s3 enp2s4" >&2
    echo "  Setup vlan on top of provided interface (exactly one)" >&2
    echo "    hhnet vlan 1000 enp2s1" >&2
}

if [ "$#" -lt 1 ]; then
    usage
    exit 1
elif [ "$1" == "cleanup" ]; then
    cleanup
    exit 0
elif [ "$1" == "bond" ]; then
    if [ "$#" -lt 3 ]; then
        echo "Usage: $0 bond <vlan_id> <interface> [...]" >&2
        exit 1
    fi
    setup_bond bond0 "${@:3}" || exit 1
    sleep 1
    wait_for_interface bond0 || exit 1
    setup_vlan bond0 "$2" || exit 1
    wait_for_interface "bond0.$2" || exit 1
    get_ip "bond0.$2" || exit 1
    exit 0
elif [ "$1" == "vlan" ]; then
    if [ "$#" -ne 3 ]; then
        echo "Usage: $0 vlan <vlan_id> <interface>" >&2
        exit 1
    fi
    setup_vlan "$3" "$2" || exit 1
    wait_for_interface "$3.$2" || exit 1
    get_ip "$3.$2" || exit 1
    exit 0
else
    usage
    exit 1
fi
