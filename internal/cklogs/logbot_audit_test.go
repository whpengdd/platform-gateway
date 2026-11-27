package cklogs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"platform-gateway/internal/auditlog"
)

// 数据均为构造的虚构值，不对应真实凭据。

func auditCalls(t *testing.T, rec *auditlog.Record) []auditlog.LogBotCall {
	t.Helper()
	var parsed struct {
		Calls []auditlog.LogBotCall `json:"logbot_calls"`
	}
	if err := json.Unmarshal(rec.Marshal(), &parsed); err != nil {
		t.Fatal(err)
	}
	return parsed.Calls
}

func TestAuditSubmitAcceptedAndLoginSuccessAreOK(t *testing.T) {
	f := newLogbotFake(t)
	res, calls := queryWithAudit(t, f)
	if !res.OK {
		t.Fatalf("%+v", res)
	}
	submit := lastCall(calls, "submit")
	if submit.HTTPStatus != http.StatusAccepted || submit.ErrorKind != "" || !submit.OK {
		t.Fatalf("submit 202 audit: %+v", submit)
	}
	login := lastCall(calls, "login")
	if login.HTTPStatus != http.StatusOK || login.ErrorKind != "" || !login.OK {
		t.Fatalf("login audit: %+v", login)
	}
}

func TestAuditReloginMarksFirstAttemptAuthExpired(t *testing.T) {
	for name, set := range map[string]func(*logbotFake){
		"401":      func(f *logbotFake) { f.poll401Once = true },
		"redirect": func(f *logbotFake) { f.redirectOnce = true },
	} {
		f := newLogbotFake(t)
		set(f)
		res, calls := queryWithAudit(t, f)
		if !res.OK {
			t.Fatalf("%s: %+v", name, res)
		}
		var polls []auditlog.LogBotCall
		for _, c := range calls {
			if c.Stage == "poll" {
				polls = append(polls, c)
			}
		}
		if len(polls) < 2 {
			t.Fatalf("%s: polls=%+v", name, polls)
		}
		first := polls[0]
		if first.ErrorKind != "auth_expired" || first.OK || (first.HTTPStatus != 401 && first.HTTPStatus != 302) {
			t.Fatalf("%s: first attempt audit: %+v", name, first)
		}
		if last := polls[len(polls)-1]; last.ErrorKind != "" || !last.OK || last.HTTPStatus != 200 {
			t.Fatalf("%s: retried attempt audit: %+v", name, last)
		}
	}
}

func TestAuditFailedJobEndToEnd(t *testing.T) {
	f := newLogbotFake(t)
	f.jobStatuses = []string{"failed"}
	f.jobError = "retention exceeded"
	rec := &auditlog.Record{}
	ctx := auditlog.WithRecord(context.Background(), rec)
	out := NewLogBotService(f.client()).QuerySelfServiceDelivery(ctx, testInput())
	if out.Status != "error" {
		t.Fatalf("%+v", out)
	}
	c := lastCall(auditCalls(t, rec), "poll")
	if c.ErrorKind != "upstream_error" || c.OK {
		t.Fatalf("poll audit: %+v", c)
	}
}

func TestAudit422AndPollStatuses(t *testing.T) {
	f := newLogbotFake(t)
	f.submitStatus = http.StatusUnprocessableEntity
	f.submitDetail = "bad form"
	res, calls := queryWithAudit(t, f)
	if res.OK || !strings.Contains(res.Message, "HTTP 422") {
		t.Fatalf("%+v", res)
	}
	if c := lastCall(calls, "submit"); c.ErrorKind != "upstream_error" || c.HTTPStatus != 422 || c.OK {
		t.Fatalf("422 audit: %+v", c)
	}

	for _, tc := range []struct {
		status int
		kind   string
	}{
		{http.StatusTooManyRequests, "rate_limited"},
		{http.StatusNotFound, "upstream_error"},
		{http.StatusForbidden, "auth_error"},
		{http.StatusInternalServerError, "upstream_error"},
	} {
		f := newLogbotFake(t)
		inner := f.srv.Config.Handler
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/agent/log-query/jobs/") && !strings.HasSuffix(r.URL.Path, "/results") {
				w.WriteHeader(tc.status)
				return
			}
			inner.ServeHTTP(w, r)
		}))
		cl := f.client()
		cl.BaseURL = srv.URL
		cl.HTTPClient = srv.Client()
		cl.HTTPClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		rec := &auditlog.Record{}
		ctx := auditlog.WithRecord(context.Background(), rec)
		res := LogBotBackend{Client: cl}.Query(ctx, Filters{Sender: "sender@example.com", TimeRange: testRange()}, QueryOptions{Dataset: "delivery_agent", CountOnly: true})
		srv.Close()
		c := lastCall(auditCalls(t, rec), "poll")
		if res.OK || c.ErrorKind != tc.kind || c.HTTPStatus != tc.status || c.OK {
			t.Fatalf("poll %d: res=%+v audit=%+v", tc.status, res, c)
		}
	}

	// 轮询到 running 再成功：中间状态审计为 200/OK 无错误类型。
	f = newLogbotFake(t)
	f.jobStatuses = []string{"queued", "running"}
	_, calls = queryWithAudit(t, f)
	n := 0
	for _, c := range calls {
		if c.Stage == "poll" {
			n++
			if c.ErrorKind != "" || !c.OK || c.HTTPStatus != 200 {
				t.Fatalf("poll audit: %+v", c)
			}
		}
	}
	if n != 3 {
		t.Fatalf("poll audits=%d", n)
	}
}

// 任务失败 error 里反射的凭据/cookie 不得进入审计或响应文本。
func TestJobErrorSanitized(t *testing.T) {
	f := newLogbotFake(t)
	f.jobStatuses = []string{"failed"}
	f.jobError = "boom password=unit-svc-password cookie: unit-session-cookie"
	rec := &auditlog.Record{}
	ctx := auditlog.WithRecord(context.Background(), rec)
	out := NewLogBotService(f.client()).QuerySelfServiceDelivery(ctx, testInput())
	if out.Status != "error" {
		t.Fatalf("%+v", out)
	}
	raw, _ := json.Marshal(out)
	audit := rec.Marshal()
	for _, leak := range []string{"unit-svc-password", "unit-session-cookie", "cookie:", "password="} {
		if strings.Contains(string(audit), leak) {
			t.Fatalf("audit leaks %q: %s", leak, audit)
		}
		if strings.Contains(string(raw), leak) {
			t.Fatalf("response leaks %q: %s", leak, raw)
		}
	}
}
