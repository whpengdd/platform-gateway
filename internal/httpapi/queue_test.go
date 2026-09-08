package httpapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"platform-gateway/internal/auditlog"
	"platform-gateway/internal/auth"
	"platform-gateway/internal/cklogs"
	"platform-gateway/internal/queue"
)

func TestQueueWaitAuditExcludesExecution(t *testing.T) {
	s := &Server{gate: queue.New("cklogs", 1, 1, time.Second)}
	r := httptest.NewRequest(http.MethodPost, "/v1/cklogs/message", nil)
	rec := auditlog.NewRecord(r, frozenNow(), time.UTC)
	r = r.WithContext(auditlog.WithRecord(r.Context(), rec))
	var waitMS int64
	if !s.runCklogs(httptest.NewRecorder(), r, func(ctx context.Context) error {
		waitMS = rec.QueueWaitMS
		time.Sleep(20 * time.Millisecond)
		return nil
	}) {
		t.Fatal("operation failed")
	}
	if rec.QueueWaitMS != waitMS {
		t.Fatalf("queue wait includes execution: before=%d after=%d", waitMS, rec.QueueWaitMS)
	}
}

func TestAllCKLogsRoutesShareQueue(t *testing.T) {
	requests := []struct{ path, token, body string }{
		{"/v1/cklogs/message", testToken, `{"tid":"test-tid"}`},
		{"/v1/cklogs/analysis/auth", internalToken, `{"account":"a@b.com","protocol":"pop3","countOnly":true}`},
		{"/v1/cklogs/analysis/ops", internalToken, `{"account":"a@b.com","protocol":"webmail","countOnly":true}`},
		{"/v1/cklogs/delivery", testToken, `{"direction":"outbound","account":"a@b.com","peer":"x@y.com","countOnly":true}`},
		{"/v1/cklogs/login", testToken, `{"account":"a@b.com","countOnly":true}`},
		{"/v1/cklogs/analysis/delivery", internalToken, `{"domain":"b.com","countOnly":true}`},
	}
	for _, limit := range []int{1, 3} {
		t.Run(fmt.Sprintf("concurrency_%d", limit), func(t *testing.T) {
			var active, peak, hits atomic.Int32
			started := make(chan struct{}, 64)
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := active.Add(1)
				defer active.Add(-1)
				for old := peak.Load(); n > old; old = peak.Load() {
					if peak.CompareAndSwap(old, n) {
						break
					}
				}
				hits.Add(1)
				started <- struct{}{}
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				emptyKibana().ServeHTTP(w, r)
			}))
			t.Cleanup(up.Close)
			q := queue.New("cklogs", limit, len(requests)-limit, 5*time.Second)
			ts := httptest.NewServer(New(Config{
				Auth: auth.NewWithClasses([]string{testToken}, []string{internalToken}, nil),
				CK: cklogs.NewService(&cklogs.Client{
					BaseURL: up.URL, User: "u", Pass: "p", HTTPClient: up.Client(), Timeout: 5 * time.Second,
				}),
				Gate: q, CKUser: "u", CKPass: "p", Now: frozenNow,
			}))
			t.Cleanup(ts.Close)
			t.Cleanup(unblock)
			ts.Client().Timeout = 5 * time.Second
			done := make(chan error, len(requests))
			run := func(i int) {
				go func() {
					item := requests[i]
					req, err := http.NewRequest(http.MethodPost, ts.URL+item.path, strings.NewReader(item.body))
					if err != nil {
						done <- err
						return
					}
					req.Header.Set("Authorization", "Bearer "+item.token)
					req.Header.Set("Content-Type", "application/json")
					res, err := ts.Client().Do(req)
					if err != nil {
						done <- err
						return
					}
					body, err := io.ReadAll(res.Body)
					res.Body.Close()
					if res.StatusCode != http.StatusOK {
						err = fmt.Errorf("%s: status=%d body=%s", item.path, res.StatusCode, body)
					}
					done <- err
				}()
			}
			for i := 0; i < limit; i++ {
				run(i)
			}
			for i := 0; i < limit; i++ {
				select {
				case <-started:
				case <-time.After(2 * time.Second):
					t.Fatal("requests did not reach Kibana")
				}
			}
			for i := limit; i < len(requests); i++ {
				run(i)
				waitQueueSnapshot(t, q, limit, i-limit+1)
			}
			if got := hits.Load(); got != int32(limit) {
				t.Fatalf("queued routes called Kibana: hits=%d limit=%d", got, limit)
			}
			res := doJSON(t, ts, http.MethodPost, requests[1].path, internalToken, requests[1].body)
			body := readMap(t, res)
			if res.StatusCode != http.StatusServiceUnavailable || body["code"] != "gateway_busy" || res.Header.Get("Retry-After") == "" {
				t.Fatalf("full shared queue: status=%d body=%v", res.StatusCode, body)
			}
			res = doJSON(t, ts, http.MethodGet, "/health", "", "")
			res.Body.Close()
			if res.StatusCode != http.StatusOK {
				t.Fatalf("health blocked by queue: status=%d", res.StatusCode)
			}
			unblock()
			for range requests {
				select {
				case err := <-done:
					if err != nil {
						t.Error(err)
					}
				case <-time.After(6 * time.Second):
					t.Fatal("queued request did not finish")
				}
			}
			if got := peak.Load(); got != int32(limit) {
				t.Fatalf("peak Kibana concurrency=%d, want %d", got, limit)
			}
			waitQueueSnapshot(t, q, 0, 0)
		})
	}
}

func waitQueueSnapshot(t *testing.T, q *queue.FIFO, wantActive, wantWaiting int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		active, waiting := q.Snapshot()
		if active == wantActive && waiting == wantWaiting {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("queue active=%d waiting=%d, want %d/%d", active, waiting, wantActive, wantWaiting)
		}
		time.Sleep(time.Millisecond)
	}
}
