package cklogs

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testRange() TimeRange {
	from, _ := time.Parse(time.RFC3339, "2026-08-01T00:00:00.000Z")
	to, _ := time.Parse(time.RFC3339, "2026-08-02T00:00:00.000Z")
	return TimeRange{FromMs: from.UnixMilli(), ToMs: to.UnixMilli()}
}

func testInput() DeliveryQuery {
	return DeliveryQuery{
		Direction: "outbound",
		Sender:    "sender@example.com",
		TimeRange: testRange(),
	}
}

func hitsBody(sources []map[string]any) []byte {
	return hitsBodyTotal(len(sources), sources)
}

func hitsBodyTotal(total int, sources []map[string]any) []byte {
	hits := make([]any, 0, len(sources))
	for _, s := range sources {
		hits = append(hits, map[string]any{"_source": s})
	}
	b, _ := json.Marshal(map[string]any{
		"responses": []any{
			map[string]any{"hits": map[string]any{"total": total, "hits": hits}},
		},
	})
	return b
}

func newService(t *testing.T, handler http.HandlerFunc) (*Service, *int32) {
	t.Helper()
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n, 1)
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	c := &Client{
		BaseURL:    srv.URL,
		User:       "u",
		Pass:       "p",
		Index:      "mtatrans_distributed",
		Timeout:    2 * time.Second,
		HTTPClient: srv.Client(),
	}
	return NewService(c), &n
}

func readBody(r *http.Request) string {
	b, _ := io.ReadAll(r.Body)
	return string(b)
}

func TestQuerySelfServiceDeliveryLog_mainActionsAndBounceReason(t *testing.T) {
	input := testInput()
	successCodes := []string{"0", "6", "7", "8", "10", "11"}
	failureCodes := []string{"2", "3", "4", "5", "12"}
	var sources []map[string]any
	for i, result := range successCodes {
		cmd := "local"
		if i == 5 {
			cmd = "dummy"
		} else if i == 4 {
			cmd = "remote"
		}
		sources = append(sources, map[string]any{
			"tid": "S" + itoa(i), "mid": "MS" + itoa(i), "timestamp": float64(100 + i),
			"mailfrom": input.Sender, "to": "success" + itoa(i) + "@example.com", "cmd": cmd, "result": result,
		})
	}
	sources = append(sources, map[string]any{
		"tid": "P1", "mid": "MP", "timestamp": float64(200), "mailfrom": input.Sender,
		"to": "pending@example.com", "cmd": "remote", "result": "1",
	})
	for i, result := range failureCodes {
		sources = append(sources, map[string]any{
			"tid": "F" + itoa(i), "mid": "MF" + itoa(i), "timestamp": float64(300 + i),
			"mailfrom": input.Sender, "to": "failed" + itoa(i) + "@example.com", "cmd": "remote", "result": result,
		})
	}
	sources = append(sources,
		map[string]any{"tid": "F0", "mid": "MF0", "timestamp": float64(400), "mailfrom": input.Sender, "to": "failed0@example.com", "cmd": "bounce", "result": "0", "desc": "550 User not found"},
		map[string]any{"tid": "I1", "mid": "MI", "timestamp": float64(500), "mailfrom": input.Sender, "to": "ignored@example.com", "cmd": "local", "result": "9"},
		map[string]any{"tid": "X1", "mid": "MX", "timestamp": float64(600), "mailfrom": input.Sender, "to": "internal@example.com", "cmd": "scan", "result": "0"},
	)
	svc, n := newService(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(hitsBody(sources))
	})
	out := svc.QuerySelfServiceDelivery(context.Background(), input)
	if out.Status != "ok" {
		t.Fatalf("status=%s code=%s lim=%v", out.Status, out.Code, out.Limitations)
	}
	if len(out.Entries) != 13 {
		t.Fatalf("len=%d", len(out.Entries))
	}
	counts := map[string]int{}
	for _, e := range out.Entries {
		counts[e.DeliveryStatus]++
		if e.Peer == "ignored@example.com" {
			t.Fatal("ignored peer should be dropped")
		}
	}
	if counts["delivered"] != 6 || counts["pending"] != 1 || counts["failed"] != 5 || counts["unknown"] != 1 {
		t.Fatalf("counts=%v", counts)
	}
	var internal, failed0 *Entry
	for i := range out.Entries {
		if out.Entries[i].Peer == "internal@example.com" {
			internal = &out.Entries[i]
		}
		if out.Entries[i].Tid == "F0" {
			failed0 = &out.Entries[i]
		}
	}
	if internal == nil || internal.Cmd != "scan" || internal.Result != "0" || internal.DeliveryStatus != "unknown" {
		t.Fatalf("internal=%+v", internal)
	}
	if failed0 == nil || failed0.Cmd != "remote" || failed0.Result != "2" || failed0.FailureReason != "收件人地址不存在，请核对后重试" || failed0.StatusText != "收件人地址不存在，请核对后重试" {
		t.Fatalf("failed0=%+v", failed0)
	}
	if atomic.LoadInt32(n) != 1 {
		t.Fatalf("calls=%d", *n)
	}
}

func TestIsOutboundSentFolderCopy(t *testing.T) {
	tests := []struct {
		name      string
		entry     Entry
		direction string
		want      bool
	}{
		{name: "folder id", entry: Entry{Cmd: " local ", FolderID: " 3 "}, direction: "outbound", want: true},
		{name: "folder name", entry: Entry{Cmd: "LOCAL", FolderName: " 已发送 "}, direction: "outbound", want: true},
		{name: "aggregated status text", entry: Entry{Cmd: "local", StatusText: " 已投递到已发送 "}, direction: "outbound", want: true},
		{name: "requires local", entry: Entry{Cmd: "remote", FolderID: "3", FolderName: "已发送", StatusText: "已投递到已发送"}, direction: "outbound", want: false},
		{name: "inbound folder copy", entry: Entry{Cmd: "local", FolderID: "3"}, direction: "inbound", want: true},
		{name: "requires delivery direction", entry: Entry{Cmd: "local", FolderID: "3"}, direction: "both", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isOutboundSentFolderCopy(tt.entry, tt.direction); got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestQuerySelfServiceDeliveryLog_outboundListFiltersSentFolderCopyButKeepsRealSelfDelivery(t *testing.T) {
	input := testInput()
	sources := []map[string]any{
		{
			"tid": "T-copy", "mid": "MID-copy", "timestamp": float64(100),
			"mailfrom": input.Sender, "to": input.Sender, "cmd": " local ", "result": "0", "folderid": []any{" 3 "},
		},
		{
			"tid": "T-self", "mid": "MID-self", "timestamp": float64(200),
			"mailfrom": input.Sender, "to": input.Sender, "cmd": "local", "result": "0", "folderid": " 1 ",
		},
		{
			"tid": "T-nonlocal", "mid": "MID-nonlocal", "timestamp": float64(250),
			"mailfrom": input.Sender, "to": "relay@example.net", "cmd": "remote", "result": "0", "folderid": " 3 ",
		},
		{
			"tid": "T-external", "mid": "MID-external", "timestamp": float64(300),
			"mailfrom": input.Sender, "to": "recipient@example.net", "cmd": "remote", "result": "0",
		},
	}
	svc, _ := newService(t, func(w http.ResponseWriter, r *http.Request) { w.Write(hitsBody(sources)) })

	out := svc.QuerySelfServiceDelivery(context.Background(), input)

	if out.Status != "ok" || out.Total == nil || *out.Total != 3 || len(out.Entries) != 3 {
		t.Fatalf("%+v", out)
	}
	foundSelfDelivery := false
	foundNonLocal := false
	for _, entry := range out.Entries {
		if entry.Tid == "T-copy" {
			t.Fatalf("sent-folder copy leaked into outbound list: %+v", entry)
		}
		if entry.Tid == "T-self" && entry.Peer == input.Sender && entry.FolderID == "1" {
			foundSelfDelivery = true
		}
		if entry.Tid == "T-nonlocal" && entry.Cmd == "remote" && entry.FolderID == "3" && entry.FolderName == "已发送" {
			foundNonLocal = true
		}
	}
	if !foundSelfDelivery {
		t.Fatalf("real self-delivery must remain in outbound list: %+v", out.Entries)
	}
	if !foundNonLocal {
		t.Fatalf("non-local action must remain in outbound list: %+v", out.Entries)
	}
}

func TestQuerySelfServiceDeliveryLog_inboundListFiltersSentFolderCopy(t *testing.T) {
	input := testInput()
	input.Direction = "inbound"
	input.Sender = ""
	input.Recipient = "recipient@example.com"
	sources := []map[string]any{
		{
			"tid": "T-copy", "mid": "MID-copy", "timestamp": float64(100),
			"mailfrom": "sender@example.com", "to": input.Recipient, "cmd": "local", "result": "0", "folderid": "3",
		},
		{
			"tid": "T-keep", "mid": "MID-keep", "timestamp": float64(200),
			"mailfrom": "other@example.com", "to": input.Recipient, "cmd": "local", "result": "0", "folderid": "1",
		},
	}
	svc, _ := newService(t, func(w http.ResponseWriter, r *http.Request) { w.Write(hitsBody(sources)) })

	out := svc.QuerySelfServiceDelivery(context.Background(), input)

	if out.Status != "ok" || out.Total == nil || *out.Total != 1 || len(out.Entries) != 1 || out.Entries[0].Tid != "T-keep" {
		t.Fatalf("sent-folder copy must be filtered from inbound list: %+v", out)
	}
}

func TestQuerySelfServiceDeliveryLog_outboundSentFolderFallbackRunsBeforePagination(t *testing.T) {
	input := testInput()
	input.Page = 2
	input.PageSize = 1
	sources := []map[string]any{
		{
			"tid": "T-keep-old", "mid": "MID-keep-old", "timestamp": float64(100),
			"mailfrom": input.Sender, "to": "old@example.net", "cmd": "remote", "result": "0",
		},
		{
			"tid": "T-copy", "mid": "MID-copy", "timestamp": float64(200),
			"mailfrom": input.Sender, "to": input.Sender, "cmd": "local", "result": "0", "folderid": "3",
		},
		{
			"tid": "T-keep-new", "mid": "MID-keep-new", "timestamp": float64(300),
			"mailfrom": input.Sender, "to": "new@example.net", "cmd": "remote", "result": "0",
		},
	}
	svc, _ := newService(t, func(w http.ResponseWriter, r *http.Request) { w.Write(hitsBody(sources)) })

	out := svc.QuerySelfServiceDelivery(context.Background(), input)

	if out.Status != "ok" || out.Total == nil || *out.Total != 2 || len(out.Entries) != 1 {
		t.Fatalf("%+v", out)
	}
	if out.Entries[0].Tid != "T-keep-old" || out.HasMore == nil || *out.HasMore {
		t.Fatalf("fallback filter must run before pagination: %+v", out)
	}
}

func TestQuerySelfServiceDeliveryLog_mailboxNotFound(t *testing.T) {
	input := testInput()
	sources := []map[string]any{
		{"tid": "F-mailbox", "mid": "MF-mailbox", "timestamp": float64(100), "mailfrom": input.Sender, "to": "missing@example.com", "cmd": "remote", "result": "2"},
		{"tid": "F-mailbox", "mid": "MF-mailbox", "timestamp": float64(200), "mailfrom": input.Sender, "to": "missing@example.com", "cmd": "bounce", "result": "0", "desc": "550 Mailbox not found"},
	}
	svc, _ := newService(t, func(w http.ResponseWriter, r *http.Request) { w.Write(hitsBody(sources)) })
	out := svc.QuerySelfServiceDelivery(context.Background(), input)
	if len(out.Entries) != 1 {
		t.Fatalf("len=%d", len(out.Entries))
	}
	if out.Entries[0].FailureReason != "收件人地址不存在，请核对后重试" || out.Entries[0].StatusText != "收件人地址不存在，请核对后重试" {
		t.Fatalf("%+v", out.Entries[0])
	}
}

func TestQuerySelfServiceDeliveryLog_aggregateByMidThenMessageIdThenTid(t *testing.T) {
	input := testInput()
	sources := []map[string]any{
		{"tid": "T1", "mid": "MID-A", "timestamp": float64(100), "mailfrom": input.Sender, "to": "a@example.com", "cmd": "local", "result": "0", "otherlog": `{"hdrmsgid":"MSG-1"}`},
		{"tid": "T2", "mid": "MID-A", "timestamp": float64(200), "mailfrom": input.Sender, "to": "a@example.com", "cmd": "remote", "result": "0", "otherlog": `{"hdrmsgid":"MSG-2"}`},
		{"tid": "T3", "timestamp": float64(300), "mailfrom": input.Sender, "to": "b@example.com", "cmd": "local", "result": "0", "otherlog": `{"hdrmsgid":"MSG-B"}`},
		{"tid": "T4", "timestamp": float64(400), "mailfrom": input.Sender, "to": "b@example.com", "cmd": "remote", "result": "0", "otherlog": `{"hdrmsgid":"MSG-B"}`},
		{"tid": "T5", "timestamp": float64(500), "mailfrom": input.Sender, "to": "c@example.com", "cmd": "local", "result": "0"},
		{"tid": "T6", "timestamp": float64(600), "mailfrom": input.Sender, "to": "c@example.com", "cmd": "remote", "result": "0"},
	}
	svc, _ := newService(t, func(w http.ResponseWriter, r *http.Request) { w.Write(hitsBody(sources)) })
	out := svc.QuerySelfServiceDelivery(context.Background(), input)
	a, b, c := 0, 0, 0
	for _, e := range out.Entries {
		switch e.Peer {
		case "a@example.com":
			a++
		case "b@example.com":
			b++
		case "c@example.com":
			c++
		}
	}
	if a != 1 || b != 1 || c != 2 {
		t.Fatalf("a=%d b=%d c=%d total=%v", a, b, c, *out.Total)
	}
	if *out.Total != 4 {
		t.Fatalf("total=%d", *out.Total)
	}
}

func TestQuerySelfServiceDeliveryLog_preferValidStatusThenNewer(t *testing.T) {
	input := testInput()
	sources := []map[string]any{
		{"tid": "T-valid", "mid": "MID-VALID", "timestamp": float64(100), "mailfrom": input.Sender, "to": "valid@example.com", "cmd": "local", "result": "2"},
		{"tid": "T-unknown-new", "mid": "MID-VALID", "timestamp": float64(300), "mailfrom": input.Sender, "to": "valid@example.com", "cmd": "remote", "result": "unexpected"},
		{"tid": "T-old", "mid": "MID-EQUIVALENT", "timestamp": float64(200), "mailfrom": input.Sender, "to": "equivalent@example.com", "cmd": "local", "result": "0"},
		{"tid": "T-new", "mid": "MID-EQUIVALENT", "timestamp": float64(400), "mailfrom": input.Sender, "to": "equivalent@example.com", "cmd": "remote", "result": "2"},
	}
	svc, n := newService(t, func(w http.ResponseWriter, r *http.Request) { w.Write(hitsBody(sources)) })
	out := svc.QuerySelfServiceDelivery(context.Background(), input)
	var valid, eq *Entry
	for i := range out.Entries {
		if out.Entries[i].Peer == "valid@example.com" {
			valid = &out.Entries[i]
		}
		if out.Entries[i].Peer == "equivalent@example.com" {
			eq = &out.Entries[i]
		}
	}
	if valid == nil || valid.Tid != "T-valid" || valid.Cmd != "local" || valid.Result != "2" || valid.DeliveryStatus != "failed" {
		t.Fatalf("valid=%+v", valid)
	}
	if eq == nil || eq.Tid != "T-new" || eq.Cmd != "remote" || eq.Result != "2" || eq.DeliveryStatus != "failed" {
		t.Fatalf("eq=%+v", eq)
	}
	if atomic.LoadInt32(n) != 1 {
		t.Fatalf("calls=%d", *n)
	}
}

func TestQuerySelfServiceDeliveryLog_mtaFallbackWhenDAZero(t *testing.T) {
	input := testInput()
	input.Subject = "关键主题"
	svc, n := newService(t, func(w http.ResponseWriter, r *http.Request) {
		body := readBody(r)
		if strings.Contains(body, "datrans_distributed") {
			w.Write(hitsBody(nil))
			return
		}
		if strings.Contains(body, `"size":0`) {
			w.Write(hitsBody([]map[string]any{{}}))
			return
		}
		if !strings.Contains(body, `"sender":"sender@example.com"`) || !strings.Contains(body, `"subject":"关键主题"`) {
			t.Errorf("unexpected mta body %s", body)
		}
		w.Write(hitsBody([]map[string]any{{
			"tid": "MTA1", "timestamp": float64(200), "sender": input.Sender,
			"rcpt": []any{"target@example.com"}, "subject": "关键主题", "cmd": "RCPT", "result": "550 rejected",
		}}))
	})
	out := svc.QuerySelfServiceDelivery(context.Background(), input)
	if out.Status != "ok" || out.Total == nil || *out.Total != 1 {
		t.Fatalf("%+v", out)
	}
	if out.Entries[0].StatusSource != "mta" || out.Entries[0].DeliveryStatus != "failed" || out.Entries[0].Tid != "MTA1" {
		t.Fatalf("%+v", out.Entries[0])
	}
	if !strings.Contains(strings.Join(out.Limitations, "\n"), "DATRANS 聚合后零命中") {
		t.Fatalf("lim=%v", out.Limitations)
	}
	if atomic.LoadInt32(n) != 3 {
		t.Fatalf("calls=%d", *n)
	}
}

func TestQuerySelfServiceDeliveryLog_mtaFallbackWhenDARawHitsHaveNoEffectiveRows(t *testing.T) {
	input := testInput()
	input.Subject = "关键主题"
	svc, n := newService(t, func(w http.ResponseWriter, r *http.Request) {
		body := readBody(r)
		if strings.Contains(body, "datrans_distributed") {
			w.Write(hitsBody([]map[string]any{{
				"tid": "IRRELEVANT", "timestamp": float64(100), "mailfrom": input.Sender,
				"to": "target@example.com", "subject": "其他主题", "cmd": "local", "result": "0",
			}}))
			return
		}
		if strings.Contains(body, `"size":0`) {
			w.Write(hitsBody([]map[string]any{{}}))
			return
		}
		w.Write(hitsBody([]map[string]any{{
			"tid": "MTA-EFFECTIVE", "timestamp": float64(200), "sender": input.Sender,
			"rcpt": []any{"target@example.com"}, "subject": "关键主题", "cmd": "RCPT", "result": "550 rejected",
		}}))
	})
	out := svc.QuerySelfServiceDelivery(context.Background(), input)
	if out.Status != "ok" || out.Total == nil || *out.Total != 1 {
		t.Fatalf("%+v", out)
	}
	if out.Entries[0].StatusSource != "mta" || out.Entries[0].DeliveryStatus != "failed" || out.Entries[0].Tid != "MTA-EFFECTIVE" {
		t.Fatalf("%+v", out.Entries[0])
	}
	if atomic.LoadInt32(n) != 3 {
		t.Fatalf("calls=%d", *n)
	}
}

func TestQuerySelfServiceDeliveryLog_bounceOnlyKeepsUnknown(t *testing.T) {
	input := testInput()
	svc, n := newService(t, func(w http.ResponseWriter, r *http.Request) {
		body := readBody(r)
		if !strings.Contains(body, "datrans_distributed") {
			t.Errorf("unexpected body %s", body)
		}
		w.Write(hitsBody([]map[string]any{{
			"tid": "BOUNCE-ONLY", "timestamp": float64(100), "mailfrom": input.Sender,
			"to": "target@example.com", "cmd": "bounce", "result": "0", "desc": "550 rejected",
		}}))
	})
	out := svc.QuerySelfServiceDelivery(context.Background(), input)
	if out.Status != "ok" || *out.Total != 1 {
		t.Fatalf("%+v", out)
	}
	e := out.Entries[0]
	if e.StatusSource != "da" || e.DeliveryStatus != "unknown" || e.Cmd != "bounce" || e.Result != "0" {
		t.Fatalf("%+v", e)
	}
	if atomic.LoadInt32(n) != 1 {
		t.Fatalf("calls=%d", *n)
	}
}

func TestQuerySelfServiceDeliveryLog_scanOnlyKeepsUnknown(t *testing.T) {
	input := testInput()
	svc, n := newService(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(hitsBody([]map[string]any{{
			"tid": "SCAN-ONLY", "timestamp": float64(100), "mailfrom": input.Sender,
			"to": "target@example.com", "cmd": "scan", "result": "0",
		}}))
	})
	out := svc.QuerySelfServiceDelivery(context.Background(), input)
	e := out.Entries[0]
	if e.StatusSource != "da" || e.DeliveryStatus != "unknown" || e.Cmd != "scan" || e.Result != "0" {
		t.Fatalf("%+v", e)
	}
	if atomic.LoadInt32(n) != 1 {
		t.Fatalf("calls=%d", *n)
	}
}

func TestQuerySelfServiceDeliveryLog_unknownMainActionLooksUpTid(t *testing.T) {
	input := testInput()
	svc, n := newService(t, func(w http.ResponseWriter, r *http.Request) {
		body := readBody(r)
		if strings.Contains(body, "datrans_distributed") {
			w.Write(hitsBody([]map[string]any{{
				"tid": "UNKNOWN-TID", "mid": "MID-U", "timestamp": float64(100), "mailfrom": input.Sender,
				"to": "u@example.com", "cmd": "local", "result": "unexpected",
			}}))
			return
		}
		if !strings.Contains(body, `"tid":"UNKNOWN-TID"`) {
			t.Errorf("missing tid in %s", body)
		}
		if strings.Contains(body, `"size":0`) {
			w.Write(hitsBody([]map[string]any{{}}))
			return
		}
		w.Write(hitsBody([]map[string]any{{
			"tid": "UNKNOWN-TID", "timestamp": float64(200), "sender": input.Sender,
			"rcpt": []any{"u@example.com"}, "cmd": "RCPT", "result": "550 User not found",
		}}))
	})
	out := svc.QuerySelfServiceDelivery(context.Background(), input)
	if out.Entries[0].StatusSource != "mta" || out.Entries[0].DeliveryStatus != "failed" || out.Entries[0].Tid != "UNKNOWN-TID" {
		t.Fatalf("%+v", out.Entries[0])
	}
	if atomic.LoadInt32(n) != 3 {
		t.Fatalf("calls=%d", *n)
	}
}

func TestQuerySelfServiceDeliveryLog_multiTidKeepsUnknown(t *testing.T) {
	input := testInput()
	svc, n := newService(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(hitsBody([]map[string]any{
			{"tid": "T-MID-1", "mid": "MID-SAME", "timestamp": float64(100), "mailfrom": input.Sender, "to": "same@example.com", "cmd": "local", "result": "unexpected"},
			{"tid": "T-MID-2", "mid": "MID-SAME", "timestamp": float64(200), "mailfrom": input.Sender, "to": "same@example.com", "cmd": "remote", "result": "unexpected"},
			{"tid": "T-MSG-1", "timestamp": float64(300), "mailfrom": input.Sender, "to": "msg@example.com", "cmd": "local", "result": "unexpected", "otherlog": `{"hdrmsgid":"MSG-SAME"}`},
			{"tid": "T-MSG-2", "timestamp": float64(400), "mailfrom": input.Sender, "to": "msg@example.com", "cmd": "remote", "result": "unexpected", "otherlog": `{"hdrmsgid":"MSG-SAME"}`},
		}))
	})
	out := svc.QuerySelfServiceDelivery(context.Background(), input)
	if out.Status != "ok" || *out.Total != 2 {
		t.Fatalf("%+v", out)
	}
	foundSame, foundMsg := false, false
	for _, e := range out.Entries {
		if e.Peer == "same@example.com" && e.Tid == "T-MID-2" && e.StatusSource == "da" && e.DeliveryStatus == "unknown" {
			foundSame = true
		}
		if e.Peer == "msg@example.com" && e.Tid == "T-MSG-2" && e.StatusSource == "da" && e.DeliveryStatus == "unknown" {
			foundMsg = true
		}
	}
	if !foundSame || !foundMsg {
		t.Fatalf("%+v", out.Entries)
	}
	if atomic.LoadInt32(n) != 1 {
		t.Fatalf("calls=%d", *n)
	}
}

func TestQuerySelfServiceDeliveryLog_distinctPeersLookupSeparately(t *testing.T) {
	input := testInput()
	svc, n := newService(t, func(w http.ResponseWriter, r *http.Request) {
		body := readBody(r)
		if strings.Contains(body, "datrans_distributed") {
			w.Write(hitsBody([]map[string]any{
				{"tid": "T-PEER-X", "mid": "MID-GROUP", "timestamp": float64(100), "mailfrom": input.Sender, "to": "x@example.com", "cmd": "local", "result": "unexpected"},
				{"tid": "T-PEER-Y", "mid": "MID-GROUP", "timestamp": float64(200), "mailfrom": input.Sender, "to": "y@example.com", "cmd": "remote", "result": "unexpected"},
			}))
			return
		}
		if strings.Contains(body, `"size":0`) {
			w.Write(hitsBody([]map[string]any{{}}))
			return
		}
		tid := "T-PEER-Y"
		peer := "y@example.com"
		if strings.Contains(body, `"tid":"T-PEER-X"`) {
			tid = "T-PEER-X"
			peer = "x@example.com"
		}
		w.Write(hitsBody([]map[string]any{{
			"tid": tid, "timestamp": float64(300), "sender": input.Sender, "rcpt": []any{peer},
			"cmd": "RCPT", "result": "550 User not found",
		}}))
	})
	out := svc.QuerySelfServiceDelivery(context.Background(), input)
	if len(out.Entries) != 2 {
		t.Fatalf("len=%d %+v", len(out.Entries), out)
	}
	for _, e := range out.Entries {
		if e.StatusSource != "mta" || e.DeliveryStatus != "failed" {
			t.Fatalf("%+v", e)
		}
	}
	if atomic.LoadInt32(n) != 5 {
		t.Fatalf("calls=%d", *n)
	}
}

func TestQuerySelfServiceDeliveryLog_mtaLookupErrorPropagates(t *testing.T) {
	input := testInput()
	svc, n := newService(t, func(w http.ResponseWriter, r *http.Request) {
		body := readBody(r)
		if strings.Contains(body, "datrans_distributed") {
			w.Write(hitsBody([]map[string]any{{
				"tid": "UNKNOWN-ERROR", "timestamp": float64(100), "mailfrom": input.Sender,
				"to": "u@example.com", "cmd": "local", "result": "unexpected",
			}}))
			return
		}
		w.WriteHeader(500)
	})
	out := svc.QuerySelfServiceDelivery(context.Background(), input)
	if out.Status != "error" || out.Code != "CK_LOGS_UPSTREAM_ERROR" {
		t.Fatalf("%+v", out)
	}
	if out.Total != nil {
		t.Fatalf("error should omit total: %+v", out)
	}
	if out.Entries == nil || len(out.Entries) != 0 {
		t.Fatalf("error entries should be empty slice: %+v", out)
	}
	if atomic.LoadInt32(n) != 2 {
		t.Fatalf("calls=%d", *n)
	}
}

func TestQuerySelfServiceDeliveryLog_unknownTidCapAddsLimitation(t *testing.T) {
	input := testInput()
	var sources []map[string]any
	for i := 0; i < 9; i++ {
		sources = append(sources, map[string]any{
			"tid": "U" + itoa(i), "mid": "MU" + itoa(i), "timestamp": float64(100 + i),
			"mailfrom": input.Sender, "to": "u" + itoa(i) + "@example.com",
			"cmd": "local", "result": "unexpected",
		})
	}
	svc, n := newService(t, func(w http.ResponseWriter, r *http.Request) {
		body := readBody(r)
		if strings.Contains(body, "datrans_distributed") {
			w.Write(hitsBody(sources))
			return
		}
		w.Write(hitsBody(nil))
	})
	out := svc.QuerySelfServiceDelivery(context.Background(), input)
	if out.Status != "ok" || out.Total == nil || *out.Total != 9 {
		t.Fatalf("%+v", out)
	}
	for _, e := range out.Entries {
		if e.DeliveryStatus != "unknown" {
			t.Fatalf("expected unknown after cap, got %+v", e)
		}
	}
	joined := strings.Join(out.Limitations, "\n")
	if !strings.Contains(joined, "MTA 回查已达上限") || !strings.Contains(joined, "不能据此断定投递结果") {
		t.Fatalf("lim=%v", out.Limitations)
	}
	if atomic.LoadInt32(n) != 1+8 {
		t.Fatalf("calls=%d want 1 DATRANS + 8 MTA counts", *n)
	}
}

func TestQuerySelfServiceDeliveryLog_unknownTidTimeoutAddsLimitation(t *testing.T) {
	input := testInput()
	svc, n := newService(t, func(w http.ResponseWriter, r *http.Request) {
		body := readBody(r)
		if strings.Contains(body, "datrans_distributed") {
			w.Write(hitsBody([]map[string]any{
				{"tid": "U-A", "mid": "MA", "timestamp": float64(100), "mailfrom": input.Sender, "to": "a@example.com", "cmd": "local", "result": "unexpected"},
				{"tid": "U-B", "mid": "MB", "timestamp": float64(200), "mailfrom": input.Sender, "to": "b@example.com", "cmd": "local", "result": "unexpected"},
			}))
			return
		}
		time.Sleep(200 * time.Millisecond)
	})
	svc.Client.Timeout = 30 * time.Millisecond
	out := svc.QuerySelfServiceDelivery(context.Background(), input)
	if out.Status != "ok" {
		t.Fatalf("timeout on tid lookup must not fail DATRANS: %+v", out)
	}
	if out.Total == nil || *out.Total != 2 {
		t.Fatalf("%+v", out)
	}
	for _, e := range out.Entries {
		if e.DeliveryStatus != "unknown" {
			t.Fatalf("%+v", e)
		}
	}
	joined := strings.Join(out.Limitations, "\n")
	if !strings.Contains(joined, "MTA 回查超时") || !strings.Contains(joined, "不能据此断定投递结果") {
		t.Fatalf("lim=%v", out.Limitations)
	}
	if atomic.LoadInt32(n) != 2 {
		t.Fatalf("calls=%d want DATRANS + first MTA", *n)
	}
}

func TestQueryCancelIsNetworkErrorNotTimeout(t *testing.T) {
	svc, _ := newService(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := svc.QuerySelfServiceDelivery(ctx, testInput())
	if out.Code != "CK_LOGS_NETWORK_ERROR" {
		t.Fatalf("canceled request should not look like timeout: %+v", out)
	}
}

func TestQuerySelfServiceDeliveryLog_datransTimeoutMessage(t *testing.T) {
	input := testInput()
	svc, _ := newService(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	})
	svc.Client.Timeout = 30 * time.Millisecond
	out := svc.QuerySelfServiceDelivery(context.Background(), input)
	if out.Status != "error" || out.Code != "CK_LOGS_TIMEOUT" {
		t.Fatalf("%+v", out)
	}
	if len(out.Limitations) == 0 || out.Limitations[0] != "日志查询超过60秒，请缩小查询范围或增加查询条件后重试" {
		t.Fatalf("lim=%v", out.Limitations)
	}
}

func TestQuerySelfServiceDeliveryLog_subjectAndDomainAreLocalFilters(t *testing.T) {
	input := testInput()
	input.RecipientDomain = "example.net"
	input.Subject = "wells"
	svc, n := newService(t, func(w http.ResponseWriter, r *http.Request) {
		body := readBody(r)
		if !strings.Contains(body, "datrans_distributed") {
			t.Errorf("want datrans, got %s", body)
		}
		if !strings.Contains(body, `"mailfrom":"sender@example.com"`) {
			t.Errorf("missing mailfrom: %s", body)
		}
		if strings.Contains(body, `"subject"`) || strings.Contains(body, `"example.net"`) {
			t.Errorf("subject/domain leaked into DATRANS: %s", body)
		}
		w.Write(hitsBody([]map[string]any{
			{"tid": "T1", "mid": "M1", "timestamp": float64(100), "mailfrom": input.Sender, "to": "a@example.net", "subject": "Wells notice", "cmd": "remote", "result": "0"},
			{"tid": "T2", "mid": "M2", "timestamp": float64(200), "mailfrom": input.Sender, "to": "b@example.org", "subject": "Wells notice", "cmd": "remote", "result": "0"},
			{"tid": "T3", "mid": "M3", "timestamp": float64(300), "mailfrom": input.Sender, "to": "c@example.net", "subject": "Other", "cmd": "remote", "result": "0"},
		}))
	})
	out := svc.QuerySelfServiceDelivery(context.Background(), input)
	if out.Status != "ok" || len(out.Entries) != 1 {
		t.Fatalf("%+v", out)
	}
	if out.Entries[0].Peer != "a@example.net" || out.Entries[0].Subject != "Wells notice" {
		t.Fatalf("%+v", out.Entries[0])
	}
	if atomic.LoadInt32(n) != 1 {
		t.Fatalf("calls=%d", *n)
	}
}

func TestQuerySelfServiceDeliveryLog_datransTruncated(t *testing.T) {
	input := testInput()
	svc, _ := newService(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(hitsBodyTotal(501, []map[string]any{
			{"tid": "T1", "timestamp": float64(100), "mailfrom": input.Sender, "to": "a@example.com", "cmd": "local", "result": "0"},
		}))
	})
	out := svc.QuerySelfServiceDelivery(context.Background(), input)
	if out.Status != "error" || out.Code != "CK_LOGS_DA_TRUNCATED" {
		t.Fatalf("%+v", out)
	}
	if out.Entries == nil || len(out.Entries) != 0 {
		t.Fatalf("entries=%v", out.Entries)
	}
	if out.Truncated == nil || !*out.Truncated {
		t.Fatal("expected truncated")
	}
}

func TestQueryLogin_unavailableDatasetsHaveNoTotal(t *testing.T) {
	svc, n := newService(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(hitsBody(nil))
	})
	out := svc.QueryLogin(context.Background(), "user@example.com", "", testRange(), false)
	if len(out.Results) != 4 {
		t.Fatalf("len=%d", len(out.Results))
	}
	wantOrder := []string{"auth_webmail", "auth_pop3", "auth_smtp", "auth_imap"}
	for i, ds := range wantOrder {
		if out.Results[i].Dataset != ds {
			t.Fatalf("results[%d]=%s", i, out.Results[i].Dataset)
		}
	}
	if out.Results[0].Status != "not_available" || out.Results[0].Total != nil {
		t.Fatalf("webmail=%+v", out.Results[0])
	}
	if !strings.Contains(out.Results[0].Limitations[0], "Webmail 登录记录不可按账号检索") {
		t.Fatalf("webmail note=%v", out.Results[0].Limitations)
	}
	if out.Results[3].Status != "not_available" || out.Results[3].Total != nil {
		t.Fatalf("imap=%+v", out.Results[3])
	}
	if !strings.Contains(out.Results[3].Limitations[0], "There is no table imaptrans") {
		t.Fatalf("imap note=%v", out.Results[3].Limitations)
	}
	if atomic.LoadInt32(n) != 2 {
		t.Fatalf("kibana calls=%d want 2 (pop3+smtp count)", *n)
	}
}
