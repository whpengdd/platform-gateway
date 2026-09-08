# Platform Gateway 接口

日志中转层（Go，默认 `127.0.0.1:8091`）。浏览器、前端、Chat BFF **都不直连**。公网自助由 rag-explorer-ai 的 **self-service Runtime** 用外网 token 调；149 Worker / Factory 用内部 token 调 `analysis/*`。

鉴权：`Authorization: Bearer <token>`。无 token / 错 token → 401。`GET /health`、`GET /ready` 不鉴权。

Token 不是登录 JWT，也不是 Kibana Basic。是本机生成的不透明字符串，写进 gateway 进程和 rag-explorer-ai 的 `.env.local`，两边必须对上。

---

## 0. Key 怎么获取

没有后台「申请 key」页面。自己生成两把，分别给外网自助和内部客服。

```bash
openssl rand -hex 32
```

两把 **不要相同**。相同的话 gateway 会当成内部档，外网机器一旦被打穿就能打 `analysis/*`。

生成后只写环境变量，**不要提交 git，不要写进这篇文档的示例值**。

### 写到哪

| 角色 | 进程环境变量 | 值 |
|---|---|---|
| gateway 认外网档 | `GATEWAY_TOKEN_EXTERNAL`（可逗号多个） | 第一把 |
| gateway 认内部档 | `GATEWAY_TOKEN_INTERNAL`（可逗号多个） | 第二把 |
| 自助 Runtime（`rag-self-service`） | `PLATFORM_GATEWAY_TOKEN` + `PLATFORM_GATEWAY_BASE_URL=http://rag-platform-gateway:8091` | **必须等于**外网那把 |
| Worker / Factory | `PLATFORM_GATEWAY_INTERNAL_TOKEN` + 同一个 `BASE_URL` | **必须等于**内部那把 |

兼容：如果 `GATEWAY_TOKEN_EXTERNAL` 和 `GATEWAY_TOKEN_INTERNAL` 都空，旧变量 `GATEWAY_AUTH_TOKENS` 里的全部 token **一律当外网档**（不能打 `analysis/*`）。63 公网机器只应持有外网 token。

Kibana 账号 `CK_LOGS_BASIC_USER` / `PASS` 只活在 gateway 容器/进程里，**不要**写进自助 Runtime 的 `.env.local`。

Token 配对脚本仍在 rag-explorer-ai：`scripts/deploy/platform-gateway/ensure-tokens.mjs`。

### 151 裸机（不用 Docker 起中转层）

1. 生成两把 token（上节）。
2. 在本仓启动 `./bin/platform-gateway` 的环境里设置：
   - `LISTEN_ADDR=127.0.0.1:8091`
   - `GATEWAY_TOKEN_EXTERNAL=<外网>`
   - `GATEWAY_TOKEN_INTERNAL=<内部>`
   - `CK_LOGS_*`（Kibana）
   - `GATEWAY_LOG_DIR=<rag-explorer-ai>/logs/platform-gateway`
3. rag-explorer-ai 仓库根 `.env.local`：
   ```bash
   PLATFORM_GATEWAY_BASE_URL=http://127.0.0.1:8091
   PLATFORM_GATEWAY_TOKEN=<外网那把>
   PLATFORM_GATEWAY_INTERNAL_TOKEN=<内部那把>
   ```
4. **改 token 后要重启 gateway**。再重启 Worker / Application Runtime。
5. 8091 只绑 loopback。本机 `curl http://127.0.0.1:8091/health` 应 200；非 loopback 应连不上。

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

`delivery`：`account` 是被查邮箱，`peer` 是对方。inbound = account 当收件人，outbound = account 当发件人。

内部 token **也可以**调这三条（内部是超集）。

### 内部 token（客服 Factory）

| 方法 | 路径 | 对应工具 | 条件 |
|---|---|---|---|
| POST | `/v1/cklogs/analysis/delivery` | `query_delivery_log` | `direction` 可 `both`；`sender` / `recipient` / `domain` / `subject` / `msgId` / `timeRange` / `countOnly`。不要求账号，不校验授权域 |
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
- 63 公网 self-service 机器：只配外网 token，不要配 `GATEWAY_TOKEN_INTERNAL` / `PLATFORM_GATEWAY_INTERNAL_TOKEN`
