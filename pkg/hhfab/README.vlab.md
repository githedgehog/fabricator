# Hybrid VLAB

A hybrid VLAB mixes VMs with hardware switches and nodes (`type.hhfab.githedgehog.com: hw`). `hhfab vlab up` installs
and powers the hardware on its own: switches over the PDU and the serial console, nodes over the BMC (Redfish).

## Wiring

- **Switches** need the PDU annotations (`power.hhfab.githedgehog.com/<psu>`) and the serial console, same as for
  `hhfab vlab switch reinstall`.
- **Nodes** need `bmc.hhfab.githedgehog.com: https://<bmc>` on the FabNode. Nodes without it are installed manually,
  and `vlab up` reminds you about them while it waits for readiness.
- **Unused hardware:** set `unused.hhfab.githedgehog.com: "true"` on a switch or node to keep it out of the VLAB. It's
  dropped from the wiring, so no connection may use it. A hardware switch with the description `unused` is treated
  the same unless the annotation says `"false"`. The annotation is removed from the switches and nodes that stay.

## Credentials

| Flag                              | Env                                                 | Needed for     |
|-----------------------------------|-----------------------------------------------------|----------------|
| `--pdu-username`/`--pdu-password` | `HHFAB_VLAB_PDU_USERNAME`/`HHFAB_VLAB_PDU_PASSWORD` | switch install |
| `--bmc-username`/`--bmc-password` | `HHFAB_VLAB_BMC_USERNAME`/`HHFAB_VLAB_BMC_PASSWORD` | node install   |

## Modes

`--hw-switches` and `--hw-nodes` (or `--hw` for both, env `HHFAB_VLAB_HW*`) take:

- `auto` (default): install what wasn't installed into this VLAB yet, power on the rest.
- `reinstall`: install all of it. Hardware without PDU or BMC info is an error.
- `disabled`: don't touch it.

```bash
hhfab vlab up -f            # new VLAB, installs all the hardware
hhfab vlab up               # same VLAB, only powers the hardware on
hhfab vlab up --hw=reinstall
hhfab vlab up --hw-nodes=disabled
```

`--ready switch-reinstall` (`-r=reinstall`) is replaced by `--hw=reinstall` and still works as an alias for it.

## How it works

- **Before the build** `vlab up` decides what to do with each switch and node and fails early on missing PDU/BMC info,
  the `expect` tool or the credentials for anything it has to install or power on or off.
- **Installed hardware** is tracked by markers in `vlab/hw/<name>.installed`, so `-f` (which wipes `vlab/`)
  reinstalls everything into the new VLAB. `vlab switch reinstall` and `vlab node reinstall` update them too.
- **Power:** on every `vlab up`, along with the VMs, unused hardware is powered off whatever the modes (disable with
  `--hw-power-off-unused=false`) and the used hardware that isn't reinstalled is powered on. Both wait for the PDU or
  BMC to report the new state and fail `vlab up` otherwise.
- **Switches** are reinstalled while the VMs boot: power cycle, ONIE install mode over the console, then `vlab up`
  waits up to 30 min for the agent at the fabric version.
- **Nodes** are built as ISO (override with `--mode-override <name>=<mode>`). `vlab up` serves the ISO on port 80 of the
  host IP in the BMC's subnet, mounts it as virtual media, boots once from it and waits for the install. On an existing
  cluster it removes the old k3s Node first.
- **Failures** stop `vlab up` (fail-fast) and collect the VLAB debug info.
- **Upgrade** (`--upgrade`): all the used hardware is powered on, with or without a marker, and nodes are upgraded over
  SSH like the gateway VMs; `reinstall` isn't allowed.

## Helpers for a running VLAB

```bash
hhfab vlab switch reinstall -n leaf-01 --wait-ready
hhfab vlab switch power -n leaf-01 -a off
hhfab vlab node reinstall -n gateway-bm
hhfab vlab node power -n gateway-bm -a cycle
```

Without `-n` they apply to all switches or BMC nodes. Unused hardware can still be powered by name.
