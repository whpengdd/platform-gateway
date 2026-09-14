## Context

见 proposal.md 的动机。当前 internal/config.File 只定义 tokens/jira.projects，cmd/gateway/main.go 混合读取文件和环境变量，jira.NewClient 接受独立的地址和凭据。Compose 和 host launcher 还会强制设置监听与日志参数。统一配置必须同时改变这些入口，确保部署入口统一使用文件配置。

实施阶段已归档 add-project-scoped-jira-gateway，正式 specs 现包含既有授权规范。本 change 不改写该历史提案；路径和旧入站环境处理 requirements 的 MODIFIED delta 已补齐。这里的新配置来源合同取代旧路径规则和旧入站环境变量拒绝规则，其余鉴权、JQL 和资源边界保留。

## Goals / Non-Goals

**Goals:** 一个文件即可审阅本次迁移的应用有效配置；严格失败、默认值明确；所有现有部署入口能够消费新格式。

**Non-Goals:** 环境变量兼容期、JSON 环境插值、多 Jira 服务器、动态重载、修改调用方 API、增加新的 Jira 超时/预算调参项、实际生产部署。

## Decisions

### 1. 文件模型按服务组织

扩展 internal/config 为完整应用配置模型，显式定义 Jira、CKLogs、Server、Audit 和认证结构。保留 tokens 和 jira.projects 的字段及含义，避免授权结构重排。命名使用现有 camelCase，时间字段统一以 Ms 结尾。

```json
{
  "server": {"listenAddr": ":8091", "allowCidrs": []},
  "audit": {"enabled": true, "dir": "/var/log/platform-gateway"},
  "tokens": [
    {"token": "<gateway-cklogs-token>", "cklogs": "external"},
    {"token": "<gateway-jira-token>", "jiraProjects": ["CS"]}
  ],
  "jira": {
    "baseUrl": "https://jira.example.com/jira",
    "auth": {"type": "bearer", "token": "<jira-upstream-token>"},
    "projects": {"CS": {}}
  },
  "cklogs": {
    "baseUrl": "https://ck-logs.icoremail.net",
    "auth": {"type": "basic", "username": "<user>", "password": "<password>"},
    "index": "mtatrans_distributed",
    "timeoutMs": 60000,
    "queue": {"maxConcurrency": 2, "size": 32, "waitTimeoutMs": 30000}
  }
}
```

示例含不可直接运行的占位凭据，部署须替换。Jira Basic 使用 `{"type":"basic","username":"…","password":"…"}` 替换整个 auth。保留单客户端而不引入 servers 列表，因为当前授权只定位项目，不存在服务器维度。

### 2. 解析、默认值、语义校验、装配分层

配置模块负责严格 JSON、字段存在性、默认值和跨字段校验；main 仅将已验证的有效配置装配到客户端、队列、鉴权和审计。保留 strictjson 的重复键检测和数字保真，不把 createDefaults 中的数字转成 float64。

使用指针或显式 presence 信息区分缺失和零值；原始 JSON 层拒绝 typed null。缺失可选配置用规格中的默认值，显式空 URL、空 index、无效监听/CIDR、负队列长度及无效时间直接报错。毫秒转 duration 前检查上界：cklogs.timeoutMs 最大为 floor((MaxInt64 - 5 秒的纳秒数) / 1 毫秒的纳秒数)，为现有 HTTP 客户端 timeout + 5*time.Second 预留余量；queue.waitTimeoutMs 最大为 floor(MaxInt64 / 1 毫秒的纳秒数)。先以整数比较校验，再转换和相加，不能以已溢出的结果作检查。服务关闭仅豁免缺失的必需上游字段，显式错误仍失败；只要提供 auth 对象，就要求该对象完整且互斥。

默认 listenAddr 延续程序的 :8091，宿主示例显式使用 127.0.0.1:8091；容器示例使用 :8091。相对 audit.dir 继续相对进程工作目录解释，不随配置文件目录变化。audit.enabled=false 不创建审计目录；普通启动日志独立保留。审计启用时，internal/auditlog.NewWriter 必须在 HTTP 服务开始监听前，以实际进程身份验证目录可创建新文件，并以追加/创建/只写模式打开当日日志文件；任一步失败均返回初始化错误、阻止启动，不能只依赖 MkdirAll 或延迟到第一条请求。检查不截断既有日志、不写入伪造审计记录；目录探测临时文件须清理。关闭审计时跳过全部写入检查。

### 3. 移除重复配置的旧环境变量处理，保留外部环境传入

应用配置仅通过 GATEWAY_CONFIG_FILE 定位文件。去掉 envOr/envInt 等旧应用参数读取入口，以及 internal/config.LegacyKeys 和配置加载、宿主启动器中的旧入站 token 检测。程序和部署脚本不维护旧变量禁止列表，不扫描、不告警、不因残留旧变量而报错；无论变量为空、非空还是与文件冲突，均不参与配置、鉴权或文件选择。不提供别名、回退和覆盖。部署声明移除旧变量的专用映射和默认值；env_file 或继承环境中的残留项允许原样传入，无需专门检测或过滤。通用环境文件读取和透传不属于旧变量的配置读取或检测；禁止的是按旧变量名称提取、校验、解释或应用这些值。字段映射表仅供人工更新部署，不作为运行时检测列表。

本次移除范围仅限已迁入 JSON 的重复配置及旧文件选择器，不全面禁用环境变量，也不限制未来增加独立的环境变量功能。保留 env_file、外部 environment 和宿主继承环境等传入渠道。保留 CKLogs 现有环境代理行为和 Jira 禁用代理的行为；系统证书环境仍交给运行时，不新增配置代理层。凭据按 JSON 解码结果原样使用，不能通过 shell source、Compose 插值或 TrimSpace 改写密码。

### 4. 部署入口必须一起切换

| 旧配置 | 新配置 |
|---|---|
| JIRA_BASE_URL | jira.baseUrl |
| JIRA_API_TOKEN | jira.auth.type=bearer / token |
| JIRA_BASIC_USER / JIRA_BASIC_PASSWORD | jira.auth.type=basic / username / password |
| CK_LOGS_BASE_URL | cklogs.baseUrl |
| CK_LOGS_BASIC_USER / CK_LOGS_BASIC_PASS | cklogs.auth.type=basic / username / password |
| CK_LOGS_INDEX / CK_LOGS_TIMEOUT_MS | cklogs.index / timeoutMs |
| CKLOGS_MAX_CONCURRENCY / CKLOGS_QUEUE_SIZE / CKLOGS_QUEUE_WAIT_MS | cklogs.queue.maxConcurrency / size / waitTimeoutMs |
| LISTEN_ADDR / GATEWAY_ALLOW_CIDRS | server.listenAddr / allowCidrs（数组） |
| GATEWAY_LOG_DIR | audit.dir；off/- 改为 audit.enabled=false |
| GATEWAY_AUTH_FILE | GATEWAY_CONFIG_FILE |
| PLATFORM_GATEWAY_AUTH_HOST_FILE | PLATFORM_GATEWAY_CONFIG_HOST_FILE（宿主挂载参数） |
| --auth-file / --listen | --config-file / JSON 中的 server.listenAddr |

Self Compose 保留 env_file 和外部环境变量传入，删除与文件配置重复的旧业务 environment 专用映射，并显式设置容器配置文件路径；Compose 的 .env 仍可提供镜像/挂载等部署插值。宿主启动器保留外部环境传入，可通用读取环境文件，但不再按旧键提取应用设置、不再强制写 LISTEN_ADDR/GATEWAY_LOG_DIR；指定 --config-file 时解析为绝对路径后传 GATEWAY_CONFIG_FILE，默认相对 app-root/config.json，child cwd 明确为 app-root。禁止静默忽略已删除的 CLI 参数。

install/149 overlay 更新基础 Compose 继承的旧业务 environment 专用映射，保留 env_file 和无关外部 environment（包括代理、证书变量）；不得整体清空环境传入渠道。若需替换 environment 映射，应保留无关项。验证最终合并配置、容器环境和文件配置实际生效；env_file 中残留的旧变量允许透传，不要求容器环境完全不含旧变量，不增加运行时旧变量检测。旧宿主挂载变量不再读取或检测，残留值不影响新挂载路径选择。调用方 PLATFORM_GATEWAY_TOKEN/PLATFORM_GATEWAY_INTERNAL_TOKEN 仍属于调用方，不迁入网关上游认证。

脚本健康检查应从有效监听端口或明确的部署探测地址构造，不能因移除 --listen 而永远探测固定 8091；探测地址不覆盖应用监听配置。日志目录准备同样依据新 audit 配置及挂载，不能覆盖文件或在关闭审计时创建不需要的目录。宿主 stdout/stderr 文件与 JSONL 审计区分处理。

### 5. API 保持原样

Jira 客户端继续追加 /rest/api/2/...，保持现有 HTTPS、重定向和附件同源校验；错误字段名从 JIRA_BASE_URL 改为 jira.baseUrl。无需让调用方传 URL，也无需改路由或 JSON 请求/响应。现有 API 测试只调整配置 fixture，不降低既有断言。

## Risks / Trade-offs

- [操作者误以为残留旧变量仍然生效] → 文档明确唯一文件来源，部署声明删除旧变量专用映射，保留通用环境透传；测试残留旧变量既不改变配置也不阻止启动，不增加运行时检测。
- [文件包含全部凭据] → 只读挂载、UID 65532 可读且最小权限，保留 Git/Docker 排除规则，错误和审计不输出配置。
- [宿主与容器的地址/路径不同] → 提供两种示例，明确端口与日志挂载对应关系。
- [旧二进制不能读取新字段] → 回退须同时恢复旧镜像、旧配置和旧部署脚本；不在新版本实现双格式兼容。
- [未归档前置规范存在路径冲突] → 在归档本 change 前完成前置归档及 gateway-file-auth 路径与旧入站环境处理 MODIFIED deltas，再严格校验。

## Migration Plan

这是一次部署格式切换，不是运行时兼容过渡。实施时提供无真实凭据的新格式示例和完整字段映射；部署者准备新文件并移除旧变量专用映射（环境文件中残留的旧值不影响启动），同时更新镜像及启动入口，核对监听、CIDR 和日志挂载。重建容器或重启宿主进程，验证 health/ready 和既有授权 API。后续仅修改配置无需重新构建镜像，但容器文件原子替换后必须 recreate。回退使用配套的旧版本资产。本 change 创建阶段不执行升级、回退、部署或上游连接。
