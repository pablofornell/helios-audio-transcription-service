#!/usr/bin/env bash
# fetch-models.sh downloads whisper.cpp ggml model weights into ./models,
# the directory HELIOS_TRANSCRIBER_MODEL_DIR points at by default (see
# deploy/.env.example). Usage:
#
#   ./scripts/fetch-models.sh [model ...]
#
# With no arguments, it fetches "large-v3" -- HELIOS_TRANSCRIBER_DEFAULT_MODEL's
# own default -- so a fresh checkout has something to transcribe with. Any
# number of model names may be given instead, each one of
# HELIOS_TRANSCRIBER_ALLOWED_MODELS' default set (tiny, base, small, medium,
# large-v3) or any other name ggerganov/whisper.cpp publishes a
# ggml-<model>.bin for.
set -euo pipefail

# models_dir is relative to this script's own location, not the caller's
# working directory, so `make models` and a direct
# `./scripts/fetch-models.sh` invocation from anywhere both land the
# weights in the same ./models next to go.mod.
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
models_dir="${script_dir}/../models"

# Official ggml model URLs, verified against
# https://huggingface.co/ggerganov/whisper.cpp/tree/main.
base_url="https://huggingface.co/ggerganov/whisper.cpp/resolve/main"

models=("$@")
if [ ${#models[@]} -eq 0 ]; then
	models=("large-v3")
fi

mkdir -p "${models_dir}"

total=${#models[@]}
n=0
for model in "${models[@]}"; do
	n=$((n + 1))
	dest="${models_dir}/ggml-${model}.bin"

	if [ -s "${dest}" ]; then
		echo "[${n}/${total}] ggml-${model}.bin already present at ${dest}, skipping"
		continue
	fi

	echo "[${n}/${total}] downloading ggml-${model}.bin ..."
	tmp="${dest}.part"
	# -L follows the redirect huggingface.co issues to its CDN; --fail
	# turns a 404 (e.g. a typo'd model name) into a non-zero exit instead
	# of silently writing an HTML error page as the model file.
	curl -fL --progress-bar -o "${tmp}" "${base_url}/ggml-${model}.bin"

	if [ ! -s "${tmp}" ]; then
		echo "[${n}/${total}] download of ggml-${model}.bin produced an empty file" >&2
		rm -f "${tmp}"
		exit 1
	fi

	mv "${tmp}" "${dest}"
	echo "[${n}/${total}] ggml-${model}.bin ready at ${dest}"
done

echo "done: ${total} model(s) available in ${models_dir}"
