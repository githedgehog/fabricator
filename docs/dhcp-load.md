# DHCP load test (`hhfab vlab dhcp-load`)

Load test for fabric-dhcpd. It creates a VPC with DHCP subnets on a running VLAB, runs thousands of DHCP clients (servers × NICs), prints how many got a lease and how fast, and removes what it created. Don't use it on a VLAB that serves other purposes.

## Modes

| Mode | Where the clients run | Relay | Needs |
|---|---|---|---|
| `access` | on a VLAB server, one VLAN interface per subnet on the server NIC that is wired to a leaf | the real leaf relays the broadcast DHCP and adds giaddr and option 82 | a server with an unbundled connection to a leaf, root on the server (the `core` user has sudo) |
| `relay` | on the VLAB host, in a network namespace attached to the management bridge, one source IP per emulated leaf | emulated: giaddr and option 82 circuit-id and VRF as a leaf sends them; the command registers the IPs as fake leaf Switches cloned from a real leaf | root on the host and L2 from the host to the control node's management network |

`relay` does not work where the control node's management NIC is a passthrough device and the host has no leg on that network. Use `access` there.

## Run

Build a static Linux binary on a master that matches the deployed fabric version (the command loads the VLAB wiring and fails with `no kind "Fabric" is registered` on an older fabric API), copy it to the host running the VLAB and run it from the VLAB workdir:

```
CGO_ENABLED=0 GOOS=linux go build -o hhfab-dhcpload ./cmd/hhfab
hhfab-dhcpload vlab dhcp-load --mode access --cidr-base 10.30.128.0 --vlan-base 2000 \
  --servers 500 --nics 8 --ramp 60s --out clients.csv
```

In `access` mode the command picks the first server with an unbundled connection (`--access-server` overrides), resets its network configuration with `hhnet cleanup`, uploads its own executable to `/home/core/hhfab-dhcp-load` and starts the hidden `dhcp-load-agent` subcommand there with a JSON config. The agent's output is streamed back and the per-client CSV is downloaded to `--out`. Everything created is labeled `loadgen.githedgehog.com/fake=true` and removed on exit; `--keep` leaves it for investigation, and leftovers of an earlier run are removed at the start.

Constraints on the flags:

- `--cidr-base`, `--subnet-prefix`: subnet i gets a prefix of length `subnet-prefix` (default 20, 16 to 28) at `cidr-base` plus i times its size; all must be inside the default IPv4 namespace. Subnets of /24 and longer keep only the gateway out of the pool, so a /24 holds 250 clients.
- `--vlan-base`: VLAN of the first subnet; all must be inside the leaves' VLAN namespace and unused.
- `--layout`: `rail` (one subnet per NIC index, the default), `flat` (one subnet) or `leaf` (one subnet per leaf × NIC, `relay` only).
- `--ramp`: client start times are spread uniformly over this window from a fixed seed; 0 starts all at once.
- `--lease`, `--duration`, `--renew-every`: with a duration, bound clients renew at T1 (half the lease, or the override) until it ends; without one the run ends when every client is bound or gave up.
- `--retries`: `pxe` (4, 8, 16, 32 s, then give up) or `rfc` (4, 8, 16, 32, 64 s with jitter).

Clients get MAC `02:4c:54:xx:xx:xx` by index. The exit status is non-zero when any client did not bind, a renewal failed or a duplicate IP was seen.

## What the summary reports

- bound: clients with an ACK carrying a non-zero IP. An OFFER or ACK with IP 0.0.0.0 (dhcpd sends one when its allocation fails) counts as a failure and is reported as `empty_replies`.
- latency percentiles of OFFER, ACK and the whole DORA exchange (first DISCOVER to ACK), and of renewals.
- retransmits, NAKs, late replies, duplicate IPs (two MACs with the same IP in one subnet), IP changes on renewal.

## Method

The question is capacity: the highest sustained arrival rate of clients, in clients per second, at which every pass criterion holds, compared with the arrival rate the deployment needs. dhcpd rewrites the status of the client's subnet through the API server on every DISCOVER and REQUEST, so a client costs two status updates, three in `l3vni` VPCs, where the first lease is 10 seconds and the client renews about 5 seconds after it binds. The cost of an update grows with the leases in the subnet, so the layout is a load variable like the arrival rate.

Pass criteria:

| Scenario | Passes when |
|---|---|
| Storm (4000 clients in 60 s, provisional until the deployment's arrival profile is known) | at least 99.9% bound, p99 DORA under 10 s, all bound under 3 minutes, 0 empty replies |
| Renewals | 0 failures, p99 under 1 s, 0 IP changes |
| dhcpd restart | serving within 30 s, 0 IP changes |
| Control plane during a storm | no controller loses leader election, a canary VPC reconciles within a bound measured without load |
| Always | 0 duplicate IPs, 0 panics |

Rules:

- One variable changes between compared runs; a claim needs three runs at identical settings, with all values reported.
- A run starts only after dhcpd's backlog is gone (the API server's queue for dhcpd is empty three checks in a row) and the previous run's objects are removed.
- API server counters are cumulative; use differences between before and after.
- A failing run is rerun once with `--keep` to look at the live state.

Capacity sweep: 4000 clients, the arrival window is the only variable.

| Window (s) | 600 | 300 | 150 | 100 | 60 | 30 | 15 | 5 |
|---|---|---|---|---|---|---|---|---|
| Clients per second | 6.7 | 13.3 | 26.7 | 40 | 66.7 | 133 | 267 | 800 |

The largest passing rate is the capacity; repeat it three times there and at the next failing rate. Run 1600, 2400 and 4000 clients at the same rate to separate rate from total. Repeat the sweep whenever the control node size, dhcpd version, VPC mode or layout changes, never together with another change.

Other scenarios: rolling boot (batches at a fixed interval), steady renewals at the production renewal rate and at 5 times it (shorten the lease to reach a rate; 4000 clients renew 0.09 times per second at an 86400 s lease and 2.2 at 3600 s), dhcpd pod restart during renewals and during a storm, API server outage of 30 s and 120 s during renewals, churn (release and rediscover for an hour), pool exhaustion (a subnet smaller than the client count), and a 24-hour soak with background fabric activity. Several of these need generator features that are not implemented yet: `l3vni` VPCs, static reservations, many small subnets, rolling batches, churn, a configurable pool size, clients behind more than one leaf, unicast renewals, recording the generator VM's CPU.

## Recording a run

Per run, keep together under one name: the generator log and CSV; the control VM sampled every 5 seconds (memory used and available, dhcpd and k3s memory and CPU); the API server's counters before and after (`apiserver_request_total` for `dhcpsubnets`, by verb and code; `apiserver_flowcontrol_current_inqueue_requests` and `apiserver_flowcontrol_rejected_requests_total` for the `hh-control` level); dhcpd's log including the rotated, compressed files under `/var/log/pods/fab_fabric-dhcpd-*/fabric-dhcpd/` (the current file holds only the last minutes); the control VM's UDP counters; the versions of hhfab, fabric, dhcpd and k3s; the flow-control and k3s configuration; the VM's CPU, memory and disk.

Check that the generator is not the limit: the control VM's UDP received count matches what the generator sent, and the generator VM's CPU stays below saturation.

## Reference configuration

Until a deployment's own values are known, use a configuration modeled on existing GPU deployments: `l3vni` VPCs; per tenant VPC one `default` subnet plus `rail1` to `rail8`, each a /24 with DHCP; a static reservation per host and NIC plus a dynamic pool starting at .128; lease 86400 s on backend VPCs and 3600 s on frontend VPCs; MTU 9036, DNS and time servers, default route disabled on the backend. A /24 holds 254 hosts, so 500 servers cannot share one rail subnet; how servers spread over tenant VPCs is a load variable.

## Settings that matter for the results

Report these with every result:

- Control node: vCPU, memory and disk. The documentation's sizes are 8 cores and 2 × 16 GiB for fewer than 50 switches. A 16 GiB VM is started with `HHFAB_VLAB_CTRL_RAM=16384`.
- k3s: `max-requests-inflight`, `max-mutating-requests-inflight`, `etcd-compaction-interval`.
- API priority and fairness: fabricator puts every service account of the `fab` namespace (fabric-dhcpd and the Fabric and Fabricator controllers) in the `hh-control` flow schema and priority level (40 shares, 64 queues, hand size 6, queue length 50, at most 300 queued requests per user); see `pkg/fab/comp/k3s/flowcontrol.go`.
- Subnet layout, lease time, VPC mode.

## Limits of the generator

All clients of a run share one leaf and one server NIC in `access` mode; renewals are broadcast where real clients send unicast; PXE options and the vendor class are not sent.
