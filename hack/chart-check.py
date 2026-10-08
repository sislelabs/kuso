#!/usr/bin/env python3
"""Static + render checks on the operator's helm charts.

  rbac   Every apiVersion/kind a watched chart can render is covered by the
         kuso-operator ClusterRole in deploy/operator.yaml. A gap only shows
         up on a live cluster, as a Forbidden that wedges the CR's helm
         release (public TCP, project quota).
  names  Project/service names that are valid DNS labels but YAML 1.1
         scalars of another type (2026, no, on, 1e3, 0x1f) render quoted.
         Helm decodes manifests untyped, so an unquoted label or name
         becomes an int/bool and the apiserver rejects the whole release.
  render Write a matrix of rendered manifests to DIR, one file per render,
         for `kubectl apply --dry-run=server` (hack/ci/apiserver-validate.sh).

Usage: hack/chart-check.py [rbac] [names] [render DIR]   (default: rbac names)
Needs helm on PATH and PyYAML.
"""
import json
import os
import re
import subprocess
import sys
import tempfile

import yaml

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
CHARTS = os.path.join(ROOT, "operator", "helm-charts")
HELM_VERBS = {"get", "list", "watch", "create", "update", "patch", "delete"}

# YAML 1.1 resolves each of these to a non-string, and every one passes the
# server's DNS-label validators.
EDGE_NAMES = ["2026", "no", "on", "y", "1e3", "0x1f"]

ADDON_KINDS = ["postgres", "redis", "valkey", "mongodb", "mysql", "rabbitmq",
               "s3", "mailpit", "nats", "meilisearch", "clickhouse", "redpanda"]
ADDON_HA_KINDS = ["postgres", "redis", "nats"]


def watched_charts():
    with open(os.path.join(ROOT, "operator", "watches.yaml")) as fh:
        watches = yaml.safe_load(fh) or []
    return [os.path.basename(w["chart"]) for w in watches if w.get("chart")]


def plural(kind):
    k = kind.lower()
    if k.endswith("y"):
        return k[:-1] + "ies"
    if k.endswith("s"):
        return k + "es"
    return k + "s"


def template_gvks(chart):
    """apiVersion/kind pairs written literally in a chart's templates.

    Read from source rather than a render so a branch the render matrix
    doesn't reach (quota, public TCP, CNPG) is still covered."""
    out = set()
    tdir = os.path.join(CHARTS, chart, "templates")
    for name in sorted(os.listdir(tdir)):
        if not name.endswith((".yaml", ".yml", ".tpl")):
            continue
        api = None
        with open(os.path.join(tdir, name)) as fh:
            for line in fh:
                m = re.match(r"^apiVersion:\s*(\S+)\s*$", line)
                if m:
                    api = m.group(1)
                    continue
                m = re.match(r"^kind:\s*(\S+)\s*$", line)
                if m and api:
                    group = api.split("/")[0] if "/" in api else ""
                    out.add((group, m.group(1), f"{chart}/templates/{name}"))
                    api = None
    return out


def operator_rules():
    with open(os.path.join(ROOT, "deploy", "operator.yaml")) as fh:
        for doc in yaml.safe_load_all(fh):
            if doc and doc.get("kind") == "ClusterRole" and doc["metadata"]["name"] == "kuso-operator":
                return doc.get("rules") or []
    raise SystemExit("deploy/operator.yaml: no kuso-operator ClusterRole")


def granted_verbs(rules, group, resource):
    verbs = set()
    for r in rules:
        groups = r.get("apiGroups") or []
        resources = r.get("resources") or []
        if (group in groups or "*" in groups) and (resource in resources or "*" in resources):
            verbs |= set(r.get("verbs") or [])
    return {"get", "list", "watch", "create", "update", "patch", "delete"} if "*" in verbs else verbs


def check_rbac():
    rules = operator_rules()
    problems = []
    for chart in watched_charts():
        for group, kind, src in sorted(template_gvks(chart)):
            missing = HELM_VERBS - granted_verbs(rules, group, plural(kind))
            if missing:
                problems.append(f"{src}: {group or 'core'}/{plural(kind)} missing {sorted(missing)}")
    for p in problems:
        print(f"rbac: {p}", file=sys.stderr)
    if problems:
        print("rbac: add the rule to the kuso-operator ClusterRole in deploy/operator.yaml "
              "(live clusters need a manual kubectl apply; the updater can't grant RBAC)",
              file=sys.stderr)
    return not problems


def helm_template(chart, release, values, namespace="kuso"):
    with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as fh:
        json.dump(values, fh)
        vals = fh.name
    try:
        res = subprocess.run(
            ["helm", "template", release, os.path.join(CHARTS, chart),
             "--namespace", namespace, "-f", vals],
            capture_output=True, text=True)
    finally:
        os.unlink(vals)
    return res.returncode, res.stdout, res.stderr


def matrix(p, s):
    """(chart, release, values) renders covering the templates' branches."""
    img = {"repository": "registry.local/x/web", "tag": "abc123"}
    env = {"project": p, "service": s, "image": img, "host": f"{s}.example.com",
           "tlsHosts": [f"{s}.example.com"]}
    yield "kusoproject", p, {"quota": {"enabled": True}}
    yield "kusoservice", f"{p}-{s}", {"project": p}
    yield "kusoenvironment", f"{p}-{s}-production", env
    yield "kusoenvironment", f"{p}-{s}-scaled", {**env, "replicaCount": 2,
                                                  "autoscaling": {"enabled": True, "minReplicas": 2, "maxReplicas": 4}}
    yield "kusoenvironment", f"{p}-{s}-rwo", {**env, "volumes": [{"name": "data", "mountPath": "/data", "size": "1Gi"}]}
    yield "kusoenvironment", f"{p}-{s}-sleepy", {**env, "sleep": {"enabled": True}}
    yield "kusoenvironment", f"{p}-{s}-worker", {**env, "runtime": "worker"}
    yield "kusocron", f"{p}-{s}-nightly", {"project": p, "service": s, "schedule": "0 3 * * *",
                                           "image": img, "command": ["true"]}
    yield "kusocron", f"{p}-{s}-ping", {"project": p, "service": s, "schedule": "*/5 * * * *",
                                        "kind": "http", "url": "https://example.com"}
    yield "kusorun", f"{p}-{s}-r1", {"project": p, "service": s, "image": img, "command": ["true"]}
    for k in ADDON_KINDS:
        base = {"project": p, "kind": k}
        if k == "postgres":
            base = {**base, "backup": {"schedule": "0 2 * * *", "bucket": "b"},
                    "pooler": {"enabled": True}, "publicTCP": {"enabled": True, "port": 30001}}
        yield "kusoaddon", f"{p}-{k}", base
        if k in ADDON_HA_KINDS:
            yield "kusoaddon", f"{p}-{k}-ha", {"project": p, "kind": k, "ha": True}


def check_names():
    ok = True
    for edge in EDGE_NAMES:
        # A bare scalar equal to the injected name: `key: 2026` or `- 2026`.
        bare = re.compile(r"^\s*(?:-\s+)?(?:[^\s:#][^:#]*:\s+)?" + re.escape(edge) + r"\s*$")
        for chart, release, values in matrix(edge, edge):
            rc, out, err = helm_template(chart, release, values)
            if rc != 0:
                print(f"names: {chart} {release}: helm template failed: {err.strip()}", file=sys.stderr)
                ok = False
                continue
            for n, line in enumerate(out.splitlines(), 1):
                if bare.match(line):
                    print(f"names: {chart} (project/service={edge!r}) line {n}: unquoted: {line.strip()}",
                          file=sys.stderr)
                    ok = False
    return ok


def render(outdir):
    os.makedirs(outdir, exist_ok=True)
    ok = True
    # Letter-first edge names: a digit-led project ("2026") also fails DNS-1035
    # Service names, which no amount of quoting fixes (see the names check).
    for p, s in (("alpha", "web"), ("no", "on"), ("y", "1e3")):
        for chart, release, values in matrix(p, s):
            rc, out, err = helm_template(chart, release, values)
            if rc != 0:
                print(f"render: {chart} {release}: {err.strip()}", file=sys.stderr)
                ok = False
                continue
            with open(os.path.join(outdir, f"{chart}--{release}.yaml"), "w") as fh:
                fh.write(out)
    return ok


def main(argv):
    if not argv:
        argv = ["rbac", "names"]
    ok = True
    i = 0
    while i < len(argv):
        cmd = argv[i]
        if cmd == "rbac":
            ok &= check_rbac()
        elif cmd == "names":
            ok &= check_names()
        elif cmd == "render" and i + 1 < len(argv):
            ok &= render(argv[i + 1])
            i += 1
        else:
            raise SystemExit(__doc__)
        i += 1
    print("chart-check: " + ("ok" if ok else "FAILED"))
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
