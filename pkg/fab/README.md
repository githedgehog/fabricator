# Artifacts preparation

## Flatcar

Stable version is listed on the [Flatcar releases page](https://www.flatcar.org/releases).

```bash
export FLATCAR_VERSION="v4230.2.0"
export FLATCAR_VERSION_UPSTREAM="${FLATCAR_VERSION:1}"

wget "https://stable.release.flatcar-linux.net/amd64-usr/${FLATCAR_VERSION_UPSTREAM}/flatcar_production_qemu_image.img"
wget "https://stable.release.flatcar-linux.net/amd64-usr/${FLATCAR_VERSION_UPSTREAM}/flatcar_production_qemu_uefi_efi_code.qcow2"
wget "https://stable.release.flatcar-linux.net/amd64-usr/${FLATCAR_VERSION_UPSTREAM}/flatcar_production_qemu_uefi_efi_vars.qcow2"

mv flatcar_production_qemu_image.img flatcar.img
mv flatcar_production_qemu_uefi_efi_code.qcow2 flatcar_efi_code.fd
mv flatcar_production_qemu_uefi_efi_vars.qcow2 flatcar_efi_vars.fd

wget "https://update.release.flatcar-linux.net/amd64-usr/${FLATCAR_VERSION_UPSTREAM}/flatcar_production_update.gz"

ls -lah

oras push "ghcr.io/githedgehog/fabricator/flatcar-vlab:${FLATCAR_VERSION}" flatcar.img flatcar_efi_code.fd flatcar_efi_vars.fd
oras push "ghcr.io/githedgehog/fabricator/flatcar-update:${FLATCAR_VERSION}" flatcar_production_update.gz
```

## K3s

When bumping k3s version you may need to update the Fabricator's pause version as well as we're using the one from k3s.

```bash
export K3S_VERSION="v1.33.2-k3s1"
export K3S_VERSION_UPSTREAM="${K3S_VERSION//-/+}"

wget "https://github.com/k3s-io/k3s/releases/download/${K3S_VERSION_UPSTREAM}/k3s"
wget "https://github.com/k3s-io/k3s/releases/download/${K3S_VERSION_UPSTREAM}/k3s-airgap-images-amd64.tar.gz"
wget "https://raw.githubusercontent.com/k3s-io/k3s/${K3S_VERSION_UPSTREAM}/install.sh"

mv install.sh k3s-install.sh

oras push "ghcr.io/githedgehog/fabricator/k3s-airgap:${K3S_VERSION}" k3s k3s-airgap-images-amd64.tar.gz k3s-install.sh
```

## Zot

```bash
export ZOT_VERSION="v2.1.5"
export ZOT_CHART_VERSION="0.1.75"

helm repo add project-zot http://zotregistry.dev/helm-charts
helm repo update project-zot

helm pull project-zot/zot --version "${ZOT_CHART_VERSION}"
tar xzf "zot-${ZOT_CHART_VERSION}.tgz"
helm package zot --version "${ZOT_VERSION}"
helm push "zot-${ZOT_VERSION}.tgz" oci://ghcr.io/githedgehog/fabricator/charts

skopeo copy "docker://ghcr.io/project-zot/zot-linux-amd64:${ZOT_VERSION}" "docker://ghcr.io/githedgehog/fabricator/zot:${ZOT_VERSION}"

docker image rm "ghcr.io/githedgehog/fabricator/zot:${ZOT_VERSION}" || true
docker pull --platform linux/amd64 "ghcr.io/githedgehog/fabricator/zot:${ZOT_VERSION}"
docker save -o zot-airgap-images-amd64.tar "ghcr.io/githedgehog/fabricator/zot:${ZOT_VERSION}"
pigz -v -c zot-airgap-images-amd64.tar > zot-airgap-images-amd64.tar.gz

cp zot-${ZOT_VERSION}.tgz zot-chart.tgz
oras push "ghcr.io/githedgehog/fabricator/zot-airgap:${ZOT_VERSION}" zot-airgap-images-amd64.tar.gz zot-chart.tgz
```

## Cert-manager

```bash
export CERT_MANAGER_VERSION="v1.18.2"

helm repo add jetstack https://charts.jetstack.io
helm repo update jetstack

helm pull jetstack/cert-manager --version "${CERT_MANAGER_VERSION}"
helm push "cert-manager-${CERT_MANAGER_VERSION}.tgz" oci://ghcr.io/githedgehog/fabricator/charts

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

docker save -o cert-manager-airgap-images-amd64.tar ghcr.io/githedgehog/fabricator/cert-manager-{controller,webhook,cainjector,acmesolver,startupapicheck}:${CERT_MANAGER_VERSION}
pigz -v -c cert-manager-airgap-images-amd64.tar > cert-manager-airgap-images-amd64.tar.gz

cp cert-manager-${CERT_MANAGER_VERSION}.tgz cert-manager-chart.tgz
oras push "ghcr.io/githedgehog/fabricator/cert-manager-airgap:${CERT_MANAGER_VERSION}" cert-manager-airgap-images-amd64.tar.gz cert-manager-chart.tgz
```

## K9s

```bash
export K9S_VERSION="v0.50.7"

wget "https://github.com/derailed/k9s/releases/download/${K9S_VERSION}/k9s_Linux_amd64.tar.gz"
tar -xzvf k9s_Linux_amd64.tar.gz

oras push "ghcr.io/githedgehog/fabricator/k9s:${K9S_VERSION}" k9s
```

## ONIE

Manually prepared ONIE image. Probably should be shrunk to the minimum size using `qemu-img convert -O qcow2 <from> <to>`.

```bash
export ONIE_VERSION="v0.2.0"

oras push "ghcr.io/githedgehog/fabricator/onie-vlab:${ONIE_VERSION}" onie-kvm_x86_64.qcow2 onie_efi_code.fd onie_efi_vars.fd
```

## NTP

Image is basically taken from cturra/ntp:latest at some point in time.

```bash
export NTP_VERSION="v0.0.2"

docker image rm cturra/ntp:latest
docker pull --platform linux/amd64 cturra/ntp:latest
docker tag cturra/ntp:latest ghcr.io/githedgehog/fabricator/ntp:${NTP_VERSION}
docker push ghcr.io/githedgehog/fabricator/ntp:${NTP_VERSION}
```

## Broadcom SONiC

```bash
export SONIC_VERSION="v4.5.0"

oras push ghcr.io/githedgehog/sonic-bcm-private/sonic-bcm-advanced:${SONIC_VERSION} sonic-broadcom-enterprise-advanced.bin
oras push ghcr.io/githedgehog/sonic-bcm-private/sonic-bcm-campus:${SONIC_VERSION} sonic-broadcom-campus.bin
oras push ghcr.io/githedgehog/sonic-bcm-private/sonic-bcm-base:${SONIC_VERSION} sonic-broadcom-enterprise-base.bin
oras push ghcr.io/githedgehog/sonic-bcm-private/sonic-bcm-vs:${SONIC_VERSION} sonic-vs.bin
```

## Grafana Alloy

```bash
export ALLOY_VERSION="v1.9.2"
export ALLOY_CHART_VERSION="1.1.2"

helm repo add grafana https://grafana.github.io/helm-charts
helm repo update

helm pull grafana/alloy --version "${ALLOY_CHART_VERSION}"
tar xzf "alloy-${ALLOY_CHART_VERSION}.tgz"
helm package alloy --version "${ALLOY_VERSION}"
helm push "alloy-${ALLOY_VERSION}.tgz" oci://ghcr.io/githedgehog/fabricator/charts
rm -rf alloy*

skopeo copy --all "docker://docker.io/grafana/alloy:${ALLOY_VERSION}" "docker://ghcr.io/githedgehog/fabricator/alloy:${ALLOY_VERSION}"

wget "https://github.com/grafana/alloy/releases/download/${ALLOY_VERSION}/alloy-linux-amd64.zip"
unzip alloy-linux-amd64.zip
mv alloy-linux-amd64 alloy
oras push "ghcr.io/githedgehog/fabricator/alloy-bin:${ALLOY_VERSION}" alloy
```

## Reloader

```bash
export RELOADER_VERSION="v1.4.5"
export RELOADER_CHART_VERSION="2.1.5"

helm repo add stakater https://stakater.github.io/stakater-charts
helm repo update

helm pull stakater/reloader --version "${RELOADER_CHART_VERSION}"
tar xzf "reloader-${RELOADER_CHART_VERSION}.tgz"
helm package reloader --version "${RELOADER_VERSION}"
helm push "reloader-${RELOADER_VERSION}.tgz" oci://ghcr.io/githedgehog/fabricator/charts
rm -rf reloader*

skopeo copy --all "docker://ghcr.io/stakater/reloader:${RELOADER_VERSION}" "docker://ghcr.io/githedgehog/fabricator/reloader:${RELOADER_VERSION}"
```