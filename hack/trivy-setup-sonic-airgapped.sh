#!/bin/bash
# Copyright 2025 Hedgehog
# SPDX-License-Identifier: Apache-2.0

# Trivy airgapped installation script for SONiC leaf node
# Downloads on HOST, transfers to leaf node for offline operation
#
# Uses hybrid approach - direct scanning for Docker containers on SONiC
# UPDATED: Added robust parallelized scanning to avoid SSH timeouts

set -e

# Define colors for better readability
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# Configuration
TRIVY_VERSION="0.63.0"  # Same as the gateway script
HOST_DOWNLOAD_DIR="/tmp/trivy-sonic-airgapped-$(date +%s)"
LEAF_NODE="leaf-01"  # Default leaf node name
MAX_PARALLEL=4  # Default to 4 parallel scans - for SONiC nodes with 4 or more cores

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --leaf-node)
            LEAF_NODE="$2"
            shift 2
            ;;
        --help|-h)
            echo "Usage: $0 [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  --leaf-node NAME   Specify leaf node name (default: leaf-01)"
            echo "  --help, -h         Show this help message"
            exit 0
            ;;
        *)
            echo "Unknown option: $1"
            echo "Use --help for usage information"
            exit 1
            ;;
    esac
done

echo -e "${GREEN}Setting up Trivy for Airgapped Operation on SONiC Leaf Node: ${LEAF_NODE}...${NC}"
echo -e "Configured for ${MAX_PARALLEL} parallel scans to avoid SSH timeouts"

# Step 1: Download everything on HOST
echo -e "${YELLOW}Downloading Trivy components on host...${NC}"
mkdir -p "$HOST_DOWNLOAD_DIR"

# Store current directory and convert HHFAB_BIN to absolute path if needed
ORIGINAL_DIR="$(pwd)"

# Step 2: Find hhfab binary (use passed environment variable or search)
if [ ! -z "$HHFAB_BIN" ] && [ -x "$HHFAB_BIN" ]; then
    # Convert relative path to absolute path
    if [[ "$HHFAB_BIN" = /* ]]; then
        # Already absolute path
        echo "Using provided hhfab binary (absolute): $HHFAB_BIN"
    else
        # Convert relative to absolute
        HHFAB_BIN="$ORIGINAL_DIR/$HHFAB_BIN"
        echo "Using provided hhfab binary (converted to absolute): $HHFAB_BIN"
    fi
elif [ -f "$ORIGINAL_DIR/hhfab" ] && [ -x "$ORIGINAL_DIR/hhfab" ]; then
    HHFAB_BIN="$ORIGINAL_DIR/hhfab"
elif [ -f "$ORIGINAL_DIR/bin/hhfab" ] && [ -x "$ORIGINAL_DIR/bin/hhfab" ]; then
    HHFAB_BIN="$ORIGINAL_DIR/bin/hhfab"
elif [ -f "$(dirname "$ORIGINAL_DIR")/hhfab" ] && [ -x "$(dirname "$ORIGINAL_DIR")/hhfab" ]; then
    HHFAB_BIN="$(dirname "$ORIGINAL_DIR")/hhfab"
elif [ -f "$(dirname "$ORIGINAL_DIR")/bin/hhfab" ] && [ -x "$(dirname "$ORIGINAL_DIR")/bin/hhfab" ]; then
    HHFAB_BIN="$(dirname "$ORIGINAL_DIR")/bin/hhfab"
else
    echo -e "${RED}ERROR: hhfab binary not found${NC}"
    echo "Current directory: $ORIGINAL_DIR"
    echo "Searched paths: $ORIGINAL_DIR/hhfab, $ORIGINAL_DIR/bin/hhfab, $(dirname "$ORIGINAL_DIR")/hhfab, $(dirname "$ORIGINAL_DIR")/bin/hhfab"
    exit 1
fi

# Verify hhfab binary exists and is executable
if [ ! -x "$HHFAB_BIN" ]; then
    echo -e "${RED}ERROR: hhfab binary not found or not executable at: $HHFAB_BIN${NC}"
    exit 1
fi

echo "Using hhfab binary: $HHFAB_BIN"

# Now change to download directory
cd "$HOST_DOWNLOAD_DIR"

# Download Trivy binary
echo "Downloading Trivy binary v${TRIVY_VERSION}..."
TRIVY_BINARY_URL="https://github.com/aquasecurity/trivy/releases/download/v${TRIVY_VERSION}/trivy_${TRIVY_VERSION}_Linux-64bit.tar.gz"

if ! curl -sfL "$TRIVY_BINARY_URL" -o trivy.tar.gz; then
    echo -e "${RED}Failed to download Trivy binary${NC}"
    exit 1
fi

# Extract binary
tar xzf trivy.tar.gz
chmod +x trivy
rm trivy.tar.gz

# Show version for verification
echo "Verifying downloaded Trivy version:"
./trivy --version

# Download vulnerability databases
echo "Downloading vulnerability databases..."
mkdir -p cache

# Download main vulnerability database
if ! ./trivy image --download-db-only --cache-dir ./cache alpine:latest >/dev/null 2>&1; then
    echo -e "${RED}Failed to download main vulnerability database${NC}"
    exit 1
fi

echo -e "${GREEN}Download complete on host${NC}"

# Step 3: Create scan script locally
echo -e "${YELLOW}Creating enhanced parallelized scan script for SONiC...${NC}"
cat > scan-sonic-airgapped.sh << 'SCANEOF'
#!/bin/bash
# Trivy scan script for SONiC leaf node (Airgapped version)
# Uses direct scanning for Docker containers
# Enhanced parallel processing with explicit job control

set -e

# Configuration
TRIVY_DIR="/var/lib/trivy"
REPORTS_DIR="${TRIVY_DIR}/reports"
CACHE_DIR="${TRIVY_DIR}/cache"
TIMESTAMP=$(date +"%Y%m%d-%H%M%S")
MAX_PARALLEL=${MAX_PARALLEL:-4}  # Default to 4 parallel scans
SCAN_TIMEOUT=${SCAN_TIMEOUT:-600}  # Default timeout per scan in seconds (10 minutes)

# Ensure directories exist
mkdir -p ${REPORTS_DIR}
mkdir -p ${TRIVY_DIR}/tmp

# Clean up old scan reports to prevent accumulation
echo "Cleaning up old scan reports..."
sudo find ${REPORTS_DIR} -type f -name "*.txt" -o -name "*.json" -o -name "*.sarif" | xargs rm -f 2>/dev/null || true
echo "Previous scan reports cleaned up"

# Setup process tracking
TEMP_DIR=$(mktemp -d -p ${TRIVY_DIR}/tmp)
PIDS_FILE="${TEMP_DIR}/pids"
LOCKS_DIR="${TEMP_DIR}/locks"
LOGS_DIR="${TEMP_DIR}/logs"

mkdir -p "$LOCKS_DIR"
mkdir -p "$LOGS_DIR"
touch "$PIDS_FILE"
touch "${TEMP_DIR}/all_results"

# Get system specs for automatic parallelism adjustment
CPU_CORES=$(grep -c ^processor /proc/cpuinfo || echo 4)
MEM_GB=$(free -g | awk '/^Mem:/{print $2}' || echo 4)

# If system has limited resources, reduce parallelism
if [ $CPU_CORES -lt $MAX_PARALLEL ] && [ $CPU_CORES -gt 0 ]; then
    echo "System has $CPU_CORES cores, adjusting parallelism to match"
    MAX_PARALLEL=$CPU_CORES
fi

if [ $MEM_GB -lt 2 ] && [ $MEM_GB -gt 0 ]; then
    echo "System has limited memory ($MEM_GB GB), reducing parallelism to avoid OOM"
    MAX_PARALLEL=2
fi

echo "Using parallelism: $MAX_PARALLEL concurrent scans"

# Cleanup function
cleanup() {
    echo "Cleaning up temporary files and any running scans..."
    # Kill any running scan processes
    if [ -f "$PIDS_FILE" ]; then
        while read pid; do
            if [ ! -z "$pid" ] && kill -0 $pid 2>/dev/null; then
                kill $pid 2>/dev/null || true
            fi
        done < "$PIDS_FILE"
    fi
    
    # Remove temp directory
    rm -rf "${TEMP_DIR}"
}
trap cleanup EXIT INT TERM

# Function to run a task in parallel, ensuring we don't exceed MAX_PARALLEL
run_parallel() {
    local cmd="$1"
    local container_name="$2"
    local log_file="${LOGS_DIR}/${container_name}.log"
    local lock_file="${LOCKS_DIR}/${container_name}.lock"
    local pid_file="${TEMP_DIR}/pid.${container_name}"
    
    # Wait until we have a free slot (below MAX_PARALLEL)
    while [ $(find "${LOCKS_DIR}" -type f | wc -l) -ge $MAX_PARALLEL ]; do
        echo "Waiting for a job slot to open (current: $(find "${LOCKS_DIR}" -type f | wc -l), max: $MAX_PARALLEL)..."
        sleep 2
        
        # Clean up any completed jobs
        for pid_file in ${TEMP_DIR}/pid.*; do
            if [ -f "$pid_file" ]; then
                pid=$(cat "$pid_file")
                if ! kill -0 $pid 2>/dev/null; then
                    # Process is no longer running
                    job_name=$(basename "$pid_file" | sed 's/^pid\.//')
                    job_lock="${LOCKS_DIR}/${job_name}.lock"
                    if [ -f "$job_lock" ]; then
                        echo "Job completed: $job_name"
                        rm -f "$job_lock" "$pid_file"
                    fi
                fi
            fi
        done
    done
    
    # Create lock file to mark slot as taken
    touch "$lock_file"
    
    # Run the command in the background
    (
        # Run the actual command
        eval "$cmd" > "$log_file" 2>&1
        result=$?
        
        # Mark as completed
        rm -f "$lock_file"
        if [ $result -eq 0 ]; then
            echo "SUCCESS:$container_name" >> "${TEMP_DIR}/results"
            echo "SUCCESS:$container_name" >> "${TEMP_DIR}/all_results"
        else
            echo "FAILED:$container_name" >> "${TEMP_DIR}/results"
            echo "FAILED:$container_name" >> "${TEMP_DIR}/all_results"
        fi
    ) &
    
    # Store the PID
    local pid=$!
    echo "$pid" > "$pid_file"
    echo "$pid" >> "$PIDS_FILE"
    
    echo "Started scan for $container_name (PID: $pid)"
}

# Function to scan a Docker container by scanning its image
scan_container() {
    local container_id="$1"
    local container_name="$2"
    local image_name="$3"
    local output_base="${REPORTS_DIR}/${TIMESTAMP}_${container_name}"
    
    # Build the command to execute
    local cmd="
        echo '[$(date '+%H:%M:%S')] Starting scan of container: $container_name (ID: $container_id, Image: $image_name)';
        
        # Text report (severity HIGH,CRITICAL) - Table format
        if timeout $SCAN_TIMEOUT sudo ${TRIVY_DIR}/trivy --cache-dir ${CACHE_DIR} image \\
            --severity HIGH,CRITICAL \\
            --format table \\
            --output '${output_base}_critical.txt' \\
            '$image_name'; then
            echo '[$(date '+%H:%M:%S')] ✓ Critical vulnerabilities report saved';
        else
            echo '[$(date '+%H:%M:%S')] WARNING: Critical vulnerabilities scan failed for $container_name';
            echo 'Container: $container_name (Image: $image_name) - Scan failed at $(date)' > '${output_base}_critical.txt';
        fi;
        
        # JSON report for all severities
        if timeout $SCAN_TIMEOUT sudo ${TRIVY_DIR}/trivy --cache-dir ${CACHE_DIR} image \\
            --format json \\
            --output '${output_base}_all.json' \\
            '$image_name'; then
            echo '[$(date '+%H:%M:%S')] ✓ JSON report saved';
        else
            echo '[$(date '+%H:%M:%S')] WARNING: JSON vulnerability scan failed for $container_name';
            echo '{\"Results\":[]}' > '${output_base}_all.json';
        fi;
        
        # SARIF report for HIGH,CRITICAL (for GitHub integration)
        if timeout $SCAN_TIMEOUT sudo ${TRIVY_DIR}/trivy --cache-dir ${CACHE_DIR} image \\
            --severity HIGH,CRITICAL \\
            --format sarif \\
            --output '${output_base}_critical.sarif' \\
            '$image_name'; then
            echo '[$(date '+%H:%M:%S')] ✓ SARIF report saved';
        else
            echo '[$(date '+%H:%M:%S')] WARNING: SARIF vulnerability scan failed for $container_name';
            echo '{\"\\$schema\":\"https://raw.githubusercontent.com/oasis-tcs/sarif-spec/master/Schemata/sarif-schema-2.1.0.json\",\"version\":\"2.1.0\",\"runs\":[{\"tool\":{\"driver\":{\"name\":\"Trivy\",\"informationUri\":\"https://github.com/aquasecurity/trivy\",\"rules\":[],\"version\":\"0.63.0\"}},\"results\":[]}]}' > '${output_base}_critical.sarif';
        fi;
        
        echo '[$(date '+%H:%M:%S')] Completed scan of container: $container_name';
        echo 'Reports saved to:';
        echo '  - ${output_base}_critical.txt (Human readable)';
        echo '  - ${output_base}_all.json (Complete JSON data)';
        echo '  - ${output_base}_critical.sarif (GitHub Security)';
    "
    
    # Run the command in parallel
    run_parallel "$cmd" "$container_name"
}

# Function to scan a container image directly
scan_image_directly() {
    local image="$1"
    local safe_name=$(echo "$image" | tr '/:' '_')
    local output_base="${REPORTS_DIR}/${TIMESTAMP}_${safe_name}"
    
    # Build the command to execute
    local cmd="
        echo '[$(date '+%H:%M:%S')] Starting scan of image directly: $image';
        
        # Text report (severity HIGH,CRITICAL) - Table format
        if timeout $SCAN_TIMEOUT sudo ${TRIVY_DIR}/trivy --cache-dir ${CACHE_DIR} image \\
            --severity HIGH,CRITICAL \\
            --format table \\
            --output '${output_base}_critical.txt' \\
            '$image'; then
            echo '[$(date '+%H:%M:%S')] ✓ Critical vulnerabilities report saved';
        else
            echo '[$(date '+%H:%M:%S')] WARNING: Critical vulnerabilities scan failed for $image';
            echo 'Image: $image - Scan failed at $(date)' > '${output_base}_critical.txt';
        fi;
        
        # JSON report for all severities
        if timeout $SCAN_TIMEOUT sudo ${TRIVY_DIR}/trivy --cache-dir ${CACHE_DIR} image \\
            --format json \\
            --output '${output_base}_all.json' \\
            '$image'; then
            echo '[$(date '+%H:%M:%S')] ✓ JSON report saved';
        else
            echo '[$(date '+%H:%M:%S')] WARNING: JSON vulnerability scan failed for $image';
            echo '{\"Results\":[]}' > '${output_base}_all.json';
        fi;
        
        # SARIF report for HIGH,CRITICAL (for GitHub integration)
        if timeout $SCAN_TIMEOUT sudo ${TRIVY_DIR}/trivy --cache-dir ${CACHE_DIR} image \\
            --severity HIGH,CRITICAL \\
            --format sarif \\
            --output '${output_base}_critical.sarif' \\
            '$image'; then
            echo '[$(date '+%H:%M:%S')] ✓ SARIF report saved';
        else
            echo '[$(date '+%H:%M:%S')] WARNING: SARIF vulnerability scan failed for $image';
            echo '{\"\\$schema\":\"https://raw.githubusercontent.com/oasis-tcs/sarif-spec/master/Schemata/sarif-schema-2.1.0.json\",\"version\":\"2.1.0\",\"runs\":[{\"tool\":{\"driver\":{\"name\":\"Trivy\",\"informationUri\":\"https://github.com/aquasecurity/trivy\",\"rules\":[],\"version\":\"0.63.0\"}},\"results\":[]}]}' > '${output_base}_critical.sarif';
        fi;
        
        echo '[$(date '+%H:%M:%S')] Completed scan of image: $image';
        echo 'Reports saved to:';
        echo '  - ${output_base}_critical.txt (Human readable)';
        echo '  - ${output_base}_all.json (Complete JSON data)';
        echo '  - ${output_base}_critical.sarif (GitHub Security)';
    "
    
    # Run the command in parallel
    run_parallel "$cmd" "$safe_name"
}

# Check if a container/image should be excluded
should_exclude() {
    local name="$1"
    if [[ "$name" == *"aquasec/trivy"* || "$name" == *"trivy-operator"* || "$name" == *"aquasecurity/trivy"* ]]; then
        return 0  # True, should exclude
    else
        return 1  # False, should not exclude
    fi
}

# Verify trivy binary exists and is executable
if ! sudo test -x ${TRIVY_DIR}/trivy; then
    echo "ERROR: Trivy binary not found or not executable at ${TRIVY_DIR}/trivy"
    echo "Debug info:"
    sudo ls -la ${TRIVY_DIR}/
    exit 1
fi

# Verify vulnerability database exists
if ! sudo test -d ${CACHE_DIR}; then
    echo "ERROR: Vulnerability database not found at ${CACHE_DIR}"
    exit 1
fi

# Verify docker command is available
if ! command -v docker >/dev/null; then
    echo "ERROR: docker command not found. Required for scanning containers."
    exit 1
fi

echo "Starting Trivy airgapped scan for SONiC containers..."
echo "Timestamp: ${TIMESTAMP}"
echo "Running with parallel scanning enabled (max: $MAX_PARALLEL concurrent scans)"
> "${TEMP_DIR}/results"  # Initialize results file

# Scan specific container if provided
if [ ! -z "$1" ]; then
    # Check if input is container ID or name
    CONTAINER_INFO=$(docker inspect --format '{{.Id}} {{.Name}} {{.Config.Image}}' "$1" 2>/dev/null || echo "")
    
    if [ ! -z "$CONTAINER_INFO" ]; then
        read -r container_id container_name image_name <<< "$CONTAINER_INFO"
        container_name=${container_name#/}  # Remove leading slash from container name
        
        if should_exclude "$image_name"; then
            echo "Skipping excluded container: $container_name (Image: $image_name)"
        else
            scan_container "$container_id" "$container_name" "$image_name"
            # Wait for the single scan to complete
            echo "Waiting for scan to complete..."
            wait
            
            # Display the log
            if [ -f "${LOGS_DIR}/${container_name}.log" ]; then
                cat "${LOGS_DIR}/${container_name}.log"
            fi
        fi
    else
        # Try as image name
        if docker image inspect "$1" >/dev/null 2>&1; then
            if should_exclude "$1"; then
                echo "Skipping excluded image: $1"
            else
                safe_name=$(echo "$1" | tr '/:' '_')
                scan_image_directly "$1"
                # Wait for the single scan to complete
                echo "Waiting for scan to complete..."
                wait
                
                # Display the log
                if [ -f "${LOGS_DIR}/${safe_name}.log" ]; then
                    cat "${LOGS_DIR}/${safe_name}.log"
                fi
            fi
        else
            echo "ERROR: '$1' is not a valid container ID/name or image"
            exit 1
        fi
    fi
    echo "All requested scans completed. Reports saved to ${REPORTS_DIR}"
    exit 0
fi

# Get list of running containers
echo "Identifying running Docker containers..."
CONTAINERS=$(docker ps --format "{{.ID}} {{.Names}} {{.Image}}" | sort)

if [ -z "$CONTAINERS" ]; then
    echo "No running containers found"
    exit 0
fi

# Calculate total container count
TOTAL_CONTAINERS=$(echo "$CONTAINERS" | wc -l)
echo "Found $TOTAL_CONTAINERS containers to scan ($MAX_PARALLEL at a time)"

# Process containers and queue scans
COMPLETED=0
QUEUED=0

echo "$CONTAINERS" | while read -r container_info; do
    if [ ! -z "$container_info" ]; then
        read -r container_id container_name image_name <<< "$container_info"
        
        if should_exclude "$image_name"; then
            echo "Skipping excluded container: $container_name (Image: $image_name)"
            COMPLETED=$((COMPLETED+1))
            continue
        fi
        
        # Start the scan in parallel
        scan_container "$container_id" "$container_name" "$image_name"
        
        QUEUED=$((QUEUED+1))
        echo "Progress: $QUEUED/$TOTAL_CONTAINERS containers queued for scanning"
        
        # Brief pause to avoid overwhelming the system
        sleep 0.5
    fi
done

# Wait for all scans to complete
echo "All scans queued, waiting for completion..."

# Display periodic status updates while waiting
(
    while [ $(find "${LOCKS_DIR}" -type f | wc -l) -gt 0 ]; do
        running_count=$(find "${LOCKS_DIR}" -type f | wc -l)
        completed_count=$(grep -c "SUCCESS\|FAILED" "${TEMP_DIR}/results" 2>/dev/null || echo 0)
        
        echo "Status: $completed_count completed, $running_count still running..."
        
        # Show recently completed jobs
        if [ -f "${TEMP_DIR}/results" ]; then
            tail -5 "${TEMP_DIR}/results" | while read line; do
                if [[ "$line" == SUCCESS:* ]]; then
                    container="${line#SUCCESS:}"
                    echo "✓ Completed successfully: $container"
                elif [[ "$line" == FAILED:* ]]; then
                    container="${line#FAILED:}"
                    echo "✗ Completed with errors: $container"
                fi
            done > "${TEMP_DIR}/recent_completed"
            
            if [ -s "${TEMP_DIR}/recent_completed" ]; then
                cat "${TEMP_DIR}/recent_completed"
            fi
            
            # Clear the results file to avoid showing the same completions repeatedly
            > "${TEMP_DIR}/results"
        fi
        
        sleep 10
    done
) &
status_pid=$!

# Wait for all background jobs to finish
wait

# Kill the status update process if it's still running
if kill -0 $status_pid 2>/dev/null; then
    kill $status_pid
fi

# Count and report success/failure
SUCCESS_COUNT=$(grep -c "SUCCESS:" "${TEMP_DIR}/all_results" 2>/dev/null || echo 0)
FAILED_COUNT=$(grep -c "FAILED:" "${TEMP_DIR}/all_results" 2>/dev/null || echo 0)

echo "All scans completed: $SUCCESS_COUNT successful, $FAILED_COUNT failed"
echo "Scan logs available in: ${LOGS_DIR}"
echo "Reports saved to: ${REPORTS_DIR}"

# Print combined log to stdout
echo "=== Combined Scan Logs ==="
cat ${LOGS_DIR}/*.log 2>/dev/null || echo "No logs found"
echo "=========================="
SCANEOF

# Step 4: Transfer to leaf node
echo -e "${YELLOW}Transferring files to leaf node...${NC}"

# Test SSH connectivity first
echo "Testing SSH connectivity to $LEAF_NODE..."
if ! (cd "$ORIGINAL_DIR" && "$HHFAB_BIN" vlab ssh -b -n "$LEAF_NODE" -- 'echo "SSH works"' >/dev/null 2>&1); then
    echo -e "${RED}ERROR: Cannot SSH to $LEAF_NODE. Please verify the node is accessible.${NC}"
    exit 1
fi

# Transfer trivy binary (run hhfab from original directory)
echo "Transferring trivy binary..."
cat trivy | (cd "$ORIGINAL_DIR" && "$HHFAB_BIN" vlab ssh -b -n "$LEAF_NODE" -- 'cat > /tmp/trivy')

# Transfer vulnerability databases (run hhfab from original directory)
echo "Transferring vulnerability databases..."
tar czf - cache | (cd "$ORIGINAL_DIR" && "$HHFAB_BIN" vlab ssh -b -n "$LEAF_NODE" -- 'cd /tmp && tar xzf - && mv cache trivy-cache')

# Transfer scan script to leaf node (run hhfab from original directory)
echo "Transferring scan script to leaf node..."
cat scan-sonic-airgapped.sh | (cd "$ORIGINAL_DIR" && "$HHFAB_BIN" vlab ssh -b -n "$LEAF_NODE" -- 'cat > /tmp/scan-sonic-airgapped.sh')

# Step 5: Install on leaf node
echo -e "${YELLOW}Installing on leaf node...${NC}"

(cd "$ORIGINAL_DIR" && "$HHFAB_BIN" vlab ssh -b -n "$LEAF_NODE" -- "
set -e

TRIVY_DIR=\"/var/lib/trivy\"
TRIVY_CACHE_DIR=\"\$TRIVY_DIR/cache\"
REPORTS_DIR=\"\$TRIVY_DIR/reports\"
MAX_PARALLEL=$MAX_PARALLEL

echo \"Installing Trivy on leaf node (airgapped)...\"
echo \"Configured for \$MAX_PARALLEL parallel scans to avoid SSH timeouts\"

# Create directories
sudo mkdir -p \"\$TRIVY_DIR\"
sudo mkdir -p \"\$TRIVY_CACHE_DIR\"
sudo mkdir -p \"\$TRIVY_DIR/tmp\"
sudo mkdir -p \"\$REPORTS_DIR\"

# Install binary
sudo mv /tmp/trivy \"\$TRIVY_DIR/trivy\"
sudo chmod +x \"\$TRIVY_DIR/trivy\"

# Install vulnerability databases
sudo cp -r /tmp/trivy-cache/* \"\$TRIVY_CACHE_DIR/\"

# Install scan script with environment variables
sudo bash -c 'cat > \"\$TRIVY_DIR/scan-sonic-airgapped.sh\"' << EOF
#!/bin/bash
# Generated scan script with configured parameters
export MAX_PARALLEL=$MAX_PARALLEL
$(cat /tmp/scan-sonic-airgapped.sh)
EOF

sudo chmod +x \"\$TRIVY_DIR/scan-sonic-airgapped.sh\"

# Clean up temp files
rm -rf /tmp/trivy-cache

# Add to path for convenience
sudo bash -c 'echo \"export PATH=\\\$PATH:/var/lib/trivy\" > /etc/profile.d/trivy.sh'
sudo chmod +x /etc/profile.d/trivy.sh

# Verify installation
if sudo \"\$TRIVY_DIR/trivy\" version >/dev/null 2>&1; then
    echo \"Trivy binary verified\"
    TRIVY_VERSION=\$(sudo \"\$TRIVY_DIR/trivy\" --version | head -n 1 | awk '{print \$2}')
    echo \"Installed Trivy version: \$TRIVY_VERSION\"
else
    echo \"Trivy binary verification failed\"
    exit 1
fi

if [ -d \"\$TRIVY_CACHE_DIR\" ] && [ \"\$(sudo ls -A \$TRIVY_CACHE_DIR)\" ]; then
    echo \"Vulnerability databases verified\"
else
    echo \"Vulnerability databases missing\"
    exit 1
fi

if sudo test -x \"\$TRIVY_DIR/scan-sonic-airgapped.sh\"; then
    echo \"Scan script verified\"
else
    echo \"Scan script missing or not executable\"
    exit 1
fi

echo \"Airgapped Trivy installation complete on SONiC leaf node!\"
echo \"Binary: \$TRIVY_DIR/trivy\"
echo \"Scan script: \$TRIVY_DIR/scan-sonic-airgapped.sh (parallelized: \$MAX_PARALLEL jobs)\"
echo \"Cache: \$TRIVY_CACHE_DIR\"
")

# Clean up local files
cd "$ORIGINAL_DIR"
rm -rf "$HOST_DOWNLOAD_DIR"

echo -e "${GREEN}Airgapped Trivy setup complete for SONiC leaf node!${NC}"
echo -e "Leaf node $LEAF_NODE now has Trivy installed with cached vulnerability databases"
echo -e "No internet connectivity required for scanning"
echo
echo -e "${YELLOW}Usage Instructions:${NC}"
echo -e "1. Scan all containers in parallel (${MAX_PARALLEL} at a time):"
echo -e "   sudo /var/lib/trivy/scan-sonic-airgapped.sh"
echo
echo -e "2. Scan a specific container by ID or name:"
echo -e "   sudo /var/lib/trivy/scan-sonic-airgapped.sh container_id_or_name"
echo
echo -e "3. Scan a specific image:"
echo -e "   sudo /var/lib/trivy/scan-sonic-airgapped.sh image_name:tag"
echo
echo -e "4. Adjust parallelization on-the-fly (if needed):"
echo -e "   sudo MAX_PARALLEL=6 /var/lib/trivy/scan-sonic-airgapped.sh"
echo
echo -e "Reports will be saved to: /var/lib/trivy/reports/"
echo -e "- *_critical.txt (Human readable HIGH/CRITICAL vulnerabilities)"
echo -e "- *_all.json (Complete JSON data)"
echo -e "- *_critical.sarif (SARIF format for GitHub Security)"
echo 
echo -e "Scan logs will be stored in a temporary directory under /var/lib/trivy/tmp/"
