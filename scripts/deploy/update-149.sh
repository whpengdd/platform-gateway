#!/usr/bin/env bash
# 149: build image in this repo, then compose --no-build in rag-explorer-ai.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
APP_ROOT="${APP_ROOT:-}"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --app-root) APP_ROOT="${2:-}"; shift 2 ;;
    *) echo "usage: update-149.sh --app-root DIR" >&2; exit 2 ;;
  esac
done
[[ -n "$APP_ROOT" ]] || APP_ROOT="${PLATFORM_GATEWAY_APP_ROOT:-}"
if [[ -z "$APP_ROOT" && -d /home/ubuntu/rag-explorer-ai ]]; then
  APP_ROOT=/home/ubuntu/rag-explorer-ai
fi
[[ -n "$APP_ROOT" ]] || { echo "update-149.sh: --app-root required (rag-explorer-ai checkout)" >&2; exit 1; }
bash "$HERE/standalone.sh" --mode docker --stack 149 --app-root "$APP_ROOT"
echo "149 platform-gateway image built and recreated (--no-build in app compose)"
