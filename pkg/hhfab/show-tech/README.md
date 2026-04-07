# show-tech

Diagnostic collection system for VLAB environments. Gathers state from every
node type in parallel and saves it to `show-tech-output/` in the work
directory.

## When it runs

| Situation | Collected? |
|-----------|------------|
| VLAB exits cleanly (pass) | Only if `--collect` / `HHFAB_VLAB_COLLECT=true` or `--force-collect-show-tech` / `HHFAB_VLAB_COLLECT_FORCE=true` |
| VM startup or post-process failure | Always |
| Any on-ready step fails (inspect, connectivity, …) | Always |
| Release test — individual test fails | Always, per failing test |
| Release test — suite setup fails | Always |

The `--collect` flag (or `HHFAB_VLAB_COLLECT` env var) enables show-tech
collection on all exits (pass and failure). The `--force-collect-show-tech`
flag (or `HHFAB_VLAB_COLLECT_FORCE` env var) forces show-tech collection
on all exits including pass. All failure paths collect unconditionally.

## Output layout

```
show-tech-output/
  runner.log                   # CI runner host diagnostics
  <vm-or-switch-name>.log      # one file per device (SSH path)
  <vm-or-switch-name>-console.log  # console fallback if SSH failed

  # release-test per-failure layout:
  <suite-name>/
    <test-name>/
      runner.log
      <device>.log
      ...
    suite-setup/               # used when the suite itself fails to set up
      ...
```

## Collection method

SSH is the primary path. When SSH is unavailable (e.g. the node never came
up), an `expect` script connects via the QEMU serial console as a fallback.
Console fallback requires `expect` to be installed and credentials set via
`HHFAB_VLAB_SWITCH_USERNAME`/`_PASSWORD` (switches) or
`HHFAB_VLAB_SERVER_USERNAME`/`_PASSWORD` (servers, control, gateway).

All devices are collected in parallel. Switches get a 10-minute timeout;
everything else gets 5 minutes.

## What each script collects

### runner.sh — CI runner host

Collected from the Linux host running the QEMU VMs, not from any VM.

- **System resources**: memory (`free`, `/proc/meminfo`), CPU count/model,
  load average, cgroup memory stats (v1 and v2), PSI pressure (memory, CPU,
  I/O)
- **OOM events**: recent kernel OOM kills from `dmesg`
- **Process state**: top memory and CPU consumers, per-QEMU-process RSS/state
- **Disk and I/O**: `df -h`, `iostat`
- **VLAB network plumbing**: `hhbr` bridge details, all `hhtap*` tap interfaces
  (address, operstate, packet counters), bridge port states, bridge FDB/VLAN
  tables, UDP sockets used by QEMU netdev links
- **Host networking**: `ip addr/route/neigh`, nftables ruleset, bridge
  netfilter sysctls
- **QEMU health**: QMP `query-status` for every running VM, per-process
  RSS/thread count, KVM/QEMU kernel messages from `dmesg`
- **Devices**: `/dev/kvm`, `/dev/net/tun` presence and permissions

### switch.sh — SONiC switch

Collected via `sonic-cli` and direct `bcmcmd` (Broadcom SDK).

- **System**: version, uptime
- **Interfaces**: status, error-disabled ports, descriptions, counters, LLDP
  neighbors, transceiver summary/laser status/wattage
- **Running config**: full `show running-configuration`
- **VLANs and VXLAN**: VLAN config/brief, VXLAN interface/VNI/VRF/tunnel/remote
  VTEP/remote MAC mappings, VLAN-VNI and VRF-VNI maps
- **L2**: MAC address table, MCLAG brief and interfaces, port-channel summary
- **BGP/EVPN**: IPv4 BGP summary, L2VPN EVPN summary and neighbors, EVPN routes,
  route-maps; EVPN VNI/MAC/ES detail, ARP cache; per-VRF route, ARP, and BGP
  IPv4 unicast summary
- **Platform**: environment sensors, fan status, firmware, PSU summary, SSD
  health, temperature
- **Broadcom ASIC** (`bcmcmd`): port status, PHY info, L2/L3 tables, ACLs,
  route and ECMP tables, host table, VLAN table, trunk table
- **Services**: system status brief/full, system logs, hedgehog-agent status
  and logs (`/var/log/agent.log`), Docker container list and last 100 log lines
  per container

### control.sh — control node (k3s)

- **System**: kernel version, OS release, k3s version
- **Networking**: `ip addr/route/neigh/link`, per-NIC ethtool stats and offload
  settings, ping+ARP+SSH reachability check to every switch listed in the
  `Switch` CRD, disk usage, process list
- **Kubernetes**: nodes (`-o wide`), all pods (`-A -o wide`), events sorted by
  time
- **Pod detail**: `kubectl describe` + current and previous logs for every pod
  in every namespace
- **Hedgehog CRDs**: `kubectl get` and `kubectl describe` for every
  `*.githedgehog.com` resource across all namespaces
- **Services and logs**: `k3s.service` status, `sshd` status, k3s logs (last
  hour), `systemd-networkd` logs, kernel logs, kernel network messages from
  `dmesg`

### server.sh — server VM (Flatcar Linux)

- **System**: kernel version, OS release
- **Networking**: `networkctl status`, VLAN interfaces (`ip -d link show type
  vlan`), `ip addr`, full and per-table routing (`ip route show table all`)
- **Connectivity**: ping to default gateway, ping to `10.0.{1-9}.2` (other
  servers in the lab)
- **Link detail**: `networkctl list`, `ip -d link show`, bonding configuration
  (`/proc/net/bonding`), MTU, LLDP data, DHCP leases, per-NIC ethtool offload
  settings, systemd network unit files
- **Services and time**: `systemd-timesyncd` status, `timedatectl`,
  `sshd` status
- **Logs**: hhnet log (`/var/log/hhnet.log`), SSH journal entries,
  `systemd-networkd` journal, kernel journal and `dmesg` network/bond/VLAN
  messages
- **Misc**: ARP table, listening ports (`ss -tulnp`), DNS resolver
  (`/etc/resolv.conf`, `resolvectl`), bond/802.1q/bridge kernel modules,
  per-interface packet statistics

### gateway.sh — gateway node (FRR + dataplane)

- **System**: kernel version, OS release, uptime, hostname, date
- **Networking**: `ip addr/route/neigh/link`
- **Resources**: disk usage, memory, top memory and CPU processes
- **FRR** (via `vtysh` inside the `frr` container): version, running config,
  BGP summary (IPv4, L2VPN EVPN), BGP routes, per-neighbor state, per-VRF BGP
  and route tables, interface status, FRR logs, protocol summary, zebra status,
  memory usage, thread CPU stats, Hedgehog plugin version and RPC stats
- **FRR container logs**: full `crictl logs` for the FRR container
- **Dataplane** (via `dataplane-cli` inside the dataplane container): `show
  tech` output
- **Dataplane container logs**: full `crictl logs` for the dataplane container
- **Services and logs**: `k3s-agent.service` status and last-hour logs,
  `sshd` status, `systemd-networkd` last-hour logs, kernel logs (last hour),
  kernel network/bond/VLAN messages from `dmesg`

## CI integration

`run-vlab.yaml` exposes a `collect_show_tech` input (default `false`) that
maps to `HHFAB_VLAB_COLLECT`. When set, show-tech is also collected on
successful exits. Failure paths always collect unconditionally.

`custom-vlab.yaml` exposes the same option as a workflow-dispatch checkbox for
manual runs.

Debug artifacts (including show-tech output) are uploaded unconditionally at
the end of every CI run, organized into three phases:

```
_debug/0-before/   # pre-upgrade run (upgrade-from scenarios only)
_debug/1-current/  # main run
_debug/2-after/    # post-upgrade run (upgrade-from scenarios only)
```
