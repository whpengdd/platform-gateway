## Why

Zammad 当前直接持有 Jira 凭据，失陷后可能访问超出业务需要的 Jira 数据。需要在 platform-gateway 提供按项目和可选 JQL 条件授权的受限 API 中转，并将现有 cklogs token 一起收敛到简单的文件配置。

## What Changes

- **BREAKING**：入站 token 从环境变量迁到默认 `./config.json`；统一支持 cklogs external/internal 和 Jira 多项目授权，容器只读挂载宿主文件，修改后重建生效。
- 新增项目范围的 Jira 资源 API：能力、结构化搜索、issue 创建/读取、评论读写、附件上传/下载，固定上游和字段白名单。
- 每个 Jira 项目可配置 `filterJql` 条件，由网关强制叠加项目范围并委托 Jira 执行；不再使用此前设计的本地属性 equals 过滤器。
- 创建使用 `createDefaults` 固定字段；仅在无附加过滤或由默认值覆盖的单条 labels 等值条件下启用，复杂 JQL 项目保留读取和既有 issue 操作。
- 保留 cklogs 请求/响应和两档权限，拒绝跨服务授权；增加 Jira 独立请求预算、脱敏审计及错误处理。
- 交付调用方接口文档、统一配置示例及 cklogs 迁移说明。
- 同步调度、webhook、业务绑定、marker、重试及幂等恢复全部由调用方负责；不改动 Zammad 源码，不部署生产服务。

## Capabilities

### New Capabilities

- `gateway-file-auth`: 统一 token 配置、cklogs 权限兼容、Jira 项目授权、文件挂载及迁移。
- `jira-project-filter`: 固定项目范围、JQL 过滤、资源归属校验和创建默认值约束。
- `jira-resource-api`: 受限资源接口、输入/输出合同、分页、传输边界和写入结果不确定性。

### Modified Capabilities

无。当前 openspec/specs 中没有既有规格；cklogs 已实现行为在新认证规格中明确保留。

## Impact

- 变更本仓 internal/auth、internal/httpapi、cmd/gateway 配置装配，新增配置及 Jira 模块；复用 backend.Gate、queue、auditlog。
- 更新 Docker/Compose、裸机部署脚本、.env.example、Git/Docker 忽略规则和 docs；不再把真实配置复制进构建上下文或镜像。
- 调用方 Bearer 形式及 cklogs token 值可不变，但 gateway 部署必须移除旧入站 token 环境变量并挂载配置。
- 不引入同步数据库、后台操作账本或多实例协调。首版单 Jira 实例，Go 标准库优先。
- 真实 Jira 版本、字段及权限验证属于实施/接入前的环境核验，不能用 mock 测试宣称生产已可用。
