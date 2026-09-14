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

JSON 使用 UTF-8，日期 RFC 3339 UTC；未知/重复 JSON 键、尾随 JSON、未知/重复查询参数均拒绝。projectId 为 `[A-Z][A-Z0-9_]{0,63}`，issueKey 为合法 Jira key（如 CS-100），commentId/attachmentId 为1–64位正十进制数字字符串。key 前缀仅作输入校验，实际项目归属始终从 Jira 响应核对。

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

以上字段均可选。limit 默认50，范围1–100。issueKeys 最多100个，均须为合法 key；空数组与不传相同，不增加 key 条件。调用方没有待查 key 时应自行跳过请求；text 最多256字节，用于固定的 Jira 文本搜索条件，仅作为转义后的字面值处理。提供时间字段时 updatedFrom/updatedTo 必须成对存在、均为非空合法时间、先后有效且范围不超过30天，允许查询历史区间；不提供时间则遍历当前项目符合过滤的集合。网关不自行选择增量起点或限制为“绑定之后”。

首请求不含 cursor；后续只提交 `{ "cursor": "<nextCursor>" }`，禁止同时改变查询或 limit。响应：

```json
{
  "items": [
    {"projectId":"CS","issueKey":"CS-100","fields":{"summary":"邮件异常","status":{"name":"处理中"},"updated":"2026-09-14T02:00:00Z"}}
  ],
  "nextCursor": null
}
```

所有条件与网关的项目/属性过滤共同生效，不接受 JQL、fields、expand。响应 fields 只包含配置的 readFields，并裁剪人员等嵌套对象；无 raw total。可能因资源变化产生空页，继续翻页直到 nextCursor 为 null。游标仅用于此次查询、绑定操作类型、项目和配置版本，最长32768字节、有效期15分钟；进程重启可以使其失效。过期返回410 cursor_expired；查询期间 Jira 数据变化可能造成重复或遗漏，调用方自行处理一致性和再次查询。

GET P/issues/{issueKey} 无参数，返回与上面 items 中相同的单个对象。fields 中 project/security 等仅用于内部授权，不会因过滤需要而自动下发。

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
| 401 | unauthorized | token 无效 |
| 403 | token_scope_forbidden / project_access_forbidden / operation_not_available | 服务、项目或当前创建能力不允许 |
| 404 | resource_not_available | 不存在、项目不符、属性过滤不通过或子资源不归属 |
| 410 | cursor_expired | 游标失效，调用方重新查询 |
| 413 | payload_too_large | 请求或附件超过限额 |
| 415 | unsupported_media_type | 不支持的请求或文件类型 |
| 429 | rate_limited | 按 Retry-After 等待 |
| 503 | gateway_busy / upstream_unavailable | 队列满、读取上游不可用，或写入尚未发送即失败 |
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

时间条件转为 Jira 分钟精度日期字面量；接入时须把 Jira 服务账号时区设为 UTC，调用方处理分钟边界重叠。分页不是快照，也不是同步水位。游标最大 32 KiB，以容纳 100 个长 issue key。

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
