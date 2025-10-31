#!/bin/bash
# Copyright 2025 Hedgehog
# SPDX-License-Identifier: Apache-2.0

set -e

# LGTM Consolidated Setup Script for Local Registry

REGISTRY="${REGISTRY:-localhost:30000}"
CHART_REPO="${REGISTRY}/githedgehog/lgtm/charts"
IMAGE_REPO="${REGISTRY}/githedgehog/lgtm/images"
GRAFANA_REPO="${REGISTRY}/grafana"  # Added for original path expected by charts
NGINX_REPO="${REGISTRY}/nginxinc"   # Added for Loki gateway dependency
KIWIGRID_REPO="${REGISTRY}/kiwigrid" # Added for k8s-sidecar
PROM_REPO="${REGISTRY}/prometheus"  # Added for Prometheus
KSM_REPO="${REGISTRY}/kube-state-metrics" # Added for kube-state-metrics

echo "LGTM Setup"
echo "=========="
echo "Registry: ${REGISTRY}"

# Component versions
declare -A COMPONENT_VERSIONS=(
  ["grafana"]="10.1.1"    # Chart version
  ["loki"]="6.43.0"       # Chart version
  ["tempo"]="1.23.3"      # Chart version
  ["prometheus"]="27.41.0"  # Latest Prometheus chart version
)

# App versions (container images)
declare -A APP_VERSIONS=(
  ["grafana"]="12.2.0"
  ["loki"]="3.5.5"
  ["tempo"]="2.8.2"
  ["prometheus"]="v3.7.0"  # Latest Prometheus image version
  ["kube-state-metrics"]="v2.17.0" # KSM version
  ["node-exporter"]="v1.9.1" # Node exporter version
  ["pushgateway"]="v1.11.1"  # Pushgateway version
)

# Dependency images
NGINX_VERSION="1.29-alpine"
SIDECAR_VERSION="1.30.10"

# Step 1: Process Helm charts
echo "Processing Helm charts..."
helm repo add grafana https://grafana.github.io/helm-charts 2>/dev/null || true
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts 2>/dev/null || true
helm repo update

# Pull and push Grafana charts
for comp in grafana loki tempo; do
  helm pull grafana/${comp} --version ${COMPONENT_VERSIONS[${comp}]}
  helm push --plain-http ${comp}-${COMPONENT_VERSIONS[${comp}]}.tgz oci://${CHART_REPO}
done

# Pull and push Prometheus chart
helm pull prometheus-community/prometheus --version ${COMPONENT_VERSIONS["prometheus"]}
helm push --plain-http prometheus-${COMPONENT_VERSIONS["prometheus"]}.tgz oci://${CHART_REPO}

# Step 2: Set up container policy for skopeo
POLICY_DIR=~/.config/containers
mkdir -p ${POLICY_DIR}
cat > ${POLICY_DIR}/policy.json <<EOF
{
    "default": [
        {
            "type": "insecureAcceptAnything"
        }
    ]
}
EOF

# Step 3: Process container images with skopeo
echo "Processing container images..."
for comp in grafana loki tempo; do
  echo "Processing ${comp}..."

  # Pull the image
  docker pull docker.io/grafana/${comp}:${APP_VERSIONS[$comp]} || {
    echo "Error pulling ${comp}:${APP_VERSIONS[$comp]} - skipping"
    continue
  }

  # Tag with only custom path
  echo "Tagging ${comp} for custom path..."
  docker tag docker.io/grafana/${comp}:${APP_VERSIONS[$comp]} ${IMAGE_REPO}/${comp}:${COMPONENT_VERSIONS[$comp]}

  # Push custom path using skopeo
  echo "Pushing to custom path..."
  REGISTRY_AUTH_FILE=${POLICY_DIR}/policy.json bin/skopeo-v1.16.1 copy --format oci --dest-tls-verify=false \
    docker-daemon:${IMAGE_REPO}/${comp}:${COMPONENT_VERSIONS[$comp]} \
    docker://${IMAGE_REPO}/${comp}:${COMPONENT_VERSIONS[$comp]} || {
      echo "Error pushing ${comp} image to custom path"
    }

  echo "${comp} processing complete"
  echo ""
done

# Process Prometheus image
echo "Processing prometheus..."
docker pull docker.io/prom/prometheus:${APP_VERSIONS["prometheus"]} || {
  echo "Error pulling prometheus:${APP_VERSIONS["prometheus"]} - skipping"
}

# Tag and push Prometheus image
echo "Tagging prometheus for custom path..."
docker tag docker.io/prom/prometheus:${APP_VERSIONS["prometheus"]} ${IMAGE_REPO}/prometheus:${COMPONENT_VERSIONS["prometheus"]}

# Push custom path using skopeo
echo "Pushing to custom path..."
REGISTRY_AUTH_FILE=${POLICY_DIR}/policy.json bin/skopeo-v1.16.1 copy --format oci --dest-tls-verify=false \
  docker-daemon:${IMAGE_REPO}/prometheus:${COMPONENT_VERSIONS["prometheus"]} \
  docker://${IMAGE_REPO}/prometheus:${COMPONENT_VERSIONS["prometheus"]} || {
    echo "Error pushing prometheus image to custom path"
  }

echo "prometheus processing complete"
echo ""

# Process Prometheus Pushgateway image
echo "Processing prometheus-pushgateway..."
docker pull quay.io/prometheus/pushgateway:${APP_VERSIONS["pushgateway"]} || {
  echo "Error pulling pushgateway:${APP_VERSIONS["pushgateway"]} - skipping"
}

# Tag and push Pushgateway image
echo "Tagging pushgateway for custom path..."
docker tag quay.io/prometheus/pushgateway:${APP_VERSIONS["pushgateway"]} ${IMAGE_REPO}/pushgateway:${APP_VERSIONS["pushgateway"]}

# Push custom path using skopeo
echo "Pushing pushgateway to custom path..."
REGISTRY_AUTH_FILE=${POLICY_DIR}/policy.json bin/skopeo-v1.16.1 copy --format oci --dest-tls-verify=false \
  docker-daemon:${IMAGE_REPO}/pushgateway:${APP_VERSIONS["pushgateway"]} \
  docker://${IMAGE_REPO}/pushgateway:${APP_VERSIONS["pushgateway"]} || {
    echo "Error pushing pushgateway image to custom path"
  }

echo "pushgateway processing complete"
echo ""

# Process kube-state-metrics image
echo "Processing kube-state-metrics..."
docker pull registry.k8s.io/kube-state-metrics/kube-state-metrics:${APP_VERSIONS["kube-state-metrics"]} || {
  echo "Error pulling kube-state-metrics:${APP_VERSIONS["kube-state-metrics"]} - skipping"
}

# Tag and push kube-state-metrics image
echo "Tagging kube-state-metrics for registry..."
docker tag registry.k8s.io/kube-state-metrics/kube-state-metrics:${APP_VERSIONS["kube-state-metrics"]} ${IMAGE_REPO}/kube-state-metrics:${APP_VERSIONS["kube-state-metrics"]}

# Push to registry
echo "Pushing kube-state-metrics to registry..."
REGISTRY_AUTH_FILE=${POLICY_DIR}/policy.json bin/skopeo-v1.16.1 copy --format oci --dest-tls-verify=false \
  docker-daemon:${IMAGE_REPO}/kube-state-metrics:${APP_VERSIONS["kube-state-metrics"]} \
  docker://${IMAGE_REPO}/kube-state-metrics:${APP_VERSIONS["kube-state-metrics"]} || {
    echo "Error pushing kube-state-metrics image"
  }

echo "kube-state-metrics processing complete"
echo ""

# Process node-exporter image
echo "Processing node-exporter..."
docker pull quay.io/prometheus/node-exporter:${APP_VERSIONS["node-exporter"]} || {
  echo "Error pulling node-exporter:${APP_VERSIONS["node-exporter"]} - skipping"
}

# Tag and push node-exporter image
echo "Tagging node-exporter for registry..."
docker tag quay.io/prometheus/node-exporter:${APP_VERSIONS["node-exporter"]} ${IMAGE_REPO}/node-exporter:${APP_VERSIONS["node-exporter"]}

# Push to registry
echo "Pushing node-exporter to registry..."
REGISTRY_AUTH_FILE=${POLICY_DIR}/policy.json bin/skopeo-v1.16.1 copy --format oci --dest-tls-verify=false \
  docker-daemon:${IMAGE_REPO}/node-exporter:${APP_VERSIONS["node-exporter"]} \
  docker://${IMAGE_REPO}/node-exporter:${APP_VERSIONS["node-exporter"]} || {
    echo "Error pushing node-exporter image"
  }

echo "node-exporter processing complete"
echo ""

# Step 4: Process dependency images
echo "Processing dependency images..."

# Nginx for Loki gateway
echo "Processing nginx-unprivileged image..."
docker pull nginxinc/nginx-unprivileged:${NGINX_VERSION} || {
  echo "Error pulling nginx-unprivileged:${NGINX_VERSION} - skipping"
}

# Tag and push nginx image
echo "Tagging nginx-unprivileged for local registry..."
docker tag nginxinc/nginx-unprivileged:${NGINX_VERSION} ${IMAGE_REPO}/nginx-unprivileged:${NGINX_VERSION}

echo "Pushing nginx-unprivileged to registry..."
REGISTRY_AUTH_FILE=${POLICY_DIR}/policy.json bin/skopeo-v1.16.1 copy --format oci --dest-tls-verify=false \
  docker-daemon:${IMAGE_REPO}/nginx-unprivileged:${NGINX_VERSION} \
  docker://${IMAGE_REPO}/nginx-unprivileged:${NGINX_VERSION} || {
    echo "Error pushing nginx-unprivileged image"
  }

echo "nginx-unprivileged processing complete"
echo ""

# k8s-sidecar for Loki and other components
echo "Processing k8s-sidecar image..."
docker pull kiwigrid/k8s-sidecar:${SIDECAR_VERSION} || {
  echo "Error pulling k8s-sidecar:${SIDECAR_VERSION} - skipping"
}

# Tag and push k8s-sidecar image
echo "Tagging k8s-sidecar for local registry..."
docker tag kiwigrid/k8s-sidecar:${SIDECAR_VERSION} ${IMAGE_REPO}/k8s-sidecar:${SIDECAR_VERSION}

echo "Pushing k8s-sidecar to registry..."
REGISTRY_AUTH_FILE=${POLICY_DIR}/policy.json bin/skopeo-v1.16.1 copy --format oci --dest-tls-verify=false \
  docker-daemon:${IMAGE_REPO}/k8s-sidecar:${SIDECAR_VERSION} \
  docker://${IMAGE_REPO}/k8s-sidecar:${SIDECAR_VERSION} || {
    echo "Error pushing k8s-sidecar image"
  }

echo "k8s-sidecar processing complete"
echo ""

# Step 5: Cleanup and verification
echo "Cleaning up..."
rm -f grafana-${COMPONENT_VERSIONS["grafana"]}.tgz
rm -f loki-${COMPONENT_VERSIONS["loki"]}.tgz
rm -f tempo-${COMPONENT_VERSIONS["tempo"]}.tgz
rm -f prometheus-${COMPONENT_VERSIONS["prometheus"]}.tgz

echo "Verifying registry contents..."
echo "Custom paths:"
for comp in grafana loki tempo prometheus pushgateway kube-state-metrics node-exporter nginx-unprivileged k8s-sidecar; do
  echo -n "${comp} tags: "
  curl -s -X GET http://${REGISTRY}/v2/githedgehog/lgtm/images/${comp}/tags/list | grep -o '"tags":\[[^]]*\]'
done

echo "LGTM setup complete. Components ready for use:"
for comp in "${!COMPONENT_VERSIONS[@]}"; do
  echo "  ${comp}: ${COMPONENT_VERSIONS[$comp]}"
done
