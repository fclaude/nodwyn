#!/usr/bin/env bash
# Builds bin/Nodwyn.app: the nodwyn binary with a native window (webview tag),
# for Apple silicon and Intel in one universal executable, ad-hoc signed.
# Usage: scripts/app-macos.sh [VERSION]
set -euo pipefail
cd "$(dirname "$0")/.."
version=${1:-$(git describe --always --dirty --abbrev=12 --exclude='*' 2>/dev/null || echo dev)}
app=bin/Nodwyn.app
rm -rf "$app" bin/.app-build
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources" bin/.app-build
# webview.h trips a clang deprecation warning that is not ours to fix.
export CGO_ENABLED=1 CGO_CXXFLAGS="-Wno-deprecated-literal-operator" MACOSX_DEPLOYMENT_TARGET=13.0
ldflags="-s -w -X main.version=$version"
for arch in arm64 amd64; do
	clang_arch=$arch
	[ "$arch" = amd64 ] && clang_arch=x86_64
	GOOS=darwin GOARCH=$arch CC="clang -arch $clang_arch" CXX="clang++ -arch $clang_arch" \
		go build -trimpath -tags webview -ldflags "$ldflags" -o "bin/.app-build/nodwyn-$arch" ./cmd/nodwyn
done
lipo -create -output "$app/Contents/MacOS/nodwyn" bin/.app-build/nodwyn-arm64 bin/.app-build/nodwyn-amd64
# The egress proxy of contained MCP servers (ADR 23 W3b) runs in the Linux
# virtual machine podman or docker uses, so it is built for Linux, once per
# architecture; nodwyn picks the one for its own.
for arch in arm64 amd64; do
	CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags "$ldflags" -o "$app/Contents/MacOS/nodwyn-egress-linux-$arch" ./cmd/nodwyn-egress
done
rm -rf bin/.app-build
sed "s/@VERSION@/${version#v}/g" packaging/macos/Info.plist > "$app/Contents/Info.plist"
cp packaging/macos/Nodwyn.icns "$app/Contents/Resources/Nodwyn.icns"
codesign --force --deep --sign - "$app"
echo "built $app ($version)"
