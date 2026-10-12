#!/usr/bin/env python3
# Copyright 2025 Hedgehog
# SPDX-License-Identifier: Apache-2.0
"""Write summary.txt (about 30 lines) into a run directory: generator result, API server counter deltas, UDP sent vs received,
sampler maxima, dhcpd error causes. The wrapper calls it after each run; read this file instead of the raw data.
usage: summarize-run.py <rundir> [...]"""
import collections, datetime, json, os, re, subprocess, sys, zlib
HERE = os.path.dirname(os.path.abspath(__file__))

def udp(f):
    for l in open(f):
        if l.startswith("Udp:"):
            v = l.split()[1:]
            if all(x.isdigit() for x in v): return list(map(int, v))

def dhcpd_errors(path, win=None):
    raw = open(path, "rb").read(); out = b""
    while raw:
        d = zlib.decompressobj(31)
        try: out += d.decompress(raw)
        except zlib.error: out += raw; break
        raw = d.unused_data
        if not d.eof: break
    c = collections.Counter()
    for l in out.decode(errors="replace").splitlines():
        if " ERR Error handling" in l:
            if win:
                m = re.search(r"(\w{3} +\d+ \d\d:\d\d:\d\d)", l)
                if not m: continue
                t = datetime.datetime.strptime(f"{win[0].year} {m[1]}", "%Y %b %d %H:%M:%S").replace(tzinfo=datetime.timezone.utc)
                if not (win[0] <= t <= win[1]): continue
            c["409 conflict, retries exhausted" if "the object has been modified" in l else "not found" if "not found" in l else "504/timeout" if ("Timeout" in l or "timeout" in l) else "429" if re.search(r"\b429\b", l.split("err=",1)[-1]) or "Too many" in l else "other"] += 1
    return c

def k3s_cpu(path):
    """max and mean millicores of k3s-server from the cumulative tick column of sampler.tsv (100 ticks per CPU second)"""
    rows = [l.rstrip("\n").split("\t") for l in open(path)][1:]
    pts = []
    for x in rows:
        if len(x) > 11 and x[11].isdigit() and re.fullmatch(r"\d\d:\d\d:\d\d", x[0]):
            h, m, s_ = map(int, x[0].split(":")); pts.append((h * 3600 + m * 60 + s_, int(x[11])))
    rates = [(b[1] - a[1]) * 10 / (b[0] - a[0]) for a, b in zip(pts, pts[1:]) if b[0] > a[0]]
    return (round(max(rates)), round(sum(rates) / len(rates))) if rates else None

def one(d):
    L = []
    run = open(f"{d}/run.log", errors="replace").read() if os.path.exists(f"{d}/run.log") else ""
    s = re.findall(r"clients=(\d+) bound=(\d+) failed=(\d+) never_started=(\d+) elapsed=(\S+)", run)
    pr = re.findall(r"sent=(\d+) retx=(\d+) naks=(\d+) empty=(\d+)", run)
    dora = re.findall(r"dora\s+n=\d+\s+p50=(\S+)\s+p95=(\S+)\s+p99=(\S+)\s+max=(\S+)", run)
    st = re.search(r"Starting clients=(\d+) servers=(\d+) nics=(\d+) leaves=\d+ subnets=(\d+) layout=(\w+) ramp=(\S+)", run)
    L.append(f"run {os.path.basename(d)}")
    if st: L.append(f"load: {st[1]} clients = {st[2]} servers x {st[3]} NICs, {st[4]} subnets, ramp {st[6]}")
    if s: L.append(f"result: bound {s[-1][1]}/{s[-1][0]}, failed {s[-1][2]}, elapsed {s[-1][4]}")
    if pr: L.append(f"generator: sent {pr[-1][0]}, retransmits {pr[-1][1]}, empty replies {pr[-1][3]}")
    if dora: L.append(f"DORA p50/p95/p99/max: {dora[-1][0]} / {dora[-1][1]} / {dora[-1][2]} / {dora[-1][3]}")
    b, a = f"{d}/metrics-before.txt", f"{d}/metrics-after.txt"
    if os.path.exists(a) and os.path.exists(b):
        o = subprocess.run([sys.executable, f"{HERE}/metrics-delta.py", b, a], capture_output=True, text=True).stdout
        kv = dict((k, int(v)) for k, v in re.findall(r"'([^']+?) \+(\d+)'", o))
        keep = [k for k in ("PUT/status 200", "PUT/status 409", "PUT/status 429", "PUT/status 504", "GET 200", "GET 429", "APF rejected (queue-full)", "APF rejected (time-out)") if k in kv]
        L.append("API server (dhcpsubnets, before/after): " + ", ".join(f"{k} +{kv[k]}" for k in keep))
        ub, ua = udp(b), udp(a)
        if ub and ua and pr:
            L.append(f"UDP: control VM received {ua[0]-ub[0]} for {pr[-1][0]} sent by the generator (received minus sent {ua[0]-ub[0]-int(pr[-1][0])}); RcvbufErrors +{ua[4]-ub[4]}")
    if os.path.exists(f"{d}/sampler.tsv"):
        rows = [l.split("\t") for l in open(f"{d}/sampler.tsv")][1:]
        def mx(i): 
            v = [int(x[i]) for x in rows if len(x) > i and x[i].strip().isdigit()]; return max(v) if v else None
        L.append(f"sampler: max hh-control queue {mx(6)}, max dhcpd RSS {mx(3)} kB, min available memory {min([int(x[2]) for x in rows if len(x)>2 and x[2].isdigit()] or [0])} MB")
    if os.path.exists(f"{d}/sampler.tsv"):
        cpu = k3s_cpu(f"{d}/sampler.tsv")
        L.append("k3s CPU (millicores between samples): " + (f"max {cpu[0]}, mean {cpu[1]}" if cpu else "not sampled"))
    if os.path.exists(f"{d}/dhcpd.raw"):
        win = None
        try:
            m = json.load(open(f"{d}/manifest.json")); f = lambda x: datetime.datetime.fromisoformat(x.replace("Z", "+00:00"))
            win = (f(m["start_utc"]), f(m["end_utc"]) + datetime.timedelta(seconds=-0))
        except (OSError, ValueError, KeyError): pass
        c = dhcpd_errors(f"{d}/dhcpd.raw", win); L.append("dhcpd 'Error handling' lines " + ("in the run window (start to end of the wrapper step): " if win else "(whole log): ") + (", ".join(f"{k} {v}" for k, v in c.most_common()) or "none"))
    if os.path.exists(f"{d}/drain.txt"): L.append("backlog check: " + open(f"{d}/drain.txt").read().strip())
    open(f"{d}/summary.txt", "w").write("\n".join(L) + "\n")
    return len(L)

for d in sys.argv[1:]: one(d.rstrip("/"))
