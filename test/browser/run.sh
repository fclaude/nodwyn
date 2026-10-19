#!/usr/bin/env bash
# Browser acceptance checks for the console (ADR 21, ADR 30). Each check
# starts its own synthetic world: a scripted model, temporary directories,
# a daemon or the app, and a headless Chromium. Nothing touches real
# sessions, keys or folders, and no two checks share a port or a folder, so
# they run at the same time.
#
#   test/browser/run.sh            # all checks; screenshots in test/browser/out/<check>
#   test/browser/run.sh firstrun   # one check
#   BROWSER=webkit test/browser/run.sh   # screens, approve and tasks in WebKit
#
# Needs Node and Playwright's Chromium (npm install; npx playwright install
# chromium); it fails, saying so, without them. The window of `nodwyn app` is
# WebKit (WebKitGTK, WKWebView), whose faults Chromium does not show;
# BROWSER=webkit runs the checks that look at layout and the main flows
# there, if Playwright's WebKit is installed (npx playwright install webkit),
# and otherwise says so and runs nothing, since a person's machine may lack
# it. Validate and promotion run Chromium alone. The other checks need
# Chromium (remote maps a host name with one of its flags).
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
out="$here/out"
engine=${BROWSER:-chromium}
case $engine in
chromium) all="screens transcript approve firstrun firstmodel remote reconnect mcp credentials settings tasks schedule away workflows mail worktrees lastfolder attach models retrieval conversation-search install hosts" ;;
webkit)
	all="screens approve tasks"
	out="$out/webkit"
	;;
*)
	echo "run.sh: BROWSER is $engine; it is chromium or webkit" >&2
	exit 2
	;;
esac
mkdir -p "$out"
command -v node >/dev/null || {
	echo "run.sh: node is not installed, so the console cannot be checked here" >&2
	exit 1
}
[ -d "$here/node_modules/playwright" ] || (cd "$here" && npm install --silent)
if ! (cd "$here" && E=$engine node -e 'process.exit(require("fs").existsSync(require("playwright")[process.env.E].executablePath()) ? 0 : 1)'); then
	if [ "$engine" = webkit ]; then
		echo "run.sh: Playwright's WebKit is not installed, so nothing ran in it (cd test/browser && npx playwright install webkit)"
		exit 0
	fi
	echo "run.sh: Playwright's Chromium is not installed: cd test/browser && npx playwright install chromium" >&2
	exit 1
fi
(cd "$root" && go build -o "$out/nodwyn" ./cmd/nodwyn && go build -o "$out/demo" ./test/browser/demo)
read -r -a checks <<<"${*:-$all}"
declare -A group took code
stop() {
	for pid in "${group[@]}"; do kill -KILL -- "-$pid" 2>/dev/null || true; done
}
trap 'stop; exit 130' INT TERM
for check in "${checks[@]}"; do
	rm -rf "${out:?}/$check"
	mkdir -p "$out/$check"
	# timeout leads a process group of its own, which the check's demo and
	# Chromium join. A check that throws can exit without stopping its demo,
	# which then holds this script's output open, so a caller reading it
	# through a pipe waits forever: the whole group goes when the check ends.
	(cd "$here" && DEMO="$out/demo" NODWYN="$out/nodwyn" BROWSER="$engine" exec timeout 180 node "checks/$check.js" "$out/$check" >"$out/$check.log" 2>&1) &
	group[$check]=$!
	took[$check]=$EPOCHREALTIME
done
# Each check's log is printed once it and every check before it have
# ended, so the output reads in the order the checks were named.
status=0
next=0
while [ "$next" -lt "${#checks[@]}" ]; do
	if wait -n -p ended "${group[@]}"; then rc=0; else rc=$?; fi
	for check in "${!group[@]}"; do
		if [ "${group[$check]}" = "$ended" ]; then
			kill -KILL -- "-$ended" 2>/dev/null || true
			unset "group[$check]"
			code[$check]=$rc
			took[$check]=$(awk "BEGIN { printf \"%.1f\", $EPOCHREALTIME - ${took[$check]} }")
		fi
	done
	while [ "$next" -lt "${#checks[@]}" ] && [ -n "${code[${checks[$next]}]+set}" ]; do
		check=${checks[$next]}
		if [ "${code[$check]}" = 0 ]; then result=ok; else result="FAILED (exit ${code[$check]})"; status=1; fi
		echo "== $check"
		cat "$out/$check.log"
		echo "== $check: $result in ${took[$check]}s"
		next=$((next + 1))
	done
done
exit $status
