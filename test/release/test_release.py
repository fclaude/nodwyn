"""Exercise publication over HTTP without a real Gitea token or release."""

import email.parser
import email.policy
import importlib.util
import json
import os
from pathlib import Path
import tarfile
import tempfile
import threading
import unittest
from unittest.mock import patch
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlsplit


ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("release", ROOT / "scripts/release.py")
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)
COMMIT = "a" * 40


class PublisherTest(unittest.TestCase):
    def setUp(self):
        environment = patch.dict(os.environ, RELEASE_VERSION="v0.1.0")
        environment.start()
        self.addCleanup(environment.stop)
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.directory = Path(self.temp.name)
        for name in release.PAYLOADS:
            (self.directory / name).write_bytes((name + " example").encode())
        (self.directory / "SOURCE.json").write_text(json.dumps({"commit": COMMIT, "tag": "v0.1.0"}))
        (self.directory / "SHA256SUMS").write_text("".join(
            f"{release.digest((self.directory / name).read_bytes())}  {name}\n"
            for name in sorted(release.PAYLOADS)))
        self.state = {"release": None, "assets": {}, "next": 1, "writes": [],
                      "fail_upload": None, "corrupt": False, "redirect": False,
                      "escaped": False}
        state = self.state

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def answer(self, status, data=None, raw=False):
                self.send_response(status)
                self.end_headers()
                if data is not None:
                    self.wfile.write(data if raw else json.dumps(data).encode())

            def authorised(self):
                if self.headers.get("Authorization") != "token fake-release-token":
                    self.answer(401)
                    return False
                return True

            def do_GET(self):
                if not self.authorised():
                    return
                path = urlsplit(self.path).path
                if path == "/escaped":
                    state["escaped"] = True
                    return self.answer(200, b"unexpected", raw=True)
                if path.startswith("/download/"):
                    if state["redirect"]:
                        self.send_response(302)
                        self.send_header("Location", "/escaped")
                        self.end_headers()
                        return
                    asset = state["assets"][int(path.rsplit("/", 1)[1])]
                    data = asset["data"]
                    if state["corrupt"] and asset["name"] == "expert.pdf":
                        data += b"corrupt"
                    return self.answer(200, data, raw=True)
                if "/releases/tags/" in path:
                    return self.answer(200, state["release"]) if state["release"] else self.answer(404)
                if path.endswith("/assets"):
                    return self.answer(200, [{k: v for k, v in a.items() if k != "data"}
                                             for a in state["assets"].values()])
                self.answer(404)

            def do_POST(self):
                if not self.authorised():
                    return
                data = self.rfile.read(int(self.headers["Content-Length"]))
                state["writes"].append(("POST", self.path))
                if self.path.endswith("/releases"):
                    state["release"] = dict(json.loads(data), id=1,
                                            html_url=f"http://127.0.0.1:{self.server.server_port}/release/1")
                    return self.answer(201, state["release"])
                message = email.parser.BytesParser(policy=email.policy.default).parsebytes(
                    ("Content-Type: " + self.headers["Content-Type"] + "\r\n\r\n").encode() + data)
                part = next(message.iter_parts())
                name = part.get_filename()
                if name == state["fail_upload"]:
                    state["fail_upload"] = None
                    return self.answer(503)
                identifier = state["next"]
                state["next"] += 1
                state["assets"][identifier] = {
                    "id": identifier, "name": name, "data": part.get_payload(decode=True),
                    "browser_download_url": f"http://127.0.0.1:{self.server.server_port}/download/{identifier}"}
                self.answer(201, {"id": identifier})

            def do_DELETE(self):
                if not self.authorised():
                    return
                state["writes"].append(("DELETE", self.path))
                del state["assets"][int(self.path.rsplit("/", 1)[1])]
                self.answer(204)

            def do_PATCH(self):
                if not self.authorised():
                    return
                state["writes"].append(("PATCH", self.path))
                state["release"].update(json.loads(self.rfile.read(int(self.headers["Content-Length"]))))
                self.answer(200, state["release"])

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(server.server_close)
        self.addCleanup(thread.join)
        self.addCleanup(server.shutdown)
        self.client = release.Gitea(f"http://127.0.0.1:{server.server_port}", "fc/nodwyn", "fake-release-token")

    def publish(self):
        release.publish(self.directory, self.client, COMMIT)

    def test_uploads_every_asset_before_publishing_and_retry_is_read_only(self):
        self.publish()
        self.assertFalse(self.state["release"]["draft"])
        self.assertEqual({a["name"] for a in self.state["assets"].values()}, release.ASSETS)
        self.assertEqual(self.state["writes"][-1][0], "PATCH")
        before = list(self.state["writes"])
        (self.directory / "expert.pdf").write_bytes(b"a later rebuild must not replace a release")
        self.publish()
        self.assertEqual(self.state["writes"], before)

    def test_explicit_version_names_the_release_and_tag(self):
        self.publish()
        self.assertEqual(self.state["release"]["tag_name"], "v0.1.0")
        self.assertEqual(self.state["release"]["name"], "nodwyn v0.1.0")

    def test_missing_or_invalid_version_creates_no_release(self):
        for version in ("", "build-aaaaaaaaaaaa", "v01.2.3", "v0.1.0/other", "$(false)"):
            with self.subTest(version=version), patch.dict(os.environ, RELEASE_VERSION=version):
                with self.assertRaisesRegex(RuntimeError, "explicit release version"):
                    self.publish()
                self.assertEqual(self.state["writes"], [])

    def test_assets_for_another_version_are_refused(self):
        with patch.dict(os.environ, RELEASE_VERSION="v0.2.0"):
            with self.assertRaisesRegex(RuntimeError, "local assets belong to another commit or version"):
                self.publish()
        self.assertEqual(self.state["writes"], [])

    def test_failed_upload_leaves_draft_and_retry_repairs_only_draft(self):
        self.state["fail_upload"] = "expert.pdf"
        with self.assertRaisesRegex(RuntimeError, "503"):
            self.publish()
        self.assertTrue(self.state["release"]["draft"])
        next(iter(self.state["assets"].values()))["data"] = b"old interrupted draft"
        self.publish()
        self.assertFalse(self.state["release"]["draft"])
        self.assertTrue(any(method == "DELETE" for method, _ in self.state["writes"]))

    def test_download_corruption_prevents_publication(self):
        self.state["corrupt"] = True
        with self.assertRaisesRegex(RuntimeError, "downloaded checksum mismatch"):
            self.publish()
        self.assertTrue(self.state["release"]["draft"])
        self.assertFalse(any(method == "PATCH" for method, _ in self.state["writes"]))

    def test_local_corruption_creates_no_release(self):
        (self.directory / "expert.pdf").write_bytes(b"bad local bytes")
        with self.assertRaisesRegex(RuntimeError, "local release checksum mismatch"):
            self.publish()
        self.assertIsNone(self.state["release"])

    def test_wrong_source_commit_is_refused(self):
        with self.assertRaisesRegex(RuntimeError, "local assets belong to another commit"):
            release.publish(self.directory, self.client, "b" * 40)
        self.assertIsNone(self.state["release"])

    def test_unrelated_release_is_not_modified(self):
        self.state["release"] = {"body": "someone else's release", "target_commitish": COMMIT, "draft": True}
        with self.assertRaisesRegex(RuntimeError, "existing release does not match"):
            self.publish()
        self.assertEqual(self.state["writes"], [])

    def test_redirects_are_not_followed(self):
        self.state["redirect"] = True
        with self.assertRaisesRegex(RuntimeError, "HTTP 302"):
            self.publish()
        self.assertTrue(self.state["release"]["draft"])
        self.assertFalse(self.state["escaped"])

    def test_other_origin_download_is_refused_before_request(self):
        with self.assertRaisesRegex(RuntimeError, "another origin"):
            self.client.download({"browser_download_url": "http://example.invalid/attachment"})

    def test_archive_contains_executable_pair_with_stable_headers(self):
        source = self.directory / "program"
        source.write_bytes(b"example binary")
        first, second = self.directory / "one.tar.gz", self.directory / "two.tar.gz"
        files = {"nodwyn": source, "nodwyn-egress": source}
        release.pack(first, files, 1234)
        os.utime(source, (9999, 9999))
        release.pack(second, files, 1234)
        self.assertEqual(first.read_bytes(), second.read_bytes())
        with tarfile.open(first) as archive:
            self.assertEqual(set(archive.getnames()), set(files))
            for item in archive.getmembers():
                self.assertEqual((item.mode, item.mtime, item.uid, item.gid), (0o755, 1234, 0, 0))
                self.assertEqual(archive.extractfile(item).read(), b"example binary")


if __name__ == "__main__":
    unittest.main()
