#!/usr/bin/env bash
# Manual smoke test against the real control plane (workshop). Not CI.
# Runs a two-turn session in the plain front-end with confirms denied, and
# then replays the log. Exit status is the session's.
set -euo pipefail
cd "$(dirname "$0")/.."
make -s build
printf '%s\n' \
	'List the repository root and tell me in one line what this project is.' \
	'Read docs/scheme.md and name three special forms it documents.' \
	':q' \
	| ./bin/nibb --no-confirm --repo . "$@"
echo
echo "=== replay ==="
./bin/nibb replay "$(./bin/nibb sessions --ids | tail -1)"
