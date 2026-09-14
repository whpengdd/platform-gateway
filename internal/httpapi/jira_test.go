package httpapi

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"platform-gateway/internal/auditlog"
	"platform-gateway/internal/auth"
	"platform-gateway/internal/config"
	"platform-gateway/internal/jira"
	"platform-gateway/internal/queue"
	"strings"
	"testing"
	"time"
)

func TestJiraTokensRejectedByEveryCKRoute(t *testing.T) {
	f, err := config.Parse([]byte(`{"tokens":[{"token":"jira","jiraProjects":["CS"]}],"jira":{"projects":{"CS":{}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	s := New(Config{Auth: auth.NewFromFile(f, nil)})
	for _, route := range []string{"delivery", "login", "message", "analysis/delivery", "analysis/auth", "analysis/ops"} {
		r := httptest.NewRequest("POST", "/v1/cklogs/"+route, strings.NewReader(`{}`))
		r.Header.Set("Authorization", "Bearer jira")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 403 || !strings.Contains(w.Body.String(), "token_scope_forbidden") {
			t.Fatal(route, w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("GET", "/ready", nil))
	if w.Code != 200 || strings.Contains(w.Body.String(), "CS") {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestJiraAuditRedaction(t *testing.T) {
	f, err := config.Parse([]byte(`{"tokens":[{"token":"jira-secret","jiraProjects":["CS"]}],"jira":{"projects":{"CS":{}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	a := auth.NewFromFile(f, nil)
	client, err := jira.NewClient("https://localhost:1", "upstream-secret", "", "")
	if err != nil {
		t.Fatal(err)
	}
	js, err := jira.NewServer(client, a, f.Jira.Projects)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	audit, err := auditlog.NewWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := New(Config{Auth: a, Jira: js, Audit: audit})
	r := httptest.NewRequest("POST", "/v1/jira/projects/CS/secret-path", strings.NewReader(`{"body":"secret-body","jql":"secret-jql"}`))
	r.Header.Set("Authorization", "Bearer jira-secret")
	r.Header.Set("X-Request-Id", "secret header with spaces")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	audit.Close()
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil || len(files) == 0 {
		t.Fatal("no audit", err)
	}
	for _, f := range files {
		b, _ := os.ReadFile(f)
		for _, secret := range []string{"jira-secret", "upstream-secret", "secret-path", "secret-body", "secret-jql", "secret header"} {
			if strings.Contains(string(b), secret) {
				t.Fatal("audit leaked", secret)
			}
		}
	}
}

func TestCKQueueDoesNotBlockJira(t *testing.T) {
	f, err := config.Parse([]byte(`{"tokens":[{"token":"jira-token","jiraProjects":["CS"]},{"token":"ck-token","cklogs":"internal"}],"jira":{"projects":{"CS":{}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	a := auth.NewFromFile(f, nil)
	client, err := jira.NewClient("https://localhost:1", "upstream", "", "")
	if err != nil {
		t.Fatal(err)
	}
	js, err := jira.NewServer(client, a, f.Jira.Projects)
	if err != nil {
		t.Fatal(err)
	}
	gate := queue.New("cklogs", 1, 0, time.Second)
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		gate.Run(context.Background(), func(context.Context) error { close(entered); <-release; return nil })
	}()
	<-entered
	s := New(Config{Auth: a, Jira: js, Gate: gate})
	r := httptest.NewRequest("GET", "/v1/jira/projects", nil)
	r.Header.Set("Authorization", "Bearer jira-token")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	close(release)
	<-done
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}
