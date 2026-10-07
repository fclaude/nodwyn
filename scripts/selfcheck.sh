#!/usr/bin/env bash
# Self-check for a candidate binary (ADR 13). The candidate must:
#   1. report a version and evaluate Scheme;
#   2. start a session in this repository with the dev tools registered;
#   3. read a file, run the log package's tests through the go-test dev
#      tool (a standing approval, so no prompt), and get them green;
#   4. complete one model turn, against the fake Ollama of
#      test/fakes/ollama/serve: building nodwyn costs nothing and needs no
#      key, no passphrase and no other machine;
#   5. replay its own log.
# The candidate runs on a temporary config and session store, so the user's
# config, keychain and sessions are neither read nor written.
set -euo pipefail
cd "$(dirname "$0")/.."
CAND=${1:-bin/nodwyn}
TMP=$(mktemp -d -t nodwyn-selfcheck.XXXXXX)
FAKE=
cleanup() {
	if [ -n "$FAKE" ]; then kill "$FAKE" 2>/dev/null || true; fi
	rm -rf "$TMP"
}
trap cleanup EXIT
OUT=$TMP/out

go build -o "$TMP/fakemodel" ./test/fakes/ollama/serve
"$TMP/fakemodel" -reply READY >"$TMP/model" &
FAKE=$!
for _ in $(seq 50); do
	grep -q '^MODEL ' "$TMP/model" && break
	sleep 0.1
done
MODEL_URL=$(awk '$1 == "MODEL" { print $2; exit }' "$TMP/model")
test -n "$MODEL_URL" || { echo "selfcheck: the fake model did not start"; exit 1; }

export XDG_CONFIG_HOME=$TMP/config XDG_DATA_HOME=$TMP/data
mkdir -p "$XDG_CONFIG_HOME/nodwyn"
printf '[control]\nprovider = "ollama"\nurl = "%s"\nmodel = "fake"\n' "$MODEL_URL" >"$XDG_CONFIG_HOME/nodwyn/config.toml"

echo "selfcheck: $CAND ($("$CAND" version))"
test "$("$CAND" eval -e '(+ 1 2)')" = "3"

# The dev tools come from .nodwyn/init.scm, which loads only once its revision
# is trusted (ADR 45). The record goes in the temporary config, so the user's
# trust is untouched; promote has refused a dirty tree, so the files trusted are
# the committed ones the candidate was built from.
REVISION=$("$CAND" trust --repo . | awk '$1 == "Revision:" { print $2; exit }')
test -n "$REVISION" || { echo "selfcheck: trust review showed no revision"; exit 1; }
"$CAND" trust --repo . --accept "$REVISION" >/dev/null

printf '%s\n' \
	':tools' \
	':eval (string-length (fs-read "README.md"))' \
	':eval (go-test "./internal/log")' \
	'Say that you are ready, in one word.' \
	':q' \
	| "$CAND" --no-confirm --no-keychain --repo . >"$OUT" 2>&1

grep -q '^go-test ' "$OUT" || { echo "selfcheck: dev tools not registered"; cat "$OUT"; exit 1; }
grep -q 'ok  \\tgithub.com/fclaude/nodwyn/internal/log' "$OUT" || { echo "selfcheck: go-test through the dev tool did not pass"; cat "$OUT"; exit 1; }
grep -q 'READY' "$OUT" || { echo "selfcheck: model turn did not complete"; cat "$OUT"; exit 1; }

SESSION=$(awk '$1 == "nodwyn" && $2 == "session" { print $3; exit }' "$OUT")
test -n "$SESSION" || { echo "selfcheck: session banner missing"; exit 1; }
"$CAND" replay "$SESSION" | grep 'READY' >/dev/null || { echo "selfcheck: replay of $SESSION failed"; exit 1; }
echo "selfcheck: ok (session $SESSION)"
