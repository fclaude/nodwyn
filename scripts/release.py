#!/usr/bin/env python3
"""Build portable release assets, and verify a downloaded copy of them.

Publishing is the release workflow's job (.github/workflows/release.yml): it uploads
what `build` wrote, downloads it again and runs `verify` before the draft is published."""

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
                info.size, info.mode, info.mtime = len(data), 0o755 if name.startswith("nodwyn") else 0o644, epoch
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
            # The licence and notice travel with the binaries (Apache 2.0, section 4).
            pack(output / f"nodwyn-{target}.tar.gz", {
                "nodwyn": binary, "nodwyn-egress": proxy,
                "LICENSE": tree / "LICENSE", "NOTICE": tree / "NOTICE"}, epoch)
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


def verify(directory, commit, version):
    """Checks a downloaded release: every asset and no other, matching its
    checksum, and built from this commit under this version."""
    if not re.fullmatch(r"[0-9a-f]{40}", commit):
        raise RuntimeError("a full source commit is required")
    version = release_version(version)
    names = {item.name for item in directory.iterdir()}
    if names != ASSETS:
        raise RuntimeError("release does not have exactly the required attachments")
    for name, expected in checksums((directory / "SHA256SUMS").read_bytes()).items():
        if digest((directory / name).read_bytes()) != expected:
            raise RuntimeError(f"downloaded checksum mismatch: {name}")
    metadata = json.loads((directory / "SOURCE.json").read_bytes())
    if metadata.get("commit") != commit or metadata.get("tag") != version:
        raise RuntimeError("release assets belong to another commit or version")
    print(f"Verified {len(ASSETS)} assets of {version} for {commit}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("build", "verify"))
    parser.add_argument("directory", type=Path)
    parser.add_argument("--version", required=True, help="release version, for example v0.1.0")
    parser.add_argument("--commit", help="verify: the full commit the release must have been built from")
    args = parser.parse_args()
    if args.action == "build":
        build(args.directory.resolve(), args.version)
    else:
        verify(args.directory.resolve(), args.commit or "", args.version)


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, OSError, subprocess.CalledProcessError) as error:
        raise SystemExit(f"release: {error}")
