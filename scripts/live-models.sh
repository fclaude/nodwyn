#!/bin/bash
# live-models.sh: run the live suite (test/live, ADR 63) once for each model
# in NODWYN_LIVE_MODELS, one after the other, so the endpoint holds one model
# at a time. Each entry is a model name, or name@context to ask an Ollama
# model for that context (NODWYN_LIVE_NUM_CTX), separated by spaces:
#
#   NODWYN_LIVE_MODELS="qwen3.8-27b-160k:latest@163840 qwen3.8-flash-next-256k:latest@262144"
#
# Each model's report goes to $NODWYN_LIVE_REPORT/<n>-<model>. Every model
# runs whatever the one before it did, and the script fails when any did.
set -uo pipefail
models=${NODWYN_LIVE_MODELS:-}
if [ -z "$models" ]; then
	echo "live-models: NODWYN_LIVE_MODELS is not set" >&2
	exit 2
fi
base=${NODWYN_LIVE_REPORT:-${TMPDIR:-/tmp}/nodwyn-live-report}
failed=()
n=0
for entry in $models; do
	n=$((n + 1))
	model=${entry%@*}
	ctx=""
	if [ "$model" != "$entry" ]; then ctx=${entry##*@}; fi
	slug=$(printf '%s' "$model" | tr -c 'A-Za-z0-9._-' '-')
	echo "== live suite on $model, context ${ctx:-as the model says}"
	start=$(date +%s)
	NODWYN_LIVE_MODEL=$model NODWYN_LIVE_NUM_CTX=$ctx NODWYN_LIVE_REPORT="$base/$n-$slug" make live
	status=$?
	echo "== $model: exit $status after $(( ($(date +%s) - start) / 60 )) min"
	if [ "$status" -ne 0 ]; then failed+=("$model"); fi
done
if [ "${#failed[@]}" -gt 0 ]; then
	echo "live-models: the suite failed on ${failed[*]}" >&2
	exit 1
fi
echo "live-models: the suite passed on every model"
