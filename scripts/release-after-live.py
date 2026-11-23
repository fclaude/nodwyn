#!/usr/bin/env python3
"""Start a GitHub release of the commit the weekly live run tested (ADR 63).

The weekly Gitea workflow runs this after the extended live suite passed. It
releases only what was tested: GitHub's main must be the tested commit, and
it must have commits since the latest release. The version is the latest
release's with its patch number raised by one; release.yml (ADR 42) does the
rest, and this waits for that run and fails if it fails.

Environment: GITHUB_TOKEN, a token that may start workflows on the
repository (its value is sent only in an Authorization header and never
printed); TESTED_SHA, the commit the live suite ran; REPOSITORY, owner/name
on GitHub (default fclaude/nodwyn); DRY_RUN=1 to say what it would do and
start nothing.
"""

import calendar
import json
import os
import re
import sys
import time
import urllib.error
import urllib.request

API = "https://api.github.com"


def call(method, path, token, body=None):
    request = urllib.request.Request(API + path, method=method, data=None if body is None else json.dumps(body).encode())
    request.add_header("Accept", "application/vnd.github+json")
    request.add_header("X-GitHub-Api-Version", "2022-11-28")
    if token:
        request.add_header("Authorization", "Bearer " + token)
    if body is not None:
        request.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(request, timeout=60) as response:
            raw = response.read()
            return response.status, json.loads(raw) if raw else None
    except urllib.error.HTTPError as error:
        return error.code, None


def next_version(tag):
    match = re.fullmatch(r"v(\d+)\.(\d+)\.(\d+)", tag)
    if not match:
        raise SystemExit(f"release-after-live: the latest release is {tag!r}, not vMAJOR.MINOR.PATCH")
    major, minor, patch = map(int, match.groups())
    return f"v{major}.{minor}.{patch + 1}"


def main():
    token = os.environ.get("GITHUB_TOKEN", "")
    tested = os.environ.get("TESTED_SHA", "")
    repository = os.environ.get("REPOSITORY", "fclaude/nodwyn")
    dry = os.environ.get("DRY_RUN") == "1"
    if not tested:
        raise SystemExit("release-after-live: TESTED_SHA is not set")
    if not token and not dry:
        raise SystemExit("release-after-live: GITHUB_TOKEN is not set (the NODWYN_RELEASE_GITHUB_TOKEN secret)")

    status, head = call("GET", f"/repos/{repository}/commits/main", token)
    if status != 200 or not head:
        raise SystemExit(f"release-after-live: reading GitHub's main answered {status}")
    if head["sha"] != tested:
        print(f"No release: GitHub's main is {head['sha'][:12]}, not the tested {tested[:12]}; the next weekly run tests it.")
        return 0

    status, latest = call("GET", f"/repos/{repository}/releases/latest", token)
    if status != 200 or not latest:
        raise SystemExit(f"release-after-live: reading the latest release answered {status}")
    tag = latest["tag_name"]
    status, compare = call("GET", f"/repos/{repository}/compare/{tag}...{tested}", token)
    if status != 200 or not compare:
        raise SystemExit(f"release-after-live: comparing {tag} with main answered {status}")
    if compare.get("ahead_by", 0) == 0:
        print(f"No release: main has nothing since {tag}.")
        return 0
    version = next_version(tag)
    print(f"Releasing {version}: {compare['ahead_by']} commits since {tag}, at {tested[:12]}.")
    if dry:
        print("DRY_RUN: release.yml was not started.")
        return 0

    started = time.time()
    status, _ = call("POST", f"/repos/{repository}/actions/workflows/release.yml/dispatches", token,
                     {"ref": "main", "inputs": {"version": version}})
    if status != 204:
        raise SystemExit(f"release-after-live: starting release.yml answered {status}")

    # Follow the run it started: the newest release.yml run created since.
    run = None
    for _ in range(90):  # up to an hour and a half
        time.sleep(60)
        status, runs = call("GET", f"/repos/{repository}/actions/workflows/release.yml/runs?event=workflow_dispatch&per_page=5", token)
        if status != 200 or not runs:
            continue
        for candidate in runs.get("workflow_runs", []):
            created = calendar.timegm(time.strptime(candidate["created_at"], "%Y-%m-%dT%H:%M:%SZ"))
            if created >= started - 60:
                run = candidate
                break
        if run and run.get("status") == "completed":
            break
        if run:
            _, run = call("GET", f"/repos/{repository}/actions/runs/{run['id']}", token)
            if run and run.get("status") == "completed":
                break
    if not run or run.get("status") != "completed":
        raise SystemExit(f"release-after-live: release.yml for {version} did not finish in time; see GitHub Actions")
    print(f"release.yml for {version}: {run['conclusion']} ({run['html_url']})")
    if run["conclusion"] != "success":
        return 1
    print(f"Released {version}: https://github.com/{repository}/releases/tag/{version}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
