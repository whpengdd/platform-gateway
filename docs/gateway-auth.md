# 网关统一应用配置

状态：本仓文件认证已实现；生产部署和调用方迁移另行执行。Jira 业务合同见 [jira-api.md](jira-api.md)，当前 cklogs 合同见 [api.md](api.md)。

## 1. 一个文件管理 cklogs/Jira token 和项目过滤

所有入站 token、上游连接、监听、CIDR、队列和审计设置只从一个 JSON 文件读取。默认读取进程工作目录下的 `./config.json`，不是可执行文件所在目录。使用默认位置时无需设置环境变量；需要覆盖路径时可设置：

```dotenv
GATEWAY_CONFIG_FILE=/custom/path/config.json
```

环境变量只指定路径，不再传 token 或项目列表。完整示例见 [gateway-auth.example.json](gateway-auth.example.json)：

```json
{
  "server": {
    "listenAddr": ":8091",
    "allowCidrs": []
  },
  "audit": {
    "enabled": true,
    "dir": "/var/log/platform-gateway"
  },
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
    "baseUrl": "https://jira.example.com/jira",
    "auth": {
      "type": "bearer",
      "token": "<jira-upstream-token>"
    },
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
  },
  "cklogs": {
    "baseUrl": "https://ck-logs.icoremail.net",
    "auth": {
      "type": "basic",
      "username": "<user>",
      "password": "<password>"
    },
    "index": "mtatrans_distributed",
    "timeoutMs": 60000,
    "queue": {
      "maxConcurrency": 2,
      "size": 32,
      "waitTimeoutMs": 30000
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
- 不读取、不检测、不告警残留旧应用环境变量；旧值不覆盖文件、不参与授权，也不阻止启动。不提供兼容别名或回退。
- 没有 Jira 条目时 Jira 模块不启用，也不要求 Jira 上游配置；只有 Jira 条目时不要求 cklogs 凭据。没有对应条目时仅豁免缺失的必需字段，显式提供的设置仍校验。全局 readiness 检查所有已启用模块的配置是否就绪；上游诊断走各业务模块，不把未启用服务算作失败。

错误行为：缺少或未知 token → 401 unauthorized；已知 token 访问另一服务，或 external 访问 analysis → 403 token_scope_forbidden；Jira token 访问未授权项目 → 403 project_access_forbidden。任何有效 token 都不能自动通过所有业务入口。

## 5. 部署与旧 cklogs 迁移

文件包含真实凭据，应放仓库外或忽略路径，仅运维和网关进程可读；宿主机维护 config.json，容器通过文件 bind mount 读取；容器只读，宿主机可修改。实际文件不提交 Git，不放镜像中，不挂载给 Zammad 或其他调用方。

容器挂载示例（已应用于本仓 Compose；以下是需要合并到服务定义的片段，不是完整 Compose 文件）：

```yaml
services:
  platform-gateway:
    working_dir: /
    environment:
      GATEWAY_CONFIG_FILE: /config.json
    volumes:
      - type: bind
        source: ./config.json
        target: /config.json
        read_only: true
        bind:
          create_host_path: false
```

这里宿主机 source 相对 Compose 文件解析；容器工作目录显式设为 `/`，因此默认 `./config.json` 对应 `/config.json`。部署声明显式设置 GATEWAY_CONFIG_FILE=/config.json，也不把真实文件 COPY 进镜像。创建容器前先准备宿主文件，缺文件报错，不自动创建同名目录。保留服务原有日志等挂载。

当前镜像以 UID 65532 的 nonroot 用户运行，宿主文件须通过匹配的属主/组权限让该用户可读，不必向其他用户开放读取。已将真实 `/config.json` 排除出 Git 和 Docker 构建上下文；本仓只保存不含凭据的示例。

修改宿主配置后重新加载进程才能生效，第一版不监听文件变化。由于编辑器可能通过替换文件保存，统一采用重新创建容器以重新挂载当前文件：

```bash
docker compose up -d --force-recreate --no-deps platform-gateway
```

这样修改 token/项目无需重新构建镜像；裸机修改文件后重启 gateway 进程即可。

## 6. 上游、默认值与部署迁移

所有 Jira 项目共用 `jira.baseUrl`（HTTPS，允许 `/jira` 部署路径，不能包含 REST API 后缀）及一组上游凭据。客户端追加 `/rest/api/2/...`。Basic 方式须将整个 `jira.auth` 替换为：

```json
{"type": "basic", "username": "<jira-user>", "password": "<jira-password>"}
```

Bearer 与 Basic 字段不能混用；凭据必须非空且不能含 CR/LF。JSON 解码后的美元符号、引号和空格原样使用，没有 shell source 或环境插值。CKLogs 只支持 Basic。服务是否启用由 tokens 决定。

| 可选字段 | 缺失时默认值 |
|---|---|
| server.listenAddr / allowCidrs | :8091 / []（不限制来源） |
| audit.enabled / dir | true / logs |
| cklogs.baseUrl / index | https://ck-logs.icoremail.net / mtatrans_distributed |
| cklogs.timeoutMs | 60000 |
| cklogs.queue.maxConcurrency / size / waitTimeoutMs | 2 / 32 / 30000 |

只有缺失使用默认值。未知/重复键、typed null、错误类型、非法 CIDR、监听地址和超过 1 MiB 的文件拒绝。时间为正整数毫秒：timeoutMs 最大 9223372031854（HTTP 客户端额外加 5 秒），waitTimeoutMs 最大 9223372036854。队列 size 可为 0；并发必须 >= 1。createDefaults 保留既有数字保真限制，接受的数字以 json.Number 保存而不转为 float64。

宿主配置将 `server.listenAddr` 改为 `127.0.0.1:8091`，`audit.dir` 可设为 `logs/platform-gateway`。相对审计路径相对进程工作目录，**不相对配置文件目录**；host launcher 的 cwd 是 app-root，配置路径默认 app-root/config.json。`--config-file` 相对 app-root 解析，或使用绝对路径。宿主健康探测使用文件内监听地址和端口。

容器示例监听 `:8091`，审计目录 `/var/log/platform-gateway` 必须对应可写挂载。提前创建审计宿主目录并授予 UID/GID 65532 所需的写入权限。self Compose 可用 PLATFORM_GATEWAY_HOST_PORT、PLATFORM_GATEWAY_CONTAINER_PORT 调整端口映射（后者必须匹配 JSON），用 PLATFORM_GATEWAY_LOG_HOST_DIR 指定日志宿主目录。app-stack 的端口/日志挂载由基础 Compose 声明；健康探测宿主端口可通过 PLATFORM_GATEWAY_HOST_PORT 指定，不覆盖 JSON。

`audit.enabled=false` 关闭 JSONL 审计，不创建审计目录、不进行写入检查；普通 stdout/stderr 日志仍保留，host launcher 写 app-root/gateway.log。`audit.dir` 的 off 和 - 只是路径，不是关闭别名。启用审计时，启动前必须能在目录创建临时文件并追加打开当日日志，否则拒绝启动；不会截断旧日志、写入伪记录或遗留探测文件。容器运行时可能为 Compose 声明的绑定挂载准备宿主目录；关闭审计且无需该挂载时可从部署声明移除日志挂载。

迁移字段表仅供人工更新部署，不是运行时旧环境检测列表：

| 旧设置 | 新设置 |
|---|---|
| JIRA_BASE_URL | jira.baseUrl |
| JIRA_API_TOKEN | jira.auth.type=bearer / token |
| JIRA_BASIC_USER / JIRA_BASIC_PASSWORD | jira.auth.type=basic / username / password |
| CK_LOGS_BASE_URL | cklogs.baseUrl |
| CK_LOGS_BASIC_USER / CK_LOGS_BASIC_PASS | cklogs.auth.type=basic / username / password |
| CK_LOGS_INDEX / CK_LOGS_TIMEOUT_MS | cklogs.index / timeoutMs |
| CKLOGS_MAX_CONCURRENCY / CKLOGS_QUEUE_SIZE / CKLOGS_QUEUE_WAIT_MS | cklogs.queue.maxConcurrency / size / waitTimeoutMs |
| LISTEN_ADDR / GATEWAY_ALLOW_CIDRS | server.listenAddr / allowCidrs（字符串数组） |
| GATEWAY_LOG_DIR | audit.dir；关闭使用 audit.enabled=false |
| GATEWAY_AUTH_FILE | GATEWAY_CONFIG_FILE |
| PLATFORM_GATEWAY_AUTH_HOST_FILE | PLATFORM_GATEWAY_CONFIG_HOST_FILE |
| --auth-file / --listen | --config-file / server.listenAddr |

旧入站 GATEWAY_TOKEN_EXTERNAL、GATEWAY_TOKEN_INTERNAL 分别迁为 external/internal 条目；GATEWAY_AUTH_TOKENS 中每项迁为 external。消除重复 token，跨服务必须用不同凭据。调用方的 PLATFORM_GATEWAY_TOKEN / PLATFORM_GATEWAY_INTERNAL_TOKEN 及现有 Bearer 请求无需改变。

本次仅删除已迁入文件的重复应用配置处理；保留 env_file、宿主继承环境及无关 environment，不限制未来独立环境变量功能。残留旧值可以透传，但不生效。CKLogs 保留 HTTP_PROXY/HTTPS_PROXY/NO_PROXY 行为，Jira 继续直连；证书变量交给运行时处理。host 环境文件通用读取为字面 KEY=VALUE，不经 shell 执行。install/149 overlay 用 Compose `!reset` 删除基础服务的旧专用映射，保留 env_file 和无关映射；要求支持 `!reset` 的 Compose（验证版本 2.26.1）。

切换时同时更新镜像/二进制、完整 JSON 和部署脚本，核对监听、挂载和文件权限后重启/重建容器。只修改 JSON 不需要重建镜像，但原子替换文件后必须 recreate。回退也必须同时恢复旧版本镜像、旧格式文件与旧部署脚本。旧二进制不能读新字段，新版本没有双格式兼容期。本次不修改调用方业务 API，也不执行生产部署。
