#!/usr/bin/env python3
"""Build portable release assets and publish a verified Gitea release."""

import argparse
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tarfile
import tempfile
import urllib.error
import urllib.parse
import urllib.request
import uuid


ROOT = Path(__file__).resolve().parents[1]
TARGETS = ("linux-amd64", "linux-arm64", "darwin-amd64", "darwin-arm64")
PAYLOADS = {f"nodwyn-{target}.tar.gz" for target in TARGETS} | {
    "nodwyn.pdf", "expert.pdf", "SOURCE.json"
}
ASSETS = PAYLOADS | {"SHA256SUMS"}


def digest(data):
    return hashlib.sha256(data).hexdigest()


def run(*args, cwd=ROOT, env=None):
    return subprocess.check_output(args, cwd=cwd, env=env, text=True).strip()


def pack(path, files, epoch):
    # Stable archive headers make a retried build independent of runner ownership.
    with path.open("wb") as raw, gzip.GzipFile(fileobj=raw, mode="wb", mtime=0, filename="") as zipped:
        with tarfile.open(fileobj=zipped, mode="w") as archive:
            for name, source in sorted(files.items()):
                data = source.read_bytes()
                info = tarfile.TarInfo(name)
                info.size, info.mode, info.mtime = len(data), 0o755, epoch
                archive.addfile(info, io.BytesIO(data))


def release_version(value):
    if not re.fullmatch(r"v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)", value):
        raise RuntimeError("an explicit release version such as v0.1.0 is required")
    return value


def build(output, version=None):
    version = release_version(version or os.environ.get("RELEASE_VERSION", ""))
    if run("git", "status", "--porcelain", "--untracked-files=no"):
        raise RuntimeError("commit tracked changes before building a release")
    commit = run("git", "rev-parse", "HEAD")
    epoch = int(run("git", "show", "-s", "--format=%ct", "HEAD"))
    subprocess.run(["python3", "docs/book/source/generate.py", "--check"], cwd=ROOT, check=True)
    output.mkdir(parents=True, exist_ok=True)
    if any(output.iterdir()):
        raise RuntimeError("the release output directory must be empty")
    env = dict(os.environ, CGO_ENABLED="0", SOURCE_DATE_EPOCH=str(epoch), FORCE_SOURCE_DATE="1")
    # Helper builds in the selfcheck also run in the archive, without Git metadata.
    env["GOFLAGS"] = (env.get("GOFLAGS", "") + " -buildvcs=false").strip()
    with tempfile.TemporaryDirectory(prefix="nodwyn-release-") as temporary:
        tree = Path(temporary)
        archive = subprocess.check_output(["git", "archive", "HEAD"], cwd=ROOT)
        with tarfile.open(fileobj=io.BytesIO(archive)) as source:
            source.extractall(tree, filter="data")
        binaries = tree / "bin"
        binaries.mkdir(exist_ok=True)
        for target in TARGETS:
            system, arch = target.split("-")
            binary = binaries / f"nodwyn-{target}"
            subprocess.run([
                "go", "build", "-trimpath", "-buildvcs=false", "-ldflags", f"-s -w -X main.version={commit}",
                "-o", str(binary), "./cmd/nodwyn"
            ], cwd=tree, env=dict(env, GOOS=system, GOARCH=arch), check=True)
            proxy = binaries / f"nodwyn-egress-linux-{arch}"
            if not proxy.exists():
                subprocess.run([
                    "go", "build", "-trimpath", "-buildvcs=false", "-ldflags", f"-s -w -X main.version={commit}",
                    "-o", str(proxy), "./cmd/nodwyn-egress"
                ], cwd=tree, env=dict(env, GOOS="linux", GOARCH=arch), check=True)
            pack(output / f"nodwyn-{target}.tar.gz", {"nodwyn": binary, "nodwyn-egress": proxy}, epoch)
        # The runner executes the Linux amd64 build against the repository's fake model.
        subprocess.run(["bash", "scripts/selfcheck.sh", str(binaries / "nodwyn-linux-amd64")],
                       cwd=tree, env=env, check=True)
        for pdf in (tree / "docs/book/nodwyn.pdf", tree / "docs/expert/expert.pdf"):
            pdf.unlink(missing_ok=True)
        subprocess.run(["make", "book", "expert"], cwd=tree, env=env, check=True)
        for name, source in [("nodwyn.pdf", tree / "docs/book/nodwyn.pdf"),
                             ("expert.pdf", tree / "docs/expert/expert.pdf")]:
            if not source.read_bytes().startswith(b"%PDF-"):
                raise RuntimeError(f"{name} is not a PDF")
            shutil.copyfile(source, output / name)
    metadata = {"commit": commit, "tag": version, "source_date_epoch": epoch,
                "go": run("go", "version"), "targets": list(TARGETS),
                "executed_target": "linux-amd64", "webview": False,
                "proxy": "Linux, matching the archive architecture; runs inside the MCP container"}
    (output / "SOURCE.json").write_text(json.dumps(metadata, indent=2) + "\n")
    (output / "SHA256SUMS").write_text("".join(
        f"{digest((output / name).read_bytes())}  {name}\n" for name in sorted(PAYLOADS)))
    print(f"Built {len(ASSETS)} assets for {commit} in {output}")


def checksums(data):
    result = {}
    for line in data.decode().splitlines():
        match = re.fullmatch(r"([0-9a-f]{64})  ([A-Za-z0-9_.-]+)", line)
        if not match or match[2] in result:
            raise RuntimeError("invalid or duplicate checksum entry")
        result[match[2]] = match[1]
    if set(result) != PAYLOADS:
        raise RuntimeError("checksum inventory does not match the release assets")
    return result


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        # Neither an API request nor a download may forward the job token elsewhere.
        return None


class Gitea:
    def __init__(self, url, repository, token):
        parsed = urllib.parse.urlsplit(url)
        if parsed.scheme not in ("http", "https") or not parsed.netloc or parsed.username or parsed.query or parsed.fragment:
            raise RuntimeError("GITEA_URL must be the server's HTTP(S) base URL")
        if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository) or not token:
            raise RuntimeError("GITEA_REPOSITORY and GITEA_TOKEN are required")
        self.url = url.rstrip("/")
        self.base = self.url + "/api/v1/repos/" + repository
        self.token = token
        self.opener = urllib.request.build_opener(NoRedirect())

    def request(self, method, url, data=None, content_type="application/json", missing=False):
        if urllib.parse.urlsplit(url)[:2] != urllib.parse.urlsplit(self.url)[:2]:
            raise RuntimeError("refusing a release attachment on another origin")
        request = urllib.request.Request(url, data=data, method=method, headers={
            "Authorization": "token " + self.token, "Content-Type": content_type})
        try:
            with self.opener.open(request, timeout=120) as response:
                return response.read()
        except urllib.error.HTTPError as error:
            status = error.code
            error.close()
            if missing and status == 404:
                return None
            raise RuntimeError(f"Gitea {method} returned HTTP {status}") from None

    def api(self, method, path, value=None, missing=False):
        data = None if value is None else json.dumps(value).encode()
        raw = self.request(method, self.base + path, data, missing=missing)
        return json.loads(raw) if raw else None

    def assets(self, release):
        items = self.api("GET", f"/releases/{release['id']}/assets")
        names = [item["name"] for item in items]
        if len(set(names)) != len(names) or not set(names) <= ASSETS:
            raise RuntimeError("release has duplicate or unexpected attachments; refusing to modify it")
        return {item["name"]: item for item in items}

    def download(self, asset):
        return self.request("GET", asset["browser_download_url"])

    def upload(self, release, name, data):
        boundary = "nodwyn-" + uuid.uuid4().hex
        body = (f'--{boundary}\r\nContent-Disposition: form-data; name="attachment"; filename="{name}"\r\n'
                'Content-Type: application/octet-stream\r\n\r\n').encode()
        body += data + f"\r\n--{boundary}--\r\n".encode()
        self.request("POST", self.base + f"/releases/{release['id']}/assets?name={urllib.parse.quote(name)}",
                     body, "multipart/form-data; boundary=" + boundary)

    def verify(self, release, commit):
        assets = self.assets(release)
        if set(assets) != ASSETS:
            raise RuntimeError("release does not have every required attachment")
        sums = checksums(self.download(assets["SHA256SUMS"]))
        for name, expected in sums.items():
            data = self.download(assets[name])
            if digest(data) != expected:
                raise RuntimeError(f"downloaded checksum mismatch: {name}")
            if name == "SOURCE.json":
                metadata = json.loads(data)
                if metadata["commit"] != commit or metadata.get("tag") != release["tag_name"]:
                    raise RuntimeError("release assets belong to another commit or version")


def publish(output, client, commit, version=None):
    if not re.fullmatch(r"[0-9a-f]{40}", commit):
        raise RuntimeError("a full source commit is required")
    tag = release_version(version or os.environ.get("RELEASE_VERSION", ""))
    marker = f"Source commit: `{commit}`"
    release = client.api("GET", "/releases/tags/" + tag, missing=True)
    if release:
        if marker not in release.get("body", "") or release["target_commitish"] != commit:
            raise RuntimeError("existing release does not match this source commit")
        if not release["draft"]:
            client.verify(release, commit)
            print("Verified existing published release: " + release["html_url"])
            return
    local = {name: (output / name).read_bytes() for name in ASSETS}
    sums = checksums(local["SHA256SUMS"])
    metadata = json.loads(local["SOURCE.json"])
    if metadata["commit"] != commit or metadata.get("tag") != tag:
        raise RuntimeError("local assets belong to another commit or version")
    if any(digest(local[name]) != sha for name, sha in sums.items()):
        raise RuntimeError("local release checksum mismatch")
    if not release:
        release = client.api("POST", "/releases", {
            "tag_name": tag, "target_commitish": commit, "name": "nodwyn " + tag,
            "draft": True, "prerelease": False,
            "body": marker + "\n\nPortable Linux and macOS binaries for amd64 and arm64, "
                    "both books rebuilt from this source, and SHA-256 checksums.\n\n"
                    "Acceptance checks and the Linux amd64 fake-model selfcheck passed. "
                    "Other targets are cross-compiled, not executed. Builds omit the native webview: "
                    "`nodwyn app` opens a browser. macOS builds are unsigned and not notarised. "
                    "Each archive includes a Linux nodwyn-egress for its architecture, for MCP containers."
        })
    assets = client.assets(release)
    for name in sorted(ASSETS):
        if name in assets:
            if digest(client.download(assets[name])) == digest(local[name]):
                continue
            # Only an unpublished draft belonging to this commit can be repaired.
            client.api("DELETE", f"/releases/{release['id']}/assets/{assets[name]['id']}")
        client.upload(release, name, local[name])
    client.verify(release, commit)
    release = client.api("PATCH", f"/releases/{release['id']}", {"draft": False})
    if release["draft"]:
        raise RuntimeError("Gitea did not publish the verified draft")
    print("Published verified release: " + release["html_url"])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("build", "publish"))
    parser.add_argument("directory", type=Path)
    parser.add_argument("--version", required=True, help="release version, for example v0.1.0")
    args = parser.parse_args()
    if args.action == "build":
        build(args.directory.resolve(), args.version)
    else:
        client = Gitea(os.environ.get("GITEA_URL", ""), os.environ.get("GITEA_REPOSITORY", ""),
                       os.environ.get("GITEA_TOKEN", ""))
        publish(args.directory, client, os.environ.get("RELEASE_COMMIT", ""), args.version)


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, OSError, subprocess.CalledProcessError) as error:
        raise SystemExit(f"release: {error}")
