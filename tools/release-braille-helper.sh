#!/usr/bin/env bash
# Build the standalone asset with the same pinned Liblouis as the runtime.
set -euo pipefail
out=${1:?usage: tools/release-braille-helper.sh OUTPUT_DIRECTORY}
repo=$(cd "$(dirname "$0")/.." && pwd)
mkdir -p "$out"
docker buildx build --platform linux/amd64 --target asset \
  --output "type=local,dest=$out" "$repo/tools/braille-map"
