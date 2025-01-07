# fabricator

Fabricator is the repo that holds the scripts and utilities used to build the
fabric, VLAB, bare metal installers, helm charts, and pods that are used in the
open network fabric.

## Description 

The components of this repository are distributed and versioned independently
as OCI compliant artifacts.


## Local Build Instructions

### Prerequisites
- go version v1.23.0+
- docker version 17.03+. (used for logging into ghcr.io)
- [zot v2.1.0](https://zotregistry.dev/v2.1.0/)
- ghcr.io classic token with package **read** permissions
- git
- [just v1.38.0 or greater](https://github.com/casey/just)

### Create a github Classic Token

1. Log into github.com 
1. Click on your icon in the top right corner
1. Click on settings (gear icon)
1. On the left side of the page, scroll down and select "Developer Settings"
1. On the left side click the drop down arrow of "Personal access tokens"
1. Select "Tokens (classic)"
1. On the next page, right of center near the top select "Generate new token"
   drop down, then select "Generate new token (classic)"
1. You will be prompted for a TOTP code
1. Name your token according to your needs
1. Select an expiration period of 60-90 days
1. The Scope of the token should be **read** packages, only
1. Click Generate token at the bottom of the page
1. Copy the token down as it will only be visible on this page, it will be used
   to configure `zot` in the following step


### Install Zot

Zot is an OCI package registry. Zot is used on your local system as a
pull-through cache for all artifacts that are not being changed locally as part
of development process.

[Zot installation
instructions](https://zotregistry.dev/v2.1.0/install-guides/install-guide-linux/#installation)

The installation instructions above are for the most part distribution
agnostic. Some of the configuration files mentioned in the link are below:

<details>

This file:
* creates a registry with data in `/tmp/zot`
* runs a localhost only server on port 30000
* mirrors everything from the githedgehog github repo

<summary> /etc/zot/config.json </summary>

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
</details>

<details>

This file is supplying credentials for zot to read packages using your github
account.

<summary>/etc/zot/creds.json</summary>

```

{
  "ghcr.io": {
    "username": "YOUR_USERNAME_HERE",
    "password": "READ_ONLY_TOKEN_FROM_GITHUB"
  }
}

```

</details>

<details>

A systemd unit file for creating a zot registry.

<summary>/etc/systemd/system/zot.service</summary>

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

</details>

A `zot` user will need to be created, per the link above.

### Just push

The fabricator repo uses a [justfile][justfile1] for building and deploying code. After
you have made changes to your code, use
`just oci=http push` to build and push your code. All OCI artifacts will be
versioned using the [version string in tools.just][justfile2]
and will be pushed to the zot registry on the local machine, the new binaries will be created in `./bin/`

[justfile1]: https://github.com/githedgehog/fabricator/blob/21154b09112bdf148957dc75f2ce46d5be7beca0/justfile
[justfile2]: https://github.com/githedgehog/fabricator/blob/21154b09112bdf148957dc75f2ce46d5be7beca0/hack/tools.just#L7

### hhfab

If the code you are changing deals with setting up or managing flatcar, hhfab
will need to be instructed to pull packages from the local zot registry and not
the ghcr. To do this, specify the repo pass the `--registry-repo
127.0.0.1:30000` flag and argument to  `hhfab init` along with other flags.
From there continue on with the `hhfab` commands. To get vlab running with
local changes:
* `hhfab init --dev --registry-repo 127.0.0.1:30000`
* `hhfab vlab gen`
* `hhfab vlab up --mode iso`

### updating pods

* (TODO)


