#!/usr/bin/env bash
# Build rag-explorer-ai-platform-gateway:<VERSION> from this repo. Prints the image name.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
IMAGE_REPO="${PLATFORM_GATEWAY_IMAGE_REPO:-rag-explorer-ai-platform-gateway}"
TAG="${PLATFORM_GATEWAY_TAG:-$(tr -d '[:space:]' < "$ROOT/VERSION")}"
[[ -n "$TAG" ]] || { echo "platform-gateway build-image: empty VERSION" >&2; exit 1; }
IMAGE="${IMAGE_REPO}:${TAG}"

cd "$ROOT"
docker build \
  --build-arg HTTP_PROXY="${HTTP_PROXY:-}" \
  --build-arg HTTPS_PROXY="${HTTPS_PROXY:-${HTTP_PROXY:-}}" \
  -t "$IMAGE" \
  -t "${IMAGE_REPO}:latest" \
  .
echo "$IMAGE"
