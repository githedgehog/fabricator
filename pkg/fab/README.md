# Artifacts preparation

## Flatcar

Stable version is listed on the [Flatcar releases page](https://www.flatcar.org/releases).

```bash
export FLATCAR_VERSION="v3975.2.1"
export FLATCAR_VERSION_UPSTREAM="${FLATCAR_VERSION:1}"

wget "https://stable.release.flatcar-linux.net/amd64-usr/${FLATCAR_VERSION_UPSTREAM}/flatcar_production_qemu_image.img"
wget "https://stable.release.flatcar-linux.net/amd64-usr/${FLATCAR_VERSION_UPSTREAM}/flatcar_production_qemu_uefi_efi_code.fd"
wget "https://stable.release.flatcar-linux.net/amd64-usr/${FLATCAR_VERSION_UPSTREAM}/flatcar_production_qemu_uefi_efi_vars.fd"

mv flatcar_production_qemu_image.img flatcar.img
mv flatcar_production_qemu_uefi_efi_code.fd flatcar_efi_code.fd
mv flatcar_production_qemu_uefi_efi_vars.fd flatcar_efi_vars.fd

ls -lah

oras push "ghcr.io/githedgehog/flatcar-vlab:${FLATCAR_VERSION}" flatcar.img flatcar_efi_code.fd flatcar_efi_vars.fd
```

## K3s

```bash
export K3S_VERSION="v1.31.0-k3s1"
export K3S_VERSION_UPSTREAM="${K3S_VERSION//-/+}"

wget "https://github.com/k3s-io/k3s/releases/download/${K3S_VERSION_UPSTREAM}/k3s"
wget "https://github.com/k3s-io/k3s/releases/download/${K3S_VERSION_UPSTREAM}/k3s-airgap-images-amd64.tar.gz"
wget "https://raw.githubusercontent.com/k3s-io/k3s/${K3S_VERSION_UPSTREAM}/install.sh"

mv install.sh k3s-install.sh

oras push "ghcr.io/githedgehog/fabricator/k3s:${K3S_VERSION}" k3s k3s-airgap-images-amd64.tar.gz k3s-install.sh
```

## Zot

```bash
export ZOT_VERSION="v2.1.1"
export ZOT_CHART_VERSION="0.1.60"

helm repo add project-zot http://zotregistry.dev/helm-charts
helm repo update project-zot

helm pull project-zot/zot --version "${ZOT_CHART_VERSION}"
mv zot-${ZOT_CHART_VERSION}.tgz "zot-${ZOT_VERSION}".tgz
helm push "zot-${ZOT_VERSION}.tgz" oci://ghcr.io/githedgehog/fabricator/charts
oras push "ghcr.io/githedgehog/fabricator/zot-chart:${ZOT_VERSION}" "zot-${ZOT_VERSION}.tgz"

skopeo copy "docker://ghcr.io/project-zot/zot-linux-amd64:${ZOT_VERSION}" "docker://ghcr.io/githedgehog/fabricator/zot:${ZOT_VERSION}"

docker image rm "ghcr.io/githedgehog/fabricator/zot:${ZOT_VERSION}"
docker pull --platform linux/amd64 "ghcr.io/githedgehog/fabricator/zot:${ZOT_VERSION}"
docker save -o zot-airgap-images-amd64.tar.gz "ghcr.io/githedgehog/fabricator/zot:${ZOT_VERSION}"
oras push "ghcr.io/githedgehog/fabricator/zot-airgap:${ZOT_VERSION}" zot-airgap-images-amd64.tar.gz
```

## Cert-manager

```bash
export CERT_MANAGER_VERSION="v1.15.3"

helm repo add jetstack https://charts.jetstack.io
helm repo update jetstack

helm pull jetstack/cert-manager --version "${CERT_MANAGER_VERSION}"
helm push "cert-manager-${CERT_MANAGER_VERSION}.tgz" oci://ghcr.io/githedgehog/fabricator/charts
oras push "ghcr.io/githedgehog/fabricator/cert-manager-chart:${CERT_MANAGER_VERSION}" "cert-manager-${CERT_MANAGER_VERSION}.tgz"

skopeo copy --all docker://{quay.io/jetstack,ghcr.io/githedgehog/fabricator}/cert-manager-controller:${CERT_MANAGER_VERSION}
skopeo copy --all docker://{quay.io/jetstack,ghcr.io/githedgehog/fabricator}/cert-manager-webhook:${CERT_MANAGER_VERSION}
skopeo copy --all docker://{quay.io/jetstack,ghcr.io/githedgehog/fabricator}/cert-manager-cainjector:${CERT_MANAGER_VERSION}
skopeo copy --all docker://{quay.io/jetstack,ghcr.io/githedgehog/fabricator}/cert-manager-acmesolver:${CERT_MANAGER_VERSION}
skopeo copy --all docker://{quay.io/jetstack,ghcr.io/githedgehog/fabricator}/cert-manager-startupapicheck:${CERT_MANAGER_VERSION}

docker image rm "ghcr.io/githedgehog/fabricator/cert-manager-controller:${CERT_MANAGER_VERSION}"
docker image rm "ghcr.io/githedgehog/fabricator/cert-manager-webhook:${CERT_MANAGER_VERSION}"
docker image rm "ghcr.io/githedgehog/fabricator/cert-manager-cainjector:${CERT_MANAGER_VERSION}"
docker image rm "ghcr.io/githedgehog/fabricator/cert-manager-acmesolver:${CERT_MANAGER_VERSION}"
docker image rm "ghcr.io/githedgehog/fabricator/cert-manager-startupapicheck:${CERT_MANAGER_VERSION}"

docker pull --platform linux/amd64 "ghcr.io/githedgehog/fabricator/cert-manager-controller:${CERT_MANAGER_VERSION}"
docker pull --platform linux/amd64 "ghcr.io/githedgehog/fabricator/cert-manager-webhook:${CERT_MANAGER_VERSION}"
docker pull --platform linux/amd64 "ghcr.io/githedgehog/fabricator/cert-manager-cainjector:${CERT_MANAGER_VERSION}"
docker pull --platform linux/amd64 "ghcr.io/githedgehog/fabricator/cert-manager-acmesolver:${CERT_MANAGER_VERSION}"
docker pull --platform linux/amd64 "ghcr.io/githedgehog/fabricator/cert-manager-startupapicheck:${CERT_MANAGER_VERSION}"

docker save -o cert-manager-airgap-images-amd64.tar.gz ghcr.io/githedgehog/fabricator/cert-manager-{controller,webhook,cainjector,acmesolver,startupapicheck}:${CERT_MANAGER_VERSION}
oras push "ghcr.io/githedgehog/fabricator/cert-manager-airgap:${CERT_MANAGER_VERSION}" cert-manager-airgap-images-amd64.tar.gz
```