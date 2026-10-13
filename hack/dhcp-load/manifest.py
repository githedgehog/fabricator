#!/usr/bin/env python3
# Copyright 2025 Hedgehog
# SPDX-License-Identifier: Apache-2.0
"""Per-run manifest for dhcp-load runs. Standard library only; called by run-steps.sh on the host that runs the VLAB.

usage:
  manifest.py start <rundir> --id ID --command CMD [--prefix P] [--group G] [--note TEXT]
  manifest.py end   <rundir>

`start` writes <rundir>/manifest.json with everything known before the generator runs. `end` parses <rundir>/run.log and fills in
the end time, exit code and result. Values that cannot be determined are the literal string "unknown", never a guess.
Environment: HHFAB_DIR, HHFAB_BIN, CTRL_PORT as in run-steps.sh; ACCESS_SERVER names the generator server (otherwise "unknown").
"""
import argparse, datetime, hashlib, json, os, re, subprocess, sys

UNKNOWN = "unknown"
HHFAB_DIR = os.path.expanduser(os.environ.get("HHFAB_DIR", "~/hhfab"))
HHFAB = os.path.join(HHFAB_DIR, os.environ.get("HHFAB_BIN", "./hhfab-dhcpload"))
K = ["ssh", "-i", os.path.join(HHFAB_DIR, "vlab/sshkey"), "-p", os.environ.get("CTRL_PORT", "22000"), "-o", "StrictHostKeyChecking=no",
     "-o", "UserKnownHostsFile=/dev/null", "-o", "LogLevel=ERROR", "core@127.0.0.1"]


def now():
    return datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def sh(cmd, timeout=20):
    try:
        return subprocess.run(cmd, capture_output=True, text=True, timeout=timeout).stdout.strip()
    except Exception:  # noqa: BLE001 - best effort, the field becomes "unknown"
        return ""


def sha256(path):
    try:
        h = hashlib.sha256()
        with open(path, "rb") as f:
            for chunk in iter(lambda: f.read(1 << 20), b""):
                h.update(chunk)
        return h.hexdigest()
    except OSError:
        return UNKNOWN


def vm_sizes(name):
    out = sh(["pgrep", "-af", f"qemu-system-x86_64 -name {name}"])
    smp = re.search(r"-smp (\S+)", out)
    mem = re.search(r"-m (\d+)M", out)
    if not (smp and mem):
        return UNKNOWN
    return f"{smp[1]} vCPU, {mem[1]} MiB"


def kube(cmd):
    return sh(K + [f"sudo /opt/bin/kubectl {cmd}"])


def start(a):
    d = a.rundir
    here = os.path.dirname(os.path.abspath(__file__))
    scripts = {}
    for n in ("run-steps.sh", "sampler.sh", "manifest.py"):
        p = os.path.join(here, n)
        scripts[n] = sha256(p) if os.path.exists(p) else UNKNOWN
    build = {}
    try:
        build = json.load(open(HHFAB + ".build.json"))
    except (OSError, ValueError):
        pass
    prefix = a.prefix or UNKNOWN
    m = {
        "schema": 1,
        "id": a.id,
        "kind": "dhcp-load",
        "provenance": "recorded",
        "group": a.group or UNKNOWN,
        "start_utc": now(),
        "end_utc": UNKNOWN,
        "command": a.command,
        "exit_code": UNKNOWN,
        "hhfab": {
            "binary": HHFAB,
            "sha256": sha256(HHFAB),
            "size": os.path.getsize(HHFAB) if os.path.exists(HHFAB) else UNKNOWN,
            "commit": build.get("commit", UNKNOWN),
            "built_utc": build.get("built_utc", UNKNOWN),
            "note": build.get("note", ""),
        },
        "scripts_sha256": scripts,
        "env": {
            "control_vm": vm_sizes("control-1"),
            "generator_vm": vm_sizes(os.environ["ACCESS_SERVER"]) if os.environ.get("ACCESS_SERVER") else UNKNOWN,
            "host": f"{os.cpu_count()} CPUs",
            "dhcpd_image": kube("-n fab get deploy fabric-dhcpd -o jsonpath={.spec.template.spec.containers[0].image}") or UNKNOWN,
            "k3s": (re.search(r'"gitVersion": "([^"]+)"', kube("version -o json | grep gitVersion")) or [None, UNKNOWN])[1],
            "config_dir": f"{prefix}-config" if os.path.isdir(os.path.join(os.path.dirname(os.path.abspath(d)), f"{prefix}-config")) else UNKNOWN,
        },
        "pre_checks": {
            "backlog_drained": (open(os.path.join(d, "drain.txt")).read().strip() if os.path.exists(os.path.join(d, "drain.txt")) else UNKNOWN),
            "leftovers_removed": "by the generator at its start (not logged)",
            "leftovers_verified": "not checked",
        },
        "notes": a.note or "",
    }
    os.makedirs(d, exist_ok=True)
    json.dump(m, open(os.path.join(d, "manifest.json"), "w"), indent=1)


def end(a):
    d = a.rundir
    p = os.path.join(d, "manifest.json")
    m = json.load(open(p))
    log = open(os.path.join(d, "run.log"), errors="replace").read()
    m["end_utc"] = now()
    e = re.search(r"EXIT=(\d+)", log)
    m["exit_code"] = int(e[1]) if e else UNKNOWN
    s = re.search(r"Starting clients=(\d+) servers=(\d+) nics=(\d+) leaves=(\d+) subnets=(\d+) layout=(\w+) ramp=(\S+) retries=(\w+) duration=(\S+)", log)
    if s:
        m["load"] = dict(zip(["clients", "servers", "nics", "leaves", "subnets", "layout", "ramp", "retries", "duration"], s.groups()))
    f = re.findall(r"clients=(\d+) bound=(\d+) failed=(\d+) never_started=(\d+) elapsed=(\S+)", log)
    pr = re.findall(r"Progress t=\S+ started=\d+ bound=\d+ failed=\d+ sent=\d+ retx=(\d+) naks=(\d+) empty=(\d+)", log)
    dora = re.findall(r"dora\s+n=\d+\s+p50=\S+\s+p95=\S+\s+p99=(\S+)", log)
    if f:
        m["summary"] = {"clients": int(f[-1][0]), "bound": int(f[-1][1]), "failed": int(f[-1][2]), "elapsed": f[-1][4],
                        "retransmits": int(pr[-1][0]) if pr else UNKNOWN, "empty_replies": int(pr[-1][2]) if pr else UNKNOWN,
                        "dora_p99": dora[-1] if dora else UNKNOWN}
    else:
        m["summary"] = UNKNOWN
    cl = os.path.join(d, "commands.log")
    m["commands_run_by_wrapper"] = [l.rstrip("\n").split("\t", 2) for l in open(cl)] if os.path.exists(cl) else UNKNOWN
    keep = "--keep" in m["command"]
    m["keep"] = keep
    json.dump(m, open(p, "w"), indent=1)


if __name__ == "__main__":
    ap = argparse.ArgumentParser()
    sub = ap.add_subparsers(dest="cmd", required=True)
    s = sub.add_parser("start")
    s.add_argument("rundir")
    s.add_argument("--id", required=True)
    s.add_argument("--command", required=True)
    s.add_argument("--prefix")
    s.add_argument("--group")
    s.add_argument("--note")
    e = sub.add_parser("end")
    e.add_argument("rundir")
    a = ap.parse_args()
    {"start": start, "end": end}[a.cmd](a)
