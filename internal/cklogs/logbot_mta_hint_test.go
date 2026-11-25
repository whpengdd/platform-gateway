package cklogs

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// Kibana 路径回归：即使 MTA 兜底结果全部是 unknown，也不能出现 LogBot 退化提示。
func TestKibanaMTAFallbackHasNoLogBotHint(t *testing.T) {
	svc, _ := newService(t, func(w http.ResponseWriter, r *http.Request) {
		body := readBody(r)
		if strings.Contains(body, "datrans_distributed") {
			w.Write(hitsBody([]map[string]any{}))
			return
		}
		w.Write(hitsBody([]map[string]any{{
			"tid": "MTA-UNKNOWN", "timestamp": float64(200), "sender": "sender@example.com",
			"rcpt": []any{"peer@example.com"}, "cmd": "DATA",
		}}))
	})
	out := svc.QuerySelfServiceDelivery(context.Background(), testInput())
	if out.Status != "ok" {
		t.Fatalf("%+v", out)
	}
	if strings.Contains(strings.Join(out.Limitations, "\n"), "MTA 行不包含投递结果") {
		t.Fatalf("logbot hint leaked into kibana path: %v", out.Limitations)
	}
}

// 空白关联键（空格）视为无键：公共字段相同、内容不同的行不得在去重时折叠。
func TestDAGroupWhitespaceKeyNotCollapsed(t *testing.T) {
	mk := func(desc string) map[string]any {
		row := daGroupTestRow("local", "r1@example.test", "", "", "0", "2026-01-01 10:00:00+08:00")
		row["otherlog"].(map[string]any)["hdrmsgid"] = "   "
		if desc != "" {
			row["desc"] = desc
		}
		return row
	}
	res := daGroupTestResult([]map[string]any{mk("a"), mk("b")})
	if len(res.Entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(res.Entries))
	}
}
