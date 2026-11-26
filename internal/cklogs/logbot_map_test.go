package cklogs

import (
	"strings"
	"testing"
)

// 本文件数据全部为构造的脱敏测试数据，不来自任何真实样本。

func TestLogbotTimestampParsing(t *testing.T) {
	// +08:00 字符串必须转成 UTC epoch 毫秒。
	got := logbotTimestamp("2026-08-01 10:00:00+08:00")
	want := float64(1785549600000) // 2026-08-01T02:00:00Z
	if got != want {
		t.Fatalf("got %v want %v", got, want)
	}
	// 不带时区按上海时间解释。
	if logbotTimestamp("2026-08-01 10:00:00") != want {
		t.Fatalf("naive timestamp not treated as Asia/Shanghai")
	}
	if logbotTimestamp("") != 0 || logbotTimestamp("not a time") != 0 || logbotTimestamp(nil) != 0 {
		t.Fatal("invalid timestamps must map to 0")
	}
	if iso := toIso(logbotTimestamp("2026-08-01 10:00:00+08:00")); iso != "2026-08-01T02:00:00.000Z" {
		t.Fatalf("iso=%q", iso)
	}
}

func TestLogBotDARowMapping(t *testing.T) {
	row := map[string]any{
		"timestamp":    "2026-08-01 10:00:01+08:00",
		"mailfrom":     "alice@unit-a.test",
		"to":           "bob@unit-b.test",
		"subject":      "Unit Subject",
		"cmd":          "local",
		"result":       "0",
		"commresult":   "Non-Spam",
		"folderid":     "1",
		"desc":         "",
		"cactag":       "7",
		"datarulename": "unit-data-rule",
		"cntrulename":  "unit-content-rule",
		"clientip":     "192.0.2.10",
		"log_source":   "DA",
		"otherlog":     map[string]any{"hdrmsgid": "<unit-da-1@unit-a.test>", "deliverreason": "unit-reason"},
	}
	e := mapLogBotRow(row, "DA")
	if e.Source != "da" || e.Sender != "alice@unit-a.test" {
		t.Fatalf("%+v", e)
	}
	if len(e.Recipients) != 1 || e.Recipients[0] != "bob@unit-b.test" {
		t.Fatalf("%+v", e)
	}
	if e.TimestampISO != "2026-08-01T02:00:01.000Z" {
		t.Fatalf("ts=%q", e.TimestampISO)
	}
	if e.FolderID != "1" || e.FolderName != "收件箱" {
		t.Fatalf("folder: %+v", e)
	}
	if e.MessageID != "<unit-da-1@unit-a.test>" {
		t.Fatalf("messageId from otherlog.hdrmsgid: %+v", e)
	}
	if e.Cmd != "local" || e.Channel != "local" || e.Result != "0" || e.Commresult != "Non-Spam" {
		t.Fatalf("%+v", e)
	}
	// 与 Kibana DA 映射（mapDeliveryAgentHit）保持一致：cactag/clientip/signals 在
	// DA 条目上本就不输出，LogBot 侧也不得额外填充。
	if e.Cactag != "" || e.CacVerdict != "" || e.ClientIP != "" || e.Signals != nil {
		t.Fatalf("DA mapping must match kibana DA shape: %+v", e)
	}
	// LogBot 未提供的字段必须缺省，不得填猜测值。
	if e.Tid != "" || e.Mid != "" || e.Respond != "" || e.Score != "" || e.Spamfng != "" || e.Blackip != "" || e.Debuginfo != "" {
		t.Fatalf("invented fields: %+v", e)
	}
}

func TestLogBotMTARowMapping(t *testing.T) {
	row := map[string]any{
		"timestamp":  "2026-08-01 10:00:05+08:00",
		"ip":         "198.51.100.7",
		"cmd":        "DATA",
		"sender":     "alice@unit-a.test",
		"rcpt":       []any{"bob@unit-b.test", "carol@unit-b.test"},
		"subject":    "Unit MTA",
		"result":     "s_ok",
		"cactag":     "3",
		"log_source": "MTA",
		"otherlog":   map[string]any{"delivered": "1", "eval": "unit-eval-note"},
	}
	e := mapLogBotRow(row, "MTA")
	if e.Source != "mta" || e.ClientIP != "198.51.100.7" {
		t.Fatalf("%+v", e)
	}
	if len(e.Recipients) != 2 || e.Recipients[1] != "carol@unit-b.test" {
		t.Fatalf("%+v", e)
	}
	if e.TimestampISO != "2026-08-01T02:00:05.000Z" || e.Cmd != "DATA" || e.Result != "s_ok" {
		t.Fatalf("%+v", e)
	}
	// MTA 映射与 Kibana 一致：cacVerdict 由 cactag 推导，signals 由 otherlog 解析。
	if e.Cactag != "3" || e.CacVerdict != "普通垃圾邮件" {
		t.Fatalf("cac: %+v", e)
	}
	if e.Signals == nil || e.Signals["eval"] != "unit-eval-note" {
		t.Fatalf("signals: %+v", e.Signals)
	}
	if e.Tid != "" || e.Mid != "" || e.Respond != "" {
		t.Fatalf("invented fields: %+v", e)
	}
}

func TestLogBotProxyRowMapping(t *testing.T) {
	row := map[string]any{
		"timestamp":  "2026-08-01 10:00:09+08:00",
		"ip":         "203.0.113.9",
		"from":       "alice@unit-a.test",
		"to":         "bob@unit-b.test",
		"subject":    "Unit Proxy",
		"result":     "0",
		"desc":       "250 OK",
		"log_source": "PROXYSVR",
	}
	e := mapLogBotRow(row, "PROXYSVR")
	if e.Source != "proxy" || e.Sender != "alice@unit-a.test" || e.ClientIP != "203.0.113.9" {
		t.Fatalf("%+v", e)
	}
	if e.Errinfo != "250 OK" || e.Channel != "PROXYSVR" {
		t.Fatalf("%+v", e)
	}
}

// 同一封邮件经两种 provider 的共有字段必须一致；差异只允许出现在 LogBot 未提供的字段。
func TestLogBotKibanaFieldParity(t *testing.T) {
	kibanaSource := map[string]any{
		"timestamp":  float64(1785549601000),
		"mailfrom":   "alice@unit-a.test",
		"to":         "bob@unit-b.test",
		"subject":    "Parity Subject",
		"cmd":        "local",
		"result":     "0",
		"commresult": "Non-Spam",
		"folderid":   "1",
		"tid":        "unit-tid-parity",
		"mid":        "unit-mid-parity",
		"otherlog":   `{"hdrmsgid":"<unit-parity@unit-a.test>"}`,
	}
	logbotRow := map[string]any{
		"timestamp":  "2026-08-01 10:00:01+08:00",
		"mailfrom":   "alice@unit-a.test",
		"to":         "bob@unit-b.test",
		"subject":    "Parity Subject",
		"cmd":        "local",
		"result":     "0",
		"commresult": "Non-Spam",
		"folderid":   "1",
		"log_source": "DA",
		"otherlog":   map[string]any{"hdrmsgid": "<unit-parity@unit-a.test>"},
	}
	ke := mapDeliveryAgentHit(kibanaSource)
	le := mapLogBotRow(logbotRow, "DA")
	if ke.TimestampISO != le.TimestampISO || ke.Sender != le.Sender || ke.Subject != le.Subject ||
		ke.Cmd != le.Cmd || ke.Result != le.Result || ke.Commresult != le.Commresult ||
		ke.FolderID != le.FolderID || ke.FolderName != le.FolderName || ke.MessageID != le.MessageID {
		t.Fatalf("parity broken:\nkibana=%+v\nlogbot=%+v", ke, le)
	}
	if len(ke.Recipients) != len(le.Recipients) || ke.Recipients[0] != le.Recipients[0] {
		t.Fatalf("recipients parity: %+v vs %+v", ke.Recipients, le.Recipients)
	}
	// LogBot 缺的 tid/mid 只在 kibana 侧出现。
	if le.Tid != "" || le.Mid != "" || ke.Tid == "" || ke.Mid == "" {
		t.Fatalf("tid/mid expectation: %+v vs %+v", ke, le)
	}
}

// agent 接口实测返回完全重复的行：整行去重；无分组键的 DA 行逐条保留。
func TestLogBotDADedupeWholeRows(t *testing.T) {
	rows := []map[string]any{
		daRow("2026-08-01 10:00:00+08:00", "a@unit-a.test", "b@unit-b.test", "0"),
		daRow("2026-08-01 10:00:00+08:00", "a@unit-a.test", "b@unit-b.test", "0"),
		daRow("2026-08-01 10:00:01+08:00", "a@unit-a.test", "b@unit-b.test", "0"),
		daRow("2026-08-01 10:00:02+08:00", "a@unit-a.test", "b@unit-b.test", "0"),
	}
	res := &logbotJobResult{Rows: rows, Total: len(rows), TotalExact: true}
	out := logbotResultToQuery(res, "DA", false, nil)
	if !out.OK || !out.TotalExact {
		t.Fatalf("%+v", out)
	}
	if out.Total != 3 || len(out.Entries) != 3 {
		t.Fatalf("完全重复行须去重、不同行须保留: total=%d entries=%d", out.Total, len(out.Entries))
	}
	if !strings.Contains(strings.Join(out.Limitations, "\n"), "无法归并") {
		t.Fatalf("unkeyed limitation missing: %v", out.Limitations)
	}
}

func TestLogBotResultIgnoresForeignSourceRows(t *testing.T) {
	rows := []map[string]any{
		daRow("2026-08-01 10:00:01+08:00", "a@unit-a.test", "b@unit-b.test", "0"),
		{"timestamp": "2026-08-01 10:00:02+08:00", "sender": "a@unit-a.test", "cmd": "DATA", "log_source": "MTA"},
	}
	res := &logbotJobResult{Rows: rows, Total: len(rows), TotalExact: true}
	out := logbotResultToQuery(res, "DA", false, nil)
	if out.Total != 1 || len(out.Entries) != 1 || out.Entries[0].Source != "da" {
		t.Fatalf("%+v", out)
	}
}
