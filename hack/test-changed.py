#!/usr/bin/env python3
"""Print the Go packages (repo-relative, one per line) touched since a base
ref, plus every package in the same module whose build or tests depend on
them. Uncommitted and untracked changes count as touched.

usage: hack/test-changed.py [base]   (default origin/main)

A changed go.mod/go.sum selects its whole module. Non-Go inputs that tests
read at runtime (helm charts, CRD YAML) are not tracked: run the owning
package by hand when you change those.
"""
import json
import os
import subprocess
import sys


def sh(*args, cwd=None):
    return subprocess.run(args, cwd=cwd, check=True, capture_output=True, text=True).stdout


def main():
    base = sys.argv[1] if len(sys.argv) > 1 else "origin/main"
    root = sh("git", "rev-parse", "--show-toplevel").strip()
    merge_base = sh("git", "merge-base", base, "HEAD", cwd=root).strip()
    files = set()
    for out in (
        sh("git", "diff", "--name-only", merge_base, cwd=root),
        sh("git", "ls-files", "--others", "--exclude-standard", cwd=root),
    ):
        files.update(os.path.join(root, f) for f in out.split())

    modules = sh("go", "list", "-m", "-f", "{{.Dir}}", cwd=root).split()
    selected = []
    for mod in modules:
        mine = {f for f in files if f.startswith(mod + os.sep)}
        # A nested module's files belong to it, not to its parent.
        mine = {f for f in mine if not any(m != mod and m.startswith(mod + os.sep) and f.startswith(m + os.sep) for m in modules)}
        if not mine:
            continue
        if any(os.path.basename(f) in ("go.mod", "go.sum") and os.path.dirname(f) == mod for f in mine):
            selected.append(os.path.relpath(mod, root) + "/...")
            continue

        listing = sh("go", "list", "-test", "-e", "-json=ImportPath,Dir,Deps,ForTest", "./...", cwd=mod)
        dec = json.JSONDecoder()
        pkgs, i = [], 0
        while i < len(listing):
            if listing[i].isspace():
                i += 1
                continue
            obj, i = dec.raw_decode(listing, i)
            pkgs.append(obj)

        dir_of = {}
        for p in pkgs:
            if not p.get("ForTest") and " " not in p["ImportPath"] and not p["ImportPath"].endswith(".test"):
                dir_of[p["ImportPath"]] = p["Dir"]

        def owns(d, f):
            # A file in the package dir or its testdata belongs to the package.
            rel = os.path.relpath(f, d)
            return not rel.startswith("..") and (os.sep not in rel or rel.split(os.sep)[0] == "testdata")

        def is_test_input(d, f):
            return f.endswith("_test.go") or os.path.relpath(f, d).split(os.sep)[0] == "testdata"

        touched = {ip for ip, d in dir_of.items() if any(owns(d, f) for f in mine)}
        if not touched:
            continue
        # Only non-test sources change what dependents compile against.
        changed = {ip for ip in touched if any(owns(dir_of[ip], f) and not is_test_input(dir_of[ip], f) for f in mine)}

        hit = set(touched)
        for p in pkgs:
            # Test variants are listed as "x [x.test]" / "x_test [x.test]" /
            # "x.test"; their Deps are transitive and include test imports.
            name = p["ImportPath"].split(" ")[0]
            owner = p.get("ForTest") or name.removesuffix(".test").removesuffix("_test")
            if owner not in dir_of:
                continue
            if any(d.split(" ")[0] in changed for d in p.get("Deps") or []):
                hit.add(owner)
        for ip in sorted(hit):
            selected.append("./" + os.path.relpath(dir_of[ip], root))

    print("\n".join(selected))


if __name__ == "__main__":
    main()
