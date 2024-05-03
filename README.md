# fabricator

## Updating 3rd-party artifacts

### Flatcar Linux

```bash
wget "https://stable.release.flatcar-linux.net/amd64-usr/current/flatcar_production_qemu_image.img"
wget "https://stable.release.flatcar-linux.net/amd64-usr/current/flatcar_production_qemu_uefi_efi_code.fd"
wget "https://stable.release.flatcar-linux.net/amd64-usr/current/flatcar_production_qemu_uefi_efi_vars.fd"

mv flatcar_production_qemu_image.img flatcar.img
mv flatcar_production_qemu_uefi_efi_code.fd flatcar_efi_code.fd
mv flatcar_production_qemu_uefi_efi_vars.fd flatcar_efi_vars.fd

ls -lah

oras push ghcr.io/githedgehog/flatcar:VERSION flatcar*
```

`VERSION` is the version of the image, e.g. `2605.12.0`, taken from "stable" at https://www.flatcar.org/releases

### K3s

```bash
wget "https://github.com/k3s-io/k3s/releases/download/v1.29.1%2Bk3s2/k3s"
wget "https://github.com/k3s-io/k3s/releases/download/v1.29.1%2Bk3s2/k3s-airgap-images-amd64.tar.gz"
wget "https://raw.githubusercontent.com/k3s-io/k3s/v1.29.1%2Bk3s2/install.sh"

mv install.sh k3s-install.sh

ls -lah

oras push ghcr.io/githedgehog/k3s:v1.29.1-k3s2 k3s*
```

`VERSION` is the version of the K3s with `+` replaced with `-`. E.g. `1.29.1-k3s1`

### Grafana Alloy

Download latest version for linux x86_64, unpack and rename binary to `alloy`.

```bash
oras push ghcr.io/githedgehog/fabric/alloy:v1.0.0 alloy
```

### Proxy chart

```bash
docker pull ghcr.io/tarampampam/3proxy:1.9.1
docker tag ghcr.io/tarampampam/3proxy:1.9.1 ghcr.io/githedgehog/fabric/fabric-proxy:1.9.1
docker push ghcr.io/githedgehog/fabric/fabric-proxy:1.9.1
```

## Steps to setup on ubuntu 22.04

I recommend using tmux or byobu. Byobu is already installed in Ubuntu and you can activate it for all sessions by

```
byobu-enable
```

and when you'll log back, you'll be in it already.

You'd need to have docker installed:

```
curl -fsSL https://get.docker.com -o install-docker.sh
sudo sh install-docker.sh
sudo usermod -aG docker $USER
newgrp docker
```

If you're running in the lab login into the registry mirror with user `lab` and regular lab password:

```
docker login https://m.l.hhdev.io:31000
```

Install some deps

```
sudo apt update
// optional: sudo apt upgrade -y
sudo apt install -y qemu-kvm swtpm-tools tpm2-tools socat
sudo usermod -aG kvm $USER
newgrp kvm
kvm-ok
```

Good output:

```
ubuntu@sl-hhfab-test-01:~$ kvm-ok
INFO: /dev/kvm exists
KVM acceleration can be used
```

Tips:

```
socat -,raw,echo=0,escape=0x1d unix-connect:.hhfab/vlab-vms/switch-1/serial.sock
```



// TODO(user): Add simple overview of use/purpose

## Description
// TODO(user): An in-depth paragraph about your project and overview of use

## Getting Started
You’ll need a Kubernetes cluster to run against. You can use [KIND](https://sigs.k8s.io/kind) to get a local cluster for testing, or run against a remote cluster.
**Note:** Your controller will automatically use the current context in your kubeconfig file (i.e. whatever cluster `kubectl cluster-info` shows).

### Running on the cluster
1. Install Instances of Custom Resources:

```sh
kubectl apply -f config/samples/
```

2. Build and push your image to the location specified by `IMG`:

```sh
make docker-build docker-push IMG=<some-registry>/fabricator:tag
```

3. Deploy the controller to the cluster with the image specified by `IMG`:

```sh
make deploy IMG=<some-registry>/fabricator:tag
```

### Uninstall CRDs
To delete the CRDs from the cluster:

```sh
make uninstall
```

### Undeploy controller
UnDeploy the controller from the cluster:

```sh
make undeploy
```

## Contributing
// TODO(user): Add detailed information on how you would like others to contribute to this project

### How it works
This project aims to follow the Kubernetes [Operator pattern](https://kubernetes.io/docs/concepts/extend-kubernetes/operator/).

It uses [Controllers](https://kubernetes.io/docs/concepts/architecture/controller/),
which provide a reconcile function responsible for synchronizing resources until the desired state is reached on the cluster.

### Test It Out
1. Install the CRDs into the cluster:

```sh
make install
```

2. Run your controller (this will run in the foreground, so switch to a new terminal if you want to leave it running):

```sh
make run
```

**NOTE:** You can also run this in one step by running: `make install run`

### Modifying the API definitions
If you are editing the API definitions, generate the manifests such as CRs or CRDs using:

```sh
make manifests
```

**NOTE:** Run `make --help` for more information on all potential `make` targets

More information can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)

## License

Copyright 2023 Hedgehog.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

