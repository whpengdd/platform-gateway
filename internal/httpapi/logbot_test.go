package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"platform-gateway/internal/auth"
	"platform-gateway/internal/cklogs"
	"platform-gateway/internal/queue"
)

// logbotHTTPFake 是最小 LogBot 模拟：表单登录 + 提交任务 + 一次轮询成功。
type logbotHTTPFake struct {
	srv      *httptest.Server
	rows     []map[string]any
	lastForm url.Values
}

func newLogbotHTTPFake(t *testing.T) *logbotHTTPFake {
	t.Helper()
	f := &logbotHTTPFake{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "logbot_session", Value: "unit-session", Path: "/"})
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/api/query-jobs", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.lastForm = r.PostForm
		_, _ = w.Write([]byte(`{"job_id":"j1"}`))
	})
	mux.HandleFunc("/api/query-jobs/j1", func(w http.ResponseWriter, r *http.Request) {
		srcs := f.lastForm["log_source[]"]
		sources := map[string]any{}
		var rows []map[string]any
		for _, src := range srcs {
			var matched []map[string]any
			for _, row := range f.rows {
				if strings.EqualFold(asStringHTTP(row["log_source"]), src) {
					matched = append(matched, row)
				}
			}
			rows = append(rows, matched...)
			sources[src] = map[string]any{"status": "complete", "loaded": len(matched)}
		}
		if rows == nil {
			rows = []map[string]any{}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "job": map[string]any{
			"job_id": "j1", "status": "success",
			"result": map[string]any{"data": rows, "sources": sources},
		}})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func asStringHTTP(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func setupLogbotAPI(t *testing.T, f *logbotHTTPFake) *httptest.Server {
	t.Helper()
	hc := f.srv.Client()
	hc.CheckRedirect = func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }
	svc := cklogs.NewLogBotService(&cklogs.LogBotClient{
		BaseURL:      f.srv.URL,
		Username:     "unit-svc-account",
		Password:     "unit-svc-password",
		PollInterval: time.Millisecond,
		Timeout:      5 * time.Second,
		HTTPClient:   hc,
	})
	h := New(Config{
		Auth:    auth.NewWithClasses([]string{testToken}, []string{internalToken}, nil),
		CK:      svc,
		Gate:    queue.New("cklogs", 1, 4, time.Second),
		CKReady: func() bool { return true },
		Now:     frozenNow,
	})
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

func daRowHTTP(ts, from, to, cmd, result, folderID, msgid string) map[string]any {
	return map[string]any{
		"timestamp": ts, "mailfrom": from, "to": to, "subject": "unit",
		"cmd": cmd, "result": result, "folderid": folderID,
		"commresult": "Non-Spam", "log_source": "DA",
		"otherlog": map[string]any{"hdrmsgid": msgid},
	}
}

func TestLogBotEndToEndOutboundInbound(t *testing.T) {
	f := newLogbotHTTPFake(t)
	f.rows = []map[string]any{
		daRowHTTP("2026-08-01 10:00:01+08:00", "a@unit.test", "b@unit.test", "remote", "0", "", "<u1@unit.test>"),
		daRowHTTP("2026-08-01 11:00:01+08:00", "c@unit.test", "a@unit.test", "local", "0", "1", "<u2@unit.test>"),
		{"timestamp": "2026-08-01 12:00:00+08:00", "log_source": "MTA", "sender": "c@unit.test",
			"rcpt": []any{"a@unit.test"}, "cmd": "DATA", "result": "s_ok", "subject": "unit"},
	}
	ts := setupLogbotAPI(t, f)
	tr := `"timeRange":{"from":"2026-08-01T00:00:00.000Z","to":"2026-08-02T00:00:00.000Z"}`

	// outbound：a@unit.test 发出的 remote 行。
	res := doJSON(t, ts, http.MethodPost, "/v1/cklogs/delivery", testToken, `{"direction":"outbound","account":"a@unit.test","peer":"b@unit.test",`+tr+`}`)
	out := readMap(t, res)
	if res.StatusCode != 200 || out["status"] != "ok" {
		t.Fatalf("%d %v", res.StatusCode, out)
	}
	if n, _ := out["total"].(float64); n != 1 {
		t.Fatalf("outbound total=%v entries=%v", out["total"], out["entries"])
	}
	entries, _ := out["entries"].([]any)
	e0, _ := entries[0].(map[string]any)
	if e0["sender"] != "a@unit.test" || e0["timestampIso"] != "2026-08-01T02:00:01.000Z" {
		t.Fatalf("%v", e0)
	}
	if f.lastForm.Get("from_addr") != "a@unit.test" {
		t.Fatalf("form=%v", f.lastForm)
	}

	// inbound：a@unit.test 收到的 local 行。
	res = doJSON(t, ts, http.MethodPost, "/v1/cklogs/delivery", testToken, `{"direction":"inbound","account":"a@unit.test","peer":"c@unit.test",`+tr+`}`)
	out = readMap(t, res)
	if res.StatusCode != 200 || out["status"] != "ok" {
		t.Fatalf("%d %v", res.StatusCode, out)
	}
	if n, _ := out["total"].(float64); n != 1 {
		t.Fatalf("inbound total=%v entries=%v", out["total"], out["entries"])
	}
	if f.lastForm.Get("to_addr") != "a@unit.test" {
		t.Fatalf("form=%v", f.lastForm)
	}

	// both：内部 token 的分析接口，发件人过滤。
	res = doJSON(t, ts, http.MethodPost, "/v1/cklogs/analysis/delivery", internalToken, `{"direction":"both","sender":"c@unit.test",`+tr+`}`)
	out = readMap(t, res)
	if res.StatusCode != 200 || out["status"] != "ok" {
		t.Fatalf("%d %v", res.StatusCode, out)
	}
	if n, _ := out["total"].(float64); n != 1 {
		t.Fatalf("both total=%v entries=%v", out["total"], out["entries"])
	}

	// 外部 token 禁止分析接口的合同不变。
	res = doJSON(t, ts, http.MethodPost, "/v1/cklogs/analysis/delivery", testToken, `{"direction":"both","sender":"c@unit.test",`+tr+`}`)
	if res.StatusCode != 403 {
		t.Fatalf("external analysis=%d", res.StatusCode)
	}
	res.Body.Close()
}

func TestLogBotEndToEndPagination(t *testing.T) {
	f := newLogbotHTTPFake(t)
	for i := 0; i < 12; i++ {
		sec := "0" + string(rune('0'+i))
		if i >= 10 {
			sec = string(rune('1')) + string(rune('0'+i-10))
		}
		f.rows = append(f.rows, daRowHTTP("2026-08-01 10:00:"+sec+"+08:00", "a@unit.test", "b@unit.test", "remote", "0", "", "<up"+string(rune('a'+i))+"@unit.test>"))
	}
	ts := setupLogbotAPI(t, f)
	tr := `"timeRange":{"from":"2026-08-01T00:00:00.000Z","to":"2026-08-02T00:00:00.000Z"}`
	res := doJSON(t, ts, http.MethodPost, "/v1/cklogs/delivery", testToken, `{"direction":"outbound","account":"a@unit.test","peer":"b@unit.test","page":1,"pageSize":5,`+tr+`}`)
	out := readMap(t, res)
	entries, _ := out["entries"].([]any)
	if len(entries) != 5 || out["hasMore"] != true {
		t.Fatalf("page1: %v entries", len(entries))
	}
	if n, _ := out["total"].(float64); n != 12 {
		t.Fatalf("total=%v", out["total"])
	}
	res = doJSON(t, ts, http.MethodPost, "/v1/cklogs/delivery", testToken, `{"direction":"outbound","account":"a@unit.test","peer":"b@unit.test","page":3,"pageSize":5,`+tr+`}`)
	out = readMap(t, res)
	entries, _ = out["entries"].([]any)
	if len(entries) != 2 || out["hasMore"] != false {
		t.Fatalf("page3: %v entries hasMore=%v", len(entries), out["hasMore"])
	}
}

func TestLogBotEndToEndNotAvailableContracts(t *testing.T) {
	f := newLogbotHTTPFake(t)
	ts := setupLogbotAPI(t, f)
	tr := `"timeRange":{"from":"2026-08-01T00:00:00.000Z","to":"2026-08-02T00:00:00.000Z"}`

	// /message：logbot 下 not_available，不冒充无记录。
	res := doJSON(t, ts, http.MethodPost, "/v1/cklogs/message", testToken, `{"tid":"unit-tid",`+tr+`}`)
	out := readMap(t, res)
	if out["status"] != "not_available" {
		t.Fatalf("message: %v", out)
	}

	// /login：各协议 not_available。
	res = doJSON(t, ts, http.MethodPost, "/v1/cklogs/login", testToken, `{"account":"a@unit.test",`+tr+`}`)
	out = readMap(t, res)
	results, _ := out["results"].([]any)
	if len(results) == 0 {
		t.Fatalf("login: %v", out)
	}
	for _, item := range results {
		m, _ := item.(map[string]any)
		if m["status"] != "not_available" {
			t.Fatalf("login item: %v", m)
		}
	}

	// analysis/auth 与 analysis/ops。
	for _, tc := range []struct{ path, body string }{
		{"/v1/cklogs/analysis/auth", `{"account":"a@unit.test","protocol":"pop3",` + tr + `}`},
		{"/v1/cklogs/analysis/ops", `{"account":"a@unit.test","protocol":"webmail","op":"delete",` + tr + `}`},
	} {
		res = doJSON(t, ts, http.MethodPost, tc.path, internalToken, tc.body)
		out = readMap(t, res)
		if out["status"] != "not_available" {
			t.Fatalf("%s: %v", tc.path, out)
		}
	}
}

func TestLogBotEndToEndRecipientFallback(t *testing.T) {
	f := newLogbotHTTPFake(t)
	// DA 与 MTA 都精确 0 命中且带 recipient 条件：保持收件人覆盖未决语义，
	// 不得报成「没有记录」。
	ts := setupLogbotAPI(t, f)
	tr := `"timeRange":{"from":"2026-08-01T00:00:00.000Z","to":"2026-08-02T00:00:00.000Z"}`
	res := doJSON(t, ts, http.MethodPost, "/v1/cklogs/analysis/delivery", internalToken, `{"direction":"outbound","sender":"c@unit.test","recipient":"a@unit.test",`+tr+`}`)
	out := readMap(t, res)
	if out["status"] != "error" || out["code"] != "CK_LOGS_RECIPIENT_FALLBACK_UNRESOLVED" {
		t.Fatalf("%v", out)
	}
	lim, _ := out["limitations"].([]any)
	if len(lim) == 0 {
		t.Fatalf("limitations empty: %v", out)
	}
}
