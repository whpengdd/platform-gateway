package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"platform-gateway/internal/httpapi"
	"platform-gateway/internal/queue"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSlowDownloadResponseHasRealWriteDeadline(t *testing.T) {
	var s *Server
	upstreamDone := make(chan struct{})
	finished := make(chan struct{})
	s, a := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/secure/attachment/") {
			w.Write(bytes.Repeat([]byte("x"), 8<<20))
			close(upstreamDone)
			return
		}
		i := mockIssue()
		object(i["fields"])["attachment"] = []any{map[string]any{"id": "30", "filename": "a.txt", "size": 8 << 20, "mimeType": "text/plain", "content": s.client.base.String() + "/secure/attachment/30/a.txt"}}
		json.NewEncoder(w).Encode(map[string]any{"issues": []any{i}, "total": 1})
	})
	if s.responseWriteTimeout != 30*time.Second {
		t.Fatal("default response deadline changed")
	}
	s.responseWriteTimeout = 250 * time.Millisecond
	handler := httpapi.New(httpapi.Config{Auth: a, Jira: s})
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { defer close(finished); handler.ServeHTTP(w, r) }))
	defer down.Close()
	conn, err := net.Dial("tcp", down.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.(*net.TCPConn).SetReadBuffer(1024)
	io.WriteString(conn, "GET /v1/jira/projects/CS/issues/CS-1/attachments/30/content HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer jira-a\r\n\r\n")
	select {
	case <-upstreamDone:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream did not complete")
	}
	// Do not read or close the client: the server must finish on its own deadline.
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		conn.Close()
		t.Fatal("response not bounded by write deadline")
	}
	active, _ := s.gate.(*queue.FIFO).Snapshot()
	if active != 0 {
		t.Fatal("upstream slot leaked")
	}
}

func multipartBody(t *testing.T, epilogue int) ([]byte, string) {
	t.Helper()
	var b bytes.Buffer
	m := multipart.NewWriter(&b)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"; filename="a.txt"`)
	h.Set("Content-Type", "text/plain")
	part, err := m.CreatePart(h)
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(part, "hello")
	m.Close()
	b.Write(bytes.Repeat([]byte("x"), epilogue))
	return b.Bytes(), m.FormDataContentType()
}
func TestMultipartEntireHTTPBodyBeforeWrite(t *testing.T) {
	for _, chunked := range []bool{false, true} {
		for _, epilogue := range []int{0, 1024, 12 << 20} {
			t.Run(fmt.Sprintf("chunked=%t/epilogue=%d", chunked, epilogue), func(t *testing.T) {
				var writes atomic.Int32
				s, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
					if strings.HasSuffix(r.URL.Path, "/attachments") {
						writes.Add(1)
						io.WriteString(w, `[{"id":"30","filename":"a.txt","size":5}]`)
						return
					}
					if strings.HasSuffix(r.URL.Path, "/issue/1") {
						io.WriteString(w, `{"id":"1","fields":{"project":{"key":"CS"},"attachment":[{"id":"30"}]}}`)
						return
					}
					baseline(w, r)
				})
				b, media := multipartBody(t, epilogue)
				down := httptest.NewServer(s)
				defer down.Close()
				req, _ := http.NewRequest("POST", down.URL+root+"/issues/CS-1/attachments", bytes.NewReader(b))
				if chunked {
					req.ContentLength = -1
				}
				req.Header.Set("Content-Type", media)
				req.Header.Set("Authorization", "Bearer jira-a")
				client := down.Client()
				client.Timeout = 5 * time.Second
				resp, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				want, expectedWrites := 201, int32(1)
				if epilogue > 11<<20 {
					want = 413
					expectedWrites = 0
				}
				if resp.StatusCode != want || writes.Load() != expectedWrites {
					t.Fatalf("status=%d writes=%d", resp.StatusCode, writes.Load())
				}
			})
		}
	}
}

func TestNumericDefaultConflictAndUpstreamPrecision(t *testing.T) {
	var writes atomic.Int32
	s, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/createmeta") {
			m := mockMetadata()
			projects := object(m)["projects"].([]any)
			types := object(projects[0])["issuetypes"].([]any)
			fields := object(object(types[0])["fields"])
			object(fields["customfield_1"])["schema"] = map[string]string{"type": "number"}
			json.NewEncoder(w).Encode(m)
			return
		}
		if r.URL.Path == "/jira/rest/api/2/issue" {
			writes.Add(1)
			b, _ := io.ReadAll(r.Body)
			if !bytes.Contains(b, []byte(`"customfield_1":9007199254740992`)) {
				t.Error("default changed")
			}
		}
		if r.URL.Path == "/jira/rest/api/2/issue/1" {
			i := mockIssue()
			object(i["fields"])["customfield_1"] = float64(9007199254740992)
			json.NewEncoder(w).Encode(i)
			return
		}
		baseline(w, r)
	})
	s.projects["CS"].CreateDefaults["customfield_1"] = float64(9007199254740992)
	for _, tt := range []struct{ value, code string }{{"9007199254740993", "invalid_body"}, {"9007199254740994", "create_default_conflict"}} {
		w := call(s, "POST", root+"/issues", "jira-a", `{"fields":{"summary":"x","customfield_1":`+tt.value+`}}`)
		if w.Code != 400 || !strings.Contains(w.Body.String(), tt.code) || writes.Load() != 0 {
			t.Fatal(w.Code, w.Body.String(), writes.Load())
		}
	}
	w := call(s, "POST", root+"/issues", "jira-a", `{"fields":{"summary":"x","customfield_1":9007199254740992.0}}`)
	if w.Code != 201 || writes.Load() != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"value":9007199254740993}`) })
	for _, write := range []bool{false, true} {
		var out map[string]any
		err := c.JSON(context.Background(), "POST", "issue", nil, map[string]string{"summary": "x"}, &out, write)
		want := "upstream_unavailable"
		if write {
			want = "outcome_unknown"
		}
		if err == nil || err.Error() != want {
			t.Fatal(err)
		}
	}
}

func TestSearchTimePresence(t *testing.T) {
	var searches atomic.Int32
	s, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/search") {
			searches.Add(1)
		}
		baseline(w, r)
	})
	if err := s.validate(context.Background(), "CS", s.projects["CS"]); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"updatedFrom":""}`, `{"updatedTo":""}`, `{"updatedFrom":"","updatedTo":""}`, `{"updatedFrom":"","updatedTo":"2026-09-14T00:00:00Z"}`, `{"updatedFrom":"2026-09-14T00:00:00Z"}`, `{"updatedFrom":null,"updatedTo":null}`} {
		before := searches.Load()
		w := call(s, "POST", root+"/issues/search", "jira-a", body)
		if w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
		if searches.Load() != before {
			t.Fatal("invalid time caused a business search")
		}
	}
	before := searches.Load()
	w := call(s, "POST", root+"/issues/search", "jira-a", `{"updatedFrom":"2026-09-13T00:00:00Z","updatedTo":"2026-09-14T00:00:00Z"}`)
	if w.Code != 200 || searches.Load() != before+1 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestProjectGrantsComplexFilterAndSearchContinuation(t *testing.T) {
	var mu sync.Mutex
	var queries []string
	var offsets []int
	var limits []int
	s, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/search") {
			var req struct {
				JQL   string `json:"jql"`
				Start int    `json:"startAt"`
				Limit int    `json:"maxResults"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			i := mockIssue()
			if strings.Contains(req.JQL, `project = "IT"`) {
				i["key"] = "IT-1"
				object(object(i["fields"])["project"])["key"] = "IT"
			}
			total := 1
			if strings.Contains(req.JQL, "ORDER BY") {
				mu.Lock()
				queries = append(queries, req.JQL)
				offsets = append(offsets, req.Start)
				limits = append(limits, req.Limit)
				mu.Unlock()
				total = 2
				i["key"] = fmt.Sprintf("CS-%d", req.Start+1)
				i["id"] = fmt.Sprint(req.Start + 1)
			}
			json.NewEncoder(w).Encode(map[string]any{"issues": []any{i}, "startAt": req.Start, "total": total})
			return
		}
		baseline(w, r)
	})
	for _, tt := range []struct {
		token, project string
		status         int
	}{{"jira-a", "CS", 200}, {"jira-a", "IT", 403}, {"jira-b", "CS", 200}, {"jira-b", "IT", 200}} {
		w := call(s, "GET", "/v1/jira/projects/"+tt.project+"/issues/"+tt.project+"-1", tt.token, "")
		if w.Code != tt.status {
			t.Fatal(tt, w.Code, w.Body.String())
		}
	}
	s.projects["CS"].FilterJQL = "labels = zammad OR labels = support"
	w := call(s, "POST", root+"/issues", "jira-a", `{"fields":{"summary":"x"}}`)
	if w.Code != 403 {
		t.Fatal("complex-filter create enabled")
	}
	w = call(s, "POST", root+"/issues/search", "jira-a", `{"text":"hello","limit":1}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var page struct {
		Items  []map[string]any `json:"items"`
		Cursor *string          `json:"nextCursor"`
	}
	json.Unmarshal(w.Body.Bytes(), &page)
	if page.Cursor == nil || page.Items[0]["issueKey"] != "CS-1" {
		t.Fatal(w.Body.String())
	}
	b, _ := json.Marshal(map[string]string{"cursor": *page.Cursor})
	w = call(s, "POST", root+"/issues/search", "jira-a", string(b))
	page.Cursor = nil
	json.Unmarshal(w.Body.Bytes(), &page)
	if w.Code != 200 || page.Cursor != nil || page.Items[0]["issueKey"] != "CS-2" {
		t.Fatal(w.Code, w.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(queries) != 2 || queries[0] != queries[1] || offsets[0] != 0 || offsets[1] != 1 || limits[0] != 1 || limits[1] != 1 {
		t.Fatal(queries, offsets, limits)
	}
	if !strings.Contains(queries[0], `(project = "CS") AND (labels = zammad OR labels = support) AND (text ~ "hello") ORDER BY updated ASC, key ASC`) {
		t.Fatal(queries[0])
	}
}

func TestCommentCreateOnceAndSupersededRoutes(t *testing.T) {
	var upstream, writes atomic.Int32
	s, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		upstream.Add(1)
		if strings.HasSuffix(r.URL.Path, "/comment") && r.Method == "POST" {
			writes.Add(1)
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if len(body) != 1 || body["body"] != "hello" {
				t.Error("comment body transformed", body)
			}
			io.WriteString(w, `{"id":"88","body":"hello","author":{"name":"bot"}}`)
			return
		}
		baseline(w, r)
	})
	w := call(s, "POST", root+"/issues/CS-1/comments", "jira-a", `{"body":"hello"}`)
	if w.Code != 201 || writes.Load() != 1 || !strings.Contains(w.Body.String(), `"commentId":"88"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	before := upstream.Load()
	for _, prefix := range []string{"/v1/jira", root} {
		for _, route := range []string{"tickets", "binding", "changes", "operations"} {
			w = call(s, "POST", prefix+"/"+route, "jira-a", `{}`)
			want := 404
			if prefix != root {
				want = 403
			}
			if w.Code != want {
				t.Fatal(route, w.Code)
			}
		}
	}
	if upstream.Load() != before {
		t.Fatal("superseded route reached Jira")
	}
}

func TestEmptyIssueKeysIsDocumentedAsOmitted(t *testing.T) {
	var mu sync.Mutex
	var queries []string
	s, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/search") {
			var req map[string]any
			json.NewDecoder(r.Body).Decode(&req)
			if strings.Contains(str(req["jql"]), "ORDER BY") {
				mu.Lock()
				queries = append(queries, str(req["jql"]))
				mu.Unlock()
			}
		}
		baseline(w, r)
	})
	for _, body := range []string{`{}`, `{"issueKeys":[]}`} {
		w := call(s, "POST", root+"/issues/search", "jira-a", body)
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(queries) != 2 || queries[0] != queries[1] {
		t.Fatal(queries)
	}
}
