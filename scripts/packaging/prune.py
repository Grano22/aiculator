#!/usr/bin/env python3
"""Deletes all but the newest versions of the packages in a directory.

usage: prune.py KEEP DIR [DIR...]
Handles aiculator_1.2.3_amd64.deb, aiculator-1.2.3-1.x86_64.rpm and aiculator-1.2.3.apk.
"""
import re
import sys
from pathlib import Path

NAME = re.compile(r"^aiculator[_-](\d+)\.(\d+)\.(\d+)(?:[_-].*)?\.(deb|rpm|apk)$")


def prune(directory, keep):
    found = {}
    for path in Path(directory).iterdir():
        m = NAME.match(path.name)
        if m:
            found.setdefault(tuple(int(x) for x in m.groups()[:3]), []).append(path)
    removed = []
    for version in sorted(found, reverse=True)[keep:]:
        for path in found[version]:
            path.unlink()
            removed.append(path.name)
    return sorted(removed)


if __name__ == "__main__":
    for d in sys.argv[2:]:
        for name in prune(d, int(sys.argv[1])):
            print("removed", name)
