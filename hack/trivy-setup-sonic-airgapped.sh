#!/bin/bash
# Copyright 2025 Hedgehog
# SPDX-License-Identifier: Apache-2.0

# Trivy airgapped installation script for SONiC switches
# Downloads on HOST, transfers to switch for offline operation

set -e

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

TRIVY_VERSION="0.65.0"
HOST_DOWNLOAD_DIR="/tmp/trivy-sonic-airgapped-$(date +%s)"
LEAF_NODE="leaf-01"  # Default leaf node name
SHOW_USAGE=false

while [[ $# -gt 0 ]]; do
    case $1 in
        --leaf-node)
            LEAF_NODE="$2"
            shift 2
            ;;
        --show-usage)
            SHOW_USAGE=true
            shift
            ;;
        --help|-h)
            echo "Usage: $0 [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  --leaf-node NAME   Specify leaf node name (default: leaf-01)"
            echo "  --show-usage       Show usage instructions after installation"
            echo "  --help, -h         Show this help message"
            echo ""
            echo "Examples:"
            echo "  $0                              # Setup on leaf-01, no usage shown"
            echo "  $0 --leaf-node spine-01         # Setup on spine-01"
            echo "  $0 --show-usage                 # Setup and show usage instructions"
            exit 0
            ;;
        *)
            echo "Unknown option: $1"
            echo "Use --help for usage information"
            exit 1
            ;;
    esac
done

echo -e "${GREEN}Setting up Trivy for SONiC Switch: ${LEAF_NODE}...${NC}"

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
echo -e "${YELLOW}Creating SONiC scan script...${NC}"
cat > scan-sonic-airgapped.sh << 'SCANEOF'
#!/bin/bash
# Trivy scan script for SONiC switches (Airgapped version)
# Based on working gateway script but adapted for Docker

set -e

# Configuration
TRIVY_DIR="/var/lib/trivy"
REPORTS_DIR="${TRIVY_DIR}/reports"
CACHE_DIR="${TRIVY_DIR}/cache"
TIMESTAMP=$(date +"%Y%m%d-%H%M%S")

# Ensure directories exist
mkdir -p ${REPORTS_DIR}

# Clean up old scan reports ONLY when scanning all images (no specific image argument)
if [ -z "$1" ]; then
    echo "Cleaning up old scan reports..."
    sudo rm -rf ${REPORTS_DIR}/* 2>/dev/null || true
    sudo mkdir -p ${REPORTS_DIR}
    echo "Previous scan reports cleaned up"
else
    echo "Individual image scan mode - preserving existing reports"
fi

# Function to scan a single image
scan_image() {
    local image="$1"
    local safe_name=$(echo "$image" | tr '/:' '_')
    local output_base="${REPORTS_DIR}/${TIMESTAMP}_${safe_name}"

    echo "=== Processing image: $image ==="

    # Text report (severity HIGH,CRITICAL) - Table format
    if sudo ${TRIVY_DIR}/trivy image \
        --skip-db-update \
        --cache-dir ${CACHE_DIR} \
        --severity HIGH,CRITICAL \
        --format table \
        --output "${output_base}_critical.txt" \
        "$image"; then
        echo "✓ Critical vulnerabilities report saved"
    else
        echo "WARNING: Critical vulnerabilities scan failed for $image"
        echo "Image: $image - Scan failed at $(date)" > "${output_base}_critical.txt"
    fi

    # JSON report for all severities
    if sudo ${TRIVY_DIR}/trivy image \
        --skip-db-update \
        --cache-dir ${CACHE_DIR} \
        --format json \
        --output "${output_base}_all.json" \
        "$image"; then
        echo "✓ JSON report saved"
    else
        echo "WARNING: JSON vulnerability scan failed for $image"
        echo '{"Results":[]}' > "${output_base}_all.json"
    fi

    # SARIF report for HIGH,CRITICAL (for GitHub integration)
    if sudo ${TRIVY_DIR}/trivy image \
        --skip-db-update \
        --cache-dir ${CACHE_DIR} \
        --severity HIGH,CRITICAL \
        --format sarif \
        --output "${output_base}_critical.sarif" \
        "$image"; then
        echo "✓ SARIF report saved"
    else
        echo "WARNING: SARIF vulnerability scan failed for $image"
        echo '{"$schema":"https://raw.githubusercontent.com/oasis-tcs/sarif-spec/master/Schemata/sarif-schema-2.1.0.json","version":"2.1.0","runs":[{"tool":{"driver":{"name":"Trivy","informationUri":"https://github.com/aquasecurity/trivy","rules":[],"version":"0.65.0"}},"results":[]}]}' > "${output_base}_critical.sarif"
    fi

    echo "Reports saved to:"
    echo "  - ${output_base}_critical.txt (Human readable)"
    echo "  - ${output_base}_all.json (Complete JSON data)"
    echo "  - ${output_base}_critical.sarif (GitHub Security)"
}

# Check if an image should be excluded
should_exclude() {
    local image="$1"
    if [[ "$image" == *"aquasec/trivy"* || "$image" == *"trivy-operator"* || "$image" == *"aquasecurity/trivy"* ]]; then
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

# Scan specific image if provided
if [ ! -z "$1" ]; then
    if should_exclude "$1"; then
        echo "Skipping excluded image: $1"
    else
        scan_image "$1"
    fi
    echo "Scan completed. Reports saved to ${REPORTS_DIR}"
    exit 0
fi

# Get list of running containers
echo "Identifying running Docker containers..."
CONTAINERS=$(docker ps --format "{{.Image}}" | grep -v "trivy" | sort -u)

if [ -z "$CONTAINERS" ]; then
    echo "No running containers found"
    exit 0
fi

echo "Found $(echo "$CONTAINERS" | wc -l) unique images to scan"

# Scan each image
while read -r image; do
    if [ ! -z "$image" ]; then
        if should_exclude "$image"; then
            echo "Skipping excluded image: $image"
        else
            scan_image "$image"
        fi
    fi
done <<< "$CONTAINERS"

echo "All scans completed. Reports saved to ${REPORTS_DIR}"
SCANEOF

# Step 4: Transfer to switch
echo -e "${YELLOW}Transferring files to switch $LEAF_NODE...${NC}"

# Test SSH connectivity first
echo "Testing SSH connectivity to $LEAF_NODE..."
if ! (cd "$ORIGINAL_DIR" && "$HHFAB_BIN" vlab ssh -b -n "$LEAF_NODE" -- 'echo "SSH works"' >/dev/null 2>&1); then
    echo -e "${RED}ERROR: Cannot SSH to $LEAF_NODE. Please verify the node is accessible.${NC}"
    exit 1
fi

# Transfer trivy binary
echo "Transferring trivy binary..."
if ! cat trivy | (cd "$ORIGINAL_DIR" && "$HHFAB_BIN" vlab ssh -b -n "$LEAF_NODE" -- 'cat > /tmp/trivy'); then
    echo -e "${RED}Failed to transfer trivy binary${NC}"
    exit 1
fi

# Transfer vulnerability databases
echo "Transferring vulnerability databases..."
if ! tar czf - cache | (cd "$ORIGINAL_DIR" && "$HHFAB_BIN" vlab ssh -b -n "$LEAF_NODE" -- 'cd /tmp && tar xzf - && mv cache trivy-cache'); then
    echo -e "${RED}Failed to transfer vulnerability databases${NC}"
    exit 1
fi

# Transfer scan script
echo "Transferring scan script..."
if ! cat scan-sonic-airgapped.sh | (cd "$ORIGINAL_DIR" && "$HHFAB_BIN" vlab ssh -b -n "$LEAF_NODE" -- 'cat > /tmp/scan-sonic-airgapped.sh'); then
    echo -e "${RED}Failed to transfer scan script${NC}"
    exit 1
fi

# Step 5: Install on switch
echo -e "${YELLOW}Installing on switch $LEAF_NODE...${NC}"

if ! (cd "$ORIGINAL_DIR" && "$HHFAB_BIN" vlab ssh -b -n "$LEAF_NODE" -- "
set -e

TRIVY_DIR=\"/var/lib/trivy\"
TRIVY_CACHE_DIR=\"\$TRIVY_DIR/cache\"
REPORTS_DIR=\"\$TRIVY_DIR/reports\"

echo \"Installing Trivy on SONiC switch (airgapped)...\"

# Verify all files exist before proceeding
if [ ! -f /tmp/trivy ]; then
    echo \"ERROR: Trivy binary not found at /tmp/trivy\"
    exit 1
fi

if [ ! -d /tmp/trivy-cache ]; then
    echo \"ERROR: Trivy cache not found at /tmp/trivy-cache\"
    exit 1
fi

if [ ! -f /tmp/scan-sonic-airgapped.sh ]; then
    echo \"ERROR: Scan script not found at /tmp/scan-sonic-airgapped.sh\"
    exit 1
fi

# Create directories
sudo mkdir -p \"\$TRIVY_DIR\"
sudo mkdir -p \"\$TRIVY_CACHE_DIR\"
sudo mkdir -p \"\$REPORTS_DIR\"

# Install binary
sudo mv /tmp/trivy \"\$TRIVY_DIR/trivy\"
sudo chmod +x \"\$TRIVY_DIR/trivy\"

# Install vulnerability databases
sudo cp -r /tmp/trivy-cache/* \"\$TRIVY_CACHE_DIR/\"

# Install scan script
sudo mv /tmp/scan-sonic-airgapped.sh \"\$TRIVY_DIR/scan-sonic-airgapped.sh\"
sudo chmod +x \"\$TRIVY_DIR/scan-sonic-airgapped.sh\"

# Clean up temp files
rm -rf /tmp/trivy-cache

# Add to path for convenience
sudo bash -c 'echo \"export PATH=\\\$PATH:/var/lib/trivy\" > /etc/profile.d/trivy.sh'
sudo chmod +x /etc/profile.d/trivy.sh

# Verify installation
if sudo \"\$TRIVY_DIR/trivy\" version >/dev/null 2>&1; then
    echo \"✓ Trivy binary verified\"
    TRIVY_VERSION=\$(sudo \"\$TRIVY_DIR/trivy\" --version | head -n 1 | awk '{print \$2}')
    echo \"Installed Trivy version: \$TRIVY_VERSION\"
else
    echo \"ERROR: Trivy binary verification failed\"
    exit 1
fi

if [ -d \"\$TRIVY_CACHE_DIR\" ] && [ \"\$(sudo ls -A \$TRIVY_CACHE_DIR)\" ]; then
    echo \"✓ Vulnerability databases verified\"
else
    echo \"ERROR: Vulnerability databases missing\"
    exit 1
fi

if sudo test -x \"\$TRIVY_DIR/scan-sonic-airgapped.sh\"; then
    echo \"✓ Scan script verified\"
else
    echo \"ERROR: Scan script missing or not executable\"
    exit 1
fi

echo \"Trivy installation complete on SONiC switch $LEAF_NODE\"
"); then
    echo -e "${RED}Failed to install on switch${NC}"
    exit 1
fi

# Clean up local files
cd "$ORIGINAL_DIR"
rm -rf "$HOST_DOWNLOAD_DIR"

echo -e "${GREEN}Trivy setup complete for SONiC switch $LEAF_NODE!${NC}"
echo -e "Switch is ready for scanning"

# Show usage instructions only if requested
if [ "$SHOW_USAGE" = true ]; then
    echo
    echo -e "${YELLOW}Usage Instructions:${NC}"
    echo -e "1. Scan all running containers:"
    echo -e "   sudo /var/lib/trivy/scan-sonic-airgapped.sh"
    echo
    echo -e "2. Scan a specific image:"
    echo -e "   sudo /var/lib/trivy/scan-sonic-airgapped.sh image_name:tag"
    echo
    echo -e "Reports will be saved to: /var/lib/trivy/reports/"
    echo -e "- *_critical.txt (Human readable HIGH/CRITICAL vulnerabilities)"
    echo -e "- *_all.json (Complete JSON data)"
    echo -e "- *_critical.sarif (SARIF format for GitHub Security)"
fi
