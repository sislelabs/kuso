#!/usr/bin/env python3
"""CRD breaking-change guard: diff one CRD file against its previous version.

Usage: hack/crd-guard.py OLD.yaml NEW.yaml   (exit 1 + report on breaking diffs)

Run by hack/release.sh against the previous release tag. The updater applies
the release's crds.yaml blindly and its server-side dry-run validates only the
CRD objects, never the CRs already stored, so anything that can make a stored
CR invalid or silently pruned has to be caught here.
"""
import sys

import yaml

# Bounds whose addition or narrowing rejects values that were valid before.
UPPER_BOUNDS = ("maxLength", "maxItems", "maxProperties", "maximum")
LOWER_BOUNDS = ("minLength", "minItems", "minProperties", "minimum")


def _bounds(o, n, path, problems):
    for key in UPPER_BOUNDS:
        if key in n and (key not in o or n[key] < o[key]):
            problems.append(f"{path}: {key} tightened {o.get(key, 'unset')!r} -> {n[key]!r}")
    for key in LOWER_BOUNDS:
        if key in n and (key not in o or n[key] > o[key]):
            problems.append(f"{path}: {key} tightened {o.get(key, 'unset')!r} -> {n[key]!r}")
    for key in ("exclusiveMaximum", "exclusiveMinimum"):
        if n.get(key) is True and o.get(key) is not True:
            problems.append(f"{path}: {key} added")
    # A changed regex/format can't be proven wider, so any change counts.
    for key in ("pattern", "format"):
        if key in n and n.get(key) != o.get(key):
            problems.append(f"{path}: {key} added/changed {o.get(key)!r} -> {n[key]!r}")
    if o.get("nullable") is True and n.get("nullable") is not True:
        problems.append(f"{path}: nullable removed")


def _cel(o, n, path, problems):
    old_rules = {r.get("rule") for r in (o.get("x-kubernetes-validations") or []) if isinstance(r, dict)}
    for r in n.get("x-kubernetes-validations") or []:
        if isinstance(r, dict) and r.get("rule") not in old_rules:
            problems.append(f"{path}: new x-kubernetes-validations rule {r.get('rule')!r}")


def walk(o, n, path, problems):
    """Recursively compare two openAPIV3Schema nodes; append breaking diffs."""
    if not isinstance(o, dict):
        return
    if not isinstance(n, dict):
        problems.append(f"{path}: schema node removed")
        return
    ot, nt = o.get("type"), n.get("type")
    if ot and nt and ot != nt:
        problems.append(f"{path}: type changed {ot} -> {nt}")
    oe, ne = o.get("enum"), n.get("enum")
    if isinstance(ne, list):
        if isinstance(oe, list):
            removed = [v for v in oe if v not in ne]
            if removed:
                problems.append(f"{path}: enum values removed: {removed}")
        else:
            problems.append(f"{path}: enum added to previously-unrestricted field: {ne}")
    if "default" in o:
        if "default" not in n:
            problems.append(f"{path}: default removed (was {o['default']!r})")
        elif n["default"] != o["default"]:
            problems.append(f"{path}: default changed {o['default']!r} -> {n['default']!r}")
    newly_required = sorted(set(n.get("required") or []) - set(o.get("required") or []))
    if newly_required:
        problems.append(f"{path}: newly required: {newly_required}")
    # Dropping preserve-unknown-fields makes the apiserver prune every field
    # the schema doesn't name on the next write of each stored CR.
    if o.get("x-kubernetes-preserve-unknown-fields") is True and \
            n.get("x-kubernetes-preserve-unknown-fields") is not True:
        problems.append(f"{path}: x-kubernetes-preserve-unknown-fields removed (stored fields get pruned)")
    _bounds(o, n, path, problems)
    _cel(o, n, path, problems)
    op, np = o.get("properties") or {}, n.get("properties") or {}
    if isinstance(op, dict) and isinstance(np, dict):
        for k, ov in op.items():
            if k not in np:
                problems.append(f"{path}.{k}: field removed")
            else:
                walk(ov, np[k], f"{path}.{k}", problems)
    oi, ni = o.get("items"), n.get("items")
    if isinstance(oi, dict):
        if isinstance(ni, dict):
            walk(oi, ni, path + "[]", problems)
        else:
            problems.append(f"{path}[]: items schema removed")
    oa, na = o.get("additionalProperties"), n.get("additionalProperties")
    if isinstance(oa, dict):
        if isinstance(na, dict):
            walk(oa, na, path + ".*", problems)
        else:
            problems.append(f"{path}.*: additionalProperties schema removed")


def _versions(doc):
    return {v.get("name"): v for v in ((doc.get("spec") or {}).get("versions") or [])}


def compare(old, new):
    """Return the list of breaking differences between two CRD documents."""
    problems = []
    # Identity fields: changing any of these re-keys every existing CR.
    for keys in (("spec", "group"), ("spec", "scope"),
                 ("spec", "names", "plural"), ("spec", "names", "kind")):
        o, n = old, new
        for k in keys:
            o = (o or {}).get(k)
            n = (n or {}).get(k)
        if o is not None and o != n:
            problems.append(f"{'.'.join(keys)}: changed {o!r} -> {n!r}")

    ov, nv = _versions(old), _versions(new)
    for name, oldv in ov.items():
        if name not in nv:
            problems.append(f"version {name}: removed")
            continue
        if oldv.get("served", True) and not nv[name].get("served", True):
            problems.append(f"version {name}: served flipped to false")
        os_ = ((oldv.get("schema") or {}).get("openAPIV3Schema")) or {}
        ns_ = ((nv[name].get("schema") or {}).get("openAPIV3Schema")) or {}
        walk(os_, ns_, name, problems)
    return problems


def main(argv):
    if len(argv) != 2:
        raise SystemExit(__doc__)
    old_path, new_path = argv
    with open(old_path) as fh:
        old = yaml.safe_load(fh)
    with open(new_path) as fh:
        new = yaml.safe_load(fh)
    problems = compare(old, new)
    if problems:
        crd = (new.get("metadata") or {}).get("name", new_path)
        print(f"  BREAKING: {crd}", file=sys.stderr)
        for p in problems:
            print(f"    - {p}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
