#!/usr/bin/env python3
"""Release-impact report: what a release does to live clusters beyond its images.

Usage: hack/release-impact.py PREV_TAG

Prints a markdown block (nothing when there is nothing to report) covering:

  RBAC   Roles/ClusterRoles in deploy/*.yaml changed since PREV_TAG that the
         updater cannot apply: it runs as kuso-server, and Kubernetes'
         escalation prevention refuses any role granting a permission
         kuso-server doesn't hold (or one excluded from the bundle outright).
         Those need a manual `kubectl apply` as cluster-admin on every cluster.
  Pods   Charts whose rendered pod template changed since PREV_TAG. The
         operator image embeds the charts, so its roll re-renders every CR
         and restarts every matching env/addon at once.

Needs PyYAML; the pod-template section also needs helm (skipped with a note
when it's missing).
"""
import difflib
import glob
import json
import os
import shutil
import subprocess
import sys
import tarfile
import tempfile

import yaml

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))

# Keep in step with ESCALATION_EXCLUDED in hack/release.sh (bundle builder).
BUNDLE_EXCLUDED = {("ClusterRole", "kuso-operator"), ("ClusterRoleBinding", "kuso-operator")}

POD_FIXTURES = [
    ("kusoenvironment", "p-web-production", {
        "project": "p", "service": "web",
        "image": {"repository": "registry.local/p/web", "tag": "abc"}, "host": "web.example.com"}),
    ("kusoenvironment", "p-web-rwo", {
        "project": "p", "service": "web",
        "image": {"repository": "registry.local/p/web", "tag": "abc"}, "host": "web.example.com",
        "volumes": [{"name": "data", "mountPath": "/data", "size": "1Gi"}]}),
    ("kusoenvironment", "p-worker-production", {
        "project": "p", "service": "worker", "runtime": "worker",
        "image": {"repository": "registry.local/p/worker", "tag": "abc"}}),
] + [("kusoaddon", f"p-{k}", {"project": "p", "kind": k}) for k in (
    "postgres", "redis", "valkey", "mongodb", "mysql", "rabbitmq",
    "s3", "mailpit", "nats", "meilisearch", "clickhouse", "redpanda")]


def git(*args):
    return subprocess.run(["git", "-C", ROOT, *args], capture_output=True, text=True)


def load_docs(text):
    return [d for d in yaml.safe_load_all(text) if isinstance(d, dict)]


def rbac_docs(ref):
    """{(kind, name, file): doc} for Role/ClusterRole docs in deploy/ at ref (None = worktree)."""
    out = {}
    if ref is None:
        files = sorted(glob.glob(os.path.join(ROOT, "deploy", "*.yaml")))
        sources = [(os.path.relpath(f, ROOT), open(f).read()) for f in files]
    else:
        ls = git("ls-tree", "--name-only", ref, "deploy/")
        sources = []
        for f in ls.stdout.split():
            if f.endswith(".yaml"):
                sources.append((f, git("show", f"{ref}:{f}").stdout))
    for f, text in sources:
        for d in load_docs(text):
            if d.get("kind") in ("Role", "ClusterRole"):
                out[(d["kind"], d["metadata"]["name"], f)] = d
    return out


def expand(rules):
    """Rules → set of (group, resource, verb, resourceName-or-None)."""
    out = set()
    for r in rules or []:
        for g in r.get("apiGroups") or [""]:
            for res in r.get("resources") or []:
                for v in r.get("verbs") or []:
                    for n in r.get("resourceNames") or [None]:
                        out.add((g, res, v, n))
    return out


def held(perms, need):
    """A name-scoped grant covers only that name; an unscoped one covers all."""
    g, res, v, n = need
    return any((hg in (g, "*")) and (hr in (res, "*")) and (hv in (v, "*")) and (hn is None or hn == n)
               for hg, hr, hv, hn in perms)


def rbac_section(prev):
    old, new = rbac_docs(prev), rbac_docs(None)
    old_by_id = {(k, n): d for (k, n, _), d in old.items()}
    # What kuso-server holds while the updater runs is the PREVIOUS release's
    # grant: its own ClusterRole cluster-wide, plus kuso-server-managed-ns in
    # namespaced roles' namespaces.
    server_cluster = expand((old_by_id.get(("ClusterRole", "kuso-server")) or {}).get("rules"))
    server_ns = server_cluster | expand((old_by_id.get(("ClusterRole", "kuso-server-managed-ns")) or {}).get("rules"))
    lines = []
    for (kind, name, f), doc in sorted(new.items()):
        before = old_by_id.get((kind, name))
        if before is not None and before.get("rules") == doc.get("rules"):
            continue
        if (kind, name) in BUNDLE_EXCLUDED:
            lines.append(f"- `{kind}/{name}` ({f}) changed; it is excluded from the upgrade bundle.")
            continue
        perms = server_ns if kind == "Role" else server_cluster
        missing = sorted({(g, r, v) for g, r, v, n in expand(doc.get("rules")) if not held(perms, (g, r, v, n))})
        if missing:
            shown = ", ".join(f"{g or 'core'}/{r}:{v}" for g, r, v in missing[:6])
            more = f" (+{len(missing) - 6} more)" if len(missing) > 6 else ""
            lines.append(f"- `{kind}/{name}` ({f}) changed and grants what kuso-server doesn't hold: {shown}{more}.")
    if not lines:
        return ""
    return ("### Manual RBAC apply required\n\n"
            "The self-updater can't apply these; on every cluster run "
            "`kubectl apply -f <file>` as cluster-admin after upgrading:\n\n" + "\n".join(lines) + "\n")


def pod_templates(manifest):
    out = {}
    for d in load_docs(manifest):
        if d.get("kind") in ("Deployment", "StatefulSet"):
            tpl = ((d.get("spec") or {}).get("template")) or {}
            out[f"{d['kind']}/{d['metadata']['name']}"] = tpl
    return out


def _flatten(node, path=()):
    if isinstance(node, dict):
        for k, v in node.items():
            yield from _flatten(v, path + (k,))
    elif isinstance(node, list):
        for i, v in enumerate(node):
            yield from _flatten(v, path + (i,))
    else:
        yield path, node


def unstable(a, b):
    fa, fb = dict(_flatten(a)), dict(_flatten(b))
    return {p for p in set(fa) | set(fb) if fa.get(p) != fb.get(p)}


def masked(node, paths, path=()):
    if path in paths:
        return "<varies per render>"
    if isinstance(node, dict):
        return {k: masked(v, paths, path + (k,)) for k, v in node.items()}
    if isinstance(node, list):
        return [masked(v, paths, path + (i,)) for i, v in enumerate(node)]
    return node


def render(charts_dir, chart, release, values):
    with tempfile.NamedTemporaryFile("w", suffix=".json", delete=False) as fh:
        json.dump(values, fh)
        vals = fh.name
    try:
        res = subprocess.run(["helm", "template", release, os.path.join(charts_dir, chart),
                              "--namespace", "kuso", "-f", vals], capture_output=True, text=True)
    finally:
        os.unlink(vals)
    return res.stdout if res.returncode == 0 else None


def pods_section(prev):
    if not shutil.which("helm"):
        return "### Pod template diff skipped\n\nhelm is not on PATH; check by hand whether this release restarts every env.\n"
    tmp = tempfile.mkdtemp()
    try:
        archive = subprocess.run(["git", "-C", ROOT, "archive", prev, "operator/helm-charts"],
                                 capture_output=True)
        if archive.returncode != 0:
            return ""
        tar_path = os.path.join(tmp, "charts.tar")
        with open(tar_path, "wb") as fh:
            fh.write(archive.stdout)
        with tarfile.open(tar_path) as tf:
            try:
                tf.extractall(tmp, filter="data")
            except TypeError:  # Python < 3.12 without the filter backport
                tf.extractall(tmp)
        old_dir = os.path.join(tmp, "operator", "helm-charts")
        new_dir = os.path.join(ROOT, "operator", "helm-charts")
        changed = []
        for chart, release, values in POD_FIXTURES:
            renders = [render(d, chart, release, values) for d in (old_dir, old_dir, new_dir, new_dir)]
            if None in renders:
                continue
            o1, o2, n1, n2 = (pod_templates(r) for r in renders)
            for key in sorted(set(o1) & set(n1)):
                # Values that differ between two renders of the SAME chart
                # (randAlphaNum passwords with no lookup) aren't a change.
                noisy = unstable(o1[key], o2.get(key)) | unstable(n1[key], n2.get(key))
                old_t, new_t = masked(o1[key], noisy), masked(n1[key], noisy)
                if old_t != new_t:
                    diff = difflib.unified_diff(
                        yaml.safe_dump(old_t, sort_keys=True).splitlines(),
                        yaml.safe_dump(new_t, sort_keys=True).splitlines(), lineterm="", n=1)
                    body = [l for l in diff if not l.startswith(("---", "+++"))][:12]
                    changed.append(f"- {chart} `{release}` {key}:\n\n```diff\n" + "\n".join(body) + "\n```")
    finally:
        shutil.rmtree(tmp, ignore_errors=True)
    if not changed:
        return ""
    return ("### This release restarts workloads\n\n"
            "The pod template changed for the shapes below. When the operator rolls, every env/addon "
            "of that shape restarts at about the same time (envs with a ReadWriteOnce volume use "
            "Recreate, so they go down for the restart). Upgrade in a quiet window.\n\n"
            + "\n".join(changed) + "\n")


def main(argv):
    if len(argv) != 1:
        raise SystemExit(__doc__)
    prev = argv[0]
    if git("rev-parse", "--verify", "--quiet", f"{prev}^{{commit}}").returncode != 0:
        raise SystemExit(f"release-impact: unknown ref {prev}")
    sections = [s for s in (rbac_section(prev), pods_section(prev)) if s]
    if sections:
        print("\n".join(sections))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
