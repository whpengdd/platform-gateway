#!/usr/bin/env bash
# 151 host binary: build this repo and start against rag-explorer-ai env.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
APP_ROOT="${APP_ROOT:-}"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --app-root) APP_ROOT="${2:-}"; shift 2 ;;
    *) echo "usage: update-151.sh --app-root DIR" >&2; exit 2 ;;
  esac
done
[[ -n "$APP_ROOT" ]] || APP_ROOT="${PLATFORM_GATEWAY_APP_ROOT:-}"
if [[ -z "$APP_ROOT" && -d "$(cd "$HERE/../../../rag-explorer-ai" 2>/dev/null && pwd)" ]]; then
  APP_ROOT="$(cd "$HERE/../../../rag-explorer-ai" && pwd)"
fi
[[ -n "$APP_ROOT" ]] || { echo "update-151.sh: --app-root required (rag-explorer-ai checkout)" >&2; exit 1; }
bash "$HERE/standalone.sh" --mode host --app-root "$APP_ROOT"
echo "151 platform-gateway updated from source repo"
