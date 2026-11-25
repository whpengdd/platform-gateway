## Context

`internal/cklogs` 依赖 Kibana：`Client.msearch` 通过 `/api/console/proxy?path=/_msearch` 发送 ES DSL（`dsl.go`），按 dataset 选择 index，例如 `mtatrans_distributed`、`datrans_distributed`、`dasvr_distributed`、`proxytrans_distributed`、`pop3trans_distributed`、`wmsvr_distributed`。`map.go` 把 `_source` 映射为 `Entry`/`TraceEvent`/`AuthEntry`；`delivery.go`、`mta.go`、`proxy.go`、`account.go`、`trace.go` 编排多来源查询与合并。所有请求共享 `queue` 的 FIFO 槽。

LogBot 是异步任务 API（见 `.orca/drops/external-log-api-guide.md`）：表单提交 `query-jobs`，轮询结果，按来源返回状态。来源只列出 DA、MTA、PROXYSVR（transmission）和 webadmin。实测：不带认证访问 `/api/*` 会 302 到 itportal SSO 登录页；带 `Authorization: Bearer` 时请求直达 LogBot（无效令牌返回 401 JSON `{"detail":"无效的日志服务令牌"}`）。服务地址为 `https://logbot.icoremail.net`（内网 172.16.1.10）。

## Goals / Non-Goals

**Goals:** 通过配置开关切换到 LogBot，调用方合同零变化；保持 Kibana 的业务流程与字段；对不完整结果诚实报告。

**Non-Goals:** 不接入完整导出（`export-jobs`）与 CSV 下载；不改调用方 API；不在本变更删除 Kibana 代码（Kibana 下线后另起变更）；不实现 webadmin 查询的新对外接口。

## Decisions

1. **Provider 接口在"按来源取行"层切分。** 定义 `Backend` 接口，例如 `Fetch(ctx, source, Filters, opts) QueryResult` 和 `Trace(ctx, tid, tr)`。Kibana 实现包装现有 `msearch` + `dsl`；LogBot 实现负责提交任务、轮询并映射行。`delivery.go` 等编排与合并逻辑保持不动，从而保证流程一致。备选方案是在 HTTP 层整体复制一套 LogBot 版 service，但会让两套业务逻辑分叉，因此放弃。
2. **鉴权：静态 Bearer 服务令牌。** 用户已选定该方案，按对接指南使用管理员发放的应用令牌。不使用 SSO/LDAP，因为浏览器登录流程不稳定，也无法复用。令牌轮换时只需改 `config.json` 并重启，指南保证同一应用标识下的原任务仍可访问。HTTP 客户端禁止跟随重定向，避免出现 302 时把请求发往 SSO。
3. **一次请求合并为一个任务。** 同一业务查询需要的多个来源放进一个 `query-jobs` 提交，用重复的 `log_source[]` 表达，再在本地按 `log_source` 字段拆行，减少轮询次数。轮询间隔从 500ms 指数退避到 3s，总时长受 `OpTimeout` 和当前队列槽剩余预算约束。HTTP 调用仍在同一个队列槽内串行执行。
4. **分页。** LogBot 展示上限（例如 1000 行）以内由网关本地分页（沿用 `delivery_pages.go`）。超过上限时 `truncated=true`，并提示缩小时间窗。`countOnly` 优先使用 `job.result.sources[].loaded`，来源不是 `complete` 时，标记总数不精确。
5. **重复提交复用。** 指南说明，同条件的活动任务会被复用；网关提交时不带 `fresh=true`，超时后重发也安全。
6. **字段映射独立文件** `logbot_map.go`：把 LogBot 列名映射到 Kibana `_source` 键，然后复用现有 `map.go` 转换函数（`toIso`、`parseOtherlog`、`FolderName`、`cacVerdict`）。映射表依据实测样本和 `translog-fields.md` 填写。
7. **配置。** `cklogs.provider`，以及 `cklogs.logbot`：`baseUrl` 默认 `https://logbot.icoremail.net`，`auth: {type: "bearer", token}`，`pollIntervalMs`，`timeoutMs`。用 `strictjson` 校验未知键。部署通过 docker-compose `extra_hosts: logbot.icoremail.net:172.16.1.10` 解析。

## 实测结论（149 与 application1 网关对照 LogBot，2026-11-24）

- **鉴权**：Bearer 令牌仍 401；改用表单登录 `POST /api/login`（`username`、`password`），带 cookie 访问 `/api/*` 可用（角色 ops，权限 SELECT）。决策 2 改为"登录换 cookie，失效时重登一次"，Bearer 保留为备选。网关长期使用应换专用服务账号。
- **来源与类型**：transmission 只有 DA、MTA、PROXYSVR，另有 webadmin。`login`、`pop3`、`webmail`、`dasvr` 等类型返回 403「该日志类型暂停提供查询和导出」。
- **展示上限**：每来源 1000 行，达到时 `status=limit`、`loaded=1000`。未遇到 429（查询间隔 3 秒以上）。
- **筛选**：表单支持 `from_addr`、`to_addr`、`subject`、`mid`（仅 DA；MTA 返回 400）、`any_ip`、`clientip`、`platform_mode` 等。**没有 tid、msgId 筛选**。域名只能带在邮箱字段中，未映射的域名要加 `platform_mode=all`，否则 0 命中。
- **行结构**：`timestamp` 为 +08:00 字符串，需转 UTC；DA 的 `to` 是单值，MTA 的 `rcpt` 是列表；一次投递在 DA 中有多行（`cmd` 为 antivirus、local、remote 和空的汇总行），MTA 按命令分行（AUTH、MAIL、RCPT、DATA 等）。
- **字段对照**：一致的有 sender、subject、cmd、result、commresult、folderid、clientip、datarulename、cntrulename、cactag；缺失的有 tid、mid（行内无）、`folderName`（网关自行由 folderid 推导）、respond、errinfo（只有 DA/PROXYSVR 的 `desc`）、score、spamfng、blackip、debuginfo；`messageId` 仅 DA 的 `otherlog.hdrmsgid`。`commrefid` 与 Kibana 的 tid 格式不同，不能当 tid。
- **条数**：同账号同窗口，Kibana 出站 9 条 DA 与 LogBot `local` 行 9 条一致；其余 cmd 行需过滤或去重才能对齐。
- **能力结论**：`/v1/cklogs/delivery`、`analysis/delivery` 的 sender、recipient、subject、域名可实现，mid 部分可实现；`/message`（按 tid）、msgId 筛选、`/login`、`analysis/auth`（pop3、webmail）、`analysis/ops` 在 LogBot 上返回 `not_available`；SMTP 登录只能从 MTA 的 AUTH 行看，无法按账号过滤。
- **未验证**：application1 只有 external 令牌，`analysis/*` 没有基准数据（不影响 LogBot 不支持的结论）。

## Risks / Trade-offs

- [能力缺口：dasvr/pop3/wmsvr/tid/subject/msgId 可能无对应] → 任务 1 实测；不支持的能力明确返回 `not_available`，并在文档列出 Kibana 下线前需由 LogBot 方补齐的清单。
- [当前提供的令牌在 2026-11-24 实测返回 401 `无效的日志服务令牌`] → 任务 1.1 与 LogBot 管理员确认令牌是否已激活，以及绑定了哪些来源权限；确认前不能完成联调。
- [异步轮询拉长时延，占用队列槽] → 轮询受操作预算约束；预算耗尽按超时返回，不伪造结果。
- [LogBot 展示上限低于 Kibana HardSize 500 × 分页] → 用 `truncated` 和 limitations 说明情况；需要全量时由人工走导出。
- [字段名与 Kibana 不同] → 用双 provider 对照测试（同条件比较共有字段）。

## Migration Plan

1. 部署新版本，默认 `provider=kibana`，行为不变。
2. 在测试环境设为 `logbot`，用相同请求和 Kibana 结果对照。
3. 生产改配置并重启；出现问题时改回 `kibana` 再重启即可回滚。
4. Kibana 下线后另起变更，删除 Kibana 代码。

## Open Questions

- Bearer 令牌无效的原因；是否能拿到专用服务账号。
- `/message` 的替代方案：是否让 LogBot 方给转换后的 tid 列，或者接受 `not_available`。
- LogBot 是否支持 tid、subject、msgId 筛选，以及 dasvr、pop3、wmsvr 来源；`query_type` 是否还有其他取值。
- LogBot 返回行的实际列名（`translog-fields.md`、`API.md#post-apiquery` 未随指南提供）和展示上限。
