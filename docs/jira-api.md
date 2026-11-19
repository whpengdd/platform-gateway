# Jira 中转 API：调用方接口文档

状态：v1 本仓实现，使用 mock 验证；未完成真实 Jira 验收或生产部署。当前合同替代之前以 Zammad ticket、绑定和同步任务为中心的接口。网关只做授权、过滤和受限 API 中转；同步调度、业务绑定、去重及重试策略由调用方负责。本项目不修改 Zammad 源码。

配置见 [gateway-auth.md](gateway-auth.md)，设计及原有调用审计见 [jira-bridge-design.md](jira-bridge-design.md)。

## 1. 鉴权和资源范围

所有接口必需 `Authorization: Bearer <token>`。token 在 ./config.json 中配置，Jira token 可访问其 jiraProjects 中的一个或多个项目。业务前缀 `P` 表示 `/v1/jira/projects/{projectId}`，projectId 直接为 Jira project key，如 CS。

有效访问范围为：token 授权项目 ∩ Jira 服务账号可见 issue ∩ 项目配置的属性过滤条件。filterJql 省略或空白时不加附加限制，非空时由 Jira 执行并强制 AND 项目范围；调用方不能修改、覆盖或跳过过滤。无需向网关登记 ticket/issue 绑定。

| 请求头 | 说明 |
|---|---|
| Authorization | 必需，Bearer token |
| Content-Type | JSON POST 使用 application/json，附件上传使用 multipart/form-data |
| X-Request-Id | 可选，1–128个 ASCII 字母/数字/-_.:；缺省由网关生成并在响应同名头返回 |

JSON 使用 UTF-8。搜索时间参数使用包含 `Z` 或明确偏移的 RFC3339；创建及读取的受支持日期字符串按第4–6节保留原值，不统一改写成 UTC。未知/重复 JSON 键、尾随 JSON、未知/重复查询参数均拒绝。projectId 为 `[A-Z][A-Z0-9_]{0,63}`，issueKey 为合法 Jira key（如 CS-100），commentId/attachmentId 为1–64位正十进制数字字符串。key 前缀仅作输入校验，实际项目归属始终从 Jira 响应核对。

不提供 /tickets、/binding、/changes 或 /operations 路径，也不接受 ticketId/articleId、绑定许可或客户端上游 URL。第一版不提供幂等保证，Idempotency-Key 不是受支持的请求语义。

## 2. 端点

| 方法 | 路径 | 成功返回 |
|---|---|---|
| GET | /v1/jira/projects | 200 当前 token 获准的项目列表 |
| GET | P/capabilities | 200 项目固定能力、字段定义及限制 |
| POST | P/issues/search | 200 满足授权及查询条件的 issue 页 |
| GET | P/issues/{issueKey} | 200 裁剪后的 issue |
| POST | P/issues | 201 新建 issue 回执 |
| GET | P/issues/{issueKey}/comments | 200 评论页 |
| GET | P/issues/{issueKey}/comments/{commentId} | 200 单条评论 |
| POST | P/issues/{issueKey}/comments | 201 新评论回执 |
| POST | P/issues/{issueKey}/attachments | 201 上传附件回执 |
| GET | P/issues/{issueKey}/attachments/{attachmentId}/content | 200 文件字节 |

全部业务接口先校验项目；目标 issue 必须通过项目 filterJql，评论/附件还校验父 issue 归属。不存在和过滤不通过统一404。cklogs token 访问 Jira 返回403 token_scope_forbidden。未授权或未配置项目统一403 project_access_forbidden。

## 3. 项目及能力

GET /v1/jira/projects 无参数，示例：

```json
{"items":[{"projectId":"CS"},{"projectId":"IT"}]}
```

不枚举 Jira 全站项目。GET P/capabilities 无参数，示例仅展示部分字段定义：

```json
{
  "projectId": "CS",
  "createEnabled": true,
  "createDisabledReason": null,
  "issueTypeId": "10504",
  "createFields": [
    {"id":"summary","type":"string","required":true,"maxBytes":1024},
    {"id":"description","type":"string","required":false,"maxBytes":32768}
  ],
  "readFields": ["summary","status","updated"],
  "bot": {"identity":"bridge-bot","displayName":"支持机器人","email":null},
  "attachmentContentTypes": ["image/png","image/jpeg","application/pdf","text/plain"],
  "attachmentMaxBytes": 10485760
}
```

createFields 仅含配置允许且 Jira 创建元数据支持的字段，必要时包含 allowedValues 和 required。bot 仅返回当前服务账号的最小身份，可供调用方自行识别其评论。字段 ID/值转换由调用方处理；网关不接受任意自定义字段。创建未配置 issueTypeId、缺少必填字段可行映射，或过滤条件不能安全用于创建时 createEnabled=false，原因是脱敏固定代码。过滤条件本身不作为客户端可更改参数公布。

bot 身份与时间搜索共用按 Jira client 缓存的 `/myself` 账号信息，默认缓存300秒。身份有效但 `timeZone` 缺失或不可用时，仍可检查创建能力并创建；仅需时区转换的搜索受影响。缓存不会跳过创建元数据、权限或资源归属检查，createEnabled 每次重新计算。

项目状态、人员/字段目录等底层 API 由网关内部按需要调用，不原样开放全站元数据。默认 bot reporter；不提供可分配人员搜索或任意 reporter 替换。

## 4. 受限搜索和单条读取

POST P/issues/search 示例：

```json
{
  "updatedFrom": "2026-09-01T00:00:00Z",
  "updatedTo": "2026-09-14T00:00:00Z",
  "issueKeys": ["CS-100", "CS-101"],
  "text": "调用方自己的业务标记",
  "limit": 50
}
```

以上字段均可选。limit 默认50，范围1–100。issueKeys 最多100个，均须为合法 key；空数组与不传相同，不增加 key 条件。调用方没有待查 key 时应自行跳过请求；text 最多256字节，用于固定的 Jira 文本搜索条件，仅作为转义后的字面值处理。提供时间字段时 updatedFrom/updatedTo 必须成对存在、均为非空且包含时区的 RFC3339 时间，按绝对时刻比较须满足 to > from、实际经过时长不超过30天，允许查询历史区间。无时区、缺失一端等非法参数在读取账号时区前返回400 invalid_parameter。不提供时间则遍历当前项目符合过滤的集合，不为此额外读取 `/myself`。网关不自行选择增量起点或限制为“绑定之后”。

带时间范围时，网关识别调用方提供的偏移，再转换到 Jira 对接账号 `/rest/api/2/myself` 返回的 `timeZone`，生成无时区后缀的 JQL 日期。例如 `2026-09-29T10:00:45.123+08:00` 在 Asia/Shanghai 账号下生成 `updated >= "2026-09-29 10:00"`，在 UTC 账号下生成 `updated >= "2026-09-29 02:00"`。同一绝对时刻的 `+08:00`、`Z`、`-04:00` 表示会生成相同边界。仍使用 `>=` / `<=` 闭区间和分钟精度，舍去秒及小数秒，不保证秒级精确过滤；调用方需处理分钟边界的重叠。配置中的 filterJql 日期不参与此转换，继续由 Jira 解释。

时区规则按每个查询日期计算，不使用账号当前的固定偏移。夏令时回拨可能使同一本地分钟出现两次，例如 America/New_York 的 `2026-11-01 01:30`；无时区 JQL 无法区分这两次，任一最终边界处于重复分钟时返回400 ambiguous_time_range，不发送业务搜索。半小时回拨也遵循同一规则；跨越夏令时切换且两个边界均唯一的范围可查询。需要查询重复小时边界的部署可为同步专用账号采用 UTC。

首请求不含 cursor；后续只提交 `{ "cursor": "<nextCursor>" }`，禁止同时改变查询或 limit。响应：

```json
{
  "items": [
    {"projectId":"CS","issueKey":"CS-100","fields":{"summary":"邮件异常","status":{"name":"处理中"},"updated":"2026-09-14T02:00:00Z"}}
  ],
  "nextCursor": null
}
```

所有条件与网关的项目/属性过滤共同生效，不接受 JQL、fields、expand。响应 fields 只包含配置的 readFields，并裁剪人员等嵌套对象；无 raw total。可能因资源变化产生空页，继续翻页直到 nextCursor 为 null。游标仅用于此次查询、绑定操作类型、项目、配置版本及本次查询捕获的账号上下文版本，最长32768字节、有效期15分钟；进程重启使其失效。过期返回410 cursor_expired；查询期间 Jira 数据变化可能造成重复或遗漏，调用方自行处理一致性和再次查询。

成功刷新发现账号身份或时区状态变化后，旧搜索游标返回410 cursor_expired，需重新开始查询；普通缓存续期、展示名变化和正常季节偏移不会使游标失效。续页先比较已知版本，已经失效的游标直接返回410；带时间的有效续页再按缓存策略获取账号上下文并再次比较版本，无时间的续页不主动刷新。首次建立账号上下文不会使此前无时间游标失效。若成功刷新发现原时区丢失，旧游标优先返回410，新时间查询返回503 timezone_unavailable；尚未发现游标失效而读取账号失败时返回503 upstream_unavailable，不伪造版本变化。评论游标不受账号时区版本影响。

GET P/issues/{issueKey} 无参数，返回与上面 items 中相同的单个对象。fields 中 project/security 等仅用于内部授权，不会因过滤需要而自动下发。

`fields.updated` 和受支持的自定义日期时间字段保留 Jira 原字符串，包括 `+0800`、`+08:00` 或 `Z` 后缀及原有精度；展示时区由调用方处理。

默认 readFields 为 summary/status/updated/reporter/creator/assignee。可按项目增加经过审核的 description/customfield_* 或 attachment；不得返回通用 self、avatar URL 或 attachment content URL。人员对象只保留 identity/displayName/email；attachment 仅保留 id/filename/size/mimeType。说明：配置 description 可供调用方核对自身 marker，但它不是网关同步功能。

## 5. 创建 issue

POST P/issues，body：

```json
{
  "fields": {
    "summary": "邮件投递异常",
    "description": "调用方组织的描述、回链及业务标记"
  }
}
```

只允许 fields 中的配置白名单键；每个值按 Jira 创建元数据校验类型、枚举及大小，禁止通用 update/actions/properties 等逃逸结构。项目和 issue type 由路径及项目配置固定，不能在 fields 提交 project/issuetype/reporter/assignee/security。JSON 最多256 KiB，summary 最多1024字节、description 最多32 KiB，其余字段按元数据及程序限额限制。

受支持的字符串类型日期时间字段原样转发，包括 `+08:00`、`+0800` 或 `Z`；网关不按账号时区再加减小时。纯日期（如 duedate）、description/comment 内的日期文字也不做转换。字段仍须满足现有 schema 和配置约束，不因此扩展支持类型或承诺 Jira 接受无时区的日期时间格式。系统 `created` / `updated` 禁止作为创建字段，由 Jira 生成。此次搜索时区适配不修改已创建工单或 Jira 页面显示，也不表示已定位“创建时间少八小时”的现场问题。

createDefaults 固定创建字段，冲突返回400 create_default_conflict。首版只在无过滤或单条 `labels = 固定字符串`（允许整体外层括号，且 createDefaults.labels 包含该值）时允许创建；AND/OR/cf[n]/函数等其他合法 JQL 关闭创建，返回403 operation_not_available。此限制不禁止 createFields 中经元数据验证的业务自定义字段。创建仍须满足实际必填项；不先创建再扩大权限。

成功返回：

```json
{"projectId":"CS","issueKey":"CS-100","browseUrl":"https://jira.example.com/browse/CS-100"}
```

不创建 Zammad 绑定，不自动添加回链或评论，不返回后台操作 ID。成功表示本次 Jira 创建请求成功；结果不明时不能假定没有创建，见第8节。

## 6. 评论

GET P/issues/{issueKey}/comments 支持 limit（1–100，默认50）或后续 cursor；分页规则同搜索，不可在带 cursor 时改 limit。评论游标额外绑定稳定父 issue ID，每页重新检查父 issue 与评论可见性；跨项目、跨 issue、跨接口复用返回400 invalid_cursor，过期或配置变化返回410 cursor_expired。返回 `{items:[Comment],nextCursor:null}`。GET P/issues/{issueKey}/comments/{commentId} 返回单个 Comment：

```json
{"id":"2001","body":"调用方组织的正文","createdAt":"2026-09-14T02:00:00Z","updatedAt":"2026-09-14T02:00:00Z","author":{"identity":"agent-1","displayName":"支持人员","email":null}}
```

每次访问先重新核对父 issue 的项目及 filterJql。保留符合可见规则的历史评论和 bot 评论，不做绑定时间过滤或发布标记解析；受 group/role visibility 限制的评论第一版默认不下发。正文不作为可执行 HTML，人员信息裁剪。

createdAt / updatedAt 仅从 Jira 的 created / updated 改名投影，保留原始字符串、时区后缀及精度；正文中的时间文字不做统一补偿。

POST P/issues/{issueKey}/comments，body 为 `{ "body": "调用方组织的正文" }`。正文必填非空、最多32 KiB。网关不添加 marker、不根据 articleId 防重、不组织附件引用。成功201返回 `{"commentId":"2001"}`；同一请求重复提交可能产生多个评论。

## 7. 附件

POST P/issues/{issueKey}/attachments 使用 multipart，只接受一个名为 file 的 part。单文件最多10 MiB、multipart 最多11 MiB；filename 为单个文件名、最多255字节，拒绝路径分隔符及控制字符。类型以程序允许集合和实际内容检查为准，capabilities 的 attachmentContentTypes 公布允许类型。

```bash
curl --fail-with-body "$GW/v1/jira/projects/CS/issues/CS-100/attachments" \
  -H "Authorization: Bearer $TOKEN" \
  -F 'file=@./example.png;type=image/png'
```

成功201返回 `{"attachmentId":"3001","filename":"example.png","size":1234,"mimeType":"image/png"}`，不返回 Jira 下载 URL。文章归属、附件引用格式及上传后的评论由调用方处理。

GET P/issues/{issueKey}/attachments/{attachmentId}/content 返回二进制，最多10 MiB。先检查父 issue 及其附件列表，确认 attachmentId 归属，再从经验证的 Jira 同源 secure/attachment 地址读取；不接受 contentUrl 参数，不跟随重定向。固定使用 attachment 的 Content-Disposition 和 nosniff，防止被当作可执行页面呈现。

## 8. 错误及写入不确定性

错误 JSON：`{"code":"resource_not_available","error":"资源不可用","requestId":"req_example"}`。

| HTTP | code | 含义 |
|---|---|---|
| 400 | invalid_body / invalid_parameter / invalid_cursor / create_default_conflict | 输入、游标或创建值不符合约束 |
| 400 | ambiguous_time_range | 时间边界在账号时区回拨时重复，无法用无时区 JQL 唯一表达 |
| 401 | unauthorized | token 无效 |
| 403 | token_scope_forbidden / project_access_forbidden / operation_not_available | 服务、项目或当前创建能力不允许 |
| 404 | resource_not_available | 不存在、项目不符、属性过滤不通过或子资源不归属 |
| 410 | cursor_expired | 游标失效，调用方重新查询 |
| 413 | payload_too_large | 请求或附件超过限额 |
| 415 | unsupported_media_type | 不支持的请求或文件类型 |
| 429 | rate_limited | 按 Retry-After 等待 |
| 503 | gateway_busy / upstream_unavailable | 队列满、读取上游不可用，或写入尚未发送即失败 |
| 503 | timezone_unavailable | 已获取有效账号身份，但缺少可用的明确时区，无法执行时间搜索 |
| 502 | outcome_unknown | 写入可能已到 Jira，无法确认结果，调用方不得假定失败 |
| 502 | upstream_rejected | Jira 明确拒绝请求，返回脱敏原因 |

gateway 不自动重试写入，不提供持久幂等保证。客户端连接超时也可能发生在 Jira 已成功写入之后，即使未收到 outcome_unknown，也应自行核对。同步周期、恢复 marker、业务去重和重试决策都属于调用方。只读搜索 POST 可重新发起，但结果可能随 Jira 数据变化。

接口不提供原始 Jira 转发、任意 JQL、用户管理、删除、transition 或任意字段更新。默认项目速率/字节上限见设计文档，cklogs 队列独立；配置过滤不会被任何读取、写入或分页路径跳过。

## 字段支持与接入差异

字段配置只允许程序支持的投影。summary/description/updated 为标量，status 为 id/name；人员为 identity/displayName/email；附件为 id/filename/size/mimeType。禁止 readFields 包含 comment/issuelinks/subtasks/parent/worklog。customfield_<数字> 仅支持已确认的标量、选项 id/value、最小人员结构或这些类型的数组；未知复杂类型拒绝，不能透传任意嵌套对象。读取类型确认独立于创建能力，元数据暂不可得时相关读取返回503。

Zammad 所需 description、attachment 和实际客户字段须显式配置；人员键名、评论 createdAt/updatedAt 与原 Jira 字段的转换及附件按 ID 下载由调用方适配。详见 [源码字段核对](jira-field-compatibility.md)。

JQL 初次验证或运行时验证遇到暂时故障返回503，5秒退避后下次请求触发重验，同项目只进行一次并发验证；成功后恢复。明确非法条件须修改配置并重启。JQL 有索引延迟，不保证即时撤权或检查/写入原子性。创建成功后的核对不依赖立即搜索命中，无法确认结果返回 outcome_unknown，不自动重试。

## 运行限制与接入核验

JSON 请求 256 KiB、description/comment 32 KiB、文件 10 MiB、multipart 11 MiB（包含结束边界后的 HTTP body 数据，在上传 Jira 前完整校验）、上游及下游 JSON 4 MiB。附件支持 PNG/JPEG/PDF/纯文本，上传检查声明类型与内容；文件名禁止路径分隔符、控制字符和百分号。下载拒绝替代路径编码。

独立 FIFO 为 2 执行槽和 32 等待位，排队及上游操作各 30 秒，内部 HTTP 串行。向客户端写响应从首次输出起另有 30 秒连接写 deadline；此阶段不占 Jira 上游槽，慢读客户端超时后连接结束。每项目读取 60 次/分钟、写入 20 次/分钟、创建 100 次/UTC日、上传 200 MiB/UTC日；请求尝试消耗额度，同项目多 token 共用，重启清零。分钟窗口按 UTC epoch 分钟切换。

时间条件自动转换到 Jira 账号时区，支持明确可加载的 IANA 时区或 UTC；不再要求所有服务账号设为 UTC。空白、未知或依赖宿主环境的 Local 不可用，网关不会猜测 UTC、容器时区或沿用旧时区。二进制内嵌时区规则以支持无系统 zoneinfo 的镜像，但系统 zoneinfo / ZONEINFO 可能优先；规则更新需要更新 Go / 网关或运行环境的时区数据，单纯清缓存不等于更新规则。分页不是快照，也不是同步水位。游标最大 32 KiB，以容纳 100 个长 issue key。

账号缓存按 Jira client / 对接凭据共享于所有项目及入站 token，各进程独立且不持久化。`jira.accountCacheTtlSeconds` 默认300秒，范围0–3600；0禁用已完成快照复用，仍合并并发读取并保留游标所需的上下文历史。TTL 从成功获取身份快照完成时起算；按需刷新，无后台轮询。过期刷新失败不使用旧身份或旧时区，失败后退避5秒再由下一次请求恢复。身份有效但时区不可用时，时间查询独立按5秒间隔重试；若该重试读取失败，时间查询在当次及退避期返回 upstream_unavailable，未过期身份仍可用于创建及 capabilities，失败不延长其 TTL。

时区不可用时的5秒退避独立于成功快照 TTL，TTL=0 或不足5秒也不会提前触发时间查询的重试；退避内不会返回过期身份。只需身份的创建和 capabilities 仍按自身 TTL 读取账号信息。

账号时区修改可能在剩余 TTL 内尚未被发现；运维应重启进程 / 重建容器清空缓存和游标，或接受此检测延迟。即使 TTL=0，`/myself` 与搜索之间仍可能发生外部配置变更，不能保证原子性。账号缓存故障不会单独阻断不需要账号上下文的普通读取或无时间搜索；所有原有父资源、过滤和可见性核验仍执行。

创建适配旧 createmeta 和新按项目/type 分页端点；旧端点明确返回 404/405/410 时才尝试新端点。未知字段类型、未覆盖必填项或不可设置 reporter 均关闭创建。实现依据见 [Atlassian REST 示例](https://developer.atlassian.com/server/jira/platform/jira-rest-api-examples/)。真实版本、字段、权限及时区仍待核验，见 [验证记录](jira-validation.md)。

最小调用示例（仅向已部署的网关发送请求）：

```bash
curl --fail-with-body "$GW/v1/jira/projects" -H "Authorization: Bearer $TOKEN"
curl --fail-with-body "$GW/v1/jira/projects/CS/capabilities" -H "Authorization: Bearer $TOKEN"
curl --fail-with-body "$GW/v1/jira/projects/CS/issues/search" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{"limit":50}'
curl --fail-with-body "$GW/v1/jira/projects/CS/issues" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"fields":{"summary":"调用方提交的标题"}}'
```

数字字段仅接受经 float64 解码并重新编码为 JSON 后数值不变的表示。比较时允许 1、1.0、1e0 等同值形式；不能保真的数字（如 9007199254740993）在配置中导致启动失败，在请求中返回 400 invalid_body，不自动舍入。数字字面量最长 1024 字符，十进制指数限制在 -2048 至 2048；这些限额也约束精确比较的资源消耗。上游 JSON 数字不能保真时，读取返回 503；可能已完成的写入返回 502 outcome_unknown。

### 客户信息查询

`GET /v1/jira/customer-lookup` 供具有 `CS` 项目权限的 Bearer token 使用。查询参数透传至上游：`senderEmail`、`email`、`domain`、`name`、`phone`、`q`、`keyword`，以及可选的 `type`（`customer`、`cj`、`employee`）。不传 `type` 时返回上游成形的 `resolution`、`profile`、`prefillFields`、`variants` 和 `query`；客户未命中时需人工补录。查询条件缺失由上游返回 400。

在网关配置文件中添加 `"customerLookup": {"baseUrl": "http://localhost:8787", "apiKey": "<上游密钥>"}`。`baseUrl` 是上游服务根地址；密钥仅放在实际配置中，勿提交仓库。未配置时接口返回 503。
