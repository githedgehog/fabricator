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
go_flags := "--tags containers_image_openpgp,containers_image_storage_stub -ldflags=\"-w -s -X go.githedgehog.com/fabricator/pkg/version.Version=" + version + "\""
go_build := "go build " + go_flags
go_linux_build := "GOOS=linux GOARCH=amd64 " + go_build

_touch_embed:
  @touch ./pkg/embed/recipebin/hhfab-recipe.gz
  @touch {{oem_dir}}/oem.cpio.gz
  @touch {{oem_dir}}/hhfab-flatcar-install

_hhfab_embed: _touch_embed _butane
  # Build hhfab-recipe binary for embedding
  {{go_linux_build}} -o ./pkg/embed/recipebin/hhfab-recipe ./cmd/hhfab-recipe
  gzip -fk ./pkg/embed/recipebin/hhfab-recipe

  # Build flatcar oem.cpio.gz for embedding
  @mkdir -p {{oem_dir}}/usr/share/oem
  {{go_linux_build}} -o {{oem_dir}}/hhfab-flatcar-install ./cmd/hhfab-flatcar-install
  {{butane}} --strict --output {{oem_dir}}/usr/share/oem/config.ign --files-dir {{oem_dir}} ./pkg/fab/recipe/control_build_usb_butane.yaml
  cd {{oem_dir}} && find usr | cpio -o -H newc | gzip -f > oem.cpio.gz

_kube_gen: _controller_gen
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
build: _license_headers _gotools hhfab-build hhfabctl-build && version
  {{go_linux_build}} -o ./bin/fabricator ./cmd
  # Build complete

# TODO rework by using existing recipes and installing with helm chart
# Run e2e tests on existing Kind cluster
# test-e2e:
#   go test ./test/e2e/ -v -ginkgo.v

oci_repo := "127.0.0.1:30000"
oci_prefix := "githedgehog/fabricator"

_helm-fabricator-api: _kustomize _helm _kube_gen
  @rm config/helm/fabricator-api-v*.tgz || true
  {{kustomize}} build config/crd > config/helm/fabricator-api/templates/crds.yaml
  {{helm}} package config/helm/fabricator-api --destination config/helm --version {{version}}
  {{helm}} lint config/helm/fabricator-api-{{version}}.tgz

_helm-fabricator: _kustomize _helm _helmify _kube_gen
  @rm config/helm/fabricator-v*.tgz || true
  @rm config/helm/fabricator/templates/*.yaml config/helm/fabricator/values.yaml || true
  {{kustomize}} build config/default | {{helmify}} config/helm/fabricator
  {{helm}} package config/helm/fabricator --destination config/helm --version {{version}}
  {{helm}} lint config/helm/fabricator-{{version}}.tgz

# Build all K8s artifacts (images and charts)
kube-build: build (_docker-build "fabricator") _helm-fabricator-api _helm-fabricator (_helm-build "ntp") && version
  # Docker images and Helm charts built

# Push all K8s artifacts (images and charts)
kube-push: kube-build (_helm-push "fabricator-api") (_kube-push "fabricator") (_helm-push "ntp") && version
  # Docker images and Helm charts pushed

# Push all K8s artifacts (images and charts) and binaries
push: kube-push _oras && version
  cd bin && oras push {{oras_insecure}} {{oci_repo}}/{{oci_prefix}}/hhfab:{{version}} hhfab
  cd bin && oras push {{oras_insecure}} {{oci_repo}}/{{oci_prefix}}/hhfabctl:{{version}} hhfabctl

_hhfab-push GOOS GOARCH: _oras (_hhfab-build GOOS GOARCH)
  cd bin/hhfab-{{GOOS}}-{{GOARCH}} && oras push {{oras_insecure}} {{oci_repo}}/{{oci_prefix}}/hhfab-{{GOOS}}-{{GOARCH}}:{{version}} hhfab

_hhfabctl-push GOOS GOARCH: _oras (_hhfabctl-build GOOS GOARCH)
  cd bin/hhfabctl-{{GOOS}}-{{GOARCH}} && oras push {{oras_insecure}} {{oci_repo}}/{{oci_prefix}}/hhfabctl-{{GOOS}}-{{GOARCH}}:{{version}} hhfabctl

# Publish hhfab and other user-facing binaries for all supported OS/Arch
push-multi: (_hhfab-push "linux" "amd64") (_hhfab-push "linux" "arm64") (_hhfab-push "darwin" "amd64") (_hhfab-push "darwin" "arm64") (_hhfabctl-push "linux" "amd64") (_hhfabctl-push "linux" "arm64") (_hhfabctl-push "darwin" "amd64") (_hhfabctl-push "darwin" "arm64") && version

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

# Patch deployment using the default kubeconfig (KUBECONFIG env or ~/.kube/config)
patch: && version
  kubectl -n fab patch helmchart/fabricator-api --type=merge -p '{"spec":{"version":"{{version}}"}}'
  kubectl -n fab patch helmchart/fabricator --type=merge -p '{"spec":{"version":"{{version}}", "set":{"ctrl.manager.image.tag":"{{version}}"}}}'

#
# Setup local registry
#
zot_version := "v2.1.1"
zot_os := `hack/os.sh`
zot_arch := `hack/arch.sh`
zot := localbin / "zot" + "-" + zot_os + "-" + zot_arch + "-" + zot_version

@_zot: _localbin
  [ -f {{zot}} ] || wget --quiet -O {{zot}} https://github.com/project-zot/zot/releases/download/{{zot_version}}/zot-{{zot_os}}-{{zot_arch}} && chmod +x {{zot}}

_localreg: _zot
  ./hack/localreg.sh
  {{zot}} serve .zot/config.json 2>&1 | tee .zot/log
