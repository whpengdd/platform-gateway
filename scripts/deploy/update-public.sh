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
bash "$HERE/standalone.sh" --mode docker --stack install --app-root "$APP_ROOT"
echo "public platform-gateway image built and recreated (--no-build in install compose)"
