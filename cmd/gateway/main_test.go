package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadConfigQueue(t *testing.T) {
	setupConfig(t)
	for _, key := range []string{"CKLOGS_MAX_CONCURRENCY", "CKLOGS_QUEUE_SIZE", "CKLOGS_QUEUE_WAIT_MS", "GATEWAY_ALLOW_CIDRS"} {
		t.Setenv(key, "")
	}
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.maxConc != 2 || cfg.queueSize != 32 || cfg.queueWait != 30*time.Second {
		t.Fatalf("queue defaults: concurrency=%d size=%d wait=%s", cfg.maxConc, cfg.queueSize, cfg.queueWait)
	}
	t.Setenv("CKLOGS_MAX_CONCURRENCY", " 5 ")
	t.Setenv("CKLOGS_QUEUE_SIZE", "0")
	t.Setenv("CKLOGS_QUEUE_WAIT_MS", "1500")
	cfg, err = loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.maxConc != 5 || cfg.queueSize != 0 || cfg.queueWait != 1500*time.Millisecond {
		t.Fatalf("queue overrides: concurrency=%d size=%d wait=%s", cfg.maxConc, cfg.queueSize, cfg.queueWait)
	}
}

func TestLoadConfigRejectsInvalidQueue(t *testing.T) {
	setupConfig(t)
	for _, key := range []string{"CKLOGS_MAX_CONCURRENCY", "CKLOGS_QUEUE_SIZE", "CKLOGS_QUEUE_WAIT_MS", "GATEWAY_ALLOW_CIDRS"} {
		t.Setenv(key, "")
	}
	for _, tt := range []struct{ key, value string }{
		{"CKLOGS_MAX_CONCURRENCY", "0"},
		{"CKLOGS_MAX_CONCURRENCY", "-1"},
		{"CKLOGS_MAX_CONCURRENCY", "2.5"},
		{"CKLOGS_MAX_CONCURRENCY", "abc"},
		{"CKLOGS_MAX_CONCURRENCY", "99999999999999999999999"},
		{"CKLOGS_QUEUE_SIZE", "-1"},
		{"CKLOGS_QUEUE_SIZE", "abc"},
		{"CKLOGS_QUEUE_WAIT_MS", "0"},
		{"CKLOGS_QUEUE_WAIT_MS", "-1"},
		{"CKLOGS_QUEUE_WAIT_MS", "abc"},
		{"CKLOGS_QUEUE_WAIT_MS", "9223372036855"},
	} {
		t.Run(tt.key+"/"+tt.value, func(t *testing.T) {
			t.Setenv(tt.key, tt.value)
			if _, err := loadConfig(); err == nil || !strings.Contains(err.Error(), tt.key) {
				t.Fatalf("expected error identifying %s, got %v", tt.key, err)
			}
		})
	}
}

func setupConfig(t *testing.T) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(`{"tokens":[{"token":"test-credential","cklogs":"external"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GATEWAY_AUTH_FILE", p)
	for _, k := range []string{"GATEWAY_TOKEN_EXTERNAL", "GATEWAY_TOKEN_INTERNAL", "GATEWAY_AUTH_TOKENS", "GATEWAY_JIRA_TOKENS", "GATEWAY_JIRA_TOKENS_FILE"} {
		t.Setenv(k, "")
	}
	t.Setenv("CK_LOGS_BASIC_USER", "test")
	t.Setenv("CK_LOGS_BASIC_PASS", "test")
}
func TestEnabledServices(t *testing.T) {
	setupConfig(t)
	t.Setenv("JIRA_BASE_URL", "")
	t.Setenv("JIRA_API_TOKEN", "")
	if _, err := loadConfig(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CK_LOGS_BASIC_PASS", "")
	if _, err := loadConfig(); err == nil {
		t.Fatal("missing CK accepted")
	}
	if err := os.WriteFile(os.Getenv("GATEWAY_AUTH_FILE"), []byte(`{"tokens":[{"token":"jira-test","jiraProjects":["CS"]}],"jira":{"projects":{"CS":{}}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(); err == nil {
		t.Fatal("missing Jira accepted")
	}
	t.Setenv("JIRA_BASE_URL", "https://jira.example.test/context")
	t.Setenv("JIRA_API_TOKEN", "upstream")
	t.Setenv("JIRA_BASIC_USER", "")
	t.Setenv("JIRA_BASIC_PASSWORD", "")
	t.Setenv("CKLOGS_MAX_CONCURRENCY", "invalid-but-disabled")
	cfg, err := loadConfig()
	if err != nil || cfg.jiraClient == nil {
		t.Fatal(err)
	}
	t.Setenv("GATEWAY_TOKEN_INTERNAL", "secret")
	if _, err := loadConfig(); err == nil {
		t.Fatal("legacy accepted")
	}
}
