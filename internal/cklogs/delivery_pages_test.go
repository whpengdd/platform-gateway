package cklogs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// The console proxy ignores `from`; full retrieval is one count plus one
// single page of size=total. The fake server models exactly that contract.
func fakeDA(t *testing.T, total int, mutate func(body map[string]any, sources []map[string]any) []map[string]any) (*Service, *int32) {
	return newService(t, func(w http.ResponseWriter, r *http.Request) {
		lines := strings.Split(strings.TrimSpace(readBody(r)), "\n")
		var body map[string]any
		if err := json.Unmarshal([]byte(lines[1]), &body); err != nil {
			t.Error(err)
			return
		}
		if body["size"] == float64(0) {
			w.Write(hitsBodyTotal(total, nil))
			return
		}
		want := total
		if total > maxDeliveryAgentEntries {
			want = maxDeliveryAgentEntries
		}
		if int(body["size"].(float64)) != want {
			t.Errorf("size=%v want=%d", body["size"], want)
		}
		if body["from"] != float64(0) {
			t.Errorf("from=%v", body["from"])
		}
		sources := make([]map[string]any, 0, total)
		for i := 0; i < total; i++ {
			sources = append(sources, map[string]any{"tid": fmt.Sprintf("T-%d", i), "timestamp": float64(1800000000000 - i*1000), "cmd": "local", "result": "0", "mailfrom": "sender@example.com", "to": "peer@example.com"})
		}
		if mutate != nil {
			sources = mutate(body, sources)
		}
		w.Write(hitsBodyTotal(total, sources))
	})
}

func TestDeliveryAgentFullFetchBoundaries(t *testing.T) {
	for _, total := range []int{0, 1, 499, 500, 501, 529, 1000, 3999, 4000, 4001} {
		t.Run(itoa(total), func(t *testing.T) {
			svc, calls := fakeDA(t, total, nil)
			result, incomplete := svc.queryDeliveryAgentPages(context.Background(), Filters{TimeRange: testRange()})
			if total > maxDeliveryAgentEntries {
				if incomplete == "" || !strings.Contains(incomplete, "护栏") || atomic.LoadInt32(calls) != 1 {
					t.Fatalf("incomplete=%q calls=%d", incomplete, *calls)
				}
				return
			}
			if !result.OK || incomplete != "" || len(result.Entries) != total {
				t.Fatalf("total=%d result=%+v incomplete=%s", total, result, incomplete)
			}
			expected := 2 // count + one full page
			if total == 0 {
				expected = 1
			}
			if int(atomic.LoadInt32(calls)) != expected {
				t.Fatalf("total=%d calls=%d want=%d", total, *calls, expected)
			}
		})
	}
}

func TestDeliveryAgentFullFetchRejectInconsistent(t *testing.T) {
	for _, mode := range []string{"short", "drift", "gte", "counterror", "detailerror"} {
		t.Run(mode, func(t *testing.T) {
			svc, calls := newService(t, func(w http.ResponseWriter, r *http.Request) {
				lines := strings.Split(strings.TrimSpace(readBody(r)), "\n")
				var body map[string]any
				if err := json.Unmarshal([]byte(lines[1]), &body); err != nil {
					t.Error(err)
					return
				}
				if body["size"] == float64(0) {
					if mode == "counterror" {
						w.WriteHeader(502)
						return
					}
					raw := hitsBodyTotal(529, nil)
					if mode == "gte" {
						raw = []byte(strings.Replace(string(raw), `"total":529`, `"total":{"value":529,"relation":"gte"}`, 1))
					}
					w.Write(raw)
					return
				}
				if mode == "detailerror" {
					w.WriteHeader(502)
					return
				}
				total := 529
				if mode == "drift" {
					total = 530
				}
				sources := make([]map[string]any, 0, 529)
				for i := 0; i < total; i++ {
					if mode == "short" && i == 528 {
						break
					}
					sources = append(sources, map[string]any{"tid": fmt.Sprintf("T-%d", i), "timestamp": float64(1800000000000 - i*1000)})
				}
				w.Write(hitsBodyTotal(530, sources))
			})
			result, incomplete := svc.queryDeliveryAgentPages(context.Background(), Filters{TimeRange: testRange()})
			if result.OK && incomplete == "" {
				t.Fatalf("mode=%s inconsistent fetch accepted", mode)
			}
			if mode == "counterror" && atomic.LoadInt32(calls) != 1 {
				t.Fatalf("counterror calls=%d", *calls)
			}
		})
	}
}

func TestDeliveryAgentFullFetchAggregateBeforePublicPagination(t *testing.T) {
	input := testInput()
	input.Page = 2
	input.PageSize = 1
	svc, _ := newService(t, func(w http.ResponseWriter, r *http.Request) {
		lines := strings.Split(strings.TrimSpace(readBody(r)), "\n")
		var body map[string]any
		_ = json.Unmarshal([]byte(lines[1]), &body)
		if body["size"] == float64(0) {
			w.Write(hitsBodyTotal(529, nil))
			return
		}
		sources := make([]map[string]any, 0, 529)
		for i := 0; i < 529; i++ {
			peer := "a@example.com"
			if i == 528 {
				peer = "b@example.com"
			}
			sources = append(sources, map[string]any{"tid": "T1", "mid": "M1", "timestamp": float64(1800000000000 - i*1000), "mailfrom": input.Sender, "to": peer, "cmd": "local", "result": "0"})
		}
		w.Write(hitsBodyTotal(529, sources))
	})
	out := svc.QuerySelfServiceDelivery(context.Background(), input)
	if out.Status != "ok" || out.Total == nil || *out.Total != 2 || len(out.Entries) != 1 || out.Entries[0].Peer != "b@example.com" {
		t.Fatalf("%+v", out)
	}
}

func TestDeliveryAgentFullFetchCancelPropagates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	svc, _ := fakeDA(t, 529, nil)
	result, incomplete := svc.queryDeliveryAgentPages(ctx, Filters{TimeRange: testRange()})
	if result.OK && incomplete == "" {
		t.Fatal("canceled context accepted")
	}
}
