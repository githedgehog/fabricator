#!/bin/bash
# Copyright 2025 Hedgehog
# SPDX-License-Identifier: Apache-2.0

# VLAB Trivy Scanner - Scans control and gateway VMs
# Usage: ./vlab-trivy-runner.sh

set -e

CONTROL_VM="control-1"
GATEWAY_VM="gateway-1"
VLAB_LOG="vlab.log"
RESULTS_DIR="trivy-reports"
SCRIPT_PATH="${SCRIPT_PATH:-./security/scripts/trivy-setup.sh}"
VLAB_TIMEOUT=${VLAB_TIMEOUT:-25}

# Find hhfab binary relative to project root
# Script may be run from project root or from security/scripts/
if [ -f "./hhfab" ] && [ -x "./hhfab" ]; then
    HHFAB_BIN="./hhfab"
elif [ -f "bin/hhfab" ] && [ -x "bin/hhfab" ]; then
    HHFAB_BIN="bin/hhfab"
elif [ -f "../../hhfab" ] && [ -x "../../hhfab" ]; then
    HHFAB_BIN="../../hhfab"
elif [ -f "../../bin/hhfab" ] && [ -x "../../bin/hhfab" ]; then
    HHFAB_BIN="../../bin/hhfab"
else
    echo -e "${RED}ERROR: hhfab binary not found${NC}"
    echo "Looked for:"
    echo "  - ./hhfab (project root - local)"
    echo "  - bin/hhfab (project root - CI)" 
    echo "  - ../../hhfab (from scripts dir - local)"
    echo "  - ../../bin/hhfab (from scripts dir - CI)"
    echo "Current directory: $(pwd)"
    echo "Please ensure hhfab binary exists or run from project root"
    exit 1
fi

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# Track if network configuration was applied
NETWORK_CONFIGURED=false

configure_network() {
    echo -e "${YELLOW}Configuring network access for gateway VM...${NC}"
    
    # Configure control-1 as NAT gateway
    echo "Setting up control-1 as NAT gateway..."
    if ! $HHFAB_BIN vlab ssh -n "$CONTROL_VM" -- '
        # Enable IP forwarding
        sudo sysctl net.ipv4.ip_forward=1
        
        # Add NAT rules to forward traffic from the internal network
        sudo iptables -A FORWARD -s 172.30.0.0/21 -o enp2s0 -j ACCEPT
        sudo iptables -A FORWARD -d 172.30.0.0/21 -i enp2s0 -m state --state RELATED,ESTABLISHED -j ACCEPT
        sudo iptables -t nat -A POSTROUTING -s 172.30.0.0/21 -o enp2s0 -j MASQUERADE
        
        echo "NAT gateway configured on control-1"
    '; then
        echo -e "${RED}Failed to configure NAT gateway on control-1${NC}"
        return 1
    fi
    
    # Configure gateway-1 to use control-1 as default gateway
    echo "Configuring gateway-1 to use control-1 as gateway..."
    if ! $HHFAB_BIN vlab ssh -n "$GATEWAY_VM" -- '
        # Remove current default route
        sudo ip route del default || true
        
        # Add control-1 as the default gateway
        sudo ip route add default via 172.30.0.5 dev enp2s0
        
        # Configure DNS resolution
        echo "Configuring DNS servers..."
        sudo mkdir -p /etc/systemd/resolved.conf.d
        sudo tee /etc/systemd/resolved.conf.d/dns.conf > /dev/null << EOF
[Resolve]
DNS=8.8.8.8 1.1.1.1
FallbackDNS=8.8.4.4 1.0.0.1
EOF
        sudo systemctl restart systemd-resolved || true
        
        # Also update /etc/resolv.conf as fallback
        sudo tee /etc/resolv.conf > /dev/null << EOF
nameserver 8.8.8.8
nameserver 1.1.1.1
EOF
        
        echo "Default gateway and DNS configured on gateway-1"
    '; then
        echo -e "${RED}Failed to configure gateway routing on gateway-1${NC}"
        return 1
    fi
    
    # Test internet connectivity from gateway-1
    echo "Testing internet connectivity from gateway-1..."
    if $HHFAB_BIN vlab ssh -n "$GATEWAY_VM" -- 'ping -c 2 8.8.8.8 >/dev/null 2>&1'; then
        echo "✓ IP connectivity working"
        
        # Test DNS resolution
        echo "Testing DNS resolution from gateway-1..."
        if $HHFAB_BIN vlab ssh -n "$GATEWAY_VM" -- 'curl -I --connect-timeout 10 https://github.com >/dev/null 2>&1'; then
            echo "✓ DNS resolution working"
            echo -e "${GREEN}Gateway VM now has full internet access${NC}"
            NETWORK_CONFIGURED=true
            return 0
        else
            echo -e "${RED}DNS resolution failed on gateway VM${NC}"
            return 1
        fi
    else
        echo -e "${RED}Gateway VM still cannot reach internet${NC}"
        return 1
    fi
}

cleanup_network() {
    if [ "$NETWORK_CONFIGURED" = true ]; then
        echo -e "${YELLOW}Cleaning up network configuration...${NC}"
        
        # Restore original routing on gateway-1
        echo "Restoring original routing on gateway-1..."
        $HHFAB_BIN vlab ssh -n "$GATEWAY_VM" -- '
            sudo ip route del default || true
            sudo ip route add default via 172.30.90.3 dev dummy0 proto static metric 42000 || true
            
            # Restore original DNS configuration
            sudo rm -f /etc/systemd/resolved.conf.d/dns.conf || true
            sudo systemctl restart systemd-resolved || true
        ' >/dev/null 2>&1 || echo "Warning: Failed to restore gateway-1 routing"
        
        # Remove NAT configuration from control-1
        echo "Removing NAT configuration from control-1..."
        $HHFAB_BIN vlab ssh -n "$CONTROL_VM" -- '
            sudo iptables -D FORWARD -s 172.30.0.0/21 -o enp2s0 -j ACCEPT 2>/dev/null || true
            sudo iptables -D FORWARD -d 172.30.0.0/21 -i enp2s0 -m state --state RELATED,ESTABLISHED -j ACCEPT 2>/dev/null || true
            sudo iptables -t nat -D POSTROUTING -s 172.30.0.0/21 -o enp2s0 -j MASQUERADE 2>/dev/null || true
            sudo sysctl net.ipv4.ip_forward=0 2>/dev/null || true
        ' >/dev/null 2>&1 || echo "Warning: Failed to cleanup control-1 NAT configuration"
        
        echo "Network configuration cleanup completed"
        NETWORK_CONFIGURED=false
    fi
}

cleanup() {
    echo -e "${YELLOW}Cleaning up...${NC}"
    
    # Clean up network configuration first
    cleanup_network
    
    # Clean up VLAB process
    if [ ! -z "$VLAB_PID" ] && kill -0 $VLAB_PID 2>/dev/null; then
        echo "Terminating VLAB process (PID: $VLAB_PID)..."
        kill $VLAB_PID || true
        wait $VLAB_PID || true
        echo "VLAB process terminated"
    fi
}

trap cleanup EXIT INT TERM

if [ ! -f "$SCRIPT_PATH" ]; then
    echo -e "${RED}ERROR: Trivy setup script not found at: $SCRIPT_PATH${NC}"
    echo "Please ensure trivy-setup.sh exists or set SCRIPT_PATH correctly"
    exit 1
fi

echo -e "${GREEN}Starting VLAB Trivy Scanner${NC}"
echo "Control VM: $CONTROL_VM"
echo "Gateway VM: $GATEWAY_VM"  
echo "hhfab binary: $HHFAB_BIN"
echo "Script: $SCRIPT_PATH"
echo "Results: $RESULTS_DIR"
echo "Log: $VLAB_LOG"
echo "Timeouts: VLAB=${VLAB_TIMEOUT}m"
echo ""

if [ ! -f "fab.yaml" ]; then
    echo -e "${YELLOW}Initializing VLAB (control + gateway)...${NC}"
    $HHFAB_BIN init -v --dev --gateway
fi

# Generate join token for gateway node (required for multi-node setup)
echo -e "${YELLOW}Generating join token for gateway node...${NC}"
export HHFAB_JOIN_TOKEN=$(openssl rand -base64 24)
echo "Join token generated: ${HHFAB_JOIN_TOKEN:0:8}..."

# Start VLAB in background
echo -e "${YELLOW}Starting VLAB...${NC}"
timeout ${VLAB_TIMEOUT}m $HHFAB_BIN vlab up --controls-restricted=false > "$VLAB_LOG" 2>&1 &
VLAB_PID=$!
echo "VLAB PID: $VLAB_PID"

# Wait for VLAB to be ready with continuous log output
echo -e "${YELLOW}Waiting for VLAB to be ready...${NC}"
echo -e "${YELLOW}=== VLAB Startup Log ===${NC}"

# Stream log output while waiting for ready message
if timeout ${VLAB_TIMEOUT}m bash -c "
    tail -f '$VLAB_LOG' &
    TAIL_PID=\$!
    
    while true; do
        if grep -q 'INF VLAB is ready took=' '$VLAB_LOG' 2>/dev/null; then
            kill \$TAIL_PID 2>/dev/null || true
            exit 0
        fi
        if ! kill -0 $VLAB_PID 2>/dev/null; then
            kill \$TAIL_PID 2>/dev/null || true
            exit 1
        fi
        sleep 2
    done
"; then
    echo -e "${GREEN}VLAB is ready${NC}"
else
    echo -e "${RED}Timeout waiting for VLAB to be ready${NC}"
    exit 1
fi

# Simple SSH connectivity test
echo -e "${YELLOW}Testing SSH connectivity...${NC}"
if ! $HHFAB_BIN vlab ssh -n "$CONTROL_VM" -- 'echo "Control SSH works"' >/dev/null 2>&1; then
    echo -e "${RED}Cannot connect to control-1${NC}"
    exit 1
fi

if ! $HHFAB_BIN vlab ssh -n "$GATEWAY_VM" -- 'echo "Gateway SSH works"' >/dev/null 2>&1; then
    echo -e "${RED}Cannot connect to gateway-1${NC}"
    exit 1
fi

echo -e "${GREEN}Both VMs accessible${NC}"

# Configure network to give gateway-1 internet access
if ! configure_network; then
    echo -e "${RED}Failed to configure network for gateway VM${NC}"
    echo "Proceeding with control VM only..."
    GATEWAY_SKIP=true
else
    echo -e "${GREEN}Network configuration successful${NC}"
    GATEWAY_SKIP=false
fi

scan_vm() {
    local vm_name="$1"
    local vm_results_dir="$RESULTS_DIR/$vm_name"
    
    echo -e "${YELLOW}=== Scanning $vm_name ===${NC}"
    
    echo "Uploading Trivy setup script to $vm_name..."
    if ! cat "$SCRIPT_PATH" | $HHFAB_BIN vlab ssh -n "$vm_name" -- 'cat > /tmp/trivy-setup.sh'; then
        echo -e "${RED}Failed to upload script to $vm_name${NC}"
        return 1
    fi

    echo "Setting up Trivy on $vm_name..."
    if ! $HHFAB_BIN vlab ssh -n "$vm_name" -- 'chmod +x /tmp/trivy-setup.sh && sudo /tmp/trivy-setup.sh'; then
        echo -e "${RED}Failed to setup Trivy on $vm_name${NC}"
        return 1
    fi

    echo "Running security scan on $vm_name..."
    if ! $HHFAB_BIN vlab ssh -n "$vm_name" -- 'sudo /var/lib/trivy/scan.sh'; then
        echo -e "${RED}Failed to run Trivy scan on $vm_name${NC}"
        return 1
    fi

    # Get list of unique images and store for GitHub Actions
    echo "Getting image list from $vm_name..."
    IMAGES=$($HHFAB_BIN vlab ssh -n "$vm_name" -- 'sudo crictl --runtime-endpoint unix:///run/k3s/containerd/containerd.sock images | grep -v IMAGE | grep -v pause | awk "{print \$1\":\"\$2}"' | sort -u || echo "")
    
    if [ ! -z "$IMAGES" ]; then
        # Convert to array
        readarray -t image_array <<< "$IMAGES"
        
        echo "=== Images found on $vm_name ==="
        printf '%s\n' "${image_array[@]}"
        echo "================================"
        
        image_count=${#image_array[@]}
        echo "Total images on $vm_name: $image_count"
        
        # Store image list and count for GitHub Actions
        printf '%s\n' "${image_array[@]}" > "$RESULTS_DIR/${vm_name}_images.txt"
        echo "$image_count" > "$RESULTS_DIR/${vm_name}_image_count.txt"
    else
        echo "No images found on $vm_name"
        echo "0" > "$RESULTS_DIR/${vm_name}_image_count.txt"
        touch "$RESULTS_DIR/${vm_name}_images.txt"  # Create empty file
    fi

    echo "Collecting scan results from $vm_name..."
    mkdir -p "$vm_results_dir"
    
    # Check if any results exist first
    if ! $HHFAB_BIN vlab ssh -n "$vm_name" -- 'sudo find /var/lib/trivy/reports -name "*.txt" -o -name "*.json" | head -1' >/dev/null 2>&1; then
        echo -e "${YELLOW}No scan results found on $vm_name${NC}"
        return 1
    fi
    
    if ! $HHFAB_BIN vlab ssh -n "$vm_name" -- 'sudo tar czf /tmp/trivy-reports.tar.gz -C /var/lib/trivy/reports . 2>/dev/null'; then
        echo -e "${YELLOW}Failed to create results archive on $vm_name${NC}"
        return 1
    fi

    if $HHFAB_BIN vlab ssh -n "$vm_name" -- 'test -s /tmp/trivy-reports.tar.gz'; then
        $HHFAB_BIN vlab ssh -n "$vm_name" -- 'cat /tmp/trivy-reports.tar.gz' > "$vm_results_dir/trivy-reports.tar.gz"
        # Extract in subshell to avoid changing working directory
        (cd "$vm_results_dir" && tar xzf trivy-reports.tar.gz && rm trivy-reports.tar.gz)
        echo -e "${GREEN}Results from $vm_name saved to: $vm_results_dir${NC}"
        
        echo -e "${YELLOW}$vm_name Scan Results:${NC}"
        find "$vm_results_dir" -name "*.txt" -exec echo "  - {}" \; 2>/dev/null || true
        find "$vm_results_dir" -name "*.json" -exec echo "  - {}" \; 2>/dev/null || true
        return 0
    else
        echo -e "${YELLOW}Results archive is empty on $vm_name${NC}"
        return 1
    fi
}

mkdir -p "$RESULTS_DIR"

echo -e "${YELLOW}Starting security scans...${NC}"

scan_vm "$CONTROL_VM"
CONTROL_RESULT=$?

# Scan gateway VM only if network configuration succeeded
if [ "$GATEWAY_SKIP" = false ]; then
    scan_vm "$GATEWAY_VM"
    GATEWAY_RESULT=$?
else
    echo -e "${YELLOW}Skipping gateway VM scan due to network configuration failure${NC}"
    GATEWAY_RESULT=1
fi

# Calculate final statistics for GitHub Actions
total_control_images=0
total_gateway_images=0
total_critical=0
total_high=0
total_medium=0
total_low=0

if [ -f "$RESULTS_DIR/control-1_image_count.txt" ]; then
    total_control_images=$(cat "$RESULTS_DIR/control-1_image_count.txt")
fi

if [ -f "$RESULTS_DIR/gateway-1_image_count.txt" ]; then
    total_gateway_images=$(cat "$RESULTS_DIR/gateway-1_image_count.txt")
fi

total_images=$((total_control_images + total_gateway_images))

# Calculate vulnerability statistics if jq is available
if command -v jq >/dev/null 2>&1; then
    for json_file in "$RESULTS_DIR"/*/20*_*_all.json; do
        if [ -f "$json_file" ]; then
            crit=$(jq '[.Results[]?.Vulnerabilities[]? | select(.Severity == "CRITICAL")] | length' "$json_file" 2>/dev/null || echo 0)
            high=$(jq '[.Results[]?.Vulnerabilities[]? | select(.Severity == "HIGH")] | length' "$json_file" 2>/dev/null || echo 0)
            medium=$(jq '[.Results[]?.Vulnerabilities[]? | select(.Severity == "MEDIUM")] | length' "$json_file" 2>/dev/null || echo 0)
            low=$(jq '[.Results[]?.Vulnerabilities[]? | select(.Severity == "LOW")] | length' "$json_file" 2>/dev/null || echo 0)
            
            total_critical=$((total_critical + crit))
            total_high=$((total_high + high))
            total_medium=$((total_medium + medium))
            total_low=$((total_low + low))
        fi
    done
    
    total_vulnerabilities=$((total_critical + total_high + total_medium + total_low))
    
    # Calculate unique CVEs
    unique_cves=0
    temp_cve_file=$(mktemp)
    for json_file in "$RESULTS_DIR"/*/20*_*_all.json; do
        if [ -f "$json_file" ]; then
            jq -r '.Results[]?.Vulnerabilities[]?.VulnerabilityID // "N/A"' "$json_file" 2>/dev/null | grep -v "^N/A$" >> "$temp_cve_file" || true
        fi
    done
    if [ -f "$temp_cve_file" ]; then
        unique_cves=$(sort -u "$temp_cve_file" | wc -l)
        rm -f "$temp_cve_file"
    fi
else
    total_vulnerabilities=0
    unique_cves=0
fi

# Export for GitHub Actions
if [ ! -z "${GITHUB_ENV:-}" ]; then
    echo "SCAN_CONTROL_IMAGES=$total_control_images" >> "$GITHUB_ENV"
    echo "SCAN_GATEWAY_IMAGES=$total_gateway_images" >> "$GITHUB_ENV"
    echo "SCAN_TOTAL_IMAGES=$total_images" >> "$GITHUB_ENV"
    echo "SCAN_CONTROL_SUCCESS=$([ $CONTROL_RESULT -eq 0 ] && echo "true" || echo "false")" >> "$GITHUB_ENV"
    echo "SCAN_GATEWAY_SUCCESS=$([ "$GATEWAY_SKIP" = false ] && [ $GATEWAY_RESULT -eq 0 ] && echo "true" || echo "false")" >> "$GITHUB_ENV"
    echo "SCAN_GATEWAY_SKIPPED=$([ "$GATEWAY_SKIP" = true ] && echo "true" || echo "false")" >> "$GITHUB_ENV"
    echo "TOTAL_VULNERABILITIES=$total_vulnerabilities" >> "$GITHUB_ENV"
    echo "CRITICAL_VULNERABILITIES=$total_critical" >> "$GITHUB_ENV"
    echo "HIGH_VULNERABILITIES=$total_high" >> "$GITHUB_ENV"
    echo "MEDIUM_VULNERABILITIES=$total_medium" >> "$GITHUB_ENV"
    echo "LOW_VULNERABILITIES=$total_low" >> "$GITHUB_ENV"
    echo "UNIQUE_CVES=$unique_cves" >> "$GITHUB_ENV"
fi

echo ""
echo -e "${GREEN}=== Security Scan Summary ===${NC}"

if [ $CONTROL_RESULT -eq 0 ]; then
    echo -e "${GREEN}Control VM ($CONTROL_VM): SUCCESS ($total_control_images images)${NC}"
else
    echo -e "${RED}Control VM ($CONTROL_VM): FAILED${NC}"
fi

if [ "$GATEWAY_SKIP" = false ]; then
    if [ $GATEWAY_RESULT -eq 0 ]; then
        echo -e "${GREEN}Gateway VM ($GATEWAY_VM): SUCCESS ($total_gateway_images images)${NC}"
    else
        echo -e "${RED}Gateway VM ($GATEWAY_VM): FAILED${NC}"
    fi
else
    echo -e "${YELLOW}Gateway VM ($GATEWAY_VM): SKIPPED (network config failed)${NC}"
fi

echo ""
echo -e "${GREEN}Scan Results Directory: $RESULTS_DIR${NC}"
echo "VLAB log: $VLAB_LOG"
echo "Total images scanned: $total_images"

# Display vulnerability summary
if command -v jq >/dev/null 2>&1 && [ "$total_vulnerabilities" -gt 0 ]; then
    echo ""
    echo -e "${YELLOW}Vulnerability Summary:${NC}"
    echo "  Critical: $total_critical"
    echo "  High: $total_high"
    echo "  Medium: $total_medium" 
    echo "  Low: $total_low"
    echo "  Total: $total_vulnerabilities"
    echo "  Unique CVEs: $unique_cves"
fi

echo ""

if [ $CONTROL_RESULT -eq 0 ] && ([ "$GATEWAY_SKIP" = true ] || [ $GATEWAY_RESULT -eq 0 ]); then
    echo -e "${GREEN}Security scan completed successfully${NC}"
    echo -e "${YELLOW}VLAB will auto-cleanup when script exits${NC}"
    exit 0
else
    echo -e "${RED}Security scan failed${NC}"
    exit 1
fi
