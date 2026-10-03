#!/usr/bin/env bash
# Browser acceptance checks for the console (ADR 21, ADR 30). Each check
# starts its own synthetic world: a scripted model, temporary directories,
# a daemon or the app, and a headless Chromium. Nothing touches real
# sessions, keys or folders.
#
#   test/browser/run.sh            # all checks; screenshots in test/browser/out
#   test/browser/run.sh firstrun   # one check
#
# Needs Node and Playwright's Chromium (npm install; npx playwright install chromium).
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
out="$here/out"
mkdir -p "$out"
(cd "$root" && go build -o "$out/nodwyn" ./cmd/nodwyn && go build -o "$out/demo" ./test/browser/demo)
[ -d "$here/node_modules/playwright" ] || (cd "$here" && npm install --silent)
checks=${*:-screens approve firstrun remote mcp credentials tasks}
status=0
pid=
trap '[ -n "$pid" ] && kill -KILL -- "-$pid" 2>/dev/null; exit 130' INT TERM
for check in $checks; do
	echo "== $check"
	# timeout leads a process group of its own, which the check's demo and
	# Chromium join. A check that throws exits without stopping its demo,
	# which then holds this script's output open, so a caller reading it
	# through a pipe waits forever: the whole group goes when the check ends.
	(cd "$here" && DEMO="$out/demo" NODWYN="$out/nodwyn" exec timeout 180 node "checks/$check.js" "$out") &
	pid=$!
	if ! wait "$pid"; then
		status=1
	fi
	kill -KILL -- "-$pid" 2>/dev/null || true
done
exit $status
