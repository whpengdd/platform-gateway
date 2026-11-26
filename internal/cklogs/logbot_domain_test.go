package cklogs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// 本文件的账号/密码/地址全部是测试专用的虚构值，不对应任何真实凭据或域名。
// 语义依据 2026-11-24 真实 LogBot 实测：from_addr/to_addr 填裸域名按地址域名
// 部分匹配，同一表单内多地址字段为 AND，platform_mode=all 覆盖未映射域名。

// domainFake 是一个最小 LogBot agent 接口假服务：按每个任务的 JSON 请求实际匹配行，
// 用于验证域名查询的请求构造、双任务合并去重与截断标记取差。
type domainFake struct {
	srv *httptest.Server

	mu        sync.Mutex
	reqs      []map[string]any // 按提交顺序记录的请求体
	truncated []bool           // 每个任务按提交顺序的 snapshot_truncated；耗尽后 false
	rows      []map[string]any
}

func newDomainFake(t *testing.T) *domainFake {
	t.Helper()
	f := &domainFake{}
	nextJob := 0
	jobs := map[string]map[string]any{}
	jobIndex := map[string]int{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "logbot_session", Value: "unit-session-cookie", Path: "/"})
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/api/agent/log-query/jobs", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		nextJob++
		jobID := itoa(nextJob)
		jobs[jobID] = req
		jobIndex[jobID] = len(f.reqs)
		f.reqs = append(f.reqs, req)
		f.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"job_id":"` + jobID + `","status":"queued"}`))
	})
	mux.HandleFunc("/api/agent/log-query/jobs/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/agent/log-query/jobs/")
		f.mu.Lock()
		if strings.HasSuffix(rest, "/results") {
			jobID := strings.TrimSuffix(rest, "/results")
			req := jobs[jobID]
			var matched []map[string]any
			for _, row := range f.rows {
				if domainRowMatches(req, row) {
					matched = append(matched, row)
				}
			}
			if matched == nil {
				matched = []map[string]any{}
			}
			trunc := false
			if idx := jobIndex[jobID]; idx < len(f.truncated) {
				trunc = f.truncated[idx]
			}
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "success", "rows": matched, "total_count": len(matched),
				"limits_applied": map[string]any{}, "snapshot_truncated": trunc,
				"retained_window": map[string]any{},
			})
			return
		}
		jobID := rest
		var n int
		for _, row := range f.rows {
			if domainRowMatches(jobs[jobID], row) {
				n++
			}
		}
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"job_id": jobID, "status": "success", "result_count": n,
		})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *domainFake) client() *LogBotClient {
	hc := f.srv.Client()
	hc.CheckRedirect = func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }
	return &LogBotClient{
		BaseURL:      f.srv.URL,
		Username:     "unit-svc-account",
		Password:     "unit-svc-password",
		PollInterval: time.Millisecond,
		Timeout:      5 * time.Second,
		HTTPClient:   hc,
	}
}

func (f *domainFake) backend() LogBotBackend { return LogBotBackend{Client: f.client()} }

func (f *domainFake) submittedReqs() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]map[string]any, len(f.reqs))
	copy(out, f.reqs)
	return out
}

// reqFilter 取请求体 filters 中的单个值。
func reqFilter(req map[string]any, key string) string {
	m, _ := req["filters"].(map[string]any)
	return asString(m[key])
}

// domainRowMatches 模拟实测语义：from_addr 匹配行 sender/mailfrom 的域名部分，
// to_addr 匹配行 rcpt/to 任一地址的域名部分，两条件并存时为 AND。
// platform_mode=all 只放宽未映射域名，不改变匹配字段。
func domainRowMatches(req map[string]any, row map[string]any) bool {
	if d := reqFilter(req, "from_addr"); d != "" && !rowAddrSideMatches(row, d, "sender", "mailfrom", "from") {
		return false
	}
	if d := reqFilter(req, "to_addr"); d != "" && !rowAddrSideMatches(row, d, "rcpt", "to") {
		return false
	}
	return true
}

// rowAddrSideMatches：表单值是完整地址时要求地址相等；是域名（无 @ 前缀部分）
// 时按地址域名部分匹配——对应实测的两种语义。
func rowAddrSideMatches(row map[string]any, want string, keys ...string) bool {
	for _, a := range rowAddrs(row, keys...) {
		addr := strings.ToLower(ExtractAddr(a))
		if addr == want || addressDomain(addr) == want {
			return true
		}
	}
	return false
}

func rowAddrs(row map[string]any, keys ...string) []string {
	var out []string
	for _, k := range keys {
		v := row[k]
		if arr, ok := v.([]any); ok {
			for _, item := range arr {
				if s, ok := item.(string); ok {
					out = append(out, s)
				}
			}
			continue
		}
		if s, ok := v.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

func mtaRow(ts, sender string, rcpts ...string) map[string]any {
	rcpt := make([]any, len(rcpts))
	for i, r := range rcpts {
		rcpt[i] = r
	}
	return map[string]any{
		"timestamp":  ts,
		"sender":     sender,
		"rcpt":       rcpt,
		"cmd":        "DATA",
		"result":     "Deliver",
		"log_source": "MTA",
	}
}

func domainFilters(direction string) Filters {
	return Filters{
		Domain:    "dom-a.example",
		Direction: direction,
		TimeRange: TimeRange{FromMs: 1700000000000, ToMs: 1700003600000},
	}
}

func TestLogBotDomainOutboundForm(t *testing.T) {
	f := newDomainFake(t)
	f.rows = []map[string]any{mtaRow("2023-11-14 22:20:00+08:00", "a@dom-a.example", "x@other.example")}
	res := f.backend().Query(context.Background(), domainFilters("outbound"), QueryOptions{})
	if !res.OK {
		t.Fatalf("查询失败：%+v", res)
	}
	reqs := f.submittedReqs()
	if len(reqs) != 1 {
		t.Fatalf("应提交 1 个任务，实际 %d", len(reqs))
	}
	req := reqs[0]
	if got := reqFilter(req, "from_addr"); got != "dom-a.example" {
		t.Fatalf("from_addr=%q，应为裸域名", got)
	}
	if got := reqFilter(req, "to_addr"); got != "" {
		t.Fatalf("to_addr=%q，outbound 不应设置", got)
	}
	if got := asString(req["platform_mode"]); got != "all" {
		t.Fatalf("platform_mode=%q，域名查询必须为 all", got)
	}
	if res.Total != 1 || len(res.Entries) != 1 {
		t.Fatalf("Total=%d entries=%d，应各为 1", res.Total, len(res.Entries))
	}
}

func TestLogBotDomainInboundForm(t *testing.T) {
	f := newDomainFake(t)
	f.rows = []map[string]any{mtaRow("2023-11-14 22:20:00+08:00", "ext@other.example", "a@dom-a.example")}
	res := f.backend().Query(context.Background(), domainFilters("inbound"), QueryOptions{})
	if !res.OK {
		t.Fatalf("查询失败：%+v", res)
	}
	reqs := f.submittedReqs()
	if len(reqs) != 1 {
		t.Fatalf("应提交 1 个任务，实际 %d", len(reqs))
	}
	if got := reqFilter(reqs[0], "to_addr"); got != "dom-a.example" {
		t.Fatalf("to_addr=%q，应为裸域名", got)
	}
	if got := reqFilter(reqs[0], "from_addr"); got != "" {
		t.Fatalf("from_addr=%q，inbound 不应设置", got)
	}
	if res.Total != 1 {
		t.Fatalf("Total=%d，应为 1", res.Total)
	}
}

func TestLogBotDomainBothMergesAndDeduplicates(t *testing.T) {
	f := newDomainFake(t)
	shared := mtaRow("2023-11-14 22:20:00+08:00", "a@dom-a.example", "b@dom-a.example")
	outOnly := mtaRow("2023-11-14 22:21:00+08:00", "c@dom-a.example", "x@other.example")
	inOnly := mtaRow("2023-11-14 22:22:00+08:00", "y@other.example", "d@dom-a.example")
	f.rows = []map[string]any{shared, outOnly, inOnly}
	res := f.backend().Query(context.Background(), domainFilters("both"), QueryOptions{})
	if !res.OK {
		t.Fatalf("查询失败：%+v", res)
	}
	reqs := f.submittedReqs()
	if len(reqs) != 2 {
		t.Fatalf("both 应提交 2 个任务，实际 %d", len(reqs))
	}
	var sawFrom, sawTo bool
	for _, req := range reqs {
		if reqFilter(req, "from_addr") == "dom-a.example" && reqFilter(req, "to_addr") == "" {
			sawFrom = true
		}
		if reqFilter(req, "to_addr") == "dom-a.example" && reqFilter(req, "from_addr") == "" {
			sawTo = true
		}
		if asString(req["platform_mode"]) != "all" {
			t.Fatalf("platform_mode=%q，域名查询必须为 all", asString(req["platform_mode"]))
		}
	}
	if !sawFrom || !sawTo {
		t.Fatalf("both 应覆盖 from/to 两个方向，reqs=%v", reqs)
	}
	if res.Total != 3 || len(res.Entries) != 3 {
		t.Fatalf("合并去重后应为 3 条（shared 只计一次），Total=%d entries=%d", res.Total, len(res.Entries))
	}
}

func TestLogBotDomainBothWorseSourceStatus(t *testing.T) {
	f := newDomainFake(t)
	f.rows = []map[string]any{mtaRow("2023-11-14 22:20:00+08:00", "a@dom-a.example", "x@other.example")}
	// 第二个任务（to 侧）快照截断。
	f.truncated = []bool{false, true}
	res := f.backend().Query(context.Background(), domainFilters("both"), QueryOptions{})
	if !res.OK {
		t.Fatalf("查询失败：%+v", res)
	}
	if !res.Truncated {
		t.Fatalf("一侧 limit 时合并结果应标记 Truncated")
	}
	if res.TotalExact {
		t.Fatalf("一侧不完整时 TotalExact 应为 false")
	}
	if len(res.Limitations) == 0 {
		t.Fatalf("应透传 limit 的 limitations")
	}
}

func TestLogBotDomainSenderInDomainSingleJob(t *testing.T) {
	f := newDomainFake(t)
	filters := domainFilters("both")
	filters.Sender = "user@dom-a.example"
	res := f.backend().Query(context.Background(), filters, QueryOptions{})
	if !res.OK {
		t.Fatalf("查询失败：%+v", res)
	}
	reqs := f.submittedReqs()
	if len(reqs) != 1 {
		t.Fatalf("发件人已在域内时 to 侧被包含，应只发 1 个任务，实际 %d", len(reqs))
	}
	if got := reqFilter(reqs[0], "from_addr"); got != "user@dom-a.example" {
		t.Fatalf("from_addr=%q，应保留显式发件人", got)
	}
}

func TestLogBotDomainSenderOutDomainBoth(t *testing.T) {
	f := newDomainFake(t)
	filters := domainFilters("both")
	filters.Sender = "user@other.example"
	f.rows = []map[string]any{mtaRow("2023-11-14 22:20:00+08:00", "user@other.example", "a@dom-a.example")}
	res := f.backend().Query(context.Background(), filters, QueryOptions{})
	if !res.OK {
		t.Fatalf("查询失败：%+v", res)
	}
	reqs := f.submittedReqs()
	if len(reqs) != 1 {
		t.Fatalf("发件人域外时 from 侧恒不成立，应只发 to 侧 1 个任务，实际 %d", len(reqs))
	}
	if got := reqFilter(reqs[0], "from_addr"); got != "user@other.example" {
		t.Fatalf("to 侧任务应 AND 显式发件人，from_addr=%q", got)
	}
	if got := reqFilter(reqs[0], "to_addr"); got != "dom-a.example" {
		t.Fatalf("to_addr=%q", got)
	}
	if res.Total != 1 {
		t.Fatalf("Total=%d，应为 1", res.Total)
	}
}

func TestLogBotDomainOutboundSenderMismatchEmpty(t *testing.T) {
	f := newDomainFake(t)
	filters := domainFilters("outbound")
	filters.Sender = "user@other.example"
	res := f.backend().Query(context.Background(), filters, QueryOptions{})
	if !res.OK {
		t.Fatalf("查询失败：%+v", res)
	}
	if res.Total != 0 || !res.TotalExact {
		t.Fatalf("显式发件人不在域内时应返回精确 0 命中，Total=%d exact=%v", res.Total, res.TotalExact)
	}
	if n := len(f.submittedReqs()); n != 0 {
		t.Fatalf("恒不成立的子句不应发任务，实际提交 %d", n)
	}
}

func TestLogBotDomainInboundRecipientMismatchEmpty(t *testing.T) {
	f := newDomainFake(t)
	filters := domainFilters("inbound")
	filters.Recipient = "user@other.example"
	res := f.backend().Query(context.Background(), filters, QueryOptions{})
	if !res.OK {
		t.Fatalf("查询失败：%+v", res)
	}
	if res.Total != 0 || !res.TotalExact {
		t.Fatalf("显式收件人不在域内时应返回精确 0 命中，Total=%d exact=%v", res.Total, res.TotalExact)
	}
	if n := len(f.submittedReqs()); n != 0 {
		t.Fatalf("恒不成立的子句不应发任务，实际提交 %d", n)
	}
}

func TestLogBotDomainRecipientNarrowsToSide(t *testing.T) {
	f := newDomainFake(t)
	filters := domainFilters("both")
	filters.Recipient = "user@dom-a.example"
	// 收件人已在域内：from 侧任务被 to 侧包含，应只发 1 个任务 to_addr=显式收件人。
	res := f.backend().Query(context.Background(), filters, QueryOptions{})
	if !res.OK {
		t.Fatalf("查询失败：%+v", res)
	}
	reqs := f.submittedReqs()
	if len(reqs) != 1 {
		t.Fatalf("收件人已在域内时应只发 1 个任务，实际 %d", len(reqs))
	}
	if got := reqFilter(reqs[0], "to_addr"); got != "user@dom-a.example" {
		t.Fatalf("to_addr=%q，应保留显式收件人", got)
	}
}
