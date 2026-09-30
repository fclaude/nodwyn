#!/usr/bin/env bash
# Self-check for a candidate binary (ADR 13). The candidate must:
#   1. report a version and evaluate Scheme;
#   2. start a session in this repository with the dev tools registered;
#   3. read a file, run the log package's tests through the go-test dev
#      tool (a standing approval, so no prompt), and get them green;
#   4. complete one real model turn against the control plane (when the
#      model's key is in the keychain, the candidate asks for the
#      passphrase on the terminal: run it, or make promote, from one);
#   5. replay its own log.
# Deterministic steps use :eval (user origin); only step 4 needs the model.
set -euo pipefail
cd "$(dirname "$0")/.."
CAND=${1:-bin/nodwyn}
OUT=$(mktemp -t nodwyn-selfcheck.XXXXXX)
trap 'rm -f "$OUT"' EXIT

echo "selfcheck: $CAND ($("$CAND" version))"
test "$("$CAND" eval -e '(+ 1 2)')" = "3"

printf '%s\n' \
	':tools' \
	':eval (string-length (fs-read "README.md"))' \
	':eval (go-test "./internal/log")' \
	'Reply with exactly the single word READY and nothing else.' \
	':q' \
	| "$CAND" --no-confirm --keychain-tty --repo . >"$OUT" 2>&1

grep -q '^go-test ' "$OUT" || { echo "selfcheck: dev tools not registered"; cat "$OUT"; exit 1; }
grep -q 'ok  \\tgithub.com/fclaude/nodwyn/internal/log' "$OUT" || { echo "selfcheck: go-test through the dev tool did not pass"; cat "$OUT"; exit 1; }
if ! grep -q 'READY' "$OUT"; then
	echo "selfcheck: model turn did not complete"
	cat "$OUT"
	if grep -q '^keychain: not a terminal' "$OUT"; then
		echo "selfcheck: the keychain stayed locked: run this (or make promote) from a terminal"
	fi
	exit 1
fi

# Other clients may open sessions during the model turn. Replay this run's
# banner ID, never whichever session happens to be newest in the shared store.
SESSION=$(awk '$1 == "nodwyn" && $2 == "session" { print $3; exit }' "$OUT")
test -n "$SESSION" || { echo "selfcheck: session banner missing"; exit 1; }
"$CAND" replay "$SESSION" | grep 'READY' >/dev/null || { echo "selfcheck: replay of $SESSION failed"; exit 1; }
echo "selfcheck: ok (session $SESSION)"
