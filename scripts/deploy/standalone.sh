#!/usr/bin/env bash
# Build and start platform-gateway from this source repo.
#
#   standalone.sh --mode host --app-root DIR
#   standalone.sh --mode docker --stack self
#   standalone.sh --mode docker --stack install|149 --app-root DIR
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
MODE=""
STACK="self"
APP_ROOT=""
ENV_FILE=""
AUTH_FILE="${GATEWAY_AUTH_FILE:-}"
LISTEN="127.0.0.1:8091"
HEALTH_URL="http://127.0.0.1:8091/health"
READY_URL="http://127.0.0.1:8091/ready"

usage() {
  echo "usage: standalone.sh --mode host|docker [--stack self|install|149] [--app-root DIR] [--env-file FILE]" >&2
  exit 2
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --mode) MODE="${2:-}"; shift 2 ;;
    --stack) STACK="${2:-}"; shift 2 ;;
    --app-root) APP_ROOT="${2:-}"; shift 2 ;;
    --auth-file) AUTH_FILE="${2:-}"; shift 2 ;;
    --env-file) ENV_FILE="${2:-}"; shift 2 ;;
    --listen) LISTEN="${2:-}"; shift 2 ;;
    -h|--help) usage ;;
    *) echo "unknown arg: $1" >&2; usage ;;
  esac
done

die() { echo "platform-gateway standalone: $*" >&2; exit 1; }

[[ "$MODE" == "host" || "$MODE" == "docker" ]] || usage
[[ "$STACK" == "self" || "$STACK" == "install" || "$STACK" == "149" ]] \
  || die "stack must be self|install|149"
[[ "$LISTEN" == 127.0.0.1:* || "$LISTEN" == localhost:* ]] \
  || die "LISTEN must be loopback, got $LISTEN"

resolve_go() {
  if command -v go >/dev/null 2>&1; then
    command -v go
    return
  fi
  for candidate in "$HOME/sdk/go/bin/go" /usr/local/go/bin/go /opt/homebrew/bin/go; do
    if [[ -x "$candidate" ]]; then
      echo "$candidate"
      return
    fi
  done
  return 1
}

wait_http() {
  local url="$1" label="$2" i
  for i in $(seq 1 30); do
    if curl -sf --noproxy '*' --max-time 3 "$url" >/dev/null 2>&1; then
      echo "ok $label"
      return 0
    fi
    sleep 2
  done
  die "$label failed ($url)"
}

require_app_root() {
  [[ -n "$APP_ROOT" ]] || die "--app-root is required for stack=$STACK (rag-explorer-ai checkout)"
  [[ -d "$APP_ROOT" ]] || die "app-root not a directory: $APP_ROOT"
}

image_name() {
  local tag
  tag="$(tr -d '[:space:]' < "$ROOT/VERSION")"
  echo "${PLATFORM_GATEWAY_IMAGE_REPO:-rag-explorer-ai-platform-gateway}:${tag}"
}

start_host() {
  require_app_root
  ENV_FILE="${ENV_FILE:-$APP_ROOT/.env.local}"
  AUTH_FILE="${AUTH_FILE:-$APP_ROOT/config.json}"
  [[ -f "$AUTH_FILE" && -r "$AUTH_FILE" ]] || die "readable authorization file required"
  mkdir -p "$APP_ROOT/logs/platform-gateway" "$ROOT/bin"
  local go_bin=""
  if go_bin="$(resolve_go)"; then
    echo "building bin/platform-gateway"
    ( cd "$ROOT" && "$go_bin" build -buildvcs=false -o bin/platform-gateway ./cmd/gateway )
  else
    echo "no go toolchain; extracting binary from docker image"
    local image
    image="$(bash "$HERE/build-image.sh")"
    local cid
    cid="$(docker create "$image")"
    docker cp "$cid:/platform-gateway" "$ROOT/bin/platform-gateway"
    docker rm "$cid" >/dev/null
    chmod +x "$ROOT/bin/platform-gateway"
  fi

  local pid_file="$APP_ROOT/gateway.pid"
  if [[ -f "$pid_file" ]]; then
    kill "$(cat "$pid_file")" >/dev/null 2>&1 || true
    rm -f "$pid_file"
    sleep 1
  fi
  local pid
  pid="$(node "$HERE/run-host.mjs" \
    --app-root "$APP_ROOT" \
    --gateway-root "$ROOT" \
    --env-file "$ENV_FILE" \
    --listen "$LISTEN" \
    --auth-file "$AUTH_FILE" \
    --binary "$ROOT/bin/platform-gateway")"
  echo "$pid" > "$pid_file"
  echo "started host gateway pid=$pid listen=$LISTEN"
  wait_http "$HEALTH_URL" "/health"
  wait_http "$READY_URL" "/ready"
}

start_docker_self() {
  export PLATFORM_GATEWAY_AUTH_HOST_FILE="${AUTH_FILE:-$ROOT/config.json}"
  [[ -f "$PLATFORM_GATEWAY_AUTH_HOST_FILE" && -r "$PLATFORM_GATEWAY_AUTH_HOST_FILE" ]] || die "readable authorization file required"
  mkdir -p "$ROOT/logs"
  docker compose --project-directory "$ROOT" -f "$ROOT/docker-compose.yml" up -d --build
  echo "started docker platform-gateway stack=self"
  wait_http "$HEALTH_URL" "/health"
}

start_docker_app() {
  require_app_root
  local image
  image="$(bash "$HERE/build-image.sh")"
  AUTH_FILE="${AUTH_FILE:-$APP_ROOT/config.json}"
  [[ -f "$AUTH_FILE" && -r "$AUTH_FILE" ]] || die "readable authorization file required"
  local overlay
  overlay="$(mktemp)"
  trap 'rm -f "$overlay"' RETURN
  node "$HERE/compose-auth.mjs" "$STACK" "$AUTH_FILE" > "$overlay"
  export PLATFORM_GATEWAY_IMAGE="$image"
  echo "PLATFORM_GATEWAY_IMAGE=$PLATFORM_GATEWAY_IMAGE"
  mkdir -p "$APP_ROOT/logs/platform-gateway"
  case "$STACK" in
    install)
      local compose="$APP_ROOT/install/docker-compose.yml"
      [[ -f "$compose" ]] || die "missing $compose"
      PLATFORM_GATEWAY_IMAGE="$image" docker compose --project-directory "$APP_ROOT/install" \
        -f "$compose" -f "$overlay" --profile platform-gateway \
        up -d --no-build --force-recreate --no-deps platform-gateway
      ;;
    149)
      local compose="$APP_ROOT/install/docker-compose.intranet-149.yml"
      [[ -f "$compose" ]] || die "missing $compose"
      PLATFORM_GATEWAY_IMAGE="$image" docker compose --project-directory "$APP_ROOT" \
        --project-name rag-explorer-ai \
        -f "$compose" -f "$overlay" --profile platform-gateway \
        up -d --no-build --force-recreate --no-deps rag-explorer-platform-gateway
      ;;
  esac
  echo "started docker platform-gateway stack=$STACK image=$image"
  wait_http "$HEALTH_URL" "/health"
}

case "$MODE" in
  host) start_host ;;
  docker)
    if [[ "$STACK" == "self" ]]; then start_docker_self
    else start_docker_app
    fi
    ;;
esac
