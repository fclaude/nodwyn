#!/usr/bin/env bash
# Enforces ADR 10 (core/front-end boundary).
#
# Rule 1: no package outside internal/frontend/... and cmd/... may depend,
#         directly or transitively, on internal/frontend/... .
# Rule 2: no package outside internal/frontend/... and cmd/... may depend,
#         directly or transitively, on any github.com/charmbracelet/... or charm.land/... package.
#
# The core must be usable by any front-end, including ones that do not exist yet.
set -euo pipefail

module=$(go list -m)
status=0

while read -r pkg; do
	case "$pkg" in
	"$module"/internal/frontend/*|"$module"/cmd/*) continue ;;
	esac
	bad=$(go list -deps "$pkg" 2>/dev/null | grep -E "^$module/internal/frontend/|^github.com/charmbracelet/|^charm.land/" || true)
	if [ -n "$bad" ]; then
		echo "boundary violation: $pkg depends on:" >&2
		echo "$bad" | sed 's/^/    /' >&2
		status=1
	fi
done < <(go list ./...)

if [ "$status" -eq 0 ]; then
	echo "boundaries ok"
fi
exit "$status"
