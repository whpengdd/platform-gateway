## 1. 统一配置与认证

- [x] 1.1 实现 ./config.json 默认路径及 GATEWAY_AUTH_FILE 覆盖，定义 tokens/jira.projects 模型和严格 JSON 解析；覆盖未知/重复键、缺文件、重复 token、占位符、混合权限和项目引用错误。
- [x] 1.2 实现旧入站 token 环境变量冲突检查，按配置条目启用 cklogs/Jira 并校验对应必需配置；更新 readiness，避免未启用模块阻塞启动。
- [x] 1.3 将 auth 从仅 class 扩展为互斥的 cklogs class/Jira projects，保留摘要匹配、CIDR 检查及现有 cklogs 权限；逐个审计业务入口，补跨服务双向拒绝测试。

## 2. Jira 上游客户端

- [x] 2.1 新增固定 HTTPS origin/context path 的 Jira Transport，支持 Bearer 或 Basic 二选一，拒绝重定向及隐式代理；实现 deadline、上游 JSON/文件大小限制和脱敏错误。
- [x] 2.2 实现 search、issue、comment、attachment 及必要元数据请求；用 httptest 覆盖路径/参数编码、认证头隔离和错误响应，不访问生产 Jira。
- [x] 2.3 实现按项目裁剪 create metadata 的适配器，覆盖已审计旧 createmeta 与新分页端点形状；仅公开配置允许字段及 bot 最小身份，未知元数据关闭创建。

## 3. 项目范围与 JQL

- [x] 3.1 实现 filterJql 片段结构校验、无 ORDER BY 约束及 Jira 语义验证；区分非法配置与上游暂不可用；5 秒退避后请求触发重验，同项目单个在途验证，覆盖恢复/并发场景，不得回退到无过滤。
- [x] 3.2 实现固定项目 AND 括号内过滤 AND 编码后的查询值，测试 OR、引号、转义、括号逃逸和跨项目结果；客户端不接受原始 JQL。
- [x] 3.3 实现现有 issue 及父子资源授权路径，搜索确认项目/过滤命中后检查 comment/attachment 归属；覆盖属性变更、key 移动和跨工单 ID。
- [x] 3.4 实现 createDefaults 不可覆盖规则及元数据校验，禁止 project/type/reporter/assignee/security 覆盖；冲突在上游写入前返回400。
- [x] 3.5 实现有限创建校验：仅完整识别单条 labels 字符串等值及整体外层括号，验证 defaults 包含该值；AND/OR/cf[n]/函数/历史条件仅禁用创建，不影响受支持业务自定义字段写入。

## 4. 受限资源 API

- [x] 4.1 实现项目列表、capabilities 和 issue 读取路由；定义受支持类型的稳定响应投影，拒绝原始 self/avatar/content URL、未知复杂对象及 comment/issuelinks/subtasks/parent/worklog；覆盖嵌套越权及客户字段读取，读取类型校验独立于创建能力。
- [x] 4.2 实现结构化 search 请求、时间/列表/text 上限及固定排序；实现绑定操作/项目/查询/配置/有效期的认证分页游标，评论额外绑定稳定父 issue ID；覆盖跨项目/issue/接口、篡改、过期、重启和逐页重验。
- [x] 4.3 实现同步 issue 创建，按默认值和创建证明装配字段，确认响应身份/固定字段；确保无202后台操作、无隐式 marker 或绑定写入。
- [x] 4.4 实现评论列表/单条/新增，检查限制可见性，保留正常历史/bot 评论；仅接受 body，不加入同步语义。
- [x] 4.5 实现单文件 multipart 上传和父 issue 归属验证后的下载，校验文件名/MIME/字节上限及 secure/attachment 路径；测试跨源/编码穿越/重定向/超大流。
- [x] 4.6 统一 HTTP 错误合同和 X-Request-Id；区分尚未发送、明确拒绝、写入可能成功三种状态；用响应丢失测试证明不自动重试 POST。

## 5. 运行预算与审计

- [x] 5.1 为 Jira 装配独立 FIFO、整体 deadline 和按项目请求/上传额度，UTC 日计数；覆盖取消、队列满、多个 token 共用额度和 cklogs 不受阻塞。
- [x] 5.2 扩展审计的 backend/project/token 指纹/上游状态字段，清理任何正文/JQL值/凭据输出；补脱敏断言及 response body 上限测试。

## 6. 部署与调用方文档

- [x] 6.1 更新 Compose 文件挂载与 working_dir=/，确认 UID 65532 可读、缺源文件拒绝；将真实 config.json 排除 Git/Docker context，保留无 secret 的示例。
- [x] 6.2 更新本仓裸机/镜像部署脚本及其测试，传递配置路径/挂载并移除旧 token 环境注入；不修改其他仓库的 token 配对脚本。
- [x] 6.3 将 docs/gateway-auth.md、示例 JSON、README 和 docs/api.md 更新为统一文件配置及 cklogs 迁移合同，说明默认 ./config.json 和修改后容器重建。
- [x] 6.4 核对已更新的 docs/jira-api.md、字段兼容审计及设计文档与实现一致，统一使用 filterJql/createDefaults；补创建禁用、JQL索引延迟、分页、outcome_unknown 及最小 curl 示例，确认不残留同步/绑定职责。

## 7. 验证与交付

- [x] 7.1 运行 go test ./...、go vet ./...、受影响部署脚本测试和构建；检查 cklogs 既有合同回归与全部三份规格的关键允许/拒绝场景。
- [x] 7.2 使用 mock Jira 和临时宿主配置完成容器挂载、修改后重建、token轮换、跨服务/跨项目拒绝 smoke；确认配置未进入镜像，不连接或写入生产 Jira。
- [x] 7.3 校验文档 JSON/链接及 OpenSpec，记录实际执行的验证与未完成的真实 Jira 版本/字段/权限核验；交付调用方接口，不宣称完成调用方适配或生产部署。

## 8. 独立复评修正

- [x] 8.1 为 Jira 客户端响应增加独立写 deadline，保留上游 FIFO 的职责；用真实慢读连接验证退出。
- [x] 8.2 在 Jira 上传前校验完整 multipart HTTP body 大小，覆盖 Content-Length、chunked 和尾部数据。
- [x] 8.3 拒绝无法保真往返的 JSON 数字，覆盖配置、请求、默认值比较及上游响应。
- [x] 8.4 按字段存在性验证搜索时间参数成对且非空，补空值回归。
- [x] 8.5 app-stack override 透传 Jira 上游环境变量，并验证 Compose 实际合并结果。
- [x] 8.6 固化正向业务和分页回归，说明空 issueKeys 语义，更新验证及复评修复记录。
