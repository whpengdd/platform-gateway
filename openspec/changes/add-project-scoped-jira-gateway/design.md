## Context

当前 Go 1.23 网关仅实现 cklogs，使用 external/internal token 环境变量、net/http、FIFO 队列及 JSONL 审计。internal 是 cklogs 权限超集，现有代码对重复 token 采用 internal 优先。Docker 以 UID 65532 运行；宿主端口映射 loopback，程序默认监听 :8091。用户要求将所有入站 token 统一放 ./config.json，并新增按项目/JQL 过滤的 Jira 中转。

已有 docs/jira-bridge-design.md 审计了 Zammad 的15种业务 HTTP 调用和未使用 serverInfo。此前的绑定名单、同步调度、异步账本以及 filters/equals 都是已被用户后续决策替代的设计，不能继续实现。当前 change 的规格优先，配套 docs 须统一更新为本设计。

## Goals / Non-Goals

**Goals:**
- 一个文件配置 cklogs/Jira token；Jira token 可授权多个项目。
- 每个项目以可选 JQL 条件收窄可访问 issue，所有资源入口执行同一授权。
- 面向调用方提供有界、裁剪、固定路径的 Jira 资源 API，保留 cklogs 合同。
- 创建默认值不被覆盖，写入前确认其受支持过滤条件可满足。

**Non-Goals:**
- 不修改 Zammad，不决定同步方式、周期、历史窗口或评论发布规则。
- 不提供 ticket 绑定、webhook、changes、operations、数据库、幂等保证或自动写入重试。
- 不建设管理后台、热更新、多 Jira 实例、动态角色或 token 级读写 scopes。
- 不开放任意上游 URL/JQL 请求参数、删除、transition 或任意 issue 更新。

## Decisions

### 1. 统一 JSON 文件，最小授权模型

默认工作目录 ./config.json，可选 GATEWAY_AUTH_FILE 覆盖路径。根键为 tokens 和可选 jira；每条 token 只设置 cklogs 或 jiraProjects 其中之一。旧 token 环境变量非空即启动冲突，不自动合并。配置拒绝重复 JSON 键、未知键、空凭据、重复 token、通配项目和非法引用，错误不包含原始配置片段。

```json
{
  "tokens": [
    {"token":"<ck-external-token>","cklogs":"external"},
    {"token":"<ck-internal-token>","cklogs":"internal"},
    {"token":"<jira-token-A>","jiraProjects":["CS","IT"]},
    {"token":"<jira-token-B>","jiraProjects":["CS"]}
  ],
  "jira": {
    "projects": {
      "CS": {
        "filterJql":"labels = zammad",
        "issueTypeId":"10504",
        "createDefaults":{"labels":["zammad"]},
        "createFields":["summary","description"],
        "readFields":["summary","description","status","updated","reporter","creator","assignee"]
      },
      "IT": {}
    }
  }
}
```

这是配置形状示例，实际创建仍需 Jira 元数据验证。issueTypeId/createFields 为启用创建的必要配置，readFields 有程序默认值，createDefaults/filterJql 可选。无 Jira token 则不启用 Jira，不要求上游凭据；无 cklogs token 则不要求 CK 凭据。健康检查不返回授权清单。

替代方案是继续分散环境变量、增加 client/scopes 和多层引用；用户明确要求简单，故不采用。同项目 token 拥有相同受限业务能力，无同项目不同调用方隔离语义。

### 2. JQL 委托 Jira，项目约束由网关强制

filterJql 是仅由受控配置提供的条件表达式，不含 ORDER BY；缺省或空白视为没有附加条件。支持目标 Jira 可接受的条件，不承诺跨版本/插件通用。网关构造 `(project = "CS") AND (<filterJql>) AND (<受控查询条件>)`，排序单独追加。只接受完整且括号/引号平衡的条件片段，禁止逃出包裹层；所有客户端查询值经独立字面量编码，不能直接拼接为语法。

语义验证交给 Jira search 的查询校验；配置加载先做结构检查，项目在 JQL 未经上游成功验证时不得服务。上游暂时不可用使该项目返回503，Jira 明确判定条件非法使项目禁用并记录脱敏配置错误；绝不能降级为无过滤。临时失败后退避 5 秒，下一次请求按需重新验证，同项目最多一个在途验证，其余请求返回 503；成功后恢复服务，不需要重启或后台任务。明确非法的条件保持禁用，修改配置并重启后重新验证。无需解析并执行任意 JQL 的本地 AST。

列表直接查询强制项目+过滤后的集合，裁剪返回；验证响应的实际项目字段，异常归属不下发。单 issue 用稳定 ID/合法 key 加入同样搜索条件检查命中，能直接使用该查询的最小字段结果时避免重复 GET。评论/附件先检验父 issue，再使用父 issue ID 查询子资源；不能因子资源 ID 有效就放行。内部恢复需要的查询也不扩大项目范围。

配置允许 OR/函数等合法 JQL，但始终在括号内。currentUser() 的用户是网关 Jira 账号；函数依赖和索引延迟属于查询语义。JQL 是访问过滤，不提供字段变更后即时撤权或检查/写入原子性的保证；若要求更强边界需 Jira 原生权限配合，不另建本地等值授权引擎。

### 3. 创建使用固定默认值与保守可行性检查

createDefaults 是强制字段值，不是客户端可覆盖的建议值。若客户端提交同键不同 JSON 值，返回400 create_default_conflict；相同值可接受，但必须属于已配置默认字段或 createFields。路径 project、配置 issuetype、bot reporter 由网关固定；禁止 defaults 或客户端覆盖 project/issuetype/reporter/assignee/security。现有 issue 的过滤属性没有写入入口。

filterJql 为空时，默认值和白名单按 Jira 创建元数据验证即可。有过滤时，首版仅支持单条 `labels = 固定字符串`（允许整体外层括号）；createDefaults.labels 必须包含该 label。有限校验器须完整消耗输入并正确处理引号/转义，不接受正则子串命中。AND、OR、cf[n]、函数、历史条件等其他合法 JQL 仅禁用创建，不影响现有 issue 操作。首版不实现自定义字段创建证明或通用 JQL AST。

createDefaults 仍可提供其他受支持的创建字段，createFields 仍可允许业务自定义字段；限制的是从 JQL 证明创建合规的语法，不是禁止自定义字段写入。默认值、调用方字段和 Jira 默认值须能满足必填项；元数据不可得或类型/必填约束无法满足时关闭创建。

创建成功后校验返回身份/实际项目，并尽可能读取实际固定字段验证；不要因为搜索索引尚未更新就重复创建。无法确认实际项目或固定字段的结果返回 outcome_unknown，不自动修正或重发。服务不承诺 Jira 自动化/工作流在创建后不会改变条件字段。

### 4. 固定资源 API，保持同步逻辑在调用方

业务前缀 P=/v1/jira/projects/{projectId}，projectId 使用大写 Jira key。

| 方法 | 路径 | 合同 |
|---|---|---|
| GET | /v1/jira/projects | token 获准的项目 key，仅配置摘要 |
| GET | P/capabilities | 裁剪字段定义、bot 最小身份、创建可用性/原因、附件限制 |
| POST | P/issues/search | 仅 updatedFrom/updatedTo、issueKeys、text、limit 或续页 cursor |
| GET | P/issues/{issueKey} | 白名单 fields |
| POST | P/issues | fields 白名单 + 固定 defaults，成功201 |
| GET/POST | P/issues/{issueKey}/comments | 读分页/写 body，成功200/201 |
| GET | P/issues/{issueKey}/comments/{commentId} | 归属及可见性校验后的评论 |
| POST | P/issues/{issueKey}/attachments | 单 file multipart，成功201 |
| GET | P/issues/{issueKey}/attachments/{attachmentId}/content | 经父 issue 校验的文件流 |

不再提供旧提议 /tickets、/binding、/changes、/operations。外部接口明确定义自己的响应模型，不让 Jira 动态字段或错误原文无约束透传。readFields 省略使用 summary/status/updated/reporter/creator/assignee；配置时精确允许字段名，不支持 * 或展开；人员裁剪 identity/displayName/email，附件裁剪 id/filename/size/mimeType。默认不下发 group/role 限制评论；不因 bot、年龄或业务 marker 过滤正常评论。

搜索时间参数可省略；提供时成对且跨度≤30天，只限制一次查询量，不是同步周期策略。text 为转义后的 Jira 固定全文搜索条件；客户端不提交 JQL。首请求 limit 默认50、最大100，issueKeys 最大100，text 最大256字节。续页只含 cursor，绑定项目、查询、分页位置、配置摘要和15分钟有效期；使用进程随机密钥认证，重启失效，无数据库。游标必须绑定操作类型；评论游标还绑定稳定父 issue ID，每页重新核对父 issue 和评论可见性。跨项目、跨 issue、跨接口复用均返回 400 invalid_cursor；时间过期或配置变化返回 410 cursor_expired。不返回未授权计数；分页在 Jira 数据变化时不保证快照一致性。



字段名白名单不能授权其包含的其他资源。程序仅支持显式响应投影：summary/description/updated 为标量，status 仅 id/name，人员仅 identity/displayName/email，attachment 仅 id/filename/size/mimeType。禁止将 comment、issuelinks、subtasks、parent、worklog 或未知复杂对象加入 readFields；评论仅通过专用接口返回。customfield_<数字> 可按项目显式允许，但仅支持已确认的标量、选项 id/value、人员最小结构及这些类型的数组，禁止任意递归 JSON 透传；无法确认类型的配置拒绝，不静默放宽。字段类型在元数据验证中确认，暂不可得时相关读取返回 503。此元数据检查独立于创建能力，无需配置 issueTypeId 才能读取已支持的客户字段。

Zammad 字段核对见 docs/jira-field-compatibility.md。读取配置需按需加上 description、attachment 和实际 customer_field_mappings 字段；创建白名单需覆盖实际必填字段，不能将示例 summary/description 当作 CS 完整创建配置。

### 5. 无自动写入重试，错误和资源预算有界

200/201 为同步请求结果，不返回202后台 operation。明确上游拒绝返回脱敏 upstream_rejected；写入可能已发送但结果不确定返回502 outcome_unknown。队列/请求取消导致尚未发送则503；读取上游不可用503。网络断开时即便客户端没收到错误也不能推断未写入，调用方负责核对。只读 search POST 不受写入不确定性规则影响。

首版默认：JSON 256 KiB、描述/评论32 KiB、文件10 MiB/multipart 11 MiB、上游 JSON 4 MiB；Jira 2个执行槽、32个等待位、排队及操作各30秒，内部请求串行且整体有 deadline。每项目读取60次/分钟、写入20次/分钟、创建100次/UTC日、上传200 MiB/UTC日，项目内多 token 共用；单进程内存计数，重启重置，文档明确不提供持久配额。下载受读取频率及每次字节限制。

上游固定 HTTPS/context path，独立 Transport 不隐式使用 HTTP_PROXY，不跟随重定向，不关闭证书验证。附件从父 issue 返回的同源 secure/attachment 地址读取，校验路径编码、userinfo、query 及归属，不接受客户端 URL。流式上限和 deadline 防止大响应占满资源。

Jira 和 cklogs 分别限并发，审计记录 backend/项目/token 指纹/操作/耗时/上游状态及拒绝原因，不记录凭据、请求正文、附件或 JQL 字段值。保留全局 CIDR 限制，不信任任意 X-Forwarded-For。

### 6. 文件挂载和模块装配

Compose 显式 working_dir=/，宿主 ./config.json 以文件 bind mount 只读挂到 /config.json，create_host_path=false；UID 65532 必须可读。真实 /config.json 纳入 .gitignore/.dockerignore，示例使用占位符。编辑器替换文件后执行 compose up -d --force-recreate --no-deps platform-gateway，重新绑定宿主当前文件，无需构建镜像。

配置模块输出互斥的 cklogs 档位或 Jira 项目集合，所有 handler 分别检查服务域，不能把任何有效 token 当作 external。ready 判断启用模块的必需配置，Jira 条件验证状态通过项目 capabilities/错误体现，不能泄露项目列表给健康检查。当前部署脚本须移除旧变量并传入挂载/路径。

## Risks / Trade-offs

- [JQL/元数据受 Jira 版本和插件影响] → mock 覆盖适配器，实际接入前用目标实例只读校验；未知条件拒绝服务或禁用创建，绝不忽略。
- [JQL 索引延迟及写入竞态] → 保留 Jira 最小权限、每次父 issue 检查、无旧授权缓存承诺；明确它不是原生权限替代。
- [复杂 JQL 的项目不能创建] → capabilities 给固定原因；运维简化条件及 defaults，读取和既有 issue 操作不受创建证明限制。
- [无幂等账本导致超时后重复提交风险] → 网关禁止自动写入重试，定义 outcome_unknown，由调用方恢复。
- [配置包含明文 token] → 文件最小读取权限、只读 mount、不进 Git/镜像/日志；进程使用摘要及常量时间匹配。
- [配置迁移是 breaking change] → 保持旧 token 值和 Bearer 合同，明确冲突及迁移步骤，不静默升级 external。

## Migration Plan

1. 迁移旧 cklogs token 到 JSON 各独立条目；跨内外档重复值先拆分并同步对应调用方。
2. 在宿主准备 config.json，新增 Jira 项目/凭据和 JQL/defaults；真实值不写入仓库。
3. 发布新镜像/二进制并移除旧入站 token 变量，挂载文件后启动；验证 cklogs 两档、Jira 跨域拒绝、项目及过滤路径。
4. 调用方适配在各自仓库完成；实际切换时撤销旧 Jira 凭据、阻断直连。此 change 不部署生产或修改调用方。
5. 回滚仅恢复可控的旧 cklogs 部署；暂停 Jira 中转，不自动恢复 Zammad 直连宽权限凭据。备份配置文件，不创建同步数据库。

## Open Questions

没有需要阻塞产物生成的产品决策。实际 Jira 版本、认证方式、目标项目/type、创建必填项及过滤字段类型需在环境核验时确认；先用明确的 labels 等值样例完成可重复测试，不能据此宣称线上创建已验证。

## 独立复评后的实现细化

Jira FIFO 限制上游调用；向客户端发送响应从首次输出起另有 30 秒连接写 deadline，不依赖已结束的上游 context，也不强制慢客户端下载占用 Jira 上游槽。multipart 11 MiB 上限覆盖完整 HTTP body，包括结束边界后的数据；以有读取 deadline 的有限额 reader 在 POST Jira 前检查完毕。

数字值仅在 float64 解码/JSON 重编码不改变数值时支持；配置和客户端输入遇到不能保真的数字均拒绝，不静默舍入。精确往返比较限制数字字面量不超过 1024 字符、指数在 -2048 至 2048。上游遇到不能保真的数字按读取不可用或写入 outcome_unknown 处理。

搜索时间字段按存在性校验：必须成对且非空有效。Jira 日期字面量使用分钟精度，bot 时区须为 UTC，调用方处理分钟边界重叠。issueKeys 空数组保持与省略字段相同的语义。

install/149 的临时 Compose override 包含四个 Jira 上游环境变量的引用；由 Compose 解析环境，不将凭据展开到临时文件。
