#!/usr/bin/env bash
# Promote the working-tree build to nibb-stable (ADR 12/13).
#   scripts/promote.sh v0.N-<phase>
# Refuses a dirty tree: the tag must point at what was built. Runs the
# acceptance suite and the selfcheck, then swaps the binaries and tags.
set -euo pipefail
cd "$(dirname "$0")/.."
TAG=${1:?usage: promote.sh vX.Y-phase}
STABLE_DIR=${NIBB_STABLE_DIR:-$HOME/.local/bin}
STABLE=$STABLE_DIR/nibb-stable

if [ -n "$(git status --porcelain)" ]; then
	echo "promote: working tree is not clean; commit first" >&2
	exit 1
fi
if git rev-parse -q --verify "refs/tags/$TAG" >/dev/null; then
	echo "promote: tag $TAG already exists" >&2
	exit 1
fi

make accept
make build VERSION="$TAG"
scripts/selfcheck.sh bin/nibb

mkdir -p "$STABLE_DIR"
if [ -x "$STABLE" ]; then
	mv -f "$STABLE" "$STABLE.prev"
	echo "promote: previous stable kept at $STABLE.prev"
fi
cp bin/nibb "$STABLE"
git tag -a "$TAG" -m "promoted to nibb-stable: $TAG"
echo "promote: $TAG -> $STABLE ($("$STABLE" version))"
case ":$PATH:" in
*":$STABLE_DIR:"*) ;;
*) echo "promote: note: $STABLE_DIR is not on your PATH" ;;
esac
