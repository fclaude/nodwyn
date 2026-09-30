#!/usr/bin/env bash
# What the webview build (`nodwyn app` in its own window, docs/app.md) needs
# from the machine it is built on.
#   scripts/webview-env.sh check        exit 0 when this system can build it
#   scripts/webview-env.sh run CMD...   run CMD with pkg-config able to find WebKitGTK
# macOS has WebKit built in: check succeeds and run runs CMD as is. On Linux
# and the BSDs, github.com/webview/webview_go asks pkg-config for
# webkit2gtk-4.0, which current distributions no longer ship; webkit2gtk-4.1
# is the same API on libsoup 3. When only 4.1 is installed, run writes a
# webkit2gtk-4.0.pc that requires 4.1 into a temporary directory at the front
# of PKG_CONFIG_PATH for as long as CMD runs. A system with 4.0 keeps 4.0.
set -euo pipefail
pkgconfig=${PKG_CONFIG:-pkg-config} # the go command honors PKG_CONFIG too

has() { command -v "$pkgconfig" >/dev/null && "$pkgconfig" --exists "$@"; }

missing() {
	echo "webview-env: WebKitGTK not found (pkg-config gtk+-3.0 and webkit2gtk-4.1);" \
		"install libwebkit2gtk-4.1-dev (Debian, Ubuntu) or webkit2gtk4.1-devel (Fedora)" >&2
}

case $(uname -s) in
Darwin) gtk=no ;;
Linux | FreeBSD | OpenBSD | NetBSD) gtk=yes ;;
*) gtk=unknown ;;
esac

case ${1:-} in
check)
	case $gtk in
	no) exit 0 ;;
	unknown) echo "webview-env: no webview build recipe for $(uname -s)" >&2; exit 1 ;;
	esac
	if has gtk+-3.0 && { has webkit2gtk-4.0 || has webkit2gtk-4.1; }; then
		exit 0
	fi
	missing
	exit 1
	;;
run)
	shift
	[ $# -gt 0 ] || { echo "usage: webview-env.sh run CMD..." >&2; exit 2; }
	if [ "$gtk" != yes ] || has webkit2gtk-4.0; then
		exec "$@"
	fi
	has webkit2gtk-4.1 || { missing; exit 1; }
	shim=$(mktemp -d -t nodwyn-webkit.XXXXXX)
	trap 'rm -rf "$shim"' EXIT
	cat >"$shim/webkit2gtk-4.0.pc" <<-EOF
		Name: webkit2gtk-4.0
		Description: webkit2gtk-4.1 under the name webview_go asks for (scripts/webview-env.sh)
		Version: $("$pkgconfig" --modversion webkit2gtk-4.1)
		Requires: webkit2gtk-4.1
	EOF
	export PKG_CONFIG_PATH="$shim${PKG_CONFIG_PATH:+:$PKG_CONFIG_PATH}"
	"$@"
	;;
*)
	echo "usage: webview-env.sh check | run CMD..." >&2
	exit 2
	;;
esac
