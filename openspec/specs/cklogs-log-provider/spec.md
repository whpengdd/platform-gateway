# cklogs-log-provider Specification

## Purpose

Define the selectable log backend for `/v1/cklogs/*`: Kibana by default or LogBot via `cklogs.provider`, with LogBot session lifecycle, per-source completeness semantics, field-mapping parity and capability gaps surfaced honestly.

## Requirements

### Requirement: Provider selection
网关 SHALL 从 `config.json` 的 `cklogs.provider` 读取日志后端，取值为 `kibana` 或 `logbot`，缺省为 `kibana`。其他取值 SHALL 使启动失败。所选 provider 的必填配置缺失时，`/ready` SHALL 返回非 200。切换 provider SHALL NOT 改变任何 `/v1/cklogs/*` 路径、请求校验、响应结构、token 权限及共享 FIFO 队列行为。

#### Scenario: Default stays on Kibana
- **WHEN** `cklogs.provider` 未配置
- **THEN** 网关继续通过 Kibana 查询，行为与变更前一致

#### Scenario: Invalid provider
- **WHEN** `cklogs.provider` 为 `elastic`
- **THEN** 启动失败，错误指出 `cklogs.provider`，不输出任何凭据

#### Scenario: LogBot selected without credentials
- **WHEN** `cklogs.provider=logbot` 且未配置 LogBot 账号
- **THEN** `/ready` 不返回 200，cklogs 请求返回 `not_available`，且不发出上游请求

### Requirement: LogBot authentication
logbot provider SHALL 优先使用 `cklogs.logbot.auth` 中 `type=bearer` 的管理员发放服务令牌：每个请求带 `Authorization: Bearer <token>`，不登录。`type=login`（`POST /api/login` 换 cookie，进程内缓存，401/重定向时加锁重登一次）SHALL 作为备选分支保留；缺少凭据时 SHALL 视为未配置。上游返回 403 时 SHALL 返回 `not_available` 并说明原因，不重试。账号、密码、cookie 和令牌 MUST NOT 出现在日志、审计记录或响应中。

#### Scenario: Bearer token rejected
- **WHEN** Bearer 令牌被上游拒绝（401）
- **THEN** 响应 `status=error`、`code=CK_LOGS_AUTH_ERROR`，审计中不出现令牌

#### Scenario: Wrong credentials
- **WHEN** 登录失败
- **THEN** 响应 `status=error`、`code=CK_LOGS_AUTH_ERROR`，审计中不出现密码或 cookie

#### Scenario: Suspended log type
- **WHEN** LogBot 对某日志类型返回 403「暂停提供查询」
- **THEN** 该项返回 `not_available`，`limitations` 写明原因

### Requirement: Asynchronous query lifecycle
logbot provider SHALL 用 JSON 提交 `POST /api/agent/log-query/jobs`（`query_type=transmission`、`start_time`/`end_time` 为无时区上海本地 `YYYY-MM-DD HH:MM:SS`、区间 `[start,end)`、`log_sources`、`filters`、`purpose`），轮询 `GET /api/agent/log-query/jobs/{job_id}` 至 `queued`/`running` 之外的状态或操作预算耗尽，成功后经 `GET /jobs/{job_id}/results` 按 `page_size`/`page_token` 游标分页取回行。单任务时间窗上限 24h，更长窗口 SHALL 切分为 ≤24h 的串行任务并合并结果；任一分段失败 SHALL 整体报错，不得冒充完整。预算耗尽时 SHALL 返回现有超时语义。上游返回 429 时 SHALL 返回可重试错误。

#### Scenario: Successful query
- **WHEN** 任务轮询到 `success`
- **THEN** 网关经 results 端点翻页取回全部行（至多展示上限），生成响应

#### Scenario: Window over 24h
- **WHEN** 查询时间窗超过 24h
- **THEN** 网关切分为多个 ≤24h 的任务串行执行，行、总数与截断标记合并

#### Scenario: Budget exhausted while running
- **WHEN** 操作超时前任务仍为 `running`
- **THEN** 响应 `status=error`、`code=CK_LOGS_TIMEOUT`，不返回 `total=0`

#### Scenario: Failed job
- **WHEN** 任务到达 `failed` 等终态
- **THEN** 响应 `status=error`、`code=CK_LOGS_UPSTREAM_ERROR`，消息取自脱敏后的上游错误

### Requirement: Result completeness
网关 SHALL 依据 results 端点的完整性字段判断结果：`snapshot_truncated=true` 或命中展示上限时 SHALL 置 `truncated=true`，并在 `limitations` 中说明该时段可能还有更多记录，不得写成「没有记录」；`total_count` 为上游去重前口径；agent 实测会返回完全重复的行，SHALL 按整行序列化去重，不得把不同邮件的相似行误并。countOnly 时 SHALL 只取 `total_count` 不翻页拉明细。

#### Scenario: Snapshot truncated
- **WHEN** 结果快照 `snapshot_truncated=true`
- **THEN** 响应含已取回数据，`truncated=true`，`limitations` 写明可能还有更多、不等于没有记录

#### Scenario: Exact duplicates deduplicated
- **WHEN** 结果含完全相同的行
- **THEN** 重复行只计一次；仅公共字段相似但内容不同的行必须保留

### Requirement: Business flow parity with Kibana
logbot provider SHALL 保留 Kibana 实现中各接口的来源选择和合并逻辑：自助与分析 delivery 的 outbound/inbound/both 方向，以及对应的 MTA、DA、PROXYSVR 来源；DA 行的出站已发送副本过滤；收件人回退；按 tid 串联的 `/v1/cklogs/message` 投递轨迹；`page`/`pageSize`/`hasMore` 分页；以及 `countOnly`。Kibana 中由 dasvr、pop3、wmsvr 等来源，或由 tid/subject 过滤提供、但 LogBot 实测不支持的能力，SHALL 在 logbot provider 下返回 `status=not_available`，并在 `limitations` 中说明原因，MUST NOT 返回空结果冒充无记录。`msgId` 筛选 SHALL 仅在 DA（投递日志）来源映射为 `mid` 过滤器下推，其他来源返回 `not_available`。

#### Scenario: Outbound self-service delivery
- **WHEN** 外部 token 以 `direction=outbound` 查询账号
- **THEN** 网关查询与 Kibana 实现相同的来源组合，结果条目与筛选语义一致

#### Scenario: Unsupported capability
- **WHEN** LogBot 对 pop3 登录日志返回 403「暂停提供查询」，而 provider 为 logbot
- **THEN** `/v1/cklogs/login` 中 pop3 一项为 `not_available`，说明 LogBot 未提供该来源

### Requirement: Field mapping parity
logbot provider SHALL 把 LogBot agent 行（`log_source` 区分来源；MTA 收件人为标量 `to`，规整为 `rcpt`）映射到现有 `Entry`、`TraceEvent`、`AuthEntry` JSON 字段，字段名和取值格式与 Kibana provider 一致（例如 `timestampIso` 为 ISO8601，`folderName` 由 folderid 推导，`cacVerdict` 由 cactag 推导）。按列名读取，忽略未知列。LogBot 未提供的字段 SHALL 省略，不得填入猜测值：DA 行缺 `cmd`/`folderid`/`otherlog`（hdrmsgid/commrefid），MTA 行缺 `result`/`respond`/`errinfo`，各来源均缺 `tid`/`mid`。DA 行无 cmd 时 `deliveryStatus` SHALL 保持 `unknown` 并附 `limitations` 说明，不得臆断投递结果；无分组键的 DA 行 SHALL 逐行保留并附 `limitations`，不得启发式归并。

#### Scenario: Same message via both providers
- **WHEN** 同一封邮件、同一时间窗分别用 kibana 和 logbot provider 查询
- **THEN** 两者共有字段的取值一致，差异只限于 LogBot 未提供的字段

### Requirement: Audit of LogBot calls
每次 LogBot HTTP 调用 SHALL 写入审计记录，包括阶段（login/submit/poll/results/result）、来源、HTTP 状态、耗时、`job_id` 和错误类型，MUST NOT 包含凭据、cookie 或日志行正文。

#### Scenario: Audit query
- **WHEN** 一次 delivery 请求经 LogBot 完成
- **THEN** 审计行列出 submit、poll 与 results 调用及 job_id，不含密码、cookie 或令牌
