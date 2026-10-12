#!/usr/bin/env python3
# Copyright 2025 Hedgehog
# SPDX-License-Identifier: Apache-2.0
"""Print the increase of the DHCPSubnet status PUT counters and the hh-control rejects between two metrics snapshots.

usage: metrics-delta.py <metrics-before.txt> <metrics-after.txt>
The snapshots are written by run-steps.sh (grep of the API server /metrics on the control VM).
"""
import re, sys

def parse(path):
    res = {}
    for line in open(path):
        m = re.match(r'(\S+?)(\{.*\})? ([0-9.e+]+)$', line.strip())
        if m and ('flowcontrol' in m.group(1) or 'apiserver_request_total' in m.group(1)):
            res[m.group(1) + (m.group(2) or '')] = float(m.group(3))
    return res

b, a = parse(sys.argv[1]), parse(sys.argv[2])
out = []
for k in a:
    d = a[k] - b.get(k, 0)
    if 'rejected_requests_total' in k:
        out.append('APF rejected (%s) +%d' % (re.search(r'reason="([^"]+)"', k).group(1), d))
    elif 'apiserver_request_total' in k:
        # every request to dhcpsubnets: status PUTs (dhcpd's updates) and GETs (dhcpd's re-reads on each retry, and the generator's polling)
        verb = re.search(r'verb="(\w+)"', k).group(1)
        sub = re.search(r'subresource="(\w*)"', k)
        out.append('%s%s %s +%d' % (verb, '/' + sub.group(1) if sub and sub.group(1) else '', re.search(r'code="(\d+)"', k).group(1), d))
print(sorted(out))
