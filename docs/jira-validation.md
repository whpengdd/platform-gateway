# Jira 网关验证记录

变更：add-project-scoped-jira-gateway（spec-driven）。本记录只覆盖本仓实现、本地 mock 和容器，不代表生产验收。

## 已执行

- 本机 Go 1.27.1：`go test ./...`、`go vet ./...`、相关包的 `go test -race`。
- 本机二进制：`go build -buildvcs=false -o bin/platform-gateway ./cmd/gateway`。工作区父目录存在另一种 VCS，关闭自动 VCS stamping 后构建成功；部署脚本也采用该参数。
- 部署脚本：`node --test scripts/deploy/*.test.mjs`，覆盖 host 文件路径、旧变量拒绝、Compose override 和 shell 语法。
- Compose：`docker-compose -f docker-compose.yml config --no-interpolate --no-env-resolution --quiet`。本机 Compose 为独立命令，未修改用户 Docker 配置。
- 镜像：`docker --context colima build -t platform-gateway:jira-smoke .`，使用 Dockerfile 的 Go 1.23 builder。
- 构建上下文：`python3 scripts/test/build-context-smoke.py colima`，在临时上下文放入根目录及嵌套 mock config.json，确认它们没有进入 builder；同时在 Go 1.23 容器运行全套测试和 vet。
- 容器 smoke：`python3 scripts/test/jira-container-smoke.py colima platform-gateway:jira-smoke`。临时 TLS mock、临时宿主配置、UID 65532、工作目录 /、只读挂载、缺源文件拒绝、跨服务/项目拒绝、原子替换文件后重建、token 轮换全部验证。临时容器和文件自动清理，没有使用真实配置或生产 Jira。
- 文档 JSON 示例、示例配置及本地链接校验；`openspec validate add-project-scoped-jira-gateway --strict`；`git diff --check`。

## 关键允许与拒绝场景

文件拒绝未知键、大小写别名、重复键、尾随 JSON、重复 token、占位符、混合权限和非法项目引用。cklogs 六个业务入口均拒绝 Jira token；external/internal 原有权限及队列测试保留。

JQL 强制项目 AND 完整过滤片段，客户端值独立编码；拒绝括号逃逸、ORDER BY 和原始 JQL 参数。暂时失败退避 5 秒、单个在途重验、恢复及永久非法关闭有测试。现有 issue/子资源重新核验父 issue；跨项目结果、移动 key、跨父评论游标和跨工单附件 ID 不下发。

读取投影去除 self、未知嵌套资源及限制评论，客户字段读取不依赖创建配置。创建默认值冲突在写入前拒绝；复杂过滤、未知元数据和不可设置字段关闭创建。写入响应丢失或固定字段核对失败返回 outcome_unknown，测试确认不自动重试。

游标绑定操作、项目、父 issue、查询、配置和有效期，篡改/跨接口/跨父/过期/重启均有拒绝验证。附件验证单文件、名称、MIME、字节上限、同源路径及父资源归属；拒绝重定向、编码穿越及超大响应。审计与错误脱敏有断言，项目多 token 共享预算及 UTC 日切换有测试，Jira 与 cklogs 队列互不占用。

## 实施时仍需核验

目标 Jira 版本及 REST 行为、认证方式、项目/type、实际创建必填字段和选项、客户字段 schema、bot reporter 可设置权限、附件权限及路径形状。必须把 Jira 服务账号时区设为 UTC：当前时间条件使用 Jira 分钟精度字面量，调用方处理分钟边界重叠。未执行任何真实 Jira 查询或写入。

JQL 存在索引延迟和检查/写入竞态；不提供即时撤权、事务授权、幂等账本或持久额度。分页不是快照。实际 CS 字段清单并未因 mock 通过而完成验收。

交付范围是调用方接口与配置合同。Zammad/rag-explorer-ai 的适配、旧凭据撤销、真实部署及切换仍由接入项目实施。

## 独立复评修复验证

最新修复及验证结果见 [修复记录](../openspec/changes/add-project-scoped-jira-gateway/remediation.md)。回归包含真实慢读 TCP、完整 multipart HTTP body、数字保真/冲突、时间参数存在性、正向多项目/评论/续页，以及实际容器环境变量检查。
