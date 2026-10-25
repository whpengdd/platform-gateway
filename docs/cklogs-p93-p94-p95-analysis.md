# CKLogs P-93 / P-94 / P-95 改造方案分析

日期：2026-10-24。分析基线：platform-gateway HEAD `a358f2a`，与工单注明的生产提交一致。本次仅新增本文，不修改业务代码、不提交、不访问生产查询接口、不变更 Linear。

## 1. 结论与证据边界

建议实施顺序为：**P-93 可观测性及整链路预算核对 → P-94 有界收件人兜底 → P-93 慢查询实测优化及有条件批处理 → P-95 DATRANS 全量分页**。P-94 可与 P-93 查询诊断交叉推进，但应先明确查询形态和预算，避免新增宽扫；P-95 保持依赖 P-93 的性能结论。

三项不宜通过统一调大 `HardSize` 或超时解决：P-93 的失败发生在小候选明细，P-94 是账号约束被删除，P-95 则缺少真正的跨页取回与完整性判断。

工单来源（本次已读取完整工单；无评论/附件）：

- [P-93](https://linear.app/vincent2026/issue/P-93)：四次明细查询 60s 超时，候选 10/10/10/85；前置 DATRANS 7.6–8.9s、MTA 精确计数 6.4–8.3s、候选计数 1.3–2.3s，总计 75.3–78.4s。
- [P-94](https://linear.app/vincent2026/issue/P-94)：OCT-030 PROXY 候选 4,870,047，OCT-032 MTA 候选 61,829。
- [P-95](https://linear.app/vincent2026/issue/P-95)：P-93 子单；OCT-009 DATRANS 单次 55.95s、total=529，超过 500 后整单拒绝。

以上生产数字来自工单，未在本次重新获取原始生产 JSONL。代码可直接证实调用顺序、DSL 和拒绝条件；存储侧扫描/排序/索引开销仍是待验证假设。三单当前均 Backlog，描述注明暂缓待排期；本文是后续实施设计，不代表启动开发。

## 2. 调用链与代码地图

下列路径均相对仓库根目录，行号对应本次基线。

| 环节 | 定位 | 当前行为及影响 |
|---|---|---|
| 自助入口 | `internal/httpapi/handlers.go:15`、`validate.go:353` | 校验 account；inbound 映射为 Recipient，outbound 映射为 Sender；peer 域名转 SenderDomain/RecipientDomain |
| 执行预算与队列 | `internal/httpapi/server.go:187`、`internal/cklogs/service.go:5` | FIFO 取得槽后开启整单预算，默认 90s；OpTimeout 为 max(90s, 单次 timeout) |
| DATRANS 主链 | `internal/cklogs/delivery.go:15` | 单页 500 → 完整性检查 → 本地条件过滤 → 聚合；聚合零命中才进 MTA，inbound MTA 零行再进 PROXY |
| MTA 计数/兜底 | `internal/cklogs/mta.go:47` | 精确计数；零命中时清空 Recipient 重计数、取候选、本地完整地址匹配；self-service requireComplete=true |
| PROXY 兜底 | `internal/cklogs/proxy.go:5` | 同样删除 Recipient；候选 >500 直接 unresolved，不查询明细 |
| DSL | `internal/cklogs/dsl.go:165`、`:246` | Dataset 使用 term(to/from/mailfrom)；MTA sender 用 term，rcpt/subject 用 match_phrase；明细 timestamp 降序 |
| HTTP 与响应解码 | `internal/cklogs/client.go:79`、`:164` | 单次独立 timeout 默认 60s；Query 只消费 responses[0]；响应体读取上限 8MiB |
| 总数解析 | `internal/cklogs/client.go` 的 `readTotal` | 只取 value，不保留 relation；不解析 timed_out 或分片失败等完整性信号 |
| 审计 | `internal/auditlog/record.go:50` | 每 HTTP 调用耗时/索引/条数；无业务阶段或最终 DSL；msearch 的 OK 先于子响应错误检查 |
| 本地过滤与聚合 | `internal/cklogs/aggregate.go:182`、`:205`、`:245` | account/对端域、主题等本地核对；按邮件/对端折叠后分页 |

重要差异：`BuildMsearchBody` 确实使用 `pageSizeFrom/offsetFrom`，但 **`BuildDatasetBody` 明细分支仍固定 size=500，没有 from**。DATRANS 走后者，因此直接循环 QueryOptions.Offset 会反复请求第一页。

## 3. P-93：小候选仍超时

### 3.1 根因层次

已证实：delivery 串行执行 DATRANS 明细，聚合为零后进入 MTA 精确计数；精确为零且满足 canRetryRecipient 才进行候选计数和候选明细。CountOnly 不带排序，明细则 timestamp 降序并取最多 500 条，且返回完整 _source。候选少不等于底层扫描少，计数快也不能保证排序/回表快。

未证实：宽时间窗上的排序、rcpt/subject match_phrase、存储分布和字段映射是否导致明细慢。接口经 Kibana console proxy，不能仅凭 URL 或 DSL 认定实际后端是某一 Elasticsearch 版本，也不能直接把 profile、PIT、keyword 子字段当作已支持能力。

前置串行调用增加总延迟，但不是“最后一个明细调用自身 60s”这一问题的充分解释。只合并前三段，即使理想并行，也无法消除该明细瓶颈。

### 3.2 建议改法

1. **先补可诊断的阶段记录。** 在 Query/Service 传递 stage（da_initial、mta_exact_count、mta_recipient_candidate_count/detail、proxy_*、unknown_tid_lookup）、请求内查询序号、查询形态摘要/hash、时间窗、size/from、返回数、总数及其精确性、剩余整单预算。多查询批次记录各子项结果，批次耗时只记录一次，不能把批次总耗时冒充各子项耗时。msearch 当前只读首项统计、含任意 size:0 就判 count_only、OK 不代表子项成功，都需配套调整。
2. **可控记录最终 DSL。** 构造完成且发送前记录脱敏摘要；有权限的诊断模式才记录严格长度限制的 DSL，带 clipped 标记和原文 hash，覆盖错误/超时路径。不记录 Authorization、命中正文。地址/主题等敏感值要按诊断需求脱敏；精确复跑参数从受控工单材料获取。`Record` 现有“不存命中正文”约定仍保留；不要把每页 DSL 无上限塞进 audit。
3. **用四例相同参数做受控对照。** 首先验证实际上游类型、版本、字段 mapping/表结构、分区/排序键、profile/EXPLAIN 支持，再比较：计数与原始明细；相同过滤去 sort；size=10/100/500；最小必需 _source；原始 rcpt/subject 与经确认的精确/规范化字段。记录冷/热缓存、扫描量、排序量、网关/上游耗时、返回一致性。选择性强的精确 account/tid 与时间约束优先，禁止未经等价性验证删除主题/收件人条件。
4. **根据证据优化查询或存储。** 若排序/回表占主因，优先利用实际索引/排序键和字段投影；若拼接 rcpt 是主因，采用 P-94 的规范化收件人策略。size 变小只有在完整分页仍成立时才可采用，不能以漏查换速度。索引/字段迁移需约定历史覆盖，旧数据仍走有界兼容路径。
5. **批量 _msearch 是可选的后续优化。** 引入内部 QueryBatch：每项包含 dataset、filters、opts、stage，拼 NDJSON，按请求顺序逐项校验/映射；Query 可复用解码以减少分歧。必须校验响应数、各项 error/hits/完整性，不能继续只用 responses[0]。Trace 已有三项分发模式可参考，但不能照搬其允许部分失败的业务语义。

### 3.3 为什么不直接合并前三段

后两段在逻辑上依赖前段结果，合并意味着推测执行。DATRANS 命中时原本不必访问 MTA；精确 MTA 命中时原本不必做候选计数。必须等 P-94 先定义有界候选才能考虑预提交候选计数，否则批量化可能让每次成功请求也执行宽扫。

可评估先 DATRANS 保持短路，仅在确需回查 MTA 后批量提交“精确计数+有界候选计数”；只有采样证明多数请求都需要 MTA 且上游容量足够，才评估三项同批。即使批内同时执行，也不是必然取最大耗时，上游可能串行/受限调度。推测分支的失败仅在实际选择该分支时影响业务结果，不能让未使用的 MTA 失败覆盖成功 DA。countOnly 保持自己的按源短路语义。

### 3.4 超时预算与影响面

当前本仓库：单次默认 60s，整单默认 90s，排队默认 30s，maxConcurrency=2。相邻 self-service 当前代码 `server/business/trace/platformGatewayClient.mjs:7-9,30-33` 为 30+60+5=95s 的 Node fetch 总预算。因此排队 30s + 执行 90s 的合法路径已可能超过 Node 95s；不是只把两个“60s”同步即可。

后续预算必须满足 `Node等待 >= Gateway最长排队 + Gateway整单预算 + 网络/序列化余量`，外层代理/请求预算也需复核。若整单保持 90s，则默认值组合需至少容纳 30+90+余量（按现有5s余量为125s），这是兼容性核算示例，不是本次建议直接提时长。单次实际截止为 min(单次预算, 整单剩余, 客户端截止)。审计区分单次截止、整单耗尽、客户端取消；静态“超过60秒”文案应改为准确的通用查询超时说明或实际预算。

影响：自助 delivery、内部 analysis 共用 queryMTA/Client；message Trace、auth/ops 共用 msearch 和队列。批次中实际并行的子查询会绕过“2个入站请求≈2个上游查询”的直觉，需限制批次宽度、验证上游同时执行量，保留 FIFO 和取消释放机制。未知状态 tid 回查仍受 8 条及整单预算限制，不因优化无限展开。

## 4. P-94：兜底丢账号边界

### 4.1 根因与现有保护

自助 inbound 的 Recipient 就是 account；delivery 到 MTA/PROXY 的 Filters 没有另一个独立 account 约束。MTA `fallbackFilters.Recipient=""`、PROXY `queryFilters.Recipient=""` 直接删除它。peer 为域名时 SenderDomain 只进入 derived 本地过滤，没有加入该候选 DSL；于是可能扫描整个平台在时间窗内的记录，不只是账号域内。

`Filters.Account` 不能直接补救：delivery 数据集没有 AccountField，MTA BuildMsearchBody 也不处理 Account。MTA Domain 现有含义为 sender 或 rcpt 的 match_phrase，不等于入站目标收件人域；PROXY BuildDatasetBody 根本不使用 Domain。仅填 Domain 字段不能保证边界。

现有实现出错时不返回候选给用户，且超过上限拒绝，保留了“不完整不能判零”的底线；问题首先是查询范围和可用性，不能仅凭此代码认定已发生跨账号数据返回。工单提到 MTA 抓一页属于一般/analysis 路径；self-service requireComplete=true 时，候选超过500会像 PROXY 一样提前拒绝。

### 4.2 方案选择

**首选长期方案：可索引的规范化完整地址数组/字段。** 在实际数据平台支持下为 MTA rcpt、PROXY to 生成规范化地址项，以完整 account 精确匹配，兼容大小写及分号列表。先验证字段存在和历史数据覆盖；不能臆造 `.keyword` 字段就上线。

**近期兼容方案：替换“收件人精确条件”，不删除“目标账号约束”。** 内部定义 recipient 匹配模式/候选规格，显式携带目标完整账号与账号域；两条 count/detail 共用同一个构造器。若上游支持且实测可接受，使用完整地址 token 边界匹配拼接收件人字段（覆盖首/中/末、分隔符、空白、数组、大小写），并保留时间、Sender/Subject/MsgID 等原条件。用户输入只作为转义后的字面地址，不能拼入任意正则/DSL。

若只能按域筛候选，必须约束**收件人侧的目标域**并在本地核对完整 account；域是候选边界，不能代替账号最终核对。`*@domain` 仅为方向示意：对拼接字符串可能漏掉非末尾地址，包含匹配又可能匹配子域/相似域；必须由实际字段类型与分隔规则决定表达式，禁止直接把该通配符当可靠实现。前导 wildcard/regexp 仍可能全扫描，域内也可能百万级，必须保留候选硬上限和预算。

如果无法构造不会漏掉目标地址且有界的候选条件，直接返回明确 unresolved 并建议缩窗/补充对端邮箱，不退回无账号的查询。不能通过只比较 localpart 解决：同名跨域必须严格区分。outbound 的 Sender=account 保留，recipient 为对端时也保留全部原始对端条件和最终完整地址核对；内部 analysis 的显式 Domain 条件不应被静默更换成自助 account 语义。

### 4.3 行为、影响与风险

- 精确零 → 有界候选计数零，只有候选谓词已证明覆盖目标地址才允许判定给定时间窗/条件无匹配；不能泛称“域内确无该收件人记录”。若仍有 Sender/Subject 条件，结论必须限定这些条件。
- 候选 <=500 且完整取回 → 本地完整账号及业务条件核对，再聚合分页；未完整取回、超时、超过上限 → unresolved，不能用第一页零命中推出不存在。
- MTA/PROXY 统一包含候选数量、阈值、是否查询明细及“请缩小时间窗/增加对端完整地址”说明。候选原始总数不能当目标邮件 total；需要统计时放专门诊断字段，保持现有 code 兼容。
- 若上游无法证明匹配覆盖，结果即使零条也应 unresolved，不能错误推进到“三源无记录”结论。delivery 对 proxy total=0 有重写 limitations 的分支，需一起验证不会覆盖不确定性。

影响文件：handlers/DeliveryQuery（如需显式内部 account scope）、types、dsl、mta、proxy、aggregate 与对应测试；外部 API 不接收 raw DSL/index，不增加用户可控制的作用域选项。主要风险为误匹配/漏匹配、正则昂贵、域条件方向错误、旧历史字段缺失，以及内部分析接口行为回归。

## 5. P-95：DATRANS 全量分页

### 5.1 根因与实现边界

delivery.go 单次取500，`da.Total > len(da.Entries)` 就 CK_LOGS_DA_TRUNCATED。这是避免不完整聚合的正确保护，但当前没有补齐能力。`BuildDatasetBody` 不消费 PageSize/Offset 是必要前置修复。不要把 HardSize 从500全局改5000：它同时约束 MTA、PROXY、auth/ops、Trace、外部页面大小。

分离内部批大小（初始500、待性能实测）、DA最大原始日志数（建议初始5000、待容量验证）、对外 pageSize（仍<=500）三个概念。5000是原始事件数，不是聚合邮件数，也不是“单次 fetch 能读多大”的保证。

### 5.2 建议流程与完整性约束

1. 实现 DATRANS 的 size/from，限定影响范围或完整验证所有 BuildDatasetBody 使用者。第一批带精确总数；总数>5000立即护栏错误，保留 CK_LOGS_DA_TRUNCATED 或显式兼容映射，并准确说明总量护栏，不再笼统称单页500就无法返回。
2. 第一批total<=护栏但未取齐，按实际取回条数推进 Offset，串行取余页；每页沿用完全相同 Filters/时间上界及稳定排序。总条数、页数（500一批最多10页）、总耗时、每响应体字节数都要有限制；ctx 截止或取消后不再发下一页。
3. 成功条件不是“累积长度>=最初total”这么简单。需先扩展解码保留 total 精确性（relation=gte 不可当精确），识别 timed_out/分片失败（实际后端有该字段时），区分 missing hits 与合法空结果；短页但仍应有剩余、总数变化、重复页、无进展、后续失败都返回未完整错误，不返回貌似成功的部分聚合，也不能转入 MTA/PROXY 当作 DA 零命中。
4. 当前仅 timestamp 降序不能保证同一时间戳跨页顺序。实施前确认稳定且唯一的上游排序键；现有解码只留 _source，不保留 hit id/sort，需要按所选分页协议扩展。tid/mid 可能多条事件共享，不能用其简单去重，否则丢失合法投递动作。稳定排序不等于快照；固定时间窗仍有迟到日志。优先确认快照/游标是否支持；不支持时明确稳定历史窗的适用条件与并发写入下的限制，发现不一致保守报错，不能声称 offset 天然严格全量。
5. 全部 DA 原始事件收齐后，执行现有本地主题/对端域过滤、已发送副本排除、聚合、unknown tid回查、排序，最后按用户 page/pageSize 切片。total/hasMore 必须来自最终聚合行，不能取529或5000当邮件数。

8MiB HTTP响应上限当前通过 LimitReader 直接截断，既不明确报告 oversize，也忽略 ReadAll 错误。分页实现应有限读取并检测超限（例如限制+1）和读取错误；单页500条也可能因大 _source 超限。若使用字段投影，需要覆盖所有状态/失败原因/文件夹/聚合所需字段，不能仅为减小字节数丢字段。

### 5.3 性能、契约与跨仓库影响

529 条理论需两次请求，但首批已经55.95s，第二批成本不能假设只有29/500。第二页可能再次扫描/排序，十页更不能按低成本推算。先拿到 P-93 对 DA/MTA 的实测结论，再确定批大小与剩余预算；不能每页重置整单90s。给必需的全量获取及后处理留余量，unknown tid仍最多8个且可降级 unknown；若耗时无法落入同步链路预算，暂停同步全量承诺，另立异步快照/结果缓存设计，不在本项偷偷增加长任务接口。

相邻 self-service 已只读核对：`web/features/selfService/trace/TraceCommon.tsx:17,344` 的500用于单页提示，不应跟 DA内部总量护栏一起改成5000；当前 pageCapped 把正常 `total>returned` 且shown>=500提示为结果不完整，成功全量聚合后正常多页应优先展示分页，而非建议缩窗重查。Node timeout 同 P-93 核算；现客户端白名单已经包含 page/pageSize/countOnly。错误 code、truncated/hasMore 文案需联调。

countOnly 当前统计的是首个有命中源的原始事件数，且没有完整应用主题/对端域的本地过滤；不等于明细聚合total。本项保持并明确这项既有差异，不为取全DA而把 countOnly 改成抓明细；如产品要求计数等价应单列需求。每个用户页当前都重查并重聚合，扩大到5000会放大翻页成本；评估有界短期结果缓存需独立处理用户/账号隔离、查询键、失效和内存，不作为本次默认必做项。

## 6. 测试与验收计划

以下为后续实现计划，本次未添加测试代码、未执行生产复跑或性能测试。纯文档分析不以现有单元测试通过作为根因证明。

| 范围 | 必需用例与断言 |
|---|---|
| P-93 观测 | 成功/超时/取消均有stage及预算；DSL长度限制/脱敏；无凭据或命中文正文；审计子项失败不显示业务成功；mixed batch每项可定位 |
| P-93 批量 | 使用 httptest 检查完整 NDJSON 和每项dataset映射；多响应、顺序映射、少/多响应、子错误、缺hits、transport失败；未选择推测分支失败不覆盖DA成功；保持countOnly/Trace语义 |
| P-93 预算 | 用毫秒级可控延时模拟排队+多阶段、Node取消、单次与整单截止，确认后续请求不发送、槽位释放；两并发和队列压测，检查新增批次对上游并行的放大 |
| P-93 性能 | 四组工单相同参数在受控环境复跑，与基线及优化后对比；候选<=100的查询端到端p95<30s，分别报告执行耗时和含排队耗时、错误率、样本数、缓存状态；少量成功样本不算p95验收 |
| P-94 DSL | MTA/PROXY count与detail始终有目标账号覆盖约束，绝不裸清Recipient；账号单独、peer邮箱/域名、subject组合；域条件只能落在正确地址方向 |
| P-94 地址 | 单值、数组、分号/逗号、空白、大小写、账号在首中末；相似localpart、同名跨域、相似域/子域不能误命中；元字符按字面处理；旧数据缺字段不能假零 |
| P-94 结果 | 0/1/500/501/百万候选；超阈值不查detail；<=阈值但少回/超时仍unresolved；完整候选本地匹配0才可条件化判零；已有sameSenderRecipient语义不变 |
| P-95 分页 | 0/1/499/500/501/**529**/1000/5000/5001；验证size/from实际写入DA DSL、第二页Offset递进、5000最多10页、5001首批即拒绝；不反复第一页 |
| P-95 完整性 | 第二页错误/取消/整单耗尽、短页/空页、重复页、total上下漂移、relation=gte、partial/timed_out、超8MiB和读失败；均不成功返回部分聚合 |
| P-95 聚合 | 同一邮件/对端动作跨页、末页bounce覆盖早页状态、同timestamp多条合法动作、已发送副本排除、主题/对端域过滤后零、unknown tid<=8；对外page1/2/超尾页，total/hasMore和未分批全量基准一致 |
| 兼容性 | analysis保持requireComplete=false的既有计数保留语义；自助完整性更严格；auth/ops/message不受分页改造污染；external/internal权限和未知字段拒绝不变；UI正常翻页不误报截断 |

现有测试锚点：`delivery_test.go` 的 datransTruncated、subjectAndDomainAreLocalFilters、inboundThreeSourcesZero、mtaErrorDoesNotQueryProxy、unknownTidCapAddsLimitation、unknownTidTimeoutAddsLimitation；`mta_test.go` 的 detailFailureKeepsCount；HTTP queue/server 测试及 auditlog 测试。

特别注意 `TestQueryProxy_recipientFallbackWithCompoundTo` 当前明确断言候选不含原精确Recipient，体现旧实现约定：应改成断言“精确匹配可放宽，但目标账号覆盖谓词不可丢失”，不能机械保留旧字符串断言。MTA 现有多处测试依赖调用次数/顺序，批量化要更新为验证协议和短路行为，而不是仅把次数数字改小。

实现后建议按变更执行：`go test ./internal/cklogs ./internal/httpapi ./internal/auditlog ./internal/queue`，再 `go test ./...`；涉及并发记录/队列时运行对应 `-race`。跨仓库实际改动后按 self-service 的 AGENTS 要求运行 typecheck/test/build 和部署路径验证。性能验收必须依赖真实或等价数据后端，httptest只验证协议/行为。

## 7. 实施拆分、上线门槛与回退

| 阶段 | 交付与门槛 | 主要风险/回退 |
|---|---|---|
| A：P-93诊断基础 | 阶段审计、整链路预算表、复跑方案；确认上游类型与字段，获取四例和529例的查询计划 | DSL敏感值/日志膨胀；受限诊断开关，默认摘要 |
| B：P-94有界兜底 | 两源共享候选策略；OCT-030/032相同条件不再无账号宽扫；证明无漏查才允许返回零 | 新匹配昂贵或漏查；回退为明确unresolved，不能恢复无边界宽扫 |
| C：P-93性能修复 | 根据计划优化字段/排序/投影；小候选p95<30s，验证共享队列负载；批量作为独立开关，收益实测后再开 | 推测执行放大/错误覆盖；可单独关闭批量，保留边界修复及审计 |
| D：P-95分页 | Dataset分页生效、稳定性/完整性策略确认、529和5000边界通过、同步预算可达、Node/UI契约联调 | 跨页漂移、响应超限、槽占用/内存上升；独立关闭多页，恢复明确DA截断错误 |

灰度指标：按stage的耗时/错误率，候选量分布，DA页数/原始数/聚合数，超护栏/不完整错误、整单剩余预算、unknown降级比例、取消后槽位释放、队列等待p95和上游并行量。先用受控账号及固定历史窗，保留原参数对照；不得用“错误率下降”掩盖误判无记录或结果缺失。

落地前待确认事项：实际上游查询引擎与可用诊断能力；地址字段mapping/规范化及历史覆盖；稳定唯一排序键与快照能力；生产Node/代理实际预算；P-93四例、P-94两例和P-95一例原始参数与审计。它们不阻碍本文静态设计，但分别决定查询表达式、全量一致性承诺与最终时长配置，不能在编码时靠猜测补齐。
