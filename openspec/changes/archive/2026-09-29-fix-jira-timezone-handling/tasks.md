## 1. 配置与装配

- [x] 1.1 在 `internal/config` 为 `jira.accountCacheTtlSeconds` 增加先失败的测试：省略为 300、显式 0、边界 3600、null/字符串/小数/负数/越界/重复键拒绝，以及错误只包含允许的字段路径。
- [x] 1.2 在 `internal/config/application.go` 等配置路径实现该可选字段的默认值和范围校验；在 `cmd/gateway` 装配到 Jira client，验证既有配置和 cklogs-only 启动不新增 Jira 依赖。

## 2. 账号快照与缓存基础

- [x] 2.1 新增 `internal/jira/account_context_test.go`，使用本地 httptest 和可注入时钟先覆盖：首次读取、跨项目共享、TTL 命中/过期、成功完成时起算 TTL、TTL=0、client 隔离及不可变快照。
- [x] 2.2 新增 `internal/jira/account_context.go`，实现 client 持有的最小身份/timeZone 快照、生命周期、刷新批次、到期判定及上下文指纹/generation；不保存完整 /myself 响应。
- [x] 2.3 先增加并发/取消测试，再实现刷新合并：相同批次含 TTL=0 只请求一次，等待者取消立即退出，刷新拥有者取消后其他请求可重新选主，取消不计为上游故障；测试使用同步信号避免依赖真实长 sleep。
- [x] 2.4 先增加错误恢复测试，再实现上游失败五秒退避、失败不续期、不复用已过期快照、恢复后重建快照；覆盖超时、非 2xx、非法 JSON/身份和原有响应大小限制。
- [x] 2.5 先增加身份/时区分离测试，再实现空白/缺失/未知/Local 时区不可用、禁止回退旧时区或宿主时区、有效身份继续可用，以及时区消费者独立五秒按需重试；覆盖独立重试上游失败时返回 upstream_unavailable、身份仍可在原 TTL 内使用且 TTL/generation 不变。W1 修复补充 TTL=0/1/4/5/300秒的退避、边界恢复、过期身份、身份消费者及 HTTP 回归。
- [x] 2.6 先增加 generation 测试，再实现首次基线、普通续期不变化、稳定身份/时区可用状态变化递增、展示名变化和季节偏移不递增，以及 TTL=0 仍保留历史。

## 3. 复用身份与创建兼容性

- [x] 3.1 修改 `internal/jira/create.go` 中 creation/capabilities 使用共享账号上下文，保留 reporter 的 accountId/name/key 优先级、现有错误行为和最小 person 响应；createEnabled 仍重新计算。
- [x] 3.2 添加 HTTP mock 回归：创建、capabilities 和后续时间搜索共享有效 /myself 快照；timeZone 缺失但身份有效时仍可创建；新的上游快照不得被缓存值反向污染。
- [x] 3.3 将既有排查复现固化为回归：创建的受支持日期字符串原样保留 +08:00/+0800/Z，系统 created/updated 被拒绝；issue.updated、自定义字段及评论时间原样返回；纯日期及正文文字不做统一转换。

## 4. 搜索边界与时区规则

- [x] 4.1 添加先失败的搜索转换用例：UTC/Asia/Shanghai/Asia/Kolkata、等价时刻不同 offset、跨日、不同 offset 的范围顺序、三十天上限、秒/毫秒截到分钟，并捕获实际发往 Jira 的完整 scoped JQL。
- [x] 4.2 重构 `internal/jira/search.go`，将参数校验与按账号 location 格式化分离；无时区等非法参数在 /myself 前拒绝，无时间查询不新增 /myself 调用；保持项目/过滤/排序和所有既有输入边界。
- [x] 4.3 在账号时间转换路径嵌入标准库 `time/tzdata`，按目标日期的完整 IANA 规则转换；增加与宿主 TZ 无关的测试，不使用当前 offset 构造 FixedZone。
- [x] 4.4 添加 America/New_York 回拨两次 01:30、Australia/Lord_Howe 半小时回拨及跨 DST 唯一边界的先失败测试，实现最终分钟边界的多候选歧义检测和 `400 ambiguous_time_range`。
- [x] 4.5 添加 `503 timezone_unavailable` 与 `503 upstream_unavailable` 的接口断言，确认失败时不发出时间业务搜索、不泄漏 JQL/profile/凭据，且不影响无需账号上下文的读取。

## 5. 游标一致性

- [x] 5.1 为 `internal/jira/cursor.go` 和搜索路径增加先失败用例：账号时区改变使旧搜索游标 410、相同上下文续期不失效、时区丢失优先使旧游标失效、账号身份改变、无时间查询比较已知 generation、评论游标不受影响。
- [x] 5.2 将账号上下文 generation 纳入搜索游标签名数据，保留原始带偏移查询和现有操作/项目/配置/父资源/十五分钟期限校验；首次建立基线不使更早的无时间搜索失效。
- [x] 5.3 使用受控并发测试验证：查询取得旧快照后若其他请求刷新出新时区，旧查询返回的 nextCursor 仍绑定旧 generation，下一页明确过期；不得在签发时换成缓存最新 generation。

## 6. 文档、部署与验收

- [x] 6.1 更新 `docs/jira-api.md`：自动账号时区转换、新错误、缓存延迟、DST 歧义、分钟精度、无时区搜索仍拒绝，以及创建/读取字符串保留；去掉把所有日期笼统描述为统一 UTC 的表述。
- [x] 6.2 更新 `docs/gateway-auth.md`、`docs/gateway-auth.example.json` 及相关部署说明：TTL 默认值/范围/0 语义、按需刷新、账号改时区后的重启步骤、回滚前删除新键及恢复旧 UTC 前提；不修改已归档历史记录或另一项变更制品。
- [x] 6.3 更新 `scripts/test/jira-container-smoke.py`、`scripts/test/jira-compose-smoke.py` 相关假 /myself 响应并增加非 UTC 搜索断言；在不安装系统 tzdata 的目标运行镜像中验证内嵌规则可用，测试只使用临时假配置及本地上游。
- [x] 6.4 运行 `go test -count=1 ./...`、`go test -race -count=1 ./internal/jira ./internal/httpapi ./internal/config` 和 `go vet ./...`，检查现有鉴权、父资源核验、写入不重试、队列隔离及脱敏测试仍通过；执行相应容器 smoke 并记录环境和结果。
- [x] 6.5 在本变更 `verification.md` 与 `docs/jira-validation.md` 记录实际通过的检查及未执行项；保留真实 Jira 版本、继承默认时区的 /myself 行为和原始“创建时间少八小时”样本为待接入验收，不宣称 mock 已证明真实创建问题修复。
- [x] 6.6 核对两个 delta spec 的全部场景与实现/测试证据，运行 `openspec validate fix-jira-timezone-handling --strict`；完成实现后进行变更验证，归档留待后续显式操作。
