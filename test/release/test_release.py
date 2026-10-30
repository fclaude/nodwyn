"""Exercise the release builder's archives and the check a downloaded release must pass."""

import importlib.util
import json
import os
from pathlib import Path
import plistlib
import tarfile
import tempfile
import unittest
import zipfile


ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("release", ROOT / "scripts/release.py")
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)
COMMIT = "a" * 40


def write_app(path, commit=COMMIT, version="0.1.0", executable=None, leave_out=(), extra=()):
    """Writes a zip shaped as scripts/app-macos.sh writes one: its bundle, and
    the resource forks ditto keeps beside it."""
    files = {name: b"example" for name in release.APP_FILES}
    files["Nodwyn.app/Contents/MacOS/nodwyn"] = executable or release.FAT_MAGIC + b"nodwyn " + commit.encode()
    files["Nodwyn.app/Contents/Info.plist"] = plistlib.dumps({
        "CFBundleIdentifier": "com.nodwyn.app", "CFBundleIconFile": "Nodwyn", "CFBundleShortVersionString": version})
    files["__MACOSX/Nodwyn.app/._Info.plist"] = b"fork"
    with zipfile.ZipFile(path, "w") as archive:
        for name, data in files.items():
            if name not in leave_out:
                archive.writestr(name, data)
        for name in extra:
            archive.writestr(name, b"stray")


class VerifyTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.directory = Path(self.temp.name)
        for name in release.PAYLOADS:
            (self.directory / name).write_bytes((name + " example").encode())
        write_app(self.directory / release.APP)
        (self.directory / "SOURCE.json").write_text(json.dumps({"commit": COMMIT, "tag": "v0.1.0"}))
        self.sign()

    def sign(self):
        (self.directory / "SHA256SUMS").write_text("".join(
            f"{release.digest((self.directory / name).read_bytes())}  {name}\n"
            for name in sorted(release.PAYLOADS)))

    def verify(self, commit=COMMIT, version="v0.1.0"):
        release.verify(self.directory, commit, version)

    def test_a_complete_release_passes(self):
        self.verify()

    def test_a_corrupt_download_is_refused(self):
        (self.directory / "expert.pdf").write_bytes(b"corrupt")
        with self.assertRaisesRegex(RuntimeError, "downloaded checksum mismatch: expert.pdf"):
            self.verify()

    def test_a_missing_or_extra_attachment_is_refused(self):
        (self.directory / "nodwyn.pdf").unlink()
        with self.assertRaisesRegex(RuntimeError, "exactly the required attachments"):
            self.verify()
        (self.directory / "nodwyn.pdf").write_bytes(b"back")
        (self.directory / "extra.txt").write_bytes(b"unexpected")
        with self.assertRaisesRegex(RuntimeError, "exactly the required attachments"):
            self.verify()

    def test_another_commit_or_version_is_refused(self):
        with self.assertRaisesRegex(RuntimeError, "another commit or version"):
            self.verify(commit="b" * 40)
        with self.assertRaisesRegex(RuntimeError, "another commit or version"):
            self.verify(version="v0.2.0")

    def test_a_checksum_file_naming_something_else_is_refused(self):
        sums = (self.directory / "SHA256SUMS").read_text().replace("nodwyn.pdf", "other.pdf")
        (self.directory / "SHA256SUMS").write_text(sums)
        with self.assertRaisesRegex(RuntimeError, "checksum inventory"):
            self.verify()

    def test_a_short_commit_or_a_bad_version_is_refused(self):
        with self.assertRaisesRegex(RuntimeError, "full source commit"):
            self.verify(commit="aaaa")
        for version in ("", "build-aaaaaaaaaaaa", "v01.2.3", "v0.1.0/other", "$(false)"):
            with self.subTest(version=version):
                with self.assertRaisesRegex(RuntimeError, "explicit release version"):
                    self.verify(version=version)


class AppTest(unittest.TestCase):
    """The macOS app is built on a Mac and handed to the build: it is taken
    only whole, universal, and built from this commit under this version."""

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.path = Path(self.temp.name) / release.APP

    def check(self, **app):
        write_app(self.path, **app)
        release.check_app(self.path, COMMIT, "v0.1.0")

    def test_this_release_s_app_passes(self):
        self.check()

    def test_an_app_without_its_icon_or_proxy_is_refused(self):
        for name in ("Nodwyn.app/Contents/Resources/Nodwyn.icns", "Nodwyn.app/Contents/MacOS/nodwyn-egress-linux-arm64"):
            with self.subTest(name=name), self.assertRaisesRegex(RuntimeError, f"the app lacks {name}"):
                self.check(leave_out=(name,))

    def test_an_app_of_another_commit_or_version_is_refused(self):
        with self.assertRaisesRegex(RuntimeError, "another commit"):
            self.check(commit="b" * 40)
        with self.assertRaisesRegex(RuntimeError, "Info.plist is not this release's"):
            self.check(version="0.2.0")

    def test_a_single_architecture_executable_is_refused(self):
        with self.assertRaisesRegex(RuntimeError, "not universal"):
            self.check(executable=b"\xcf\xfa\xed\xfe" + COMMIT.encode())

    def test_a_file_outside_the_bundle_is_refused(self):
        with self.assertRaisesRegex(RuntimeError, "outside the bundle: run.sh"):
            self.check(extra=("run.sh",))

    def test_a_download_with_another_app_is_refused(self):
        directory = Path(self.temp.name) / "download"
        directory.mkdir()
        for name in release.PAYLOADS:
            (directory / name).write_bytes(b"example")
        write_app(directory / release.APP, commit="b" * 40)
        (directory / "SOURCE.json").write_text(json.dumps({"commit": COMMIT, "tag": "v0.1.0"}))
        (directory / "SHA256SUMS").write_text("".join(
            f"{release.digest((directory / name).read_bytes())}  {name}\n" for name in sorted(release.PAYLOADS)))
        with self.assertRaisesRegex(RuntimeError, "another commit"):
            release.verify(directory, COMMIT, "v0.1.0")


class ArchiveTest(unittest.TestCase):
    def test_archive_has_stable_headers_and_only_programs_run(self):
        with tempfile.TemporaryDirectory() as temp:
            directory = Path(temp)
            source = directory / "program"
            source.write_bytes(b"example binary")
            licence = directory / "LICENSE"
            licence.write_bytes(b"example licence")
            first, second = directory / "one.tar.gz", directory / "two.tar.gz"
            files = {"nodwyn": source, "nodwyn-egress": source, "LICENSE": licence}
            release.pack(first, files, 1234)
            os.utime(source, (9999, 9999))
            release.pack(second, files, 1234)
            self.assertEqual(first.read_bytes(), second.read_bytes())
            with tarfile.open(first) as archive:
                self.assertEqual(set(archive.getnames()), set(files))
                for item in archive.getmembers():
                    mode = 0o644 if item.name == "LICENSE" else 0o755
                    self.assertEqual((item.mode, item.mtime, item.uid, item.gid), (mode, 1234, 0, 0))
                    self.assertEqual(archive.extractfile(item).read(), files[item.name].read_bytes())


if __name__ == "__main__":
    unittest.main()
