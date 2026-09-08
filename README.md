# platform-gateway

Coremail cklogs 中转。Go 服务，默认只绑 `127.0.0.1:8091`。浏览器和 nginx 公网不直连。

客户：

- rag-explorer-ai **self-service Runtime**：外网 token，只打 `/v1/cklogs/delivery|login|message`
- rag-explorer-ai **Worker / Factory**：内部 token，可打 `/v1/cklogs/analysis/*`

HTTP 合同见 [docs/api.md](docs/api.md)。未知字段 400。外网 token 打 `analysis/*` 返回 403 `token_scope_forbidden`。Kibana 凭据只活在本进程。

本仓负责 **编镜像 / 编 151 二进制并拉起进程**。Token 生成和配对仍在 rag-explorer-ai 的 `scripts/deploy/platform-gateway/ensure-tokens.mjs`。

镜像名保持 `rag-explorer-ai-platform-gateway:<VERSION>`（`VERSION` 文件）。rag-explorer-ai compose 钉这个 tag，不再 `build` 本仓源码。

## 开发

Go 1.23。无第三方依赖。

```bash
export PATH="$HOME/sdk/go/bin:$PATH"   # 或系统 go1.23
go test ./...
go build -o bin/platform-gateway ./cmd/gateway
```

本地 Docker（本仓可以 `--build`）：

```bash
docker compose up -d --build
curl -sf http://127.0.0.1:8091/health
```

8091 必须 loopback。不要映射 `0.0.0.0:8091`。

## Kibana 并发与排队

所有 `/v1/cklogs/*` 接口（包括自助和内部 `analysis/*`）共用一个进程内 FIFO 队列。每个执行中的业务请求占用一个槽，其内部 Kibana HTTP 调用串行执行，因此同时调用 Kibana 的数量不会超过配置上限。

在本仓 `.env` 中配置，Docker Compose 会将以下环境变量传入容器：

```dotenv
CKLOGS_MAX_CONCURRENCY=2
CKLOGS_QUEUE_SIZE=32
CKLOGS_QUEUE_WAIT_MS=30000
```

- `CKLOGS_MAX_CONCURRENCY`：共享并发上限，默认 2，必须大于 0。
- `CKLOGS_QUEUE_SIZE`：额外等待的请求数，默认 32；设为 0 时无空闲槽即拒绝。
- `CKLOGS_QUEUE_WAIT_MS`：最长排队时间（毫秒），默认 30000，必须大于 0。请求取消时退出队列；执行超时从取得槽后开始计算。

队列满返回 HTTP 503 `gateway_busy`，附带 `Retry-After`；排队超时返回 HTTP 503 `gateway_queue_timeout`。这三个变量为空时使用默认值，非法值会导致启动失败。

修改配置后运行 `docker compose up -d --build`，重建并应用环境变量。直接使用 `docker run` 时通过 `-e CKLOGS_MAX_CONCURRENCY=2` 等参数传入；使用其他仓库的 Compose 部署时，也需在对应服务中传入这些变量。

限制按容器/进程独立计算：多个实例的总并发上限为各实例配置之和。

## 发版

1. 改 Go 或 Dockerfile。
2.  bump `VERSION`（semver，例如 `1.0.1`）。
3. `bash scripts/deploy/build-image.sh` 打出 `rag-explorer-ai-platform-gateway:<VERSION>`。
4. 在目标机拉起（见下）。
5. rag-explorer-ai 把 compose 默认 tag / `PLATFORM_GATEWAY_IMAGE` 钉到同一版本，再 `up --no-build`。

Chat `v2-release` 不再编译本仓。只有 rag-explorer-ai 里钉的镜像 tag 变了，才会 recreate 容器。

## 部署

脚本不打印 token 明文。`--app-root` 指向 rag-explorer-ai 工作区（compose、`.env.local`、日志目录）。

```bash
# 只编镜像，stdout 最后一行是 image tag
bash scripts/deploy/build-image.sh

# 151 裸机：go test + go build + 拉起二进制（env 来自 app-root/.env.local）
bash scripts/deploy/update-151.sh --app-root /path/to/rag-explorer-ai

# 149：编镜像，再在 app-root 的 intranet compose 里 --no-build 起 rag-explorer-platform-gateway
bash scripts/deploy/update-149.sh --app-root /path/to/rag-explorer-ai

# 公网 63：编镜像，再在 install compose profile 里 --no-build 起 platform-gateway
bash scripts/deploy/update-public.sh --app-root /path/to/rag-explorer-ai
```

健康检查：`curl -sf --noproxy '*' http://127.0.0.1:8091/health` 和 `/ready`。
