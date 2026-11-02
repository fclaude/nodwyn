#!/usr/bin/env python3
"""Run the complete test suite with small, independent test processes."""
import contextlib
import hashlib
import json
import math
import os
from pathlib import Path
import platform
import plistlib
import re
import signal
import subprocess
import sys
import tempfile
import time
import uuid

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


class MacRamWorkspace:
    """Own one disposable RAM image; retain diagnostics outside its lifetime."""
    size = 2 * 1024**3

    def __init__(self, directory):
        self.directory = directory
        if directory.stat().st_uid != os.getuid():
            raise ValueError("workspace diagnostics directory must belong to this user")
        directory.chmod(0o700)
        self.label = "nodwyn-" + uuid.uuid4().hex[:16]
        self.device = None
        self.helper = None
        self.helper_identity = None
        self.mount = None
        self.holder = None
        self.commands = []
        self.state = {"mode": "disk", "size_bytes": self.size}
        self.receipt()

    def receipt(self):
        self.state.update(device=self.device, helper_pid=self.helper, helper_identity=self.helper_identity,
                          commands=self.commands)
        (self.directory / "workspace.json").write_text(json.dumps(self.state, indent=2) + "\n")

    @contextlib.contextmanager
    def deferred_signals(self):
        # Finish the bounded image operation and register ownership before an
        # interruption unwinds into cleanup. SIGKILL cannot run any cleanup.
        pending = []
        previous = {}
        for sig in (signal.SIGINT, signal.SIGTERM, signal.SIGHUP):
            previous[sig] = signal.signal(sig, lambda number, _: pending.append(number))
        try:
            yield
        finally:
            for sig, handler in previous.items():
                signal.signal(sig, handler)
            if pending:
                raise SystemExit(128 + pending[0])

    def tool(self, command):
        with (self.directory / "workspace-tools.log").open("ab") as errors:
            process = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=errors,
                                       start_new_session=True)
            record = {"command": command, "pid": process.pid, "exit": None}
            self.commands.append(record)
            self.receipt()
            try:
                output, _ = process.communicate(timeout=30)
            except subprocess.TimeoutExpired as error:
                # The unreaped child still owns its PID and process group.
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    record["signal"] = "process group already exited"
                output, _ = process.communicate()
                record.update(exit=process.returncode, timeout=True)
                self.receipt()
                raise subprocess.TimeoutExpired(command, 30, output=output) from error
            record["exit"] = process.returncode
            self.receipt()
            if process.returncode:
                raise subprocess.CalledProcessError(process.returncode, command, output)
            return output

    def images(self):
        return plistlib.loads(self.tool(["/usr/bin/hdiutil", "info", "-plist"]))["images"]

    def owned_image(self):
        for image in self.images():
            entities = image.get("system-entities", [])
            if (image.get("image-path") == "ram://" + str(self.size // 512)
                    and image.get("owner-uid") == os.getuid()
                    and any(entity.get("dev-entry") == self.device for entity in entities)
                    and (self.helper is None or image.get("hdid-pid") == self.helper)):
                return image
        return None

    def activate(self, env):
        mode = env.get("NODWYN_TEST_RAM", "auto")
        if mode not in ("auto", "off"):
            raise ValueError("NODWYN_TEST_RAM must be auto or off")
        if platform.system() != "Darwin" or mode == "off":
            self.state["reason"] = "disabled" if mode == "off" else "not macOS"
            self.receipt()
            return None
        with self.deferred_signals():
            try:
                stats = self.tool(["/usr/bin/vm_stat"]).decode()
                page_size = re.search(r"page size of (\d+) bytes", stats)
                counts = [re.search(r"^Pages " + kind + r":\s+(\d+)", stats, re.M)
                          for kind in ("free", "inactive", "speculative")]
                if not page_size or not all(counts):
                    raise ValueError("unrecognised vm_stat output")
                pages = sum(int(count[1]) for count in counts)
                page_size = int(page_size[1])
                if pages * page_size < self.size + 4 * 1024**3:
                    raise ValueError("less than 6 GiB of free/reclaimable memory")
                command = ["/usr/bin/hdiutil", "attach", "-nomount", "ram://" + str(self.size // 512)]
                try:
                    output = self.tool(command)
                except (subprocess.CalledProcessError, subprocess.TimeoutExpired) as error:
                    output = error.output or b""
                    self.remember_device(output)
                    if self.device is None:
                        raise RuntimeError("RAM attachment failed without a device receipt; inspect workspace-tools.log and hdiutil info") from error
                    raise
                self.remember_device(output)
                if self.device is None:
                    raise RuntimeError("RAM attachment returned no device receipt; inspect hdiutil info")
                image = self.owned_image()
                info = plistlib.loads(self.tool(["/usr/sbin/diskutil", "info", "-plist", self.device]))
                if not image or not (info.get("WholeDisk") is True
                        and info.get("VirtualOrPhysical") == "Virtual"
                        and info.get("BusProtocol") == "Disk Image"
                        and info.get("TotalSize") == self.size
                        and info.get("DeviceNode") == self.device):
                    raise RuntimeError("refusing to initialise an unverified RAM device: " + self.device)
                self.helper = image["hdid-pid"]
                row = process_table().get(self.helper)
                self.helper_identity = row[2:] if row else None
                self.receipt()
                self.tool(["/usr/sbin/diskutil", "eraseVolume", "HFS+", self.label, self.device])
                self.tool(["/usr/sbin/diskutil", "unmount", self.device])
                # HFS images may ignore ownership. Mount below a private host
                # directory so access is enforced before entering that volume.
                self.holder = Path(tempfile.mkdtemp(prefix="nw-", dir="/private/tmp")).resolve()
                self.state["host_directory"] = str(self.holder)
                self.receipt()
                mount = self.holder / "ram"
                mount.mkdir(mode=0o700)
                self.mount = mount
                self.tool(["/usr/sbin/diskutil", "mount", "-mountPoint", str(mount), self.device])
                info = plistlib.loads(self.tool(["/usr/sbin/diskutil", "info", "-plist", self.device]))
                if (info.get("VolumeName") != self.label or not info.get("MountPoint")
                        or Path(info["MountPoint"]).resolve() != mount
                        or not os.path.ismount(mount)):
                    raise ValueError("new RAM volume did not mount at its private path")
                mount.chmod(0o700)
                scratch = mount / "workspace"
                scratch.mkdir(mode=0o700)
                for name in ("tmp", "go"):
                    (scratch / name).mkdir(mode=0o700)
                env.update(TMPDIR=str(scratch / "tmp"), GOTMPDIR=str(scratch / "go"))
                self.state.update(mode="ram", mount=str(mount), active=True)
                self.receipt()
                print("make test: private 2 GiB RAM workspace; diagnostics: " + str(self.directory), flush=True)
                return scratch
            except (OSError, ValueError, subprocess.SubprocessError) as error:
                self.state["reason"] = str(error)
                self.close()
                print("make test: RAM workspace unavailable; using disk (" + str(error) + ")", flush=True)
                return None

    def remember_device(self, output):
        devices = re.findall(rb"(?m)^(/dev/disk\d+)\s*$", output)
        if len(devices) == 1:
            self.device = devices[0].decode()
            self.state["attached_device"] = self.device
            self.receipt()

    def close(self):
        with self.deferred_signals():
            if self.device:
                image = self.owned_image()
                if image:
                    # Recheck the image identity; a reused /dev/diskN is not ours.
                    for attempt in range(5):
                        if attempt and not self.owned_image():
                            break
                        try:
                            self.tool(["/usr/bin/hdiutil", "detach", self.device])
                            break
                        except subprocess.SubprocessError as error:
                            if (isinstance(error, subprocess.CalledProcessError)
                                    and error.returncode == 16 and attempt < 4):
                                time.sleep(.2)
                                continue
                            self.state["cleanup_error"] = str(error)
                            self.receipt()
                            raise RuntimeError("RAM cleanup failed for " + self.device + "; inspect " + str(self.directory / "workspace.json")) from error
                    if self.owned_image():
                        raise RuntimeError("RAM image remains attached: " + self.device)
                    if self.helper_identity:
                        for _ in range(30):
                            row = process_table().get(self.helper)
                            if not row or row[2:] != self.helper_identity:
                                break
                            time.sleep(.1)
                        else:
                            self.state["cleanup_error"] = "image helper still running: " + str(self.helper)
                            self.receipt()
                            raise RuntimeError(self.state["cleanup_error"])
                self.device = None
            if self.mount and self.mount.exists():
                if os.path.ismount(self.mount):
                    raise RuntimeError("workspace mount is still in use: " + str(self.mount))
                self.mount.rmdir()
            if self.holder and self.holder.exists():
                self.holder.rmdir()
            self.state.update(active=False, cleaned=True)
            self.receipt()


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
        self.scratch = directory
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
    binaries = supervisor.scratch / "bin"
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
    workspace = MacRamWorkspace(directory)
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(143))
    signal.signal(signal.SIGHUP, lambda *_: sys.exit(129))
    code = 1
    try:
        detected = supervisor.start("go-environment", ["go", "env", "-json", "GOOS", "GOARCH"], ROOT, env, capture=True)
        profile = profile_name(json.loads(supervisor.wait(detected)))
        weights = load_weights(Path(__file__).with_name("test-weights"), profile)
        supervisor.scratch = workspace.activate(env) or directory
        print("make test: sharded " + ("race" if race else "non-race") + " suite; " + profile + "; logs: " + str(directory), flush=True)
        tests, packages = run_suite(ROOT, supervisor, env, profile, cpus, weights, race)
        code = 0
    except (OSError, ValueError, RuntimeError, TimeoutError) as error:
        print("FAIL make test: " + str(error) + "; logs: " + str(directory), file=sys.stderr)
    finally:
        try:
            supervisor.close()
        finally:
            workspace.close()
    if code == 0:
        print("PASS make test: " + str(tests) + " sharded tests; all " + str(packages) + " packages; "
              + format(time.monotonic() - supervisor.started, ".2f") + "s", flush=True)
    return code


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
