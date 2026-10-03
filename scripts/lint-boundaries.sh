#!/usr/bin/env bash
# Enforces ADR 10 (core/front-end boundary).
#
# Rule 1: no package outside internal/frontend/... and cmd/... may depend,
#         directly or transitively, on internal/frontend/... .
# Rule 2: no package outside internal/frontend/... and cmd/... may depend,
#         directly or transitively, on any github.com/charmbracelet/... or charm.land/... package.
#
# Rule 3: "don't ask" (core.Session.SetApproveAll) is turned on only by a
#         person through the daemon's protocol: nothing outside
#         internal/frontend/rpc calls it, and nothing outside
#         internal/core/confirm.go touches the flag, so no Scheme primitive
#         can be wired to it (tests aside).
#
# The core must be usable by any front-end, including ones that do not exist yet.
# test/browser/... is exempt too: it is a harness that drives front-ends.
set -euo pipefail

module=$(go list -m)
status=0

while read -r pkg; do
	case "$pkg" in
	"$module"/internal/frontend/*|"$module"/cmd/*|"$module"/test/browser/*) continue ;;
	esac
	bad=$(go list -deps "$pkg" 2>/dev/null | grep -E "^$module/internal/frontend/|^github.com/charmbracelet/|^charm.land/" || true)
	if [ -n "$bad" ]; then
		echo "boundary violation: $pkg depends on:" >&2
		echo "$bad" | sed 's/^/    /' >&2
		status=1
	fi
done < <(go list ./...)

callers=$(git ls-files '*.go' | grep -v '_test\.go$' | xargs grep -l 'SetApproveAll(' | grep -v -e '^internal/frontend/rpc/' -e '^internal/core/confirm\.go$' || true)
writers=$(git ls-files 'internal/core/*.go' | grep -v '_test\.go$' | xargs grep -l 's\.approveAll\b' | grep -v '^internal/core/confirm\.go$' || true)
if [ -n "$callers$writers" ]; then
	echo "boundary violation: \"don't ask\" reached outside the protocol:" >&2
	echo "$callers$writers" | sed 's/^/    /' >&2
	status=1
fi

if [ "$status" -eq 0 ]; then
	echo "boundaries ok"
fi
exit "$status"
