package cklogs

import (
	"bytes"
	"encoding/json"
	"strings"
)

const (
	defaultIndex = "mtatrans_distributed"
)

type Dataset struct {
	Key              string
	Index            string
	Kind             string
	Protocol         string
	AccountField     string
	AccountMatch     string
	AccountTransform string
	VerifyAccount    bool
	Fixed            []any
	Ops              map[string][]any
	Unavailable      string
}

const (
	IMAPUnavailableNote = "IMAP 侧日志不可查:平台 imaptrans 的 index pattern 存在但底层表不存在" +
		"(There is no table imaptrans),该协议的登录与删信记录无法取证,需转人工或向平台方确认。" +
		"注意:这不等于「没有记录」,不得据此答复客户「未发现操作」。"

	WebmailLoginUnavailableNote = "Webmail 登录记录不可按账号检索:wmsvr 的登录行(func=user:login)" +
		"user 列恒为空,账号只存在于 user:loginNotify 行的 requestvar,而该列在 ClickHouse 侧不能作为查询条件" +
		"(term/match_phrase 均 0 命中)。因此本档按账号查恒为 0 条,属结构性查不到。" +
		"注意:这不等于「该账号没有登录」,不得据此答复客户「未发现异常登录」;" +
		"需要 webmail 登录取证请转人工或向平台方确认。POP3/SMTP 两档不受影响,可正常检索。"
)

var datasets = map[string]Dataset{
	"delivery":          {Index: "mtatrans_distributed", Kind: "delivery"},
	"delivery_agent":    {Index: "datrans_distributed", Kind: "delivery_agent"},
	"delivery_pipeline": {Index: "dasvr_distributed", Kind: "delivery_pipeline"},
	"auth_pop3": {
		Index:        "pop3trans_distributed",
		Kind:         "auth",
		Protocol:     "pop3",
		AccountField: "loginname",
		Fixed:        []any{map[string]any{"term": map[string]any{"cmd": "login"}}},
	},
	"auth_smtp": {
		Index:        "mtatrans_distributed",
		Kind:         "auth",
		Protocol:     "smtp",
		AccountField: "authuser",
		Fixed:        []any{map[string]any{"term": map[string]any{"cmd": "AUTH"}}},
	},
	"auth_webmail": {Unavailable: "webmail_login_account_not_queryable", Protocol: "webmail"},
	"mail_ops_webmail": {
		Index:        "wmsvr_distributed",
		Kind:         "ops",
		Protocol:     "webmail",
		AccountField: "user",
		Ops: map[string][]any{
			"delete":       {map[string]any{"match_phrase": map[string]any{"func": "mbox:deleteMessages"}}},
			"forward":      {map[string]any{"match_phrase": map[string]any{"func": "mbox:forwardMessages"}}},
			"empty_folder": {map[string]any{"match_phrase": map[string]any{"func": "mbox:emptyFolder"}}},
			"recall":       {map[string]any{"match_phrase": map[string]any{"func": "mbox:recallMessage2"}}},
		},
	},
	"mail_ops_pop3": {
		Index:            "pop3trans_distributed",
		Kind:             "ops",
		Protocol:         "pop3",
		AccountField:     "dn",
		AccountMatch:     "match_phrase",
		AccountTransform: "localpart",
		VerifyAccount:    true,
		Ops:              map[string][]any{"delete": {map[string]any{"term": map[string]any{"cmd": "del"}}}},
	},
	"auth_imap":     {Unavailable: "imap_table_missing", Protocol: "imap"},
	"mail_ops_imap": {Unavailable: "imap_table_missing", Protocol: "imap"},
}

func UnavailableNote(reason string) string {
	switch reason {
	case "imap_table_missing":
		return IMAPUnavailableNote
	case "webmail_login_account_not_queryable":
		return WebmailLoginUnavailableNote
	default:
		if reason == "" {
			reason = "unknown"
		}
		return "该数据档不可查(" + reason + "),未执行任何查询。注意:这不等于「没有记录」,不得据此答复客户。"
	}
}

func ResolveDataset(name string) *Dataset {
	key := strings.TrimSpace(name)
	if key == "" {
		key = "delivery"
	}
	ds, ok := datasets[key]
	if !ok {
		return nil
	}
	out := ds
	out.Key = key
	return &out
}

func marshalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	b := buf.Bytes()
	if len(b) > 0 && b[len(b)-1] == '\n' {
		b = b[:len(b)-1]
	}
	return b, nil
}

func ndjson(header, body any) (string, error) {
	h, err := marshalJSON(header)
	if err != nil {
		return "", err
	}
	b, err := marshalJSON(body)
	if err != nil {
		return "", err
	}
	return string(h) + "\n" + string(b) + "\n", nil
}

func timestampFilter(tr TimeRange) any {
	return map[string]any{
		"range": map[string]any{
			"timestamp": map[string]any{"gte": tr.FromMs, "lt": tr.ToMs},
		},
	}
}

func pageSizeFrom(opts QueryOptions) int {
	if opts.PageSize < 1 {
		return HardSize
	}
	if opts.PageSize > HardSize {
		return HardSize
	}
	return opts.PageSize
}

func offsetFrom(opts QueryOptions) int {
	if opts.Offset > 0 {
		return opts.Offset
	}
	return 0
}

func BuildDatasetBody(ds *Dataset, filters Filters, opts QueryOptions) (string, error) {
	filter := []any{timestampFilter(filters.TimeRange)}
	if ds.Kind == "delivery_agent" {
		if filters.Tid != "" {
			filter = append(filter, map[string]any{"term": map[string]any{"tid": filters.Tid}})
		}
		if filters.Sender != "" {
			filter = append(filter, map[string]any{"term": map[string]any{"mailfrom": filters.Sender}})
		}
		if filters.Recipient != "" {
			filter = append(filter, map[string]any{"term": map[string]any{"to": filters.Recipient}})
		}
		if filters.Subject != "" {
			filter = append(filter, map[string]any{"match_phrase": map[string]any{"subject": filters.Subject}})
		}
	}
	if ds.Kind == "delivery_pipeline" {
		if filters.Tid != "" {
			filter = append(filter, map[string]any{"term": map[string]any{"mailtid": filters.Tid}})
		}
		if filters.Sender != "" {
			filter = append(filter, map[string]any{"term": map[string]any{"from": filters.Sender}})
		}
		if filters.Recipient != "" {
			filter = append(filter, map[string]any{"term": map[string]any{"to": filters.Recipient}})
		}
	}
	if filters.Account != "" && ds.AccountField != "" {
		clause := "term"
		if ds.AccountMatch == "match_phrase" {
			clause = "match_phrase"
		}
		value := filters.Account
		if ds.AccountTransform == "localpart" {
			value = strings.Split(value, "@")[0]
		}
		filter = append(filter, map[string]any{clause: map[string]any{ds.AccountField: value}})
	}
	filter = append(filter, ds.Fixed...)
	if ds.Ops != nil {
		op := strings.TrimSpace(filters.Op)
		if op != "" && op != "all" {
			filter = append(filter, ds.Ops[op]...)
		} else {
			var should []any
			for _, clauses := range ds.Ops {
				should = append(should, clauses...)
			}
			if len(should) > 0 {
				filter = append(filter, map[string]any{"bool": map[string]any{"should": should, "minimum_should_match": 1}})
			}
		}
	}
	if filters.ClientIP != "" {
		filter = append(filter, map[string]any{"term": map[string]any{"ip": filters.ClientIP}})
	}
	header := map[string]any{"index": ds.Index, "ignore_unavailable": true}
	var body any
	if opts.CountOnly {
		body = map[string]any{"size": 0, "query": map[string]any{"bool": map[string]any{"filter": filter}}}
	} else {
		body = map[string]any{
			"size":  HardSize,
			"sort":  []any{map[string]any{"timestamp": map[string]any{"order": "desc"}}},
			"query": map[string]any{"bool": map[string]any{"filter": filter}},
		}
	}
	return ndjson(header, body)
}

func BuildMsearchBody(filters Filters, indexPattern string, opts QueryOptions) (string, error) {
	if indexPattern == "" {
		indexPattern = defaultIndex
	}
	filter := []any{timestampFilter(filters.TimeRange)}
	if filters.MsgID != "" {
		filter = append(filter, map[string]any{"term": map[string]any{"tid": filters.MsgID}})
	}
	if filters.Sender != "" {
		filter = append(filter, map[string]any{"term": map[string]any{"sender": filters.Sender}})
	}
	if filters.Recipient != "" {
		filter = append(filter, map[string]any{"match_phrase": map[string]any{"rcpt": filters.Recipient}})
	}
	if filters.Subject != "" {
		filter = append(filter, map[string]any{"match_phrase": map[string]any{"subject": filters.Subject}})
	}
	if filters.Domain != "" {
		d := filters.Domain
		filter = append(filter, map[string]any{
			"bool": map[string]any{
				"should": []any{
					map[string]any{"match_phrase": map[string]any{"sender": d}},
					map[string]any{"match_phrase": map[string]any{"rcpt": d}},
				},
				"minimum_should_match": 1,
			},
		})
	}
	header := map[string]any{"index": indexPattern, "ignore_unavailable": true}
	var body any
	if opts.CountOnly {
		body = map[string]any{"size": 0, "query": map[string]any{"bool": map[string]any{"filter": filter}}}
	} else {
		pageSize := pageSizeFrom(opts)
		offset := offsetFrom(opts)
		m := map[string]any{
			"size":  pageSize,
			"sort":  []any{map[string]any{"timestamp": map[string]any{"order": "desc"}}},
			"query": map[string]any{"bool": map[string]any{"filter": filter}},
		}
		if offset > 0 {
			m["from"] = offset
		}
		body = m
	}
	return ndjson(header, body)
}

type traceSource struct {
	Source   string
	Index    string
	TidField string
}

var traceSources = []traceSource{
	{Source: "mta", Index: "mtatrans_distributed", TidField: "tid"},
	{Source: "pipeline", Index: "dasvr_distributed", TidField: "mailtid"},
	{Source: "delivery_agent", Index: "datrans_distributed", TidField: "tid"},
}

func BuildTraceBody(tid string, tr TimeRange) (string, error) {
	var b strings.Builder
	for _, meta := range traceSources {
		header := map[string]any{"index": meta.Index, "ignore_unavailable": true}
		body := map[string]any{
			"size": HardSize,
			"sort": []any{map[string]any{"timestamp": map[string]any{"order": "asc"}}},
			"query": map[string]any{
				"bool": map[string]any{
					"filter": []any{
						timestampFilter(tr),
						map[string]any{"term": map[string]any{meta.TidField: tid}},
					},
				},
			},
		}
		chunk, err := ndjson(header, body)
		if err != nil {
			return "", err
		}
		b.WriteString(chunk)
	}
	return b.String(), nil
}
