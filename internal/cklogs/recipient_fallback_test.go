package cklogs

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestMTARecipientFallbackNeverDropsAccount(t *testing.T) {
	for _, recipient := range []string{"same@example.com", "same@other.net", `a+b.*?@example.com`} {
		t.Run(recipient, func(t *testing.T) {
			svc, calls := newService(t, func(w http.ResponseWriter, r *http.Request) {
				body := readBody(r)
				lines := strings.Split(strings.TrimSpace(body), "\n")
				var query map[string]any
				if err := json.Unmarshal([]byte(lines[1]), &query); err != nil {
					t.Fatal(err)
				}
				clauses := query["query"].(map[string]any)["bool"].(map[string]any)["filter"].([]any)
				found := false
				for _, clause := range clauses {
					if phrase, ok := clause.(map[string]any)["match_phrase"].(map[string]any); ok && phrase["rcpt"] == recipient {
						found = true
					}
				}
				if !found {
					t.Errorf("literal full-address constraint missing: %s", body)
				}
				w.Write(hitsBody(nil))
			})
			filters := Filters{Sender: "peer@example.net", Recipient: recipient, TimeRange: testRange()}
			derived := derivedQuery{Sender: filters.Sender, Recipient: recipient}
			out := svc.queryMTA(context.Background(), filters, derived, true)
			if out.Status != "error" || out.Code != "CK_LOGS_RECIPIENT_FALLBACK_UNRESOLVED" || out.Total != nil || len(out.Entries) != 0 {
				t.Fatalf("%+v", out)
			}
			if atomic.LoadInt32(calls) != 1 {
				t.Fatalf("unsafe candidate requests: %d", *calls)
			}
		})
	}
}

func TestSubjectMatchesNormalizesReplyPrefixesAndWhitespace(t *testing.T) {
	cases := []struct {
		actual, requested string
	}{
		{"Re: Re: Re: Re: P76243 NJ TO AUCKLAND, 1*20GP,  10/19完成  SO#279522525", "Re: Re: P76243 NJ TO AUCKLAND, 1*20GP, 10/19完成 SO#279522525"},
		{"回复: 回复: 项目通知", "项目通知"},
		{"  Re: Subject\tline 2 ", "subject line 2"},
	}
	for _, tc := range cases {
		if !subjectMatches(tc.actual, tc.requested) {
			t.Errorf("subjectMatches(%q, %q) = false", tc.actual, tc.requested)
		}
	}
	if subjectMatches("P76243", "AUCKLAND") {
		t.Fatal("unrelated subjects must not match")
	}
	if subjectMatches("", "wanted") {
		t.Fatal("empty actual subject must not satisfy a subject filter")
	}
	if subjectMatches("27涓撳崥鐢宠-鍗庝腑绉戞妧澶у-鑻忛潤", "27专博申请-华中科技大学-苏静") {
		t.Fatal("ambiguous legacy encoding must not be guessed")
	}
}

func TestProxyConditionsCompoundFullAddress(t *testing.T) {
	for _, value := range []string{"USER@example.com; other@example.net", "other@example.net, USER@example.com", "other@example.net; USER@example.com; third@example.net"} {
		entry := Entry{Recipients: []string{value}}
		if !matchesProxyConditions(entry, derivedQuery{Recipient: "user@example.com"}) {
			t.Fatalf("missed full address: %s", value)
		}
		for _, wrong := range []string{"user@other.net", "user@example.com.cn", "otheruser@example.com"} {
			if matchesProxyConditions(entry, derivedQuery{Recipient: wrong}) {
				t.Fatalf("matched %s in %s", wrong, value)
			}
		}
	}
}

func TestCompleteDeliveryRejectsCountDriftAndInexactTotals(t *testing.T) {
	for _, source := range []string{"mta", "proxy"} {
		for _, mode := range []string{"inexact-count", "inexact-detail", "drift"} {
			t.Run(source+"/"+mode, func(t *testing.T) {
				input := inboundInput()
				calls := 0
				svc, _ := newService(t, func(w http.ResponseWriter, r *http.Request) {
					calls++
					count := strings.Contains(readBody(r), `"size":0`)
					total := 1
					entries := []map[string]any(nil)
					if !count {
						entries = []map[string]any{{"timestamp": float64(100), "to": input.Recipient, "rcpt": input.Recipient}}
					}
					if mode == "drift" && !count {
						total = 0
						entries = nil
					}
					raw := hitsBodyTotal(total, entries)
					if (mode == "inexact-count" && count) || (mode == "inexact-detail" && !count) {
						raw = []byte(strings.Replace(string(raw), `"total":1`, `"total":{"value":1,"relation":"gte"}`, 1))
					}
					_, _ = w.Write(raw)
				})
				derived := deriveQuery(input)
				filters := Filters{Recipient: input.Recipient, TimeRange: input.TimeRange}
				var out DeliveryResult
				if source == "mta" {
					out = svc.queryMTA(context.Background(), filters, derived, true)
				} else {
					out = svc.queryProxy(context.Background(), filters, derived, 1, 500)
				}
				if out.Status != "error" || len(out.Entries) != 0 {
					t.Fatalf("incomplete success: %+v", out)
				}
				if mode == "inexact-count" && calls != 1 {
					t.Fatalf("unexpected detail request: %d", calls)
				}
			})
		}
	}
}
