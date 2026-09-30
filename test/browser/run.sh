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
checks=${*:-screens approve firstrun remote mcp}
status=0
for check in $checks; do
	echo "== $check"
	if ! (cd "$here" && DEMO="$out/demo" NODWYN="$out/nodwyn" timeout 180 node "checks/$check.js" "$out"); then
		status=1
	fi
done
exit $status
