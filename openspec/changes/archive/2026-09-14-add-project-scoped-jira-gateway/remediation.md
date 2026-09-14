# 独立复评修复记录

本记录对应 verification.md 与 reassessment.md 中的 C1、W1–W5、S1。保留原报告作为发现时的历史记录，当前实现状态以本记录及任务 8.1–8.6 为准。

## 修复与回归

| 项目 | 修复 | 持久验证 |
|---|---|---|
| C1 | Jira 响应从首次输出起设置 30 秒真实连接写 deadline；上游 FIFO 仍只限制 Jira 调用 | TestSlowDownloadResponseHasRealWriteDeadline：真实 TCP 客户端不读 8 MiB 文件，测试缩短写超时后 handler 自行退出，未由客户端主动断开触发 |
| W1 | 提前检查 Content-Length；multipart 最终边界后继续在 MaxBytesReader 和读取 deadline 下计量到 HTTP EOF，检查完成前不上传 Jira | TestMultipartEntireHTTPBodyBeforeWrite：Content-Length/chunked，正常/小 epilogue 成功，12 MiB epilogue 返回 413 且 Jira POST=0 |
| W2 | 只接受 float64/JSON 往返不改变数值的数字；配置、请求和上游响应都做检查，不静默舍入 | TestNumbersMustRoundTripWithoutChangingValue、TestRejectLossyNumericDefaults、TestNumericDefaultConflictAndUpstreamPrecision |
| W3 | 使用原始 JSON 字段存在性校验时间成对且非空，之后再验证格式、顺序和跨度 | TestSearchTimePresence：单边空、双边空、缺一端、null 均返回 400，不执行业务搜索；合法成对时间正常查询 |
| W4 | app-stack overlay 引用四个 Jira 上游环境变量，由 Compose 展开，临时文件不含凭据值 | Node Compose 合并测试；jira-compose-smoke.py 对 install/149、Bearer/Basic 检查实际 Docker container Config.Env，包含美元符号的假凭据保持原值 |
| W5 | 补持久正向回归 | 多 token/项目允许拒绝矩阵、无过滤 IT 读取、OR 过滤读取与创建拒绝、成功续页的查询/排序/偏移一致性、正常评论创建一次、旧路径不触发上游 |
| S1 | 保持空 issueKeys 与不传相同，明确写入文档 | TestEmptyIssueKeysIsDocumentedAsOmitted |

## 数字支持范围

选择复评允许的保守方案：不承诺任意精度数值，而是拒绝不能保真往返的表示。例如 9007199254740993 拒绝，9007199254740992 与 9007199254740992.0 同值可接受，相邻可表示但不同的数字仍触发 create_default_conflict。0.1 等可稳定 JSON 往返的普通小数可使用。

数字字面量最长 1024 字符、指数限 -2048 至 2048，约束精确比较成本。无法保真的配置拒绝启动，请求返回 400 invalid_body；上游读取失败为 503，写入可能成功时为 502 outcome_unknown。

## 已执行验证

- `go test ./...`、`go vet ./...`。
- `go test -race ./internal/jira ./internal/strictjson ./internal/config ./internal/httpapi`。
- `go build -buildvcs=false -o bin/platform-gateway ./cmd/gateway`。
- `node --test scripts/deploy/*.test.mjs`：9 个测试通过，含真实 Compose 合并。
- `python3 scripts/test/jira-compose-smoke.py colima platform-gateway:jira-smoke`：4 组容器环境检查通过，仅创建检查并清理临时容器，不连接上游。
- 更新后的 Docker 镜像构建、TLS mock 容器挂载/轮换 smoke，以及 Go 1.23 builder 的测试/vet 与配置文件排除 smoke。
- 文档 JSON/本地链接检查、OpenSpec strict validation、git diff --check。

容器 smoke 及其构建上下文只使用临时配置、假凭据和本地 mock，没有读取真实 env/token 文件，没有访问生产 Jira，没有修改调用方仓库。真实 Jira 版本、字段、权限及时区仍须在接入时核验。
