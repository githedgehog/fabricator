#!/bin/bash
# Copyright 2025 Hedgehog
# SPDX-License-Identifier: Apache-2.0

# Trivy installation and scanning script for Flatcar Linux with K3s
# Saves scan results to files and configures registry access automatically

set -e

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

TRIVY_DIR="/var/lib/trivy"
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

sudo mkdir -p $TRIVY_DIR
sudo mkdir -p $TRIVY_DIR/.docker
sudo mkdir -p $TRIVY_DIR/reports

echo -e "${GREEN}Installing Trivy to Flatcar Linux...${NC}"
echo "Downloading Trivy installer..."
if ! sudo curl -sfL https://raw.githubusercontent.com/aquasecurity/trivy/main/contrib/install.sh -o /tmp/trivy-install.sh; then
    echo -e "${RED}Failed to download Trivy installer${NC}"
    echo "Network or download issue - check connectivity to github.com"
    exit 1
fi

echo "Running Trivy installer..."
if ! sudo sh /tmp/trivy-install.sh -b /var/lib/trivy; then
    echo -e "${RED}Trivy installation failed${NC}"
    echo "Installation script failed. Debug info:"
    echo "Installer script contents:"
    sudo head -20 /tmp/trivy-install.sh || echo "Cannot read installer"
    exit 1
fi

# Clean up installer
sudo rm -f /tmp/trivy-install.sh

# Verify installation actually worked
if ! sudo test -f /var/lib/trivy/trivy; then
    echo -e "${RED}Trivy binary not found after installation${NC}"
    echo "Contents of /var/lib/trivy/:"
    sudo ls -la /var/lib/trivy/ || echo "Directory doesn't exist"
    exit 1
fi

sudo chmod +x /var/lib/trivy/trivy

# Verify installation works
if sudo test -x /var/lib/trivy/trivy; then
    echo -e "${GREEN}Trivy binary installed and executable${NC}"
    echo "Trivy version:"
    sudo /var/lib/trivy/trivy version || echo "Version check failed but binary exists"
else
    echo -e "${RED}Trivy installation verification failed - binary not executable${NC}"
    echo "Debug info:"
    sudo ls -la /var/lib/trivy/
    exit 1
fi

sudo bash -c 'echo "export PATH=\$PATH:/var/lib/trivy" > /etc/profile.d/trivy.sh'
sudo chmod +x /etc/profile.d/trivy.sh
export PATH=$PATH:/var/lib/trivy

# Extract registry credentials from K3s
echo -e "${YELLOW}Attempting to configure registry credentials automatically...${NC}"
REGISTRY="172.30.0.1:31000"
USERNAME=""
PASSWORD=""
AUTH_CONFIGURED=false

if sudo test -f /etc/rancher/k3s/registries.yaml; then
    echo "Found K3s registry configuration, attempting to extract credentials..."

    USERNAME=$(sudo grep -A5 "$REGISTRY" /etc/rancher/k3s/registries.yaml | sudo grep "username" | head -1 | sed 's/.*username: *//' | tr -d '"' || echo "")
    PASSWORD=$(sudo grep -A5 "$REGISTRY" /etc/rancher/k3s/registries.yaml | sudo grep "password" | head -1 | sed 's/.*password: *//' | tr -d '"' || echo "")

    if [ ! -z "$USERNAME" ] && [ ! -z "$PASSWORD" ]; then
        echo -e "${GREEN}Successfully extracted registry credentials from K3s config${NC}"
        AUTH_CONFIGURED=true
    fi
fi

# Create Docker config file with registry credentials
if [ "$AUTH_CONFIGURED" = true ]; then
    echo "Configuring registry authentication..."
    sudo tee $TRIVY_DIR/.docker/config.json > /dev/null << EOF
{
    "auths": {
        "$REGISTRY": {
            "auth": "$(echo -n "$USERNAME:$PASSWORD" | base64)"
        }
    }
}
EOF
    sudo chmod 600 $TRIVY_DIR/.docker/config.json
    echo -e "${GREEN}Registry authentication configured.${NC}"
fi

echo "Creating scan script..."
sudo tee $TRIVY_DIR/scan.sh > /dev/null << 'EOF'
#!/bin/bash
# Trivy scan script for K3s on Flatcar Linux

set -e

TRIVY_DIR="/var/lib/trivy"
REPORTS_DIR="${TRIVY_DIR}/reports"
DOCKER_CONFIG="${TRIVY_DIR}/.docker"
TIMESTAMP=$(date +"%Y%m%d-%H%M%S")
CONTAINERD_ADDRESS="/run/k3s/containerd/containerd.sock"

mkdir -p ${REPORTS_DIR}

echo "Cleaning up old scan reports..."
sudo rm -rf ${REPORTS_DIR}/* 2>/dev/null || true
sudo mkdir -p ${REPORTS_DIR}
echo "Previous scan reports cleaned up"

scan_image() {
    local image="$1"
    local output_base="${REPORTS_DIR}/${TIMESTAMP}_$(echo $image | tr '/:' '_')"

    echo "Scanning $image..."

    # Text report (severity HIGH,CRITICAL) - Table format
    if sudo DOCKER_CONFIG=${DOCKER_CONFIG} ${TRIVY_DIR}/trivy image \
        --insecure \
        --severity HIGH,CRITICAL \
        --output "${output_base}_critical.txt" \
        "$image"; then
        echo "✓ Critical vulnerabilities report saved"
    else
        echo "WARNING: Critical vulnerabilities scan failed for $image"
        echo "Image: $image - Scan failed at $(date)" > "${output_base}_critical.txt"
    fi

    # JSON report for all severities
    if sudo DOCKER_CONFIG=${DOCKER_CONFIG} ${TRIVY_DIR}/trivy image \
        --insecure \
        --format json \
        --output "${output_base}_all.json" \
        "$image"; then
        echo "✓ JSON report saved"
    else
        echo "WARNING: JSON vulnerability scan failed for $image"
        echo "{\"Results\":[]}" > "${output_base}_all.json"
    fi

    # SARIF report for HIGH,CRITICAL (for GitHub integration)
    if sudo DOCKER_CONFIG=${DOCKER_CONFIG} ${TRIVY_DIR}/trivy image \
        --insecure \
        --severity HIGH,CRITICAL \
        --format sarif \
        --output "${output_base}_critical.sarif" \
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

# Check if an image should be excluded (Trivy itself)
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

# Scan specific image if provided
if [ ! -z "$1" ]; then
    if should_exclude "$1"; then
        echo "Skipping Trivy image: $1"
    else
        scan_image "$1"
    fi
    exit 0
fi

# Try to get images from k3s
echo "Identifying container images in K3s..."
if command -v crictl >/dev/null && [ -S "$CONTAINERD_ADDRESS" ]; then
    # Get list of images, excluding the 'pause' image
    IMAGES=$(sudo crictl --runtime-endpoint unix://${CONTAINERD_ADDRESS} images | grep -v IMAGE | grep -v pause | awk '{print $1":"$2}' || echo "")

    for IMAGE in $IMAGES; do
        if [ "$IMAGE" != ":" ] && [ ! -z "$IMAGE" ]; then
            if should_exclude "$IMAGE"; then
                echo "Skipping Trivy image: $IMAGE"
            else
                scan_image "$IMAGE"
            fi
        fi
    done
else
    echo "Warning: crictl not available or containerd socket not found at ${CONTAINERD_ADDRESS}"
    echo "Please specify an image to scan as a parameter."
    exit 1
fi

echo "All scans completed. Reports saved to ${REPORTS_DIR}"
EOF

sudo chmod +x $TRIVY_DIR/scan.sh

echo -e "${GREEN}Setup complete!${NC}"
echo -e "Trivy installed at: ${TRIVY_DIR}/trivy"
echo -e "Scan script created at: ${TRIVY_DIR}/scan.sh"
echo -e "Reports will be saved to: ${TRIVY_DIR}/reports/"

# Show usage instructions only if requested
if [ "$SHOW_USAGE" = true ]; then
    echo -e ""
    echo -e "${YELLOW}To run a scan:${NC}"
    echo -e "  ${GREEN}sudo ${TRIVY_DIR}/scan.sh${NC} - Scan all images in K3s"
    echo -e "  ${GREEN}sudo ${TRIVY_DIR}/scan.sh [IMAGE]${NC} - Scan a specific image"
fi

echo -e ""
echo -e "${GREEN}Done!${NC}"
