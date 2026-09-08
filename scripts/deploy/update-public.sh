#!/usr/bin/env bash
# Public-63: build image in this repo, then install compose --no-build.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
APP_ROOT="${APP_ROOT:-}"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --app-root) APP_ROOT="${2:-}"; shift 2 ;;
    *) echo "usage: update-public.sh --app-root DIR" >&2; exit 2 ;;
  esac
done
[[ -n "$APP_ROOT" ]] || APP_ROOT="${PLATFORM_GATEWAY_APP_ROOT:-}"
[[ -n "$APP_ROOT" ]] || { echo "update-public.sh: --app-root required (rag-explorer-ai checkout)" >&2; exit 1; }

load_build_proxy() {
  local file key line value
  for file in "$APP_ROOT/install/.env" "$APP_ROOT/.env" "$APP_ROOT/.env.local"; do
    [[ -f "$file" ]] || continue
    for key in BUILD_HTTPS_PROXY HTTPS_PROXY HTTP_PROXY; do
      line="$(grep -E "^${key}=" "$file" | tail -1 || true)"
      [[ -n "$line" ]] || continue
      value="${line#*=}"
      value="${value%$'\r'}"
      [[ -n "$value" ]] || continue
      export HTTP_PROXY="$value" HTTPS_PROXY="$value" http_proxy="$value" https_proxy="$value" BUILD_HTTPS_PROXY="$value"
      echo "compose build proxy: $key from env file (value not printed)"
      return 0
    done
  done
  return 0
}
load_build_proxy

bash "$HERE/standalone.sh" --mode docker --stack install --app-root "$APP_ROOT"
echo "public platform-gateway image built and recreated (--no-build in install compose)"
