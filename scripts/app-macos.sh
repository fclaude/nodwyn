#!/usr/bin/env bash
# Builds bin/Nibb.app: the nibb binary with a native window (webview tag),
# for Apple silicon and Intel in one universal executable, ad-hoc signed.
# Usage: scripts/app-macos.sh [VERSION]
set -euo pipefail
cd "$(dirname "$0")/.."
version=${1:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}
app=bin/Nibb.app
rm -rf "$app" bin/.app-build
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources" bin/.app-build
# webview.h trips a clang deprecation warning that is not ours to fix.
export CGO_ENABLED=1 CGO_CXXFLAGS="-Wno-deprecated-literal-operator" MACOSX_DEPLOYMENT_TARGET=13.0
ldflags="-s -w -X main.version=$version"
for arch in arm64 amd64; do
	clang_arch=$arch
	[ "$arch" = amd64 ] && clang_arch=x86_64
	GOOS=darwin GOARCH=$arch CC="clang -arch $clang_arch" CXX="clang++ -arch $clang_arch" \
		go build -trimpath -tags webview -ldflags "$ldflags" -o "bin/.app-build/nibb-$arch" ./cmd/nibb
done
lipo -create -output "$app/Contents/MacOS/nibb" bin/.app-build/nibb-arm64 bin/.app-build/nibb-amd64
rm -rf bin/.app-build
sed "s/@VERSION@/${version#v}/g" packaging/macos/Info.plist > "$app/Contents/Info.plist"
cp packaging/macos/Nibb.icns "$app/Contents/Resources/Nibb.icns"
codesign --force --deep --sign - "$app"
echo "built $app ($version)"
