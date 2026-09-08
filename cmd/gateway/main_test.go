package main

import (
	"strings"
	"testing"
	"time"
)

func TestLoadConfigQueue(t *testing.T) {
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
