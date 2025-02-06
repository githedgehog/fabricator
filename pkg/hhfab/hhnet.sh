#!/usr/bin/env bash
# Copyright 2024 Hedgehog
# SPDX-License-Identifier: Apache-2.0

set -euo pipefail

readonly MAX_WAIT_ATTEMPTS=30
readonly WAIT_INTERVAL=0.1
readonly MAX_IP_ATTEMPTS=300
readonly VLAN_MIN=1
readonly VLAN_MAX=4094

if [[ "${DEBUG:-}" == "1" ]]; then
    exec 3>&2
    exec 2>/tmp/hhnet.log
    set -x
fi

function log() {
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] $*" >&2
}

function dump_network_state() {
    log "Current network state:"
    sudo ip link show >&2 || true
    sudo ip addr show >&2 || true
}

function validate_interface_name() {
    local iface=$1
    if ! [[ "$iface" =~ ^[a-zA-Z0-9._-]+$ ]]; then
        log "ERROR: Invalid interface name: $iface"
        return 1
    fi
    return 0
}

function validate_vlan_id() {
    local vlan_id=$1
    if ! [[ "$vlan_id" =~ ^[0-9]+$ ]] || [ "$vlan_id" -lt $VLAN_MIN ] || [ "$vlan_id" -gt $VLAN_MAX ]; then
        log "ERROR: Invalid VLAN ID. Must be between $VLAN_MIN and $VLAN_MAX"
        return 1
    fi
    return 0
}

function wait_for_interface_state() {
    local iface=$1
    local state=$2  # "up" or "down"
    local attempt=0

    log "Waiting for interface $iface to become $state"
    while [ $attempt -lt $MAX_WAIT_ATTEMPTS ]; do
        if [ "$state" = "up" ]; then
            if sudo ip link show "$iface" | grep -q "state UP"; then
                log "Interface $iface is UP"
                return 0
            fi
        else
            if sudo ip link show "$iface" | grep -q "state DOWN"; then
                log "Interface $iface is DOWN"
                return 0
            fi
        fi
        sleep $WAIT_INTERVAL
        attempt=$((attempt + 1))
        log "Still waiting for $iface to be $state (attempt $attempt/$MAX_WAIT_ATTEMPTS)"
    done

    log "ERROR: Interface $iface did not reach state $state within $((MAX_WAIT_ATTEMPTS * 100))ms"
    sudo ip link show "$iface" >&2 || true
    return 1
}

function cleanup() {
    local failed=0

    log "Starting cleanup"
    dump_network_state

    for i in {0..3}; do
        if sudo ip link show "bond$i" &>/dev/null; then
            log "Cleaning up bond$i"
            sudo ip link set "bond$i" down 2>/dev/null || failed=1
            wait_for_interface_state "bond$i" "down" || true
            sudo ip link delete "bond$i" 2>/dev/null || failed=1
        fi
    done

    for i in {1..9}; do
        if sudo ip link show "enp2s$i" &>/dev/null; then
            log "Cleaning up enp2s$i"
            sudo ip link set "enp2s$i" down 2>/dev/null || failed=1
            wait_for_interface_state "enp2s$i" "down" || true
            for j in {1000..1020}; do
                if sudo ip link show "enp2s$i.$j" &>/dev/null; then
                    log "Cleaning up VLAN enp2s$i.$j"
                    sudo ip link set "enp2s$i.$j" down 2>/dev/null || failed=1
                    wait_for_interface_state "enp2s$i.$j" "down" || true
                    sudo ip link delete "enp2s$i.$j" 2>/dev/null || failed=1
                fi
            done
        fi
    done

    log "Cleanup completed with status: $failed"
    dump_network_state
    sleep 2
    return $failed
}

function setup_bond() {
    local bond_name=$1
    shift  # Remove first argument (bond_name)

    log "Setting up bond: $bond_name with interfaces: $*"
    dump_network_state

    if [ $# -lt 2 ]; then
        log "ERROR: At least one interface must be provided for bonding"
        return 1
    fi

    for iface in "$@"; do
        if ! validate_interface_name "$iface"; then
            return 1
        fi
    done

    for iface in "$@"; do
        if ! sudo ip link show "$iface" &>/dev/null; then
            log "ERROR: Interface $iface does not exist"
            return 1
        fi
    done

    if ! sudo ip link add "$bond_name" type bond miimon 100 mode 802.3ad 2>/dev/null; then
        log "ERROR: Failed to create bond $bond_name"
        return 1
    fi

    local success=true
    for iface in "$@"; do
        log "Adding $iface to $bond_name"
        if ! sudo ip link set "$iface" down 2>/dev/null || \
           ! wait_for_interface_state "$iface" "down" || \
           ! sudo ip link set "$iface" master "$bond_name" 2>/dev/null; then
            log "ERROR: Failed to add $iface to bond $bond_name"
            success=false
            break
        fi
    done

    if ! $success; then
        log "ERROR: Bond setup failed, cleaning up"
        sudo ip link delete "$bond_name" 2>/dev/null || true
        dump_network_state
        return 1
    fi

    log "Setting $bond_name up"
    if ! sudo ip link set "$bond_name" up 2>/dev/null || \
       ! wait_for_interface_state "$bond_name" "up"; then
        log "ERROR: Bond $bond_name did not come up"
        sudo ip link delete "$bond_name" 2>/dev/null || true
        dump_network_state
        return 1
    fi

    log "Bond setup completed successfully"
    dump_network_state
    return 0
}

function setup_vlan() {
    local iface_name=$1
    local vlan_id=$2

    log "Setting up VLAN $vlan_id on interface $iface_name"
    dump_network_state

    # Validate inputs
    if ! validate_interface_name "$iface_name" || ! validate_vlan_id "$vlan_id"; then
        return 1
    fi

    if ! sudo ip link show "$iface_name" &>/dev/null; then
        log "ERROR: Interface $iface_name does not exist"
        return 1
    fi

    log "Setting $iface_name up"
    if ! sudo ip link set "$iface_name" up 2>/dev/null || \
       ! wait_for_interface_state "$iface_name" "up"; then
        log "ERROR: Interface $iface_name did not come up"
        dump_network_state
        return 1
    fi

    log "Creating VLAN $vlan_id"
    if ! sudo ip link add link "$iface_name" name "$iface_name.$vlan_id" type vlan id "$vlan_id" 2>/dev/null; then
        log "ERROR: Failed to create VLAN $vlan_id on $iface_name"
        dump_network_state
        return 1
    fi

    log "Setting VLAN up"
    if ! sudo ip link set "$iface_name.$vlan_id" up 2>/dev/null || \
       ! wait_for_interface_state "$iface_name.$vlan_id" "up"; then
        log "ERROR: VLAN $iface_name.$vlan_id did not come up"
        sudo ip link delete "$iface_name.$vlan_id" 2>/dev/null || true
        dump_network_state
        return 1
    fi

    log "VLAN setup completed successfully"
    dump_network_state
    return 0
}

function get_ip() {
    local iface_name=$1
    local ip=""
    local attempt=0

    if ! validate_interface_name "$iface_name"; then
        return 1
    fi

    log "Waiting for IP address on $iface_name"
    while [ -z "$ip" ] && [ $attempt -lt $MAX_IP_ATTEMPTS ]; do
        ip=$(sudo ip address show "$iface_name" 2>/dev/null | awk '/inet / {print $2; exit}')
        [ -z "$ip" ] && {
            sleep 1
            attempt=$((attempt + 1))
            if ((attempt % 10 == 0)); then
                log "Still waiting for IP (attempt $attempt/$MAX_IP_ATTEMPTS)"
            fi
        }
    done

    if [ -z "$ip" ]; then
        log "ERROR: Failed to get IP address for $iface_name"
        sudo ip address show "$iface_name" >&2 || true
        dump_network_state
        return 1
    fi

    log "Got IP address $ip for $iface_name"
    echo "$ip"
    return 0
}

function usage() {
    cat <<EOF >&2
Usage: $0 <command> [args...]

Commands:
    cleanup
        Cleanup all interfaces (enp2s1-9, bond0-3, vlans 1000-1020)

    bond <vlan_id> <iface1> [<iface2> ...]
        Setup bond from provided interfaces and VLAN on top of it
        - vlan_id must be between $VLAN_MIN and $VLAN_MAX
        - at least one interface must be provided

    vlan <vlan_id> <iface1>
        Setup VLAN on top of provided interface
        - vlan_id must be between $VLAN_MIN and $VLAN_MAX
        - exactly one interface must be provided

Examples:
    $0 cleanup
    $0 bond 1000 enp2s1 enp2s2
    $0 vlan 1000 enp2s1

Debug mode:
    DEBUG=1 $0 <command> [args...]
    - Enables detailed logging to /tmp/hhnet.log
    - Shows all commands being executed
    - Dumps network state at key points
EOF
}

function main() {
    log "Starting hhnet with arguments: $*"

    if [ $# -lt 1 ]; then
        usage
        return 1
    fi

    case "$1" in
        cleanup)
            cleanup
            ;;
        bond)
            if [ $# -lt 3 ]; then
                log "ERROR: Insufficient arguments for bond command"
                usage
                return 1
            fi
            if ! setup_bond bond0 "${@:3}" || \
               ! { sleep 1; setup_vlan bond0 "$2"; } || \
               ! get_ip "bond0.$2"; then
                return 1
            fi
            ;;
        vlan)
            if [ $# -ne 3 ]; then
                log "ERROR: Invalid number of arguments for vlan command"
                usage
                return 1
            fi
            if ! setup_vlan "$3" "$2" || ! get_ip "$3.$2"; then
                return 1
            fi
            ;;
        *)
            log "ERROR: Unknown command: $1"
            usage
            return 1
            ;;
    esac
}

main "$@"
