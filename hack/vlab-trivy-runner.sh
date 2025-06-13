#!/bin/bash
# Copyright 2025 Hedgehog
# SPDX-License-Identifier: Apache-2.0

set -e

# Parse command line arguments
RUN_CONTROL=true
RUN_GATEWAY=true
SKIP_VLAB_LAUNCH=false
ALLOW_PARTIAL_SUCCESS=true

while [[ $# -gt 0 ]]; do
    case $1 in
        --control-only)
            RUN_CONTROL=true
            RUN_GATEWAY=false
            shift
            ;;
        --gateway-only)
            RUN_CONTROL=false
            RUN_GATEWAY=true
            shift
            ;;
        --skip-vlab)
            SKIP_VLAB_LAUNCH=true
            shift
            ;;
        --strict)
            ALLOW_PARTIAL_SUCCESS=false
            shift
            ;;
        --help|-h)
            echo "Usage: $0 [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  --control-only     Run only control VM setup and scanning"
            echo "  --gateway-only     Run only gateway VM setup and scanning"
            echo "  --skip-vlab        Skip launching VLAB (assumes VLAB is already running)"
            echo "  --strict           Require all scans to succeed (no partial successes)"
            echo "  --help, -h         Show this help message"
            echo ""
            echo "Default: Run both control and gateway VMs with VLAB launch"
            exit 0
            ;;
        *)
            echo "Unknown option: $1"
            echo "Use --help for usage information"
            exit 1
            ;;
    esac
done

# Validate that at least one VM is enabled
if [ "$RUN_CONTROL" = false ] && [ "$RUN_GATEWAY" = false ]; then
    echo -e "${RED}ERROR: No VMs enabled. Use --help for usage information.${NC}"
    exit 1
fi

CONTROL_VM="control-1"
GATEWAY_VM="gateway-1"
VLAB_LOG="vlab.log"
RESULTS_DIR="trivy-reports"
SCRIPT_PATH="${SCRIPT_PATH:-./hack/trivy-setup.sh}"
AIRGAPPED_SCRIPT_PATH="${AIRGAPPED_SCRIPT_PATH:-./hack/trivy-setup-airgapped.sh}"
VLAB_TIMEOUT=${VLAB_TIMEOUT:-25}

# Variables to track vulnerability counts
CONTROL_HIGH_VULNS=0
CONTROL_CRITICAL_VULNS=0
GATEWAY_HIGH_VULNS=0
GATEWAY_CRITICAL_VULNS=0
CONTROL_IMAGES_SCANNED=0
GATEWAY_IMAGES_SCANNED=0

# Find hhfab binary relative to project root
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
    echo "Current directory: $(pwd)"
    echo "Searched paths: $ORIGINAL_DIR/hhfab, $ORIGINAL_DIR/bin/hhfab, $(dirname "$ORIGINAL_DIR")/hhfab, $(dirname "$ORIGINAL_DIR")/bin/hhfab"
    exit 1
fi

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

cleanup() {
    echo -e "${YELLOW}Cleaning up...${NC}"

    if [ "$SKIP_VLAB_LAUNCH" = false ] && [ ! -z "$VLAB_PID" ] && kill -0 $VLAB_PID 2>/dev/null; then
        echo "Terminating VLAB process (PID: $VLAB_PID)..."
        kill $VLAB_PID || true
        wait $VLAB_PID || true
        echo "VLAB process terminated"
    fi
}

trap cleanup EXIT INT TERM

# Check required scripts exist
if [ "$RUN_CONTROL" = true ] && [ ! -f "$SCRIPT_PATH" ]; then
    echo -e "${RED}ERROR: Trivy setup script not found at: $SCRIPT_PATH${NC}"
    echo "Please ensure trivy-setup.sh exists or set SCRIPT_PATH correctly"
    exit 1
fi

if [ "$RUN_GATEWAY" = true ] && [ ! -f "$AIRGAPPED_SCRIPT_PATH" ]; then
    echo -e "${RED}ERROR: Airgapped setup script not found at: $AIRGAPPED_SCRIPT_PATH${NC}"
    echo "Please ensure trivy-setup-airgapped.sh exists or set AIRGAPPED_SCRIPT_PATH correctly"
    exit 1
fi

echo -e "${GREEN}Starting VLAB Trivy Scanner${NC}"
echo "Control VM: $CONTROL_VM $([ "$RUN_CONTROL" = true ] && echo "(enabled)" || echo "(disabled)")"
echo "Gateway VM: $GATEWAY_VM $([ "$RUN_GATEWAY" = true ] && echo "(enabled)" || echo "(disabled)")"
echo "Skip VLAB launch: $([ "$SKIP_VLAB_LAUNCH" = true ] && echo "Yes (using external VLAB)" || echo "No")"
echo "Allow partial success: $([ "$ALLOW_PARTIAL_SUCCESS" = true ] && echo "Yes" || echo "No (strict mode)")"
echo "hhfab binary: $HHFAB_BIN"
echo "Control script: $SCRIPT_PATH"
echo "Gateway script: $AIRGAPPED_SCRIPT_PATH (airgapped mode)"
echo "Results: $RESULTS_DIR"
echo "Log: $VLAB_LOG"
if [ "$SKIP_VLAB_LAUNCH" = false ]; then
    echo "Timeouts: VLAB=${VLAB_TIMEOUT}m"
fi
echo ""

# Launch VLAB if not skipped
if [ "$SKIP_VLAB_LAUNCH" = false ]; then
    if [ ! -f "fab.yaml" ]; then
        echo -e "${YELLOW}Initializing VLAB (control + gateway)...${NC}"
        $HHFAB_BIN init -v --dev --gateway
    fi

    echo -e "${YELLOW}Generating join token for gateway node...${NC}"
    export HHFAB_JOIN_TOKEN=$(openssl rand -base64 24)
    echo "Join token generated: ${HHFAB_JOIN_TOKEN:0:8}..."

    echo -e "${YELLOW}Starting VLAB...${NC}"
    timeout ${VLAB_TIMEOUT}m $HHFAB_BIN vlab up --controls-restricted=false > "$VLAB_LOG" 2>&1 &
    VLAB_PID=$!
    echo "VLAB PID: $VLAB_PID"

    echo -e "${YELLOW}Waiting for VLAB to be ready...${NC}"
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

    echo -e "${YELLOW}Waiting for SSH services to be ready...${NC}"
    sleep 30
else
    echo -e "${YELLOW}Skipping VLAB launch (assuming VLAB is already running)${NC}"
    echo -e "${YELLOW}Will verify VLAB is running through SSH connectivity tests${NC}"
fi

# Test SSH connectivity with retries
echo -e "${YELLOW}Testing SSH connectivity...${NC}"

# Function to test SSH with retries
test_ssh_with_retry() {
    local vm_name="$1"
    local max_attempts=10
    local attempt=1
    
    while [ $attempt -le $max_attempts ]; do
        echo "Attempt $attempt/$max_attempts: Testing SSH to $vm_name..."
        if $HHFAB_BIN vlab ssh -b -n "$vm_name" -- 'echo "SSH works"' >/dev/null 2>&1; then
            echo -e "${GREEN}SSH to $vm_name: SUCCESS${NC}"
            return 0
        fi
        echo "SSH to $vm_name failed, waiting 10 seconds..."
        sleep 10
        attempt=$((attempt + 1))
    done

    echo -e "${RED}SSH to $vm_name: FAILED after $max_attempts attempts${NC}"
    return 1
}

# Test both VMs with retries
if [ "$RUN_CONTROL" = true ]; then
    if ! test_ssh_with_retry "$CONTROL_VM"; then
        echo -e "${RED}Cannot connect to $CONTROL_VM after multiple attempts${NC}"
        echo "Debug: Checking VLAB status..."
        $HHFAB_BIN vlab status || true
        exit 1
    fi
fi

if [ "$RUN_GATEWAY" = true ]; then
    if ! test_ssh_with_retry "$GATEWAY_VM"; then
        echo -e "${RED}Cannot connect to $GATEWAY_VM after multiple attempts${NC}"
        echo "Debug: Checking VLAB status..."
        $HHFAB_BIN vlab status || true
        exit 1
    fi
fi

echo -e "${GREEN}All enabled VMs accessible via SSH${NC}"

# Function to setup Trivy on Control VM (online setup)
setup_control_vm() {
    echo -e "${YELLOW}=== Setting up Control VM (online) ===${NC}"

    echo "Uploading Trivy setup script to Control VM..."
    if ! cat "$SCRIPT_PATH" | $HHFAB_BIN vlab ssh -b -n "$CONTROL_VM" -- 'cat > /tmp/trivy-setup.sh'; then
        echo -e "${RED}Failed to upload script to Control VM${NC}"
        return 1
    fi

    echo "Installing Trivy on Control VM (online mode)..."
    if ! $HHFAB_BIN vlab ssh -b -n "$CONTROL_VM" -- 'chmod +x /tmp/trivy-setup.sh && sudo /tmp/trivy-setup.sh'; then
        echo -e "${RED}Failed to setup Trivy on Control VM${NC}"
        return 1
    fi

    echo -e "${GREEN}Control VM setup complete (online)${NC}"
    return 0
}

# Function to setup Gateway VM (airgapped)
setup_gateway_vm() {
    echo -e "${YELLOW}=== Setting up Gateway VM (airgap) ===${NC}"

    echo "Running airgapped setup script..."
    if ! HHFAB_BIN="$HHFAB_BIN" "$AIRGAPPED_SCRIPT_PATH"; then
        echo -e "${RED}Failed to setup Trivy on Gateway VM in airgapped mode${NC}"
        return 1
    fi

    echo -e "${GREEN}Gateway VM setup complete (airgap)${NC}"
    return 0
}

echo -e "${YELLOW}Setting up VMs...${NC}"

# Setup Control VM (online mode)
if [ "$RUN_CONTROL" = true ]; then
    setup_control_vm
    CONTROL_SETUP=$?
else
    echo "Skipping Control VM setup (disabled)"
    CONTROL_SETUP=0
fi

# Setup Gateway VM (airgapped mode)
if [ "$RUN_GATEWAY" = true ]; then
    setup_gateway_vm
    GATEWAY_SETUP=$?
else
    echo "Skipping Gateway VM setup (disabled)"
    GATEWAY_SETUP=0
fi

if [ $CONTROL_SETUP -ne 0 ] || [ $GATEWAY_SETUP -ne 0 ]; then
    echo -e "${RED}Failed to setup Trivy on one or more VMs${NC}"
    exit 1
fi

echo -e "${GREEN}All enabled VMs setup complete${NC}"

# Function to scan VM with native SARIF output
scan_vm() {
    local vm_name="$1"
    local vm_results_dir="$RESULTS_DIR/$vm_name"
    local scan_errors=0
    local local_high_vulns=0
    local local_critical_vulns=0
    local local_images_scanned=0

    echo -e "${YELLOW}=== Scanning $vm_name ===${NC}"

    mkdir -p sarif-reports
    local scan_errors=0

    if [ "$vm_name" = "$GATEWAY_VM" ]; then
        echo "Running airgapped security scan on $vm_name..."
        if ! $HHFAB_BIN vlab ssh -b -n "$vm_name" -- 'sudo /var/lib/trivy/scan-airgapped.sh'; then
            echo -e "${RED}Failed to run airgapped scan on $vm_name${NC}"
            return 1
        fi
    else
        echo "Running online security scan on $vm_name..."
        if ! $HHFAB_BIN vlab ssh -b -n "$vm_name" -- 'sudo /var/lib/trivy/scan.sh'; then
            echo -e "${RED}Failed to run Trivy scan on $vm_name${NC}"
            return 1
        fi
    fi

    echo "Getting image list for SARIF generation..."
    IMAGES=$($HHFAB_BIN vlab ssh -b -n "$vm_name" -- 'sudo crictl --runtime-endpoint unix:///run/k3s/containerd/containerd.sock images | grep -v IMAGE | grep -v pause | awk "{print \$1\":\"\$2}"' | sort -u || echo "")

    readarray -t image_array <<< "$IMAGES"
    local image_count=${#image_array[@]}
    local_images_scanned=$image_count

    if [ $image_count -eq 0 ]; then
        echo "No images found for scanning on $vm_name"
        return 1
    fi

    echo "=== Images found for scanning ==="
    printf '%s\n' "${image_array[@]}"
    echo "==============================="

    local scan_mode="online"
    local registry="172.30.0.1:31000"
    if [ "$vm_name" = "$GATEWAY_VM" ]; then
        scan_mode="airgapped"
    fi

    echo "Generating SARIF files for each image..."
    temp_sarifs=()
    success_count=0

    for i in "${!image_array[@]}"; do
        image="${image_array[$i]}"
        if [ ! -z "$image" ] && [ "$image" != ":" ]; then
            current=$((i + 1))
            safe_name=$(echo "${image}" | tr '/:' '_')
            echo "[$current/$image_count] Processing: $image"

            if [ "$vm_name" = "$GATEWAY_VM" ]; then
                echo "Generating SARIF from existing JSON scan results..."

                if $HHFAB_BIN vlab ssh -b -n "$vm_name" -- "
                    # Find existing JSON result for this image
                    JSON_FILE=\$(find /var/lib/trivy/reports -name \"*_$(echo $image | tr '/:' '_')_all.json\" | head -1) && \\
                    if [ -f \"\$JSON_FILE\" ]; then \\
                        echo \"Found existing scan result: \$JSON_FILE\" && \\
                        # Export JSON to tarball
                        sudo cat \"\$JSON_FILE\" > /tmp/scan_result.json && \\
                        # Use jq to convert JSON to SARIF - ORIGINAL with minimal fixes
                        sudo jq --arg image_name \"$image\" '{
                            \"\$schema\": \"https://raw.githubusercontent.com/oasis-tcs/sarif-spec/master/Schemata/sarif-schema-2.1.0.json\",
                            \"version\": \"2.1.0\",
                            \"runs\": [{
                                \"tool\": {
                                    \"driver\": {
                                        \"name\": \"Trivy\",
                                        \"informationUri\": \"https://github.com/aquasecurity/trivy\",
                                        \"rules\": [
                                            (if has(\"Results\") and (.Results | length > 0) then (.Results[]?.Vulnerabilities[]? // []) else [] end) | 
                                            {
                                                \"id\": .VulnerabilityID,
                                                \"name\": .VulnerabilityID,
                                                \"shortDescription\": {
                                                    \"text\": \"Vulnerability \\(.VulnerabilityID) in \\(.PkgName)\"
                                                },
                                                \"fullDescription\": {
                                                    \"text\": \"\\(.Title) - \\(.VulnerabilityID) (\\(.Severity))\"
                                                },
                                                \"help\": {
                                                    \"text\": \"\\(.Description)\",
                                                    \"markdown\": \"\\(.Description)\"
                                                },
                                                \"properties\": {
                                                    \"tags\": [
                                                        \"security\",
                                                        \"vulnerability\",
                                                        .PkgName,
                                                        .VulnerabilityID,
                                                        .Severity | ascii_downcase
                                                    ],
                                                    \"security-severity\": (
                                                        if .CVSS.score then
                                                            .CVSS.score | tostring
                                                        else
                                                            if .Severity == \"CRITICAL\" then \"9.5\"
                                                            elif .Severity == \"HIGH\" then \"8.0\"
                                                            elif .Severity == \"MEDIUM\" then \"5.5\"
                                                            elif .Severity == \"LOW\" then \"2.0\"
                                                            else \"0.0\"
                                                            end
                                                        end
                                                    )
                                                },
                                                \"defaultConfiguration\": {
                                                    \"level\": (
                                                        if .Severity == \"CRITICAL\" or .Severity == \"HIGH\" then \"error\"
                                                        elif .Severity == \"MEDIUM\" then \"warning\"
                                                        else \"note\"
                                                        end
                                                    )
                                                }
                                            }
                                        ] | unique_by(.id),
                                        \"version\": \"1.0.0\"
                                    }
                                },
                                \"results\": [
                                    (if has(\"Results\") and (.Results | length > 0) then (.Results[]?.Vulnerabilities[]? // []) else [] end) | 
                                    {
                                        \"ruleId\": .VulnerabilityID,
                                        \"level\": (
                                            if .Severity == \"CRITICAL\" or .Severity == \"HIGH\" then \"error\"
                                            elif .Severity == \"MEDIUM\" then \"warning\"
                                            else \"note\"
                                            end
                                        ),
                                        \"message\": {
                                            \"text\": \"Vulnerability \\(.VulnerabilityID) in \\(.PkgName) - Severity: \\(.Severity)\"
                                        },
                                        \"locations\": [{
                                            \"physicalLocation\": {
                                                \"artifactLocation\": {
                                                    \"uri\": (\$image_name + \"/\" + .PkgName)
                                                },
                                                \"region\": {
                                                    \"startLine\": 1,
                                                    \"endLine\": 1,
                                                    \"startColumn\": 1,
                                                    \"endColumn\": 1
                                                }
                                            },
                                            \"message\": {
                                                \"text\": \"Package: \\(.PkgName) - Version: \\(.InstalledVersion)\"
                                            }
                                        }]
                                    }
                                ],
                                \"properties\": {
                                    \"imageScanned\": \$image_name,
                                    \"vulnerabilitiesFound\": ((if has(\"Results\") and (.Results | length > 0) then .Results[0].Vulnerabilities else [] end) | length),
                                    \"scanTimestamp\": (.CreatedAt // \"unknown\"),
                                    \"trivySchemaVersion\": (.SchemaVersion // 0),
                                    \"scanSource\": \"json-converted\"
                                }
                            }]
                        }' /tmp/scan_result.json > '/tmp/sarif_${safe_name}.sarif' && \\
                        echo \"SARIF file generated successfully from JSON\"; \\
                    else \\
                        echo \"No existing scan result found for ${image}\" && exit 1; \\
                    fi
                "; then
                    if $HHFAB_BIN vlab ssh -b -n "$vm_name" -- "test -f '/tmp/sarif_${safe_name}.sarif' && cat '/tmp/sarif_${safe_name}.sarif'" > "/tmp/sarif_${safe_name}.sarif"; then
                        temp_sarifs+=("/tmp/sarif_${safe_name}.sarif")
                        success_count=$((success_count + 1))
                        echo "  ✓ SARIF generated for $image"
                    else
                        echo "  ✗ Failed to download SARIF for $image"

                        echo "  Attempting fallback direct scan..."
                        if $HHFAB_BIN vlab ssh -b -n "$vm_name" -- "
                            if [[ \"$image\" == *\"$registry\"* ]]; then \\
                                sudo DOCKER_CONFIG=/var/lib/trivy/.docker /var/lib/trivy/trivy image \\
                                    --skip-db-update \\
                                    --cache-dir /var/lib/trivy/cache \\
                                    --severity HIGH,CRITICAL \\
                                    --format sarif \\
                                    --output '/tmp/sarif_${safe_name}.sarif' \\
                                    --insecure \\
                                    '$image'; \\
                            else \\
                                echo \"Skipping fallback for non-private image\" && exit 1; \\
                            fi
                        " && $HHFAB_BIN vlab ssh -b -n "$vm_name" -- "test -f '/tmp/sarif_${safe_name}.sarif' && cat '/tmp/sarif_${safe_name}.sarif'" > "/tmp/sarif_${safe_name}.sarif"; then
                            temp_sarifs+=("/tmp/sarif_${safe_name}.sarif")
                            success_count=$((success_count + 1))
                            echo "  ✓ SARIF generated using fallback method"
                        else
                            echo "  ✗ Fallback method also failed"
                            scan_errors=$((scan_errors + 1))
                        fi
                    fi
                else
                    echo "  ✗ Failed to generate SARIF for $image"
                    scan_errors=$((scan_errors + 1))

                    if [[ "$image" == *"$registry"* ]]; then
                        echo "  Attempting fallback direct scan..."
                        if $HHFAB_BIN vlab ssh -b -n "$vm_name" -- "
                            sudo DOCKER_CONFIG=/var/lib/trivy/.docker /var/lib/trivy/trivy image \\
                                --skip-db-update \\
                                --cache-dir /var/lib/trivy/cache \\
                                --severity HIGH,CRITICAL \\
                                --format sarif \\
                                --output '/tmp/sarif_${safe_name}.sarif' \\
                                --insecure \\
                                '$image'
                        " && $HHFAB_BIN vlab ssh -b -n "$vm_name" -- "test -f '/tmp/sarif_${safe_name}.sarif' && cat '/tmp/sarif_${safe_name}.sarif'" > "/tmp/sarif_${safe_name}.sarif"; then
                            temp_sarifs+=("/tmp/sarif_${safe_name}.sarif")
                            success_count=$((success_count + 1))
                            echo "  ✓ SARIF generated using fallback method"
                        else
                            echo "  ✗ Fallback method also failed"
                            scan_errors=$((scan_errors + 1))
                        fi
                    fi
                fi
            else
                echo "Generating SARIF for image on $vm_name (online mode)..."
                if $HHFAB_BIN vlab ssh -b -n "$vm_name" -- "
                    sudo DOCKER_CONFIG=/var/lib/trivy/.docker /var/lib/trivy/trivy image \\
                        --insecure \\
                        --severity HIGH,CRITICAL \\
                        --format sarif \\
                        --output '/tmp/sarif_${safe_name}.sarif' \\
                        '$image'
                "; then
                    if $HHFAB_BIN vlab ssh -n "$vm_name" -- "test -f '/tmp/sarif_${safe_name}.sarif' && cat '/tmp/sarif_${safe_name}.sarif'" > "/tmp/sarif_${safe_name}.sarif"; then
                        temp_sarifs+=("/tmp/sarif_${safe_name}.sarif")
                        success_count=$((success_count + 1))
                        echo "  ✓ SARIF generated for $image"
                    else
                        echo "  ✗ Failed to download SARIF for $image"
                        scan_errors=$((scan_errors + 1))
                    fi
                else
                    echo "  ✗ Failed to generate SARIF for $image"
                    scan_errors=$((scan_errors + 1))
                fi
            fi
        fi
    done

    if [ $success_count -gt 0 ]; then
        echo "Merging $success_count SARIF files into consolidated report..."

        consolidated_sarif="sarif-reports/trivy-consolidated-${vm_name}.sarif"

        if [ ${#temp_sarifs[@]} -gt 0 ]; then
            cp "${temp_sarifs[0]}" "$consolidated_sarif"

            if [ ${#temp_sarifs[@]} -gt 1 ]; then
                echo "Merging multiple SARIF files using jq..."

                for ((i=1; i<${#temp_sarifs[@]}; i++)); do
                    merge_file="${temp_sarifs[$i]}"
                    if [ -f "$merge_file" ]; then
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

            echo "Adding VM context information..."

            total_critical=$(jq '[.runs[0].results[]? | select(.level == "error" and (.message.text | contains("CRITICAL")))? | select(. != null)] | length' "$consolidated_sarif" 2>/dev/null || echo 0)
            total_high=$(jq '[.runs[0].results[]? | select(.level == "error" and (.message.text | contains("HIGH")))? | select(. != null)] | length' "$consolidated_sarif" 2>/dev/null || echo 0)
            total_medium=$(jq '[.runs[0].results[]? | select(.level == "warning")? | select(. != null)] | length' "$consolidated_sarif" 2>/dev/null || echo 0)
            total_low=$(jq '[.runs[0].results[]? | select(.level == "note")? | select(. != null)] | length' "$consolidated_sarif" 2>/dev/null || echo 0)

            local_critical_vulns=$total_critical
            local_high_vulns=$total_high

            containers_json="[]"
            if [ $image_count -gt 0 ]; then
                containers_json=$(printf '%s\n' "${image_array[@]}" | jq -R . | jq -s .)
            fi

            deployment_id="${GITHUB_RUN_ID:-unknown}"
            commit_sha="${GITHUB_SHA:-unknown}"
            repo="${GITHUB_REPOSITORY:-unknown}"
            actor="${GITHUB_ACTOR:-unknown}"
            registry_repo="${HHFAB_REG_REPO:-127.0.0.1:30000}"

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
               --arg scan_mode "$scan_mode" \
               --argjson container_images "$containers_json" \
               '.runs[0].properties = {
                 vmContext: {
                   name: $vm_name,
                   type: (if ($vm_name | startswith("control")) then "control" elif ($vm_name | startswith("gateway")) then "gateway" else "unknown" end),
                   scanTimestamp: $scan_time,
                   environment: "vlab",
                   scanMode: $scan_mode,
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
                   category: ("vm-container-runtime-scan-" + $scan_mode),
                   scanScope: "production-deployment",
                   consolidatedReport: true,
                   imageCount: ($container_images | length)
                 }
               } |
               .runs[0].tool.driver.informationUri = ("https://github.com/" + $repo + "/security") |
               .runs[0].results[].locations[].physicalLocation.artifactLocation.uri |=
                 ($vm_name + "/" + .) |
               .runs[0].results[].locations[].message.text |=
                 ("[" + $vm_name + "] " + .) |
               .runs[0].results[] |= . + {
                 properties: {
                   vmName: $vm_name,
                   vmType: (if ($vm_name | startswith("control")) then "control" elif ($vm_name | startswith("gateway")) then "gateway" else "unknown" end),
                   scanContext: ("runtime-deployment-" + $scan_mode)
                 }
               }' "$consolidated_sarif" > "${consolidated_sarif}.enhanced"

            mv "${consolidated_sarif}.enhanced" "$consolidated_sarif"

            echo "Enhanced SARIF with VM context"
            echo "  - VM: $vm_name ($scan_mode mode)"
            echo "  - Container images: $image_count"
            echo "  - Total vulnerabilities: $((total_critical + total_high + total_medium + total_low))"
            echo "  - Critical/High: $((total_critical + total_high))"
        else
            echo "✗ No valid SARIF files to consolidate"
        fi

        for temp_file in "${temp_sarifs[@]}"; do
            rm -f "$temp_file"
        done
    else
        echo "✗ No SARIF files generated successfully"
        scan_errors=$((scan_errors + 1))
    fi

    echo "Collecting scan results from $vm_name..."
    mkdir -p "$vm_results_dir"

    if ! $HHFAB_BIN vlab ssh -b -n "$vm_name" -- 'sudo find /var/lib/trivy/reports -name "*.txt" -o -name "*.json" | head -1' >/dev/null 2>&1; then
        echo -e "${YELLOW}No scan results found on $vm_name${NC}"
        return 1
    fi

    if ! $HHFAB_BIN vlab ssh -b -n "$vm_name" -- 'sudo tar czf /tmp/trivy-reports.tar.gz -C /var/lib/trivy/reports . 2>/dev/null'; then
        echo -e "${YELLOW}Failed to create results archive on $vm_name${NC}"
        return 1
    fi

    if $HHFAB_BIN vlab ssh -b -n "$vm_name" -- 'test -s /tmp/trivy-reports.tar.gz'; then
        $HHFAB_BIN vlab ssh -b -n "$vm_name" -- 'cat /tmp/trivy-reports.tar.gz' > "$vm_results_dir/trivy-reports.tar.gz"
        (cd "$vm_results_dir" && tar xzf trivy-reports.tar.gz && rm trivy-reports.tar.gz)
        echo -e "${GREEN}Results from $vm_name saved to: $vm_results_dir${NC}"
    else
        echo -e "${YELLOW}Results archive is empty on $vm_name${NC}"
        scan_errors=$((scan_errors + 1))
        return 1
    fi

    if [ "$vm_name" = "$GATEWAY_VM" ]; then
        GATEWAY_HIGH_VULNS=$local_high_vulns
        GATEWAY_CRITICAL_VULNS=$local_critical_vulns
        GATEWAY_IMAGES_SCANNED=$local_images_scanned
    else
        CONTROL_HIGH_VULNS=$local_high_vulns
        CONTROL_CRITICAL_VULNS=$local_critical_vulns
        CONTROL_IMAGES_SCANNED=$local_images_scanned
    fi

    if [ $success_count -gt 0 ]; then
        if [ $scan_errors -eq 0 ]; then
            echo -e "${GREEN}All scans for $vm_name completed successfully${NC}"
        else
            echo -e "${YELLOW}$vm_name scans completed with $scan_errors errors, but $success_count successful scans were processed${NC}"
        fi
        return 0
    else
        echo -e "${RED}$vm_name scans failed completely with $scan_errors errors${NC}"
        return 1
    fi
}

mkdir -p "$RESULTS_DIR"

echo -e "${YELLOW}Starting security scans...${NC}"

if [ "$RUN_CONTROL" = true ]; then
    scan_vm "$CONTROL_VM"
    CONTROL_RESULT=$?
else
    echo "Skipping Control VM scan (disabled)"
    CONTROL_RESULT=0
fi

if [ "$RUN_GATEWAY" = true ]; then
    scan_vm "$GATEWAY_VM"
    GATEWAY_RESULT=$?
else
    echo "Skipping Gateway VM scan (disabled)"
    GATEWAY_RESULT=0
fi

TOTAL_IMAGES_SCANNED=$((CONTROL_IMAGES_SCANNED + GATEWAY_IMAGES_SCANNED))
TOTAL_CRITICAL_VULNS=$((CONTROL_CRITICAL_VULNS + GATEWAY_CRITICAL_VULNS))
TOTAL_HIGH_VULNS=$((CONTROL_HIGH_VULNS + GATEWAY_HIGH_VULNS))
TOTAL_CRITICAL_HIGH=$((TOTAL_CRITICAL_VULNS + TOTAL_HIGH_VULNS))

echo ""
echo -e "${GREEN}=== Security Scan Summary ===${NC}"

if [ "$RUN_CONTROL" = true ]; then
    if [ $CONTROL_RESULT -eq 0 ]; then
        echo -e "${GREEN}Control VM ($CONTROL_VM): SUCCESS (online)${NC}"
        echo -e "  - Images scanned: $CONTROL_IMAGES_SCANNED"
        echo -e "  - Critical vulnerabilities: $CONTROL_CRITICAL_VULNS"
        echo -e "  - High vulnerabilities: $CONTROL_HIGH_VULNS"
    else
        echo -e "${RED}Control VM ($CONTROL_VM): FAILED${NC}"
    fi
else
    echo -e "${YELLOW}Control VM ($CONTROL_VM): SKIPPED${NC}"
fi

if [ "$RUN_GATEWAY" = true ]; then
    if [ $GATEWAY_RESULT -eq 0 ]; then
        echo -e "${GREEN}Gateway VM ($GATEWAY_VM): SUCCESS (airgap)${NC}"
        echo -e "  - Images scanned: $GATEWAY_IMAGES_SCANNED"
        echo -e "  - Critical vulnerabilities: $GATEWAY_CRITICAL_VULNS"
        echo -e "  - High vulnerabilities: $GATEWAY_HIGH_VULNS"
    else
        echo -e "${RED}Gateway VM ($GATEWAY_VM): FAILED${NC}"
    fi
else
    echo -e "${YELLOW}Gateway VM ($GATEWAY_VM): SKIPPED${NC}"
fi

echo ""
echo -e "${GREEN}=== Aggregated Scan Results ===${NC}"
echo -e "Total container images scanned: $TOTAL_IMAGES_SCANNED"
echo -e "Total Critical vulnerabilities: $TOTAL_CRITICAL_VULNS"
echo -e "Total High vulnerabilities: $TOTAL_HIGH_VULNS"
echo -e "Total Critical+High vulnerabilities: $TOTAL_CRITICAL_HIGH"

sarif_count=$(find sarif-reports -name "*.sarif" -type f 2>/dev/null | wc -l)
echo ""
echo "Results directory: $RESULTS_DIR"
echo "SARIF files generated: $sarif_count"
echo "VLAB log: $VLAB_LOG"

if [ ! -z "$GITHUB_STEP_SUMMARY" ] && [ -f "$GITHUB_STEP_SUMMARY" ]; then
    echo "## Security Scan Summary" >> $GITHUB_STEP_SUMMARY
    echo "" >> $GITHUB_STEP_SUMMARY
    echo "- **SARIF files generated:** $sarif_count" >> $GITHUB_STEP_SUMMARY

    if [ "$RUN_CONTROL" = true ]; then
        echo "- **Control VM container images scanned:** $CONTROL_IMAGES_SCANNED" >> $GITHUB_STEP_SUMMARY
        echo "  - Critical vulnerabilities: $CONTROL_CRITICAL_VULNS" >> $GITHUB_STEP_SUMMARY
        echo "  - High vulnerabilities: $CONTROL_HIGH_VULNS" >> $GITHUB_STEP_SUMMARY
    fi

    if [ "$RUN_GATEWAY" = true ]; then
        echo "- **Gateway VM container images scanned:** $GATEWAY_IMAGES_SCANNED" >> $GITHUB_STEP_SUMMARY
        echo "  - Critical vulnerabilities: $GATEWAY_CRITICAL_VULNS" >> $GITHUB_STEP_SUMMARY
        echo "  - High vulnerabilities: $GATEWAY_HIGH_VULNS" >> $GITHUB_STEP_SUMMARY
    fi

    echo "- **Total images scanned:** $TOTAL_IMAGES_SCANNED" >> $GITHUB_STEP_SUMMARY
    echo "- **Total Critical+High vulnerabilities:** $TOTAL_CRITICAL_HIGH" >> $GITHUB_STEP_SUMMARY

    echo "" >> $GITHUB_STEP_SUMMARY
    echo "Check the [Security tab](https://github.com/$GITHUB_REPOSITORY/security) for detailed vulnerability reports and [artifacts](https://github.com/$GITHUB_REPOSITORY/actions/runs/$GITHUB_RUN_ID) for raw scan data." >> $GITHUB_STEP_SUMMARY
fi

SUCCESS=true
if [ "$ALLOW_PARTIAL_SUCCESS" = "true" ]; then
    if [ $sarif_count -eq 0 ]; then
        SUCCESS=false
    fi
else
    if [ "$RUN_CONTROL" = true ] && [ $CONTROL_RESULT -ne 0 ]; then
        SUCCESS=false
    fi
    if [ "$RUN_GATEWAY" = true ] && [ $GATEWAY_RESULT -ne 0 ]; then
        SUCCESS=false
    fi
fi

if [ "$SUCCESS" = true ]; then
    if [ "$RUN_CONTROL" = true ] && [ $CONTROL_RESULT -ne 0 ] || [ "$RUN_GATEWAY" = true ] && [ $GATEWAY_RESULT -ne 0 ]; then
        echo -e "${YELLOW}Security scan completed with some errors, but generated usable results${NC}"
        echo -e "${YELLOW}Found $sarif_count SARIF files with vulnerability data${NC}"
    else
        echo -e "${GREEN}Security scan completed successfully${NC}"
    fi

    if [ "$SKIP_VLAB_LAUNCH" = false ]; then
        echo -e "${YELLOW}VLAB will auto-cleanup when script exits${NC}"
    else
        echo -e "${YELLOW}External VLAB will remain running (not managed by this script)${NC}"
    fi
    if [ "$RUN_GATEWAY" = true ]; then
        echo -e "${YELLOW}To manually update Gateway VM: Upload and run trivy-setup-airgapped.sh${NC}"
    fi
    exit 0
else
    echo -e "${RED}Security scan failed completely - no usable results generated${NC}"
    exit 1
fi
