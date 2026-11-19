package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"platform-gateway/internal/auth"
	"platform-gateway/internal/config"
)

func TestCustomerLookup(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ops/jira-modal/lookup" || r.URL.Query().Get("senderEmail") != "a@example.com" || r.Header.Get("X-API-Key") != "upstream-secret" || r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected upstream request: %s", r.URL.String())
		}
		io.WriteString(w, `{"resolution":"cj-hit","prefillFields":{"summary":"hello"}}`)
	}))
	defer upstream.Close()
	checker := auth.NewFromFile(&config.File{Tokens: []config.Token{{Token: "cs-token", JiraProjects: []string{"CS"}}, {Token: "other-token", JiraProjects: []string{"IT"}}}}, nil)
	handler := customerLookup(checker, &config.CustomerLookup{BaseURL: upstream.URL, APIKey: "upstream-secret"})
	for _, tt := range []struct {
		token, query string
		status       int
	}{{"cs-token", "senderEmail=a%40example.com", 200}, {"other-token", "senderEmail=a%40example.com", 403}, {"", "senderEmail=a%40example.com", 401}, {"cs-token", "unexpected=x", 400}} {
		req := httptest.NewRequest("GET", "/v1/jira/customer-lookup?"+tt.query, nil)
		if tt.token != "" {
			req.Header.Set("Authorization", "Bearer "+tt.token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != tt.status {
			t.Errorf("status = %d, want %d: %s", w.Code, tt.status, w.Body.String())
		}
		if tt.status == 200 && !strings.Contains(w.Body.String(), `"resolution":"cj-hit"`) {
			t.Error("missing upstream response")
		}
	}
}
