# Proposal

## Why

`/v1/cklogs/*` 现在通过 Kibana console proxy（`ck-logs.icoremail.net`，`_msearch`）查询日志，Kibana 计划下线。LogBot（`https://logbot.icoremail.net`）提供独立的异步查询/导出 API，网关需要在不改变调用方合同的前提下切换到 LogBot，并保持原 Kibana 下的业务流程：按方向查 MTA、DA、PROXYSVR，按 tid 串联投递轨迹，返回相同的字段。

## What Changes

- 新增日志 provider 抽象，`config.json` 中 `cklogs.provider` 可选 `kibana`（默认）或 `logbot`，重启生效，可回滚。
- 新增 LogBot 客户端：使用 LogBot 管理员发放的 Bearer 服务令牌（`Authorization: Bearer <令牌>`），提交 `POST /api/query-jobs`，轮询 `GET /api/query-jobs/{job_id}`，按 `job.result.sources[].status` 判断各来源完整性，并在网关操作超时内完成。
- 把 LogBot 返回的行映射到现有 `Entry` / `TraceEvent` / `AuthEntry` 字段（mta→`MTA`，delivery_agent→`DA`，delivery_proxy→`PROXYSVR`），保留出站已发送副本过滤、收件人回退、垃圾邮件建议、分页和 `countOnly` 语义。
- 来源状态 `limit`/`partial`/`failed`/`expired`/`skipped_future` 映射为网关现有的 `truncated`、`limitations` 和 `status`，不把未完成扫描当作"无记录"。
- 实施第一步实测 LogBot 能否覆盖 Kibana 现有但 LogBot 指南未列出的能力：dasvr 投递流水、pop3/smtp 登录、webmail/pop3 操作日志，以及 tid/subject/msgId 过滤。核实结论决定这些能力在 logbot provider 下走 LogBot，或返回 `not_available` 并附说明。
- 调用方 HTTP 路径、请求字段、响应结构不变。

## Capabilities

### New Capabilities
- `cklogs-log-provider`: cklogs 接口的后端日志源选择，LogBot 会话、异步任务查询、来源完整性判断，以及与 Kibana 一致的字段映射。

### Modified Capabilities
（无。`gateway-file-auth` 中 cklogs 路由、权限、响应校验保持不变；新增配置项归入新 capability。）

## Impact

- 代码：`internal/cklogs`（新增 provider 接口与 LogBot 实现，`Service` 改为依赖 provider），`internal/config/file.go`（`cklogs.provider`、`cklogs.logbot`），`cmd/gateway/main.go`（装配、`/ready` 判定），`internal/auditlog`（记录 LogBot 调用）。
- 外部依赖：LogBot API（Bearer 服务令牌）；部署机需能解析 `logbot.icoremail.net`（内网 172.16.1.10，可写 hosts 或容器 `extra_hosts`）。
- 文档：`README.md`、`docs/api.md`、`docs/gateway-auth.md` 增补 provider 配置与切换说明。
- 凭据仅存于宿主 `config.json`，不入 Git、不入日志。
