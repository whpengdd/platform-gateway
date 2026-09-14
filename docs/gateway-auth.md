# 网关统一 token 配置

状态：本仓文件认证已实现；生产部署和调用方迁移另行执行。Jira 业务合同见 [jira-api.md](jira-api.md)，当前 cklogs 合同见 [api.md](api.md)。

## 1. 一个文件管理 cklogs/Jira token 和项目过滤

所有入站 token 只从一个 JSON 文件读取。默认读取进程工作目录下的 `./config.json`，不是可执行文件所在目录。使用默认位置时无需设置环境变量；需要覆盖路径时可设置：

```dotenv
GATEWAY_AUTH_FILE=/custom/path/config.json
```

环境变量只指定路径，不再传 token 或项目列表。完整示例见 [gateway-auth.example.json](gateway-auth.example.json)：

```json
{
  "tokens": [
    {
      "token": "<ck-external-token>",
      "cklogs": "external"
    },
    {
      "token": "<ck-internal-token>",
      "cklogs": "internal"
    },
    {
      "token": "<zammad-token-A>",
      "jiraProjects": [
        "CS",
        "IT"
      ]
    },
    {
      "token": "<zammad-token-B>",
      "jiraProjects": [
        "CS"
      ]
    }
  ],
  "jira": {
    "projects": {
      "CS": {
        "filterJql": "labels = zammad",
        "issueTypeId": "10504",
        "createDefaults": {
          "labels": [
            "zammad"
          ]
        },
        "createFields": [
          "summary",
          "description"
        ],
        "readFields": [
          "summary",
          "description",
          "status",
          "updated",
          "reporter",
          "creator",
          "assignee",
          "attachment"
        ]
      },
      "IT": {}
    }
  }
}
```

真实文件需把占位符替换为独立生成的随机 token；占位符不应被程序作为有效凭据接受。数组可继续增加条目，每个权限档均可配置多枚 token。

## 2. 只有三个条目字段

| 字段 | 说明 |
|---|---|
| token | 必填，随机、不透明的 Bearer 凭据；配置中唯一 |
| cklogs | cklogs token 必填，取 external 或 internal |
| jiraProjects | Jira token 必填，非空项目 key 数组，例如 ["CS", "IT"] |

每条记录必须且只能设置 cklogs、jiraProjects 其中一个。这样统一配置不会把两个服务的权限合并到同一凭据；Zammad 只拿 Jira token。不增加 name、clientId、角色、scopes、有效期等字段。

| 配置 | 允许访问 |
|---|---|
| cklogs=external | POST /v1/cklogs/delivery、/login、/message |
| cklogs=internal | external 的全部接口，以及 POST /v1/cklogs/analysis/delivery、/auth、/ops |
| jiraProjects=["CS","IT"] | GET /v1/jira/projects，以及 /v1/jira/projects/CS/... 和 /IT/... 的固定业务接口 |

internal 只是 cklogs 内部分析档，不是网关超级管理员，不能访问 Jira。Jira 按项目授权，可通过项目 filterJql 收窄 issue 集合。不配置过滤时，项目内 Jira 服务账号可见的全部 issue 均可通过固定 API 访问，仍受字段白名单和限额约束。同项目获准 token 使用相同过滤规则；网关不管理工单绑定、同步进度或业务幂等记录。

GET /health、/ready 的公开健康检查不因 token 配置变成业务授权入口，也不返回 token/项目清单。

## 3. 项目属性过滤

jira.projects 以项目 key 为键。示例 CS 使用 `labels = zammad`，IT 不附加过滤。示例仅展示配置形状，CS 实际创建必填字段尚须按 Jira 元数据补齐。

| 项目字段 | 说明 |
|---|---|
| filterJql | 可选 Jira 条件片段，不含 ORDER BY；空白或省略表示无附加限制 |
| issueTypeId | 启用创建时必填，固定创建类型 |
| createDefaults | 可选固定创建值，调用方不能覆盖；冲突返回 create_default_conflict |
| createFields | 创建时允许调用方提交的字段 ID；按元数据验证 |
| readFields | 覆盖默认读取字段列表，只能选择程序支持的字段和投影类型 |

查询强制组合 `(project = "CS") AND (<filterJql>) AND (<调用方受控条件>)`。JQL 由 Jira 执行，可使用合法 OR/函数；调用方不能提交 JQL。结构非法配置拒绝；上游明确判定 JQL 非法时项目禁用，修改配置并重启恢复。临时不可用返回 503，退避 5 秒后由下一次请求触发重验，同项目仅一个在途验证。禁止退回无过滤。

首版创建只支持无附加过滤或单条 `labels = 固定字符串`（允许整体外层括号），后者须由 createDefaults.labels 覆盖；其他合法 JQL 只关闭创建。自定义字段仍可用于受支持的业务创建值，只是不支持 cf[n] 创建证明。固定 project/type/bot reporter，禁止调用方或 defaults 覆盖 project/issuetype/reporter/assignee/security。元数据和必填值须验证可行；示例不是可直接上线的 CS 完整配置。

readFields 默认 summary/status/updated/reporter/creator/assignee；description、attachment、实际客户映射字段需显式加入。禁止 comment/issuelinks/subtasks/parent/worklog；自定义字段仅支持确认类型的标量、裁剪后的选项/人员及其数组，不透传任意对象。读取类型验证不依赖创建能力；未知类型拒绝，暂无法获取元数据时相关读取返回 503。详见 [字段兼容核对](jira-field-compatibility.md)。

过滤适用于现有 issue 及其评论/附件；每次访问检查父 issue。JQL 索引延迟和检查/写入竞态意味着它不提供即时撤权或原子授权保证。不增加独立项目文件、issue 白名单或同步状态。

## 4. 校验和生效

- 文件不存在、不可读、JSON 非法、未知/重复键、tokens 为空或重复 token，启动失败；不打印文件正文、token 或解析片段。
- token 不能为空或包含空白；示例占位符拒绝。缺少权限、同时声明两类权限、非法 cklogs 值或空 jiraProjects 均拒绝。
- Jira project key 使用 `[A-Z][A-Z0-9_]{0,63}`；禁止通配符和重复项目，引用未配置的业务项目时启动失败。
- 整个文件加载校验通过后才启用；修改 token、项目或 cklogs 档位后重启生效。第一版不做热更新和管理后台。
- 不支持 token 环境变量与文件混用；新实现遇到非空 GATEWAY_TOKEN_EXTERNAL、GATEWAY_TOKEN_INTERNAL、GATEWAY_AUTH_TOKENS、GATEWAY_JIRA_TOKENS 或 GATEWAY_JIRA_TOKENS_FILE 时明确报配置冲突，不回退或合并。后两项只是早期设计变量，从未实现。
- 没有 Jira 条目时 Jira 模块不启用，也不要求 Jira 上游配置；只有 Jira 条目时不要求 cklogs 凭据。有对应条目时才校验该服务的必需配置。全局 readiness 检查所有已启用模块的配置是否就绪；上游诊断走各业务模块，不把未启用服务算作失败。

错误行为：缺少或未知 token → 401 unauthorized；已知 token 访问另一服务，或 external 访问 analysis → 403 token_scope_forbidden；Jira token 访问未授权项目 → 403 project_access_forbidden。任何有效 token 都不能自动通过所有业务入口。

## 5. 部署与旧 cklogs 迁移

文件包含真实凭据，应放仓库外或忽略路径，仅运维和网关进程可读；宿主机维护 config.json，容器通过文件 bind mount 读取；容器只读，宿主机可修改。实际文件不提交 Git，不放镜像中，不挂载给 Zammad 或其他调用方。

容器挂载示例（已应用于本仓 Compose；以下是需要合并到服务定义的片段，不是完整 Compose 文件）：

```yaml
services:
  platform-gateway:
    working_dir: /
    volumes:
      - type: bind
        source: ./config.json
        target: /config.json
        read_only: true
        bind:
          create_host_path: false
```

这里宿主机 source 相对 Compose 文件解析；容器工作目录显式设为 `/`，因此默认 `./config.json` 对应 `/config.json`。不需要设置 GATEWAY_AUTH_FILE，也不把真实文件 COPY 进镜像。创建容器前先准备宿主文件，缺文件报错，不自动创建同名目录。保留服务原有日志等挂载。

当前镜像以 UID 65532 的 nonroot 用户运行，宿主文件须通过匹配的属主/组权限让该用户可读，不必向其他用户开放读取。已将真实 `/config.json` 排除出 Git 和 Docker 构建上下文；本仓只保存不含凭据的示例。

修改宿主配置后重新加载进程才能生效，第一版不监听文件变化。由于编辑器可能通过替换文件保存，统一采用重新创建容器以重新挂载当前文件：

```bash
docker compose up -d --force-recreate --no-deps platform-gateway
```

这样修改 token/项目无需重新构建镜像；裸机修改文件后重启 gateway 进程即可。

config.json 统一管理入站 token 和 jira.projects 项目规则。以下配置仍保留环境变量方式：

- CK_LOGS_BASE_URL、CK_LOGS_BASIC_USER/PASS：网关调用 Kibana 的地址和凭据。
- JIRA_BASE_URL 及 JIRA_API_TOKEN（或 JIRA_BASIC_USER/PASSWORD）：上游地址/凭据；不与入站 token 混用。
- LISTEN_ADDR、GATEWAY_ALLOW_CIDRS、日志与队列参数：继续使用环境变量，全局来源限制继续生效。

迁移步骤（部署新版时执行）：

1. 把 GATEWAY_TOKEN_EXTERNAL 中每一枚 token 写成一个 external 条目，把 GATEWAY_TOKEN_INTERNAL 写成 internal 条目。仅使用旧 GATEWAY_AUTH_TOKENS 时，全部迁为 external。
2. 若旧配置中同一个值重复或横跨内外档，先消除重复；横跨档位应拆成不同凭据并同步调用方，不能依赖旧代码“internal 优先”的行为继续运行。
3. 增加独立 Jira token 及项目列表；把宿主 ./config.json 按只读方式挂载到容器 /config.json，使用默认路径即可。
4. 从 gateway 的环境、Compose 显式 environment 和部署脚本移除旧 token 变量，然后重启新版服务。回归 external/internal 的允许、拒绝和跨服务拒绝路径。
5. 原 cklogs token 值未变时，调用方的 PLATFORM_GATEWAY_TOKEN / PLATFORM_GATEWAY_INTERNAL_TOKEN 无需改名或换值；它们仍通过 Authorization: Bearer 调用。Jira 调用方也只持有自己的 Bearer token。

原 rag-explorer-ai token 配对脚本只懂旧网关环境变量；部署切换时须同步其生成文件的方式，或由运维维护此文件。本次不修改该调用方仓库或 Zammad。

本配置不改变 cklogs 路径、请求体、返回体、external/internal 权限关系和共享队列。新增验证应覆盖文件错误、旧变量冲突、多 token、重复 token、缺权限及 cklogs/Jira 双向越权。

createDefaults 的数字不做静默舍入：仅接受 float64/JSON 往返保持数值的表示；不能保真的值会拒绝加载。客户端同样拒绝不能保真的数字，已支持的同值形式（如 1 与 1.0）可通过默认值一致性比较。

本仓 install/149 部署 overlay 显式透传 JIRA_BASE_URL、JIRA_API_TOKEN、JIRA_BASIC_USER、JIRA_BASIC_PASSWORD；在对应 Compose 项目的环境文件或宿主环境中配置。临时 overlay 只包含变量引用，实际凭据不写入该文件。
