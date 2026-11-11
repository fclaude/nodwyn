#!/usr/bin/env python3
"""Install a Linux launcher and icons for an existing nodwyn executable."""

import argparse
import os
from pathlib import Path
import shutil
import subprocess


ROOT = Path(__file__).resolve().parents[1]


def install(binary, data):
    binary = binary.expanduser().resolve(strict=True)
    if not binary.is_file() or not os.access(binary, os.X_OK):
        raise ValueError(f"not an executable: {binary}")
    # GIO looks up the executable before expanding percent field codes.
    if "%" in str(binary):
        raise ValueError("the executable path must not contain a percent sign")
    # Desktop Exec quoting is escaped again for the key file.
    command = str(binary)
    for char in ("\\", '"', "`", "$"):
        command = command.replace(char, "\\" + char)
    if "\n" in command or "\r" in command:
        raise ValueError("the executable path must not contain a newline")
    command = command.replace("\\", "\\\\")
    entry = (ROOT / "packaging/linux/com.nodwyn.app.desktop").read_text()
    entry = entry.replace("Exec=nodwyn app", f'Exec="{command}" app')
    icons = data / "icons/hicolor"
    # Earlier installs and packages put the console's square mark in
    # scalable/apps, which hicolor prefers to 512x512/apps at the sizes a dock
    # asks for. The tile goes in both, so that this account's copy is chosen
    # over a square that a system package still carries.
    for size in ("512x512", "scalable"):
        target = icons / size / "apps/com.nodwyn.app.png"
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(ROOT / "packaging/linux/com.nodwyn.app.png", target)
    (icons / "scalable/apps/com.nodwyn.app.svg").unlink(missing_ok=True)
    target = data / "applications/com.nodwyn.app.desktop"
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_text(entry)
    # Earlier workstation installs used this name; keep existing pins usable
    # without showing a second application in the menu.
    legacy = target.parent / "nodwyn.desktop"
    if legacy.exists():
        legacy.write_text(entry + "NoDisplay=true\n")
    if shutil.which("gtk-update-icon-cache"):
        subprocess.run(["gtk-update-icon-cache", "--force", "--ignore-theme-index",
                        str(data / "icons/hicolor")], check=True)
    if shutil.which("update-desktop-database"):
        subprocess.run(["update-desktop-database", str(target.parent)], check=True)
    print(f"Installed {target}; pin nodwyn from your application menu.")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path, help="path to nodwyn or nodwyn-stable")
    parser.add_argument("--data-dir", type=Path, default=Path(
        os.environ.get("XDG_DATA_HOME") or Path.home() / ".local/share"))
    args = parser.parse_args()
    install(args.binary, args.data_dir.expanduser().resolve())
