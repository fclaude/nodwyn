"""Exercise the installed launcher through GIO's desktop-entry parser."""

import os
from pathlib import Path
import subprocess
import tempfile
import time
import unittest


ROOT = Path(__file__).resolve().parents[2]


class DesktopTest(unittest.TestCase):
    def test_launcher_opens_app_with_its_installed_icon(self):
        with tempfile.TemporaryDirectory() as scratch:
            root = Path(scratch)
            binary = root / 'space " quote $dollar `tick` \\slash' / "nodwyn"
            binary.parent.mkdir()
            binary.write_text('#!/bin/sh\nprintf "%s" "$*" > "$NODWYN_LAUNCH_RESULT"\n')
            binary.chmod(0o755)
            data = root / "share"
            legacy = data / "applications/nodwyn.desktop"
            legacy.parent.mkdir(parents=True)
            legacy.write_text("[Desktop Entry]\nIcon=utilities-terminal\n")
            subprocess.run(["python3", str(ROOT / "scripts/install-desktop.py"),
                            str(binary), "--data-dir", str(data)], check=True)
            launcher = data / "applications/com.nodwyn.app.desktop"
            subprocess.run(["desktop-file-validate", str(launcher)], check=True)
            result = root / "result"
            subprocess.run(["gio", "launch", str(launcher)], check=True,
                           env=dict(os.environ, NODWYN_LAUNCH_RESULT=str(result)))
            deadline = time.monotonic() + 5
            while not result.exists() and time.monotonic() < deadline:
                time.sleep(0.02)
            self.assertEqual(result.read_text(), "app")
            self.assertIn("Icon=com.nodwyn.app\n", launcher.read_text())
            self.assertEqual(legacy.read_text(), launcher.read_text() + "NoDisplay=true\n")
            for size, source, extension in (("scalable", "icon.svg", "svg"),
                                             ("512x512", "icon-512.png", "png")):
                icon = data / f"icons/hicolor/{size}/apps/com.nodwyn.app.{extension}"
                self.assertEqual(icon.read_bytes(),
                                 (ROOT / "internal/frontend/web/assets/icons" / source).read_bytes())


if __name__ == "__main__":
    unittest.main()
