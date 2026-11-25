package cklogs

import (
	"context"
	"net/url"
	"strings"
	"time"

	"platform-gateway/internal/auditlog"
)

// LogBotBackend 把 LogBot 异步任务 API 包装成 Backend。
// 支持 DA/MTA/PROXYSVR 三个 transmission 来源；dasvr 投递流水、登录/操作日志、
// 按 tid/msgId 筛选在 LogBot 上不可用，返回 ErrorKind="not_available"。
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
	if filters.MsgID != "" || filters.Tid != "" {
		// 实测：LogBot 没有 tid/msgId 筛选（mid 仅 DA 支持且与 Kibana tid 不同义）。
		return QueryResult{ErrorKind: "not_available", Message: "LogBot 暂不支持按 tid/msgId 筛选，无法执行该查询；这不等于「没有记录」，请改用发件人/收件人/主题/时间窗条件。"}
	}
	domain := strings.ToLower(strings.TrimSpace(filters.Domain))
	buildForm := func(fromAddr, toAddr string) url.Values {
		form := url.Values{}
		form.Set("query_type", "transmission")
		form.Set("start_time", logbotFormTime(filters.TimeRange.FromMs))
		// LogBot 起止时间为秒精度且含两端；Kibana 语义是 [from, to)，故终点回退 1ms。
		endMs := filters.TimeRange.ToMs - 1
		if endMs < filters.TimeRange.FromMs {
			endMs = filters.TimeRange.FromMs
		}
		form.Set("end_time", logbotFormTime(endMs))
		form.Add("log_source[]", source)
		if fromAddr != "" {
			form.Set("from_addr", fromAddr)
		}
		if toAddr != "" {
			form.Set("to_addr", toAddr)
		}
		// 实测 subject 筛选适用于 DA/MTA；PROXYSVR 不推送，由本地过滤兜底。
		if filters.Subject != "" && source != logbotSourceProxy {
			form.Set("subject", filters.Subject)
		}
		if domain != "" {
			// 域名只能带在邮箱字段中；未映射的域名需要 platform_mode=all，否则 0 命中。
			form.Set("platform_mode", "all")
		}
		return form
	}
	// 域名查询（实测语义）：from_addr/to_addr 填裸域名时按地址的域名部分精确匹配
	// （不是子串/后缀匹配），同一表单内两个地址字段是 AND 关系，无法表达
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
			return b.toQueryResult(b.runSingle(ctx, buildForm(filters.Sender, toAddr), source), source, opts)
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
				run := b.runSingle(ctx, buildForm(fromAddr, filters.Recipient), source)
				if run.fail != nil {
					return *run.fail
				}
				runs = append(runs, run)
			}
			if runTo {
				run := b.runSingle(ctx, buildForm(filters.Sender, toAddr), source)
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
			return b.toQueryResult(b.runSingle(ctx, buildForm(fromAddr, filters.Recipient), source), source, opts)
		}
	}
	return b.toQueryResult(b.runSingle(ctx, buildForm(filters.Sender, filters.Recipient), source), source, opts)
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

func (b LogBotBackend) runSingle(ctx context.Context, form url.Values, source string) logbotRun {
	res, fail := b.Client.runQuery(ctx, form, []string{source})
	return logbotRun{result: res, fail: fail}
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
	merged := &logbotJobResult{
		Sources: map[string]logbotSourceStatus{},
	}
	seen := map[string]bool{}
	for _, run := range []logbotRun{outbound, inbound} {
		for _, row := range run.result.Data {
			key := logbotRowKey(row, source)
			if seen[key] {
				continue
			}
			seen[key] = true
			merged.Data = append(merged.Data, row)
		}
		for name, st := range run.result.Sources {
			cur, ok := merged.Sources[name]
			// 合并同一来源的两次执行：取较差的状态、loaded 取去重前的总和无意义，
			// 以去重后的行数为准（在 logbotResultToQuery 中重算）。
			if !ok || sourceStatusRank(st.Status) > sourceStatusRank(cur.Status) {
				merged.Sources[name] = st
			}
		}
	}
	return logbotResultToQuery(merged, source, opts.CountOnly, b.secrets())
}

func sourceStatusRank(status string) int {
	switch status {
	case "complete":
		return 0
	case "skipped_future":
		return 1
	case "limit":
		return 2
	case "partial", "queued", "running":
		return 3
	default: // failed/expired/unknown
		return 4
	}
}

// logbotResultToQuery 把任务结果映射为 QueryResult：DA 只取 cmd=local/remote 行并去重，
// 再按投递分组键（hdrmsgid → commrefid → 单行）把同一次投递同一收件人的多行
// 折成一条；行映射到 Entry，来源状态映射为 Truncated/Partial/Limitations。
func logbotResultToQuery(res *logbotJobResult, source string, countOnly bool, secrets []string) QueryResult {
	var rows []map[string]any
	seen := map[string]bool{}
	for _, row := range res.Data {
		if rowSource := asString(row["log_source"]); rowSource != "" && !strings.EqualFold(rowSource, source) {
			continue
		}
		if source == logbotSourceDA && !logbotDAMainCmd(row) {
			continue
		}
		key := logbotRowKey(row, source)
		if seen[key] {
			continue
		}
		seen[key] = true
		rows = append(rows, row)
	}
	unkeyed := 0
	if source == logbotSourceDA {
		rows, unkeyed = mergeLogbotDARows(rows)
	}
	var entries []Entry
	if !countOnly {
		entries = make([]Entry, 0, len(rows))
		for _, row := range rows {
			entries = append(entries, mapLogBotRow(row, source))
		}
		sortEntriesDesc(entries)
	}
	out, fail := assessSources([]string{source}, res.Sources, len(rows), secrets)
	if fail != nil {
		return *fail
	}
	if unkeyed > 0 {
		out.Limitations = append(out.Limitations, "部分 DA 行缺少 message-id（hdrmsgid）与投递关联键（commrefid），无法归并到具体邮件，已按命令行逐条保留；同一收件人的多封此类邮件在结果中可能被聚为一条，条数不代表邮件封数。")
	}
	out.Entries = entries
	return out
}

// logbotFormTime 按 Asia/Shanghai 输出 "YYYY-MM-DD HH:MM:SS"。
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
