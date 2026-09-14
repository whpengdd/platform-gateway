package jira

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"platform-gateway/internal/auth"
	"platform-gateway/internal/cklogs"
	"platform-gateway/internal/config"
	"platform-gateway/internal/httpapi"
	"platform-gateway/internal/queue"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fixture(t *testing.T, h http.HandlerFunc) (*Server, *auth.Checker) {
	t.Helper()
	f, err := config.Parse([]byte(`{"cklogs":{"auth":{"type":"basic","username":"user","password":"pass"}},"tokens":[{"token":"jira-a","jiraProjects":["CS"]},{"token":"jira-b","jiraProjects":["CS","IT"]},{"token":"ck-a","cklogs":"external"},{"token":"ck-b","cklogs":"internal"}],"jira":{"baseUrl":"https://jira.example.test/context","auth":{"type":"bearer","token":"upstream"},"projects":{"CS":{"filterJql":"labels = zammad","issueTypeId":"10","createDefaults":{"labels":["zammad"]},"createFields":["summary","description","customfield_1"],"readFields":["summary","reporter","customfield_1"]},"IT":{}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	a := auth.NewFromFile(f, nil)
	s, err := NewServer(testClient(t, h), a, f.Jira.Projects)
	if err != nil {
		t.Fatal(err)
	}
	return s, a
}
func call(s http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

const root = "/v1/jira/projects/CS"

func mockIssue() map[string]any {
	return map[string]any{"id": "1", "key": "CS-1", "fields": map[string]any{"project": map[string]any{"key": "CS"}, "summary": "safe", "reporter": map[string]any{"name": "bot", "displayName": "Bot", "self": "SECRET_URL"}, "customfield_1": "customer", "comment": map[string]any{"body": "PRIVATE_COMMENT"}, "parent": map[string]any{"key": "IT-1"}, "issuetype": map[string]any{"id": "10"}, "labels": []string{"zammad"}}}
}
func mockMetadata() any {
	return map[string]any{"projects": []any{map[string]any{"key": "CS", "issuetypes": []any{map[string]any{"id": "10", "fields": map[string]any{"summary": map[string]any{"required": true, "operations": []string{"set"}, "schema": map[string]string{"type": "string"}}, "description": map[string]any{"operations": []string{"set"}, "schema": map[string]string{"type": "string"}}, "labels": map[string]any{"operations": []string{"set"}, "schema": map[string]string{"type": "array", "items": "string"}}, "customfield_1": map[string]any{"operations": []string{"set"}, "schema": map[string]string{"type": "string"}}, "reporter": map[string]any{"required": true, "operations": []string{"set"}, "schema": map[string]string{"type": "user"}}}}}}}}
}
func baseline(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/jira/rest/api/2/search":
		json.NewEncoder(w).Encode(map[string]any{"issues": []any{mockIssue()}, "total": 1, "startAt": 0})
	case "/jira/rest/api/2/field":
		io.WriteString(w, `[{"id":"customfield_1","schema":{"type":"string"}}]`)
	case "/jira/rest/api/2/myself":
		io.WriteString(w, `{"name":"bot","displayName":"Bot","self":"SECRET_URL"}`)
	case "/jira/rest/api/2/issue/createmeta":
		json.NewEncoder(w).Encode(mockMetadata())
	case "/jira/rest/api/2/issue":
		io.WriteString(w, `{"id":"1","key":"CS-1"}`)
	case "/jira/rest/api/2/issue/1":
		json.NewEncoder(w).Encode(mockIssue())
	default:
		w.WriteHeader(404)
	}
}
func TestAuthorizationAndProjection(t *testing.T) {
	var count atomic.Int32
	s, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) { count.Add(1); baseline(w, r) })
	for _, tt := range []struct {
		token, path string
		status      int
	}{{"bad", root + "/capabilities", 401}, {"ck-a", root + "/capabilities", 403}, {"ck-b", root + "/issues/CS-1", 403}, {"jira-a", "/v1/jira/projects/IT/capabilities", 403}} {
		w := call(s, "GET", tt.path, tt.token, "")
		if w.Code != tt.status {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if count.Load() != 0 {
		t.Fatal("upstream before auth")
	}
	w := call(s, "GET", root+"/issues/CS-1", "jira-a", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "customer") || strings.Contains(w.Body.String(), "PRIVATE") || strings.Contains(w.Body.String(), "SECRET") || strings.Contains(w.Body.String(), "IT-1") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w.Header().Get("X-Request-Id") == "" {
		t.Fatal("no request id")
	}
	s.projects["CS"].IssueTypeID = ""
	w = call(s, "GET", root+"/issues/CS-1", "jira-a", "")
	if w.Code != 200 {
		t.Fatal("read depends on create", w.Body.String())
	}
}
func TestCreateDefaultsAndProof(t *testing.T) {
	var writes atomic.Int32
	s, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/jira/rest/api/2/issue" {
			writes.Add(1)
			var body struct {
				Fields map[string]any `json:"fields"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			if str(object(body.Fields["project"])["key"]) != "CS" || str(object(body.Fields["reporter"])["name"]) != "bot" {
				t.Error("fixed fields")
			}
		}
		baseline(w, r)
	})
	for _, body := range []string{`{"fields":{"labels":["other"],"summary":"x"}}`, `{"fields":{"project":{"key":"IT"}}}`, `{"fields":{"assignee":{"name":"x"}}}`, `{"fields":{"summary":"x"},"fields":{}}`, `{"fields":{"summary":{}}}`} {
		w := call(s, "POST", root+"/issues", "jira-a", body)
		if w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if writes.Load() != 0 {
		t.Fatal("invalid create written")
	}
	w := call(s, "POST", root+"/issues", "jira-a", `{"fields":{"summary":"x","customfield_1":"customer"}}`)
	if w.Code != 201 || writes.Load() != 1 {
		t.Fatal(w.Code, w.Body.String(), writes.Load())
	}
	s.projects["CS"].FilterJQL = "labels = zammad OR labels = support"
	w = call(s, "POST", root+"/issues", "jira-a", `{"fields":{"summary":"x"}}`)
	if w.Code != 403 || writes.Load() != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestRecoverySingleFlight(t *testing.T) {
	var calls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	s, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.WriteHeader(503)
			return
		}
		close(entered)
		<-release
		io.WriteString(w, `{"issues":[],"total":0}`)
	})
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	p := s.projects["CS"]
	if s.validate(context.Background(), "CS", p) == nil {
		t.Fatal("outage allowed")
	}
	if s.validate(context.Background(), "CS", p) == nil || calls.Load() != 1 {
		t.Fatal("backoff")
	}
	now = now.Add(5 * time.Second)
	done := make(chan error, 1)
	go func() { done <- s.validate(context.Background(), "CS", p) }()
	<-entered
	if s.validate(context.Background(), "CS", p) == nil || calls.Load() != 2 {
		t.Fatal("parallel validation")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := s.validate(context.Background(), "CS", p); err != nil || calls.Load() != 2 {
		t.Fatal(err)
	}
}
func TestInvalidFilterStaysDisabled(t *testing.T) {
	var calls atomic.Int32
	s, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(400)
		io.WriteString(w, "PRIVATE JQL")
	})
	for i := 0; i < 3; i++ {
		w := call(s, "GET", root+"/capabilities", "jira-a", "")
		if w.Code != 503 || strings.Contains(w.Body.String(), "PRIVATE") {
			t.Fatal(w.Body.String())
		}
	}
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
}
func TestCursors(t *testing.T) {
	s, _ := fixture(t, baseline)
	raw := s.nextCursor("comments", "CS", "1", Search{Limit: 50}, 50, 0)
	for _, tt := range []struct{ raw, op, key, parent string }{{raw, "search", "CS", ""}, {raw, "comments", "IT", "1"}, {raw, "comments", "CS", "2"}, {raw + "x", "comments", "CS", "1"}} {
		if _, err := s.decodeCursor(tt.raw, tt.op, tt.key, tt.parent); err == nil {
			t.Fatal("cursor accepted")
		}
	}
	if _, err := s.decodeCursor(raw, "comments", "CS", "1"); err != nil {
		t.Fatal(err)
	}
	other, _ := fixture(t, baseline)
	if _, err := other.decodeCursor(raw, "comments", "CS", "1"); err == nil {
		t.Fatal("restart accepted")
	}
	s.digest = "new"
	if _, err := s.decodeCursor(raw, "comments", "CS", "1"); err.(*Error).Code != "cursor_expired" {
		t.Fatal(err)
	}
	s, _ = fixture(t, baseline)
	raw = s.nextCursor("search", "CS", "", Search{Limit: 50}, 50, s.now().Add(-time.Second).Unix())
	if _, err := s.decodeCursor(raw, "search", "CS", ""); err.(*Error).Code != "cursor_expired" {
		t.Fatal(err)
	}
}
func TestParentAndComments(t *testing.T) {
	var denied atomic.Bool
	var commentCalls atomic.Int32
	s, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/jira/rest/api/2/search" && denied.Load() {
			io.WriteString(w, `{"issues":[],"total":0}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/comment") {
			commentCalls.Add(1)
			io.WriteString(w, `{"startAt":0,"total":3,"comments":[{"id":"11","body":"historic bot","author":{"name":"bot"}},{"id":"12","body":"PRIVATE","visibility":{"type":"group","value":"staff"}}]}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/comment/12") {
			io.WriteString(w, `{"id":"12","body":"PRIVATE","visibility":{"type":"group","value":"staff"}}`)
			return
		}
		baseline(w, r)
	})
	w := call(s, "GET", root+"/issues/CS-1/comments", "jira-a", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), "PRIVATE") || !strings.Contains(w.Body.String(), "historic bot") {
		t.Fatal(w.Code, w.Body.String())
	}
	var page struct {
		Cursor string `json:"nextCursor"`
	}
	json.Unmarshal(w.Body.Bytes(), &page)
	w = call(s, "GET", root+"/issues/CS-1/comments/12", "jira-a", "")
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
	denied.Store(true)
	w = call(s, "GET", root+"/issues/CS-1/comments?cursor="+page.Cursor, "jira-a", "")
	if w.Code != 404 || commentCalls.Load() != 1 {
		t.Fatal(w.Code, commentCalls.Load())
	}
	w = call(s, "POST", root+"/issues/CS-1/comments", "jira-a", `{"body":"write"}`)
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
}
func TestSearchInputAndCrossProject(t *testing.T) {
	s, _ := fixture(t, baseline)
	for _, body := range []string{`{"jql":"project=IT"}`, `{"limit":101}`, `{"text":"x","text":"y"}`, `{"updatedFrom":"2026-01-01T00:00:00Z"}`, `{"issueKeys":["CS-1) OR project=IT"]}`, `{} {}`} {
		w := call(s, "POST", root+"/issues/search", "jira-a", body)
		if w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w := call(s, "POST", root+"/issues/search", "jira-a", `{"text":"\") OR project=IT"}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	s.client = testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "search") {
			i := mockIssue()
			object(object(i["fields"])["project"])["key"] = "IT"
			json.NewEncoder(w).Encode(map[string]any{"issues": []any{i}, "total": 1})
			return
		}
		baseline(w, r)
	})
	w = call(s, "POST", root+"/issues/search", "jira-a", `{}`)
	if w.Code != 404 || strings.Contains(w.Body.String(), "customer") {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestBudgetsUTCAndQueue(t *testing.T) {
	var b budget
	now := time.Date(2026, 1, 1, 23, 59, 0, 0, time.UTC)
	for i := 0; i < 60; i++ {
		if !b.take(now, false, false, 0) {
			t.Fatal(i)
		}
	}
	if b.take(now, false, false, 0) {
		t.Fatal("read budget")
	}
	if !b.take(now.Add(time.Minute), false, false, 0) {
		t.Fatal("minute reset")
	}
	b = budget{}
	createTime := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 100; i++ {
		if !b.take(createTime.Add(time.Duration(i)*time.Minute), true, true, 0) {
			t.Fatal(i)
		}
	}
	if b.take(createTime.Add(101*time.Minute), true, true, 0) {
		t.Fatal("create budget")
	}
	var bytes budget
	if !bytes.take(now, true, false, 200<<20) || bytes.take(now, true, false, 1) || !bytes.take(now.Add(time.Minute), true, false, 1) {
		t.Fatal("UTC byte reset")
	}
	s, _ := fixture(t, baseline)
	s.gate = queue.New("jira", 1, 0, time.Second)
	entered := make(chan struct{})
	release := make(chan struct{})
	go s.gate.Run(context.Background(), func(context.Context) error { close(entered); <-release; return nil })
	<-entered
	w := call(s, "GET", root+"/capabilities", "jira-a", "")
	close(release)
	if w.Code != 503 || !strings.Contains(w.Body.String(), "gateway_busy") {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestLongCursorAndExplicitNulls(t *testing.T) {
	s, _ := fixture(t, baseline)
	keys := []string{}
	for i := 0; i < 100; i++ {
		keys = append(keys, strings.Repeat("A", 64)+"-"+strings.Repeat("1", 64))
	}
	raw := s.nextCursor("search", "CS", "", Search{Keys: keys, Limit: 50}, 50, 0)
	if len(raw) > 32768 {
		t.Fatal("oversized own cursor")
	}
	if _, err := s.decodeCursor(raw, "search", "CS", ""); err != nil {
		t.Fatal(err)
	}
	for _, b := range []string{`{"cursor":""}`, `{"limit":0}`, `{"limit":null}`, `{"Limit":50}`, `{"cursor":"x","text":""}`} {
		if w := call(s, "POST", root+"/issues/search", "jira-a", b); w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
func TestSharedProjectBudgetAndCancellation(t *testing.T) {
	s, _ := fixture(t, baseline)
	now := time.Unix(10000, 0)
	s.now = func() time.Time { return now }
	for i := 0; i < 60; i++ {
		token := "jira-a"
		if i%2 == 1 {
			token = "jira-b"
		}
		w := call(s, "POST", root+"/issues/search", token, `{}`)
		if w.Code != 200 {
			t.Fatal(i, w.Code)
		}
	}
	if w := call(s, "POST", root+"/issues/search", "jira-b", `{}`); w.Code != 429 {
		t.Fatal(w.Code)
	}
	s, _ = fixture(t, baseline)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequest("POST", root+"/issues", strings.NewReader(`{"fields":{"summary":"x"}}`)).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer jira-a")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 503 || strings.Contains(w.Body.String(), "outcome_unknown") {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestCommentCursorCrossParentAndOperation(t *testing.T) {
	var childCalls atomic.Int32
	s, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/search") {
			var q map[string]any
			json.NewDecoder(r.Body).Decode(&q)
			i := mockIssue()
			if strings.Contains(str(q["jql"]), "CS-2") {
				i["id"] = "2"
				i["key"] = "CS-2"
			}
			json.NewEncoder(w).Encode(map[string]any{"issues": []any{i}, "total": 1})
			return
		}
		if strings.Contains(r.URL.Path, "/comment") {
			childCalls.Add(1)
		}
		baseline(w, r)
	})
	raw := s.nextCursor("comments", "CS", "1", Search{Limit: 50}, 50, 0)
	w := call(s, "GET", root+"/issues/CS-2/comments?cursor="+raw, "jira-a", "")
	if w.Code != 400 || childCalls.Load() != 0 {
		t.Fatal(w.Code, w.Body.String())
	}
	b, _ := json.Marshal(map[string]string{"cursor": raw})
	w = call(s, "POST", root+"/issues/search", "jira-a", string(b))
	if w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestMovedIssueAndFixedCreateMismatch(t *testing.T) {
	var writes atomic.Int32
	s, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/jira/rest/api/2/issue" {
			writes.Add(1)
		}
		if r.URL.Path == "/jira/rest/api/2/issue/1" {
			i := mockIssue()
			object(i["fields"])["labels"] = []any{"other"}
			json.NewEncoder(w).Encode(i)
			return
		}
		baseline(w, r)
	})
	w := call(s, "POST", root+"/issues", "jira-a", `{"fields":{"summary":"x"}}`)
	if w.Code != 502 || !strings.Contains(w.Body.String(), "outcome_unknown") || writes.Load() != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	s.client = testClient(t, func(w http.ResponseWriter, r *http.Request) {
		i := mockIssue()
		i["key"] = "IT-9"
		object(object(i["fields"])["project"])["key"] = "IT"
		json.NewEncoder(w).Encode(map[string]any{"issues": []any{i}, "total": 1})
	})
	w = call(s, "GET", root+"/issues/CS-1", "jira-a", "")
	if w.Code != 404 {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestSaturatedJiraDoesNotBlockCKLogs(t *testing.T) {
	s, a := fixture(t, baseline)
	s.gate = queue.New("jira", 1, 0, time.Second)
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.gate.Run(context.Background(), func(context.Context) error { close(entered); <-release; return nil })
	}()
	<-entered
	ck := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"responses":[{"hits":{"total":{"value":0},"hits":[]}}]}`)
	}))
	defer ck.Close()
	handler := httpapi.New(httpapi.Config{Auth: a, Jira: s, CK: cklogs.NewService(&cklogs.Client{BaseURL: ck.URL, User: "u", Pass: "p", HTTPClient: ck.Client()}), Gate: queue.New("cklogs", 1, 0, time.Second), CKUser: "u", CKPass: "p"})
	w := call(handler, "GET", root+"/capabilities", "jira-a", "")
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
	w = call(handler, "POST", "/v1/cklogs/message", "ck-b", `{"tid":"test-tid"}`)
	close(release)
	<-done
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestResponseBodyLimitIncludesCorrelation(t *testing.T) {
	w := httptest.NewRecorder()
	w.Header().Set("X-Request-Id", "test-rid")
	sendJSON(w, 200, map[string]string{"value": strings.Repeat("x", MaxJSON+1)})
	if w.Code != 503 || w.Body.Len() > MaxJSON || !strings.Contains(w.Body.String(), "test-rid") {
		t.Fatal(w.Code, w.Body.Len())
	}
}
