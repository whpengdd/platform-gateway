# 独立复评：add-project-scoped-jira-gateway

> 本报告记录的是修复前状态。后续修复及回归结果见 [remediation.md](remediation.md)。

本轮从当前规格、实现与部署入口重新验证，没有修改业务代码或任务勾选。原 verification.md 作为历史报告保留，本文件收窄其风险描述和修复建议。

## 结论

原报告指出的五个具体实现问题（C1、W1–W4）均有依据，且本轮获得了更强的复现证据。但不能把这些问题都解释成授权漏洞，不能把“缺少端到端测试”直接解释成“功能未实现”。

保留修复后再归档的建议，优先处理下载响应超时与 app-stack 部署配置。上一轮的 CRITICAL 是归档流程中的阻断标签，不是 CVSS 安全严重级别；本轮将 C1 定位为优先修复的资源防护缺陷，而不将上游队列提前释放本身定为错误。W5 降为回归补强建议。S1 保持合同澄清建议。

“19/25 未发现偏差”不是正确率或已证明通过率；同一缺陷被映射到多个要求，不应据此产生量化的质量分数。

## 逐项复评

| 原编号 | 复评判定 | 影响与边界 |
|---|---|---|
| C1 | 缺少响应写 deadline 成立；队列部分论证需收窄 | 真实 TCP 慢读测试超过 32 秒仍阻塞。Gate 用于上游预算，结束上游后释放槽有合理性；真正遗漏是客户端响应阶段没有时间/资源控制。没有证据表明上游并发超过配置值或可跨项目读取。 |
| W1 | 成立，属于请求大小合同缺口 | 已在真实 HTTP、Content-Length 和 chunked 两种路径复现 201。未消费的 epilogue 不会被转发为文件；10 MiB 单文件上限仍有效，不能推导为可向 Jira 上传任意大文件。 |
| W2 | 成立，且已复现到 POST | 超出 float64 精确整数范围的不同数字折叠后，冲突检查通过并创建。主要影响高精度数字；不能推导为 labels/project 等其他固定值可被覆盖。 |
| W3 | 成立，输入合同问题 | 单边或双边空时间返回 200；空值与合法另一端组合返回 400。项目及 filterJql 仍生效，没有跨项目授权扩大。 |
| W4 | 成立，限定 install/149 部署路径 | 使用实际 gateway Compose 服务片段和本仓 overlay 做真正合并，宿主提供假 Jira 配置后，服务 environment 中仍没有 Jira 键。self Compose 与 host launcher 不受此遗漏影响。 |
| W5 | 覆盖缺口成立；降为测试补强建议 | 本轮正向测试通过，不能把缺少这些持久测试当作功能失败或独立的归档阻断理由。 |
| S1 | 保留澄清建议 | 空 issueKeys 等价于不传；规格没有规定空数组一定应返回空集合。不存在足够依据判定它违规。 |

## 新证据

### C1：真实 TCP 非读取客户端

- 使用本地 TLS mock Jira 返回 8 MiB 文件；下游为与生产相同的无 WriteTimeout net/http server，ReadHeaderTimeout 为 10 秒。
- TCP 客户端发送合法 GET 后不读取，并将接收缓冲区设为 1024 字节。
- 上游传输结束 32 秒后，handler 仍未返回，Jira gate active=0；关闭客户端连接后 handler 才退出。
- 这比原来的阻塞 ResponseWriter 替身更直接：实际连接行为确实不受 30 秒操作 context 约束。

定位：`internal/jira/server.go:217-220`、`:266-271`，`cmd/gateway/main.go:66-70`。`internal/backend/backend.go:8` 明确说明 Gate 限制的是 upstream backend，因此不能仅从 active=0 推导出 FIFO 设计错误。

修复建议：增加 Jira 响应写 deadline；若还要求发送缓冲区/下载连接的严格并发上限，可采用独立的输出预算。也可以把完整传输放进现有 gate，但这会让慢客户端占用上游槽，不是唯一正确方案。测试应断言真实连接按时退出，不能仅断言调用过 SetWriteDeadline。

### W1：真实 HTTP 的两种传输方式

对正常单 file part 的结束边界追加 12 MiB 尾部数据；构造 body 共 12,583,140 字节，合同上限 11,534,336 字节。

| 传输 | HTTP 结果 | Jira 上传次数 |
|---|---:|---:|
| Content-Length | 201 | 1 |
| chunked | 201 | 1 |

定位：`internal/jira/attachments.go:29`、`:58-60`。这里证明的是完整实体大小没有在写入前验证；并未证明这些尾部字节全部被服务器读取或存入内存。multipart parser 提前停止正是问题的来源。

修复建议：已知 Content-Length 超限提前拒绝；未知长度使用有 deadline、有限额的完整请求体计量，并在 Jira POST 前确认上限。不能通过无界 drain 来修复，否则会引入新的慢请求问题。

### W2：固定数字冲突穿透创建校验

- 从真实 config.Parse 载入默认值 `9007199254740993`。
- 客户端字段提交不同值 `9007199254740992`。
- mock 元数据声明该业务自定义字段为可设置的 number；通过正常创建 handler 调用。
- 结果为 HTTP 201，Jira POST 次数为 1；实际发送数字为 `9007199254740992`。

定位：`internal/strictjson/json.go:31-33`、`internal/jira/create.go:115-117`、`internal/jira/fields.go:100-102`。

修复建议：精确保留/比较支持的数值，或在不能保真的范围内明确拒绝。仅给 Decoder 添加 UseNumber 不足以完成修复，因为现有字段校验仍断言 float64；配置、输入、比较、序列化和上游投影需要一致处理。允许明确缩小支持范围，不必为了此问题承诺任意精度 Jira 数字。

### W3：参数存在性与空字符串

| 请求 | 当前状态 |
|---|---:|
| `{"updatedFrom":""}` | 200 |
| `{"updatedTo":""}` | 200 |
| `{"updatedFrom":"","updatedTo":""}` | 200 |
| `{"updatedFrom":"","updatedTo":"2026-09-14T00:00:00Z"}` | 400 |

定位：`internal/jira/search.go:29`。规格明确要求提供时间时成对且有效，因此前面三个请求应被拒绝。

分钟精度和 bot UTC 时区另作合同说明事项：当前 API 文档已主动声明这些限制，原规格没有明确要求秒级精度。不能仅因 design.md 未详细说明就将它认定为另一个确定的功能缺陷。

### W4：实际 Compose 合并

提取相邻 rag-explorer-ai 两份版本控制 Compose 中的 gateway 服务片段，放入临时目录；启用 platform-gateway profile，与本仓脚本生成的 overlay 合并。使用隔离环境，设置假 JIRA_BASE_URL/JIRA_API_TOKEN，并用 /dev/null 作为 env-file，未读取真实 env。

两次 `docker-compose config --format json` 的目标服务 Jira 环境键均为 `[]`。宿主环境变量不会因为存在而自动传入容器。

定位：`scripts/deploy/compose-auth.mjs:7-10`、`scripts/deploy/standalone.sh:150-160`。建议本仓 overlay 明确声明四个受支持的 Jira 上游键，再用 Compose 合并后的结果做测试；不要求修改调用方仓库。

### W5/S1：不能用缺少测试推导功能失败

本轮临时测试全部通过：

- jira-b 读取无过滤的 IT issue；
- 配置 OR 过滤后正常搜索；
- 获取第一页 nextCursor 并成功读取下一页；
- 正常评论 POST 201 且只写一次；
- 四个旧路径返回 404。

这些测试只证明 mock 场景中的当前行为，不代表真实 Jira 兼容性。但足以反驳“这些路径因为原来没有测试，所以应视为功能缺失”的推断。建议把关键用例转成持久回归，尤其是成功续页和评论创建。

空 issueKeys 返回 200、使用项目与 filterJql 范围的行为也再次确认。缺省/空数组的区分由合同决定，不建议仅为了让审查清单消失而擅自改变 API。

## 本轮范围

本轮重新设计并执行真实 TCP、真实 HTTP、正常创建 handler、成功业务路径和 Compose 合并测试。临时 probe 文件及配置已清理；只交付此复评记录，没有修改实现、任务状态或真实部署。未访问生产 Jira。

优先顺序：先处理 C1 的真实响应超时与 W4 的部署阻断，再处理 W1–W3 的输入/数值合同；W5 与 S1 作为回归和文档补强。总体仍建议修复已确认缺陷后再归档，但不沿用夸大的安全定性或“未测即未实现”的解释。
