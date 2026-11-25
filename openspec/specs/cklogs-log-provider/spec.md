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
logbot provider SHALL 用 `cklogs.logbot.auth` 中的账号通过 `POST /api/login` 登录，在进程内缓存 cookie 并复用；缺少账号时 SHALL 视为未配置。上游返回 401 或重定向到登录页时，SHALL 加锁重登一次后重试原请求，仍失败则返回 `auth_error`。上游对日志类型或来源返回 403 时 SHALL 返回 `not_available` 并说明原因，不重试。账号、密码和 cookie MUST NOT 出现在日志、审计记录或响应中。

#### Scenario: Expired session
- **WHEN** 缓存会话失效，请求返回 401
- **THEN** 网关重登一次并完成该查询；错误码沿用现有 `CK_LOGS_*` 合同前缀

#### Scenario: Wrong credentials
- **WHEN** 登录失败
- **THEN** 响应 `status=error`、`code=CK_LOGS_AUTH_ERROR`，审计中不出现密码或 cookie

#### Scenario: Suspended log type
- **WHEN** LogBot 对某日志类型返回 403「暂停提供查询」
- **THEN** 该项返回 `not_available`，`limitations` 写明原因

### Requirement: Asynchronous query lifecycle
logbot provider SHALL 用表单提交 `POST /api/query-jobs`，起止时间按 Asia/Shanghai 格式化为 `YYYY-MM-DD HH:MM:SS`（含两端），`query_type=transmission`，用重复的 `log_source[]` 选择来源，用 `from_addr`、`to_addr`、`any_ip` 表达筛选条件。网关 SHALL 轮询 `GET /api/query-jobs/{job_id}`，直到 `job.status` 不是 `queued` 或 `running`，或者网关操作预算耗尽。预算耗尽时 SHALL 返回现有超时语义（"查询尚未完成，不能据此判断没有记录"）。上游返回 429 时 SHALL 返回可重试错误，不得被动循环提交。

#### Scenario: Successful query
- **WHEN** 任务轮询到 `job.status=success`
- **THEN** 网关读取 `job.result.data` 和 `job.result.sources`，生成响应

#### Scenario: Budget exhausted while running
- **WHEN** 操作超时前任务仍为 `running`
- **THEN** 响应 `status=error`、`code=CK_LOGS_TIMEOUT`，不返回 `total=0`

#### Scenario: Failed job
- **WHEN** `job.status` 为 `failed` 等终态
- **THEN** 响应 `status=error`、`code=CK_LOGS_UPSTREAM_ERROR`，消息取自 `job.error`

### Requirement: Per-source completeness
网关 SHALL 按 `job.result.sources[]` 的来源逐一判断结果。只有 `complete` 的来源 SHALL 可以把 `loaded=0` 解释为"该时段无匹配记录"。`limit` SHALL 置 `truncated=true`，并在 `limitations` 中说明已达展示上限。`partial`、`queued`、`running` SHALL 在 `limitations` 中说明仍有时段未扫描，并给出 `scanned_through`。`failed`、`expired` SHALL 在 `limitations` 中给出来源 `error`；所需来源全部失败时 SHALL 返回 `status=error`。`skipped_future` SHALL 说明该时段在未来。

#### Scenario: DA hits limit
- **WHEN** DA 来源返回 `status=limit`、`loaded=1000`
- **THEN** 响应含 1000 条以内数据，`truncated=true`，`limitations` 写明可能还有更多

#### Scenario: Partial source is not treated as empty
- **WHEN** MTA 为 `partial` 且 `loaded=0`
- **THEN** 响应不写"没有记录"，而是说明扫描到的截止时间

### Requirement: Business flow parity with Kibana
logbot provider SHALL 保留 Kibana 实现中各接口的来源选择和合并逻辑：自助与分析 delivery 的 outbound/inbound/both 方向，以及对应的 MTA、DA、PROXYSVR 来源；DA 行的出站已发送副本过滤；收件人回退；按 tid 串联的 `/v1/cklogs/message` 投递轨迹；`page`/`pageSize`/`hasMore` 分页；以及 `countOnly`。Kibana 中由 dasvr、pop3、wmsvr 等来源，或由 tid/subject/msgId 过滤提供、但 LogBot 实测不支持的能力，SHALL 在 logbot provider 下返回 `status=not_available`，并在 `limitations` 中说明原因，MUST NOT 返回空结果冒充无记录。

#### Scenario: Outbound self-service delivery
- **WHEN** 外部 token 以 `direction=outbound` 查询账号
- **THEN** 网关查询与 Kibana 实现相同的来源组合，结果条目与筛选语义一致

#### Scenario: Unsupported capability
- **WHEN** LogBot 对 pop3 登录日志返回 403「暂停提供查询」，而 provider 为 logbot
- **THEN** `/v1/cklogs/login` 中 pop3 一项为 `not_available`，说明 LogBot 未提供该来源

### Requirement: Field mapping parity
logbot provider SHALL 把 LogBot 行映射到现有 `Entry`、`TraceEvent`、`AuthEntry` JSON 字段，字段名和取值格式与 Kibana provider 一致（例如 `timestampIso` 为 ISO8601，`folderName` 由 folderid 推导，`cacVerdict` 由 cactag 推导，`signals` 由 otherlog 解析）。按列名读取，忽略未知列。LogBot 未提供的字段 SHALL 省略，不得填入猜测值。

#### Scenario: Same message via both providers
- **WHEN** 同一封邮件、同一时间窗分别用 kibana 和 logbot provider 查询
- **THEN** 两者共有字段的取值一致，差异只限于 LogBot 未提供的字段

### Requirement: Audit of LogBot calls
每次 LogBot HTTP 调用 SHALL 写入审计记录，包括阶段（submit/poll）、来源、HTTP 状态、耗时、`job_id`、各来源状态和错误类型，MUST NOT 包含凭据、cookie 或日志行正文。

#### Scenario: Audit query
- **WHEN** 一次 delivery 请求经 LogBot 完成
- **THEN** 审计行列出 submit 与 poll 调用及各来源状态，不含密码或 cookie
