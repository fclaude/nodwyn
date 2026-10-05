#!/usr/bin/env bash
# Wails uses the platform WebKit on macOS and GTK/WebKitGTK on Linux. Select
# its real GTK build tag rather than aliasing one pkg-config package to another.
set -euo pipefail
pkgconfig=${PKG_CONFIG:-pkg-config}
has() { command -v "$pkgconfig" >/dev/null && "$pkgconfig" --exists "$@"; }
tags() {
	case $(uname -s) in
	Darwin) echo webview ;;
	Linux)
		if has gtk4 webkitgtk-6.0; then echo webview
		elif has gtk+-3.0 webkit2gtk-4.1; then echo webview,gtk3
		else
			echo 'webview-env: install GTK and WebKitGTK development packages (gtk4/webkitgtk-6.0 or gtk+-3.0/webkit2gtk-4.1)' >&2
			return 1
		fi ;;
	*) echo "webview-env: no native build recipe for $(uname -s)" >&2; return 1 ;;
	esac
}
case ${1:-} in
check) tags >/dev/null ;;
tags) tags ;;
run)
	shift
	[ $# -gt 0 ] || { echo 'usage: webview-env.sh run CMD...' >&2; exit 2; }
	tags >/dev/null
	exec "$@" ;;
*) echo 'usage: webview-env.sh check | tags | run CMD...' >&2; exit 2 ;;
esac
