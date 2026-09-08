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
