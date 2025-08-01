set shell := ["bash", "-euo", "pipefail", "-c"]

import "hack/tools.just"

# Print list of available recipes
default:
  @just --list

export CGO_ENABLED := "0"

_gotools: _touch_embed
  go fmt ./...
  go vet {{go_flags}} ./...

# Called in CI
_lint: _license_headers _gotools

# Generate, lint, test and build everything
all: gen lint lint-gha test build kube-build && version

# Run linters against code (incl. license headers)
lint: _lint _golangci_lint
  {{golangci_lint}} run --show-stats ./...

# Run golangci-lint to attempt to fix issues
lint-fix: _lint _golangci_lint
  {{golangci_lint}} run --show-stats --fix ./...

oem_dir := "./pkg/embed/flatcaroem"
go_base_flags := "--tags containers_image_openpgp,containers_image_storage_stub"
go_flags := go_base_flags + " -ldflags=\"-w -s -X go.githedgehog.com/fabricator/pkg/version.Version=" + version + "\""
go_build := "go build " + go_flags
go_linux_build := "GOOS=linux GOARCH=amd64 " + go_build

_touch_embed:
  @touch ./pkg/embed/recipebin/hhfab-recipe.gz
  @touch {{oem_dir}}/oem.cpio.gz
  @touch {{oem_dir}}/hhfab-flatcar-install

_hhfab_embed: _touch_embed
  # Build hhfab-recipe binary for embedding
  {{go_linux_build}} -o ./pkg/embed/recipebin/hhfab-recipe ./cmd/hhfab-recipe
  gzip -fk ./pkg/embed/recipebin/hhfab-recipe

  # Build flatcar oem.cpio.gz for embedding
  @mkdir -p {{oem_dir}}/usr/share/oem
  {{go_linux_build}} -o {{oem_dir}}/hhfab-flatcar-install ./cmd/hhfab-flatcar-install
  {{butane}} --strict --output {{oem_dir}}/usr/share/oem/config.ign --files-dir {{oem_dir}} ./pkg/fab/recipe/flatcar/os_install_butane.yaml
  cd {{oem_dir}} && find usr | cpio -o -H newc | gzip -f > oem.cpio.gz

_kube_gen:
  # Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject implementations
  {{controller_gen}} object:headerFile="hack/boilerplate.go.txt" paths="./..."
  # Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects
  {{controller_gen}} rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases

# Generate docs, code/manifests, things to embed, etc
gen: _kube_gen _hhfab_embed _crd_ref_docs
  {{crd_ref_docs}} --source-path=./api/ --config=api/docs.config.yaml --renderer=markdown --output-path=./docs/api.md

hhfab-build: _license_headers _gotools _kube_gen _hhfab_embed && version
  {{go_linux_build}} -o ./bin/hhfab ./cmd/hhfab

hhfabctl-build: _license_headers _gotools _kube_gen && version
  {{go_linux_build}} -o ./bin/hhfabctl ./cmd/hhfabctl

# Build hhfab for local OS/Arch
hhfab-build-local: _license_headers _gotools _kube_gen _hhfab_embed && version
  {{go_build}} -o ./bin/hhfab ./cmd/hhfab

_hhfab-build GOOS GOARCH: _license_headers _gotools _kube_gen _hhfab_embed
  GOOS={{GOOS}} GOARCH={{GOARCH}} {{go_build}} -o ./bin/hhfab-{{GOOS}}-{{GOARCH}}/hhfab ./cmd/hhfab
  cd bin && tar -czvf hhfab-{{GOOS}}-{{GOARCH}}-{{version}}.tar.gz hhfab-{{GOOS}}-{{GOARCH}}/hhfab

_hhfabctl-build GOOS GOARCH: _license_headers _gotools _kube_gen
  GOOS={{GOOS}} GOARCH={{GOARCH}} {{go_build}} -o ./bin/hhfabctl-{{GOOS}}-{{GOARCH}}/hhfabctl ./cmd/hhfabctl
  cd bin && tar -czvf hhfabctl-{{GOOS}}-{{GOARCH}}-{{version}}.tar.gz hhfabctl-{{GOOS}}-{{GOARCH}}/hhfabctl

# Build hhfab and other user-facing binaries for all supported OS/Arch
build-multi: (_hhfab-build "linux" "amd64") (_hhfab-build "linux" "arm64") (_hhfab-build "darwin" "amd64") (_hhfab-build "darwin" "arm64") (_hhfabctl-build "linux" "amd64") (_hhfabctl-build "linux" "arm64") (_hhfabctl-build "darwin" "amd64") (_hhfabctl-build "darwin" "arm64") && version

# Build all artifacts
build: _license_headers gen _gotools hhfab-build hhfabctl-build && version
  {{go_linux_build}} -o ./bin/fabricator ./cmd
  {{go_linux_build}} -o ./bin/hhfab-node-config ./cmd/hhfab-node-config
  # Build complete

# TODO rework by using existing recipes and installing with helm chart
# Run e2e tests on existing Kind cluster
# test-e2e:
#   go test ./test/e2e/ -v -ginkgo.v

oci_repo := "127.0.0.1:30000"
oci_prefix := "githedgehog/fabricator"

_helm-fabricator-api: _helm _kube_gen
  @rm config/helm/fabricator-api-v*.tgz || true
  {{kustomize}} build config/crd > config/helm/fabricator-api/templates/crds.yaml
  {{helm}} package config/helm/fabricator-api --destination config/helm --version {{version}}
  {{helm}} lint config/helm/fabricator-api-{{version}}.tgz

_helm-fabricator: _helm _helmify _kube_gen
  @rm config/helm/fabricator-v*.tgz || true
  @rm config/helm/fabricator/templates/*.yaml config/helm/fabricator/values.yaml || true
  {{kustomize}} build config/default | {{helmify}} config/helm/fabricator
  {{helm}} package config/helm/fabricator --destination config/helm --version {{version}}
  {{helm}} lint config/helm/fabricator-{{version}}.tgz

# Build all K8s artifacts (images and charts)
kube-build: build (_docker-build "fabricator") (_docker-build "hhfab-node-config") _helm-fabricator-api _helm-fabricator (_helm-build "ntp") && version
  # Docker images and Helm charts built

# Push all K8s artifacts (images and charts)
kube-push: kube-build (_helm-push "fabricator-api") (_kube-push "fabricator") (_docker-push "hhfab-node-config") (_helm-push "ntp") && version
  # Docker images and Helm charts pushed

_hhfab-push-main: _oras hhfab-build && version
  cd bin && {{localpath}}/{{oras}} push {{oras_insecure}} {{oci_repo}}/{{oci_prefix}}/hhfab:{{version}} hhfab

_hhfabctl-push-main: _oras hhfabctl-build && version
  cd bin && {{localpath}}/{{oras}} push {{oras_insecure}} {{oci_repo}}/{{oci_prefix}}/hhfabctl:{{version}} hhfabctl

# Push all K8s artifacts (images and charts) and binaries
push: kube-push _hhfab-push-main _hhfabctl-push-main && version

_hhfab-push GOOS GOARCH: _oras (_hhfab-build GOOS GOARCH)
  cd bin/hhfab-{{GOOS}}-{{GOARCH}} && {{localpath}}/{{oras}} push {{oras_insecure}} {{oci_repo}}/{{oci_prefix}}/hhfab-{{GOOS}}-{{GOARCH}}:{{version}} hhfab

_hhfabctl-push GOOS GOARCH: _oras (_hhfabctl-build GOOS GOARCH)
  cd bin/hhfabctl-{{GOOS}}-{{GOARCH}} && {{localpath}}/{{oras}} push {{oras_insecure}} {{oci_repo}}/{{oci_prefix}}/hhfabctl-{{GOOS}}-{{GOARCH}}:{{version}} hhfabctl

_hhfab-push-multi: (_hhfab-push "linux" "amd64") (_hhfab-push "linux" "arm64") (_hhfab-push "darwin" "amd64") (_hhfab-push "darwin" "arm64")

_hhfabctl-push-multi: (_hhfabctl-push "linux" "amd64") (_hhfabctl-push "linux" "arm64") (_hhfabctl-push "darwin" "amd64") (_hhfabctl-push "darwin" "arm64")

# Publish hhfab and other user-facing binaries for all supported OS/Arch
push-multi: _hhfab-push-multi _hhfabctl-push-multi && version

# Install API on a kind cluster and wait for CRDs to be ready
test-api: _helm-fabricator-api
    kind export kubeconfig --name kind || kind create cluster --name kind
    kind export kubeconfig --name kind
    {{helm}} install -n default fabricator-api config/helm/fabricator-api-{{version}}.tgz
    sleep 10
    kubectl wait --for condition=established --timeout=60s crd/fabricators.fabricator.githedgehog.com
    kubectl wait --for condition=established --timeout=60s crd/controlnodes.fabricator.githedgehog.com
    kubectl get crd | grep fabricator
    kind delete cluster --name kind

# Run VLAB Trivy security scan with configurable options
security-scan *args="": && version
  @echo "Checking prerequisites for VLAB security scan..."
  @if [ ! -f "bin/hhfab" ]; then echo "ERROR: hhfab binary not found. Run 'just push' first."; exit 1; fi
  @echo "Running VLAB Trivy security scan..."
  @echo "Available options:"
  @echo "  --control-only     Run only control VM setup and scanning"
  @echo "  --gateway-only     Run only gateway VM setup and scanning"
  @echo "  --skip-vlab        Skip launching VLAB (assumes VLAB is already running)"
  @echo "  --strict           Require all scans to succeed (no partial successes)"
  @echo "  --help, -h         Show help message"
  @echo ""
  ./hack/vlab-trivy-runner.sh {{args}}

# Patch deployment using the default kubeconfig (KUBECONFIG env or ~/.kube/config)
patch: && version
  kubectl -n fab patch fab/default --type=merge -p '{"spec":{"overrides":{"versions":{"fabricator":{"api":"{{version}}","controller":"{{version}}","ctl":"{{version}}","nodeConfig":"{{version}}"}}}}}'

#
# Setup local registry
#
zot_version := "v2.1.5"
zot_os := `hack/os.sh`
zot_arch := `hack/arch.sh`
zot := localbin / "zot" + "-" + zot_os + "-" + zot_arch + "-" + zot_version

@_zot: _localbin
  [ -f {{zot}} ] || wget --quiet -O {{zot}} https://github.com/project-zot/zot/releases/download/{{zot_version}}/zot-{{zot_os}}-{{zot_arch}} && chmod +x {{zot}}

_localreg: _zot
  ./hack/localreg.sh
  {{zot}} serve .zot/config.json 2>&1 | tee .zot/log

# Run specified command with args with minimal Go flags (no version provided)
run cmd *args:
  @echo "Running: {{cmd}} {{args}} (run gen manually if needed)"
  @go run {{go_base_flags}} ./cmd/{{cmd}} {{args}}

#
# Generate diagrams for multiple environments in different formats and styles
#
test-diagram y="":
  @mkdir -p test-diagram
  @echo "==============================================="
  @echo "Diagram generation test - various topologies, formats, and styles"
  @echo "==============================================="

  # Check if a VLAB is actually running
  @echo "=== Checking for running VLAB ==="
  @VLAB_PIDS=$(pgrep -f "[h]hfab vlab up" 2>/dev/null || echo ""); \
  if [ -n "$VLAB_PIDS" ] && [ -f "vlab/kubeconfig" ]; then \
    echo "=== Detected running VLAB, generating live diagrams ==="; \
    bin/hhfab diagram --format drawio --style default --live --output test-diagram/live-drawio-default.drawio || echo "Failed to generate live DrawIO diagram"; \
    bin/hhfab diagram --format drawio --style cisco --live --output test-diagram/live-drawio-cisco.drawio || echo "Failed to generate live DrawIO cisco diagram"; \
    bin/hhfab diagram --format drawio --style hedgehog --live --output test-diagram/live-drawio-hedgehog.drawio || echo "Failed to generate live DrawIO hedgehog diagram"; \
    bin/hhfab diagram --format dot --live --output test-diagram/live-dot.dot || echo "Failed to generate live DOT diagram"; \
    if command -v dot >/dev/null 2>&1 && [ -f "test-diagram/live-dot.dot" ]; then \
      dot -Tpng test-diagram/live-dot.dot -o test-diagram/live-dot.png; \
    fi; \
    bin/hhfab diagram --format mermaid --live --output test-diagram/live-mermaid.mermaid || echo "Failed to generate live Mermaid diagram"; \
    if [ -f "test-diagram/live-mermaid.mermaid" ]; then \
      echo '# Live Network Diagram' > test-diagram/live-mermaid.md; \
      echo '```mermaid' >> test-diagram/live-mermaid.md; \
      cat test-diagram/live-mermaid.mermaid >> test-diagram/live-mermaid.md; \
      echo '```' >> test-diagram/live-mermaid.md; \
    fi; \
  else \
    echo "No running VLAB detected, skipping live diagrams"; \
  fi

  # Skip prompt if -y flag is provided
  @if [ "{{y}}" = "-y" ]; then \
    true; \
  else \
    echo -n "This will generate diagrams from multiple environments. Continue? [y/N] " && read ans && [ "$ans" = "y" -o "$ans" = "Y" ]; \
  fi

  @echo "=== Generating diagrams for default VLAB topology ==="
  bin/hhfab init -f --dev --gw
  bin/hhfab vlab gen

  # Generate all formats and styles for default topology
  bin/hhfab diagram --format drawio --style default --output test-diagram/default-drawio-default.drawio
  bin/hhfab diagram --format drawio --style cisco --output test-diagram/default-drawio-cisco.drawio
  bin/hhfab diagram --format drawio --style hedgehog --output test-diagram/default-drawio-hedgehog.drawio
  bin/hhfab diagram --format dot --output test-diagram/default-dot.dot
  bin/hhfab diagram --format mermaid --output test-diagram/default-mermaid.mermaid

  @echo "=== Generating diagrams for variant 3-spine topology ==="
  bin/hhfab vlab gen --spines-count 3 --mclag-leafs-count 2 --orphan-leafs-count 1 --eslag-leaf-groups 2

  # Generate all formats and styles for 3-spine topology
  bin/hhfab diagram --format drawio --style default --output test-diagram/3spine-drawio-default.drawio
  bin/hhfab diagram --format drawio --style cisco --output test-diagram/3spine-drawio-cisco.drawio
  bin/hhfab diagram --format drawio --style hedgehog --output test-diagram/3spine-drawio-hedgehog.drawio
  bin/hhfab diagram --format dot --output test-diagram/3spine-dot.dot
  bin/hhfab diagram --format mermaid --output test-diagram/3spine-mermaid.mermaid

  @echo "=== Generating diagrams for 4-mclag-2-orphan topology ==="
  bin/hhfab vlab gen --mclag-leafs-count 4 --orphan-leafs-count 2

  # Generate all formats and styles for 4-mclag-2-orphan topology
  bin/hhfab diagram --format drawio --style default --output test-diagram/4mclag2orphan-drawio-default.drawio
  bin/hhfab diagram --format drawio --style cisco --output test-diagram/4mclag2orphan-drawio-cisco.drawio
  bin/hhfab diagram --format drawio --style hedgehog --output test-diagram/4mclag2orphan-drawio-hedgehog.drawio
  bin/hhfab diagram --format dot --output test-diagram/4mclag2orphan-dot.dot
  bin/hhfab diagram --format mermaid --output test-diagram/4mclag2orphan-mermaid.mermaid

  @echo "=== Generating diagrams for collapsed core topology ==="
  bin/hhfab init -f --dev --registry-repo localhost:30000 --fabric-mode collapsed-core
  bin/hhfab vlab gen

  # Generate all formats and styles for collapsed core topology
  bin/hhfab diagram --format drawio --style default --output test-diagram/collapsed-core-drawio-default.drawio
  bin/hhfab diagram --format drawio --style cisco --output test-diagram/collapsed-core-drawio-cisco.drawio
  bin/hhfab diagram --format drawio --style hedgehog --output test-diagram/collapsed-core-drawio-hedgehog.drawio
  bin/hhfab diagram --format dot --output test-diagram/collapsed-core-dot.dot
  bin/hhfab diagram --format mermaid --output test-diagram/collapsed-core-mermaid.mermaid

  # Convert DOT files to PNG if GraphViz is installed
  @echo "=== Converting DOT files to PNG if GraphViz is installed ==="
  @if command -v dot >/dev/null 2>&1; then \
    for DOT_FILE in test-diagram/*-dot.dot; do \
      PNG_FILE="${DOT_FILE%.dot}.png"; \
      echo "Converting $DOT_FILE to $PNG_FILE"; \
      dot -Tpng "$DOT_FILE" -o "$PNG_FILE"; \
    done; \
  else \
    echo "GraphViz dot not installed, skipping PNG conversion"; \
  fi

  # Create markdown files with embedded mermaid diagrams
  @echo "=== Creating Markdown files with embedded Mermaid diagrams ==="
  @for MERMAID_FILE in test-diagram/*-mermaid.mermaid; do \
    MD_FILE="${MERMAID_FILE%.mermaid}.md"; \
    BASE_NAME=$(basename "$MERMAID_FILE" -mermaid.mermaid); \
    echo "Creating $MD_FILE"; \
    echo "# $BASE_NAME Network Diagram" > "$MD_FILE"; \
    echo '```mermaid' >> "$MD_FILE"; \
    cat "$MERMAID_FILE" >> "$MD_FILE"; \
    echo '```' >> "$MD_FILE"; \
  done

  @echo ""
  @echo "All diagrams generated in test-diagram/ directory"
  @ls -la test-diagram/
  @echo ""
  @echo "Summary of generated files:"
  @echo "- Default VLAB topology: default-*"
  @echo "- 3-spine VLAB topology: 3spine-*"
  @echo "- 4-mclag-2-orphan topology: 4mclag2orphan-*"
  @echo "- Collapsed core topology: collapsed-core-*"
  @echo "- Live diagrams (if VLAB running): live-*"
  @echo ""
  @echo "For each topology, these formats are available:"
  @echo "- DrawIO: *-drawio-{default,cisco,hedgehog}.drawio"
  @echo "- DOT: *-dot.dot (and PNG if GraphViz was installed)"
  @echo "- Mermaid: *-mermaid.mermaid (and embedded in markdown *.md)"

bump component version ref="":
  #!/usr/bin/env bash
  set -euo pipefail

  tidy=false
  if [ "{{component}}" == "fabric" ]; then
    echo "Bumping fabric version to {{version}} ref {{ref}}"
    sed -i.bak "s/^\tFabricVersion.*/\tFabricVersion=meta.Version(\"{{ version }}\")/" pkg/fab/versions.go
    go get go.githedgehog.com/fabric@{{ ref }}
    tidy=true
  elif [ "{{component}}" == "gateway" ]; then
    echo "Bumping gateway version to {{version}} ref {{ref}}"
    sed -i.bak "s/^\tGatewayVersion.*/\tGatewayVersion=meta.Version(\"{{ version }}\")/" pkg/fab/versions.go
    go get go.githedgehog.com/gateway@{{ ref }}
    tidy=true
  elif [ "{{component}}" == "dataplane" ]; then
    echo "Bumping dataplane version to {{version}}"
    sed -i.bak "s/^\tDataplaneVersion.*/\tDataplaneVersion=meta.Version(\"{{ version }}\")/" pkg/fab/versions.go
  elif [ "{{component}}" == "frr" ]; then
    echo "Bumping frr version to {{version}}"
    sed -i.bak "s/^\tFRRVersion.*/\tFRRVersion=meta.Version(\"{{ version }}\")/" pkg/fab/versions.go
  else
    echo "Unknown component: {{component}}"
    exit 1
  fi

  if [ "$tidy" == "true" ]; then
    go mod tidy && go mod vendor && git add vendor
  fi

  rm pkg/fab/versions.go.bak
  go fmt pkg/fab/versions.go 1>/dev/null
