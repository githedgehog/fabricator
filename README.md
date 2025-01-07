# Fabricator
// TODO(user): Add a simple overview of use/purpose

## Description
// TODO(user): An in-depth paragraph about your project and an overview of the use

## Getting Started

### Prerequisites
- go version v1.22.0+
- docker version 17.03+.
- kubectl version v1.11.3+.
- Access to a Kubernetes v1.11.3+ cluster.

### To Deploy on the cluster
**Build and push your image to the location specified by `IMG`:**

```sh
make docker-build docker-push IMG=<some-registry>/fabricator:tag
```

**NOTE:** This image ought to be published in the personal registry you specified.
And it is required to have access to pull the image from the working environment.
Make sure you have the proper permission to the registry if the above commands don’t work.

**Install the CRDs into the cluster:**

```sh
make install
```

**Deploy the Manager to the cluster with the image specified by `IMG`:**

```sh
make deploy IMG=<some-registry>/fabricator:tag
```

> **NOTE**: If you encounter RBAC errors, you may need to grant yourself cluster-admin
privileges or be logged in as admin.

**Create instances of your solution**
You can apply the samples (examples) from the config/sample:

```sh
kubectl apply -k config/samples/
```

>**NOTE**: Ensure that the samples has default values to test it out.

### To Uninstall
**Delete the instances (CRs) from the cluster:**

```sh
kubectl delete -k config/samples/
```

**Delete the APIs(CRDs) from the cluster:**

```sh
make uninstall
```

**UnDeploy the controller from the cluster:**

```sh
make undeploy
```

## Project Distribution

Following are the steps to build the installer and distribute this project to users.

1. Build the installer for the image built and published in the registry:

```sh
make build-installer IMG=<some-registry>/fabricator:tag
```

NOTE: The makefile target mentioned above generates an 'install.yaml'
file in the dist directory. This file contains all the resources built
with Kustomize, which are necessary to install this project without
its dependencies.

2. Using the installer

Users can just run kubectl apply -f <URL for YAML BUNDLE> to install the project, i.e.:

```sh
kubectl apply -f https://raw.githubusercontent.com/<org>/fabricator/<tag or branch>/dist/install.yaml
```

## Contributing
// TODO(user): Add detailed information on how you would like others to contribute to this project

**NOTE:** Run `make help` for more information on all potential `make` targets

More information can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)

## How to locally build Fabricator using an OCI-native container image registry (Zot)

- First, you should set up zot on your build machine:

https://zotregistry.dev/v2.1.0/install-guides/install-guide-linux/

- Create /etc/zot/config.json:
```
{
  "log": {
    "level": "debug"
  },
  "storage": {
    "rootDirectory": "/tmp/zot"
  },
  "http": {
    "address": "127.0.0.1",
    "port": "30000"
  },
  "extensions": {
    "sync": {
      "enable": true,
      "credentialsFile": "/etc/zot/creds.json",
      "registries": [
        {
          "urls": [
            "https://ghcr.io"
          ],
          "onDemand": true,
          "tlsVerify": true,
          "content": [
            {
              "prefix": "/githedgehog/**",
              "destination": "/githedgehog",
              "stripPrefix": true
            }
          ]
        }
      ]
    }
  }
}
```
- and /etc/zot/config.json
```
{
  "ghcr.io": {
    "username": "mrbojangle3",
    "password":  TODO-GENERATE A CLASSIC TOKEN ON YOUR GITHUB PROFILE FOR THIS PART
  }
}
```
- To build your packages, use the command: 
```
$just oci=http push
```
- When running hhfab commands, you need to specify a different repository during initialization with the following command: 
```
$ hhfab init --dev --registry-repo 127.0.0.1:30000
```
- Create /etc/systemd/system/zot.service:
```
[Unit]
Description=OCI Distribution Registry
Documentation=https://zotregistry.dev/
After=network.target auditd.service local-fs.target

[Service]
Type=simple
ExecStart=/usr/bin/zot serve /etc/zot/config.json
Restart=on-failure
User=zot
Group=zot
LimitNOFILE=500000
MemoryHigh=30G
MemoryMax=32G

[Install]
WantedBy=multi-user.target
```
