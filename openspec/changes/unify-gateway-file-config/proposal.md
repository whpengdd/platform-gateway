## Why

当前 config.json 只管理入站 token 和 Jira 项目策略，上游地址、凭据及运行参数分散在环境变量和部署脚本中，难以统一核对。需要把 Jira 服务器 URL 明确纳入文件，并将应用配置收敛到单一来源。

## What Changes

- **BREAKING**：config.json 统一管理 tokens、jira、cklogs、server、audit；本次迁入文件的应用参数仅从文件读取，缺失可选字段使用内置默认值。
- 增加 jira.baseUrl 和 jira.auth，所有项目共用单个 Jira 服务器及一组 Bearer 或 Basic 凭据；保留 jira.projects。
- **BREAKING**：使用 GATEWAY_CONFIG_FILE 定位配置文件，默认 ./config.json；不读取、不检测旧应用配置环境变量（包括旧入站 token 变量和 GATEWAY_AUTH_FILE），残留变量不影响启动，不提供别名、回退、覆盖或过渡版本。
- 将 CKLogs 连接、索引、超时、队列以及监听、CIDR、审计配置迁入文件；严格校验字段、认证互斥、数值范围和服务依赖。
- 同步更新 self Compose、host launcher 和 install/149 overlay、配置示例、文档及测试；保留 env_file 和外部环境变量传入，仅移除与文件配置重复的旧变量处理，不限制未来引入独立的环境变量功能。
- 网关业务 API、调用方 Bearer 认证和 Jira REST API 均不变；不新增多服务器或热加载。

## Capabilities

### New Capabilities

- `gateway-application-config`: 统一应用文件配置、Jira/CKLogs 上游设置、旧环境不参与配置、部署和重启生效合同。

### Modified Capabilities

- `gateway-file-auth`: 前置 change 已归档；更新文件选择器为 GATEWAY_CONFIG_FILE，并将旧入站环境变量拒绝规则改为不读取、不检测、不参与授权。其余授权合同保留，统一应用字段遵循 gateway-application-config。

## Impact

- internal/config、cmd/gateway 配置装配和 CKLogs HTTP 超时边界、internal/auditlog 启动写入检查及 Jira 客户端的配置错误字段名称。
- docker-compose.yml、scripts/deploy 下启动/overlay 脚本及 scripts/test 容器验证。
- .env.example、docs/gateway-auth.example.json、README 和配置/API 部署说明。
- 新镜像、配置和部署脚本必须配套更新；不改变调用方代码，不连接生产上游，不执行实际部署。
