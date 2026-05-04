#!/usr/bin/env python3
"""Read a ghcr tags-list JSON from stdin, print the latest vX.Y.Z tag.

Used by hack/release.sh's `latest_ghcr_tag` helper to pin operator
images at release-time. Lives in its own file because escaping multi-
line python through bash heredocs is unreliable.
"""

import json
import re
import sys


def main() -> int:
    try:
        data = json.load(sys.stdin)
    except json.JSONDecodeError:
        return 0
    tags = [t for t in data.get("tags", []) if re.match(r"^v[0-9]+\.[0-9]+\.[0-9]+$", t)]

    def key(tag: str) -> tuple[int, int, int]:
        major, minor, patch = tag.lstrip("v").split(".")
        return (int(major), int(minor), int(patch))

    tags.sort(key=key, reverse=True)
    if tags:
        print(tags[0])
    return 0


if __name__ == "__main__":
    sys.exit(main())
