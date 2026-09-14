## 1. 统一配置模型与校验

- [x] 1.1 扩展 internal/config 的 server/audit/jira/cklogs 模型，保留 tokens/projects 和 createDefaults 数字保真；以解析测试验证未知键、重复键、null、无效类型、文件大小及既有权限校验。
- [x] 1.2 实现 GATEWAY_CONFIG_FILE 和默认 ./config.json，删除旧应用变量读取及 LegacyKeys/旧入站环境检测；用残留旧 URL、旧路径和旧入站 token 的测试验证启动成功、文件配置不被覆盖、旧 token 不获授权，并检查无旧变量扫描或警告逻辑。
- [x] 1.3 实现 Jira URL、Bearer/Basic 互斥与 CKLogs Basic 配置校验；测试部署路径、认证混用/缺失、CR/LF 拒绝、美元符号和引号保持原值及仅启用单服务的配置。
- [x] 1.4 实现默认值、CIDR 数组、监听地址、审计开关、数值边界及相对路径语义；测试 queue.size=0、audit.enabled=false、超时溢出/小数拒绝和显式非法值不回退；分别测试 timeoutMs 预留 HTTP 客户端额外 5 秒后的最大合法值及最大值 + 1、waitTimeoutMs 的 duration 边界，并验证实际 HTTP 客户端 Timeout 为正且等于配置时长 + 5 秒。

## 2. 启动装配与 API 回归

- [x] 2.1 将 cmd/gateway 改为使用完整文件配置，删除业务 envOr/envInt/logDir 读取入口；启动测试验证 Jira-only、CKLogs-only、双服务与缺失上游配置。
- [x] 2.2 从 jira.baseUrl/auth 装配客户端并更新配置错误字段名称；通过 TLS mock 验证 context path 下 /rest/api/2/search、Basic/Bearer、单服务器多项目以及既有重定向/附件安全边界。
- [x] 2.3 将队列、CKLogs 客户端、CIDR 和审计装配到新配置；改造 internal/auditlog.NewWriter，在开始监听前以实际进程身份检查目录可创建文件及当日日志可追加打开，失败即阻止启动，清理探测文件且不截断既有日志或写入伪记录；以非 root 身份测试不可写目录、当日日志不可写、正常追加保留旧内容及审计关闭无文件/目录副作用，同时验证 CIDR 限制及既有 FIFO 行为。
- [x] 2.4 更新既有测试的配置 fixture 并保留原 API 断言；运行 go test ./...、go vet ./... 和相关包 race 测试，确认外部路由、响应和授权不变。

## 3. 部署入口同步切换

- [x] 3.1 更新 self Compose，保留 env_file、外部环境传入、部署插值，设置 GATEWAY_CONFIG_FILE 与只读挂载，采用 PLATFORM_GATEWAY_CONFIG_HOST_FILE；检查删除旧业务 environment 专用映射且保留无关环境设置，允许 env_file 透传残留旧变量。
- [x] 3.2 更新 standalone.sh/run-host.mjs 的 --config-file 和文件定位，移除 --auth-file/--listen、旧应用变量的专用导入/覆盖和检测，保留外部环境通用传入；用脚本测试验证旧 CLI 参数拒绝、残留旧环境不影响启动或路径选择、路径解析、非默认监听端口的健康检查、审计关闭行为。
- [x] 3.3 更新 install/149 overlay，更新基础服务继承的旧业务 environment 专用映射，保留 env_file 及无关 environment，并更新所有挂载变量引用；用两种 stack fixture 检查最终合并配置，包含 env_file 有残留旧变量、代理/证书变量及无关外部变量的情形，验证透传保留且旧值不覆盖 JSON。
- [x] 3.4 将容器 smoke 从旧 Jira/CKLogs 环境注入改为文件配置；验证 self/install/149 容器实际环境、UID 65532 可读、只读挂载、缺源文件失败、文件原子替换后 recreate 生效，验证外部环境变量可传入、CKLogs 经 mock 代理及 NO_PROXY 绕过、Jira 仍直连；全部使用临时假凭据和 mock 上游。

## 4. 示例与操作文档

- [x] 4.1 更新 docs/gateway-auth.example.json、.env.example、README、gateway-auth 和相关 API 部署说明；提供 host/container、Jira Bearer/Basic 示例和旧字段映射，校验 JSON 并扫描活跃文档是否残留旧部署指令。
- [x] 4.2 说明本次移除仅限重复配置的旧变量处理，保留 env_file/外部环境传入且不限制未来环境变量功能；说明应用默认值、相对路径、关闭审计、宿主/容器监听与挂载差异、镜像/文件/脚本成套更新及回退；通过文档审阅确认无需修改调用方 API。
- [x] 4.3 保留真实配置的 Git/Docker 排除和最小读取权限说明；运行构建上下文 smoke，验证真实 config.json 不进入镜像。

## 5. 规范衔接与最终验证

- [x] 5.1 在本 change 归档前先完成 add-project-scoped-jira-gateway 的归档，再复制正式 gateway-file-auth 的完整路径及旧入站环境处理 requirements 为本 change 的 MODIFIED deltas，改用 GATEWAY_CONFIG_FILE、移除旧入站环境拒绝规则并引用统一应用配置合同，同时更新 proposal 的 Modified Capabilities；核对不存在相互冲突的正式配置路径或旧环境处理规范。此项不要求在本轮创建 change 时执行归档。
- [x] 5.2 完成应用、脚本和容器联合验证后记录实际命令、结果及环境限制；运行 openspec validate unify-gateway-file-config --strict 与 git diff --check，确认所有实施任务有可核对结果。
