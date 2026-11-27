package cklogs

import (
	"context"
	"strings"
	"testing"
	"time"
)

// LogBot 的 MTA detail 一次任务同时带回 rows/total 语义，不再需要 count 阶段。
// 三天窗口按 24h 切成 3 个 detail job；旧 count-then-detail 会是 6 个串行 job。
func TestLogBotMTAAnalysisDirectDetailSkipsCount(t *testing.T) {
	f := newLogbotFake(t)
	f.rows = []map[string]any{
		mtaRow("2026-08-01 10:00:00+08:00", "sender@example.com", "peer@example.com"),
	}
	tr := testRange()
	tr.ToMs = tr.FromMs + 3*24*time.Hour.Milliseconds()
	out := f.service().QueryAnalysisDelivery(context.Background(), DeliveryQuery{
		Direction: "outbound",
		Sender:    "sender@example.com",
		TimeRange: tr,
	})
	if out.Status != "ok" || out.Total == nil || *out.Total != 1 || len(out.Entries) != 1 {
		t.Fatalf("%+v", out)
	}
	if out.Entries[0].Sender != "sender@example.com" || len(out.Entries[0].Recipients) != 1 || out.Entries[0].Recipients[0] != "peer@example.com" {
		t.Fatalf("entry=%+v", out.Entries[0])
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.submits != 3 {
		t.Fatalf("submits=%d want 3 detail jobs (count-then-detail would be 6)", f.submits)
	}
}

func TestLogBotMTAAnalysisDirectDetailTruncatedLimitations(t *testing.T) {
	f := newLogbotFake(t)
	f.rows = []map[string]any{
		mtaRow("2026-08-01 10:00:00+08:00", "sender@example.com", "peer@example.com"),
	}
	f.truncated = true
	out := f.service().QueryAnalysisDelivery(context.Background(), DeliveryQuery{
		Direction: "outbound",
		Sender:    "sender@example.com",
		TimeRange: testRange(),
	})
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
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.submits != 1 {
		t.Fatalf("submits=%d want 1", f.submits)
	}
}

func TestLogBotMTAAnalysisDirectDetailExactZeroAndRecipientUnresolved(t *testing.T) {
	f := newLogbotFake(t)
	out := f.service().QueryAnalysisDelivery(context.Background(), DeliveryQuery{
		Direction: "outbound",
		Sender:    "sender@example.com",
		TimeRange: testRange(),
	})
	if out.Status != "ok" || out.Total == nil || *out.Total != 0 {
		t.Fatalf("zero: %+v", out)
	}
	if !strings.Contains(strings.Join(out.Limitations, "\n"), "精确命中 0 条") {
		t.Fatalf("zero lim=%v", out.Limitations)
	}

	out = f.service().QueryAnalysisDelivery(context.Background(), DeliveryQuery{
		Direction: "outbound",
		Sender:    "sender@example.com",
		Recipient: "peer@example.com",
		TimeRange: testRange(),
	})
	if out.Status != "error" || out.Code != "CK_LOGS_RECIPIENT_FALLBACK_UNRESOLVED" {
		t.Fatalf("recipient fallback: %+v", out)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.submits != 2 {
		t.Fatalf("submits=%d want 2 detail jobs total", f.submits)
	}
}

func TestLogBotRequestTimeoutDecoupledFromSingleJobTimeout(t *testing.T) {
	jobTimeout := 55 * time.Second
	requestTimeout := 180 * time.Second
	client := &LogBotClient{Timeout: jobTimeout}
	svc := NewLogBotServiceWithRequestTimeout(client, requestTimeout)
	if got := svc.OpTimeout(); got != requestTimeout {
		t.Fatalf("OpTimeout=%s want request budget %s (single job budget %s must not win)", got, requestTimeout, jobTimeout)
	}
	if got := (LogBotBackend{Client: client}).Timeout(); got != jobTimeout {
		t.Fatalf("single job timeout=%s want %s", got, jobTimeout)
	}
	if got := NewLogBotService(&LogBotClient{Timeout: time.Millisecond}).OpTimeout(); got != DefaultOpTimeout {
		t.Fatalf("legacy logbot service must not reuse single job timeout as request budget: %s", got)
	}
	if got := NewService(&Client{Timeout: 120 * time.Second}).OpTimeout(); got != 120*time.Second {
		t.Fatalf("kibana OpTimeout behavior changed: %s", got)
	}
}

// 恰好顶满 maxRows 且游标未耗尽时必须标截断；刚好取完最后一页则不是截断。
func TestLogBotFetchResultsExactMaxRowsBoundary(t *testing.T) {
	for _, tc := range []struct {
		name      string
		rowCount  int
		truncated bool
	}{
		{"next-page-remains", 101, true},
		{"exactly-complete", 100, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLogbotFake(t)
			for i := 0; i < tc.rowCount; i++ {
				f.rows = append(f.rows, mtaRow("2026-08-01 10:00:00+08:00", "sender@example.com", "peer@example.com"))
			}
			f.jobs["manual"] = map[string]any{"log_sources": []any{"MTA"}}
			res, fail := f.client().fetchResults(context.Background(), "manual", []string{"MTA"}, 100)
			if fail != nil {
				t.Fatalf("%+v", fail)
			}
			if len(res.Rows) != 100 || res.SnapshotTruncated != tc.truncated {
				t.Fatalf("rows=%d truncated=%v want %v", len(res.Rows), res.SnapshotTruncated, tc.truncated)
			}
		})
	}
}

// 多窗口合并时上游 total_count 与原始取回行数分别累加；单段截断会污染整体完整性，
// 但 analysis 路径仍透传已取回行并说明命中/取回差异。
func TestLogBotMTAMultiWindowMixedTruncationMergesUpstreamTotals(t *testing.T) {
	f := newLogbotFake(t)
	f.rows = []map[string]any{
		mtaRow("2026-08-01 10:00:00+08:00", "sender@example.com", "peer@example.com"),
	}
	f.truncatedJobs = map[string]bool{"1": true}
	tr := testRange()
	tr.ToMs = tr.FromMs + 2*24*time.Hour.Milliseconds()
	out := f.service().QueryAnalysisDelivery(context.Background(), DeliveryQuery{
		Direction: "outbound",
		Sender:    "sender@example.com",
		TimeRange: tr,
	})
	if out.Status != "ok" || out.Total == nil || *out.Total != 1 || len(out.Entries) != 1 {
		t.Fatalf("%+v", out)
	}
	if out.Truncated == nil || !*out.Truncated {
		t.Fatalf("truncated=%v", out.Truncated)
	}
	joined := strings.Join(out.Limitations, "\n")
	if !strings.Contains(joined, "上游命中 2 条") || !strings.Contains(joined, "本次取回 1 条") {
		t.Fatalf("upstream/fetched limitation missing: %v", out.Limitations)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.submits != 2 {
		t.Fatalf("submits=%d want 2 window jobs", f.submits)
	}
}

// 跨窗合计会差额相抵：窗 A 少取 5 行、窗 B 多取 5 行时总和相等。
// 完整性必须来自每个 job 的 RowsIncomplete 逐段置位并 OR 累积。
func TestLogBotMTARowsIncompleteDoesNotCancelAcrossWindows(t *testing.T) {
	makeRows := func(prefix string, n int) []map[string]any {
		rows := []map[string]any{}
		for i := 0; i < n; i++ {
			row := mtaRow("2026-08-01 10:00:00+08:00", "sender@example.com", "peer@example.com")
			row["seq"] = prefix + "-" + itoa(i)
			rows = append(rows, row)
		}
		return rows
	}
	newCompensatingFake := func() *logbotFake {
		f := newLogbotFake(t)
		f.jobRows = map[string][]map[string]any{
			"1": makeRows("a", 5),
			"2": makeRows("b", 10),
		}
		f.totalCountJobs = map[string]int{"1": 10, "2": 5}
		return f
	}
	tr := testRange()
	tr.ToMs = tr.FromMs + 2*24*time.Hour.Milliseconds()

	t.Run("require-complete-rejects", func(t *testing.T) {
		f := newCompensatingFake()
		filters := Filters{Sender: "sender@example.com", TimeRange: tr}
		out := f.service().queryMTA(context.Background(), filters, derivedQuery{Sender: "sender@example.com"}, true)
		if out.Status != "error" || out.Code != "CK_LOGS_MTA_TRUNCATED" {
			t.Fatalf("%+v", out)
		}
		if !strings.Contains(strings.Join(out.Limitations, "\n"), "部分命中行未取回") {
			t.Fatalf("limitations=%v", out.Limitations)
		}
	})

	t.Run("analysis-marks-truncated-without-hasmore", func(t *testing.T) {
		f := newCompensatingFake()
		out := f.service().QueryAnalysisDelivery(context.Background(), DeliveryQuery{
			Direction: "outbound",
			Sender:    "sender@example.com",
			TimeRange: tr,
		})
		if out.Status != "ok" || len(out.Entries) != 15 {
			t.Fatalf("%+v", out)
		}
		if out.Truncated == nil || !*out.Truncated {
			t.Fatalf("rows-incomplete must set truncated, got %v", out.Truncated)
		}
		if out.HasMore == nil || *out.HasMore {
			t.Fatalf("hasMore=%v", out.HasMore)
		}
		if !strings.Contains(strings.Join(out.Limitations, "\n"), "部分命中行未取回") {
			t.Fatalf("limitations=%v", out.Limitations)
		}
	})
}

// domain both 的本地合并也是跨 job 合并，RowsIncomplete 必须随 OR 传递。
func TestLogBotMergeDomainResultsPropagatesRowsIncomplete(t *testing.T) {
	out := (LogBotBackend{}).mergeDomainResults(
		logbotRun{result: &logbotJobResult{TotalExact: true, RowsIncomplete: true}},
		logbotRun{result: &logbotJobResult{TotalExact: true}},
		logbotSourceMTA,
		QueryOptions{},
	)
	if !out.RowsIncomplete || !out.Truncated || out.TotalExact {
		t.Fatalf("%+v", out)
	}
}

// requireComplete 需要完整明细做本地过滤/聚合：上游声明多取回少、或快照截断，
// 都不能把部分行冒充完整结果。
func TestLogBotMTARequireCompleteRejectsMissingOrTruncatedDetail(t *testing.T) {
	for _, tc := range []struct {
		name       string
		totalCount int
		truncated  bool
		wantHit    string
	}{
		{"missing-rows", 2, false, "上游命中 2 条"},
		{"snapshot-truncated", -1, true, "上游命中 1 条"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLogbotFake(t)
			f.rows = []map[string]any{
				mtaRow("2026-08-01 10:00:00+08:00", "sender@example.com", "peer@example.com"),
			}
			f.totalCount = tc.totalCount
			f.truncated = tc.truncated
			filters := Filters{Sender: "sender@example.com", TimeRange: testRange()}
			out := f.service().queryMTA(context.Background(), filters, derivedQuery{Sender: "sender@example.com"}, true)
			if out.Status != "error" || out.Code != "CK_LOGS_MTA_TRUNCATED" || len(out.Entries) != 0 {
				t.Fatalf("%+v", out)
			}
			joined := strings.Join(out.Limitations, "\n")
			if !strings.Contains(joined, tc.wantHit) || !strings.Contains(joined, "本次取回 1 条") {
				t.Fatalf("limitations=%v", out.Limitations)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.submits != 1 {
				t.Fatalf("submits=%d want 1 detail job", f.submits)
			}
		})
	}
}

// 回归 blocker 的完整链路：恰好取满行上限但上游还有下一页时，
// requireComplete 不能因为 Truncated 漏标而把部分明细当成完整结果。
func TestLogBotMTARequireCompleteRejectsExactRowLimitWithNextPage(t *testing.T) {
	f := newLogbotFake(t)
	for i := 0; i < LogBotDisplayLimit+1; i++ {
		row := mtaRow("2026-08-01 10:00:00+08:00", "sender@example.com", "peer@example.com")
		row["seq"] = itoa(i)
		f.rows = append(f.rows, row)
	}
	// total_count 可能只反映快照/上限口径而不是后续页真实行数；这里故意让它
	// 等于行上限，确保测试覆盖的是“游标未耗尽”本身，而不是上游计数差异兜底。
	f.totalCount = LogBotDisplayLimit
	filters := Filters{Sender: "sender@example.com", TimeRange: testRange()}
	out := f.service().queryMTA(context.Background(), filters, derivedQuery{Sender: "sender@example.com"}, true)
	if out.Status != "error" || out.Code != "CK_LOGS_MTA_TRUNCATED" {
		t.Fatalf("%+v", out)
	}
	joined := strings.Join(out.Limitations, "\n")
	if !strings.Contains(joined, "上游命中 1000 条") || !strings.Contains(joined, "本次取回 1000 条") {
		t.Fatalf("limitations=%v", out.Limitations)
	}
}

// 上游 total_count 按原始行计（含完全重复行）；重复行去重不代表缺行，
// requireComplete 不应误拒，但必须把命中/取回口径差异写给用户。
func TestLogBotMTARequireCompleteAllowsExactDuplicatesWithLimitation(t *testing.T) {
	row := mtaRow("2026-08-01 10:00:00+08:00", "sender@example.com", "peer@example.com")
	f := newLogbotFake(t)
	f.rows = []map[string]any{row, row}
	filters := Filters{Sender: "sender@example.com", TimeRange: testRange()}
	out := f.service().queryMTA(context.Background(), filters, derivedQuery{Sender: "sender@example.com"}, true)
	if out.Status != "ok" || out.Total == nil || *out.Total != 1 || len(out.Entries) != 1 {
		t.Fatalf("%+v", out)
	}
	if out.HasMore == nil || *out.HasMore {
		t.Fatalf("duplicate rows must not mean missing rows, hasMore=%v", out.HasMore)
	}
	joined := strings.Join(out.Limitations, "\n")
	if !strings.Contains(joined, "上游命中 2 条") || !strings.Contains(joined, "本次取回 1 条") {
		t.Fatalf("limitations=%v", out.Limitations)
	}
}

// 不完整且没取到行时，只能说明“本次未取回”，不能走精确零命中或
// recipient fallback unresolved 的定论分支。
func TestLogBotMTAIncompleteZeroDoesNotClaimExactZeroOrFallback(t *testing.T) {
	for _, tc := range []struct {
		name      string
		recipient string
	}{
		{"plain-zero", ""},
		{"recipient-zero", "peer@example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newLogbotFake(t)
			f.truncated = true
			out := f.service().QueryAnalysisDelivery(context.Background(), DeliveryQuery{
				Direction: "outbound",
				Sender:    "sender@example.com",
				Recipient: tc.recipient,
				TimeRange: testRange(),
			})
			if out.Status != "ok" || out.Code == "CK_LOGS_RECIPIENT_FALLBACK_UNRESOLVED" {
				t.Fatalf("%+v", out)
			}
			joined := strings.Join(out.Limitations, "\n")
			if strings.Contains(joined, "精确命中 0 条") {
				t.Fatalf("incomplete zero claimed exact: %v", out.Limitations)
			}
			if out.Truncated == nil || !*out.Truncated {
				t.Fatalf("truncated=%v", out.Truncated)
			}
			if out.HasMore == nil || *out.HasMore {
				t.Fatalf("logbot detail has no caller-visible continuation token, hasMore=%v", out.HasMore)
			}
		})
	}
}

func TestLogBotMTASameSenderRecipientZeroWording(t *testing.T) {
	f := newLogbotFake(t)
	out := f.service().QueryAnalysisDelivery(context.Background(), DeliveryQuery{
		Direction: "outbound",
		Sender:    "same@example.com",
		Recipient: "same@example.com",
		TimeRange: testRange(),
	})
	if out.Status != "ok" || out.Total == nil || *out.Total != 0 {
		t.Fatalf("%+v", out)
	}
	if !strings.Contains(strings.Join(out.Limitations, "\n"), "发给自己的邮件") {
		t.Fatalf("lim=%v", out.Limitations)
	}
}
