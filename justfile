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

# Generate code/manifests, things to embed, etc
gen: _kube_gen _hhfab_embed

_hhfab_build: _license_headers _gotools _kube_gen _hhfab_embed
  {{go_linux_build}} -o ./bin/hhfab ./cmd/hhfab

# Build hhfab for local OS/Arch
hhfab-build-local: _license_headers _gotools _kube_gen _hhfab_embed && version
  {{go_build}} -o ./bin/hhfab ./cmd/hhfab

# Build all artifacts
build: _license_headers _gotools _hhfab_build && version
  @echo "Build complete"

# .PHONY: test
# test: manifests generate fmt vet envtest ## Run tests.
# 	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test $$(go list ./... | grep -v /e2e) -coverprofile cover.out

# # Utilize Kind or modify the e2e tests to load the image locally, enabling compatibility with other vendors.
# .PHONY: test-e2e  # Run the e2e tests against a Kind k8s instance that is spun up.
# test-e2e:
# 	go test ./test/e2e/ -v -ginkgo.v
