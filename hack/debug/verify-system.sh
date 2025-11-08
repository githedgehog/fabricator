#!/usr/bin/env bash
# Copyright 2024 Hedgehog
# SPDX-License-Identifier: Apache-2.0

# System Requirements Verification Script
# Verifies that system meets requirements for fabricator development

set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

errors=0
warnings=0

check_command() {
    local cmd=$1
    local required=$2
    local min_version=${3:-}

    if command -v "$cmd" &> /dev/null; then
        local version=$($cmd --version 2>&1 | head -1 || echo "unknown")
        echo -e "${GREEN}✓${NC} $cmd: $version"

        if [ -n "$min_version" ]; then
            # Version checking would go here
            :
        fi
    else
        if [ "$required" = "yes" ]; then
            echo -e "${RED}✗${NC} $cmd: NOT FOUND (required)"
            ((errors++))
        else
            echo -e "${YELLOW}!${NC} $cmd: NOT FOUND (optional)"
            ((warnings++))
        fi
    fi
}

check_service() {
    local service=$1
    local required=$2

    if systemctl is-active --quiet "$service" 2>/dev/null; then
        echo -e "${GREEN}✓${NC} $service: running"
    elif systemctl list-unit-files | grep -q "^$service"; then
        echo -e "${YELLOW}!${NC} $service: not running"
        if [ "$required" = "yes" ]; then
            ((errors++))
        else
            ((warnings++))
        fi
    else
        if [ "$required" = "yes" ]; then
            echo -e "${RED}✗${NC} $service: NOT INSTALLED (required)"
            ((errors++))
        else
            echo -e "${YELLOW}!${NC} $service: not installed (optional)"
            ((warnings++))
        fi
    fi
}

check_port() {
    local port=$1
    local name=$2

    if nc -z localhost "$port" 2>/dev/null || curl -s "http://localhost:$port" > /dev/null 2>&1; then
        echo -e "${GREEN}✓${NC} $name (port $port): accessible"
    else
        echo -e "${YELLOW}!${NC} $name (port $port): not accessible"
        ((warnings++))
    fi
}

echo "=== Fabricator System Requirements Verification ==="
echo ""

echo "=== Required Commands ==="
check_command "go" "yes"
check_command "docker" "yes"
check_command "just" "yes"
check_command "git" "yes"
echo ""

echo "=== Optional Commands ==="
check_command "kubectl" "no"
check_command "virsh" "no"
check_command "k9s" "no"
check_command "oras" "no"
echo ""

echo "=== System Services ==="
check_service "docker" "yes"
check_service "libvirtd" "no"
echo ""

echo "=== Local Registry ==="
check_port "30000" "Zot registry"
echo ""

echo "=== Resource Requirements ==="

# Check CPU cores
cpu_cores=$(nproc)
if [ "$cpu_cores" -ge 4 ]; then
    echo -e "${GREEN}✓${NC} CPU cores: $cpu_cores (minimum 4)"
else
    echo -e "${RED}✗${NC} CPU cores: $cpu_cores (minimum 4 required)"
    ((errors++))
fi

# Check memory
mem_gb=$(free -g | awk '/^Mem:/{print $2}')
if [ "$mem_gb" -ge 8 ]; then
    echo -e "${GREEN}✓${NC} Memory: ${mem_gb}GB (minimum 8GB)"
else
    echo -e "${YELLOW}!${NC} Memory: ${mem_gb}GB (8GB recommended)"
    ((warnings++))
fi

# Check disk space
root_free=$(df -BG / | awk 'NR==2 {print $4}' | tr -d 'G')
if [ "$root_free" -ge 50 ]; then
    echo -e "${GREEN}✓${NC} Disk space (/): ${root_free}GB free (minimum 50GB)"
else
    echo -e "${RED}✗${NC} Disk space (/): ${root_free}GB free (minimum 50GB required)"
    ((errors++))
fi

echo ""

echo "=== Network Connectivity ==="

# Test ghcr.io
if curl -s -I --max-time 5 https://ghcr.io/v2/ > /dev/null 2>&1; then
    echo -e "${GREEN}✓${NC} ghcr.io: reachable"
else
    echo -e "${RED}✗${NC} ghcr.io: NOT reachable"
    ((errors++))
fi

# Test DNS
if nslookup google.com > /dev/null 2>&1; then
    echo -e "${GREEN}✓${NC} DNS resolution: working"
else
    echo -e "${RED}✗${NC} DNS resolution: failing"
    ((errors++))
fi

echo ""

echo "=== Go Environment ==="
if command -v go &> /dev/null; then
    echo "GOROOT: ${GOROOT:-not set}"
    echo "GOPATH: ${GOPATH:-not set}"
    echo "Go modules: $(go env GOMOD 2>/dev/null || echo 'N/A')"
fi
echo ""

echo "=== Docker Configuration ==="
if command -v docker &> /dev/null && docker info &> /dev/null; then
    echo "Docker root: $(docker info 2>/dev/null | grep 'Docker Root Dir' | cut -d: -f2 | xargs)"
    echo "Storage driver: $(docker info 2>/dev/null | grep 'Storage Driver' | cut -d: -f2 | xargs)"
    echo "Logged in registries:"
    if [ -f "$HOME/.docker/config.json" ]; then
        cat "$HOME/.docker/config.json" | jq -r '.auths | keys[]' 2>/dev/null || echo "  Unable to parse config"
    else
        echo "  No credentials configured"
    fi
fi
echo ""

echo "=== Summary ==="
echo -e "Errors: ${RED}$errors${NC}"
echo -e "Warnings: ${YELLOW}$warnings${NC}"
echo ""

if [ $errors -eq 0 ]; then
    echo -e "${GREEN}✓ System meets minimum requirements for fabricator development${NC}"
    exit 0
else
    echo -e "${RED}✗ System does NOT meet minimum requirements${NC}"
    echo ""
    echo "Please address the errors above before proceeding."
    echo "See README.md for installation instructions."
    exit 1
fi
