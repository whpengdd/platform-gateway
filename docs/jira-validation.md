# Jira 网关验证记录

本记录只覆盖本仓实现、本地 mock 和容器，不代表生产验收。历史基线变更为 add-project-scoped-jira-gateway（spec-driven）。

## 2026-09-29：账号时区与缓存改进

变更 `fix-jira-timezone-handling` 已同步主规格并于2026-09-29归档，实施及逐项证据见 [验证报告](../openspec/changes/archive/2026-09-29-fix-jira-timezone-handling/verification.md)。已通过：

- `go test -count=1 ./...`、`go test -race -count=1 ./internal/jira ./internal/httpapi ./internal/config`、`go vet ./...`。
- 独立进程 `TZ=Pacific/Honolulu` 下的搜索时区与 DST 回归；缺失/Local 时区时 HTTP 创建仍返回201。
- `docker --context colima build -t platform-gateway:jira-timezone-smoke .`，以及该镜像的 TLS container smoke 和 self/install/149 × bearer/basic 六组 Compose smoke。
- 容器没有系统 zoneinfo，ZONEINFO/GOROOT 指向不存在位置、TZ=UTC；仍正确生成上海当地时间的 JQL，+08:00 与 Z 的等价输入一致，capabilities 与时间搜索共享一次 /myself。
- 配置默认300秒/显式0、缓存合并/取消/失败退避/身份与时区隔离、游标 generation、历史秒级回拨、创建/读取原字符串保留及原有授权回归。
- OpenSpec 严格校验、脚本语法及 diff 空白检查。

后续显式复核发现的 W1 已修复：缺失时区的5秒退避不再依赖成功快照 TTL，TTL=0/1/4/5/300秒的组件与 HTTP 回归通过；过期身份不返回，身份消费者仍可按自身 TTL 刷新。修复后重新运行全量 Go 测试、相关包竞态检查、vet 和 OpenSpec 严格校验，均通过；本次未重复容器或真实 Jira 验收。

MR 提交前对包含 W1 的最终代码再次完成全量 Go / race / vet / 主规格校验，并重新构建镜像、补跑 TLS container smoke 和六组 Compose smoke，全部通过；真实 Jira 验收仍未执行。

Compose 的缺失挂载测试保留部署脚本对已删除配置的拒绝，Docker 层改用从未创建过的路径，避免共享文件系统对最近删除挂载项的时序影响；未放宽部署校验。所有上游均为临时本地 mock，未读取真实凭据或访问生产 Jira。

## 历史基线已执行

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

目标 Jira 版本及 REST 行为、认证方式、项目/type、实际创建必填字段和选项、客户字段 schema、bot reporter 可设置权限、附件权限及路径形状。当前版本自动读取 Jira 服务账号的 timeZone，不再要求统一 UTC；仍需核对继承默认时区时 /myself 的实际返回及规则版本。时间条件保留分钟精度，调用方处理边界重叠；回拨重复分钟边界明确拒绝，缓存内账号变化存在检测延迟。原始“创建时间少八小时”尚无现场样本，不能视为已定位或修复。未执行任何真实 Jira 查询或写入。

JQL 存在索引延迟和检查/写入竞态；不提供即时撤权、事务授权、幂等账本或持久额度。分页不是快照。实际 CS 字段清单并未因 mock 通过而完成验收。

交付范围是调用方接口与配置合同。Zammad/rag-explorer-ai 的适配、旧凭据撤销、真实部署及切换仍由接入项目实施。

## 独立复评修复验证

最新修复及验证结果见 [修复记录](../openspec/changes/archive/2026-09-14-add-project-scoped-jira-gateway/remediation.md)。回归包含真实慢读 TCP、完整 multipart HTTP body、数字保真/冲突、时间参数存在性、正向多项目/评论/续页，以及实际容器环境变量检查。

统一应用文件配置的最新验证见 [配置迁移验证](../openspec/changes/unify-gateway-file-config/verification.md)。
