#!/usr/bin/env bash
# Copyright 2024 Hedgehog
# SPDX-License-Identifier: Apache-2.0

set -e
trap 'echo "Error on line $LINENO: $BASH_COMMAND" >&2' ERR

function cleanup() {
    # Drop source-based policy rules left over from previous trunking setups.
    # Only remove priority-1000 rules that have BOTH a `from <src>` and a
    # `lookup <table>` clause (the exact shape setup_policy_routing creates),
    # so unrelated rules at the same priority are untouched.
    mapfile -t policy_rules < <(ip rule show priority 1000 2>/dev/null | awk '
        /from / && /lookup / {
            src = ""
            table = ""
            for (i = 1; i <= NF; i++) {
                if ($i == "from" && i + 1 <= NF) src = $(i + 1)
                if ($i == "lookup" && i + 1 <= NF) table = $(i + 1)
            }
            if (src != "" && table != "") print src "\t" table
        }')
    for rule in "${policy_rules[@]}"; do
        IFS=$'\t' read -r src table <<< "$rule"
        [ -n "$src" ] && [ -n "$table" ] || continue
        sudo ip rule del from "$src" lookup "$table" priority 1000 2>/dev/null || true
    done

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

    if [ -n "$mtu" ]; then
        sudo ip l s "$bond_name" mtu "$mtu"
    fi

    for iface in "${@:4}"; do
        # cannot enslave interface if it is up
        sudo ip l s "$iface" down 2> /dev/null || echo "warning: could not bring down $iface" >&2
        sudo ip l s "$iface" master "$bond_name"
    done

    sudo ip l s "$bond_name" up
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

# Source-based policy routing for a VLAN sub-interface.
#
# When a server is trunked into multiple VPCs, every sub-interface gets its own
# DHCP default route with the same metric. Linux FIB-multipath then alternates
# replies across the sub-interfaces; replies that egress the "wrong" VLAN land
# in a VRF at the leaf that doesn't have the route back (the peering only
# leaks one direction per pair) and get silently dropped.
#
# Fix: for each sub-interface, add an `ip rule from <our IP> lookup <vlan>`
# plus a default route pinned to that sub-interface in table <vlan>. Replies
# then always egress the sub-interface that owns the source IP. Benign for
# single-attachment servers (one rule, same default as main).
function setup_policy_routing() {
    local subif=$1
    local vlan=$2

    local cidr
    cidr=$(ip -4 -o addr show dev "$subif" 2>/dev/null | awk '{print $4; exit}')
    if [ -z "$cidr" ]; then
        echo "warning: no IPv4 on $subif, skipping policy routing" >&2

        return 0
    fi
    local ip="${cidr%%/*}"

    local gw
    gw=$(ip route show dev "$subif" 2>/dev/null | awk '$1=="default"{print $3; exit}')
    if [ -z "$gw" ]; then
        echo "warning: no default route on $subif, skipping policy routing" >&2

        return 0
    fi

    # Idempotent: drop any prior rule for this source, then add fresh.
    sudo ip rule del from "$ip" lookup "$vlan" 2>/dev/null || true
    sudo ip rule add from "$ip" lookup "$vlan" priority 1000
    sudo ip route replace default via "$gw" dev "$subif" table "$vlan"
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
    setup_policy_routing "bond0.$2" "$2"

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
    setup_policy_routing "$iface.$2" "$2"

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
