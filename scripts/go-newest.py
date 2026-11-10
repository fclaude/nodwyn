#!/usr/bin/env python3
"""Prints the newest stable patch of the Go release go.mod names, such as
1.27.4 for `go 1.27.0`, as go.dev lists it. The workflows pass it to
setup-go (ADR 33).

setup-go resolves a range such as `1.27.x` from its own list of versions,
which trailed go.dev by most of a day when Go 1.27.4 came out, so v0.1.16
was built with 1.27.3. Given an exact version its list lacks, setup-go
downloads that version from go.dev instead.

    python3 scripts/go-newest.py
"""
import json
import os
import re
import sys
import urllib.request

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
RELEASES = "https://go.dev/dl/?mode=json"


def series(gomod):
    """Returns the release go.mod's `go` line names, "1.27" for `go 1.27.0`."""
    match = re.search(r"^go (\d+)\.(\d+)(?:\.\d+)?\s*$", gomod, re.MULTILINE)
    if not match:
        raise ValueError("go.mod has no go line")
    return f"{match.group(1)}.{match.group(2)}"


def newest(releases, release):
    """Returns the highest stable patch of release among go.dev's releases."""
    patches = []
    for entry in releases:
        match = re.fullmatch(r"go(\d+\.\d+)(?:\.(\d+))?", entry.get("version", ""))
        if entry.get("stable") and match and match.group(1) == release:
            patches.append(int(match.group(2) or 0))
    if not patches:
        raise ValueError(f"go.dev lists no stable Go {release}")
    return f"{release}.{max(patches)}"


def main():
    with open(os.path.join(ROOT, "go.mod"), encoding="utf-8") as f:
        release = series(f.read())
    with urllib.request.urlopen(RELEASES, timeout=30) as response:
        releases = json.load(response)
    print(newest(releases, release))


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError) as err:
        sys.exit(f"go-newest: {err}")
