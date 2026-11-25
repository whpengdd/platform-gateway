package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"platform-gateway/internal/auth"
	fileconfig "platform-gateway/internal/config"
	"platform-gateway/internal/jira"
	"sync/atomic"
	"testing"
	"time"
)

func loadTestConfig(t *testing.T, raw string) (config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GATEWAY_CONFIG_FILE", p)
	return loadConfig()
}

const ckFixture = `"tokens":[{"token":"test-credential","cklogs":"external"}],"cklogs":{"auth":{"type":"basic","username":"test","password":"p$'\""}`

func TestLoadConfigQueue(t *testing.T) {
	cfg, err := loadTestConfig(t, `{`+ckFixture+`}}`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.maxConc != 2 || cfg.queueSize != 32 || cfg.queueWait != 30*time.Second {
		t.Fatal("queue defaults")
	}
	cfg, err = loadTestConfig(t, `{`+ckFixture+`,"queue":{"maxConcurrency":5,"size":0,"waitTimeoutMs":1500}},"audit":{"enabled":false},"server":{"listenAddr":"127.0.0.1:9123","allowCidrs":["127.0.0.0/8"]}}`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.maxConc != 5 || cfg.queueSize != 0 || cfg.queueWait != 1500*time.Millisecond || cfg.logDir != "" || cfg.listen != "127.0.0.1:9123" || len(cfg.cidrs) != 1 || cfg.ckPass != "p$'\"" {
		t.Fatal("file values not retained")
	}
}
func TestTimeoutBoundary(t *testing.T) {
	cfg, err := loadTestConfig(t, fmt.Sprintf(`{%s,"timeoutMs":%d}}`, ckFixture, fileconfig.MaxTimeoutMS))
	if err != nil {
		t.Fatal(err)
	}
	got := newHTTPClient(cfg.ckTimeout).Timeout
	if got <= 0 || got != time.Duration(fileconfig.MaxTimeoutMS)*time.Millisecond+5*time.Second {
		t.Fatal("HTTP timeout overflow", got)
	}
	if _, err := loadTestConfig(t, fmt.Sprintf(`{%s,"timeoutMs":%d}}`, ckFixture, fileconfig.MaxTimeoutMS+1)); err == nil {
		t.Fatal("overflow accepted")
	}
}
func TestEnabledServicesAndStaleEnvironment(t *testing.T) {
	for _, key := range []string{"GATEWAY_AUTH_FILE", "JIRA_BASE_URL", "CK_LOGS_BASIC_PASS", "LISTEN_ADDR", "CKLOGS_QUEUE_SIZE", "GATEWAY_TOKEN_INTERNAL"} {
		t.Setenv(key, "obsolete-value")
	}
	jira := `"jira":{"baseUrl":"https://jira.example.test/context","auth":{"type":"bearer","token":"upstream"},"projects":{"CS":{}}}`
	for _, raw := range []string{`{` + ckFixture + `}}`, `{"tokens":[{"token":"jira-test","jiraProjects":["CS"]}],` + jira + `}`, `{"tokens":[{"token":"jira-test","jiraProjects":["CS"]},{"token":"ck-test","cklogs":"internal"}],` + jira + `,"cklogs":{"auth":{"type":"basic","username":"u","password":"p"}}}`} {
		cfg, err := loadTestConfig(t, raw)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.listen != ":8091" {
			t.Fatal("stale override")
		}
		checker := auth.NewFromFile(cfg.authFile, cfg.cidrs)
		r := httptest.NewRequest("GET", "/ready", nil)
		r.Header.Set("Authorization", "Bearer obsolete-value")
		if _, status, _ := checker.Authenticate(r); status != 401 {
			t.Fatal("stale token authorized")
		}
		for _, entry := range cfg.authFile.Tokens {
			if entry.Token == "obsolete-value" {
				t.Fatal("legacy authorization")
			}
		}
	}
	for _, raw := range []string{`{"tokens":[{"token":"ck-test","cklogs":"internal"}]}`, `{"tokens":[{"token":"jira-test","jiraProjects":["CS"]}],"jira":{"projects":{"CS":{}}}}`} {
		if _, err := loadTestConfig(t, raw); err == nil {
			t.Fatal("missing upstream accepted")
		}
	}
}

func TestLoadConfigJiraAccountCacheTTL(t *testing.T) {
	for _, tt := range []struct {
		name, setting string
		wantReads     int32
	}{{"default", "", 1}, {"zero", `,"accountCacheTtlSeconds":0`, 2}, {"maximum", `,"accountCacheTtlSeconds":3600`, 1}} {
		t.Run(tt.name, func(t *testing.T) {
			var profileReads atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/rest/api/2/search":
					io.WriteString(w, `{"issues":[],"total":0,"startAt":0}`)
				case "/rest/api/2/myself":
					profileReads.Add(1)
					io.WriteString(w, `{"name":"bot","timeZone":"Asia/Shanghai"}`)
				default:
					t.Errorf("unexpected Jira request: %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer upstream.Close()
			raw := fmt.Sprintf(`{"tokens":[{"token":"jira-test","jiraProjects":["CS"]}],"jira":{"baseUrl":%q,"auth":{"type":"bearer","token":"upstream"},"projects":{"CS":{}}%s}}`, upstream.URL, tt.setting)
			cfg, err := loadTestConfig(t, raw)
			if err != nil {
				t.Fatal(err)
			}
			if profileReads.Load() != 0 {
				t.Fatal("loading configuration unexpectedly reads Jira account")
			}
			s, err := jira.NewServer(cfg.jiraClient, auth.NewFromFile(cfg.authFile, nil), cfg.authFile.Jira.Projects, int(cfg.authFile.Jira.MaxFieldBytes))
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				r := httptest.NewRequest("GET", "/v1/jira/projects/CS/capabilities", nil)
				r.Header.Set("Authorization", "Bearer jira-test")
				w := httptest.NewRecorder()
				s.ServeHTTP(w, r)
				if w.Code != 200 {
					t.Fatalf("capabilities status %d: %s", w.Code, w.Body.String())
				}
			}
			if got := profileReads.Load(); got != tt.wantReads {
				t.Fatalf("Jira account reads = %d, want %d", got, tt.wantReads)
			}
		})
	}
	cfg, err := loadTestConfig(t, `{`+ckFixture+`}}`)
	if err != nil || cfg.jiraClient != nil {
		t.Fatalf("cklogs-only configuration acquired a Jira client: %v", err)
	}
}
