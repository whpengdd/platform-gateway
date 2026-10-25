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

func TestDeliveryAgentPagesBoundaries(t *testing.T) {
	for _, total := range []int{0, 1, 499, 500, 501, 529, 1000, 5000, 5001} {
		t.Run(itoa(total), func(t *testing.T) {
			svc, calls := newService(t, func(w http.ResponseWriter, r *http.Request) {
				lines := strings.Split(strings.TrimSpace(readBody(r)), "\n")
				var body map[string]any
				if err := json.Unmarshal([]byte(lines[1]), &body); err != nil {
					t.Error(err)
					return
				}
				offset := int(body["from"].(float64))
				if body["size"] != float64(HardSize) {
					t.Errorf("size=%v", body["size"])
				}
				end := offset + HardSize
				if end > total {
					end = total
				}
				sources := []map[string]any{}
				for i := offset; i < end; i++ {
					sources = append(sources, map[string]any{"tid": fmt.Sprintf("T-%d", i), "timestamp": float64(1800000000000 - i*1000), "cmd": "local", "result": "0", "mailfrom": "sender@example.com", "to": "peer@example.com"})
				}
				w.Write(hitsBodyTotal(total, sources))
			})
			result, incomplete := svc.queryDeliveryAgentPages(context.Background(), Filters{TimeRange: testRange()})
			if total > 5000 {
				if incomplete == "" || atomic.LoadInt32(calls) != 1 {
					t.Fatalf("incomplete=%q calls=%d", incomplete, *calls)
				}
				return
			}
			if !result.OK || incomplete != "" || len(result.Entries) != total {
				t.Fatalf("result=%+v incomplete=%s", result, incomplete)
			}
			expected := (total + 499) / 500
			if expected == 0 {
				expected = 1
			}
			if int(atomic.LoadInt32(calls)) != expected {
				t.Fatalf("calls=%d", *calls)
			}
		})
	}
}

func TestDeliveryAgentPagesRejectInconsistent(t *testing.T) {
	for _, mode := range []string{"short", "drift", "repeat", "tie", "error", "gte", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			svc, calls := newService(t, func(w http.ResponseWriter, r *http.Request) {
				body := readBody(r)
				second := strings.Contains(body, `"from":500`)
				if mode == "error" && second {
					w.WriteHeader(502)
					return
				}
				total := 529
				if mode == "drift" && second {
					total = 530
				}
				start, end := 0, 500
				if second {
					start, end = 500, 529
				}
				if mode == "short" && second {
					end = 528
				}
				if mode == "repeat" && second {
					start, end = 0, 29
				}
				sources := []map[string]any{}
				for i := start; i < end; i++ {
					ts := 1800000000000 - i*1000
					if mode == "tie" {
						ts = 1800000000000
					}
					sources = append(sources, map[string]any{"tid": fmt.Sprintf("T-%d", i), "timestamp": float64(ts)})
				}
				raw := hitsBodyTotal(total, sources)
				if mode == "gte" {
					raw = []byte(strings.Replace(string(raw), `"total":529`, `"total":{"value":529,"relation":"gte"}`, 1))
				}
				w.Write(raw)
				if mode == "cancel" {
					cancel()
				}
			})
			result, incomplete := svc.queryDeliveryAgentPages(ctx, Filters{TimeRange: testRange()})
			if result.OK && incomplete == "" {
				t.Fatal("inconsistent pages accepted")
			}
			if mode == "gte" && atomic.LoadInt32(calls) != 1 {
				t.Fatalf("calls=%d", *calls)
			}
		})
	}
}

func TestDeliveryAgentPagesAggregateBeforePublicPagination(t *testing.T) {
	input := testInput()
	input.Page = 2
	input.PageSize = 1
	svc, _ := newService(t, func(w http.ResponseWriter, r *http.Request) {
		second := strings.Contains(readBody(r), `"from":500`)
		start, end := 0, 500
		if second {
			start, end = 500, 529
		}
		sources := []map[string]any{}
		for i := start; i < end; i++ {
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

func TestDeliveryAgentPagesRejectOverlappingIdentities(t *testing.T) {
	svc, _ := newService(t, func(w http.ResponseWriter, r *http.Request) {
		second := strings.Contains(readBody(r), `"from":500`)
		start, count := 0, 500
		if second {
			start, count = 500, 29
		}
		hits := make([]any, 0, count)
		for i := 0; i < count; i++ {
			id := fmt.Sprintf("event-%d", start+i)
			if second && i == 0 {
				id = "event-0"
			}
			hits = append(hits, map[string]any{"_index": "logs", "_id": id, "_source": map[string]any{"timestamp": float64(1800000000000 - (start+i)*1000)}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"responses": []any{map[string]any{"hits": map[string]any{"total": 529, "hits": hits}}}})
	})
	_, incomplete := svc.queryDeliveryAgentPages(context.Background(), Filters{})
	if !strings.Contains(incomplete, "重复日志标识") {
		t.Fatalf("incomplete=%q", incomplete)
	}
}
