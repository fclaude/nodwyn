#!/bin/bash
# live-models.sh: run the live suite (test/live, ADR 63) once for each model
# in NODWYN_LIVE_MODELS, one after the other, so the endpoint holds one model
# at a time. Each entry is a model name, or name@context to ask an Ollama
# model for that context (NODWYN_LIVE_NUM_CTX), separated by spaces:
#
#   NODWYN_LIVE_MODELS="qwen3.8-27b-160k:latest@163840 qwen3.8-flash-next-256k:latest@262144"
#
# A model whose suite fails is run again, up to NODWYN_LIVE_ATTEMPTS times in
# all (default 1), and passes when one run passes whole. Each run's report
# goes to $NODWYN_LIVE_REPORT/<n>-<model>-<attempt>. Every model runs whatever
# the one before it did, and the script fails when any model did not pass.
set -uo pipefail
models=${NODWYN_LIVE_MODELS:-}
if [ -z "$models" ]; then
	echo "live-models: NODWYN_LIVE_MODELS is not set" >&2
	exit 2
fi
base=${NODWYN_LIVE_REPORT:-${TMPDIR:-/tmp}/nodwyn-live-report}
attempts=${NODWYN_LIVE_ATTEMPTS:-1}
if ! [[ $attempts =~ ^[1-9]$ ]]; then
	echo "live-models: NODWYN_LIVE_ATTEMPTS is a number from 1 to 9" >&2
	exit 2
fi
failed=()
n=0
for entry in $models; do
	n=$((n + 1))
	model=${entry%@*}
	ctx=""
	if [ "$model" != "$entry" ]; then ctx=${entry##*@}; fi
	slug=$(printf '%s' "$model" | tr -c 'A-Za-z0-9._-' '-')
	echo "== live suite on $model, context ${ctx:-as the model says}"
	status=1
	for ((try = 1; try <= attempts && status != 0; try++)); do
		start=$(date +%s)
		NODWYN_LIVE_MODEL=$model NODWYN_LIVE_NUM_CTX=$ctx NODWYN_LIVE_REPORT="$base/$n-$slug-$try" make live
		status=$?
		echo "== $model, run $try of at most $attempts: exit $status after $(( ($(date +%s) - start) / 60 )) min"
	done
	if [ "$status" -ne 0 ]; then failed+=("$model"); fi
done
if [ "${#failed[@]}" -gt 0 ]; then
	echo "live-models: the suite failed on ${failed[*]}" >&2
	exit 1
fi
echo "live-models: the suite passed on every model"
