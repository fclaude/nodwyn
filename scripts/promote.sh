#!/usr/bin/env bash
# Promote the working-tree build to nodwyn-stable (ADR 12/13).
#   scripts/promote.sh
# The stable binary's version is the commit it was built from, so a dirty
# tree is refused. Runs the acceptance suite and the selfcheck, then swaps
# the binaries.
# nodwyn-stable is the webview build, so `nodwyn-stable app` started without a
# terminal (a desktop launcher) opens a window. Where it cannot be built (Linux
# without WebKitGTK) it is the browser build, and promote says so.
set -euo pipefail
cd "$(dirname "$0")/.."
STABLE_DIR=${NODWYN_STABLE_DIR:-$HOME/.local/bin}
STABLE=$STABLE_DIR/nodwyn-stable

if [ -n "$(git status --porcelain)" ]; then
	echo "promote: working tree is not clean; commit first" >&2
	exit 1
fi
id=$(git rev-parse --short=12 HEAD)

target=build-webview
browser_only=
if ! scripts/webview-env.sh check; then
	target=build
	browser_only="promote: note: $STABLE is built without the webview tag; \`nodwyn-stable app\` opens the default browser, not a window (docs/app.md, \"Linux and other systems\")"
	echo "$browser_only" >&2
fi

make accept
make "$target" VERSION="$id"
scripts/selfcheck.sh bin/nodwyn

mkdir -p "$STABLE_DIR"
kept=
if [ -x "$STABLE" ]; then
	mv -f "$STABLE" "$STABLE.prev"
	kept=1
	echo "promote: previous stable kept at $STABLE.prev"
fi
cp bin/nodwyn "$STABLE"
if [ "$target" = build-webview ]; then
	tags=$(go version -m "$STABLE" | awk -F'\t' '$2 == "build" && $3 ~ /^-tags=/ { print substr($3, 7) }') || tags=
	case ",$tags," in
	*,webview,*) ;;
	*)
		echo "promote: go version -m $STABLE does not show -tags=webview; \`nodwyn-stable app\` would open no window" >&2
		if [ -n "$kept" ]; then
			mv -f "$STABLE.prev" "$STABLE"
			echo "promote: previous stable restored" >&2
		else
			rm -f "$STABLE"
		fi
		exit 1
		;;
	esac
fi
echo "promote: $STABLE is now $("$STABLE" version)"
if [ -n "$browser_only" ]; then echo "$browser_only" >&2; fi
case ":$PATH:" in
*":$STABLE_DIR:"*) ;;
*) echo "promote: note: $STABLE_DIR is not on your PATH" ;;
esac
