## 1. 实测 LogBot（阻塞后续决策）

- [x] 1.1 ~~确认 Bearer 服务令牌可用~~ **已替代**：实测 Bearer 仍返回 401「无效的日志服务令牌」，改用 `POST /api/login` 表单登录 cookie 验证可用（角色 ops），客户端已按 cookie 方案实现并保留 bearer 备选分支。遗留：Bearer 令牌有效性待 LogBot 管理员确认
- [x] 1.2a 获取 LogBot `API.md` 与 `translog-fields.md`；用真实邮件分别提交 DA、MTA、PROXYSVR 查询（已完成）。样本仅保存在本机 `/tmp/logbot-samples/`（含真实邮箱、未脱敏、不入仓）
- [x] 1.2b ~~将脱敏样本落盘到 `internal/cklogs/testdata/logbot/`~~ **已替代**：真实样本含真实邮箱未脱敏不入仓，仓库测试改用构造的脱敏数据（logbot_map_test.go 黄金测试、kibana/logbot 共有字段对照）
- [x] 1.3 逐项核实 tid、subject、msgId 筛选，以及 dasvr、pop3、wmsvr 对应能力是否可用，输出"Kibana dataset → LogBot 来源/不支持"对照表，并更新 spec 中的 `not_available` 清单
- [x] 1.4a 展示上限与 `loaded`/`limit` 的关系（已完成）：每来源 1000 行，达到时 `status=limit`、`loaded=1000`，已实测
- [x] 1.4b 429 限频阈值 **未实测（已知限制，不再追测）**：查询间隔 ≥3 秒未触发 429；代码已对 429 返回可重试错误，阈值待上线后观察

## 2. 配置

- [x] 2.1 在 `internal/config/file.go` 中增加 `cklogs.provider`（kibana|logbot，默认 kibana）和 `cklogs.logbot`（baseUrl、auth.type=bearer、auth.token、pollIntervalMs、timeoutMs），并做严格校验；令牌拒绝占位符和空白
- [x] 2.2 在 `config_test.go`/`diagnostics_test.go` 中增加非法 provider、缺凭据、未知键用例
- [x] 2.3 docker-compose 增加 `extra_hosts`；配置示例中不包含真实凭据

## 3. Provider 抽象

- [x] 3.1 在 `internal/cklogs` 中定义 `Backend` 接口（按来源取行、按 tid 取轨迹），把现有 `Client.msearch`+DSL 包装成 Kibana 实现，现有测试全部通过
- [x] 3.2 `Service` 改为依赖 `Backend`；`cmd/gateway/main.go` 按配置装配，`/ready` 按所选 provider 判断

## 4. LogBot 客户端

- [x] 4.1 `logbot_client.go` 鉴权：`POST /api/login` 取 cookie 并缓存，401/重定向时加锁重登一次；仍失败映射为 auth_error；凭据不进日志（Bearer 作为备选分支保留）
- [x] 4.2 `logbot_client.go`：表单提交 query-jobs，轮询并退避，受预算约束，处理 409/429/4xx/5xx 错误
- [x] 4.3 来源状态映射：complete/limit/partial/failed/expired/skipped_future → truncated/limitations/status
- [x] 4.4 `logbot_map.go`：把 LogBot 列映射到现有 Entry/TraceEvent/AuthEntry，复用 map.go 转换函数
- [x] 4.5 审计：在 `internal/auditlog` 中增加 LogBot 调用记录（阶段、job_id、来源状态、耗时），不含凭据

## 5. 业务流程对齐

- [x] 5.1 自助与分析 delivery（outbound/inbound/both）走 LogBot：DA 只取 `cmd=local/remote` 行并去重，时间转 UTC，域名查询加 `platform_mode=all`，保留已发送副本过滤、收件人回退、分页、countOnly；`folderName`、`deliveryStatus` 等由网关推导
- [x] 5.2 `/v1/cklogs/message` 在 logbot provider 下返回 not_available（LogBot 无 tid 列和筛选）；msgId 筛选同理
- [x] 5.3 `/login`、`analysis/auth`（pop3、webmail）、`analysis/ops` 返回 not_available 并附说明；SMTP 登录能力仅限 MTA AUTH 行，不做按账号过滤承诺

## 6. 测试与文档

- [x] 6.1 用 httptest 模拟 LogBot：成功、轮询超时、limit、partial、failed、401/403/3xx 鉴权失败、429
- [x] 6.2 字段映射黄金测试与 kibana/logbot 共有字段对照（使用构造的脱敏数据；真实样本含真实邮箱未脱敏，不入仓，见 1.2b）
- [x] 6.3 运行 `go test ./...` 与 `go vet ./...`
- [x] 6.4 更新 README、docs/api.md、docs/gateway-auth.md：provider 配置、切换与回滚、能力缺口清单
