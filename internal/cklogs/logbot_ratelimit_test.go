package cklogs

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// submit 前两次 429 → 客户端退避重试后成功；审计如实记录每次 429 尝试。
func TestRateLimitRetryThenSuccess(t *testing.T) {
	f := newLogbotFake(t)
	f.submit429Times = 2
	res, calls := queryWithAudit(t, f)
	if !res.OK {
		t.Fatalf("expect success after retry: %+v", res)
	}
	f.mu.Lock()
	submits := f.submits
	f.mu.Unlock()
	if submits != 3 {
		t.Fatalf("expect 3 submit attempts (1+2 retries), got %d", submits)
	}
	var submitCalls []string
	for _, c := range calls {
		if c.Stage == "submit" {
			submitCalls = append(submitCalls, c.ErrorKind)
		}
	}
	if len(submitCalls) != 3 || submitCalls[0] != "rate_limited" || submitCalls[1] != "rate_limited" || submitCalls[2] != "" {
		t.Fatalf("submit audit kinds: %v", submitCalls)
	}
}

// 持续 429 → 重试次数受 RateLimitMaxRetries 约束，最终如实报 rate_limited。
func TestRateLimitPersistent429Bounded(t *testing.T) {
	f := newLogbotFake(t)
	f.submitStatus = http.StatusTooManyRequests
	res, calls := queryWithAudit(t, f)
	if res.OK || !strings.Contains(res.Message, "429") {
		t.Fatalf("expect rate limited error: %+v", res)
	}
	// 测试客户端 RateLimitMaxRetries=2 → 共 3 次尝试。
	f.mu.Lock()
	submits := f.submits
	f.mu.Unlock()
	if submits != 3 {
		t.Fatalf("expect 3 bounded attempts, got %d", submits)
	}
	kinds := 0
	for _, c := range calls {
		if c.Stage == "submit" && c.ErrorKind == "rate_limited" {
			kinds++
		}
	}
	if kinds != 3 {
		t.Fatalf("every 429 attempt must be audited as rate_limited: %d/3", kinds)
	}
}

// RateLimitMaxRetries<0 禁用重试：一次 429 直接返回。
func TestRateLimitRetryDisabled(t *testing.T) {
	f := newLogbotFake(t)
	f.submitStatus = http.StatusTooManyRequests
	c := f.client()
	c.RateLimitMaxRetries = -1
	backend := LogBotBackend{Client: c}
	res := backend.Query(context.Background(), Filters{Sender: "a@b.com", TimeRange: TimeRange{FromMs: 0, ToMs: 3600000}}, QueryOptions{Dataset: "delivery_agent"})
	if res.ErrorKind != "rate_limited" {
		t.Fatalf("expect rate_limited, got %+v", res)
	}
	f.mu.Lock()
	submits := f.submits
	f.mu.Unlock()
	if submits != 1 {
		t.Fatalf("disabled retry must stop after 1 attempt, got %d", submits)
	}
}

// 退避时长超过 ctx 剩余预算时不重试（预算护栏）。
func TestRateLimitBudgetGuard(t *testing.T) {
	f := newLogbotFake(t)
	f.submitStatus = http.StatusTooManyRequests
	c := f.client()
	// 退避 10s 起步，远超 Timeout(2s)-margin(3s) 的剩余预算 → 不重试。
	c.RateLimitBackoff = 10 * time.Second
	c.Timeout = 2 * time.Second
	backend := LogBotBackend{Client: c}
	started := time.Now()
	res := backend.Query(context.Background(), Filters{Sender: "a@b.com", TimeRange: TimeRange{FromMs: 0, ToMs: 3600000}}, QueryOptions{Dataset: "delivery_agent"})
	if res.ErrorKind != "rate_limited" {
		t.Fatalf("expect rate_limited, got %+v", res)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("budget guard failed: retry happened despite insufficient budget (%v)", time.Since(started))
	}
	f.mu.Lock()
	submits := f.submits
	f.mu.Unlock()
	if submits != 1 {
		t.Fatalf("budget guard must prevent retry, got %d attempts", submits)
	}
}
