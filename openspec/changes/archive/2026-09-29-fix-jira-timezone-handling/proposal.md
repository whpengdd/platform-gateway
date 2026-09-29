## Why

Jira 搜索目前先把调用者的 RFC3339 时间转成 UTC，再生成不含时区的 JQL 日期字面量；当 Jira 对接账号不是 UTC 时，实际查询窗口会偏移。账号时区可以通过 `/rest/api/2/myself` 获取，但逐次读取会增加上游请求，因此需要将正确的时间转换与有界账号信息缓存一并设计。

## What Changes

- 带时间范围的搜索按调用者提供的偏移解析绝对时刻，再按 Jira 对接账号的有效 `timeZone` 生成分钟精度 JQL，取消必须把服务账号设为 UTC 的接入前提。
- 为 `/myself` 的最小账号身份和时区建立进程内、Jira client/账号级共享缓存；默认 TTL 300 秒，允许配置，按需刷新、合并并发请求并在失败后短暂退避。
- 创建与 capabilities 复用账号身份缓存；缺失或不支持的时区仅阻断需要转换的时间搜索，不因时区不可用而禁用身份有效的创建。
- 检测到账号身份或时区上下文变化时失效旧搜索游标；明确 TTL 内变更检测延迟，保留原有分页期限和非快照语义。
- 对夏令时回拨中不能由无时区 JQL 唯一表达的边界明确拒绝，提供完整时区规则数据，避免依赖宿主机或容器本地时区。
- 保持创建日期字符串、工单/评论时间字符串原样传递；补充回归测试及调用方、配置、部署验证文档。
- 本次不增加 `/field` 或 createmeta 缓存，不缓存授权/可见性/父资源核验，不修改 Jira 账号设置，不调整已有工单时间或 Zammad 页面/评论展示。

## Capabilities

### New Capabilities

- `jira-account-context`: 定义账号身份和时区信息的获取、最小化缓存、TTL 配置、并发刷新、失败隔离及上下文版本。

### Modified Capabilities

- `jira-resource-api`: 定义带时区时间范围的转换、无歧义边界、搜索游标上下文校验，以及创建和读取时间值的保留行为。

## Impact

- 涉及 `internal/jira/client.go`、`create.go`、`search.go`、`cursor.go`、`server.go` 和对应测试；新增账号上下文与时间转换辅助逻辑。
- 配置增加可选 `jira.accountCacheTtlSeconds`，涉及 `internal/config` 的类型/默认值/严格校验、`cmd/gateway` 装配和示例配置；已有配置省略时使用 300 秒。
- 使用 Go 标准库时区能力并嵌入 `time/tzdata`，不增加外部 Go 依赖或 Redis，不引入后台轮询。
- HTTP 路径与成功响应结构不变；增加明确的时区不可用/边界歧义错误，并在检测到上下文变化时返回既有 `410 cursor_expired`。
- 更新 `docs/jira-api.md`、`docs/jira-validation.md`、配置文档和本地 mock smoke；真实 Jira 验收仍与本地 mock 结果分开记录。
