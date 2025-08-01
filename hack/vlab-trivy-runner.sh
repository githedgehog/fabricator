#!/bin/bash
# Copyright 2025 Hedgehog
# SPDX-License-Identifier: Apache-2.0

set -e

# Parse command line arguments
RUN_CONTROL=true
RUN_GATEWAY=true
RUN_SWITCH=false
SKIP_VLAB_LAUNCH=false
ALLOW_PARTIAL_SUCCESS=true

while [[ $# -gt 0 ]]; do
    case $1 in
        --control-only)
            RUN_CONTROL=true
            RUN_GATEWAY=false
            RUN_SWITCH=false
            shift
            ;;
        --gateway-only)
            RUN_CONTROL=false
            RUN_GATEWAY=true
            RUN_SWITCH=false
            shift
            ;;
        --switch-only)
            RUN_CONTROL=false
            RUN_GATEWAY=false
            RUN_SWITCH=true
            shift
            ;;
        --all)
            RUN_CONTROL=true
            RUN_GATEWAY=true
            RUN_SWITCH=true
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
            echo "  --switch-only      Run only SONiC switch setup and scanning"
            echo "  --all              Run scanning on all VMs (control, gateway, and switch)"
            echo "  --skip-vlab        Skip launching VLAB (assumes VLAB is already running)"
            echo "  --strict           Require all scans to succeed (no partial successes)"
            echo "  --help, -h         Show this help message"
            echo ""
            echo "Default: Run both control and gateway VMs with VLAB launch (switch disabled)"
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
if [ "$RUN_CONTROL" = false ] && [ "$RUN_GATEWAY" = false ] && [ "$RUN_SWITCH" = false ]; then
    echo -e "${RED}ERROR: No VMs enabled. Use --help for usage information.${NC}"
    exit 1
fi

CONTROL_VM="control-1"
GATEWAY_VM="gateway-1"
SWITCH_VMS=("leaf-01" "spine-01" "spine-02")
VLAB_LOG="vlab.log"
RESULTS_DIR="trivy-reports"
SCRIPT_PATH="${SCRIPT_PATH:-./hack/trivy-setup.sh}"
AIRGAPPED_SCRIPT_PATH="${AIRGAPPED_SCRIPT_PATH:-./hack/trivy-setup-airgapped.sh}"
SONIC_AIRGAPPED_SCRIPT_PATH="${SONIC_AIRGAPPED_SCRIPT_PATH:-./hack/trivy-setup-sonic-airgapped.sh}"
VLAB_TIMEOUT=${VLAB_TIMEOUT:-30}

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
NC='\033[0m'

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

if [ "$RUN_SWITCH" = true ] && [ ! -f "$SONIC_AIRGAPPED_SCRIPT_PATH" ]; then
    echo -e "${RED}ERROR: SONiC airgapped setup script not found at: $SONIC_AIRGAPPED_SCRIPT_PATH${NC}"
    echo "Please ensure trivy-setup-sonic-airgapped.sh exists or set SONIC_AIRGAPPED_SCRIPT_PATH correctly"
    exit 1
fi

echo -e "${GREEN}Starting VLAB Trivy Scanner${NC}"
echo "Control VM: $CONTROL_VM $([ "$RUN_CONTROL" = true ] && echo "(enabled)" || echo "(disabled)")"
echo "Gateway VM: $GATEWAY_VM $([ "$RUN_GATEWAY" = true ] && echo "(enabled)" || echo "(disabled)")"
echo "Switch VMs: ${SWITCH_VMS[*]} $([ "$RUN_SWITCH" = true ] && echo "(enabled)" || echo "(disabled)")"
echo "Skip VLAB launch: $([ "$SKIP_VLAB_LAUNCH" = true ] && echo "Yes (using external VLAB)" || echo "No")"
echo "Allow partial success: $([ "$ALLOW_PARTIAL_SUCCESS" = true ] && echo "Yes" || echo "No (strict mode)")"
echo "hhfab binary: $HHFAB_BIN"
echo "Control script: $SCRIPT_PATH"
echo "Gateway script: $AIRGAPPED_SCRIPT_PATH (airgapped mode)"
echo "Switch script: $SONIC_AIRGAPPED_SCRIPT_PATH (sonic airgapped mode)"
echo "Results: $RESULTS_DIR"
echo "Log: $VLAB_LOG"
if [ "$SKIP_VLAB_LAUNCH" = false ]; then
    echo "Timeouts: VLAB=${VLAB_TIMEOUT}m"
fi
echo ""

# Launch VLAB if not skipped
if [ "$SKIP_VLAB_LAUNCH" = false ]; then
    VLAB_EXTRA_ARGS=""

    # Add gateway flag if gateway scanning is enabled
    if [ "$RUN_GATEWAY" = true ]; then
        VLAB_EXTRA_ARGS="$VLAB_EXTRA_ARGS --gateway"
    fi

    if [ ! -f "fab.yaml" ]; then
        echo -e "${YELLOW}Initializing VLAB...${NC}"
        $HHFAB_BIN init -v --dev $VLAB_EXTRA_ARGS

        # Generate topology if switch scanning is enabled
        if [ "$RUN_SWITCH" = true ]; then
            echo -e "${YELLOW}Generating VLAB topology for switch scanning...${NC}"
            $HHFAB_BIN vlab gen --spines-count 2 --fabric-links-count 1 --orphan-leafs-count 1 --mclag-leafs-count 0 --unbundled-servers 1 --bundled-servers 0 --mclag-servers 0 --eslag-servers 0
        fi
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
fi

# Function to test SSH with retry for SONiC switches
test_sonic_ssh() {
    local vm_name="$1"
    local max_retries=10
    local retry_delay=90

    echo "Testing SSH to $vm_name (SONiC switch with retries)..."
    for ((i=1; i<=max_retries; i++)); do
        if $HHFAB_BIN vlab ssh -b -n "$vm_name" -- 'echo "SSH works"' >/dev/null 2>&1; then
            echo -e "${GREEN}SSH to $vm_name: SUCCESS (attempt $i)${NC}"
            return 0
        fi
        if [ $i -lt $max_retries ]; then
            echo "SSH retry $i/$max_retries for $vm_name (waiting ${retry_delay}s)..."
            sleep $retry_delay
        fi
    done
    echo -e "${RED}SSH to $vm_name: FAILED (all retries exhausted)${NC}"
    return 1
}

# Function to verify switch images are consistent
verify_switch_images() {
    echo -e "${YELLOW}Verifying Docker images consistency across switches...${NC}"
    local temp_images_dir="/tmp/switch-images-$$"
    mkdir -p "$temp_images_dir"

    for switch in "${SWITCH_VMS[@]}"; do
        echo "Getting image list from $switch..."
        $HHFAB_BIN vlab ssh -b -n "$switch" -- 'sudo docker images --format "{{.Repository}}:{{.Tag}}" | grep -v "^<none>" | sort' > "$temp_images_dir/$switch.txt" || {
            echo -e "${RED}Failed to get images from $switch${NC}"
            rm -rf "$temp_images_dir"
            return 1
        }
    done

    local reference_switch="${SWITCH_VMS[0]}"
    local all_consistent=true

    for switch in "${SWITCH_VMS[@]:1}"; do
        if ! diff -q "$temp_images_dir/$reference_switch.txt" "$temp_images_dir/$switch.txt" >/dev/null; then
            echo -e "${RED}Image mismatch between $reference_switch and $switch${NC}"
            echo "Differences:"
            diff "$temp_images_dir/$reference_switch.txt" "$temp_images_dir/$switch.txt" || true
            all_consistent=false
        fi
    done

    if [ "$all_consistent" = true ]; then
        local image_count=$(wc -l < "$temp_images_dir/$reference_switch.txt")
        echo -e "${GREEN}All switches have consistent Docker images ($image_count images)${NC}"
    else
        echo -e "${RED}Docker images are not consistent across switches${NC}"
        rm -rf "$temp_images_dir"
        return 1
    fi

    rm -rf "$temp_images_dir"
    return 0
}

# SSH connectivity check
echo -e "${YELLOW}Testing SSH connectivity...${NC}"

if [ "$RUN_CONTROL" = true ]; then
    echo "Testing SSH to $CONTROL_VM..."
    if $HHFAB_BIN vlab ssh -b -n "$CONTROL_VM" -- 'echo "SSH works"' >/dev/null 2>&1; then
        echo -e "${GREEN}SSH to $CONTROL_VM: SUCCESS${NC}"
    else
        echo -e "${RED}SSH to $CONTROL_VM: FAILED${NC}"
        exit 1
    fi
fi

if [ "$RUN_GATEWAY" = true ]; then
    echo "Testing SSH to $GATEWAY_VM..."
    if $HHFAB_BIN vlab ssh -b -n "$GATEWAY_VM" -- 'echo "SSH works"' >/dev/null 2>&1; then
        echo -e "${GREEN}SSH to $GATEWAY_VM: SUCCESS${NC}"
    else
        echo -e "${RED}SSH to $GATEWAY_VM: FAILED${NC}"
        exit 1
    fi
fi

if [ "$RUN_SWITCH" = true ]; then
    for switch in "${SWITCH_VMS[@]}"; do
        if ! test_sonic_ssh "$switch"; then
            exit 1
        fi
    done

    if ! verify_switch_images; then
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

# Function to setup SONiC Switches (airgapped)
setup_switch_vm() {
    echo -e "${YELLOW}=== Setting up SONiC Switches (airgap) ===${NC}"

    # Setup Trivy on all switches for load balancing
    for switch in "${SWITCH_VMS[@]}"; do
        echo "Setting up $switch..."
        if ! HHFAB_BIN="$HHFAB_BIN" "$SONIC_AIRGAPPED_SCRIPT_PATH" --leaf-node "$switch"; then
            echo -e "${RED}Failed to setup Trivy on $switch${NC}"
            return 1
        fi
    done

    echo -e "${GREEN}All SONiC Switches setup complete (airgap)${NC}"
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

# Setup SONiC Switches (airgapped mode)
if [ "$RUN_SWITCH" = true ]; then
    setup_switch_vm
    SWITCH_SETUP=$?
else
    echo "Skipping SONiC Switches setup (disabled)"
    SWITCH_SETUP=0
fi

if [ $CONTROL_SETUP -ne 0 ] || [ $GATEWAY_SETUP -ne 0 ] || [ $SWITCH_SETUP -ne 0 ]; then
    echo -e "${RED}Failed to setup Trivy on one or more VMs${NC}"
    exit 1
fi

echo -e "${GREEN}All enabled VMs setup complete${NC}"

# Function to scan VM and collect scan results
scan_vm() {
    local vm_name="$1"
    local vm_results_dir="$RESULTS_DIR/$vm_name"
    local vm_type="control"

    # Determine VM type
    if [[ "$vm_name" == "$GATEWAY_VM" ]]; then
        vm_type="gateway"
    elif [[ " ${SWITCH_VMS[*]} " =~ " $vm_name " ]]; then
        vm_type="switch"
    fi

    echo -e "${YELLOW}=== Scanning $vm_name ($vm_type) ===${NC}"

    mkdir -p "$vm_results_dir"

    # Run the appropriate scan script based on VM type
    if [ "$vm_type" = "switch" ]; then
        echo "Running airgapped security scan on SONiC $vm_name..."
        if ! $HHFAB_BIN vlab ssh -b -n "$vm_name" -- 'sudo /var/lib/trivy/scan-sonic-airgapped.sh'; then
            echo -e "${RED}Failed to run airgapped scan on $vm_name${NC}"
            return 1
        fi
    elif [ "$vm_type" = "gateway" ]; then
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

    # Get container list for metadata
    if [ "$vm_type" = "switch" ]; then
        echo "Getting container list from SONiC switch..."
        IMAGES=$($HHFAB_BIN vlab ssh -b -n "$vm_name" -- 'sudo docker ps --format "{{.Image}}" | grep -v "trivy" | sort -u' || echo "")
    else
        echo "Getting image list..."
        IMAGES=$($HHFAB_BIN vlab ssh -b -n "$vm_name" -- 'sudo crictl --runtime-endpoint unix:///run/k3s/containerd/containerd.sock images | grep -v IMAGE | grep -v pause | awk "{print \$1\":\"\$2}"' | sort -u || echo "")
    fi

    # Save container images list for later processing
    echo "$IMAGES" > "$vm_results_dir/container_images.txt"

    # Display images
    readarray -t image_array <<< "$IMAGES"
    local image_count=${#image_array[@]}

    if [ $image_count -eq 0 ]; then
        echo "No images found for scanning on $vm_name"
        return 1
    fi

    echo "=== Images found for scanning ==="
    printf '%s\n' "${image_array[@]}"
    echo "==============================="

    # Collect scan results
    echo "Collecting scan results from $vm_name..."
    if ! $HHFAB_BIN vlab ssh -b -n "$vm_name" -- 'sudo find /var/lib/trivy/reports -name "*.txt" -o -name "*.json" -o -name "*.sarif" | head -1' >/dev/null 2>&1; then
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
        return 1
    fi

    # Clean up remote temp files
    $HHFAB_BIN vlab ssh -b -n "$vm_name" -- "sudo rm -f /tmp/trivy-reports.tar.gz" || true

    echo -e "${GREEN}Scan data collection for $vm_name completed${NC}"
    return 0
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

if [ "$RUN_SWITCH" = true ]; then
    # Load balance scanning across all switches
    echo -e "${YELLOW}Load balancing switch scanning across ${#SWITCH_VMS[@]} switches...${NC}"

    primary_switch="${SWITCH_VMS[0]}"
    echo "Getting container list from $primary_switch..."
    ALL_IMAGES=$($HHFAB_BIN vlab ssh -b -n "$primary_switch" -- 'sudo docker ps --format "{{.Image}}" | grep -v "trivy" | sort -u' || echo "")

    if [ -z "$ALL_IMAGES" ]; then
        echo "No containers found for scanning"
        SWITCH_RESULT=1
    else
        readarray -t image_array <<< "$ALL_IMAGES"
        total_images=${#image_array[@]}
        echo "Found $total_images unique images to scan across ${#SWITCH_VMS[@]} switches"

        switch_results_dir="$RESULTS_DIR/sonic-switches"
        mkdir -p "$switch_results_dir"
        echo "$ALL_IMAGES" > "$switch_results_dir/container_images.txt"

        # Distribute images across switches
        SWITCH_PIDS=()

        for i in "${!SWITCH_VMS[@]}"; do
            switch="${SWITCH_VMS[$i]}"

            # Calculate which images this switch should scan
            images_for_switch=()
            for ((j=i; j<total_images; j+=$((${#SWITCH_VMS[@]})))); do
                images_for_switch+=("${image_array[$j]}")
            done

            if [ ${#images_for_switch[@]} -gt 0 ]; then
                echo "Assigning ${#images_for_switch[@]} images to $switch: ${images_for_switch[*]}"

                (
                    echo "Starting load-balanced scan on $switch..."
                    scan_success=true

                    for image in "${images_for_switch[@]}"; do
                        echo "Scanning $image on $switch..."
                        if ! $HHFAB_BIN vlab ssh -b -n "$switch" -- "sudo /var/lib/trivy/scan-sonic-airgapped.sh '$image'"; then
                            echo "Failed to scan $image on $switch"
                            scan_success=false
                        fi
                    done

                    if [ "$scan_success" = true ]; then
                        echo "0" > "/tmp/switch-result-$switch"
                    else
                        echo "1" > "/tmp/switch-result-$switch"
                    fi
                ) &
                SWITCH_PIDS+=($!)
            fi
        done

        # Wait for all switch scans to complete
        for i in "${!SWITCH_PIDS[@]}"; do
            wait "${SWITCH_PIDS[$i]}"
        done

        echo "Collecting scan results from all switches..."
        SWITCH_RESULT=0

        for switch in "${SWITCH_VMS[@]}"; do
            if [ -f "/tmp/switch-result-$switch" ]; then
                result=$(cat "/tmp/switch-result-$switch")
                if [ $result -ne 0 ]; then
                    SWITCH_RESULT=1
                fi
                rm -f "/tmp/switch-result-$switch"
            fi

            echo "Collecting results from $switch..."

            $HHFAB_BIN vlab ssh -b -n "$switch" -- 'sudo find /var/lib/trivy/reports -name "*.txt" -o -name "*.json" -o -name "*.sarif" | head -1' >/dev/null 2>&1 && {
                $HHFAB_BIN vlab ssh -b -n "$switch" -- 'sudo tar czf /tmp/trivy-reports.tar.gz -C /var/lib/trivy/reports . 2>/dev/null' || true
                $HHFAB_BIN vlab ssh -b -n "$switch" -- 'test -s /tmp/trivy-reports.tar.gz' && {
                    $HHFAB_BIN vlab ssh -b -n "$switch" -- 'cat /tmp/trivy-reports.tar.gz' > "$switch_results_dir/trivy-reports-${switch}.tar.gz"
                    (cd "$switch_results_dir" && tar xzf "trivy-reports-${switch}.tar.gz" && rm "trivy-reports-${switch}.tar.gz") || true
                }
            }

            # Clean up remote temp files
            $HHFAB_BIN vlab ssh -b -n "$switch" -- "sudo rm -f /tmp/trivy-reports.tar.gz" || true
        done

        echo -e "${GREEN}Load-balanced scanning across switches completed${NC}"
    fi
else
    echo "Skipping Switch VMs scan (disabled)"
    SWITCH_RESULT=0
fi

echo ""
echo -e "${GREEN}=== Scan Data Collection Summary ===${NC}"

if [ "$RUN_CONTROL" = true ]; then
    if [ $CONTROL_RESULT -eq 0 ]; then
        echo -e "${GREEN}Control VM ($CONTROL_VM): SUCCESS${NC}"
    else
        echo -e "${RED}Control VM ($CONTROL_VM): FAILED${NC}"
    fi
else
    echo -e "${YELLOW}Control VM ($CONTROL_VM): SKIPPED${NC}"
fi

if [ "$RUN_GATEWAY" = true ]; then
    if [ $GATEWAY_RESULT -eq 0 ]; then
        echo -e "${GREEN}Gateway VM ($GATEWAY_VM): SUCCESS${NC}"
    else
        echo -e "${RED}Gateway VM ($GATEWAY_VM): FAILED${NC}"
    fi
else
    echo -e "${YELLOW}Gateway VM ($GATEWAY_VM): SKIPPED${NC}"
fi

if [ "$RUN_SWITCH" = true ]; then
    if [ $SWITCH_RESULT -eq 0 ]; then
        echo -e "${GREEN}SONiC Switches: SUCCESS${NC}"
    else
        echo -e "${RED}SONiC Switches: FAILED${NC}"
    fi
else
    echo -e "${YELLOW}SONiC Switches: SKIPPED${NC}"
fi

SUCCESS=true
if [ "$ALLOW_PARTIAL_SUCCESS" = "true" ]; then
    if [ ! -d "$RESULTS_DIR" ] || [ -z "$(find "$RESULTS_DIR" -name "*.sarif" -type f 2>/dev/null)" ]; then
        SUCCESS=false
    fi
else
    if [ "$RUN_CONTROL" = true ] && [ $CONTROL_RESULT -ne 0 ]; then
        SUCCESS=false
    fi
    if [ "$RUN_GATEWAY" = true ] && [ $GATEWAY_RESULT -ne 0 ]; then
        SUCCESS=false
    fi
    if [ "$RUN_SWITCH" = true ] && [ $SWITCH_RESULT -ne 0 ]; then
        SUCCESS=false
    fi
fi

if [ "$SUCCESS" = true ]; then
    echo -e "${GREEN}Scan data collection completed successfully${NC}"

    CONSOLIDATOR_SCRIPT="${BASH_SOURCE%/*}/sarif-consolidator.sh"
    if [ ! -f "$CONSOLIDATOR_SCRIPT" ]; then
        CONSOLIDATOR_SCRIPT="./sarif-consolidator.sh"
    fi

    if [ -f "$CONSOLIDATOR_SCRIPT" ]; then
        echo -e "${YELLOW}Processing and consolidating SARIF files...${NC}"
        if "$CONSOLIDATOR_SCRIPT" "$RESULTS_DIR"; then
            echo -e "${GREEN}SARIF processing completed successfully${NC}"

            if [ -f "sarif-reports/trivy-security-scan.sarif" ]; then
                # Extract vulnerability counts from final SARIF for summary
                DEDUP_CRITICAL=$(jq '[.runs[0].tool.driver.rules[]? | select(.properties.tags | contains(["CRITICAL"]))] | length' "sarif-reports/trivy-security-scan.sarif" 2>/dev/null || echo 0)
                DEDUP_HIGH=$(jq '[.runs[0].tool.driver.rules[]? | select(.properties.tags | contains(["HIGH"]))] | length' "sarif-reports/trivy-security-scan.sarif" 2>/dev/null || echo 0)

                # Count raw instances for backwards compatibility
                TOTAL_CRITICAL_VULNS=$(jq '[.runs[0].results[]? | select(.level == "error" and (.message.text | contains("CRITICAL")))] | length' "sarif-reports/trivy-security-scan.sarif" 2>/dev/null || echo 0)
                TOTAL_HIGH_VULNS=$(jq '[.runs[0].results[]? | select(.level == "error" and (.message.text | contains("HIGH")))] | length' "sarif-reports/trivy-security-scan.sarif" 2>/dev/null || echo 0)

                # Count images scanned across all VMs and get VM-specific counts
                TOTAL_IMAGES_SCANNED=0
                declare -A VM_IMAGES_SCANNED VM_CRITICAL_VULNS VM_HIGH_VULNS

                for results_subdir in "$RESULTS_DIR"/*; do
                    if [ -f "$results_subdir/container_images.txt" ]; then
                        vm_name=$(basename "$results_subdir")
                        vm_image_count=$(wc -l < "$results_subdir/container_images.txt" 2>/dev/null || echo 0)
                        TOTAL_IMAGES_SCANNED=$((TOTAL_IMAGES_SCANNED + vm_image_count))
                        VM_IMAGES_SCANNED["$vm_name"]=$vm_image_count

                        # Extract VM-specific vulnerability counts from individual consolidated SARIF
                        vm_sarif="sarif-reports/trivy-consolidated-${vm_name}.sarif"
                        if [ -f "$vm_sarif" ]; then
                            VM_CRITICAL_VULNS["$vm_name"]=$(jq '[.runs[0].results[]? | select(.level == "error" and (.message.text | contains("CRITICAL")))] | length' "$vm_sarif" 2>/dev/null || echo 0)
                            VM_HIGH_VULNS["$vm_name"]=$(jq '[.runs[0].results[]? | select(.level == "error" and (.message.text | contains("HIGH")))] | length' "$vm_sarif" 2>/dev/null || echo 0)
                        else
                            VM_CRITICAL_VULNS["$vm_name"]=0
                            VM_HIGH_VULNS["$vm_name"]=0
                        fi
                    fi
                done

                echo ""
                echo -e "${GREEN}=== Security Scan Summary ===${NC}"
                echo "Total images scanned: $TOTAL_IMAGES_SCANNED"
                echo "Unique Critical vulnerability rules: $DEDUP_CRITICAL"
                echo "Unique High vulnerability rules: $DEDUP_HIGH"
                echo "Critical vulnerability instances: $TOTAL_CRITICAL_VULNS"
                echo "High vulnerability instances: $TOTAL_HIGH_VULNS"
                echo "Total vulnerability instances: $((TOTAL_CRITICAL_VULNS + TOTAL_HIGH_VULNS))"
                echo ""
                echo -e "${GREEN}=== VM-Specific Breakdown ===${NC}"

                # Add VM-specific details to console output
                for vm_name in "${!VM_IMAGES_SCANNED[@]}"; do
                    vm_display_name=""
                    case "$vm_name" in
                        control-*) vm_display_name="Control VM" ;;
                        gateway-*) vm_display_name="Gateway VM" ;;
                        sonic-switches) vm_display_name="SONiC Switches" ;;
                        leaf-*|spine-*|*switch*) vm_display_name="SONiC Switch ($vm_name)" ;;
                        *) vm_display_name="$vm_name" ;;
                    esac

                    echo "${vm_display_name} container images scanned: ${VM_IMAGES_SCANNED[$vm_name]}"
                    echo "  - Critical vulnerability instances: ${VM_CRITICAL_VULNS[$vm_name]}"
                    echo "  - High vulnerability instances: ${VM_HIGH_VULNS[$vm_name]}"
                done

                # GitHub Actions integration
                if [ ! -z "$GITHUB_STEP_SUMMARY" ] && [ -f "$GITHUB_STEP_SUMMARY" ]; then
                    echo "## Security Scan Summary" >> "$GITHUB_STEP_SUMMARY"
                    echo "- **Total images scanned:** $TOTAL_IMAGES_SCANNED" >> "$GITHUB_STEP_SUMMARY"
                    echo "- **Unique Critical vulnerability rules:** $DEDUP_CRITICAL" >> "$GITHUB_STEP_SUMMARY"
                    echo "- **Unique High vulnerability rules:** $DEDUP_HIGH" >> "$GITHUB_STEP_SUMMARY"
                    echo "- **Critical vulnerability instances:** $TOTAL_CRITICAL_VULNS" >> "$GITHUB_STEP_SUMMARY"
                    echo "- **High vulnerability instances:** $TOTAL_HIGH_VULNS" >> "$GITHUB_STEP_SUMMARY"
                    echo "- **Total vulnerability instances:** $((TOTAL_CRITICAL_VULNS + TOTAL_HIGH_VULNS))" >> "$GITHUB_STEP_SUMMARY"
                    echo "" >> "$GITHUB_STEP_SUMMARY"
                    echo "### VM-Specific Breakdown" >> "$GITHUB_STEP_SUMMARY"

                    # Add VM-specific details
                    for vm_name in "${!VM_IMAGES_SCANNED[@]}"; do
                        vm_display_name=""
                        case "$vm_name" in
                            control-*) vm_display_name="Control VM" ;;
                            gateway-*) vm_display_name="Gateway VM" ;;
                            sonic-switches) vm_display_name="SONiC Switches" ;;
                            leaf-*|spine-*|*switch*) vm_display_name="SONiC Switch ($vm_name)" ;;
                            *) vm_display_name="$vm_name" ;;
                        esac

                        echo "- **${vm_display_name} container images scanned:** ${VM_IMAGES_SCANNED[$vm_name]}" >> "$GITHUB_STEP_SUMMARY"
                        echo "  - Critical vulnerability instances: ${VM_CRITICAL_VULNS[$vm_name]}" >> "$GITHUB_STEP_SUMMARY"
                        echo "  - High vulnerability instances: ${VM_HIGH_VULNS[$vm_name]}" >> "$GITHUB_STEP_SUMMARY"
                    done

                    echo "" >> "$GITHUB_STEP_SUMMARY"
                    echo "Check the [Security tab](https://github.com/$GITHUB_REPOSITORY/security) for detailed vulnerability reports." >> "$GITHUB_STEP_SUMMARY"
                fi

                if [ ! -z "$GITHUB_ENV" ]; then
                    echo "SARIF_FILE=sarif-reports/trivy-security-scan.sarif" >> "$GITHUB_ENV"
                    echo "UPLOAD_SARIF=true" >> "$GITHUB_ENV"
                fi
            fi

            echo ""
            echo "Results directory: $RESULTS_DIR"
            echo "SARIF directory: sarif-reports"
            echo "Final SARIF report: sarif-reports/trivy-security-scan.sarif"
            echo "VLAB log: $VLAB_LOG"

        else
            echo -e "${RED}SARIF processing failed${NC}"
            exit 1
        fi
    else
        echo -e "${YELLOW}SARIF consolidator not found at $CONSOLIDATOR_SCRIPT${NC}"
        echo -e "${YELLOW}Run sarif-consolidator.sh manually to process SARIF files${NC}"
    fi

    if [ "$SKIP_VLAB_LAUNCH" = false ]; then
        echo -e "${YELLOW}VLAB will auto-cleanup when script exits${NC}"
    else
        echo -e "${YELLOW}External VLAB will remain running (not managed by this script)${NC}"
    fi
    exit 0
else
    echo -e "${RED}Scan data collection failed${NC}"
    exit 1
fi
