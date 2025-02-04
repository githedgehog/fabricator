#!/usr/bin/env bash
# Copyright 2024 Hedgehog
# SPDX-License-Identifier: Apache-2.0

set -e

LOGFILE="/var/log/hhnet.log"
sudo touch "$LOGFILE"
sudo chmod 666 "$LOGFILE"

function log() {
    local timestamp=$(date '+%Y-%m-%d %H:%M:%S')
    echo "[$timestamp] $*" >> "$LOGFILE"
}

function log_error() {
    local timestamp=$(date '+%Y-%m-%d %H:%M:%S')
    echo "[$timestamp] ERROR: $*" >> "$LOGFILE"
    echo "$*" >&2
}

function cleanup() {
    log "Starting cleanup of network interfaces"
    
    for i in {0..3}; do
        if ip link show "bond$i" &>/dev/null; then
            log "Removing bond$i"
            if ! sudo ip l d "bond$i" 2>> "$LOGFILE"; then
                log_error "Failed to remove bond$i"
            fi
        fi
    done

    for i in {1..9}; do
        if ip link show "enp2s$i" &>/dev/null; then
            log "Setting enp2s$i down"
            sudo ip l s "enp2s$i" down 2> /dev/null || log_error "Failed to set enp2s$i down"
        fi
        
        for j in {1000..1020}; do
            if ip link show "enp2s$i.$j" &>/dev/null; then
                log "Removing VLAN enp2s$i.$j"
                sudo ip l d "enp2s$i.$j" 2> /dev/null || log_error "Failed to remove VLAN enp2s$i.$j"
            fi
        done
    done

    log "Cleanup completed"
    sleep 1
}

function wait_interface_up() {
    local iface_name=$1
    local timeout=120
    local interval=1
    local elapsed=0

    log "Waiting up to ${timeout}s for interface $iface_name to come up"

    while [ $elapsed -lt $timeout ]; do
        if ip link show "$iface_name" | grep -q "state UP"; then
            log "Interface $iface_name is up after ${elapsed}s"
            return 0
        fi
        sleep $interval
        elapsed=$((elapsed + interval))

        if [ $((elapsed % 10)) -eq 0 ]; then
            log "Still waiting for interface $iface_name to come up (${elapsed}s elapsed)"
        fi
    done

    log_error "Interface $iface_name failed to come up within ${timeout}s"
    return 1
}

function setup_bond() {
    local bond_name=$1
    log "Setting up bond $bond_name with interfaces: ${*:2}"

    sudo ip l a "$bond_name" type bond miimon 100 mode 802.3ad || {
        log_error "Failed to create bond $bond_name"
        return 1
    }

    for iface in "${@:2}"; do
        log "Adding interface $iface to bond $bond_name"
        # cannot enslave interface if it is up
        sudo ip l s "$iface" down 2> /dev/null || log_error "Failed to set $iface down"
        sudo ip l s "$iface" master "$bond_name" || {
            log_error "Failed to add $iface to bond $bond_name"
            return 1
        }
    done

    sudo ip l s "$bond_name" up || {
        log_error "Failed to bring up bond $bond_name"
        return 1
    }

    if ! wait_interface_up "$bond_name"; then
        log_error "Bond interface $bond_name failed to come up properly"
        return 1
    fi

    log "Successfully set up bond $bond_name"
}

function setup_vlan() {
    local iface_name=$1
    local vlan_id=$2
    log "Setting up VLAN $vlan_id on interface $iface_name"

    sudo ip l s "$iface_name" up || {
        log_error "Failed to bring up interface $iface_name"
        return 1
    }
    
    sudo ip l a link "$iface_name" name "$iface_name.$vlan_id" type vlan id "$vlan_id" || {
        log_error "Failed to create VLAN $vlan_id on $iface_name"
        return 1
    }
    
    sudo ip l s "$iface_name.$vlan_id" up || {
        log_error "Failed to bring up VLAN interface $iface_name.$vlan_id"
        return 1
    }
    
    if ! wait_interface_up "$iface_name.$vlan_id"; then
        log_error "VLAN interface $iface_name.$vlan_id failed to come up properly"
        return 1
    fi

    log "Successfully set up VLAN $vlan_id on $iface_name"
}

function get_ip() {
    local iface_name=$1
    local ip=""
    local max_attempts=300 # 5 minutes
    local attempt=0

    log "Waiting for IP address on interface $iface_name"
    while [ -z "$ip" ]; do
        attempt=$((attempt + 1))
        ip=$(ip a s "$iface_name" | awk '/inet / {print $2}')
        [ "$attempt" -ge "$max_attempts" ] && break
        sleep 1
    done
    
    if [ -z "$ip" ]; then
        echo "Failed to get IP address for $iface_name after $max_attempts attempts"
        exit 1
    fi
    
    log "Got IP address $ip for interface $iface_name"
    echo "$ip"
}

function usage() {
    echo "Usage: $0 <cleanup|bond|vlan> [<args> ...]" >&2
    echo " Cleanup all interfaces (enp2s1-9, bond0-3, vlans 1000-1020): " >&2
    echo "  hhnet cleanup" >&2
    echo " Setup bond from provided interfaces (at least one) and vlan on top of it" >&2
    echo "  hhnet bond 1000 enp2s1 enp2s2 enp2s3 enp2s4" >&2
    echo " Setup vlan on top of provided interface (exactly one)" >&2
    echo "  hhnet vlan 1000 enp2s1" >&2
}

log "Starting hhnet with arguments: $*"

if [ "$#" -lt 1 ]; then
    echo "No arguments provided"
    usage
    exit 1
elif [ "$1" == "cleanup" ]; then
    cleanup
    exit 0
elif [ "$1" == "bond" ]; then
    if [ "$#" -lt 3 ]; then
        echo "Insufficient arguments for bond command"
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
        echo "Incorrect number of arguments for vlan command"
        echo "Usage: $0 vlan <vlan_id> <iface1>" >&2
        exit 1
    fi

    setup_vlan "$3" "$2"
    get_ip "$3"."$2"
    exit 0
else
    log_error "Invalid command: $1"
    usage
    exit 1
fi
