# Verification: fix-jira-timezone-handling

验证日期：2026-09-29。范围为本仓实现、本地 HTTP/TLS mock 和 Colima 容器，不代表生产 Jira 验收。

## Archive record

2026-09-29 按用户确认归档至 `openspec/changes/archive/2026-09-29-fix-jira-timezone-handling/`，保留 `.openspec.yaml`。归档前已将8条账号上下文需求、新增的4条时间处理需求及分页规则修改同步至主规格；其余10条原 Jira API 需求保持完整。两份同步规格及全部主规格均通过严格校验，25项任务完成、W1关闭。归档不代表 Git 合并、发布或真实 Jira 验收。

## Latest verification: W1 remediation

MR 提交前已对包含 W1 修复的最终工作区再次运行全量 Go 测试、相关包竞态检查、vet 和全部主规格严格校验；重新构建 `platform-gateway:jira-timezone-smoke` 并运行 TLS container smoke 及 self/install/149 × bearer/basic 六组 Compose smoke，全部通过。下方“未重复容器”的说明仅记录此前 W1 修复当时的验证范围。

本节记录用户要求修正 W1 后的最新结论，优先于下方实施阶段记录。W1 已修复，并增加永久回归测试及明确的规格场景。

| Dimension | Status |
|---|---|
| Completeness | 25/25 任务完成；13 条 requirement、47 个 scenario 已核对 |
| Correctness | 13 条均有实现证据；W1 的短 TTL 组合回归已通过 |
| Coherence | 5 秒时区退避与成功快照 TTL 已分离，符合更新后的明确契约 |

### CRITICAL

无未实现的完整能力或 CRITICAL 问题。

### Resolved WARNING W1: Short or disabled cache TTL bypasses timezone retry backoff

当前无待处理 WARNING。下列触发条件与影响记录修复前问题，供追溯。

- **位置**：`internal/jira/account_context.go:73`、`:83`；对应 `specs/jira-account-context/spec.md:67` 的身份/时区独立可用性要求，以及任务 2.5。
- **触发条件**：配置 TTL=0 或小于5秒；/myself 返回有效身份但缺失/不支持 timeZone；后续时间消费者在5秒内再次访问。默认300秒不受此复现影响。
- **修复前行为**：`timezoneRetryAfter` 虽被设置为5秒后，但检查它需要 `fresh`。TTL=0 时 fresh 永远为 false，短 TTL 到期后也为 false，因此时区请求会提前再次访问 /myself。
- **修复前影响**：无效时区仍被拒绝，没有观察到错误的业务搜索或过期身份被输出；但会增加故障期上游请求，违背文档中时区独立5秒退避的承诺。
- **复现证据**：临时 Go overlay 新增 `TestVerifyTimezoneRetryIndependentOfShortTTL`，先在 t=0 读取缺失时区的有效账号，再推进测试时钟1秒并再次读取。TTL=0 与 TTL=1秒均得到 `calls=2, want=1`；TTL=300秒对照得到1次。复现结束后临时文件已清理，未向仓库加入失败测试。
- **已完成修正**：`account_context.go:83` 不再以 fresh 作为时区退避的前提；缓存过期时只返回错误及已观察到的 generation，不返回过期 Bot。`search.go:126` 根据 timezone_unavailable 错误类型识别可比较的 generation，保留游标失效优先级。身份消费者仍按自身 TTL 工作，上游读取故障退避仍优先。
- **永久测试**：`TestAccountTimezoneBackoffIndependentOfTTL`、`TestAccountIdentityRefreshDuringTimezoneBackoff`、`TestAccountShortTTLTimezoneRetryTransportFailure`、`TestSearchShortTTLTimezoneBackoff`，均参数化覆盖 TTL=0、1、4、5、300秒。覆盖1秒/4秒/4.999秒不重试、5秒边界恢复、过期身份不返回、仅身份消费者可刷新、上游失败错误优先级及 HTTP 搜索故障隔离。

原临时复现的核心步骤如下；现已固化为上述永久测试并通过：

```go
h := newAccountHarness(t, time.Second) // 对照分别使用 0、5*time.Minute
h.profile.Store(`{"name":"bot"}`)
_, err := h.client.accountContext(context.Background(), true)
assertAccountError(t, err, "timezone_unavailable")
h.advance(time.Second)
_, err = h.client.accountContext(context.Background(), true)
assertAccountError(t, err, "timezone_unavailable")
if h.calls.Load() != 1 {
    t.Fatalf("timezone retry within 5s: calls=%d want=1", h.calls.Load())
}
```

### SUGGESTION

无独立于 W1 的新增建议。真实 Jira 接入验收仍按下方已声明范围保留，不与本地实现符合性混为一谈。

### Checks repeated in this verification

- `go test -count=1 ./...`：通过。
- `go test -race -count=1 ./internal/jira ./internal/httpapi ./internal/config`：通过。
- `go vet ./...`：通过。
- `openspec validate fix-jira-timezone-handling --strict`：通过。
- `git diff --check`：通过。
- W1 永久回归先观察到 TTL=0/1/4秒失败，修正后0/1/4/5/300秒全部通过；HTTP、身份消费者、错误优先级和游标回归通过。
- 容器镜像和 smoke 的实现阶段成功记录保留；本次聚焦缓存重试与搜索错误分支，以全量 Go 回归及竞态检查验证，未重复运行容器，也未执行真实 Jira 查询或写入。

**最新评估：W1 已关闭，没有待处理 CRITICAL 或 WARNING，可继续归档流程。真实 Jira 验收及已声明的缓存/分页限制仍保留。**

## Implementation-stage record

以下为初次实施与验证记录；其通过项仍成立，后续 W1 的发现与修复以上方最新记录为准。

## Completeness

- 实施任务：25/25 已完成；未执行归档或部署到真实 Jira。
- 两份 delta spec 共 13 条 requirement、46 个 scenario；实现、回归和文档证据按下节归组核对。
- 配置、账号缓存、搜索转换、DST 边界、分页版本、创建/读取兼容、容器与文档均有对应产出。

## Correctness: requirement and scenario evidence

1. **账号共享与隔离**：`internal/jira/account_context.go`、`client.go`。`TestAccountSnapshotLifetimeAndIsolation` 验证最小字段、复制隔离和不同 client；`TestAccountContextSharedByCapabilitiesAndCreation`、`TestCreationCapabilitiesAndSearchShareAccountSnapshot` 验证跨项目及三种消费者共用一次读取。
2. **TTL 配置**：`internal/config/application.go:114`、`cmd/gateway/main.go:134`。`TestJiraAccountCacheTTL` 及同文件默认值用例覆盖缺省 300、显式 0、3600、非法类型/范围/重复键；main 装配 HTTP 测试覆盖默认/零/最大 TTL、启动不主动读取和 cklogs-only。
3. **按需与合并刷新**：`account_context.go:65`。`TestAccountRefreshCoalescesIncludingZeroTTL`、`TestAccountWaitingCallerCancellation`、`TestAccountCancelledOwnerAllowsLiveWaiterToRefresh` 覆盖共享在途批次和取消；实现无后台 goroutine/timer 轮询，空闲时只保存状态。
4. **失败不服务过期数据**：`TestAccountRefreshFailuresBackOffWithoutStaleData` 覆盖 HTTP 403、超时、非法 JSON/身份和超大响应；五秒后恢复，旧快照不续期。`TestSearchValidationAndTimezoneFailureIsolation` 覆盖普通读取及无时间查询继续可用。
5. **身份与时区隔离**：`TestAccountTimezoneUnavailableIsIndependent`、`TestAccountTimezoneRetryFailurePreservesIdentityLifetime`、`TestAccountTimezoneLossDoesNotReuseOldLocation` 覆盖空白/缺失/未知/Local/非字符串、恢复及不回退旧时区。`TestAccountMissingTimezoneStillAllowsCreation` 通过真实 HTTP handler 调用 capabilities 和 POST /issues，确认缺失或 Local 时区下创建仍返回 201。
6. **上下文 generation**：`TestAccountGenerationTracksContext` 覆盖身份、时区状态变化、普通续期与展示名变化；`TestSearchCursorUsesQuerySnapshotDuringRefresh` 覆盖旧查询只签发旧 generation；TTL=0 保留上下文历史。
7. **缓存不代替权限/字段检查**：原有 `TestAuthorizationAndProjection` 等鉴权、createDefaults、父资源、visibility、附件、写后核验和不重试用例通过；新增 `TestCommentContinuationStillChecksParentAfterTimezoneChange` 经 HTTP 验证隐藏评论裁剪、续页成功及父资源失效后的 404。字段 schema 和 createmeta 未加入缓存。
8. **缓存运维合同**：`docs/jira-api.md`、`docs/gateway-auth.md`、示例 JSON 明确 TTL、变更检测延迟、重启失效、TTL=0 非原子、账号切换及回滚移除新键的要求。
9. **查询分页绑定**：`internal/jira/cursor.go:12`、`search.go:70`；`TestSearchCursorAccountChanges` 覆盖变更/缺失/同值续期/身份变更/运输失败。`TestKnownExpiredSearchCursorDoesNotRefreshProfile` 确认已知失效优先返回 410；`TestSearchCursorInitialBaselineAndNoTime` 覆盖首次基线、无时间查询和评论隔离；既有签名/跨项目/跨操作/过期测试保留。
10. **调用者偏移与校验**：`search.go:21`，`TestSearchUsesAccountTimezone` 与 `TestSearchValidationAndTimezoneFailureIsolation` 覆盖 +08:00/Z/-04:00 等价输入、绝对时间排序、无时区拒绝、超过三十天拒绝及校验发生在 /myself 前。
11. **账号当地 JQL**：`search.go:46`；捕获完整 scoped JQL 的用例覆盖 Shanghai、UTC、Kolkata、跨日、季节偏移、秒与毫秒舍去；同一用例在 `TZ=Pacific/Honolulu` 的新测试进程中通过。原有 filterJql、排序和无时间查询测试通过。
12. **时区规则与回拨歧义**：`account_context.go` 嵌入 time/tzdata；`search_time.go:12` 对整个分钟与回拨本地重复区间求交。`TestSearchRejectsRepeatedLocalMinutes` 覆盖 New York 两次重复时间、Lord Howe 半小时回拨、Paris 1911 部分分钟重复，以及 Colombo/Kolkata/Tbilisi/St_Johns 历史秒级变更附近的正常分钟和跨 DST 唯一端点。两个容器 smoke 在没有系统 zoneinfo、ZONEINFO/GOROOT 不存在且 TZ=UTC 的条件下通过非 UTC 查询。
13. **原时间字符串保留**：`TestCreateAndReadPreserveTimestampStrings` 覆盖 +08:00/+0800/Z、纯日期、自定义字段、description、评论及 updated 的原字符串，以及 created/updated 创建字段拒绝；创建链不做统一小时补偿。

## Executed checks

全部以下检查成功，除专门列出的初始失败复现外：

- `go test -count=1 ./...`：全部包通过。
- `go test -race -count=1 ./internal/jira ./internal/httpapi ./internal/config`：通过，无竞态报告。
- `go vet ./...`：通过。
- `TZ=Pacific/Honolulu go test -count=1 ./internal/jira -run 'TestSearchUsesAccountTimezone|TestSearchRejectsRepeatedLocalMinutes'`：通过。
- `go test -race -count=1 ./internal/jira -run TestAccountMissingTimezoneStillAllowsCreation`：补充 HTTP 回归后通过。
- `docker --context colima build -t platform-gateway:jira-timezone-smoke .`：按仓库 Dockerfile 的 Go 1.23 builder 构建成功。
- `python3 scripts/test/jira-container-smoke.py colima platform-gateway:jira-timezone-smoke`：TLS mock、账号缓存、等价偏移、上海查询、无系统时区库、认证/项目/代理隔离、配置原子替换及 token 轮换均通过。
- `python3 scripts/test/jira-compose-smoke.py colima platform-gateway:jira-timezone-smoke`：self/install/149 × bearer/basic 六种组合全部通过。
- 两个 smoke 脚本 `py_compile`、`openspec validate fix-jira-timezone-handling --strict`、`git diff --check`：通过。

本机 Go 1.27.1，Docker Server 29.5.2，Compose 5.5.0。容器与 mock 只使用临时假配置/凭据，临时容器通过 finally 清理；没有调用真实 Jira 或修改真实账号设置。

## Coherence and review

实现保持单 client 共享缓存、同步拥有者/可取消等待者、已有队列和 deadline、无外部缓存依赖，以及原有业务授权边界。设计补充了已知失效游标优先级与整个分钟区间的历史回拨检测，与规格保持一致。

已观察到的测试失败及其修复证据：

- 初始账号共享回归观察到 /myself 三次而非一次；共享缓存实现后通过。
- 初始上海搜索捕获 UTC 02:00 而非账号当地 10:00；账号时区转换实现后通过，重复小时也由接受变为明确拒绝。
- 独立审查发现旧游标已知失效后仍被下一次 /myself 故障覆盖为 503；先加入失败复现，再调整为刷新前比较 generation，回归现在返回 410 且不再请求 /myself。
- 独立审查发现历史秒级变更导致部分分钟漏判或普通分钟误报；Paris 回归先失败，再改为整个分钟与回拨重复区间求交，重复及非重复历史用例全部通过。
- Compose 首次运行在最近删除的 bind 源检查上失败：主机文件已经不存在，但 Compose 短暂确认容器启动，应用日志随后报告配置 ENOENT。这符合共享文件系统/启动确认时序差异，不能据此当作网关成功启动。测试保留部署脚本对已删除配置的拒绝，再用从未创建过的路径检查 Docker 缺失挂载拒绝，六种组合稳定通过；未放宽生产部署行为。

实施阶段独立规格符合性与代码质量复查当时未发现未处理的 P1/P2；后续显式复核发现并已修复的 W1 见上方最新记录。

## Live acceptance and acknowledged limits

- 未执行真实 Jira 验收：目标版本的 /myself.timeZone 在继承默认时区时的返回、命名时区规则版本、真实 JQL 结果及权限仍需接入环境核对。
- 缓存有效期内的账号设置变化可能尚未被发现；/myself 与 search 也不是原子操作。已在文档中说明 TTL、重启和回滚策略。
- 保留分钟精度和非快照分页，不承诺秒级精确结果；重复本地分钟边界明确拒绝。
- 原始“创建工单后少八小时”缺少具体字段和原始时间样本，不能宣称此次搜索修复解决了该显示/业务现象。
- 已完成规格同步和归档，见上方归档记录；尚未合并或发布。
