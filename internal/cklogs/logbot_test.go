package cklogs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"platform-gateway/internal/auditlog"
)

// 本文件的账号/密码/令牌全部是测试专用的虚构值，不对应任何真实凭据。

type logbotFake struct {
	srv *httptest.Server

	mu           sync.Mutex
	logins       int
	submits      int
	polls        int
	lastForm     url.Values
	failLogin    bool
	poll401Once  bool
	redirectOnce bool
	submitStatus int
	submitDetail string
	jobStatuses  []string // 每次轮询依次返回；耗尽后返回 success
	neverDone    bool     // 轮询恒为 running（预算耗尽测试）
	jobError     string
	rows         []map[string]any
	sources      map[string]map[string]any // 按来源名覆盖状态；未覆盖时默认 complete
	jobs         map[string]url.Values
	nextJob      int
}

func newLogbotFake(t *testing.T) *logbotFake {
	t.Helper()
	f := &logbotFake{jobs: map[string]url.Values{}}
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
	mux.HandleFunc("/api/query-jobs", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		f.submits++
		f.lastForm = r.PostForm
		authed := f.hasAuthLocked(r)
		status := f.submitStatus
		detail := f.submitDetail
		if authed && status == 0 {
			f.nextJob++
			f.jobs[itoa(f.nextJob)] = r.PostForm
		}
		jobID := itoa(f.nextJob)
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
		_, _ = w.Write([]byte(`{"job_id":"` + jobID + `"}`))
	})
	mux.HandleFunc("/api/query-jobs/", func(w http.ResponseWriter, r *http.Request) {
		jobID := strings.TrimPrefix(r.URL.Path, "/api/query-jobs/")
		f.mu.Lock()
		f.polls++
		authed := f.hasAuthLocked(r)
		if authed && f.poll401Once {
			f.poll401Once = false
			f.mu.Unlock()
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"detail":"session expired"}`))
			return
		}
		if authed && f.redirectOnce {
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
		form := f.jobs[jobID]
		var rows []map[string]any
		var sources map[string]map[string]any
		if status == "success" {
			srcs := form["log_source[]"]
			sources = map[string]map[string]any{}
			for _, src := range srcs {
				var matched []map[string]any
				for _, row := range f.rows {
					if strings.EqualFold(asString(row["log_source"]), src) {
						matched = append(matched, row)
					}
				}
				rows = append(rows, matched...)
				st, ok := f.sources[src]
				if !ok {
					st = map[string]any{"status": "complete", "loaded": len(matched)}
				}
				sources[src] = st
			}
			if rows == nil {
				rows = []map[string]any{}
			}
		}
		f.mu.Unlock()
		if !authed {
			w.Header().Set("Location", "/login.html")
			w.WriteHeader(http.StatusFound)
			return
		}
		job := map[string]any{"job_id": jobID, "status": status}
		if status == "success" {
			job["result"] = map[string]any{"data": rows, "sources": sources}
		} else if status != "queued" && status != "running" {
			job["error"] = jobErr
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "job": job})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
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
		BaseURL:      f.srv.URL,
		Username:     "unit-svc-account",
		Password:     "unit-svc-password",
		PollInterval: time.Millisecond,
		Timeout:      5 * time.Second,
		HTTPClient:   hc,
	}
}

func (f *logbotFake) service() *Service { return NewLogBotService(f.client()) }

func daRow(ts, from, to, cmd, result, folderID string, other map[string]any) map[string]any {
	row := map[string]any{
		"timestamp":  ts,
		"mailfrom":   from,
		"to":         to,
		"subject":    "unit test subject",
		"cmd":        cmd,
		"result":     result,
		"folderid":   folderID,
		"commresult": "Non-Spam",
		"log_source": "DA",
	}
	if other != nil {
		row["otherlog"] = other
	}
	return row
}

func TestLogBotDeliveryOutboundFlow(t *testing.T) {
	f := newLogbotFake(t)
	f.rows = []map[string]any{
		// antivirus 行与空 cmd 汇总行必须被过滤；重复 local 行必须去重。
		daRow("2026-08-01 10:00:00+08:00", "sender@example.com", "peer@example.com", "antivirus", "0", "", nil),
		daRow("2026-08-01 10:00:00+08:00", "sender@example.com", "peer@example.com", "", "0", "", nil),
		daRow("2026-08-01 10:00:01+08:00", "sender@example.com", "peer@example.com", "local", "0", "1", map[string]any{"hdrmsgid": "<unit-msg-1@example.com>"}),
		daRow("2026-08-01 10:00:01+08:00", "sender@example.com", "peer@example.com", "local", "0", "1", map[string]any{"hdrmsgid": "<unit-msg-1@example.com>"}),
		daRow("2026-08-01 10:00:02+08:00", "sender@example.com", "peer@example.com", "remote", "0", "", map[string]any{"hdrmsgid": "<unit-msg-1@example.com>"}),
		// 已发送副本必须被过滤。
		daRow("2026-08-01 10:00:03+08:00", "sender@example.com", "peer@example.com", "local", "0", "3", map[string]any{"hdrmsgid": "<unit-msg-2@example.com>"}),
	}
	f.sources = map[string]map[string]any{"DA": {"status": "complete", "loaded": 6, "scanned_through": "2026-08-02 00:00:00"}}
	out := f.service().QuerySelfServiceDelivery(context.Background(), testInput())
	if out.Status != "ok" {
		t.Fatalf("%+v", out)
	}
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
	if e.DeliveryStatus != "delivered" || e.MessageID != "<unit-msg-1@example.com>" {
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
	form := f.lastForm
	if form.Get("query_type") != "transmission" || form.Get("from_addr") != "sender@example.com" {
		t.Fatalf("form=%v", form)
	}
	if form.Get("start_time") != "2026-08-01 08:00:00" || form.Get("end_time") != "2026-08-02 07:59:59" {
		t.Fatalf("time window: %v %v", form.Get("start_time"), form.Get("end_time"))
	}
}

func TestLogBotDeliveryLimitMarksTruncated(t *testing.T) {
	f := newLogbotFake(t)
	f.rows = []map[string]any{
		daRow("2026-08-01 10:00:01+08:00", "sender@example.com", "peer@example.com", "remote", "0", "", map[string]any{"hdrmsgid": "<unit-msg-9@example.com>"}),
	}
	f.sources = map[string]map[string]any{"DA": {"status": "limit", "loaded": 1000}}
	out := f.service().QuerySelfServiceDelivery(context.Background(), testInput())
	if out.Status != "ok" {
		t.Fatalf("%+v", out)
	}
	if out.Truncated == nil || !*out.Truncated {
		t.Fatalf("truncated=%v", out.Truncated)
	}
	joined := strings.Join(out.Limitations, "\n")
	if !strings.Contains(joined, "展示上限") {
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

func TestLogBotPartialSourceIsNotEmptyClaim(t *testing.T) {
	f := newLogbotFake(t)
	f.sources = map[string]map[string]any{"MTA": {"status": "partial", "loaded": 0, "scanned_through": "2026-08-01 12:00:00"}}
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
		t.Fatalf("partial source must not claim zero records: %v", out.Limitations)
	}
	if !strings.Contains(joined, "2026-08-01 12:00:00") {
		t.Fatalf("partial limitation must include scanned_through: %v", out.Limitations)
	}
}

func TestLogBotSkippedFutureSource(t *testing.T) {
	f := newLogbotFake(t)
	f.sources = map[string]map[string]any{"MTA": {"status": "skipped_future", "loaded": 0}}
	out := f.service().QueryAnalysisDelivery(context.Background(), DeliveryQuery{
		Direction: "outbound",
		Sender:    "sender@example.com",
		TimeRange: testRange(),
	})
	if out.Status != "ok" {
		t.Fatalf("%+v", out)
	}
	if !strings.Contains(strings.Join(out.Limitations, "\n"), "在未来") {
		t.Fatalf("lim=%v", out.Limitations)
	}
}

func TestLogBotAllSourcesFailed(t *testing.T) {
	f := newLogbotFake(t)
	f.sources = map[string]map[string]any{"MTA": {"status": "failed", "error": "disk full"}}
	out := f.service().QueryAnalysisDelivery(context.Background(), DeliveryQuery{
		Direction: "outbound",
		Sender:    "sender@example.com",
		TimeRange: testRange(),
	})
	if out.Status != "error" {
		t.Fatalf("%+v", out)
	}
	if !strings.Contains(strings.Join(out.Limitations, "\n"), "disk full") {
		t.Fatalf("lim=%v", out.Limitations)
	}
}

func TestLogBotCountOnlyUsesFilteredCount(t *testing.T) {
	f := newLogbotFake(t)
	f.rows = []map[string]any{
		daRow("2026-08-01 10:00:01+08:00", "sender@example.com", "peer@example.com", "local", "0", "1", nil),
		daRow("2026-08-01 10:00:02+08:00", "sender@example.com", "peer@example.com", "antivirus", "0", "", nil),
	}
	in := testInput()
	in.CountOnly = true
	out := f.service().QuerySelfServiceDelivery(context.Background(), in)
	// antivirus 行被过滤，计数与明细口径一致（只算 local/remote）。
	if out.Status != "ok" || out.Total == nil || *out.Total != 1 {
		t.Fatalf("%+v", out)
	}
	if out.CountOnly == nil || !*out.CountOnly || len(out.Entries) != 0 {
		t.Fatalf("%+v", out)
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
	if f.lastForm.Get("platform_mode") != "all" || f.lastForm.Get("from_addr") != "example.com" {
		t.Fatalf("domain query form=%v", f.lastForm)
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
	f.sources = map[string]map[string]any{"DA": {"status": "complete", "loaded": 0}}
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
	// Bearer 备选分支：不登录，直接带 Authorization 头。
	f := newLogbotFake(t)
	f.sources = map[string]map[string]any{"DA": {"status": "complete", "loaded": 0}}
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
		"rcpt":       []any{"peer@example.com"},
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
