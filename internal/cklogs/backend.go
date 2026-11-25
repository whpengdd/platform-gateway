package cklogs

import (
	"context"
	"time"
)

// Backend 是 cklogs 的日志后端抽象，在「按来源取行」层切分：
// 上层编排（delivery/mta/proxy/account/trace）不感知 provider 差异。
// KibanaBackend 包装现有的 Kibana console proxy 客户端，
// LogBotBackend 走 LogBot 异步任务 API（logbot_*.go）。
type Backend interface {
	// Name 返回 provider 标识（"kibana" 或 "logbot"），用于少数无法抽象的能力分支。
	Name() string
	// Configured 报告所选 provider 的凭据是否齐全；缺失时请求返回 not_available。
	Configured() bool
	// Timeout 返回单次后端调用的预算上限。
	Timeout() time.Duration
	// MaxDetailRows 返回该后端单次可完整取回的明细行数上限
	//（Kibana 单页 HardSize；LogBot 展示上限 1000）。
	MaxDetailRows() int
	Query(ctx context.Context, filters Filters, opts QueryOptions) QueryResult
	QueryAuth(ctx context.Context, filters Filters, opts QueryOptions) (QueryResult, []AuthEntry)
	Trace(ctx context.Context, tid string, tr TimeRange) TraceResult
}

// KibanaBackend 把现有 Client（msearch + DSL）包装成 Backend，行为不变。
type KibanaBackend struct {
	Client *Client
}

func (b KibanaBackend) Name() string { return "kibana" }

func (b KibanaBackend) Configured() bool { return b.Client != nil && b.Client.Configured() }

func (b KibanaBackend) Timeout() time.Duration {
	if b.Client == nil {
		return DefaultTimeoutMS * time.Millisecond
	}
	return b.Client.timeout()
}

func (b KibanaBackend) MaxDetailRows() int { return HardSize }

func (b KibanaBackend) Query(ctx context.Context, filters Filters, opts QueryOptions) QueryResult {
	if b.Client == nil {
		return QueryResult{ErrorKind: "not_configured", Message: "CK_LOGS_BASIC_USER/CK_LOGS_BASIC_PASS 未配置"}
	}
	return b.Client.Query(ctx, filters, opts)
}

func (b KibanaBackend) QueryAuth(ctx context.Context, filters Filters, opts QueryOptions) (QueryResult, []AuthEntry) {
	if b.Client == nil {
		return QueryResult{ErrorKind: "not_configured", Message: "CK_LOGS_BASIC_USER/CK_LOGS_BASIC_PASS 未配置"}, nil
	}
	return b.Client.QueryAuth(ctx, filters, opts)
}

func (b KibanaBackend) Trace(ctx context.Context, tid string, tr TimeRange) TraceResult {
	if b.Client == nil {
		return TraceResult{ErrorKind: "not_configured", Message: "CK_LOGS_BASIC_USER/CK_LOGS_BASIC_PASS 未配置"}
	}
	return b.Client.Trace(ctx, tid, tr)
}
