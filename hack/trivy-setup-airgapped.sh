#!/bin/bash
# Copyright 2025 Hedgehog
# SPDX-License-Identifier: Apache-2.0

# Trivy airgapped installation script for gateway VM
# Downloads on HOST, transfers to gateway VM for offline operation

set -e

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

TRIVY_VERSION="0.65.0"
HOST_DOWNLOAD_DIR="/tmp/trivy-airgapped-$(date +%s)"
SHOW_USAGE=false

while [[ $# -gt 0 ]]; do
    case $1 in
        --show-usage)
            SHOW_USAGE=true
            shift
            ;;
        --help|-h)
            echo "Usage: $0 [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  --show-usage       Show usage instructions after installation"
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

echo -e "${GREEN}Setting up Trivy for Airgapped Operation...${NC}"

# Step 1: Download everything on HOST
echo -e "${YELLOW}Downloading Trivy components on host...${NC}"
mkdir -p "$HOST_DOWNLOAD_DIR"

# Store current directory and convert HHFAB_BIN to absolute path if needed
ORIGINAL_DIR="$(pwd)"

# Step 3: Find hhfab binary (use passed environment variable or search)
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

# Step 2: Create scan script locally
echo -e "${YELLOW}Creating airgapped scan script...${NC}"
cat > scan-airgapped.sh << 'SCANEOF'
#!/bin/bash
# Trivy scan script for K3s on Flatcar Linux (Airgapped version)
# MODIFIED: Uses direct scanning for private registry images and export method for public images

set -e

# Configuration
TRIVY_DIR="/var/lib/trivy"
REPORTS_DIR="${TRIVY_DIR}/reports"
CACHE_DIR="${TRIVY_DIR}/cache"
TIMESTAMP=$(date +"%Y%m%d-%H%M%S")
CONTAINERD_ADDRESS="/run/k3s/containerd/containerd.sock"
TEMP_DIR="/tmp/trivy-export-$"

# Ensure directories exist
mkdir -p ${REPORTS_DIR}
mkdir -p ${TEMP_DIR}

# Clean up old scan reports to prevent accumulation
echo "Cleaning up old scan reports..."
sudo rm -rf ${REPORTS_DIR}/* 2>/dev/null || true
sudo mkdir -p ${REPORTS_DIR}
echo "Previous scan reports cleaned up"

# Cleanup function
cleanup() {
    rm -rf ${TEMP_DIR}
}
trap cleanup EXIT

# Set up registry credentials from K3s config
echo "Setting up registry authentication..."
REGISTRY="172.30.0.1:31000"
AUTH_CONFIGURED=false

# Check if k3s registries.yaml exists and extract credentials
if [ -f /etc/rancher/k3s/registries.yaml ]; then
    echo "Found K3s registry configuration, extracting credentials..."

    USERNAME=$(sudo grep -A5 "$REGISTRY" /etc/rancher/k3s/registries.yaml | grep "username" | head -1 | sed 's/.*username: *//' | tr -d '"' || echo "")
    PASSWORD=$(sudo grep -A5 "$REGISTRY" /etc/rancher/k3s/registries.yaml | grep "password" | head -1 | sed 's/.*password: *//' | tr -d '"' || echo "")

    if [ ! -z "$USERNAME" ] && [ ! -z "$PASSWORD" ]; then
        echo "Successfully extracted registry credentials"
        AUTH_CONFIGURED=true

        # Create Docker config file with registry credentials
        mkdir -p ${TRIVY_DIR}/.docker
        cat > ${TRIVY_DIR}/.docker/config.json << EOF
{
    "auths": {
        "$REGISTRY": {
            "auth": "$(echo -n "$USERNAME:$PASSWORD" | base64)"
        }
    }
}
EOF
        chmod 600 ${TRIVY_DIR}/.docker/config.json
    fi
fi

# Function to directly scan an image (for private registry images)
scan_image_directly() {
    local image="$1"
    local output_base="${REPORTS_DIR}/${TIMESTAMP}_$(echo $image | tr '/:' '_')"

    echo "Scanning image directly: $image"

    # Text report (severity HIGH,CRITICAL) - Table format
    if sudo DOCKER_CONFIG=${TRIVY_DIR}/.docker ${TRIVY_DIR}/trivy image \
        --skip-db-update \
        --cache-dir ${CACHE_DIR} \
        --severity HIGH,CRITICAL \
        --format table \
        --output "${output_base}_critical.txt" \
        --insecure \
        "$image"; then
        echo "✓ Critical vulnerabilities report saved"
    else
        echo "WARNING: Critical vulnerabilities scan failed for $image"
        echo "Image: $image - Scan failed at $(date)" > "${output_base}_critical.txt"
    fi

    # JSON report for all severities
    if sudo DOCKER_CONFIG=${TRIVY_DIR}/.docker ${TRIVY_DIR}/trivy image \
        --skip-db-update \
        --cache-dir ${CACHE_DIR} \
        --format json \
        --output "${output_base}_all.json" \
        --insecure \
        "$image"; then
        echo "✓ JSON report saved"
    else
        echo "WARNING: JSON vulnerability scan failed for $image"
        echo "{\"Results\":[]}" > "${output_base}_all.json"
    fi

    # SARIF report for HIGH,CRITICAL (for GitHub integration)
    if sudo DOCKER_CONFIG=${TRIVY_DIR}/.docker ${TRIVY_DIR}/trivy image \
        --skip-db-update \
        --cache-dir ${CACHE_DIR} \
        --severity HIGH,CRITICAL \
        --format sarif \
        --output "${output_base}_critical.sarif" \
        --insecure \
        "$image"; then
        echo "✓ SARIF report saved"
    else
        echo "WARNING: SARIF vulnerability scan failed for $image"
        echo "{\"$schema\":\"https://raw.githubusercontent.com/oasis-tcs/sarif-spec/master/Schemata/sarif-schema-2.1.0.json\",\"version\":\"2.1.0\",\"runs\":[{\"tool\":{\"driver\":{\"name\":\"Trivy\",\"informationUri\":\"https://github.com/aquasecurity/trivy\",\"rules\":[],\"version\":\"0.65.0\"}},\"results\":[]}]}" > "${output_base}_critical.sarif"
    fi

    echo "Reports saved to:"
    echo "  - ${output_base}_critical.txt (Human readable)"
    echo "  - ${output_base}_all.json (Complete JSON data)"
    echo "  - ${output_base}_critical.sarif (GitHub Security)"
}

# Function to scan an exported image tarball (for public images)
scan_image_tarball() {
    local image="$1"
    local tarball="$2"
    local output_base="${REPORTS_DIR}/${TIMESTAMP}_$(echo $image | tr '/:' '_')"

    echo "Scanning exported image: $image"

    # Text report (severity HIGH,CRITICAL) - Table format
    if sudo DOCKER_CONFIG=${TRIVY_DIR}/.docker ${TRIVY_DIR}/trivy image \
        --skip-db-update \
        --cache-dir ${CACHE_DIR} \
        --severity HIGH,CRITICAL \
        --format table \
        --output "${output_base}_critical.txt" \
        --input "$tarball"; then
        echo "✓ Critical vulnerabilities report saved"
    else
        echo "WARNING: Critical vulnerabilities scan failed for $image"
        echo "Image: $image - Scan failed at $(date)" > "${output_base}_critical.txt"
    fi

    # JSON report for all severities
    if sudo DOCKER_CONFIG=${TRIVY_DIR}/.docker ${TRIVY_DIR}/trivy image \
        --skip-db-update \
        --cache-dir ${CACHE_DIR} \
        --format json \
        --output "${output_base}_all.json" \
        --input "$tarball"; then
        echo "✓ JSON report saved"
    else
        echo "WARNING: JSON vulnerability scan failed for $image"
        echo "{\"Results\":[]}" > "${output_base}_all.json"
    fi

    # SARIF report for HIGH,CRITICAL (for GitHub integration)
    if sudo DOCKER_CONFIG=${TRIVY_DIR}/.docker ${TRIVY_DIR}/trivy image \
        --skip-db-update \
        --cache-dir ${CACHE_DIR} \
        --severity HIGH,CRITICAL \
        --format sarif \
        --output "${output_base}_critical.sarif" \
        --input "$tarball"; then
        echo "✓ SARIF report saved"
    else
        echo "WARNING: SARIF vulnerability scan failed for $image"
        echo "{\"$schema\":\"https://raw.githubusercontent.com/oasis-tcs/sarif-spec/master/Schemata/sarif-schema-2.1.0.json\",\"version\":\"2.1.0\",\"runs\":[{\"tool\":{\"driver\":{\"name\":\"Trivy\",\"informationUri\":\"https://github.com/aquasecurity/trivy\",\"rules\":[],\"version\":\"0.65.0\"}},\"results\":[]}]}" > "${output_base}_critical.sarif"
    fi

    echo "Reports saved to:"
    echo "  - ${output_base}_critical.txt (Human readable)"
    echo "  - ${output_base}_all.json (Complete JSON data)"
    echo "  - ${output_base}_critical.sarif (GitHub Security)"
}

# Function to export and scan a single image (uses direct scan for private registry)
export_and_scan_image() {
    local image="$1"

    echo "=== Processing image: $image ==="

    # Check if this is a private registry image
    if [[ "$image" == *"$REGISTRY"* ]]; then
        echo "Private registry image detected, using direct scan method..."
        # Use direct scan for private registry images
        scan_image_directly "$image"
    else
        # Export and scan for public images
        local image_safe=$(echo "$image" | tr '/:' '_')
        local tarball="${TEMP_DIR}/${image_safe}.tar"

        # Export image from containerd
        echo "Exporting image from containerd..."
        if sudo ctr -a ${CONTAINERD_ADDRESS} -n k8s.io image export "$tarball" "$image" 2>/dev/null; then
            echo "✓ Image exported successfully"

            # Scan the exported tarball
            scan_image_tarball "$image" "$tarball"

            # Clean up tarball
            rm -f "$tarball"
        else
            echo "✗ Failed to export image: $image"
            echo "Image: $image - Export failed at $(date)" > "${REPORTS_DIR}/${TIMESTAMP}_$(echo $image | tr '/:' '_')_failed.txt"
        fi
    fi
}

# Check if an image should be excluded
should_exclude() {
    local image="$1"
    if [[ "$image" == *"aquasec/trivy"* || "$image" == *"trivy-operator"* || "$image" == *"aquasecurity/trivy"* || "$image" == *"pause"* ]]; then
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

# Verify containerd socket exists
if ! sudo test -S ${CONTAINERD_ADDRESS}; then
    echo "ERROR: Containerd socket not found at ${CONTAINERD_ADDRESS}"
    exit 1
fi

# Verify ctr command is available
if ! command -v ctr >/dev/null; then
    echo "ERROR: ctr command not found. Required for exporting images from containerd."
    exit 1
fi

echo "Starting Trivy airgapped scan using hybrid method..."
echo "Timestamp: ${TIMESTAMP}"

# Scan specific image if provided
if [ ! -z "$1" ]; then
    if should_exclude "$1"; then
        echo "Skipping excluded image: $1"
    else
        export_and_scan_image "$1"
    fi
    exit 0
fi

# Get list of images from crictl
echo "Identifying container images in K3s..."
if command -v crictl >/dev/null && [ -S "$CONTAINERD_ADDRESS" ]; then
    # Get list of images, excluding the 'pause' image and other excluded images
    IMAGES=$(sudo crictl --runtime-endpoint unix://${CONTAINERD_ADDRESS} images --verbose 2>/dev/null | \
             grep "RepoTags:" | \
             sed 's/RepoTags: //' | \
             grep -v "pause" | \
             sort -u || echo "")

    if [ -z "$IMAGES" ]; then
        echo "No images found in K3s"
        exit 0
    fi

    echo "Found $(echo "$IMAGES" | wc -l) images to scan"

    # Scan each image using appropriate method
    for IMAGE in $IMAGES; do
        if [ "$IMAGE" != ":" ] && [ ! -z "$IMAGE" ]; then
            if should_exclude "$IMAGE"; then
                echo "Skipping excluded image: $IMAGE"
            else
                export_and_scan_image "$IMAGE"
            fi
        fi
    done
else
    echo "ERROR: crictl not available or containerd socket not found at ${CONTAINERD_ADDRESS}"
    echo "Please specify an image to scan as a parameter."
    exit 1
fi

echo "All scans completed. Reports saved to ${REPORTS_DIR}"
echo "Temporary files cleaned up."
SCANEOF

# Step 4: Transfer to gateway VM
echo -e "${YELLOW}Transferring files to gateway VM...${NC}"

# Transfer trivy binary (run hhfab from original directory)
echo "Transferring trivy binary..."
cat trivy | (cd "$ORIGINAL_DIR" && "$HHFAB_BIN" vlab ssh -b -n gateway-1 -- 'cat > /tmp/trivy')

# Transfer vulnerability databases (run hhfab from original directory)
echo "Transferring vulnerability databases..."
tar czf - cache | (cd "$ORIGINAL_DIR" && "$HHFAB_BIN" vlab ssh -b -n gateway-1 -- 'cd /tmp && tar xzf - && mv cache trivy-cache')

# Transfer scan script to gateway VM (run hhfab from original directory)
echo "Transferring scan script to gateway VM..."
cat scan-airgapped.sh | (cd "$ORIGINAL_DIR" && "$HHFAB_BIN" vlab ssh -b -n gateway-1 -- 'cat > /tmp/scan-airgapped.sh')

# Step 5: Install on gateway VM
echo -e "${YELLOW}Installing on gateway VM...${NC}"

(cd "$ORIGINAL_DIR" && "$HHFAB_BIN" vlab ssh -b -n gateway-1 -- '
set -e

TRIVY_DIR="/var/lib/trivy"
TRIVY_CACHE_DIR="$TRIVY_DIR/cache"
REPORTS_DIR="$TRIVY_DIR/reports"

echo "Installing Trivy on gateway VM (airgapped)..."

# Create directories
sudo mkdir -p "$TRIVY_DIR"
sudo mkdir -p "$TRIVY_CACHE_DIR"
sudo mkdir -p "$REPORTS_DIR"

# Install binary
sudo mv /tmp/trivy "$TRIVY_DIR/trivy"
sudo chmod +x "$TRIVY_DIR/trivy"

# Install vulnerability databases
sudo cp -r /tmp/trivy-cache/* "$TRIVY_CACHE_DIR/"

# Install scan script
sudo mv /tmp/scan-airgapped.sh "$TRIVY_DIR/scan-airgapped.sh"
sudo chmod +x "$TRIVY_DIR/scan-airgapped.sh"

# Clean up temp files
rm -rf /tmp/trivy-cache

# Configure registry authentication from K3s
echo "Setting up registry authentication from K3s..."
REGISTRY="172.30.0.1:31000"

# Check if k3s registries.yaml exists and extract credentials
if [ -f /etc/rancher/k3s/registries.yaml ]; then
    echo "Found K3s registry configuration, attempting to extract credentials..."

    USERNAME=$(sudo grep -A5 "$REGISTRY" /etc/rancher/k3s/registries.yaml | grep "username" | head -1 | sed "s/.*username: *//" | tr -d "\"" || echo "")
    PASSWORD=$(sudo grep -A5 "$REGISTRY" /etc/rancher/k3s/registries.yaml | grep "password" | head -1 | sed "s/.*password: *//" | tr -d "\"" || echo "")

    if [ ! -z "$USERNAME" ] && [ ! -z "$PASSWORD" ]; then
        echo "Successfully extracted registry credentials"

        # Create Docker config file with registry credentials
        sudo mkdir -p ${TRIVY_DIR}/.docker
        sudo bash -c "cat > ${TRIVY_DIR}/.docker/config.json << EOF
{
    \"auths\": {
        \"$REGISTRY\": {
            \"auth\": \"$(echo -n "$USERNAME:$PASSWORD" | base64)\"
        }
    }
}
EOF"
        sudo chmod 600 ${TRIVY_DIR}/.docker/config.json

        # Also login via containerd for export operations (optional)
        echo "Configuring containerd authentication..."
        sudo ctr -a /run/k3s/containerd/containerd.sock -n k8s.io registry login --user "$USERNAME" --password "$PASSWORD" "$REGISTRY" || echo "Containerd login failed, but continuing..."
    fi
fi

# Add to path for convenience
sudo bash -c '\''echo "export PATH=\$PATH:/var/lib/trivy" > /etc/profile.d/trivy.sh'\''
sudo chmod +x /etc/profile.d/trivy.sh

# Verify installation
if sudo "$TRIVY_DIR/trivy" version >/dev/null 2>&1; then
    echo "Trivy binary verified"
    TRIVY_VERSION=$(sudo "$TRIVY_DIR/trivy" --version | head -n 1 | awk '"'"'{print $2}'"'"')
    echo "Installed Trivy version: $TRIVY_VERSION"
else
    echo "Trivy binary verification failed"
    exit 1
fi

if [ -d "$TRIVY_CACHE_DIR" ] && [ "$(sudo ls -A $TRIVY_CACHE_DIR)" ]; then
    echo "Vulnerability databases verified"
else
    echo "Vulnerability databases missing"
    exit 1
fi

if sudo test -x "$TRIVY_DIR/scan-airgapped.sh"; then
    echo "Scan script verified"
else
    echo "Scan script missing or not executable"
    exit 1
fi

echo "Airgapped Trivy installation complete!"
echo "Binary: $TRIVY_DIR/trivy"
echo "Scan script: $TRIVY_DIR/scan-airgapped.sh"
echo "Cache: $TRIVY_CACHE_DIR"
')

# Clean up local files
cd "$ORIGINAL_DIR"
rm -rf "$HOST_DOWNLOAD_DIR"

echo -e "${GREEN}Airgapped Trivy setup complete!${NC}"
echo -e "Gateway VM now has Trivy installed with cached vulnerability databases"
echo -e "No internet connectivity required for scanning"

# Show usage instructions only if requested
if [ "$SHOW_USAGE" = true ]; then
    echo
    echo -e "${YELLOW}Usage Instructions:${NC}"
    echo -e "1. Scan all images automatically:"
    echo -e "   sudo /var/lib/trivy/scan-airgapped.sh"
    echo
    echo -e "2. Scan a specific image:"
    echo -e "   sudo /var/lib/trivy/scan-airgapped.sh docker.io/rancher/mirrored-coredns-coredns:1.12.1"
    echo
    echo -e "3. Manual scan using direct method for private registry images:"
    echo -e "   sudo DOCKER_CONFIG=/var/lib/trivy/.docker /var/lib/trivy/trivy image --skip-db-update --cache-dir /var/lib/trivy/cache --insecure 172.30.0.1:31000/image:tag"
    echo
    echo -e "4. Manual scan using export method for public images:"
    echo -e "   sudo ctr -a /run/k3s/containerd/containerd.sock -n k8s.io image export image.tar <image:tag>"
    echo -e "   sudo /var/lib/trivy/trivy image --skip-db-update --cache-dir /var/lib/trivy/cache --input image.tar"
    echo
    echo -e "Reports will be saved to: /var/lib/trivy/reports/"
    echo -e "- *_critical.txt (Human readable HIGH/CRITICAL vulnerabilities)"
    echo -e "- *_all.json (Complete JSON data)"
    echo -e "- *_critical.sarif (SARIF format for GitHub Security)"
fi
