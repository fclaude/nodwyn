import importlib.util
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch
from types import SimpleNamespace

SOURCE = Path(__file__).resolve().parents[2] / "scripts" / "test.py"
spec = importlib.util.spec_from_file_location("nodwyn_test_runner", SOURCE)
runner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runner)

BINARY = r'''
import json, os, re, sys, time
from pathlib import Path
package = Path(sys.argv[0]).stem
names = ["TestHeavy", "TestNew", "TestSkipped", "ExampleDemo", "FuzzCorpus"] if package == "core" else ["TestPrim"]
if os.environ.get("FAKE_MODE") == "empty-tests" and package == "core": names = []
if "-test.list" in sys.argv:
    print("\n".join(names)); raise SystemExit(0)
with open(os.environ["FAKE_EVENTS"], "a") as f: f.write(json.dumps({"kind":"worker-start", "pid":os.getpid()})+"\n")
if os.environ.get("FAKE_MODE") == "slow-workers": time.sleep(.4)
pattern = next(a.split("=", 1)[1] for a in sys.argv if a.startswith("-test.run="))
for name in names:
    if not re.match(pattern, name): continue
    with open(os.environ["FAKE_EVENTS"], "a") as f: f.write(json.dumps({"kind":"test", "name":name, "pid":os.getpid()})+"\n")
    if os.environ.get("FAKE_MODE") == "hang": time.sleep(60)
    if os.environ.get("FAKE_MODE") == "missing-test" and name == "TestNew": continue
    status = "SKIP" if name == "TestSkipped" else "PASS"
    if os.environ.get("FAKE_MODE") == "fail-test" and name == "TestHeavy": status = "FAIL"
    print("--- " + status + ": " + name + " (0.01s)", flush=True)
    if status == "FAIL": raise SystemExit(1)
with open(os.environ["FAKE_EVENTS"], "a") as f: f.write(json.dumps({"kind":"worker-end", "pid":os.getpid()})+"\n")
'''

FAKE_GO = r'''
import json, os, sys
from pathlib import Path
args = sys.argv[1:]; root = Path(os.environ["FAKE_ROOT"])
with open(os.environ["FAKE_EVENTS"], "a") as f: f.write(json.dumps({"kind":"go", "args":args})+"\n")
if args[:2] == ["env", "-json"]:
    import platform
    machine = platform.machine().lower()
    arch = {"x86_64":"amd64", "aarch64":"arm64"}.get(machine, machine)
    print(json.dumps({"GOOS":platform.system().lower(), "GOARCH":arch}))
elif args[:2] == ["list", "-json"]:
    print("go: downloading a fixture", file=sys.stderr)
    for p in ([] if os.environ.get("FAKE_MODE") == "no-packages" else ["internal/core", "internal/prim", "internal/extra"]):
        print(json.dumps({"ImportPath":"github.com/fclaude/nodwyn/"+p,"Dir":str(root/p),"TestGoFiles":["fixture_test.go"]}))
elif "-c" in args:
    out = Path(args[args.index("-o")+1])
    for p in args:
        if not p.startswith("./"): continue
        binary = out/(p.rsplit("/",1)[-1]+".test")
        binary.write_text("#!"+sys.executable+"\n"+BINARY)
        binary.chmod(0o755)
else:
    if os.environ.get("FAKE_MODE") != "missing-package":
        print(json.dumps({"Action":"pass","Package":"github.com/fclaude/nodwyn/internal/extra","Test":"TestExtra"}))
        print(json.dumps({"Action":"pass","Package":"github.com/fclaude/nodwyn/internal/extra"}))
'''


@unittest.skipUnless(os.name == "posix", "the optimised runner is for Linux/macOS/WSL")
class RunnerTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="test-nodwyn-runner-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.repo = self.root / "repo"
        for path in ["scripts", "internal/core", "internal/prim", "internal/extra"]:
            (self.repo/path).mkdir(parents=True)
        (self.repo/"scripts/test.py").write_text(SOURCE.read_text())
        weights = self.repo/"scripts/test-weights"
        weights.mkdir()
        (weights/(runner.profile_name()+".json")).write_text(json.dumps({"schema":1,"packages":{"internal/core":{"TestHeavy":9,"TestRemoved":100}}}))
        bindir = self.root/"bin"; bindir.mkdir()
        go = bindir/"go"
        go.write_text("#!"+sys.executable+"\nBINARY="+repr(BINARY)+"\n"+FAKE_GO)
        go.chmod(0o755)
        self.events = self.root/"events.jsonl"
        self.env = dict(os.environ, PATH=str(bindir)+os.pathsep+os.environ["PATH"],
                        FAKE_ROOT=str(self.repo), FAKE_EVENTS=str(self.events), TMPDIR=str(self.root),
                        NODWYN_TEST_RAM="off")
        for key in ["GOFLAGS", "GOMAXPROCS", "GOOS", "GOARCH", "NODWYN_TEST_MODE"]:
            self.env.pop(key, None)

    def run_command(self, *args, mode=""):
        env = dict(self.env, FAKE_MODE=mode)
        p = subprocess.Popen([sys.executable, str(self.repo/"scripts/test.py"), *args],
                             stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True,
                             env=env, start_new_session=True)
        try:
            text, _ = p.communicate(timeout=12)
            return p.returncode, text
        finally:
            if p.poll() is None:
                os.killpg(p.pid, signal.SIGTERM)
                p.wait(timeout=5)
            self.clean_fixture_children()

    def clean_fixture_children(self):
        for pid, row in runner.process_table().items():
            if str(self.root) in row[3] and pid != os.getpid():
                current = runner.process_table().get(pid)
                if current and current[2:] == row[2:]:
                    try:
                        os.kill(pid, signal.SIGKILL)
                    except ProcessLookupError:
                        pass

    def recorded(self):
        return [json.loads(x) for x in self.events.read_text().splitlines()]

    def test_all_current_tests_run_once_including_new_tests_and_fuzz_seeds(self):
        code, text = self.run_command()
        self.assertEqual(code, 0, text)
        cases = [r["name"] for r in self.recorded() if r["kind"] == "test"]
        self.assertCountEqual(cases, ["TestHeavy", "TestNew", "TestSkipped", "ExampleDemo", "FuzzCorpus", "TestPrim"])
        self.assertEqual(len(cases), len(set(cases)))
        commands = [r["args"] for r in self.recorded() if r["kind"] == "go"]
        self.assertFalse(any("-race" in command for command in commands))
        self.assertIn("all 3 packages", text)

    def test_package_directory_symlinks_do_not_lose_hot_packages(self):
        alias = self.root/"repo-alias"
        alias.symlink_to(self.repo, target_is_directory=True)
        self.env["FAKE_ROOT"] = str(alias)
        code, text = self.run_command()
        self.assertEqual(code, 0, text)
        cases = [r["name"] for r in self.recorded() if r["kind"] == "test"]
        self.assertIn("TestNew", cases)
        self.assertIn("FuzzCorpus", cases)

    def test_race_target_instruments_both_compilation_and_other_packages(self):
        code, text = self.run_command("--race", "-count=1")
        self.assertEqual(code, 0, text)
        commands = [r["args"] for r in self.recorded() if r["kind"] == "go" and r["args"][0] == "test"]
        self.assertEqual(len(commands), 2)
        self.assertTrue(all("-race" in command for command in commands))

    def test_explicit_test_filter_preserves_go_semantics(self):
        code, text = self.run_command("--race", "-run", "TestExtra", "-count=2")
        self.assertEqual(code, 0, text)
        commands = [r["args"] for r in self.recorded() if r["kind"] == "go"]
        self.assertEqual(commands, [["test", "-race", "-run", "TestExtra", "-count=2", "./..."]])

    def test_filter_retains_an_explicit_uncached_pass(self):
        code, text = self.run_command("--race", "-run", "TestExtra", "-count=1")
        self.assertEqual(code, 0, text)
        commands = [r["args"] for r in self.recorded() if r["kind"] == "go"]
        self.assertEqual(commands, [["test", "-race", "-run", "TestExtra", "-count=1", "./..."]])

    def test_empty_package_inventory_cannot_pass(self):
        code, text = self.run_command(mode="no-packages")
        self.assertNotEqual(code, 0, text)
        self.assertIn("no packages", text)

    def test_package_without_named_tests_still_executes(self):
        code, text = self.run_command(mode="empty-tests")
        self.assertEqual(code, 0, text)
        started = [r for r in self.recorded() if r["kind"] == "worker-start"]
        self.assertEqual(len(started), 2)
        self.assertIn("all 3 packages", text)

    def test_successful_worker_that_omits_a_test_is_rejected(self):
        code, text = self.run_command(mode="missing-test")
        self.assertNotEqual(code, 0, text)
        self.assertIn("inventory mismatch", text)

    def test_successful_go_command_that_omits_a_package_is_rejected(self):
        code, text = self.run_command(mode="missing-package")
        self.assertNotEqual(code, 0, text)
        self.assertIn("other-package inventory mismatch", text)

    def test_test_failure_propagates(self):
        code, text = self.run_command(mode="fail-test")
        self.assertNotEqual(code, 0, text)
        self.assertIn("failed", text)

    def test_sigterm_cleans_up_the_started_workers(self):
        log = self.root/"cancel.log"
        with log.open("w") as handle:
            p = subprocess.Popen([sys.executable, str(self.repo/"scripts/test.py")],
                                 stdout=handle, stderr=subprocess.STDOUT, env=dict(self.env, FAKE_MODE="hang"),
                                 start_new_session=True)
            try:
                deadline = time.monotonic()+8
                while time.monotonic() < deadline:
                    if self.events.exists() and any(x.get("kind")=="test" for x in self.recorded()):
                        break
                    time.sleep(.05)
                else:
                    self.fail("fixture workers never started")
                p.terminate()
                self.assertEqual(p.wait(timeout=5), 143)
                receipts = list(self.root.glob("nodwyn-tests-*/cleanup.json"))
                self.assertEqual(len(receipts), 1)
                self.assertEqual(json.loads(receipts[0].read_text())["remaining"], [])
            finally:
                if p.poll() is None:
                    os.killpg(p.pid, signal.SIGKILL)
                    p.wait(timeout=5)
                self.clean_fixture_children()

    def test_full_suite_closes_workspace_before_success_and_after_failure(self):
        for mode in ("", "fail-test"):
            with self.subTest(mode=mode):
                label = "failed" if mode else "passed"
                logs = self.root / (label + "-logs")
                scratch = self.root / (label + "-scratch")
                logs.mkdir()
                scratch.mkdir()
                events = []
                def activate(env):
                    events.append("activated")
                    env["TMPDIR"] = str(scratch)
                    return scratch
                workspace = SimpleNamespace(activate=activate, close=lambda: events.append("closed"))
                previous = {sig: signal.getsignal(sig) for sig in (signal.SIGTERM, signal.SIGHUP)}
                try:
                    with patch.object(runner, "ROOT", self.repo), \
                            patch.object(runner, "MacRamWorkspace", return_value=workspace, create=True), \
                            patch.object(runner.tempfile, "mkdtemp", return_value=str(logs)), \
                            patch.dict(os.environ, dict(self.env, FAKE_MODE=mode), clear=True), \
                            patch.object(runner, "print", side_effect=lambda message, **_: events.append(message), create=True):
                        code = runner.main([])
                finally:
                    for sig, handler in previous.items():
                        signal.signal(sig, handler)
                    self.clean_fixture_children()
                self.assertEqual(code, 1 if mode else 0)
                self.assertEqual(events.count("activated"), 1)
                self.assertEqual(events.count("closed"), 1)
                success = [i for i, event in enumerate(events) if event.startswith("PASS make test:")]
                if mode:
                    self.assertEqual(success, [])
                else:
                    self.assertGreater(success[0], events.index("closed"))
                self.assertEqual(json.loads((logs / "cleanup.json").read_text())["remaining"], [])

    def test_platform_selects_the_macos_weights(self):
        with patch.object(runner.platform, "system", return_value="Darwin"), patch.object(runner.platform, "machine", return_value="arm64"):
            profile = runner.profile_name()
        self.assertEqual(profile, "darwin-arm64")
        # The Go binary's target wins when Python runs under Rosetta.
        with patch.object(runner.platform, "machine", return_value="x86_64"):
            self.assertEqual(runner.profile_name({"GOOS":"darwin", "GOARCH":"arm64"}), "darwin-arm64")
        weights = runner.load_weights(SOURCE.with_name("test-weights"), profile)
        self.assertGreater(len(weights["internal/core"]), 500)
        groups = runner.partition(["TestNew", "TestHeavy", "TestFast"], 16, {"TestHeavy":10,"TestRemoved":1000})
        self.assertCountEqual([n for group in groups for n in group], ["TestNew", "TestHeavy", "TestFast"])

    def test_small_hosts_bound_concurrent_hot_processes(self):
        directory = self.root/"limited"
        directory.mkdir()
        supervisor = runner.Supervisor(directory)
        try:
            runner.run_suite(self.repo, supervisor, dict(self.env, FAKE_MODE="slow-workers"),
                             runner.profile_name(), 2, {})
        finally:
            supervisor.close()
        live = set()
        peak = 0
        for event in self.recorded():
            if event["kind"] == "worker-start":
                live.add(event["pid"])
                peak = max(peak, len(live))
            elif event["kind"] == "worker-end":
                live.remove(event["pid"])
        self.assertEqual(live, set())
        self.assertLessEqual(peak, 2)

    def test_cleanup_never_adopts_a_reused_process_id(self):
        for identity in [("old start", "old command"), None]:
            with self.subTest(identity=identity):
                supervisor = runner.Supervisor(self.root)
                supervisor.owned[123] = (1, 123, "old start", "old command")
                supervisor.groups[123] = identity
                changed = {123: (1, 123, "new start", "unrelated command"),
                           124: (123, 123, "new child", "unrelated child")}
                with patch.object(runner, "process_table", return_value=changed), patch.object(runner.os, "kill") as kill:
                    supervisor.close()
                kill.assert_not_called()
                self.assertNotIn(124, supervisor.owned)

    def test_live_child_keeps_ownership_across_exec(self):
        supervisor = runner.Supervisor(self.root)
        supervisor.groups[123] = ("start", "go launcher")
        supervisor.jobs = [{"process": SimpleNamespace(pid=123, poll=lambda: None)}]
        changed = {123: (1, 123, "start", "downloaded go toolchain"),
                   124: (123, 123, "child start", "test child")}
        with patch.object(runner, "process_table", return_value=changed):
            supervisor.sample()
        self.assertIn(124, supervisor.owned)
        self.assertEqual(supervisor.groups[123], changed[123][2:])

    def test_duplicate_and_failed_outcomes_are_rejected(self):
        with self.assertRaises(ValueError):
            runner.check_worker(["TestOne"], "--- PASS: TestOne (0.1s)\n--- PASS: TestOne (0.1s)\n")
        with self.assertRaises(ValueError):
            runner.check_worker(["TestOne"], "--- FAIL: TestOne (0.1s)\n")


if __name__ == "__main__":
    unittest.main()
