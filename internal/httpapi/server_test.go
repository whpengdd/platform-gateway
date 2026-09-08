package httpapi

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

const testToken = "test-token"

func frozenNow() time.Time {
	return time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
}

func setupAPI(t *testing.T, kibana http.Handler, maxConc, qsize int, wait time.Duration) (*httptest.Server, *int32) {
	t.Helper()
	var hits int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		kibana.ServeHTTP(w, r)
	}))
	t.Cleanup(up.Close)
	client := &cklogs.Client{
		BaseURL:    up.URL,
		User:       "u",
		Pass:       "p",
		Timeout:    3 * time.Second,
		HTTPClient: up.Client(),
	}
	if wait <= 0 {
		wait = time.Second
	}
	h := New(Config{
		Auth:   auth.New([]string{testToken}, nil),
		CK:     cklogs.NewService(client),
		Gate:   queue.New("cklogs", maxConc, qsize, wait),
		CKUser: "u",
		CKPass: "p",
		Now:    frozenNow,
	})
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts, &hits
}

func doJSON(t *testing.T, ts *httptest.Server, method, path, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func readMap(t *testing.T, res *http.Response) map[string]any {
	t.Helper()
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("json %s: %v", b, err)
	}
	return m
}

func emptyKibana() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"responses":[{"hits":{"total":0,"hits":[]}}]}`))
	})
}

func TestAuthMissDoesNotHitUpstream(t *testing.T) {
	ts, hits := setupAPI(t, emptyKibana(), 2, 32, time.Second)
	res := doJSON(t, ts, http.MethodPost, "/v1/cklogs/delivery", "", `{"direction":"outbound","account":"a@b.com","timeRange":{"from":"2026-08-01T00:00:00.000Z","to":"2026-08-02T00:00:00.000Z"}}`)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d", res.StatusCode)
	}
	readMap(t, res)
	if atomic.LoadInt32(hits) != 0 {
		t.Fatalf("upstream hits=%d", *hits)
	}
	res = doJSON(t, ts, http.MethodPost, "/v1/cklogs/delivery", "wrong", `{"direction":"outbound","account":"a@b.com","timeRange":{"from":"2026-08-01T00:00:00.000Z","to":"2026-08-02T00:00:00.000Z"}}`)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d", res.StatusCode)
	}
	res.Body.Close()
	if atomic.LoadInt32(hits) != 0 {
		t.Fatalf("upstream hits=%d", *hits)
	}
}

func TestUnknownJSONField400(t *testing.T) {
	ts, hits := setupAPI(t, emptyKibana(), 2, 32, time.Second)
	res := doJSON(t, ts, http.MethodPost, "/v1/cklogs/delivery", testToken, `{"direction":"outbound","account":"a@b.com","sender":"evil@x.com","timeRange":{"from":"2026-08-01T00:00:00.000Z","to":"2026-08-02T00:00:00.000Z"}}`)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d", res.StatusCode)
	}
	m := readMap(t, res)
	if m["code"] != "unknown_field" {
		t.Fatalf("%v", m)
	}
	if atomic.LoadInt32(hits) != 0 {
		t.Fatalf("hits=%d", *hits)
	}
}

func TestMissingPeer400DoesNotHitUpstream(t *testing.T) {
	ts, hits := setupAPI(t, emptyKibana(), 2, 32, time.Second)
	for _, body := range []string{
		`{"direction":"outbound","account":"a@b.com","timeRange":{"from":"2026-08-01T00:00:00.000Z","to":"2026-08-02T00:00:00.000Z"}}`,
		`{"direction":"inbound","account":"a@b.com","peer":"   ","timeRange":{"from":"2026-08-01T00:00:00.000Z","to":"2026-08-02T00:00:00.000Z"}}`,
	} {
		res := doJSON(t, ts, http.MethodPost, "/v1/cklogs/delivery", testToken, body)
		if res.StatusCode != http.StatusBadRequest {
			t.Fatalf("status=%d", res.StatusCode)
		}
		if m := readMap(t, res); m["code"] != "invalid_peer_filter" {
			t.Fatalf("%v", m)
		}
	}
	if atomic.LoadInt32(hits) != 0 {
		t.Fatalf("upstream hits=%d", *hits)
	}
}

func TestMissingAccountAndTid400(t *testing.T) {
	ts, hits := setupAPI(t, emptyKibana(), 2, 32, time.Second)
	res := doJSON(t, ts, http.MethodPost, "/v1/cklogs/delivery", testToken, `{"direction":"outbound","timeRange":{"from":"2026-08-01T00:00:00.000Z","to":"2026-08-02T00:00:00.000Z"}}`)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("delivery status=%d", res.StatusCode)
	}
	m := readMap(t, res)
	if m["code"] != "missing_account" {
		t.Fatalf("%v", m)
	}
	res = doJSON(t, ts, http.MethodPost, "/v1/cklogs/login", testToken, `{"timeRange":{"from":"2026-08-01T00:00:00.000Z","to":"2026-08-02T00:00:00.000Z"}}`)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("login status=%d", res.StatusCode)
	}
	res.Body.Close()
	res = doJSON(t, ts, http.MethodPost, "/v1/cklogs/message", testToken, `{"timeRange":{"from":"2026-08-01T00:00:00.000Z","to":"2026-08-02T00:00:00.000Z"}}`)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("message status=%d", res.StatusCode)
	}
	m = readMap(t, res)
	if m["code"] != "missing_tid" {
		t.Fatalf("%v", m)
	}
	if atomic.LoadInt32(hits) != 0 {
		t.Fatalf("hits=%d", *hits)
	}
}

func TestQueueThirdWaitsFourthBusy(t *testing.T) {
	started := make(chan struct{}, 8)
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	kibana := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-release
		w.Write([]byte(`{"responses":[{"hits":{"total":0,"hits":[]}}]}`))
	})
	ts, _ := setupAPI(t, kibana, 2, 1, 5*time.Second)
	body := `{"direction":"outbound","account":"a@b.com","timeRange":{"from":"2026-08-01T00:00:00.000Z","to":"2026-08-02T00:00:00.000Z"}}`

	var wg sync.WaitGroup
	type result struct {
		code int
		body map[string]any
	}
	run := func(ch chan result) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res := doJSON(t, ts, http.MethodPost, "/v1/cklogs/delivery", testToken, body)
			m := readMap(t, res)
			ch <- result{code: res.StatusCode, body: m}
		}()
	}
	c1 := make(chan result, 1)
	c2 := make(chan result, 1)
	c3 := make(chan result, 1)
	c4 := make(chan result, 1)
	run(c1)
	run(c2)
	deadline := time.After(2 * time.Second)
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-deadline:
			t.Fatal("in-flight requests did not reach kibana")
		}
	}
	run(c3)
	time.Sleep(50 * time.Millisecond)
	select {
	case <-started:
		t.Fatal("third request should wait, not hit kibana yet")
	default:
	}
	run(c4)
	select {
	case got := <-c4:
		if got.code != http.StatusServiceUnavailable || got.body["code"] != "gateway_busy" {
			t.Fatalf("fourth=%d %v", got.code, got.body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fourth did not return")
	}
	close(release)
	wg.Wait()
}

func TestQueueWaitTimeout(t *testing.T) {
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	kibana := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-release
		w.Write([]byte(`{"responses":[{"hits":{"total":0,"hits":[]}}]}`))
	})
	ts, hits := setupAPI(t, kibana, 1, 1, 60*time.Millisecond)
	body := `{"direction":"outbound","account":"a@b.com","timeRange":{"from":"2026-08-01T00:00:00.000Z","to":"2026-08-02T00:00:00.000Z"}}`

	firstDone := make(chan struct{})
	go func() {
		res := doJSON(t, ts, http.MethodPost, "/v1/cklogs/delivery", testToken, body)
		res.Body.Close()
		close(firstDone)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first request did not reach kibana")
	}
	hitsBefore := atomic.LoadInt32(hits)
	res := doJSON(t, ts, http.MethodPost, "/v1/cklogs/delivery", testToken, body)
	m := readMap(t, res)
	if res.StatusCode != http.StatusServiceUnavailable || m["code"] != "gateway_queue_timeout" {
		t.Fatalf("status=%d body=%v", res.StatusCode, m)
	}
	if atomic.LoadInt32(hits) != hitsBefore {
		t.Fatalf("waiter should not call kibana, hits %d -> %d", hitsBefore, atomic.LoadInt32(hits))
	}
	close(release)
	<-firstDone
}

func TestZeroHitAndTruncatedJSONHasEntriesArray(t *testing.T) {
	ts, _ := setupAPI(t, emptyKibana(), 2, 32, time.Second)
	res := doJSON(t, ts, http.MethodPost, "/v1/cklogs/delivery", testToken, `{"direction":"outbound","account":"a@b.com","timeRange":{"from":"2026-08-01T00:00:00.000Z","to":"2026-08-02T00:00:00.000Z"}}`)
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", res.StatusCode, raw)
	}
	if !strings.Contains(string(raw), `"entries":[]`) {
		t.Fatalf("0-hit JSON missing entries array: %s", raw)
	}

	trunc := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"responses":[{"hits":{"total":501,"hits":[{"_source":{"tid":"T1","timestamp":100,"mailfrom":"a@b.com","to":"x@y.com","cmd":"local","result":"0"}}]}}]}`))
	})
	ts2, _ := setupAPI(t, trunc, 2, 32, time.Second)
	res = doJSON(t, ts2, http.MethodPost, "/v1/cklogs/delivery", testToken, `{"direction":"outbound","account":"a@b.com","timeRange":{"from":"2026-08-01T00:00:00.000Z","to":"2026-08-02T00:00:00.000Z"}}`)
	raw, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(raw), `"entries":[]`) {
		t.Fatalf("truncated JSON missing entries array: %s", raw)
	}
	if !strings.Contains(string(raw), `"CK_LOGS_DA_TRUNCATED"`) {
		t.Fatalf("expected truncated code: %s", raw)
	}
}

func TestProseAccountRejected(t *testing.T) {
	ts, hits := setupAPI(t, emptyKibana(), 2, 32, time.Second)
	res := doJSON(t, ts, http.MethodPost, "/v1/cklogs/delivery", testToken, `{"direction":"outbound","account":"please check a@b.com","timeRange":{"from":"2026-08-01T00:00:00.000Z","to":"2026-08-02T00:00:00.000Z"}}`)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d", res.StatusCode)
	}
	m := readMap(t, res)
	if m["code"] != "invalid_account" {
		t.Fatalf("%v", m)
	}
	if atomic.LoadInt32(hits) != 0 {
		t.Fatalf("hits=%d", *hits)
	}
}

func TestHealthAndReady(t *testing.T) {
	ts, _ := setupAPI(t, emptyKibana(), 2, 32, time.Second)
	res, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("health=%d", res.StatusCode)
	}
	m := readMap(t, res)
	if m["status"] != "ok" {
		t.Fatalf("%v", m)
	}
	res, err = http.Get(ts.URL + "/ready")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("ready=%d", res.StatusCode)
	}
	res.Body.Close()

	h := New(Config{Auth: auth.New(nil, nil), Now: frozenNow})
	bare := httptest.NewServer(h)
	t.Cleanup(bare.Close)
	res, err = http.Get(bare.URL + "/ready")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("ready without tokens=%d", res.StatusCode)
	}
	res.Body.Close()
}

func TestDeliveryHTTPGoldenSmoke(t *testing.T) {
	kibana := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"responses":[{"hits":{"total":1,"hits":[{"_source":{"tid":"T1","mid":"M1","timestamp":100,"mailfrom":"a@b.com","to":"x@y.com","cmd":"remote","result":"0"}}]}}]}`))
	})
	ts, hits := setupAPI(t, kibana, 2, 32, time.Second)
	res := doJSON(t, ts, http.MethodPost, "/v1/cklogs/delivery", testToken, `{"direction":"outbound","account":"a@b.com","timeRange":{"from":"2026-08-01T00:00:00.000Z","to":"2026-08-02T00:00:00.000Z"}}`)
	if res.StatusCode != 200 {
		t.Fatalf("status=%d", res.StatusCode)
	}
	m := readMap(t, res)
	if m["status"] != "ok" {
		t.Fatalf("%v", m)
	}
	if atomic.LoadInt32(hits) < 1 {
		t.Fatal("expected kibana call")
	}
}

func TestLoginHTTPUnavailableNotes(t *testing.T) {
	ts, hits := setupAPI(t, emptyKibana(), 2, 32, time.Second)
	res := doJSON(t, ts, http.MethodPost, "/v1/cklogs/login", testToken, `{"account":"user@example.com","timeRange":{"from":"2026-08-01T00:00:00.000Z","to":"2026-08-02T00:00:00.000Z"}}`)
	if res.StatusCode != 200 {
		t.Fatalf("status=%d", res.StatusCode)
	}
	m := readMap(t, res)
	results, _ := m["results"].([]any)
	if len(results) != 4 {
		t.Fatalf("%v", m)
	}
	web := results[0].(map[string]any)
	imap := results[3].(map[string]any)
	if web["status"] != "not_available" {
		t.Fatalf("webmail=%v", web)
	}
	if _, ok := web["total"]; ok {
		t.Fatalf("webmail must not include total: %v", web)
	}
	if imap["status"] != "not_available" {
		t.Fatalf("imap=%v", imap)
	}
	if _, ok := imap["total"]; ok {
		t.Fatalf("imap must not include total")
	}
	if atomic.LoadInt32(hits) != 2 {
		t.Fatalf("hits=%d", *hits)
	}
}

func TestXRequestIdEcho(t *testing.T) {
	ts, _ := setupAPI(t, emptyKibana(), 2, 32, time.Second)
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/health", nil)
	req.Header.Set("X-Request-Id", "rid-1")
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.Header.Get("X-Request-Id") != "rid-1" {
		t.Fatalf("echo=%s", res.Header.Get("X-Request-Id"))
	}
}

func TestAuditJSONLRecordsRequestAndKibanaCall(t *testing.T) {
	dir := t.TempDir()
	writer, err := auditlog.NewWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	kibana := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"responses":[{"hits":{"total":1,"hits":[{"_source":{"tid":"T1","mid":"M1","timestamp":100,"mailfrom":"a@b.com","to":"x@y.com","cmd":"remote","result":"0"}}]}}]}`))
	})
	up := httptest.NewServer(kibana)
	t.Cleanup(up.Close)
	client := &cklogs.Client{BaseURL: up.URL, User: "u", Pass: "p", Timeout: 3 * time.Second, HTTPClient: up.Client()}
	h := New(Config{
		Auth:   auth.New([]string{testToken}, nil),
		CK:     cklogs.NewService(client),
		Gate:   queue.New("cklogs", 2, 32, time.Second),
		Audit:  writer,
		CKUser: "u",
		CKPass: "p",
		Now:    frozenNow,
	})
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	res := doJSON(t, ts, http.MethodPost, "/v1/cklogs/delivery", testToken, `{"direction":"outbound","account":"a@b.com","subject":"secret-subject","timeRange":{"from":"2026-08-01T00:00:00.000Z","to":"2026-08-02T00:00:00.000Z"}}`)
	if res.StatusCode != 200 {
		t.Fatalf("status=%d", res.StatusCode)
	}
	readMap(t, res)
	res = doJSON(t, ts, http.MethodPost, "/v1/cklogs/delivery", "", `{"direction":"outbound","account":"a@b.com"}`)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauth=%d", res.StatusCode)
	}
	readMap(t, res)
	writer.Close()

	matches, _ := filepath.Glob(filepath.Join(dir, "platform-gateway-*.jsonl"))
	if len(matches) != 1 {
		t.Fatalf("files=%v", matches)
	}
	f, err := os.Open(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var lines []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		raw := sc.Text()
		if strings.Contains(raw, testToken) || strings.Contains(raw, "Bearer") {
			t.Fatalf("secret leaked: %s", raw)
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(raw), &row); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, row)
	}
	if len(lines) != 2 {
		t.Fatalf("lines=%d %#v", len(lines), lines)
	}
	okRow, denyRow := lines[0], lines[1]
	if okRow["auth"] != "ok" || okRow["http_status"].(float64) != 200 {
		t.Fatalf("ok row=%v", okRow)
	}
	query, _ := okRow["query"].(map[string]any)
	if query["account"] != "a@b.com" || query["subject"] != "secret-subject" {
		t.Fatalf("query=%v", query)
	}
	calls, _ := okRow["kibana_calls"].([]any)
	if len(calls) < 1 {
		t.Fatalf("no kibana_calls in %v", okRow)
	}
	if denyRow["auth"] == "ok" {
		t.Fatalf("deny row=%v", denyRow)
	}
	denyCalls, _ := denyRow["kibana_calls"].([]any)
	if len(denyCalls) != 0 {
		t.Fatalf("unauth must not call kibana: %v", denyRow)
	}
}

const internalToken = "internal-token"

func setupClassAPI(t *testing.T, kibana http.Handler) *httptest.Server {
	t.Helper()
	up := httptest.NewServer(kibana)
	t.Cleanup(up.Close)
	client := &cklogs.Client{
		BaseURL:    up.URL,
		User:       "u",
		Pass:       "p",
		Timeout:    3 * time.Second,
		HTTPClient: up.Client(),
	}
	h := New(Config{
		Auth:         auth.NewWithClasses([]string{testToken}, []string{internalToken}, nil),
		CK:           cklogs.NewService(client),
		Gate:         queue.New("cklogs-selfservice", 2, 32, time.Second),
		AnalysisGate: queue.New("cklogs-factory", 2, 32, time.Second),
		CKUser:       "u",
		CKPass:       "p",
		Now:          frozenNow,
	})
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

func TestExternalTokenForbiddenOnAnalysis(t *testing.T) {
	ts := setupClassAPI(t, emptyKibana())
	body := `{"direction":"both","domain":"evil.example","timeRange":{"from":"2026-08-01T00:00:00.000Z","to":"2026-08-02T00:00:00.000Z"}}`
	res := doJSON(t, ts, http.MethodPost, "/v1/cklogs/analysis/delivery", testToken, body)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("status=%d", res.StatusCode)
	}
	m := readMap(t, res)
	if m["code"] != "token_scope_forbidden" {
		t.Fatalf("%v", m)
	}
}

func TestInternalTokenAnalysisDomainOnly(t *testing.T) {
	ts := setupClassAPI(t, emptyKibana())
	body := `{"direction":"both","domain":"evil.example","timeRange":{"from":"2026-08-01T00:00:00.000Z","to":"2026-08-02T00:00:00.000Z"}}`
	res := doJSON(t, ts, http.MethodPost, "/v1/cklogs/analysis/delivery", internalToken, body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", res.StatusCode)
	}
	m := readMap(t, res)
	if m["status"] != "ok" {
		t.Fatalf("%v", m)
	}
}

func TestInternalTokenCanCallSelfServiceDelivery(t *testing.T) {
	ts := setupClassAPI(t, emptyKibana())
	body := `{"direction":"outbound","account":"a@b.com","timeRange":{"from":"2026-08-01T00:00:00.000Z","to":"2026-08-02T00:00:00.000Z"}}`
	res := doJSON(t, ts, http.MethodPost, "/v1/cklogs/delivery", internalToken, body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", res.StatusCode)
	}
	readMap(t, res)
}

func TestAnalysisAuthImapNotAvailable(t *testing.T) {
	ts := setupClassAPI(t, emptyKibana())
	body := `{"account":"a@b.com","protocol":"imap","timeRange":{"from":"2026-08-01T00:00:00.000Z","to":"2026-08-02T00:00:00.000Z"}}`
	res := doJSON(t, ts, http.MethodPost, "/v1/cklogs/analysis/auth", internalToken, body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", res.StatusCode)
	}
	m := readMap(t, res)
	if m["status"] != "not_available" {
		t.Fatalf("%v", m)
	}
}

func TestAnalysisUnknownField400(t *testing.T) {
	ts := setupClassAPI(t, emptyKibana())
	body := `{"direction":"both","domain":"evil.example","eml":{"from":"x"},"timeRange":{"from":"2026-08-01T00:00:00.000Z","to":"2026-08-02T00:00:00.000Z"}}`
	res := doJSON(t, ts, http.MethodPost, "/v1/cklogs/analysis/delivery", internalToken, body)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d", res.StatusCode)
	}
	m := readMap(t, res)
	if m["code"] != "unknown_field" {
		t.Fatalf("%v", m)
	}
}
