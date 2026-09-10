package cklogs

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestQueryAnalysisDelivery_countOnlyUsesSizeZero(t *testing.T) {
	input := testInput()
	input.CountOnly = true
	svc, n := newService(t, func(w http.ResponseWriter, r *http.Request) {
		body := readBody(r)
		if !strings.Contains(body, `"size":0`) {
			t.Errorf("count body must use size 0: %s", body)
		}
		if strings.Contains(body, `"sort"`) {
			t.Errorf("count body must not sort: %s", body)
		}
		w.Write(hitsBodyTotal(2, nil))
	})
	out := svc.QueryAnalysisDelivery(context.Background(), input)
	if out.Status != "ok" || out.Total == nil || *out.Total != 2 {
		t.Fatalf("%+v", out)
	}
	if len(out.Entries) != 0 {
		t.Fatalf("entries=%v", out.Entries)
	}
	if atomic.LoadInt32(n) != 1 {
		t.Fatalf("calls=%d", *n)
	}
}

func TestQueryAnalysisDelivery_zeroCountSkipsDetail(t *testing.T) {
	svc, n := newService(t, func(w http.ResponseWriter, r *http.Request) {
		body := readBody(r)
		if strings.Contains(body, `"sort"`) {
			t.Errorf("zero count must not request detail: %s", body)
		}
		w.Write(hitsBodyTotal(0, nil))
	})
	out := svc.QueryAnalysisDelivery(context.Background(), testInput())
	if out.Status != "ok" || out.Total == nil || *out.Total != 0 {
		t.Fatalf("%+v", out)
	}
	if atomic.LoadInt32(n) != 1 {
		t.Fatalf("calls=%d want 1", *n)
	}
}

func TestQueryAnalysisDelivery_detailFailureKeepsCount(t *testing.T) {
	svc, n := newService(t, func(w http.ResponseWriter, r *http.Request) {
		body := readBody(r)
		if strings.Contains(body, `"size":0`) {
			w.Write(hitsBodyTotal(4, nil))
			return
		}
		w.WriteHeader(http.StatusBadGateway)
	})
	out := svc.QueryAnalysisDelivery(context.Background(), testInput())
	if out.Total == nil || *out.Total != 4 {
		t.Fatalf("want kept total=4 got %+v", out)
	}
	if len(out.Entries) != 0 {
		t.Fatalf("entries=%v", out.Entries)
	}
	joined := strings.Join(out.Limitations, " ")
	if !strings.Contains(joined, "明细") {
		t.Fatalf("limitations=%v", out.Limitations)
	}
	if atomic.LoadInt32(n) != 2 {
		t.Fatalf("calls=%d want 2", *n)
	}
}
