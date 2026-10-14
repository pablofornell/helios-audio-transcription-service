#!/usr/bin/env bash
# Check AMD Vulkan access as the service user. Optionally transcribe a
# readable mono 16 kHz WAV to prove that whisper-cli uses the Vulkan backend.
# Usage: ./scripts/check-vulkan.sh [audio.wav]
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_dir="$(cd "${script_dir}/.." && pwd)"
compose=(docker compose -f "${repo_dir}/deploy/docker-compose.yml"
  -f "${repo_dir}/deploy/docker-compose.vulkan.yml")

if (( $# > 1 )); then
  echo "Usage: $0 [mono-16kHz.wav]" >&2
  exit 2
fi

echo '[1/3] Checking Vulkan Compose configuration'
"${compose[@]}" config --quiet

echo '[2/3] Checking RADV as the unprivileged service user'
"${compose[@]}" run --rm --no-deps --entrypoint /bin/sh helios -eu -c '
  vulkaninfo --summary > /tmp/vulkan-summary.txt 2>/tmp/vulkan-errors.txt || {
    cat /tmp/vulkan-errors.txt >&2
    exit 1
  }
  cat /tmp/vulkan-summary.txt
  grep -q DRIVER_ID_MESA_RADV /tmp/vulkan-summary.txt || {
    echo "No AMD RADV device found; check the render node and group ID." >&2
    exit 1
  }
'

if (( $# == 0 )); then
  echo '[3/3] RADV access verified; pass a WAV file to verify actual transcription'
  exit 0
fi

audio_path="$(realpath -- "$1")"
if [[ ! -f "${audio_path}" || ! -r "${audio_path}" ]]; then
  echo "Audio must be a readable file: ${audio_path}" >&2
  exit 1
fi

echo '[3/3] Transcribing with whisper-cli and checking Vulkan backend selection'
# Variables in this command must expand inside the container.
# shellcheck disable=SC2016
"${compose[@]}" run --rm --no-deps \
  --volume "${audio_path}:/vulkan-check.wav:ro" --entrypoint /bin/sh helios -eu -c '
  whisper-cli -m "/models/ggml-${HELIOS_TRANSCRIBER_DEFAULT_MODEL:-large-v3}.bin" \
    -f /vulkan-check.wav -t "${HELIOS_TRANSCRIBER_THREADS:-4}" \
    --device "${HELIOS_TRANSCRIBER_GPU_DEVICE:-0}" \
    -l auto -oj -of /tmp/vulkan-check > /tmp/whisper-stdout.txt 2>/tmp/whisper-stderr.txt || {
      cat /tmp/whisper-stderr.txt >&2
      exit 1
    }
  cat /tmp/whisper-stderr.txt
  grep -Eq "using Vulkan[0-9]+ backend" /tmp/whisper-stderr.txt || {
    echo "Transcription did not select Vulkan; CPU fallback is not a passing check." >&2
    exit 1
  }
  if grep -Eq "failed to initialize Vulkan[0-9]+ backend" /tmp/whisper-stderr.txt; then
    echo "Vulkan initialization failed; CPU fallback is not a passing check." >&2
    exit 1
  fi
  test -s /tmp/vulkan-check.json
  cat /tmp/vulkan-check.json
  echo "AMD Vulkan transcription verified."
'
