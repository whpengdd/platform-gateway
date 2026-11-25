package cklogs

import (
	"strings"
	"testing"
)

// 脱敏构造数据：字段全部虚构，不对应任何真实邮件。

func daGroupTestRow(cmd, to, hdr, cref, result, ts string) map[string]any {
	other := map[string]any{}
	if hdr != "" {
		other["hdrmsgid"] = hdr
	}
	if cref != "" {
		other["commrefid"] = cref
	}
	return map[string]any{
		"timestamp": ts,
		"mailfrom":  "sender@example.test",
		"to":        to,
		"subject":   "unit test subject",
		"cmd":       cmd,
		"result":    result,
		"otherlog":  other,
	}
}

func daGroupTestResult(rows []map[string]any) QueryResult {
	return logbotResultToQuery(&logbotJobResult{
		Data:    rows,
		Sources: map[string]logbotSourceStatus{"DA": {Status: "complete", Loaded: len(rows)}},
	}, logbotSourceDA, false, nil)
}

// 有 hdrmsgid：同一投递同一收件人的 local+remote 行合并为一条，message-id 保留。
func TestDAGroupByHdrmsgid(t *testing.T) {
	res := daGroupTestResult([]map[string]any{
		daGroupTestRow("remote", "r1@example.test", "<m1@test>", "c1", "0", "2026-01-01 10:00:00+08:00"),
		daGroupTestRow("local", "r1@example.test", "<m1@test>", "c1", "3", "2026-01-01 10:00:01+08:00"),
	})
	if len(res.Entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(res.Entries))
	}
	e := res.Entries[0]
	if e.MessageID != "<m1@test>" {
		t.Fatalf("messageId=%q", e.MessageID)
	}
	if e.Result != "3" {
		t.Fatalf("newer row should win, result=%q", e.Result)
	}
	if len(res.Limitations) != 0 {
		t.Fatalf("unexpected limitations: %v", res.Limitations)
	}
}

// 无 hdrmsgid 但有 commrefid：归入共享同一 commrefid 的 hdr 组，
// 代表行继承组的 message-id；另有纯 commrefid 组（全组无 hdr）独立合并。
func TestDAGroupByCommrefidFallback(t *testing.T) {
	res := daGroupTestResult([]map[string]any{
		// 邮件 A：hdr 行 + 同 commrefid 的无 hdr 行（同收件人）→ 合并并继承 hdr。
		daGroupTestRow("remote", "r1@example.test", "<mA@test>", "cA", "0", "2026-01-01 10:00:00+08:00"),
		daGroupTestRow("local", "r1@example.test", "", "cA", "0", "2026-01-01 10:00:02+08:00"),
		// 邮件 B：全组只有 commrefid → 按 commrefid 合并成一条。
		daGroupTestRow("remote", "r2@example.test", "", "cB", "0", "2026-01-01 10:05:00+08:00"),
		daGroupTestRow("remote", "r2@example.test", "", "cB", "0", "2026-01-01 10:05:03+08:00"),
	})
	if len(res.Entries) != 2 {
		t.Fatalf("want 2 entries, got %d: %+v", len(res.Entries), res.Entries)
	}
	var mailA *Entry
	for i := range res.Entries {
		if res.Entries[i].Recipients[0] == "r1@example.test" {
			mailA = &res.Entries[i]
		}
	}
	if mailA == nil || mailA.MessageID != "<mA@test>" || mailA.Cmd != "local" {
		t.Fatalf("mail A merged wrong: %+v", mailA)
	}
	if len(res.Limitations) != 0 {
		t.Fatalf("unexpected limitations: %v", res.Limitations)
	}
}

// 全无键：各行保留，且 limitations 提示无法归并。
func TestDAGroupNoKeysKeepsRows(t *testing.T) {
	res := daGroupTestResult([]map[string]any{
		daGroupTestRow("local", "r1@example.test", "", "", "0", "2026-01-01 10:00:00+08:00"),
		daGroupTestRow("local", "r1@example.test", "", "", "0", "2026-01-01 10:00:05+08:00"),
	})
	if len(res.Entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(res.Entries))
	}
	joined := strings.Join(res.Limitations, "\n")
	if !strings.Contains(joined, "无法归并") {
		t.Fatalf("missing unkeyed limitation: %v", res.Limitations)
	}
}

// 无键行在去重阶段：公共字段完全相同但内容不同的行不得折叠，完全一致的行仍去重。
func TestDAGroupKeylessRowsNotCollapsedByDedup(t *testing.T) {
	base := func() map[string]any {
		return daGroupTestRow("local", "r1@example.test", "", "", "0", "2026-01-01 10:00:00+08:00")
	}
	diff := base()
	diff["desc"] = "different failure detail"
	res := daGroupTestResult([]map[string]any{base(), diff, base()})
	if len(res.Entries) != 2 {
		t.Fatalf("want 2 entries (keyless differing rows kept, exact duplicate deduped), got %d", len(res.Entries))
	}
}

// 不同邮件同秒、同收件人、同主题：不同 hdrmsgid 不得误并。
func TestDAGroupDifferentMailsNotMerged(t *testing.T) {
	res := daGroupTestResult([]map[string]any{
		daGroupTestRow("local", "r1@example.test", "<mX@test>", "cX", "0", "2026-01-01 10:00:00+08:00"),
		daGroupTestRow("local", "r1@example.test", "<mY@test>", "cY", "0", "2026-01-01 10:00:00+08:00"),
		daGroupTestRow("local", "r1@example.test", "", "cZ", "0", "2026-01-01 10:00:00+08:00"),
	})
	if len(res.Entries) != 3 {
		t.Fatalf("want 3 entries, got %d", len(res.Entries))
	}
}

// 同一 hdr 不同收件人：按收件人拆分，不跨收件人合并。
func TestDAGroupKeepsPeersSeparate(t *testing.T) {
	res := daGroupTestResult([]map[string]any{
		daGroupTestRow("local", "r1@example.test", "<m1@test>", "c1", "0", "2026-01-01 10:00:00+08:00"),
		daGroupTestRow("local", "r2@example.test", "<m1@test>", "c1", "0", "2026-01-01 10:00:00+08:00"),
	})
	if len(res.Entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(res.Entries))
	}
}

// countOnly：合并后的行数作为计数基准，且非 DA 来源不受影响。
func TestDAGroupCountOnly(t *testing.T) {
	res := logbotResultToQuery(&logbotJobResult{
		Data: []map[string]any{
			daGroupTestRow("remote", "r1@example.test", "<m1@test>", "c1", "0", "2026-01-01 10:00:00+08:00"),
			daGroupTestRow("local", "r1@example.test", "<m1@test>", "c1", "0", "2026-01-01 10:00:00+08:00"),
		},
		Sources: map[string]logbotSourceStatus{"DA": {Status: "complete", Loaded: 2}},
	}, logbotSourceDA, true, nil)
	if res.Total != 1 || len(res.Entries) != 0 {
		t.Fatalf("countOnly: total=%d entries=%d", res.Total, len(res.Entries))
	}
}
