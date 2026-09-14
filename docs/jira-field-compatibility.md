# Zammad Jira 同步字段兼容核对

状态：源码审计及已实现的字段约束；真实 Jira 字段仍待验收。核对相邻 zammad 仓库的当前源码与 runbook，未读取生产配置或 Jira 实例；动态字段的实际启用集合须接入时确认。本变更不修改 Zammad。

## 读取字段

| 用途 | 当前使用字段 | 网关处理 |
|---|---|---|
| 基础信息和人员投影 | summary、status.name、reporter/creator/assignee 的 accountId/key/name、displayName、emailAddress | 默认读取字段覆盖；status 输出 id/name，人员输出 identity/displayName/email，调用方转换结构 |
| 增量拉取、手动绑定核验 | 顶层 key；updated | key 作为资源身份返回，updated 在默认字段中；不需要将 key 配成动态 field |
| 客户映射及预览 | customer_field_mappings 配置的 email/name 等字段 ID | 按实际配置加入 readFields；标量、选项和人员结构按支持类型裁剪，不开放任意对象 |
| 创建不确定结果核对 | key、description 中的调用方 marker | 显式启用 description；marker 查询和判定由调用方负责 |
| 评论投影及重试核对 | id、body、created、author 的身份/名称/emailAddress；恢复逻辑读取评论 id/body | 专用评论接口保留所需数据，时间输出 createdAt/updatedAt，人员结构转换；不需要 issue.fields.comment |
| 附件上传结果核对 | attachment 中 id、filename、size、content；下载内容计算 hash | 显式启用 attachment，返回 id/filename/size/mimeType；调用方改为按项目/issue/attachment ID 下载，不返回 Jira content URL |
| 可选回链字段 | backlink_field_id 指定字段，读取后 PUT 更新 | 首版不开放任意更新；当前 CS runbook 要求留空 |

源码依据：

- [IssueMetadata](../../zammad/app/services/service/ticket/jira_bridge/issue_metadata.rb)、[IssueProjector](../../zammad/app/services/service/ticket/jira_bridge/issue_projector.rb)：CORE_FIELDS、客户字段及人员/值读取。
- [JiraPullJob](../../zammad/app/jobs/jira_pull_job.rb)、[CustomerPreview](../../zammad/app/services/service/ticket/jira_bridge/customer_preview.rb)：增量与客户预览。
- [IssueCreator](../../zammad/app/services/service/ticket/jira_bridge/issue_creator.rb)：description 搜索恢复及 reporter 选择。
- [CommentProjector](../../zammad/app/services/service/ticket/jira_bridge/comment_projector.rb)、[ArticleRelay](../../zammad/app/services/service/ticket/jira_bridge/article_relay.rb)：评论与附件恢复。
- [ManualBinder](../../zammad/app/services/service/ticket/jira_bridge/manual_binder.rb)：key 核验及可选回链字段。

未发现这些同步调用依赖 issuelinks、subtasks、parent、worklog 或 issue 内嵌 comment。因此拒绝这些 readFields 不影响已审计的核心数据需求。自定义客户字段不能一概禁用；源码接受标量、对象 value/emailAddress/displayName/name 和数组，网关仅覆盖已确认的标量、选项、人员及其数组，其他插件对象需单独评估。

建议核心读取列表为 summary/status/updated/reporter/creator/assignee/description/attachment，再追加实际客户映射字段。readFields 为覆盖列表；只写 description 会丢掉默认字段。

## 创建字段

[IssueFieldResolver](../../zammad/app/services/service/ticket/jira_bridge/issue_field_resolver.rb) 按创建元数据与配置映射动态装配字段，固定 project/issuetype；无法仅从程序常量得出生产完整字段集合。

[CS runbook](../../zammad/doc/runbooks/jira_bridge.md) 列出的 16 个创建字段为：

| 类别 | 字段 |
|---|---|
| 基础字段 | project、issuetype、summary、description、priority、reporter |
| 故障等级、KA 客户、自动分配 | customfield_16408、customfield_16521、customfield_16802 |
| 客户域名、产品数据、服务等级 | customfield_12400、customfield_16404、customfield_16524 |
| 客户用户数、联系资历、客户联系人、来源 | customfield_12401、customfield_16515、customfield_10280、customfield_16500 |

其中若干业务值/选项映射在 runbook 中仍为 BLOCKED，不能声称已在生产启用。网关配置示例中的 summary/description 仅展示形状，不能满足这份完整清单。接入时由 createFields 或 createDefaults 覆盖实际需要的受支持字段；project/type/bot reporter 由网关装配，调用方不能提交覆盖。

首版不支持“匹配 Zammad 创建人后替换 reporter”，保留 bot 身份；也不支持回链字段 PUT。业务自定义字段创建与 JQL 创建证明是两件事：自定义字段可以正常作为已授权创建值，但只有无过滤或单条 labels 等值过滤可开放首版创建。

## 接入结论

收紧嵌套资源字段不与核心读取需求冲突，但网关不是原 Jira REST 的透明替代。调用方后续适配须转换人员/评论结构、按 ID 下载附件，并显式配置 description、attachment、客户字段及完整创建字段。同步、marker、去重、字段映射及适配工作均留在调用方，不属于本仓实现。
