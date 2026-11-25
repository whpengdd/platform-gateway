package cklogs

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"platform-gateway/internal/auditlog"
)

// 本文件覆盖独立验收后的修复项：skipped_future 不精确、审计错误类型完整、
// 上游文本脱敏、expired 与多来源混合状态。数据均为构造的脱敏测试数据。

func queryWithAudit(t *testing.T, f *logbotFake) (QueryResult, []auditlog.LogBotCall) {
	t.Helper()
	rec := &auditlog.Record{}
	ctx := auditlog.WithRecord(context.Background(), rec)
	backend := LogBotBackend{Client: f.client()}
	res := backend.Query(ctx, Filters{Sender: "sender@example.com", TimeRange: testRange()}, QueryOptions{Dataset: "delivery_agent", CountOnly: true})
	raw := rec.Marshal()
	var parsed struct {
		Calls []auditlog.LogBotCall `json:"logbot_calls"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	return res, parsed.Calls
}

func lastCall(calls []auditlog.LogBotCall, stage string) auditlog.LogBotCall {
	for i := len(calls) - 1; i >= 0; i-- {
		if calls[i].Stage == stage {
			return calls[i]
		}
	}
	return auditlog.LogBotCall{}
}

// Fix 2：skipped_future 必须标记不精确（Partial=true、TotalExact=false）。
func TestSkippedFutureIsNotExact(t *testing.T) {
	f := newLogbotFake(t)
	f.sources = map[string]map[string]any{"DA": {"status": "skipped_future", "loaded": 0}}
	res, _ := queryWithAudit(t, f)
	if !res.OK {
		t.Fatalf("%+v", res)
	}
	if !res.Partial || res.TotalExact {
		t.Fatalf("skipped_future must be partial and inexact: %+v", res)
	}
	joined := strings.Join(res.Limitations, "\n")
	if !strings.Contains(joined, "在未来") || !strings.Contains(joined, "不能据此断言没有记录") {
		t.Fatalf("lim=%v", res.Limitations)
	}
}

// Fix 5：expired 来源；以及 complete+limit+failed 多来源混合。
func TestExpiredAndMixedSources(t *testing.T) {
	// 单来源 expired → 全部失败 → error。
	_, fail := assessSources([]string{"MTA"}, map[string]logbotSourceStatus{
		"MTA": {Status: "expired", Error: "retention exceeded"},
	}, 0, nil)
	if fail == nil || fail.ErrorKind != "upstream_error" || !strings.Contains(fail.Message, "retention exceeded") {
		t.Fatalf("expired: %+v", fail)
	}
	// 混合：DA complete、MTA limit、PROXYSVR failed → 有数据但截断+部分失败，不是整体错误。
	res, fail := assessSources([]string{"DA", "MTA", "PROXYSVR"}, map[string]logbotSourceStatus{
		"DA":       {Status: "complete", Loaded: 3},
		"MTA":      {Status: "limit", Loaded: 1000},
		"PROXYSVR": {Status: "failed", Error: "disk full"},
	}, 1003, nil)
	if fail != nil {
		t.Fatalf("mixed sources must not fail wholly: %+v", fail)
	}
	if !res.Truncated || !res.Partial || res.TotalExact {
		t.Fatalf("mixed: %+v", res)
	}
	joined := strings.Join(res.Limitations, "\n")
	if !strings.Contains(joined, "展示上限") || !strings.Contains(joined, "disk full") || !strings.Contains(joined, "未查到") {
		t.Fatalf("lim=%v", res.Limitations)
	}
	// 全部失败（failed+expired）→ error。
	_, fail = assessSources([]string{"DA", "MTA"}, map[string]logbotSourceStatus{
		"DA":  {Status: "failed", Error: "x"},
		"MTA": {Status: "expired", Error: "y"},
	}, 0, nil)
	if fail == nil || fail.ErrorKind != "upstream_error" {
		t.Fatalf("all failed: %+v", fail)
	}
}

// Fix 3：submit 403/429 的审计带映射后的错误类型。
func TestAuditSubmitErrorKinds(t *testing.T) {
	for _, tc := range []struct {
		status   int
		wantKind string
	}{
		{http.StatusForbidden, "not_available"},
		{http.StatusTooManyRequests, "rate_limited"},
		{http.StatusBadRequest, "upstream_error"},
		{http.StatusConflict, "upstream_error"},
		{http.StatusInternalServerError, "upstream_error"},
	} {
		f := newLogbotFake(t)
		f.submitStatus = tc.status
		f.submitDetail = "unit detail"
		_, calls := queryWithAudit(t, f)
		c := lastCall(calls, "submit")
		if c.ErrorKind != tc.wantKind || c.HTTPStatus != tc.status || c.OK {
			t.Fatalf("status %d: %+v", tc.status, c)
		}
	}
}

// Fix 3：HTTP 200 但任务失败 → poll 审计带 upstream_error；来源失败 → result 审计。
func TestAuditPollFailedJobAndFailedSources(t *testing.T) {
	f := newLogbotFake(t)
	f.jobStatuses = []string{"failed"}
	f.jobError = "unit backend exploded"
	_, calls := queryWithAudit(t, f)
	c := lastCall(calls, "poll")
	if c.ErrorKind != "upstream_error" || c.OK {
		t.Fatalf("failed job poll audit: %+v", c)
	}

	// 来源部分失败 → result 审计 partial_failure；全部失败 → upstream_error。
	f = newLogbotFake(t)
	f.sources = map[string]map[string]any{"DA": {"status": "failed", "error": "unit source error"}}
	_, calls = queryWithAudit(t, f)
	c = lastCall(calls, "result")
	if c.ErrorKind != "upstream_error" || c.SourceStatus != "DA=failed" {
		t.Fatalf("all-failed result audit: %+v", c)
	}
}

func TestAuditPartialFailureKind(t *testing.T) {
	// 通过 backend 混合来源只能构造单来源任务，这里直接验证 auditSourceOutcome。
	f := newLogbotFake(t)
	c := f.client()
	rec := &auditlog.Record{}
	ctx := auditlog.WithRecord(context.Background(), rec)
	c.auditSourceOutcome(ctx, "j1", []string{"DA", "MTA"}, map[string]logbotSourceStatus{
		"DA":  {Status: "complete", Loaded: 1},
		"MTA": {Status: "failed", Error: "unit err"},
	})
	raw := rec.Marshal()
	var parsed struct {
		Calls []auditlog.LogBotCall `json:"logbot_calls"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Calls) != 1 || parsed.Calls[0].ErrorKind != "partial_failure" || parsed.Calls[0].OK {
		t.Fatalf("%+v", parsed.Calls)
	}
}

// Fix 4：上游反射凭据/cookie 时，响应与审计都不含原文；控制字符去除、限长。
func TestUpstreamTextSanitized(t *testing.T) {
	// 403 detail 反射密码、cookie 与敏感 key=value。
	f := newLogbotFake(t)
	f.submitStatus = http.StatusForbidden
	f.submitDetail = "denied for unit-svc-password cookie=unit-session-cookie authorization: Bearer unit-token\n\x00binary"
	rec := &auditlog.Record{}
	ctx := auditlog.WithRecord(context.Background(), rec)
	out := NewLogBotService(f.client()).QuerySelfServiceDelivery(ctx, testInput())
	if out.Status != "not_available" {
		t.Fatalf("%+v", out)
	}
	raw, _ := json.Marshal(out)
	audit := rec.Marshal()
	for _, leak := range []string{"unit-svc-password", "unit-session-cookie", "unit-token", "cookie=", "authorization:"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("response leaks %q: %s", leak, raw)
		}
		if strings.Contains(string(audit), leak) {
			t.Fatalf("audit leaks %q: %s", leak, audit)
		}
	}
	joined := strings.Join(out.Limitations, "\n")
	if !strings.Contains(joined, "HTTP 403") || strings.Contains(joined, "\x00") {
		t.Fatalf("lim=%v", out.Limitations)
	}

	// 任务失败 error 反射密码。
	f = newLogbotFake(t)
	f.jobStatuses = []string{"failed"}
	f.jobError = "auth failed for unit-svc-account password=unit-svc-password"
	rec = &auditlog.Record{}
	ctx = auditlog.WithRecord(context.Background(), rec)
	out = NewLogBotService(f.client()).QuerySelfServiceDelivery(ctx, testInput())
	if out.Status != "error" {
		t.Fatalf("%+v", out)
	}
	raw, _ = json.Marshal(out)
	if strings.Contains(string(raw), "unit-svc-password") || strings.Contains(string(raw), "password=") {
		t.Fatalf("job error leaks: %s", raw)
	}

	// 限长与来源 error 脱敏。
	long := strings.Repeat("x", 500) + " unit-svc-password " + strings.Repeat("y", 500)
	f = newLogbotFake(t)
	f.sources = map[string]map[string]any{"DA": {"status": "failed", "error": long}}
	out = NewLogBotService(f.client()).QuerySelfServiceDelivery(context.Background(), testInput())
	joined = strings.Join(out.Limitations, "\n")
	if strings.Contains(joined, "unit-svc-password") {
		t.Fatalf("source error leaks: %v", out.Limitations)
	}
	if len([]rune(joined)) > 300 {
		t.Fatalf("not truncated: %d runes", len([]rune(joined)))
	}
}

// Fix 4：400/409/422 以固定中文说明为主，上游文本只作脱敏后附加。
func TestFormRejectionUsesFixedMessage(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusConflict, http.StatusUnprocessableEntity} {
		f := newLogbotFake(t)
		f.submitStatus = status
		f.submitDetail = "unit-svc-password in detail"
		out := NewLogBotService(f.client()).QuerySelfServiceDelivery(context.Background(), testInput())
		if out.Status != "error" {
			t.Fatalf("%d: %+v", status, out)
		}
		joined := strings.Join(out.Limitations, "\n")
		if !strings.Contains(joined, "HTTP "+itoa(status)) {
			t.Fatalf("%d: lim=%v", status, out.Limitations)
		}
		if strings.Contains(joined, "unit-svc-password") {
			t.Fatalf("%d: leaked: %v", status, out.Limitations)
		}
		// 固定说明在前，上游附加在后。
		if strings.Index(joined, "HTTP "+itoa(status)) > strings.Index(joined, "上游说明") && strings.Contains(joined, "上游说明") {
			t.Fatalf("%d: fixed text must lead: %v", status, out.Limitations)
		}
	}
}

// Fix 7：DA 行缺 tid 且缺 message-id 时，必须提示同一封邮件可能拆成多行（不自行认定可接受）。
func TestLogBotDASplitRiskLimitation(t *testing.T) {
	f := newLogbotFake(t)
	f.rows = []map[string]any{
		daRow("2026-08-01 10:00:01+08:00", "sender@example.com", "peer@example.com", "local", "0", "1", nil),
		daRow("2026-08-01 10:00:02+08:00", "sender@example.com", "peer@example.com", "remote", "0", "", nil),
	}
	f.sources = map[string]map[string]any{"DA": {"status": "complete", "loaded": 2}}
	out := f.service().QuerySelfServiceDelivery(context.Background(), testInput())
	if out.Status != "ok" {
		t.Fatalf("%+v", out)
	}
	joined := strings.Join(out.Limitations, "\n")
	if !strings.Contains(joined, "分行显示") {
		t.Fatalf("split-risk limitation missing: %v", out.Limitations)
	}
	// 有 hdrmsgid 时不应出现该提示。
	f2 := newLogbotFake(t)
	f2.rows = []map[string]any{
		daRow("2026-08-01 10:00:01+08:00", "sender@example.com", "peer@example.com", "remote", "0", "", map[string]any{"hdrmsgid": "<u1@example.com>"}),
	}
	f2.sources = map[string]map[string]any{"DA": {"status": "complete", "loaded": 1}}
	out2 := f2.service().QuerySelfServiceDelivery(context.Background(), testInput())
	if strings.Contains(strings.Join(out2.Limitations, "\n"), "分行显示") {
		t.Fatalf("false positive split-risk: %v", out2.Limitations)
	}
}

func TestSanitizeUpstreamText(t *testing.T) {
	got := sanitizeUpstreamText("a\x00b\x07c password= hunter2 token:abc123", []string{"hunter2"})
	if strings.Contains(got, "hunter2") || strings.Contains(got, "abc123") || strings.Contains(got, "\x00") || strings.Contains(got, "\x07") {
		t.Fatalf("%q", got)
	}
	if !strings.Contains(got, "abc") {
		t.Fatalf("over-sanitized: %q", got)
	}
	long := strings.Repeat("z", 300)
	if out := sanitizeUpstreamText(long, nil); len([]rune(out)) > 201 {
		t.Fatalf("length: %d", len([]rune(out)))
	}
	if got := sanitizeUpstreamText("", []string{"x"}); got != "" {
		t.Fatalf("%q", got)
	}
}
