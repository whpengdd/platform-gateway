package cklogs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"platform-gateway/internal/auditlog"
)

// 本文件的账号/密码/令牌全部是测试专用的虚构值，不对应任何真实凭据。

type logbotFake struct {
	srv *httptest.Server

	mu             sync.Mutex
	logins         int
	submits        int
	polls          int
	resultCalls    int
	lastReq        map[string]any
	reqs           []map[string]any
	failLogin      bool
	poll401Once    bool
	redirectOnce   bool
	submitStatus   int
	submitDetail   string
	submit429Times int // 前 N 次 submit 返回 429（耗尽后正常）
	resultsStatus  int
	jobStatuses    []string // 每次轮询依次返回；耗尽后返回 success
	neverDone      bool     // 轮询恒为 running（预算耗尽测试）
	jobError       string
	rows           []map[string]any
	jobRows        map[string][]map[string]any // 指定 job 专用行（多窗口差异场景）；未指定时回退 rows
	truncated      bool                        // 结果快照标记 snapshot_truncated
	truncatedJobs  map[string]bool             // 仅指定 job 的 snapshot_truncated（多窗口混合场景）
	totalCount     int                         // 覆盖 total_count；-1 表示按匹配行数
	totalCountJobs map[string]int              // 指定 job 的 total_count（优先级高于 totalCount）
	jobs           map[string]map[string]any
	nextJob        int
}

func newLogbotFake(t *testing.T) *logbotFake {
	t.Helper()
	f := &logbotFake{jobs: map[string]map[string]any{}, totalCount: -1}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/login", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		f.logins++
		fail := f.failLogin
		f.mu.Unlock()
		if fail || r.PostForm.Get("username") != "unit-svc-account" || r.PostForm.Get("password") != "unit-svc-password" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"detail":"invalid credentials"}`))
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "logbot_session", Value: "unit-session-cookie", Path: "/"})
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/api/agent/log-query/jobs", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.submits++
		authed := f.hasAuthLocked(r)
		status := f.submitStatus
		if status == 0 && f.submit429Times > 0 {
			f.submit429Times--
			status = http.StatusTooManyRequests
		}
		detail := f.submitDetail
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.lastReq = req
		var jobID string
		if authed && status == 0 {
			f.nextJob++
			jobID = itoa(f.nextJob)
			f.jobs[jobID] = req
			f.reqs = append(f.reqs, req)
		}
		f.mu.Unlock()
		if !authed {
			w.Header().Set("Location", "/login.html")
			w.WriteHeader(http.StatusFound)
			return
		}
		if status != 0 {
			w.WriteHeader(status)
			raw, _ := json.Marshal(detail)
			_, _ = w.Write([]byte(`{"detail":` + string(raw) + `}`))
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"job_id":"` + jobID + `","status":"queued"}`))
	})
	mux.HandleFunc("/api/agent/log-query/jobs/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/api/agent/log-query/jobs/")
		f.mu.Lock()
		authed := f.hasAuthLocked(r)
		if !authed {
			f.mu.Unlock()
			w.Header().Set("Location", "/login.html")
			w.WriteHeader(http.StatusFound)
			return
		}
		if strings.HasSuffix(rest, "/results") {
			jobID := strings.TrimSuffix(rest, "/results")
			f.resultsLocked(w, r, jobID)
			f.mu.Unlock()
			return
		}
		f.polls++
		if f.poll401Once {
			f.poll401Once = false
			f.mu.Unlock()
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"detail":"session expired"}`))
			return
		}
		if f.redirectOnce {
			f.redirectOnce = false
			f.mu.Unlock()
			w.Header().Set("Location", "/login.html")
			w.WriteHeader(http.StatusFound)
			return
		}
		var status string
		switch {
		case f.neverDone:
			status = "running"
		case len(f.jobStatuses) > 0:
			status = f.jobStatuses[0]
			f.jobStatuses = f.jobStatuses[1:]
		default:
			status = "success"
		}
		jobErr := f.jobError
		matched := f.matchedRowsLocked(rest)
		f.mu.Unlock()
		job := map[string]any{"job_id": rest, "status": status}
		if status == "success" {
			job["result_count"] = len(matched)
		} else if status != "queued" && status != "running" {
			job["error"] = jobErr
		}
		_ = json.NewEncoder(w).Encode(job)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// matchedRowsLocked 返回该任务 log_sources 匹配的 fixture 行（调用时须持锁）。
func (f *logbotFake) matchedRowsLocked(jobID string) []map[string]any {
	if rows, ok := f.jobRows[jobID]; ok {
		if rows == nil {
			return []map[string]any{}
		}
		return rows
	}
	req := f.jobs[jobID]
	var srcs []any
	if l, ok := req["log_sources"].([]any); ok {
		srcs = l
	}
	var matched []map[string]any
	for _, row := range f.rows {
		for _, s := range srcs {
			if strings.EqualFold(asString(row["log_source"]), asString(s)) {
				matched = append(matched, row)
			}
		}
	}
	if matched == nil {
		matched = []map[string]any{}
	}
	return matched
}

// resultsLocked 处理 GET /jobs/{id}/results：支持 page_size/page_token 翻页（游标为行偏移）。
func (f *logbotFake) resultsLocked(w http.ResponseWriter, r *http.Request, jobID string) {
	f.resultCalls++
	if st := f.resultsStatus; st != 0 {
		w.WriteHeader(st)
		_, _ = w.Write([]byte(`{"detail":"results error"}`))
		return
	}
	matched := f.matchedRowsLocked(jobID)
	pageSize := 500
	if ps := r.URL.Query().Get("page_size"); ps != "" {
		if n, err := strconv.Atoi(ps); err == nil && n > 0 {
			pageSize = n
		}
	}
	offset := 0
	if pt := r.URL.Query().Get("page_token"); pt != "" {
		if n, err := strconv.Atoi(strings.TrimPrefix(pt, "off-")); err == nil {
			offset = n
		}
	}
	end := offset + pageSize
	if end > len(matched) {
		end = len(matched)
	}
	page := matched[offset:end]
	next := ""
	if end < len(matched) {
		next = "off-" + itoa(end)
	}
	total := f.totalCount
	if jobTotal, ok := f.totalCountJobs[jobID]; ok {
		total = jobTotal
	}
	if total < 0 {
		total = len(matched)
	}
	truncated := f.truncated || f.truncatedJobs[jobID]
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "success", "rows": page, "total_count": total,
		"limits_applied": map[string]any{}, "next_page_token": next,
		"snapshot_truncated": truncated, "retained_window": map[string]any{},
	})
}

// hasAuthLocked 接受会话 cookie 或正确的 Bearer 服务令牌（备选分支）。
func (f *logbotFake) hasAuthLocked(r *http.Request) bool {
	if r.Header.Get("Authorization") == "Bearer unit-service-token" {
		return true
	}
	_, err := r.Cookie("logbot_session")
	return err == nil
}

func (f *logbotFake) client() *LogBotClient {
	hc := f.srv.Client()
	hc.CheckRedirect = func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }
	return &LogBotClient{
		BaseURL:             f.srv.URL,
		Username:            "unit-svc-account",
		Password:            "unit-svc-password",
		PollInterval:        time.Millisecond,
		Timeout:             5 * time.Second,
		RateLimitBackoff:    time.Millisecond,
		RateLimitMaxRetries: 2,
		HTTPClient:          hc,
	}
}

func (f *logbotFake) service() *Service { return NewLogBotService(f.client()) }

// daRow 构造 agent 接口形态的 DA 行（固定 11 列；无 cmd/folderid/otherlog/分组键）。
func daRow(ts, from, to, result string) map[string]any {
	return map[string]any{
		"timestamp":  ts,
		"mailfrom":   from,
		"hdrfrom":    from,
		"to":         to,
		"subject":    "unit test subject",
		"result":     result,
		"commresult": "Non-Spam",
		"log_source": "DA",
		"ip":         "10.0.0.1",
	}
}

func TestLogBotDeliveryOutboundFlow(t *testing.T) {
	f := newLogbotFake(t)
	f.rows = []map[string]any{
		// agent 接口实测会返回完全重复的行：必须整行去重。
		daRow("2026-08-01 10:00:01+08:00", "sender@example.com", "peer@example.com", "0"),
		daRow("2026-08-01 10:00:01+08:00", "sender@example.com", "peer@example.com", "0"),
		// 同封邮件的另一条投递行动行：无分组键，逐行保留。
		daRow("2026-08-01 10:00:02+08:00", "sender@example.com", "peer@example.com", "0"),
	}
	out := f.service().QuerySelfServiceDelivery(context.Background(), testInput())
	if out.Status != "ok" {
		t.Fatalf("%+v", out)
	}
	// 服务层按 message-id/tid+收件人聚合同一封邮件：两行代表同一投递 → 1 条。
	if out.Total == nil || *out.Total != 1 {
		t.Fatalf("total=%v entries=%d", out.Total, len(out.Entries))
	}
	e := out.Entries[0]
	if e.TimestampISO != "2026-08-01T02:00:02.000Z" {
		t.Fatalf("timestamp not converted to UTC: %q", e.TimestampISO)
	}
	if e.Sender != "sender@example.com" || len(e.Recipients) != 1 || e.Recipients[0] != "peer@example.com" {
		t.Fatalf("%+v", e)
	}
	// agent 行无 cmd 列：投递状态不可判定，必须保持 unknown 而非臆断 delivered。
	if e.DeliveryStatus != "unknown" || e.MessageID != "" {
		t.Fatalf("%+v", e)
	}
	if e.Tid != "" || e.Mid != "" {
		t.Fatalf("logbot rows must not invent tid/mid: %+v", e)
	}
	if out.Truncated == nil || *out.Truncated {
		t.Fatalf("truncated=%v", out.Truncated)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "unit-svc-password") || strings.Contains(string(raw), "unit-session-cookie") {
		t.Fatalf("credential leaked in result: %s", raw)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.logins != 1 {
		t.Fatalf("logins=%d (session must be cached)", f.logins)
	}
	req := f.lastReq
	filters, _ := req["filters"].(map[string]any)
	if req["query_type"] != "transmission" || filters["from_addr"] != "sender@example.com" {
		t.Fatalf("req=%v", req)
	}
	if req["start_time"] != "2026-08-01 08:00:00" || req["end_time"] != "2026-08-02 07:59:59" {
		t.Fatalf("time window: %v %v", req["start_time"], req["end_time"])
	}
}

func TestLogBotDeliveryLimitMarksTruncated(t *testing.T) {
	f := newLogbotFake(t)
	f.rows = []map[string]any{
		daRow("2026-08-01 10:00:01+08:00", "sender@example.com", "peer@example.com", "0"),
	}
	f.truncated = true
	out := f.service().QuerySelfServiceDelivery(context.Background(), testInput())
	if out.Status != "ok" {
		t.Fatalf("%+v", out)
	}
	if out.Truncated == nil || !*out.Truncated {
		t.Fatalf("truncated=%v", out.Truncated)
	}
	joined := strings.Join(out.Limitations, "\n")
	if !strings.Contains(joined, "快照上限") {
		t.Fatalf("lim=%v", out.Limitations)
	}
}

func TestLogBotExpiredSessionReloginOnce(t *testing.T) {
	f := newLogbotFake(t)
	f.poll401Once = true
	out := f.service().QuerySelfServiceDelivery(context.Background(), testInput())
	if out.Status != "ok" {
		t.Fatalf("%+v", out)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.logins < 2 {
		t.Fatalf("expected relogin, logins=%d", f.logins)
	}
}

func TestLogBotRedirectTriggersRelogin(t *testing.T) {
	f := newLogbotFake(t)
	f.redirectOnce = true
	out := f.service().QuerySelfServiceDelivery(context.Background(), testInput())
	if out.Status != "ok" {
		t.Fatalf("%+v", out)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.logins < 2 {
		t.Fatalf("expected relogin after redirect, logins=%d", f.logins)
	}
}

func TestLogBotBadCredentialsAuthError(t *testing.T) {
	f := newLogbotFake(t)
	f.failLogin = true
	out := f.service().QuerySelfServiceDelivery(context.Background(), testInput())
	if out.Status != "error" || out.Code != "CK_LOGS_AUTH_ERROR" {
		t.Fatalf("%+v", out)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "unit-svc-password") {
		t.Fatalf("credential leaked: %s", raw)
	}
}

func TestLogBotSubmitForbiddenIsNotAvailable(t *testing.T) {
	f := newLogbotFake(t)
	f.submitStatus = http.StatusForbidden
	f.submitDetail = "该日志类型暂停提供查询和导出"
	out := f.service().QuerySelfServiceDelivery(context.Background(), testInput())
	if out.Status != "not_available" {
		t.Fatalf("%+v", out)
	}
	joined := strings.Join(out.Limitations, "\n")
	if !strings.Contains(joined, "暂停提供查询") {
		t.Fatalf("lim=%v", out.Limitations)
	}
}

func TestLogBotRateLimited(t *testing.T) {
	f := newLogbotFake(t)
	f.submitStatus = http.StatusTooManyRequests
	out := f.service().QuerySelfServiceDelivery(context.Background(), testInput())
	if out.Status != "error" || out.Code != "CK_LOGS_RATE_LIMITED" {
		t.Fatalf("%+v", out)
	}
}

func TestLogBotJobFailed(t *testing.T) {
	f := newLogbotFake(t)
	f.jobStatuses = []string{"failed"}
	f.jobError = "clickhouse unavailable"
	out := f.service().QuerySelfServiceDelivery(context.Background(), testInput())
	if out.Status != "error" || out.Code != "CK_LOGS_UPSTREAM_ERROR" {
		t.Fatalf("%+v", out)
	}
	if !strings.Contains(strings.Join(out.Limitations, "\n"), "clickhouse unavailable") {
		t.Fatalf("lim=%v", out.Limitations)
	}
}

func TestLogBotPollBudgetExhausted(t *testing.T) {
	f := newLogbotFake(t)
	f.neverDone = true
	c := f.client()
	c.Timeout = 25 * time.Millisecond
	out := NewLogBotService(c).QuerySelfServiceDelivery(context.Background(), testInput())
	if out.Status != "error" || out.Code != "CK_LOGS_TIMEOUT" {
		t.Fatalf("%+v", out)
	}
	if !strings.Contains(strings.Join(out.Limitations, "\n"), "查询尚未完成") {
		t.Fatalf("timeout must not claim zero records: %v", out.Limitations)
	}
	if out.Total != nil && *out.Total == 0 {
		t.Fatalf("timeout must not report total=0: %+v", out)
	}
}

// 快照截断时不得给出"精确命中 0 条"式的空结果结论，须带快照上限提示。
func TestLogBotTruncatedIsNotEmptyClaim(t *testing.T) {
	f := newLogbotFake(t)
	f.truncated = true
	out := f.service().QueryAnalysisDelivery(context.Background(), DeliveryQuery{
		Direction: "outbound",
		Sender:    "sender@example.com",
		TimeRange: testRange(),
	})
	if out.Status != "ok" {
		t.Fatalf("%+v", out)
	}
	joined := strings.Join(out.Limitations, "\n")
	if strings.Contains(joined, "精确命中 0 条") {
		t.Fatalf("truncated result must not claim zero records: %v", out.Limitations)
	}
	if !strings.Contains(joined, "快照上限") {
		t.Fatalf("lim=%v", out.Limitations)
	}
}

func TestLogBotResultsEndpointError(t *testing.T) {
	f := newLogbotFake(t)
	f.resultsStatus = http.StatusNotFound
	out := f.service().QueryAnalysisDelivery(context.Background(), DeliveryQuery{
		Direction: "outbound",
		Sender:    "sender@example.com",
		TimeRange: testRange(),
	})
	if out.Status != "error" || out.Code != "CK_LOGS_UPSTREAM_ERROR" {
		t.Fatalf("%+v", out)
	}
	if !strings.Contains(strings.Join(out.Limitations, "\n"), "404") {
		t.Fatalf("lim=%v", out.Limitations)
	}
}

// countOnly 只取 total_count，不翻页拉明细；即使第一页后仍有游标，
// maxRows==1 的计数语义也不能被 detail 的行上限截断逻辑污染。
func TestLogBotCountOnlyUsesFilteredCount(t *testing.T) {
	f := newLogbotFake(t)
	for i := 0; i < 101; i++ {
		f.rows = append(f.rows, daRow("2026-08-01 10:00:01+08:00", "sender@example.com", "peer@example.com", "0"))
	}
	in := testInput()
	in.CountOnly = true
	out := f.service().QuerySelfServiceDelivery(context.Background(), in)
	if out.Status != "ok" || out.Total == nil || *out.Total != 101 {
		t.Fatalf("%+v", out)
	}
	if out.CountOnly == nil || !*out.CountOnly || len(out.Entries) != 0 {
		t.Fatalf("%+v", out)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.resultCalls != 1 {
		t.Fatalf("countOnly resultCalls=%d want 1 (no detail paging)", f.resultCalls)
	}
}

func TestLogBotDomainQueryAddsPlatformMode(t *testing.T) {
	f := newLogbotFake(t)
	out := f.service().QueryAnalysisDelivery(context.Background(), DeliveryQuery{
		Direction: "outbound",
		Domain:    "Example.COM",
		TimeRange: testRange(),
	})
	if out.Status != "ok" {
		t.Fatalf("%+v", out)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	filters, _ := f.lastReq["filters"].(map[string]any)
	if f.lastReq["platform_mode"] != "all" || filters["from_addr"] != "example.com" {
		t.Fatalf("domain query req=%v", f.lastReq)
	}
}

// DA 源的 msgId 走 mid 筛选提交给 LogBot；非 DA 源才直接 not_available。
func TestLogBotMsgIDOnDAUsesMidFilter(t *testing.T) {
	f := newLogbotFake(t)
	res := LogBotBackend{Client: f.client()}.Query(context.Background(),
		Filters{MsgID: "unit-mid-1", TimeRange: testRange()},
		QueryOptions{Dataset: "delivery_agent"})
	if !res.OK {
		t.Fatalf("%+v", res)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	filters, _ := f.lastReq["filters"].(map[string]any)
	if filters["mid"] != "unit-mid-1" {
		t.Fatalf("req=%v", f.lastReq)
	}
}

func TestLogBotMsgIDFilterNotAvailable(t *testing.T) {
	f := newLogbotFake(t)
	out := f.service().QueryAnalysisDelivery(context.Background(), DeliveryQuery{
		Direction: "outbound",
		Sender:    "sender@example.com",
		MsgID:     "unit-tid-1",
		TimeRange: testRange(),
	})
	if out.Status != "not_available" {
		t.Fatalf("%+v", out)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.submits != 0 {
		t.Fatalf("must not submit upstream for msgId filter, submits=%d", f.submits)
	}
}

func TestLogBotTraceNotAvailable(t *testing.T) {
	f := newLogbotFake(t)
	out := f.service().TraceByTid(context.Background(), MessageQuery{Tid: "unit-tid-1", TimeRange: testRange()})
	if out.Status != "not_available" {
		t.Fatalf("%+v", out)
	}
	joined := strings.Join(out.Limitations, "\n")
	if !strings.Contains(joined, "不等于「没有记录」") {
		t.Fatalf("lim=%v", out.Limitations)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.submits != 0 {
		t.Fatalf("must not submit upstream for trace")
	}
}

func TestLogBotAccountLogsNotAvailable(t *testing.T) {
	f := newLogbotFake(t)
	for _, ds := range []string{"auth_pop3", "auth_webmail", "auth_smtp", "mail_ops_pop3", "mail_ops_webmail"} {
		out := f.service().QueryAccountLog(context.Background(), AccountQuery{
			Dataset:   ds,
			Account:   "user@example.com",
			TimeRange: testRange(),
		})
		if out.Status != "not_available" {
			t.Fatalf("%s: %+v", ds, out)
		}
		if !strings.Contains(strings.Join(out.Limitations, "\n"), "LogBot") {
			t.Fatalf("%s: lim=%v", ds, out.Limitations)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.submits != 0 {
		t.Fatalf("must not submit upstream for auth/ops datasets, submits=%d", f.submits)
	}
}

func TestLogBotNotConfigured(t *testing.T) {
	svc := NewLogBotService(&LogBotClient{})
	out := svc.QuerySelfServiceDelivery(context.Background(), testInput())
	if out.Status != "not_available" {
		t.Fatalf("%+v", out)
	}
	if !strings.Contains(strings.Join(out.Limitations, "\n"), "LogBot 账号未配置") {
		t.Fatalf("lim=%v", out.Limitations)
	}
}

func TestLogBotAuditTrail(t *testing.T) {
	f := newLogbotFake(t)
	rec := &auditlog.Record{}
	ctx := auditlog.WithRecord(context.Background(), rec)
	out := f.service().QuerySelfServiceDelivery(ctx, testInput())
	if out.Status != "ok" {
		t.Fatalf("%+v", out)
	}
	raw := rec.Marshal()
	var parsed struct {
		LogBotCalls []auditlog.LogBotCall `json:"logbot_calls"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	var stages []string
	for _, c := range parsed.LogBotCalls {
		stages = append(stages, c.Stage)
		if c.Stage == "poll" && c.JobID == "" {
			t.Fatalf("poll audit missing job_id: %+v", c)
		}
	}
	joined := strings.Join(stages, ",")
	if !strings.Contains(joined, "login") || !strings.Contains(joined, "submit") || !strings.Contains(joined, "poll") {
		t.Fatalf("audit stages=%v", stages)
	}
	if strings.Contains(string(raw), "unit-svc-password") || strings.Contains(string(raw), "unit-session-cookie") {
		t.Fatalf("credential in audit: %s", raw)
	}
}

func TestLogBotBearerFallback(t *testing.T) {
	// Bearer 服务令牌：不登录，直接带 Authorization 头。
	f := newLogbotFake(t)
	c := f.client()
	c.Username, c.Password = "", ""
	c.Token = "unit-service-token"
	backend := LogBotBackend{Client: c}
	res := backend.Query(context.Background(), Filters{Sender: "sender@example.com", TimeRange: testRange()}, QueryOptions{Dataset: "delivery_agent", CountOnly: true})
	if !res.OK {
		t.Fatalf("%+v", res)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.logins != 0 {
		t.Fatalf("bearer mode must not login, logins=%d", f.logins)
	}
}

// LogBot 的 MTA 行无 result/respond/errinfo：兜底路径对 unknown 状态给出退化提示。
func TestLogBotMTAFallbackUnknownHint(t *testing.T) {
	f := newLogbotFake(t)
	f.rows = []map[string]any{{
		"timestamp":  "2026-08-01 10:00:00+08:00",
		"sender":     "sender@example.com",
		"to":         "peer@example.com",
		"cmd":        "DATA",
		"log_source": "MTA",
	}}
	out := f.service().QuerySelfServiceDelivery(context.Background(), testInput())
	if out.Status != "ok" {
		t.Fatalf("status=%s code=%s lim=%v", out.Status, out.Code, out.Limitations)
	}
	if len(out.Entries) == 0 {
		t.Fatal("expected MTA fallback entries")
	}
	joined := strings.Join(out.Limitations, "\n")
	if !strings.Contains(joined, "MTA 行不包含投递结果") {
		t.Fatalf("missing degradation hint: %v", out.Limitations)
	}
}
