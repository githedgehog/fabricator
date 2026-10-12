#!/usr/bin/env bash
# Copyright 2025 Hedgehog
# SPDX-License-Identifier: Apache-2.0

# Samples the control VM every 5 s until the file given as $2 exists. Started by run-steps.sh.
# usage: sampler.sh <out.tsv> <stop-file>      (HHFAB_DIR and CTRL_PORT as in run-steps.sh)
set -u
OUT=$1; STOP=$2
K="ssh -i ${HHFAB_DIR:-$HOME/hhfab}/vlab/sshkey -p ${CTRL_PORT:-22000} -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null core@127.0.0.1"
echo -e "time\tmem_used_mb\tmem_avail_mb\tdhcpd_rss_kb\tk3s_rss_kb\tdhcpd_cpu_m\thh_control_inqueue\tput200\tput409\tput429\tapf_rejected" > "$OUT"
while [ ! -e "$STOP" ]; do
  $K 'bash -s' <<'REMOTE' 2>/dev/null | tr '\n' '\t' | sed 's/\t$/\n/' >> "$OUT"
date +%T
d() { [ -n "$1" ] && echo "$1" || echo 0; }
d "$(free -m | awk 'NR==2{print $3}')"
d "$(free -m | awk 'NR==2{print $7}')"
d "$(ps -eo rss,comm | awk '$2=="fabric-dhcpd"{print $1}' | head -1)"
d "$(ps -eo rss,comm | awk '$2=="k3s-server"{print $1}' | head -1)"
d "$(sudo /opt/bin/kubectl -n fab top pod -l app.kubernetes.io/name=fabric-dhcpd --no-headers 2>/dev/null | awk '{gsub("m","",$2); print $2}')"
M=$(sudo /opt/bin/kubectl get --raw /metrics)
d "$(echo "$M" | grep '^apiserver_flowcontrol_current_inqueue_requests{flow_schema="hh-control"' | grep -oE '[0-9]+$' | head -1)"
for c in 200 409 429; do d "$(echo "$M" | grep "apiserver_request_total{.*code=\"$c\".*resource=\"dhcpsubnets\".*subresource=\"status\".*verb=\"PUT\"" | grep -oE '[0-9]+$' | head -1)"; done
d "$(echo "$M" | grep '^apiserver_flowcontrol_rejected_requests_total{flow_schema="hh-control"' | grep -oE '[0-9]+$' | head -1)"
REMOTE
  sleep 5
done
