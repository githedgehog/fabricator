#!/usr/bin/env bash
# Copyright 2025 Hedgehog
# SPDX-License-Identifier: Apache-2.0

# Runs `hhfab vlab dhcp-load` once per server count and records the run.
# usage: [EXTRA="<more dhcp-load flags>"] run-steps.sh <label-prefix> <ramp> <servers>...
# Run it on the host that runs the VLAB. Settings (environment):
#   HHFAB_DIR   VLAB workdir (default ~/hhfab), must hold vlab/sshkey
#   HHFAB_BIN   hhfab binary built from this branch (default ./hhfab-dhcpload, relative to HHFAB_DIR)
#   CTRL_PORT   host port forwarded to the control VM's ssh (default 22000)
#   OUT_DIR     where run directories go (default ~/dhcp-load-runs)
#   ENV_FILE    optional file to source first (e.g. HHFAB_VLAB_CTRL_RAM)
#   GROUP, NOTE free text recorded in manifest.json
# Per run it writes <OUT_DIR>/<prefix>-<N>/: drain.txt, metrics-before.txt, sampler.tsv, run.log, clients.csv,
# metrics-after.txt, dhcpd.raw, manifest.json, summary.txt and, once per prefix, <prefix>-config/.
set -u
P=$1; RAMP=$2; shift 2
S=$(cd "$(dirname "$0")" && pwd)
HHFAB_DIR=${HHFAB_DIR:-$HOME/hhfab}; HHFAB_BIN=${HHFAB_BIN:-./hhfab-dhcpload}; CTRL_PORT=${CTRL_PORT:-22000}
OUT_DIR=${OUT_DIR:-$HOME/dhcp-load-runs}
export HHFAB_DIR HHFAB_BIN CTRL_PORT
cd "$HHFAB_DIR" || exit 1
# shellcheck disable=SC1090
[ -n "${ENV_FILE:-}" ] && source "$ENV_FILE" 2>/dev/null
K="ssh -i vlab/sshkey -p $CTRL_PORT -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null core@127.0.0.1"
# API server counters for dhcpsubnets and the hh-control flow-control level, UDP counters and memory of the control VM
metrics() { $K 'sudo /opt/bin/kubectl get --raw /metrics | grep -E "^apiserver_flowcontrol_(rejected_requests_total|current_inqueue_requests)\{flow_schema=\"hh-control\"|apiserver_request_total\{.*resource=\"dhcpsubnets\"|^apiserver_flowcontrol_request_wait_duration_seconds_(sum|count)\{execute=\"true\",flow_schema=\"hh-control\"|^apiserver_flowcontrol_request_execution_seconds_(sum|count)\{flow_schema=\"hh-control\""; grep "^Udp:" /proc/net/snmp | tail -1; free -m | sed -n 2p' 2>/dev/null; }
# configuration the runs are tied to: saved once per prefix
C=$OUT_DIR/$P-config; mkdir -p "$C"
$K 'sudo /opt/bin/kubectl get flowschema,prioritylevelconfiguration -o yaml' > "$C/flowcontrol.yaml" 2>/dev/null
$K 'sudo cat /etc/rancher/k3s/config.yaml; free -m' > "$C/k3s-config-and-memory.txt" 2>/dev/null
pgrep -af "qemu-system-x86_64 -name control-1" | grep -o -- "-m [0-9]*M" | head -1 > "$C/control-vm-ram.txt"
$K 'sudo /opt/bin/kubectl -n fab get deploy -o wide' > "$C/deployments.txt" 2>/dev/null
rec() { echo -e "$(date -u +%FT%TZ)\t$1\t$2" >> "$D/commands.log"; }   # commands the wrapper runs, copied into manifest.json by manifest.py end
for N in "$@"; do
  D=$OUT_DIR/$P-$N; mkdir -p "$D"
  # drain gate: no queued requests in hh-control three checks in a row, five seconds apart (gives up after 120 checks)
  ok=0; for i in $(seq 1 120); do
    q=$($K 'sudo /opt/bin/kubectl get --raw /metrics | grep "^apiserver_flowcontrol_current_inqueue_requests{flow_schema=\"hh-control\"" | grep -oE "[0-9]+$"' 2>/dev/null)
    if [ "${q:-1}" = "0" ]; then ok=$((ok+1)); else ok=0; fi
    [ $ok -ge 3 ] && break; sleep 5
  done
  echo "drain-wait-checks=$i inqueue=$q" > "$D/drain.txt"
  CMD="$HHFAB_BIN vlab dhcp-load --mode access --cidr-base 10.30.128.0 --vlan-base 2000 --servers $N --nics 8 --ramp $RAMP --out $D/clients.csv ${EXTRA:-}"
  python3 "$S/manifest.py" start "$D" --id "$P-$N" --prefix "$P" --group "${GROUP:-}" --note "${NOTE:-}" --command "EXTRA=\"${EXTRA:-}\" run-steps.sh $P $RAMP $N  =>  $CMD"
  rec host "metrics snapshot before: kubectl get --raw /metrics (hh-control, dhcpsubnets), /proc/net/snmp Udp, free -m (via control-1 ssh)"
  metrics > "$D/metrics-before.txt"
  rm -f "$D/stop"; "$S/sampler.sh" "$D/sampler.tsv" "$D/stop" &
  SAMPLER=$!
  rec host "$CMD"
  # shellcheck disable=SC2086
  $CMD 2>&1 | tee "$D/run.log"
  echo EXIT="${PIPESTATUS[0]}" >> "$D/run.log"
  touch "$D/stop"; wait $SAMPLER 2>/dev/null
  metrics > "$D/metrics-after.txt"
  rec control-1 "copy dhcpd logs: cat /var/log/pods/fab_fabric-dhcpd-*/fabric-dhcpd/0.log.* ; kubectl -n fab logs deploy/fabric-dhcpd"
  $K 'sudo sh -c "cat /var/log/pods/fab_fabric-dhcpd-*/fabric-dhcpd/0.log.*"; sudo /opt/bin/kubectl -n fab logs deploy/fabric-dhcpd' > "$D/dhcpd.raw" 2>/dev/null
  echo DONE >> "$D/run.log"
  python3 "$S/manifest.py" end "$D"
  python3 "$S/summarize-run.py" "$D"
  touch "$D/DONE"
done
echo ALLDONE > "$OUT_DIR/$P-ALLDONE"
