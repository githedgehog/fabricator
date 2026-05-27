# Fabricator

Fabricator builds `hhfab`, the CLI for installing and operating
[Hedgehog Open Network Fabric](https://docs.hedgehog.cloud), together with the
Virtual Lab (VLAB), the bare-metal Flatcar installers, the in-cluster
fabricator operator (CRDs + controller), and the Helm charts shipped to
customer control nodes.

## Description

The components of this repository are distributed and versioned independently
as OCI compliant artifacts.

## Local Build Instructions

### Prerequisites
- Go v1.26+
- Docker 17.03+ (used for logging into ghcr.io)
- [zot v2.1.5](https://zotregistry.dev/v2.1.5/)
- A GitHub classic token with package **read** permissions
- git
- [just v1.36.0 or greater](https://github.com/casey/just)

### Clone the repository

```
git clone https://github.com/githedgehog/fabricator.git
cd fabricator
```

### Create a GitHub classic token

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
1. Set an expiration that matches your org policy
1. The scope of the token should be **read** packages, only
1. Click Generate token at the bottom of the page
1. Copy the token down as it will only be visible on this page, it will be used
   to configure `zot` in the following step


### Install Zot

Zot is an OCI package registry. Zot is used on your local system as a
pull-through cache for all artifacts that are not being changed locally as part
of development process.

[Zot installation
instructions](https://zotregistry.dev/v2.1.5/install-guides/install-guide-linux/#installation)

The installation instructions above are for the most part distribution
agnostic. Some of the configuration files mentioned in the link are below:

<details>
<summary>/etc/zot/config.json</summary>

This file:
* creates a registry with data in `/tmp/zot`
* runs a localhost only server on port 30000, depending on your use case you
  might want to have it bind to 0.0.0.0.
* mirrors everything from the githedgehog github repo

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
<summary>/etc/zot/creds.json</summary>

Supplies credentials for zot to read packages using your GitHub account.

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
<summary>/etc/systemd/system/zot.service</summary>

A systemd unit file for creating a zot registry.

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

### Common just targets

The fabricator repo uses a [justfile](justfile) for building and deploying
code. The most useful targets:

| Target | What it does |
| ------ | ------------ |
| `just` | List all available recipes |
| `just tools` | Download build tools into `./bin/` |
| `just gen` | Regenerate code, CRDs, and embedded artifacts |
| `just build` | Build `hhfab` and `hhfabctl` for the host platform |
| `just lint` | Run linters (must pass before pushing) |
| `just test` | Run unit tests |
| `just all` | gen + lint + test + build (the CI bundle) |
| `just oci=http push` | Build everything and push it to the local zot |

Build version strings are derived from `git describe`; see
[hack/tools.just](hack/tools.just) for the full version logic.

### Just push

After making changes, run `just oci=http push` to build and push all OCI
artifacts to the local zot registry. Binaries land in `./bin/`. The `oci=http`
override switches `oras` and `helm` to plain-HTTP so they can talk to the
local registry without TLS.

### hhfab

If the code you are changing deals with setting up or managing flatcar, hhfab
will need to be instructed to pull packages from the local zot registry and
not the GitHub container registry (ghcr.io). Pass
`--registry-repo 127.0.0.1:30000` to `hhfab init` along with other flags.
From there continue on with the `hhfab` commands. To get vlab running with
local changes:

* `hhfab init --dev --registry-repo 127.0.0.1:30000`
* `hhfab vlab gen`
* `hhfab vlab up`

**Iteration flags.** `hhfab vlab up -f` (`--recreate`) tears the existing
VLAB down and rebuilds it from scratch. `--mode=manual` (`-m=manual`) uses
the legacy installer path: `hhfab` scps the installer to the control node
and you run it there, which keeps the install logs on the node and skips
the ISO build step.

**Overriding component versions.** The `Fabricator` object `fab/default` in
the `fab` namespace lets you pin any component to a non-master version
without rebuilding `hhfab`. Either edit `fab.yaml` before `vlab up`, or
`kubectl edit fab/default -n fab` on a running env (replace `<version>` with
the tag you want):

```yaml
spec:
  overrides:
    versions:
      fabric:
        agent: <version>
        api: <version>
        boot: <version>
        controller: <version>
        ctl: <version>
        dhcpd: <version>
```

**Cluster access.** For VLAB, `hhfab` writes the kubeconfig to
`<init-dir>/vlab/kubeconfig` (commonly `~/hhfab/vlab/kubeconfig`); export
`KUBECONFIG` to that path before running `kubectl edit` or `just patch`
from your laptop. On HW environments the kubeconfig lives on the control
node at `/etc/rancher/k3s/k3s.yaml`; pick whichever fits your workflow:

* ssh into the control node and run `kubectl` (and the `just` recipes
  too, if you check the repo out there) directly: it's preinstalled.
* `scp` the file to your laptop, swap the `server:` field (default
  `https://127.0.0.1:6443`) for an address the API answers on (one of
  the control node's `tlsSAN` entries), then `export KUBECONFIG`.
* Merge it into your existing `~/.kube/config` and switch contexts.

As an alternative to editing the CR by hand, both this repo and the
[fabric](https://github.com/githedgehog/fabric) repo ship a `just patch`
target that applies the same override via `kubectl patch`, scoped to the
versions block that repo owns and pinned to its current `git describe`
build:

* `just patch` from **fabricator** (this repo) updates
  `spec.overrides.versions.fabricator.*` (api, controller, ctl, nodeConfig).
* `just patch` from **fabric** updates `spec.overrides.versions.fabric.*`
  (api, agent, boot, controller, ctl, dhcpd).

The fabricator operator picks the change up automatically and rolls the new
versions out to control nodes and switch agents. To swap versions
permanently at build time instead, edit `pkg/fab/versions.go` and rebuild.

## Contributing

This repo enforces:

* **DCO sign-off** on every commit: use `git commit -s` (or `--signoff`).
* **Conventional Commits**: commit subjects must follow the
  [Conventional Commits](https://www.conventionalcommits.org/) spec; the
  `commitlint` workflow checks this and blocks merge commits.

CI rejects PRs that fail either check. Run `just lint` before pushing.

**Troubleshooting: golangci-lint version error after a Go bump.** If `just
lint` fails with `can't load config: the Go language version (goX.Y) used to
build golangci-lint is lower than the targeted Go version (...)`, the cached
linter in `bin/` was built with an older Go. The install step is skipped when
the binary already exists, so remove it and re-run to rebuild it against your
current Go:

```
rm -f bin/golangci-lint-* && just lint
```
