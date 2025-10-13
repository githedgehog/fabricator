# Artifacts preparation

## Flatcar

Stable version is listed on the [Flatcar releases page](https://www.flatcar.org/releases).

```bash
export FLATCAR_VERSION="v4230.2.3"
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
export K3S_VERSION="v1.34.1-k3s1"
export K3S_VERSION_UPSTREAM="${K3S_VERSION//-/+}"

wget "https://github.com/k3s-io/k3s/releases/download/${K3S_VERSION_UPSTREAM}/k3s"
wget "https://github.com/k3s-io/k3s/releases/download/${K3S_VERSION_UPSTREAM}/k3s-airgap-images-amd64.tar.gz"
wget "https://raw.githubusercontent.com/k3s-io/k3s/${K3S_VERSION_UPSTREAM}/install.sh"

mv install.sh k3s-install.sh

oras push "ghcr.io/githedgehog/fabricator/k3s-airgap:${K3S_VERSION}" k3s k3s-airgap-images-amd64.tar.gz k3s-install.sh
```

## Zot

```bash
export ZOT_VERSION="v2.1.7"
export HH_CHART_VERSION="v0.1.67-hh1"

helm pull --version ${HH_CHART_VERSION} "oci://ghcr.io/githedgehog/fabricator/charts/zot"
mv zot-${HH_CHART_VERSION}.tgz zot-chart.tgz

skopeo copy "docker://ghcr.io/project-zot/zot-linux-amd64:${ZOT_VERSION}" "docker://ghcr.io/githedgehog/fabricator/zot:${ZOT_VERSION}"

docker image rm "ghcr.io/githedgehog/fabricator/zot:${ZOT_VERSION}" || true
docker pull --platform linux/amd64 "ghcr.io/githedgehog/fabricator/zot:${ZOT_VERSION}"
docker save -o zot-airgap-images-amd64.tar "ghcr.io/githedgehog/fabricator/zot:${ZOT_VERSION}"
pigz -v -c zot-airgap-images-amd64.tar > zot-airgap-images-amd64.tar.gz

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
export K9S_VERSION="v0.50.15"

wget "https://github.com/derailed/k9s/releases/download/${K9S_VERSION}/k9s_Linux_amd64.tar.gz"
tar -xzvf k9s_Linux_amd64.tar.gz

oras push "ghcr.io/githedgehog/fabricator/k9s:${K9S_VERSION}" k9s
```

## ONIE

ONIE image is preprocessed to 32GB with fixed GPT partition table to prevent installer failures in VLAB. This eliminates the second retry delay during switch startup.

```bash
export ONIE_VERSION="v0.2.1"

# Step 1: Resize image to target size (32GB to match switch vm.Size.Disk)
qemu-img resize onie-kvm_x86_64.qcow2 32G

# Step 2: Fix GPT partition table to recognize new disk size
# This updates the backup GPT header and recalculates usable sectors
sudo modprobe nbd max_part=8
sudo qemu-nbd --format=qcow2 --connect=/dev/nbd0 onie-kvm_x86_64.qcow2
sudo sgdisk --move-second-header /dev/nbd0

# Verify the fix
sudo sgdisk --print /dev/nbd0 | grep "last usable sector"
# Should show: last usable sector is 67108830 (32GB)

sudo qemu-nbd --disconnect /dev/nbd0

# Step 3: Verify final image
qemu-img info onie-kvm_x86_64.qcow2
# Should show: virtual size: 32 GiB

# Step 4: Push to registry
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
export BCM_SONIC_VERSION="v4.4.2"

oras push ghcr.io/githedgehog/sonic-bcm-private/sonic-bcm-advanced:${BCM_SONIC_VERSION} sonic-broadcom-enterprise-advanced.bin
oras push ghcr.io/githedgehog/sonic-bcm-private/sonic-bcm-campus:${BCM_SONIC_VERSION} sonic-broadcom-campus.bin
oras push ghcr.io/githedgehog/sonic-bcm-private/sonic-bcm-base:${BCM_SONIC_VERSION} sonic-broadcom-enterprise-base.bin
oras push ghcr.io/githedgehog/sonic-bcm-private/sonic-bcm-vs:${BCM_SONIC_VERSION} sonic-vs.bin
```

## Celestica SONiC+

```bash
export CLS_SONIC_VERSION="v4.1.0-beta1-hh" # 4.1.0_Beta1-b6f3c5a4f

oras push ghcr.io/githedgehog/sonic-cls-private/sonic-cls-plus-broadcom:${CLS_SONIC_VERSION} sonic-broadcom.bin
oras push ghcr.io/githedgehog/sonic-cls-private/sonic-cls-plus-marvell:${CLS_SONIC_VERSION} sonic-innovium.bin
oras push ghcr.io/githedgehog/sonic-cls-private/sonic-cls-plus-vs:${CLS_SONIC_VERSION} sonic-vs.bin
```

## Grafana Alloy

```bash
export ALLOY_VERSION="v1.11.0"
export ALLOY_CHART_VERSION="1.3.0"

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

## Bash completion

We need https://github.com/scop/bash-completion to be installed on the system. It's under GPL v2, we don't modify it by
any means and so we publish the whole package as is and only taking needed files from it when building the installer
with hhfab build.

```bash
export BASH_COMPLETION_VERSION="2.16.0"

wget "https://github.com/scop/bash-completion/releases/download/${BASH_COMPLETION_VERSION}/bash-completion-${BASH_COMPLETION_VERSION}.tar.xz"
tar xzf "bash-completion-${BASH_COMPLETION_VERSION}.tar.xz"
mv bash-completion-${BASH_COMPLETION_VERSION} bash-completion
oras push "ghcr.io/githedgehog/fabricator/bash-completion:v${BASH_COMPLETION_VERSION}" bash-completion
```
