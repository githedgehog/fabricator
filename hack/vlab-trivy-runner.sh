#!/bin/bash
# Copyright 2025 Hedgehog
# SPDX-License-Identifier: Apache-2.0

set -e

CONTROL_VM="control-1"
GATEWAY_VM="gateway-1"
VLAB_LOG="vlab.log"
RESULTS_DIR="trivy-reports"
SCRIPT_PATH="${SCRIPT_PATH:-./hack/trivy-setup.sh}"
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

    echo "Generating consolidated SARIF file for GitHub Security on $vm_name..."
    echo "Creating single SARIF run with all images (GitHub's new requirement)"
    # Create sarif-reports directory if it doesn't exist
    mkdir -p sarif-reports
    
    # Get list of images
    echo "Getting image list for SARIF generation..."
    IMAGES=$($HHFAB_BIN vlab ssh -n "$vm_name" -- 'sudo crictl --runtime-endpoint unix:///run/k3s/containerd/containerd.sock images | grep -v IMAGE | grep -v pause | awk "{print \$1\":\"\$2}"' | sort -u || echo "")
    
    if [ ! -z "$IMAGES" ]; then
        # Convert to array
        readarray -t image_array <<< "$IMAGES"
        
        echo "=== Images found for consolidated SARIF ==="
        printf '%s\n' "${image_array[@]}"
        echo "============================================"
        
        image_count=${#image_array[@]}
        echo "Total images to include in single SARIF: $image_count"
        
        # Create consolidated SARIF by merging individual scans
        echo "Generating individual SARIF files for merging..."
        temp_sarifs=()
        success_count=0
        
        for i in "${!image_array[@]}"; do
            image="${image_array[$i]}"
            if [ ! -z "$image" ] && [ "$image" != ":" ]; then
                current=$((i + 1))
                safe_name=$(echo "${image}" | tr '/:' '_')
                echo "[$current/$image_count] Scanning: $image"
                
                # Generate individual SARIF
                if $HHFAB_BIN vlab ssh -n "$vm_name" -- "
                    sudo DOCKER_CONFIG=/var/lib/trivy/.docker /var/lib/trivy/trivy image \\
                        --insecure \\
                        --severity HIGH,CRITICAL \\
                        --format sarif \\
                        --output '/tmp/sarif_${safe_name}.sarif' \\
                        '$image'
                "; then
                    # Download individual SARIF
                    if $HHFAB_BIN vlab ssh -n "$vm_name" -- "cat '/tmp/sarif_${safe_name}.sarif'" > "/tmp/sarif_${safe_name}.sarif"; then
                        temp_sarifs+=("/tmp/sarif_${safe_name}.sarif")
                        success_count=$((success_count + 1))
                        echo "  ✓ SARIF generated for $image"
                    else
                        echo "  ✗ Failed to download SARIF for $image"
                    fi
                else
                    echo "  ✗ Failed to generate SARIF for $image"
                fi
            fi
        done
        
        # Merge all SARIF files into one consolidated file
        if [ $success_count -gt 0 ]; then
            echo "Merging $success_count SARIF files into consolidated report..."
            
            consolidated_sarif="sarif-reports/trivy-consolidated-${vm_name}.sarif"
            
            # Start with first SARIF as base
            if [ ${#temp_sarifs[@]} -gt 0 ]; then
                cp "${temp_sarifs[0]}" "$consolidated_sarif"
                
                # Merge remaining SARIF files if we have more than one
                if [ ${#temp_sarifs[@]} -gt 1 ]; then
                    echo "Merging multiple SARIF files using jq..."
                    
                    for ((i=1; i<${#temp_sarifs[@]}; i++)); do
                        merge_file="${temp_sarifs[$i]}"
                        if [ -f "$merge_file" ]; then
                            # Merge results and rules arrays using jq with deduplication
                            jq -s '
                                .[0].runs[0].results += .[1].runs[0].results |
                                .[0].runs[0].tool.driver.rules += (.[1].runs[0].tool.driver.rules // []) |
                                .[0].runs[0].tool.driver.rules |= unique_by(.id) |
                                .[0]
                            ' "$consolidated_sarif" "$merge_file" > "${consolidated_sarif}.tmp" && \
                            mv "${consolidated_sarif}.tmp" "$consolidated_sarif"
                            echo "  ✓ Merged: $(basename "$merge_file")"
                        fi
                    done
                fi
                
                echo "✓ Consolidated SARIF created: trivy-consolidated-${vm_name}.sarif"
                echo "✓ Contains vulnerabilities from $success_count/$image_count images"
                
                # === ENHANCED SARIF CONTEXT INTEGRATION WITH VM VISIBILITY ===
                echo "Enhancing SARIF with VM and container context..."
                
                # Get aggregated vulnerability counts from JSON reports
                total_critical=0
                total_high=0
                total_medium=0
                total_low=0
                
                if [ -d "$vm_results_dir" ]; then
                    for json_file in "$vm_results_dir"/*.json; do
                        if [ -f "$json_file" ]; then
                            critical=$(jq '[.Results[]?.Vulnerabilities[]? | select(.Severity == "CRITICAL")] | length' "$json_file" 2>/dev/null || echo 0)
                            high=$(jq '[.Results[]?.Vulnerabilities[]? | select(.Severity == "HIGH")] | length' "$json_file" 2>/dev/null || echo 0)
                            medium=$(jq '[.Results[]?.Vulnerabilities[]? | select(.Severity == "MEDIUM")] | length' "$json_file" 2>/dev/null || echo 0)
                            low=$(jq '[.Results[]?.Vulnerabilities[]? | select(.Severity == "LOW")] | length' "$json_file" 2>/dev/null || echo 0)
                            
                            total_critical=$((total_critical + critical))
                            total_high=$((total_high + high))
                            total_medium=$((total_medium + medium))
                            total_low=$((total_low + low))
                        fi
                    done
                fi
                
                # Build container images array for JSON
                containers_json="[]"
                if [ ${#image_array[@]} -gt 0 ]; then
                    containers_json=$(printf '%s\n' "${image_array[@]}" | jq -R . | jq -s .)
                fi
                
                # Get deployment context from environment
                deployment_id="${GITHUB_RUN_ID:-unknown}"
                commit_sha="${GITHUB_SHA:-unknown}"
                repo="${GITHUB_REPOSITORY:-unknown}"
                actor="${GITHUB_ACTOR:-unknown}"
                registry_repo="${HHFAB_REG_REPO:-127.0.0.1:30000}"
                
                # Enhance the consolidated SARIF with full context + VM visibility
                jq --arg vm_name "$vm_name" \
                   --arg scan_time "$(date -Iseconds)" \
                   --arg deployment_id "$deployment_id" \
                   --arg commit_sha "$commit_sha" \
                   --arg repo "$repo" \
                   --arg actor "$actor" \
                   --arg registry_repo "$registry_repo" \
                   --arg total_critical "$total_critical" \
                   --arg total_high "$total_high" \
                   --arg total_medium "$total_medium" \
                   --arg total_low "$total_low" \
                   --argjson container_images "$containers_json" \
                   '.runs[0].properties = {
                     vmContext: {
                       name: $vm_name,
                       type: (if ($vm_name | startswith("control")) then "control" elif ($vm_name | startswith("gateway")) then "gateway" else "unknown" end),
                       scanTimestamp: $scan_time,
                       environment: "vlab",
                       totalContainerImages: ($container_images | length)
                     },
                     containerContext: {
                       scannedImages: $container_images,
                       registry: $registry_repo,
                       aggregatedVulnerabilities: {
                         critical: ($total_critical | tonumber),
                         high: ($total_high | tonumber),
                         medium: ($total_medium | tonumber),
                         low: ($total_low | tonumber),
                         total: (($total_critical | tonumber) + ($total_high | tonumber) + ($total_medium | tonumber) + ($total_low | tonumber))
                       }
                     },
                     deploymentContext: {
                       deploymentId: $deployment_id,
                       commitSha: $commit_sha,
                       repository: $repo,
                       triggeredBy: $actor,
                       workflowRun: ("https://github.com/" + $repo + "/actions/runs/" + $deployment_id)
                     },
                     scanMetadata: {
                       tool: "trivy",
                       category: "vm-container-runtime-scan",
                       scanScope: "production-deployment",
                       consolidatedReport: true,
                       imageCount: ($container_images | length)
                     }
                   } |
                   .runs[0].tool.driver.informationUri = ("https://github.com/" + $repo + "/security") |
                   # ENHANCED: Add VM context to artifact URIs for GitHub UI visibility
                   .runs[0].results[].locations[].physicalLocation.artifactLocation.uri |= 
                     ($vm_name + "/" + .) |
                   # ENHANCED: Add VM context to location messages for GitHub UI visibility  
                   .runs[0].results[].locations[].message.text |= 
                     ("[" + $vm_name + "] " + .) |
                   # Add VM context to each vulnerability result (existing functionality)
                   .runs[0].results[] |= . + {
                     properties: {
                       vmName: $vm_name,
                       vmType: (if ($vm_name | startswith("control")) then "control" elif ($vm_name | startswith("gateway")) then "gateway" else "unknown" end),
                       scanContext: "runtime-deployment-consolidated"
                     }
                   }' "$consolidated_sarif" > "${consolidated_sarif}.enhanced"
                
                # Replace original with enhanced version
                mv "${consolidated_sarif}.enhanced" "$consolidated_sarif"
                echo "✓ Enhanced SARIF with VM and container context"
                echo "✓ Added VM visibility to artifact URIs and messages"
                echo "  - VM: $vm_name"
                echo "  - Container images: ${#image_array[@]}"
                echo "  - Total vulnerabilities: $((total_critical + total_high + total_medium + total_low))"
                echo "  - Critical/High: $((total_critical + total_high))"
                # === END ENHANCED SARIF CONTEXT INTEGRATION ===
                
            else
                echo "✗ No valid SARIF files to consolidate"
            fi
            
            # Clean up temporary files
            for temp_file in "${temp_sarifs[@]}"; do
                rm -f "$temp_file"
            done
        else
            echo "✗ No SARIF files generated successfully"
        fi
        
        echo "SARIF generation complete: 1 consolidated file with $success_count images"
    else
        echo "No images found for SARIF generation on $vm_name"
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

# Create SARIF directory (simplified - no fallback)
mkdir -p sarif-reports

echo ""
echo -e "${GREEN}=== Security Scan Summary ===${NC}"
if [ $CONTROL_RESULT -eq 0 ]; then
    echo -e "${GREEN}Control VM ($CONTROL_VM): SUCCESS${NC}"
else
    echo -e "${RED}Control VM ($CONTROL_VM): FAILED${NC}"
fi

if [ "$GATEWAY_SKIP" = false ]; then
    if [ $GATEWAY_RESULT -eq 0 ]; then
        echo -e "${GREEN}Gateway VM ($GATEWAY_VM): SUCCESS${NC}"
    else
        echo -e "${RED}Gateway VM ($GATEWAY_VM): FAILED${NC}"
    fi
else
    echo -e "${YELLOW}Gateway VM ($GATEWAY_VM): SKIPPED (network config failed)${NC}"
fi

# Show SARIF generation results
sarif_count=$(find sarif-reports -name "*.sarif" -type f 2>/dev/null | wc -l)
echo ""
echo "Results directory: $RESULTS_DIR"
echo "SARIF files generated: $sarif_count"
echo "VLAB log: $VLAB_LOG"
echo ""

if [ $CONTROL_RESULT -eq 0 ] && ([ "$GATEWAY_SKIP" = true ] || [ $GATEWAY_RESULT -eq 0 ]); then
    echo -e "${GREEN}Security scan completed successfully${NC}"
    echo -e "${YELLOW}VLAB will auto-cleanup when script exits${NC}"
    exit 0
else
    echo -e "${RED}Security scan failed${NC}"
    exit 1
fi
