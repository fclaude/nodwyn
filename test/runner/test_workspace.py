import json
import os
from pathlib import Path
import plistlib
import signal
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from test_runner import runner


class WorkspaceTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="test-nodwyn-workspace-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.logs = self.root / "logs"
        self.logs.mkdir()
        self.holder = self.root / "short-private-parent"
        self.mount = self.holder / "ram"
        self.mounted = False
        self.workspace = runner.MacRamWorkspace(self.logs)
        self.calls = []
        self.attached = False
        self.helper = 12345
        self.uid = os.getuid()
        self.image_path = "ram://4194304"
        self.info = {"WholeDisk": True, "VirtualOrPhysical": "Virtual",
                     "BusProtocol": "Disk Image", "TotalSize": 2 * 1024**3,
                     "DeviceNode": "/dev/disk99"}
        self.env = {"TMPDIR": "original-temp", "GOTMPDIR": "original-go", "GOCACHE": "cache"}
        self.addCleanup(patch.stopall)
        patch.object(runner.platform, "system", return_value="Darwin").start()
        patch.object(runner, "print", create=True).start()
        patch.object(runner, "process_table", return_value={}).start()
        patch.object(runner.tempfile, "mkdtemp", side_effect=self.private_holder).start()
        patch.object(runner.os.path, "ismount", side_effect=lambda path: self.mounted and Path(path) == self.mount).start()
        patch.object(self.workspace, "tool", side_effect=self.tool).start()

    def private_holder(self, **options):
        self.assertEqual(options, {"prefix": "nw-", "dir": "/private/tmp"})
        self.holder.mkdir(mode=0o700)
        return str(self.holder)

    def tool(self, command):
        self.calls.append(command)
        if command == ["/usr/bin/vm_stat"]:
            return b"Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free: 500000.\nPages inactive: 100000.\nPages speculative: 100000.\n"
        if command[1:3] == ["attach", "-nomount"]:
            self.attached = True
            return b"/dev/disk99\t\n"
        if command[1:] == ["info", "-plist"]:
            images = [{"image-path": self.image_path, "owner-uid": self.uid,
                       "hdid-pid": self.helper,
                       "system-entities": [{"dev-entry": "/dev/disk99"}]}] if self.attached else []
            return plistlib.dumps({"images": images})
        if command[1:3] == ["info", "-plist"]:
            return plistlib.dumps(self.info)
        if command[1:2] == ["eraseVolume"]:
            self.info.update(VolumeName=self.workspace.label, MountPoint=str(self.mount))
            return b"mounted\n"
        if command[1] == "unmount":
            self.mounted = False
            return b"unmounted\n"
        if command[1] == "mount":
            self.mounted = True
            self.info["MountPoint"] = command[3]
            return b"mounted\n"
        if command[1] == "detach":
            self.attached = False
            self.mounted = False
            if (self.mount / "workspace").exists():
                shutil.rmtree(self.mount / "workspace")
            return b"ejected\n"
        self.fail("unexpected command " + repr(command))

    def activate(self):
        return self.workspace.activate(self.env)

    def test_private_scratch_and_env_are_scoped_and_logs_survive_cleanup(self):
        scratch = self.activate()
        self.assertEqual(scratch, self.mount / "workspace")
        self.assertEqual(self.mount.stat().st_mode & 0o777, 0o700)
        self.assertEqual(self.holder.stat().st_mode & 0o777, 0o700)
        self.assertEqual(self.logs.stat().st_mode & 0o777, 0o700)
        self.assertEqual(self.env["TMPDIR"], str(scratch / "tmp"))
        self.assertEqual(self.env["GOTMPDIR"], str(scratch / "go"))
        self.assertEqual(self.env["GOCACHE"], "cache")
        self.workspace.close()
        self.workspace.close()
        self.assertEqual(sum(c[1:2] == ["detach"] for c in self.calls if len(c) > 1), 1)
        receipt = json.loads((self.logs / "workspace.json").read_text())
        self.assertEqual(receipt["attached_device"], "/dev/disk99")
        self.assertTrue(receipt["cleaned"])
        self.assertFalse(receipt["active"])

    def test_linux_and_explicit_disk_mode_never_call_native_image_tools(self):
        for system, mode in [("Linux", "auto"), ("Darwin", "off")]:
            with self.subTest(system=system, mode=mode), patch.object(runner.platform, "system", return_value=system):
                self.env["NODWYN_TEST_RAM"] = mode
                self.assertIsNone(self.activate())
                self.workspace.close()
        self.assertEqual(self.calls, [])
        self.assertEqual(self.env["TMPDIR"], "original-temp")

    def test_low_memory_falls_back_before_attachment(self):
        with patch.object(self.workspace, "tool", return_value=b"page size of 16384 bytes\nPages free: 1.\nPages inactive: 1.\nPages speculative: 1.\n"):
            self.assertIsNone(self.activate())
        self.assertIsNone(self.workspace.device)
        self.assertIn("less than 6 GiB", self.workspace.state["reason"])
        self.assertEqual(self.env["GOTMPDIR"], "original-go")

    def test_missing_tools_or_unknown_memory_output_fall_back(self):
        for result in [FileNotFoundError("not installed"), b"unexpected output"]:
            with self.subTest(result=result), patch.object(self.workspace, "tool") as tool:
                if isinstance(result, Exception): tool.side_effect = result
                else: tool.return_value = result
                self.assertIsNone(self.activate())
        self.assertEqual(self.env["TMPDIR"], "original-temp")

    def test_format_failure_detaches_before_disk_fallback(self):
        original = self.tool
        def failed(command):
            if command[1:2] == ["eraseVolume"]:
                raise subprocess.CalledProcessError(1, command)
            return original(command)
        with patch.object(self.workspace, "tool", side_effect=failed):
            self.assertIsNone(self.activate())
        self.assertFalse(self.attached)
        self.assertTrue(self.workspace.state["cleaned"])
        self.assertEqual(self.env["TMPDIR"], "original-temp")

    def test_timeout_with_a_device_receipt_still_detaches(self):
        original = self.tool
        def failed(command):
            output = original(command)
            if command[1:3] == ["attach", "-nomount"]:
                raise subprocess.TimeoutExpired(command, 30, output=output)
            return output
        with patch.object(self.workspace, "tool", side_effect=failed):
            self.assertIsNone(self.activate())
        self.assertFalse(self.attached)

    def test_ambiguous_attachment_does_not_silently_fall_back(self):
        original = self.tool
        def failed(command):
            if command[1:3] == ["attach", "-nomount"]:
                raise subprocess.TimeoutExpired(command, 30)
            return original(command)
        with patch.object(self.workspace, "tool", side_effect=failed):
            with self.assertRaisesRegex(RuntimeError, "without a device receipt"):
                self.activate()
        self.assertFalse(any(c[1:2] in (["eraseVolume"], ["detach"]) for c in self.calls))

    def test_physical_device_is_never_formatted(self):
        self.info["VirtualOrPhysical"] = "Physical"
        with self.assertRaisesRegex(RuntimeError, "unverified RAM device"):
            try:
                self.activate()
            finally:
                self.workspace.close()
        self.assertFalse(any(c[1:2] == ["eraseVolume"] for c in self.calls))

    def test_other_users_and_non_ram_images_are_never_formatted_or_detached(self):
        for field, value in [("uid", os.getuid()+1), ("image_path", "/a/real/disk.dmg")]:
            with self.subTest(field=field):
                setattr(self, field, value)
                self.workspace.device = None
                with self.assertRaises(RuntimeError):
                    try:
                        self.activate()
                    finally:
                        self.workspace.close()
                self.assertFalse(any(c[1:2] in (["eraseVolume"], ["detach"]) for c in self.calls))
                self.uid = os.getuid()
                self.image_path = "ram://4194304"

    def test_reused_device_identity_is_not_detached(self):
        self.activate()
        self.helper += 1
        self.mounted = False  # The old image was removed before its device ID was reused.
        shutil.rmtree(self.mount / "workspace")
        self.workspace.close()
        self.assertFalse(any(c[1:2] == ["detach"] for c in self.calls))

    def test_detach_failure_is_an_error_and_keeps_device_receipt(self):
        self.activate()
        original = self.tool
        def failed(command):
            if command[1] == "detach": raise subprocess.CalledProcessError(1, command)
            return original(command)
        with patch.object(self.workspace, "tool", side_effect=failed):
            with self.assertRaisesRegex(RuntimeError, "RAM cleanup failed for /dev/disk99"):
                self.workspace.close()
        self.assertEqual(self.workspace.device, "/dev/disk99")
        self.assertIn("cleanup_error", json.loads((self.logs / "workspace.json").read_text()))

    def test_transient_busy_detach_is_retried_without_force(self):
        self.activate()
        original = self.tool
        attempts = []
        def busy(command):
            if command[1:2] == ["detach"]:
                attempts.append(command)
                if len(attempts) == 1:
                    raise subprocess.CalledProcessError(16, command)
            return original(command)
        with patch.object(self.workspace, "tool", side_effect=busy), patch.object(runner.time, "sleep"):
            self.workspace.close()
        self.assertEqual(len(attempts), 2)
        self.assertTrue(all(command == ["/usr/bin/hdiutil", "detach", "/dev/disk99"] for command in attempts))
        self.assertFalse(self.holder.exists())

    def test_native_tool_deadline_kills_and_reaps_its_owned_group(self):
        command = ["fixture-native-tool"]
        with patch.object(runner.subprocess, "Popen") as popen, patch.object(runner.os, "killpg") as kill:
            process = popen.return_value
            process.pid = 123
            process.returncode = -signal.SIGKILL
            process.communicate.side_effect = [subprocess.TimeoutExpired(command, 30), (b"", None)]
            with self.assertRaises(subprocess.TimeoutExpired):
                runner.MacRamWorkspace.tool(self.workspace, command)
            kill.assert_called_once_with(123, signal.SIGKILL)
            self.assertEqual(process.communicate.call_count, 2)
        self.assertTrue(self.workspace.commands[0]["timeout"])
        self.assertEqual(self.workspace.commands[0]["exit"], -signal.SIGKILL)

    def test_setup_interrupt_is_delayed_until_device_can_be_cleaned(self):
        original = self.tool
        def interrupted(command):
            output = original(command)
            if command[1:3] == ["attach", "-nomount"]:
                signal.getsignal(signal.SIGTERM)(signal.SIGTERM, None)
            return output
        with patch.object(self.workspace, "tool", side_effect=interrupted):
            with self.assertRaises(SystemExit) as stopped:
                try:
                    self.activate()
                finally:
                    self.workspace.close()
        self.assertEqual(stopped.exception.code, 143)
        self.assertFalse(self.attached)


if __name__ == "__main__":
    unittest.main()
