# Verification Report: add-project-scoped-jira-gateway

> 本报告记录的是修复前状态。后续修复及回归结果见 [remediation.md](remediation.md)。

> 后续独立复评见 [reassessment.md](reassessment.md)：五个具体问题均获复现，但收窄 C1 的队列论证，并将 W5 作为测试补强建议。

Schema: spec-driven。核验对象为当前未提交工作区。完整读取 proposal、design、tasks 和三份 delta specs；没有跳过完整性、正确性或一致性检查。

## Summary

| Dimension | Status |
|---|---|
| Completeness | 26/26 tasks 已勾选；25/25 requirements 找到实现入口；共 39 scenarios |
| Correctness | 19/25 requirements 未发现明确偏差；6 项要求受下列实现问题影响；另有场景覆盖缺口 |
| Coherence | 服务隔离、固定上游、JQL 委托、有限创建证明及无同步账本符合设计；下载预算和部分部署路径未完整遵循设计 |
| Assessment | 1 CRITICAL、5 WARNING、1 SUGGESTION；修复关键问题后再归档 |

任务勾选和原验证记录表示已执行过相应工作，不代表这些边界均正确。此次核验发现的问题修正了此前“可归档”的判断。

## CRITICAL

### C1. 下载响应缺少写 deadline，并提前释放 Jira 队列槽

依据：`internal/jira/server.go:217-220` 只设置读取 deadline 和内部 context；`server.go:261-271` 在 Gate.Run 返回后才写附件。`cmd/gateway/main.go:66-70` 也没有 WriteTimeout。

临时阻塞 ResponseWriter 测试确认，在附件 Write 阻塞期间，Jira 队列 active=0，且 SetWriteDeadline 从未调用。内部 context 已退出，不能终止阻塞的客户端写入。慢客户端可持续占用连接、goroutine 和附件缓冲区；2 个执行槽和 30 秒操作预算未覆盖完整下载。

违反 `jira-resource-api` 的 Attachment transfer cannot become a URL proxy（下载 deadline）以及 Jira resource use is isolated and bounded。关联任务 4.5、5.1。

建议：给 Jira 响应写入设置真实连接写 deadline，将附件输出及缓冲区生命周期纳入明确的并发预算；不要仅依赖 context 取消。补真实慢读客户端测试，验证超时后释放连接/内存，以及阻塞下载不能无限绕开并发槽。避免直接改变 cklogs 的既有长操作合同。

## WARNING

### W1. multipart 总大小上限可被结束边界后的数据绕过

依据：`internal/jira/attachments.go:29` 包装 MaxBytesReader，但 `:58-60` 在 multipart reader 遇到最终边界后直接继续上传，没有检查整个 HTTP body。multipart EOF 不等同于 HTTP body EOF。

临时测试使用 5 字节文件和约 12 MiB epilogue：body=12,583,140 字节，超过 11,534,336 字节上限，仍成功执行一次 Jira 上传并返回成功。

违反 Jira resource use is isolated and bounded，关联任务 4.5、5.1。建议提前拒绝超限 Content-Length，同时对未知长度/chunked 请求计量至真实 HTTP EOF，并在上游 POST 前完成检查；补普通和 chunked epilogue 用例，断言 413 且写入次数为零。

### W2. 数字默认值会在 JSON 解析时失真

依据：`internal/strictjson/json.go:31-33` 对 map[string]any 使用默认 float64 解码；`internal/config/file.go:58` 的 createDefaults 使用该路径。`internal/jira/create.go:115-117` 用失真后的值比较冲突。

临时测试：配置 `createDefaults.customfield_1=9007199254740993`，解析后重新编码变成 `9007199254740992`。不同数字还可能在冲突比较前折叠为相同值。配置的固定 JSON 值不再被准确保留。

违反 Creation enforces non-overridable defaults，关联任务 1.1、3.4。建议保留 json.Number/原始数字表示并进行精确数值比较，或明确拒绝不支持精度的输入；同步处理上游数字投影。补 2^53 边界及相邻数字冲突测试，要求值不被静默改写。

### W3. 显式空时间参数被当作未提供

依据：`internal/jira/search.go:29` 以字符串是否非空决定是否验证时间，未检查 raw map 中字段是否存在。临时请求 `{"updatedFrom":""}` 返回 200，上游 JQL 没有任何时间条件。

违反 Inputs and output fields have explicit boundaries、Query pagination is bounded and not a sync cursor 的成对时间参数要求。关联任务 4.2。

建议在 raw 字段层检查 updatedFrom/updatedTo 是否成对存在，存在时两者均须为非空合法时间。补单边空字符串、双边空字符串和空值/合法值混合测试，要求 400 且不执行业务搜索。

此外，当前实现将时间截到分钟，文档要求 Jira bot 使用 UTC（`docs/jira-api.md` 运行限制段）；原 design.md 未记录该精度/时区合同。修复或明确写入设计并覆盖边界测试，避免调用方把 RFC3339 参数理解为秒级过滤。

### W4. app-stack 部署没有传递 Jira 上游配置

依据：`scripts/deploy/compose-auth.mjs:7-10` 仅生成认证文件路径、旧变量清空和挂载；`scripts/deploy/standalone.sh:150-160` 依赖调用方 Compose 加此 overlay 启动。

只读检查相邻仓库现有 `install/docker-compose.yml:391` 和 `docker-compose.intranet-149.yml:439` 的 gateway 服务：两者均未声明 JIRA_* 或通用 env_file。即使宿主设置 mock JIRA_BASE_URL/JIRA_API_TOKEN，生成 overlay 的 Jira 环境键仍为空数组。因此新 config.json 含 Jira token 时，这两条部署路径无法向容器提供必需上游配置，程序会拒绝启动。本仓 self Compose 和 host launcher 不存在这个具体遗漏。

关联 Caller documentation and deployment examples match the contract、任务 6.2。建议由本仓 overlay 显式透传四个受支持的 Jira 上游环境变量，保持 Bearer/Basic 互斥校验，不修改其他仓库；用临时 base Compose 和假凭据验证合并后的环境。当前脚本测试只检查挂载，不覆盖 Jira 启动配置。

### W5. 部分场景只有组件测试或缺少正向端到端回归

以下 coverage 是对现有测试的核对，不表示已证明对应实现错误：

- Project has no filter：有 Scope 空过滤实现，但缺无 filter 项目的业务读取成功测试。补无过滤 CS 的授权查询与项目归属断言。
- One token has multiple projects：已测试 A 拒绝 IT；缺 B 成功调用 IT 业务接口的正向测试。补 A/B 与 CS/IT 允许/拒绝矩阵。
- Complex filter is valid for reading but not creation：`TestCreateDefaultsAndProof` 只验证复杂过滤拒绝创建。补相同配置成功搜索，并断言完整 OR 条件仍受项目 AND 约束。
- Caller supplies a Zammad binding request：路由没有兼容别名，但缺 /tickets、/binding、/changes、/operations 不触发上游的回归。
- 评论正常创建 201 和搜索成功续页未被当前 mock 测试直接覆盖。评论 POST 测试主要为父权限拒绝；游标测试主要验证签名/跨范围拒绝。补一次正常评论写入，以及第一页 nextCursor 到第二页 startAt、查询、固定排序一致的完整测试。

关联测试入口：`internal/jira/server_test.go:68`、`:96`、`:185`、`:210`、`:251`。这些用例应纳入完成声明，而非以现有绿灯代替。

## SUGGESTION

### S1. 明确空 issueKeys 的语义

`internal/jira/search.go:37` 将空数组视为无 key 条件。临时请求 `{"issueKeys":[]}` 返回整个已授权项目过滤集合中的结果。规格未明确空列表的含义，因此不判断为确定违规。

建议在接口文档中明确空列表与缺省是否相同；更保守的合同是空列表返回空结果或 400。选定后加测试，避免调用方把“没有待查 key”变成遍历项目。

## Requirement mapping

下列路径均相对于仓库根目录。OK 表示找到对应实现且本次未发现明确偏差，不表示已完成真实 Jira 验收。

| Spec / requirement | 实现证据 | 核验结果 |
|---|---|---|
| auth / Single file is the source of inbound authorization | internal/config/file.go:36、internal/strictjson/json.go:16 | OK；数字默认值另见 W2 |
| auth / Legacy authorization sources cannot expand file permissions | internal/config/file.go:37、scripts/deploy/run-host.mjs:53 | OK |
| auth / cklogs retains two permission classes | internal/auth/auth.go、internal/httpapi/server.go 的 checkAuth/requireInternal | OK |
| auth / Jira permissions are project scoped and service isolated | internal/auth/auth.go 的 Authenticate/Allows、internal/jira/server.go | OK；正向矩阵见 W5 |
| auth / Only configured services require their upstream settings | cmd/gateway/main.go 的 loadConfig、internal/httpapi/server.go 的 handleReady | OK |
| auth / External file mounting is the deployment contract | docker-compose.yml、.dockerignore、scripts/test/jira-container-smoke.py | OK |
| auth / Existing cklogs callers can migrate without changing token values | docs/gateway-auth.md、docs/api.md | OK |
| filter / Projects define optional JQL filtering | internal/jql/jql.go、internal/jira/server.go 的 validate | OK；无过滤正向测试见 W5 |
| filter / Project restriction is mandatory in every query | internal/jql/jql.go 的 Scope/Literal、internal/jira/search.go:95 | OK |
| filter / Parent issue filtering protects all child resources | internal/jira/server.go:303、comments.go:30、attachments.go:107 | OK |
| filter / JQL evaluation does not become synchronization policy | Jira handler 路由、docs/jira-api.md | OK |
| filter / Creation enforces non-overridable defaults | internal/jira/create.go:115、internal/strictjson/json.go:31 | W2 |
| filter / Filtered creation requires a supported proof | internal/jql/jql.go 的 Label、internal/jira/fields.go:271、create.go | OK |
| filter / Transient validation failures recover on demand | internal/jira/server.go 的 validate | OK |
| resource / Resource endpoints are scoped by Jira project | internal/jira/server.go 的路由 switch | OK；旧路径测试见 W5 |
| resource / Inputs and output fields have explicit boundaries | internal/jira/search.go:29、strictjson、fields.go:290 | W3 |
| resource / Query pagination is bounded and not a sync cursor | internal/jira/search.go、cursor.go | W3；完整续页测试见 W5 |
| resource / Comment visibility is checked separately from synchronization | internal/jira/comments.go:20、:103 | OK；正常 POST 测试见 W5 |
| resource / Attachment transfer cannot become a URL proxy | internal/jira/client.go:162、attachments.go、server.go:266 | C1（缺响应 deadline） |
| resource / Writes report uncertain outcomes without automatic retry | internal/jira/client.go:92、:106、create.go:173 | OK |
| resource / Jira resource use is isolated and bounded | internal/jira/server.go:217、budget.go、attachments.go:29 | C1、W1 |
| resource / Credentials and diagnostics remain inside the gateway | internal/jira/client.go、internal/httpapi/jira_test.go:40 | OK |
| resource / Caller documentation and deployment examples match the contract | docs/jira-api.md、scripts/deploy/compose-auth.mjs | W4 |
| resource / Field selection cannot expose nested resources | internal/config/file.go 的 ReadField、internal/jira/fields.go:243、:290 | OK |
| resource / Comment cursors identify their parent and operation | internal/jira/cursor.go、comments.go:78 | OK |

## Scenario mapping

| Spec | Scenario | 证据 / 状态 |
|---|---|---|
| auth | Default file and multiple tokens | TestDefaultPath、fixture、TestAuthorizationAndProjection |
| auth | Ambiguous or invalid entry | TestStrictFile、TestRejectNullAndBothDomains |
| auth | Old internal token variable remains | TestLoad、TestEnabledServices |
| auth | External attempts analysis | TestExternalTokenForbiddenOnAnalysis、容器 smoke |
| auth | Internal uses self-service | TestInternalTokenCanCallSelfServiceDelivery |
| auth | One token has multiple projects | 拒绝分支有测试；正向 IT 见 W5 |
| auth | Jira token attempts cklogs access | TestJiraTokensRejectedByEveryCKRoute、容器 smoke |
| auth | cklogs-only deployment | TestEnabledServices |
| auth | Host editor replaces the file | scripts/test/jira-container-smoke.py |
| auth | Existing external caller after deployment migration | 文件 auth 到原 cklogs handler 的代码映射；容器 smoke 的 external 拒绝 analysis 与现有 cklogs 允许测试组合验证 |
| filter | Project has no filter | 实现存在；缺业务成功回归，见 W5 |
| filter | Configured JQL is invalid or cannot be validated | TestInvalidFilterStaysDisabled、TestRecoverySingleFlight |
| filter | Filter contains OR | TestBoundaries；完整业务正向用例见 W5 |
| filter | Client text contains JQL-looking syntax | TestBoundaries、TestSearchInputAndCrossProject |
| filter | Attachment belongs to another issue | TestAttachmentOwnershipAndUpload |
| filter | Parent no longer matches | TestParentAndComments |
| filter | Historical or bot comment on an eligible issue | TestParentAndComments |
| filter | Conflicting label | TestCreateDefaultsAndProof |
| filter | Simple label equality is covered | TestFiniteProof、TestCreateDefaultsAndProof |
| filter | Complex filter is valid for reading but not creation | 创建拒绝有测试；读取成功见 W5 |
| filter | Jira changes data during creation | TestMovedIssueAndFixedCreateMismatch |
| filter | Jira recovers after startup outage | TestRecoverySingleFlight（等价首请求验证故障） |
| filter | Concurrent recovery requests | TestRecoverySingleFlight |
| filter | Custom fields used for creation but not proof | TestCreateDefaultsAndProof |
| resource | Caller supplies a Zammad binding request | 路由检查；缺回归，见 W5 |
| resource | Caller requests unrestricted fields | TestSearchInputAndCrossProject、TestCreateDefaultsAndProof |
| resource | Jira returns extra embedded data | TestAuthorizationAndProjection、TestCapabilitiesPruned |
| resource | Cursor used under another project | TestCursors |
| resource | Cursor expires or configuration changes | TestCursors |
| resource | Restricted comment is requested by ID | TestParentAndComments |
| resource | Upstream attachment redirects outside Jira | TestDownloadLargeAndRedirect |
| resource | Jira commits and the response connection closes | TestLostWriteNoRetry（模拟响应丢失并断言仅一次发送） |
| resource | Jira is saturated | TestBudgetsUTCAndQueue、TestSaturatedJiraDoesNotBlockCKLogs；下载绕开预算见 C1 |
| resource | Upstream error includes credentials or issue content | TestRedirectLimitsAndRedaction、TestJiraAuditRedaction |
| resource | Consumer implements from repository documentation | 文档/路由检查；app-stack 配置缺口见 W4 |
| resource | Linked issue is outside authorization | TestStrictFile、TestUnknownCustomTypesFailClosed |
| resource | Embedded restricted comment | TestAuthorizationAndProjection |
| resource | Customer field remains available without issue creation | TestAuthorizationAndProjection |
| resource | Comment cursor is used for another issue or search | TestCommentCursorCrossParentAndOperation |

## 本轮执行与边界

- `go test ./...`、`go vet ./...`、8 个部署脚本测试、OpenSpec strict validation 均通过。
- 额外临时 Go probes 复现 C1、W1、W2、W3/S1；其中预期安全断言有 3 个失败。临时测试文件已删除，未修改生产代码或任务勾选。
- overlay 假环境变量检查确认 install/149 均未生成 Jira 环境键。只读查看相邻仓库的版本控制 Compose 文件，没有读取真实 env/token 文件或修改其他仓库。
- 本轮未重新执行 Docker build/容器 smoke；其记录在 docs/jira-validation.md，不能覆盖上述新发现的边界。没有访问真实 Jira。
- 真实 Jira 版本、字段、权限及时区仍按设计留待接入核验，不计为本变更未完成任务。

结论：存在 1 项 CRITICAL，归档前必须修复；建议同时处理 5 项 WARNING 并补相应回归。此前“26/26、可归档”的判断需要以上述核验结果为准。
