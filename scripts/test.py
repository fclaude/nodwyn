#!/usr/bin/env python3
"""Run the complete race suite with small, independent test processes."""
import hashlib
import json
import math
import os
from pathlib import Path
import platform
import re
import signal
import subprocess
import sys
import tempfile
import time

ROOT = Path(__file__).resolve().parent.parent
HOT = {"internal/core": 16, "internal/prim": 8,
       "internal/frontend/rpc": 8, "cmd/nodwyn": 4, "scripts/validate": 4}
RESULT = re.compile(r"^--- (PASS|SKIP|FAIL): (\S+) \([\d.]+s\)", re.M)


def profile_name(go_environment=None):
    if go_environment is not None:
        return go_environment["GOOS"] + "-" + go_environment["GOARCH"]
    arch = {"x86_64": "amd64", "aarch64": "arm64"}.get(platform.machine().lower(), platform.machine().lower())
    return platform.system().lower() + "-" + arch


def load_weights(directory, profile):
    path = directory / (profile + ".json")
    if not path.exists():
        return {}
    data = json.loads(path.read_text())
    if data.get("schema") != 1:
        raise ValueError("unsupported timing-weight schema: " + str(path))
    packages = data["packages"]
    for weights in packages.values():
        for value in weights.values():
            if isinstance(value, bool) or not isinstance(value, (int, float)) or not math.isfinite(value) or value < 0:
                raise ValueError("invalid test duration in " + str(path))
    return packages


def partition(names, workers, weights):
    # Weights decide placement only. The binary's current inventory decides coverage.
    groups = [[] for _ in range(min(workers, len(names)))]
    costs = [0.0] * len(groups)
    for name in sorted(names, key=lambda n: (-weights.get(n, .1), n)):
        index = min(range(len(groups)), key=lambda i: costs[i])
        groups[index].append(name)
        costs[index] += weights.get(name, .1)
    return groups


def inventory(text):
    return sorted(set(line for line in text.splitlines()
                      if line.startswith(("Test", "Example", "Fuzz"))))


def json_stream(text):
    decoder = json.JSONDecoder()
    while text.strip():
        value, end = decoder.raw_decode(text.lstrip())
        yield value
        text = text.lstrip()[end:]


def check_worker(names, text):
    outcomes = RESULT.findall(text)
    seen = [name for _, name in outcomes]
    if set(seen) != set(names) or len(seen) != len(names):
        raise ValueError("test inventory mismatch; missing=" + repr(sorted(set(names) - set(seen))))
    if any(status == "FAIL" for status, _ in outcomes):
        raise ValueError("a test failed")
    return {status.lower(): sum(s == status for s, _ in outcomes)
            for status in ("PASS", "SKIP", "FAIL")}


def process_table():
    rows = {}
    text = subprocess.check_output(["ps", "-axo", "pid=,ppid=,pgid=,lstart=,command="], text=True)
    for line in text.splitlines():
        fields = line.split(None, 8)
        if len(fields) == 9:
            rows[int(fields[0])] = (int(fields[1]), int(fields[2]), " ".join(fields[3:8]), fields[8])
    return rows


class Supervisor:
    def __init__(self, directory, limit=900):
        self.directory = directory
        self.limit = limit
        self.started = time.monotonic()
        self.jobs = []
        self.owned = {}
        self.groups = {}

    def start(self, label, command, cwd, env, capture=False):
        log = self.directory / (label + ".log")
        output = self.directory / (label + ".stdout") if capture else log
        handles = [output.open("w")]
        if capture:
            handles.append(log.open("w"))
        try:
            process = subprocess.Popen(command, cwd=cwd, env=env, stdout=handles[0],
                                       stderr=handles[1] if capture else subprocess.STDOUT,
                                       start_new_session=True)
        except OSError:
            for handle in handles:
                handle.close()
            raise
        row = process_table().get(process.pid)
        self.groups[process.pid] = row[2:] if row else None
        job = {"label": label, "process": process, "handles": handles,
               "log": log, "output": output, "command": command}
        self.jobs.append(job)
        self.sample()
        self.receipt()
        return job

    def receipt(self):
        data = [{"label": j["label"], "pid": j["process"].pid,
                 "command": j["command"], "identity": self.groups.get(j["process"].pid), "exit": j["process"].poll()} for j in self.jobs]
        (self.directory / "processes.json").write_text(json.dumps(data, indent=2) + "\n")

    def sample(self):
        rows = process_table()
        # A live, unreaped Popen child cannot have had its PID re-used. Keep
        # ownership across exec, including Go's automatic toolchain switch.
        for job in self.jobs:
            process = job["process"]
            if process.poll() is None and process.pid in rows:
                self.groups[process.pid] = rows[process.pid][2:]
                self.owned[process.pid] = rows[process.pid]
        # A re-used PID is not ours. Group IDs remain valid only while their
        # original leader has not been replaced by another process.
        groups = {pid for pid, identity in self.groups.items()
                  if pid not in rows or (identity is not None and rows[pid][2:] == identity)}
        family = {pid for pid, row in self.owned.items()
                  if pid in rows and rows[pid][2:] == row[2:]}
        family |= {pid for pid in groups if pid in rows}
        for _ in range(32):
            found = {pid for pid, row in rows.items() if row[0] in family or row[1] in groups}
            if found <= family:
                break
            family |= found
        for pid in family:
            if pid in rows:
                self.owned[pid] = rows[pid]
        return rows

    def wait(self, job):
        process = job["process"]
        while process.poll() is None:
            self.sample()
            if time.monotonic() - self.started > self.limit:
                raise TimeoutError("test runner exceeded its 15-minute deadline")
            time.sleep(.1)
        self.sample()
        for handle in job["handles"]:
            handle.close()
        self.receipt()
        if process.returncode:
            raise RuntimeError(job["label"] + " failed; " + str(job["log"]))
        return job["output"].read_text(errors="replace")

    def close(self):
        self.sample()
        remaining = []
        for sig in (signal.SIGTERM, signal.SIGKILL):
            rows = process_table()
            remaining = [pid for pid, original in self.owned.items()
                         if pid in rows and rows[pid][2:] == original[2:]]
            for pid in remaining:
                try:
                    os.kill(pid, sig)
                except ProcessLookupError:
                    pass
            if remaining:
                time.sleep(.2)
            for job in self.jobs:
                job["process"].poll()
        for job in self.jobs:
            job["process"].wait()
            for handle in job["handles"]:
                handle.close()
        rows = process_table()
        remaining = [pid for pid, original in self.owned.items()
                     if pid in rows and rows[pid][2:] == original[2:]]
        (self.directory / "cleanup.json").write_text(json.dumps({"remaining": remaining}) + "\n")
        self.receipt()
        if remaining:
            raise RuntimeError("test processes remain: " + repr(remaining))


def run_suite(root, supervisor, env, profile, cpu_count, weights, race=False):
    race_flags = ["-race"] if race else []
    listing = supervisor.start("packages", ["go", "list", "-json", "./..."], root, env, capture=True)
    packages = list(json_stream(supervisor.wait(listing)))
    if not packages:
        raise ValueError("Go returned no packages for the full test suite")
    relative = {os.path.relpath(Path(p["Dir"]).resolve(), root.resolve()).replace(os.sep, "/"): p for p in packages}
    hot = {path: min(count, cpu_count) for path, count in HOT.items()
           if path in relative and (relative[path].get("TestGoFiles") or relative[path].get("XTestGoFiles"))}
    others = [p["ImportPath"] for p in packages if p not in [relative[path] for path in hot]]
    jobs = []
    if others:
        jobs.append((supervisor.start("other-packages", ["go", "test", *race_flags, "-count=1", "-timeout=5m", "-json", *others], root, env), None))
    binaries = supervisor.directory / "bin"
    binaries.mkdir()
    if hot:
        build = supervisor.start("compile", ["go", "test", *race_flags, "-c", "-o", str(binaries) + os.sep,
                                               *["./" + path for path in hot]], root, env)
        supervisor.wait(build)
    hot_limit = max(2, min(32, cpu_count))
    manifest = {"hot_process_limit": hot_limit, "profile": profile, "race": race, "logical_cpus": cpu_count, "packages": {}}
    for path, count in hot.items():
        binary = binaries / (path.rsplit("/", 1)[-1] + ".test")
        cwd = relative[path]["Dir"]
        label = path.replace("/", "-")
        listed = supervisor.start(label + "-inventory", [str(binary), "-test.list", "."], cwd, env)
        names = inventory(supervisor.wait(listed))
        groups = partition(names, count, weights.get(path, {})) or [[]]
        manifest["packages"][path] = {"tests": names, "groups": groups,
            "sha256": hashlib.sha256("\n".join(names).encode()).hexdigest()}
        for i, group in enumerate(groups):
            child_env = dict(env, GOMAXPROCS=str(max(2, cpu_count // count)))
            pattern = "^(" + "|".join(re.escape(name) for name in group) + ")$"
            command = [str(binary), "-test.count=1", "-test.timeout=5m", "-test.v", "-test.run=" + pattern]
            # Small heaps help, but starting every heap at once can saturate
            # the host and overrun application deadlines in otherwise small tests.
            while sum(j["process"].poll() is None for j, assigned in jobs if assigned is not None) >= hot_limit:
                supervisor.sample()
                if time.monotonic() - supervisor.started > supervisor.limit:
                    raise TimeoutError("test runner exceeded its 15-minute deadline")
                time.sleep(.1)
            jobs.append((supervisor.start(label + "-" + str(i), command, cwd, child_env), group))
    (supervisor.directory / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    results = {}
    for job, names in jobs:
        text = supervisor.wait(job)
        if names is not None:
            results[job["label"]] = check_worker(names, text)
        else:
            records = [json.loads(line) for line in text.splitlines() if line.startswith("{")]
            completed = {r["Package"] for r in records if r.get("Action") in ("pass", "skip") and "Test" not in r}
            if completed != set(others) or any(r.get("Action") == "fail" for r in records):
                raise ValueError("other-package inventory mismatch or failure")
            results[job["label"]] = {"packages": len(completed)}
    (supervisor.directory / "results.json").write_text(json.dumps(results, indent=2) + "\n")
    return sum(len(p["tests"]) for p in manifest["packages"].values()), len(packages)


def main(arguments):
    race = arguments[:1] == ["--race"]
    if race:
        arguments = arguments[1:]
    # CI already requests one uncached pass; that is also the sharded default.
    if arguments == ["-count=1"]:
        arguments = []
    # Explicit flags and runtime budgets retain normal Go semantics, including filters and coverage.
    explicit = arguments or any(os.environ.get(key) for key in ("GOFLAGS", "GOMAXPROCS", "GOOS", "GOARCH"))
    if explicit or platform.system() not in ("Linux", "Darwin") or os.environ.get("NODWYN_TEST_MODE") == "unsharded":
        print("make test: standard Go runner (explicit options or unsupported host)", flush=True)
        return subprocess.call(["go", "test", *(["-race"] if race else []), *arguments, "./..."], cwd=ROOT)
    directory = Path(tempfile.mkdtemp(prefix="nodwyn-tests-"))
    cpus = len(os.sched_getaffinity(0)) if hasattr(os, "sched_getaffinity") else (os.cpu_count() or 1)
    env = dict(os.environ)
    env.setdefault("GOMEMLIMIT", "off")
    supervisor = Supervisor(directory)
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(143))
    try:
        detected = supervisor.start("go-environment", ["go", "env", "-json", "GOOS", "GOARCH"], ROOT, env, capture=True)
        profile = profile_name(json.loads(supervisor.wait(detected)))
        weights = load_weights(Path(__file__).with_name("test-weights"), profile)
        print("make test: sharded " + ("race" if race else "non-race") + " suite; " + profile + "; logs: " + str(directory), flush=True)
        tests, packages = run_suite(ROOT, supervisor, env, profile, cpus, weights, race)
        print("PASS make test: " + str(tests) + " sharded tests; all " + str(packages) + " packages; "
              + format(time.monotonic() - supervisor.started, ".2f") + "s", flush=True)
        return 0
    except (OSError, ValueError, RuntimeError, TimeoutError) as error:
        print("FAIL make test: " + str(error) + "; logs: " + str(directory), file=sys.stderr)
        return 1
    finally:
        supervisor.close()


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
