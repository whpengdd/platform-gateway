# Platform Gateway 接口

本文记录已实现的 cklogs 接口。认证使用 [统一 token 配置文件](gateway-auth.md)，同时管理 cklogs external/internal 和 Jira 项目授权；Jira 接口见 [jira-api.md](jira-api.md)。cklogs 路径及两档权限保持兼容。

日志中转层（Go，默认 `127.0.0.1:8091`）。浏览器、前端、Chat BFF **都不直连**。公网自助由 rag-explorer-ai 的 **self-service Runtime** 用外网 token 调；149 Worker / Factory 用内部 token 调 `analysis/*`。

鉴权：`Authorization: Bearer <token>`。无 token / 错 token → 401。`GET /health`、`GET /ready` 不鉴权。

Token 使用不透明 Bearer 值。网关从工作目录 ./config.json 读取，GATEWAY_AUTH_FILE 可覆盖路径。调用方仍从自己的环境变量发送原 Bearer 值。

## 0. 凭据与迁移

用 openssl rand -hex 32 分别生成 external/internal 凭据，写为文件中的独立条目。重复 token 启动失败，不再采用 internal 优先。

| 角色 | 配置位置 |
|---|---|
| gateway external | tokens 中的 cklogs=external 条目 |
| gateway internal | tokens 中的 cklogs=internal 条目 |
| 自助 Runtime | PLATFORM_GATEWAY_TOKEN，保持与 external 条目值相同 |
| Worker / Factory | PLATFORM_GATEWAY_INTERNAL_TOKEN，保持与 internal 条目值相同 |

完整 JSON 与步骤见 [迁移文档](gateway-auth.md)。旧 GATEWAY_TOKEN_EXTERNAL、GATEWAY_TOKEN_INTERNAL、GATEWAY_AUTH_TOKENS 必须从网关环境删除；旧 AUTH_TOKENS 每个值迁为 external。调用方变量名和值可以保留。

裸机默认读取 app-root/config.json，容器工作目录为 /，只读挂载宿主文件至 /config.json。确保 UID 65532 可读。修改后执行 docker compose up -d --force-recreate --no-deps platform-gateway；裸机重启进程。文件不提交 Git、不进入镜像。调用方仓库的旧 token 配对脚本未修改。

### 校验自己拿到的是哪一档

```bash
export GW=http://127.0.0.1:8091
export EXT='<外网 token>'
export INT='<内部 token>'

curl -sS "$GW/health"

# 外网 token 打自助接口 → 200 或业务 4xx，不是 401
curl -sS -o /dev/null -w '%{http_code}\n' -X POST "$GW/v1/cklogs/delivery" \
  -H "Authorization: Bearer $EXT" -H 'Content-Type: application/json' \
  -d '{"direction":"outbound","account":"a@example.com","timeRange":{"from":"2026-08-01T00:00:00Z","to":"2026-08-02T00:00:00Z"}}'

# 外网 token 打客服接口 → 403 token_scope_forbidden
curl -sS -X POST "$GW/v1/cklogs/analysis/delivery" \
  -H "Authorization: Bearer $EXT" -H 'Content-Type: application/json' \
  -d '{"domain":"example.com","timeRange":{"from":"2026-08-01T00:00:00Z","to":"2026-08-02T00:00:00Z"}}'

# 内部 token 打客服接口 → 200（或平台 0 命中，不是 403）
curl -sS -X POST "$GW/v1/cklogs/analysis/delivery" \
  -H "Authorization: Bearer $INT" -H 'Content-Type: application/json' \
  -d '{"domain":"example.com","timeRange":{"from":"2026-08-01T00:00:00Z","to":"2026-08-02T00:00:00Z"}}'
```

| HTTP | 含义 |
|---|---|
| 401 `unauthorized` | 没带 Bearer，或 token 不在 gateway 名单 |
| 403 `token_scope_forbidden` | token 有效但是外网档，不能打 `analysis/*` |
| 403 `forbidden` | 配了 `GATEWAY_ALLOW_CIDRS` 且来源 IP 不在范围内 |
| 400 | JSON 字段不在白名单，或缺 `account` / `tid` 等 |
| 503 `gateway_busy` | 共享 Kibana 队列已满，附带 `Retry-After`（秒） |
| 503 `gateway_queue_timeout` | 等待共享 Kibana 队列超时 |

自助与内部分析接口共享并发上限和 FIFO 等待队列。容器环境变量 `CKLOGS_MAX_CONCURRENCY`（默认 2）、`CKLOGS_QUEUE_SIZE`（默认 32）、`CKLOGS_QUEUE_WAIT_MS`（默认 30000）控制并发数、等待容量和最长排队时间。请求取消时退出队列，执行超时从取得并发槽后起算；多容器分别计数。

---

## 1. 端点

### 不鉴权

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/health` | 进程活着 |
| GET | `/ready` | token 名单 + Kibana 账号都配了才 200 |

### 外网 token（自助）

| 方法 | 路径 | 必填 | 选填 | 限制 |
|---|---|---|---|---|
| POST | `/v1/cklogs/delivery` | `direction`=`inbound`\|`outbound`，`account`=完整邮箱 | `peer`（邮箱或域名）、`subject`、`timeRange`、`page`、`pageSize`、`countOnly` | 必须有账号；不能 `both`；不能只拿域名扫全平台；窗口 ≤30 天；pageSize ≤500；禁止未知字段 |
| POST | `/v1/cklogs/login` | `account` | `clientIp`、`timeRange`、`countOnly` | 必须有账号；窗口 ≤30 天 |
| POST | `/v1/cklogs/message` | `tid` | `timeRange` | 必须先有 tid；窗口 ≤30 天 |

`delivery`：`account` 是被查邮箱，`peer` 是对方。inbound = account 当收件人，outbound = account 当发件人。`countOnly` 只做 `size:0` 计数、不拉明细：先 DATRANS，零命中再 MTA；inbound 仍为零再查 PROXY。

内部 token **也可以**调这三条（内部是超集）。

### 内部 token（客服 Factory）

| 方法 | 路径 | 对应工具 | 条件 |
|---|---|---|---|
| POST | `/v1/cklogs/analysis/delivery` | `query_delivery_log` | `direction` 可 `both`；`sender` / `recipient` / `domain` / `subject` / `msgId` / `timeRange` / `countOnly`。不要求账号，不校验授权域。`countOnly` 只返回 hits.total，不拉明细 |
| POST | `/v1/cklogs/analysis/auth` | `query_auth_log` | `account`、`protocol`=`pop3`\|`smtp`\|`webmail`\|`imap`、`timeRange`、`countOnly`。imap → `not_available` |
| POST | `/v1/cklogs/analysis/ops` | `query_mail_ops_log` | `account`、`protocol`=`webmail`\|`pop3`\|`imap`、`op`、`timeRange`、`countOnly` |
| POST | `/v1/cklogs/message` | `query_delivery_trace` | 与自助同一 URL |

内部 token 仍禁止：raw DSL、index 名、把 `eml` 原文塞进 body。窗口/pageSize 硬顶是护 Kibana，不是租户授权。

请求一律 `DisallowUnknownFields`：多一个字段就是 400 `unknown_field`。

---

## 2. 调用示例

时间窗用 ISO-8601。`to` 不能晚于现在。

```bash
curl -sS -X POST "$GW/v1/cklogs/delivery" \
  -H "Authorization: Bearer $EXT" -H 'Content-Type: application/json' \
  -d '{
    "direction": "outbound",
    "account": "user@example.com",
    "timeRange": { "from": "2026-08-24T00:00:00Z", "to": "2026-08-26T00:00:00Z" },
    "page": 1,
    "pageSize": 20
  }'
```

可选头 `X-Request-Id` 会写进 JSONL，便于和 Runtime 日志对上。

---

## 3. 日志

每日文件：`$GATEWAY_LOG_DIR/platform-gateway-YYYY-MM-DD.jsonl`（日期按 Asia/Shanghai）。

有 `operation`、`token_class`（`external`\|`internal`）、白名单条件、每次 Kibana 调用耗时。不写 token 明文、不写命中行正文、不写数据库。

---

## 4. 谁不该拿 key

- 浏览器 / 前端 / nginx 公网
- 客户 SSO / 登录 JWT（那是 rag-explorer-ai Node `scope.mjs`）
- 63 公网 self-service 机器：只配外网 token，不要授权 internal 条目或配置调用方 PLATFORM_GATEWAY_INTERNAL_TOKEN
