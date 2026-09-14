# platform-gateway

Coremail cklogs 与项目范围 Jira 中转。Go 程序默认监听 :8091；宿主示例及容器宿主端口绑定 127.0.0.1:8091。浏览器和 nginx 公网不直连。

客户：

- rag-explorer-ai **self-service Runtime**：外网 token，只打 `/v1/cklogs/delivery|login|message`
- rag-explorer-ai **Worker / Factory**：内部 token，可打 `/v1/cklogs/analysis/*`

HTTP 合同见 [docs/api.md](docs/api.md)。未知字段 400。外网 token 打 `analysis/*` 返回 403 `token_scope_forbidden`。Kibana 凭据只活在本进程。

Zammad → Jira 的 [调用清单与安全设计](docs/jira-bridge-design.md) 和 [调用方 API 接口文档](docs/jira-api.md) 已整理：通过 [统一 token 配置文件](docs/gateway-auth.md) 管理 cklogs 两档权限与 Jira 项目授权（默认 `./config.json`，容器通过文件挂载提供），支持多 token、多项目；业务接口按项目隔离并支持可选 JQL 过滤，同步逻辑由调用方负责（本仓接口已实现；Zammad 适配与生产接入另行实施）。

本仓负责 **编镜像 / 编 151 二进制并拉起进程**。入站 token 由运维维护在 config.json；调用方仓库的旧 token 配对脚本未修改，新版忽略残留旧应用变量，应用设置只读 JSON。

镜像名保持 `rag-explorer-ai-platform-gateway:<VERSION>`（`VERSION` 文件）。rag-explorer-ai compose 钉这个 tag，不再 `build` 本仓源码。

## 开发

Go 1.23 或更新版本。无第三方依赖。

```bash
export PATH="$HOME/sdk/go/bin:$PATH"   # 或系统 go1.23
go test ./...
go build -buildvcs=false -o bin/platform-gateway ./cmd/gateway
```

先按 [完整配置说明](docs/gateway-auth.md) 准备 config.json 和可写审计挂载目录。本地 Docker（本仓可以 `--build`）：

```bash
docker compose up -d --build
curl -sf http://127.0.0.1:8091/health
```

8091 必须 loopback。不要映射 `0.0.0.0:8091`。

## Kibana 并发与排队

所有 `/v1/cklogs/*` 接口（包括自助和内部 `analysis/*`）共用一个进程内 FIFO 队列。每个执行中的业务请求占用一个槽，其内部 Kibana HTTP 调用串行执行，因此同时调用 Kibana 的数量不会超过配置上限。

在 `config.json` 的 `cklogs.queue` 中配置：

```json
{"maxConcurrency": 2, "size": 32, "waitTimeoutMs": 30000}
```

`maxConcurrency` 必须 >= 1；`size` 可以为 0，表示无等待位；`waitTimeoutMs` 是正整数毫秒。只有缺失字段采用上述默认值，显式非法值启动失败。请求取消时退出队列；执行超时从取得槽后开始计算。

队列满返回 HTTP 503 `gateway_busy`，附带 `Retry-After`；排队超时返回 HTTP 503 `gateway_queue_timeout`。文件修改后重启进程或 `docker compose up -d --force-recreate --no-deps platform-gateway`，无需重新构建镜像。

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

# 151 裸机：go build + 拉起二进制（env 来自 app-root/.env.local）
bash scripts/deploy/update-151.sh --app-root /path/to/rag-explorer-ai

# 149：编镜像，再在 app-root 的 intranet compose 里 --no-build 起 rag-explorer-platform-gateway
bash scripts/deploy/update-149.sh --app-root /path/to/rag-explorer-ai

# 公网 63：编镜像，再在 install compose profile 里 --no-build 起 platform-gateway
bash scripts/deploy/update-public.sh --app-root /path/to/rag-explorer-ai
```

健康检查：`curl -sf --noproxy '*' http://127.0.0.1:8091/health` 和 `/ready`。

## Jira 与文件配置

启动前按 [配置示例](docs/gateway-auth.example.json) 准备 config.json，替换占位符，并确保 UID 65532 可读。默认读取 ./config.json；容器工作目录 /，文件只读挂载为 /config.json。只有启用的服务才要求相应上游凭据；残留旧应用环境变量不参与配置或授权，也不阻止启动。

修改宿主文件后执行 docker compose up -d --force-recreate --no-deps platform-gateway，重新绑定当前文件，无需重建镜像。裸机默认 app-root/config.json，可用 GATEWAY_CONFIG_FILE 或 standalone 的 --config-file 覆盖。app-stack 脚本通过临时 Compose override 挂载文件，不修改调用方仓库。

Jira 使用独立的 2 执行槽、32 等待位 FIFO，排队及操作各 30 秒。每项目读取 60 次/分钟、写入 20 次/分钟、创建 100 次/UTC日、上传 200 MiB/UTC日。额度按进程计算，同项目多 token 共享，重启清零。同步、业务绑定和不确定写入的恢复由调用方负责。

验证记录见 [jira-validation.md](docs/jira-validation.md)。没有连接生产 Jira，没有完成调用方适配或生产部署。
