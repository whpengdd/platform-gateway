package auditlog

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriterEmitsJSONLWithoutSecrets(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/cklogs/delivery", nil)
	req.RemoteAddr = "10.0.0.8:1234"
	req.Header.Set("X-Request-Id", "rid-1")
	req.Header.Set("Authorization", "Bearer super-secret-token")
	rec := NewRecord(req, time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC), Shanghai())
	rec.SetAuth("ok")
	rec.SetQuery(map[string]any{"direction": "outbound", "account": "a@example.com", "subject": "invoice"})
	rec.AddKibana(KibanaCall{Index: "datrans_distributed", DurationMS: 12, OK: true, Total: 1, Entries: 1})
	rec.SetHTTPStatus(200)
	rec.SetResult("ok", "", ptr(1), 1, nil)
	w.Emit(rec)
	w.Close()

	matches, _ := filepath.Glob(filepath.Join(dir, "platform-gateway-*.jsonl"))
	if len(matches) != 1 {
		t.Fatalf("files=%v", matches)
	}
	f, err := os.Open(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		t.Fatal("empty log")
	}
	line := sc.Text()
	if strings.Contains(line, "super-secret-token") || strings.Contains(line, "Bearer") {
		t.Fatalf("token leaked: %s", line)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatal(err)
	}
	if got["event"] != "gateway_request" || got["request_id"] != "rid-1" {
		t.Fatalf("got=%v", got)
	}
	if got["client_ip"] != "10.0.0.8" {
		t.Fatalf("ip=%v", got["client_ip"])
	}
	calls, _ := got["kibana_calls"].([]any)
	if len(calls) != 1 {
		t.Fatalf("calls=%v", got["kibana_calls"])
	}
}

func TestNewWriterEmptyDirDisabled(t *testing.T) {
	w, err := NewWriter("")
	if err != nil || w != nil {
		t.Fatalf("w=%v err=%v", w, err)
	}
}

func ptr(v int) *int { return &v }
