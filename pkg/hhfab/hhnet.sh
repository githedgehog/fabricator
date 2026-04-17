#!/usr/bin/env bash
# Copyright 2024 Hedgehog
# SPDX-License-Identifier: Apache-2.0

set -e
trap 'echo "Error on line $LINENO: $BASH_COMMAND" >&2' ERR

function cleanup() {
    mapfile -t bond_intfs < <(ip -brief link show type bond)

    for intf in "${bond_intfs[@]}"; do
            intf="${intf%% *}"
            sudo ip link del "$intf" || echo "warning: could not delete $intf" >&2
    done

    mapfile -t vlan_intfs < <(ip -brief link show type vlan)
    for intf in "${vlan_intfs[@]}"; do
            intf="${intf%@*}"
            sudo ip link set "$intf" down || echo "warning: could not bring down $intf" >&2
            sudo ip link del "$intf" || echo "warning: could not delete $intf" >&2
    done

    mapfile -t loopback_ips < <(ip -o address show dev lo scope global | awk '{ print $4 }')
    for addr in "${loopback_ips[@]}"; do
        sudo ip addr del "$addr" dev lo || echo "warning: could not delete VIP $addr" >&2
    done

    sleep 1
}

function restart_networkd() {
	sudo systemctl restart systemd-networkd
}

function setup_bond() {
    local bond_name=$1
    local hash_policy=$2
    local mtu=$3

    sudo ip l a "$bond_name" type bond miimon 100 mode 802.3ad xmit_hash_policy "$hash_policy"

    for iface in "${@:4}"; do
        # cannot enslave interface if it is up
        sudo ip l s "$iface" down 2> /dev/null || echo "warning: could not bring down $iface" >&2
        sudo ip l s "$iface" master "$bond_name"
    done

    sudo ip l s "$bond_name" up

    # Set MTU after bring-up: enslaving triggers carrier events that cause
    # systemd-networkd to re-apply its default config and reset slave MTUs.
    # Setting bond MTU last ensures it sticks and propagates to all slaves.
    if [ -n "$mtu" ]; then
        sleep 1
        sudo ip l s "$bond_name" mtu "$mtu"
    fi
}

function setup_vlan() {
    local iface_name=$1
    local vlan_id=$2
    local mtu=$3

    if [ -n "$mtu" ]; then
        sudo ip l s "$iface_name" mtu "$mtu"
    fi

    sudo ip l s "$iface_name" up
    sudo ip l a link "$iface_name" name "$iface_name.$vlan_id" type vlan id "$vlan_id"
    sudo ip l s "$iface_name.$vlan_id" up
}

function setup_p2p() {
    local iface_name=$1
    local ip_local=$2
    local ip_remote=$3

    sudo ip l s "$iface_name" up
    sudo ip a a "$ip_local" dev "$iface_name"
    sudo ip r a default via "$ip_remote" dev "$iface_name"
}

function get_vips() {
    ip -o address show dev lo scope global | awk '{ print $4 }'
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
    echo "Usage: $0 <cleanup|bond|vlan|p2p|getvips> [<args> ...]" >&2
    echo " Cleanup all interfaces (enp2s1-9, bond0-3, vlans 1000-1020): " >&2
    echo "  hhnet cleanup" >&2
    echo " Setup bond from provided interfaces (at least one) and vlan on top of it" >&2
    echo "  hhnet bond 1000 layer2+3 enp2s1 enp2s2 enp2s3 enp2s4" >&2
    echo " Setup vlan on top of provided interface (exactly one)" >&2
    echo "  hhnet vlan 1000 enp2s1" >&2
    echo " Setup p2p link (switch port and host on /31)" >&2
    echo "  hhnet p2p <iface> <local_ip> <remote_ip>" >&2
    echo " Get all Virtual IPs (VIPs) on the loopback interface: " >&2
    echo "  hhnet getvips" >&2
}

if [ "$#" -lt 1 ]; then
    usage

    exit 1
elif [ "$1" == "cleanup" ]; then
    cleanup
    restart_networkd

    exit 0
elif [ "$1" == "bond" ]; then
    if [ "$#" -lt 4 ]; then
        echo "Usage: $0 bond <vlan_id> <hash_policy> <iface1> [<iface2> ...] [--mtu=<value>]" >&2
        exit 1
    fi

    mtu=""
    ifaces=()
    for arg in "${@:4}"; do
        case "$arg" in
            --mtu=*) mtu="${arg#--mtu=}" ;;
            *) ifaces+=("$arg") ;;
        esac
    done

    if [ "${#ifaces[@]}" -eq 0 ]; then
        echo "Error: at least one interface required for bond" >&2
        exit 1
    fi

    setup_bond bond0 "$3" "$mtu" "${ifaces[@]}"
    sleep 1
    setup_vlan bond0 "$2"
    get_ip bond0."$2"

    exit 0
elif [ "$1" == "vlan" ]; then
    if [ "$#" -lt 3 ]; then
        echo "Usage: $0 vlan <vlan_id> <iface1> [--mtu=<value>]" >&2
        exit 1
    fi

    mtu=""
    iface="$3"
    for arg in "${@:4}"; do
        case "$arg" in
            --mtu=*) mtu="${arg#--mtu=}" ;;
        esac
    done

    setup_vlan "$iface" "$2" "$mtu"
    get_ip "$iface"."$2"

    exit 0
elif [ "$1" == "p2p" ]; then
    if [ "$#" -ne 4 ]; then
        echo "Usage: $0 p2p <iface> <local_ip> <remote_ip>" >&2
        exit 1
    fi

    setup_p2p "$2" "$3" "$4"
    get_ip "$2"

    exit 0
elif [ "$1" == "getvips" ] ; then
    get_vips

    exit 0
else
    usage

    exit 1
fi
