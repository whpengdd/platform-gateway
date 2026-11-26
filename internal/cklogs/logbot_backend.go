package cklogs

import (
	"context"
	"strings"
	"time"

	"platform-gateway/internal/auditlog"
)

// LogBotBackend 把 LogBot agent 异步任务 API 包装成 Backend。
// 支持 DA/MTA/PROXYSVR 三个 transmission 来源；dasvr 投递流水、登录日志、
// 按 tid 筛选在 LogBot 上不可用，返回 ErrorKind="not_available"。
// msgId 仅在 DA 来源经 mid 筛选支持，其余来源按 not_available 处理。
// agent API 单任务时间窗上限 24h，更长窗口在本层切分串行提交后合并。
type LogBotBackend struct {
	Client *LogBotClient
}

func (b LogBotBackend) Name() string { return "logbot" }

func (b LogBotBackend) Configured() bool { return b.Client != nil && b.Client.Configured() }

func (b LogBotBackend) Timeout() time.Duration {
	if b.Client == nil {
		return DefaultOpTimeout
	}
	return b.Client.timeout()
}

func (b LogBotBackend) MaxDetailRows() int { return LogBotDisplayLimit }

const (
	logbotSourceDA    = "DA"
	logbotSourceMTA   = "MTA"
	logbotSourceProxy = "PROXYSVR"
)

func (b LogBotBackend) notConfigured() QueryResult {
	return QueryResult{ErrorKind: "not_configured", Message: "LogBot 账号未配置（cklogs.logbot.auth）"}
}

func (b LogBotBackend) Query(ctx context.Context, filters Filters, opts QueryOptions) QueryResult {
	source := logbotSourceMTA
	if opts.Dataset != "" {
		ds := ResolveDataset(opts.Dataset)
		if ds == nil {
			return QueryResult{ErrorKind: "unknown_dataset", Message: "未知 dataset：" + opts.Dataset}
		}
		if ds.Unavailable != "" {
			return QueryResult{ErrorKind: ds.Unavailable, Message: UnavailableNote(ds.Unavailable)}
		}
		switch ds.Kind {
		case "delivery":
			source = logbotSourceMTA
		case "delivery_agent":
			source = logbotSourceDA
		case "delivery_proxy":
			source = logbotSourceProxy
		case "delivery_pipeline":
			return QueryResult{ErrorKind: "not_available", Message: "LogBot 未提供 dasvr 投递流水来源（该日志类型在 LogBot 暂不支持查询）。这不等于「没有记录」。"}
		default:
			return QueryResult{ErrorKind: "not_available", Message: "LogBot 未提供「" + ds.Key + "」对应的日志来源（登录/操作日志在 LogBot 暂不支持查询），未执行任何查询。这不等于「没有记录」。"}
		}
	}
	if !b.Configured() {
		return b.notConfigured()
	}
	if filters.Tid != "" {
		return QueryResult{ErrorKind: "not_available", Message: "LogBot 不提供 tid 列也不支持按 tid 筛选，无法执行该查询；这不等于「没有记录」，请改用发件人/收件人/主题/时间窗条件。"}
	}
	msgID := strings.TrimSpace(filters.MsgID)
	if msgID != "" && source != logbotSourceDA {
		return QueryResult{ErrorKind: "not_available", Message: "LogBot 的 msgId 筛选（mid）仅投递日志（DA）来源可用，当前查询来源不支持；这不等于「没有记录」。"}
	}
	domain := strings.ToLower(strings.TrimSpace(filters.Domain))
	buildReq := func(fromAddr, toAddr string, fromMs, toMs int64) logbotAgentRequest {
		req := logbotAgentRequest{
			QueryType: "transmission",
			StartTime: logbotFormTime(fromMs),
			// agent 区间为 [start,end)；Kibana 语义也是 [from, to)，终点回退 1ms。
			EndTime:    logbotFormTime(toMs - 1),
			LogSources: []string{source},
			Purpose:    "platform-gateway cklogs " + opts.Dataset,
		}
		if req.EndTime < req.StartTime {
			req.EndTime = req.StartTime
		}
		f := map[string]any{}
		if fromAddr != "" {
			f["from_addr"] = fromAddr
		}
		if toAddr != "" {
			f["to_addr"] = toAddr
		}
		if msgID != "" {
			f["mid"] = msgID
		}
		// 实测 subject 筛选适用于 DA/MTA；PROXYSVR 侧效果未验证，保持不推、由服务层过滤兜底。
		if filters.Subject != "" && source != logbotSourceProxy {
			f["subject"] = filters.Subject
		}
		if domain != "" {
			// 域名只能带在邮箱字段中；未映射的域名需要 platform_mode=all，否则 0 命中。
			req.PlatformMode = "all"
		}
		req.Filters = f
		return req
	}
	// 域名查询（实测语义）：from_addr/to_addr 填裸域名时按地址的域名部分精确匹配
	// （不是子串/后缀匹配），同一请求内两个地址字段是 AND 关系，无法表达
	// 「发件人或收件人属于该域」的 OR；platform_mode=all 必须带上，否则未映射
	// 到本平台的域名恒 0 命中。故按方向把域名落到 from_addr/to_addr，
	// both 时发两个任务并在本地合并去重。
	if domain != "" {
		// 显式发件人/收件人会收窄对应一侧的域句子：显式地址本身不在该域内时，
		// 该侧「这一端属于域名 D」恒不成立，可直接跳过。
		fromAddr, fromOK := domainSideAddr(filters.Sender, domain)
		toAddr, toOK := domainSideAddr(filters.Recipient, domain)
		switch filters.Direction {
		case "inbound":
			if !toOK {
				return emptyDomainResult()
			}
			return b.toQueryResult(b.runWindowed(ctx, buildReq, filters.Sender, toAddr, filters.TimeRange, source, opts), source, opts)
		case "both":
			// 一侧子句被另一侧完全包含时不必重复发任务：
			// 显式发件人在域内时 to 侧结果是 from 侧子集，收件人同理。
			subsumesTo := fromOK && filters.Sender != ""
			subsumesFrom := toOK && filters.Recipient != ""
			runFrom := fromOK && !subsumesFrom
			runTo := toOK && !subsumesTo
			if subsumesFrom && subsumesTo {
				runTo = false
				runFrom = true
			}
			var runs []logbotRun
			if runFrom {
				run := b.runWindowed(ctx, buildReq, fromAddr, filters.Recipient, filters.TimeRange, source, opts)
				if run.fail != nil {
					return *run.fail
				}
				runs = append(runs, run)
			}
			if runTo {
				run := b.runWindowed(ctx, buildReq, filters.Sender, toAddr, filters.TimeRange, source, opts)
				if run.fail != nil {
					return *run.fail
				}
				runs = append(runs, run)
			}
			switch len(runs) {
			case 0:
				return emptyDomainResult()
			case 1:
				return b.toQueryResult(runs[0], source, opts)
			}
			return b.mergeDomainResults(runs[0], runs[1], source, opts)
		default: // outbound 或缺省
			if !fromOK {
				return emptyDomainResult()
			}
			return b.toQueryResult(b.runWindowed(ctx, buildReq, fromAddr, filters.Recipient, filters.TimeRange, source, opts), source, opts)
		}
	}
	return b.toQueryResult(b.runWindowed(ctx, buildReq, filters.Sender, filters.Recipient, filters.TimeRange, source, opts), source, opts)
}

// domainSideAddr 计算域名查询某一侧的地址条件：无显式地址时用域名本身匹配
// 该侧任意地址；显式地址在该域内时约束被地址吸收；显式地址不在该域内时
// 该侧无法满足「属于域名 D」，返回 ok=false。
func domainSideAddr(addr, domain string) (string, bool) {
	if addr == "" {
		return domain, true
	}
	if addressDomain(addr) == domain {
		return addr, true
	}
	return "", false
}

// emptyDomainResult：域名子句恒不成立时返回精确的 0 命中，不发任务。
func emptyDomainResult() QueryResult {
	return QueryResult{OK: true, Entries: []Entry{}, TotalExact: true, TotalRelation: "eq"}
}

// runWindowed 按 ≤24h 切分时间窗串行提交 agent 任务并合并结果。
// 任一分段失败即返回错误（失败窗口行缺失，结果不能冒充完整）；
// countOnly 时各分段只取 total_count。
func (b LogBotBackend) runWindowed(ctx context.Context, buildReq func(fromAddr, toAddr string, fromMs, toMs int64) logbotAgentRequest, fromAddr, toAddr string, tr TimeRange, source string, opts QueryOptions) logbotRun {
	merged := &logbotJobResult{TotalExact: true}
	start := tr.FromMs
	for {
		end := tr.ToMs
		if start+logbotAgentMaxWindow.Milliseconds() < end {
			end = start + logbotAgentMaxWindow.Milliseconds()
		}
		req := buildReq(fromAddr, toAddr, start, end)
		res, fail := b.Client.runQuery(ctx, req, []string{source}, LogBotDisplayLimit, !opts.CountOnly)
		if fail != nil {
			return logbotRun{fail: fail}
		}
		merged.Rows = append(merged.Rows, res.Rows...)
		merged.Total += res.Total
		merged.Truncated = merged.Truncated || res.Truncated
		merged.TotalExact = merged.TotalExact && res.TotalExact
		if end >= tr.ToMs {
			break
		}
		start = end
	}
	return logbotRun{result: merged}
}

type logbotRun struct {
	result *logbotJobResult
	fail   *QueryResult
}

func (b LogBotBackend) secrets() []string {
	if b.Client == nil {
		return nil
	}
	return b.Client.secrets()
}

func (b LogBotBackend) toQueryResult(r logbotRun, source string, opts QueryOptions) QueryResult {
	if r.fail != nil {
		return *r.fail
	}
	return logbotResultToQuery(r.result, source, opts.CountOnly, b.secrets())
}

// mergeDomainResults 合并 both 方向的两个域名查询结果。
func (b LogBotBackend) mergeDomainResults(outbound, inbound logbotRun, source string, opts QueryOptions) QueryResult {
	merged := &logbotJobResult{TotalExact: true}
	seen := map[string]bool{}
	for _, run := range []logbotRun{outbound, inbound} {
		for _, row := range run.result.Rows {
			key := logbotRowKey(row, source)
			if seen[key] {
				continue
			}
			seen[key] = true
			merged.Rows = append(merged.Rows, row)
		}
		merged.Total += run.result.Total
		merged.Truncated = merged.Truncated || run.result.Truncated
		merged.TotalExact = merged.TotalExact && run.result.TotalExact
	}
	if opts.CountOnly {
		// 两侧合计会重复计算重叠行，标注为不精确。
		merged.TotalExact = false
	}
	return logbotResultToQuery(merged, source, opts.CountOnly, b.secrets())
}

// logbotFormTime 按 Asia/Shanghai 输出 agent API 要求的 "YYYY-MM-DD HH:MM:SS"。
func logbotFormTime(ms int64) string {
	return time.UnixMilli(ms).In(auditlog.Shanghai()).Format("2006-01-02 15:04:05")
}

func (b LogBotBackend) QueryAuth(ctx context.Context, filters Filters, opts QueryOptions) (QueryResult, []AuthEntry) {
	ds := ResolveDataset(opts.Dataset)
	key := opts.Dataset
	if ds != nil {
		key = ds.Key
	}
	return QueryResult{ErrorKind: "not_available", Message: "LogBot 未提供「" + key + "」对应的登录/操作日志来源（该日志类型在 LogBot 暂不支持查询），未执行任何查询。这不等于「没有记录」。"}, nil
}

func (b LogBotBackend) Trace(ctx context.Context, tid string, tr TimeRange) TraceResult {
	return TraceResult{ErrorKind: "not_available", Message: "LogBot 不提供 tid 列，也不支持按 tid 筛选，按 tid 串联投递轨迹暂不支持。这不等于「没有记录」。"}
}
