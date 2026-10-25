package cklogs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"platform-gateway/internal/auditlog"
)

func fixtureClient(t *testing.T, response string) *Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(response)) }))
	t.Cleanup(server.Close)
	return &Client{BaseURL: server.URL, User: "user", Pass: "secret"}
}

func TestQueryRejectsIncompleteResponses(t *testing.T) {
	for _, response := range []string{
		`{"responses":[]}`,
		`{"responses":[{"hits":{"hits":[]}},{"hits":{"hits":[]}}]}`,
		`{"responses":[{}]}`,
		`{"responses":[{"error":{"reason":"private"}}]}`,
		`{"responses":[{"timed_out":true,"hits":{"total":0,"hits":[]}}]}`,
		`{"responses":[{"_shards":{"failed":1},"hits":{"total":0,"hits":[]}}]}`,
	} {
		t.Run(response, func(t *testing.T) {
			rec := &auditlog.Record{}
			result := fixtureClient(t, response).Query(auditlog.WithRecord(context.Background(), rec), Filters{}, QueryOptions{})
			if result.OK || result.ErrorKind != "upstream_error" {
				t.Fatalf("unexpected result: %+v", result)
			}
			if len(rec.KibanaCalls) != 1 || rec.KibanaCalls[0].OK {
				t.Fatalf("audit incorrectly successful: %+v", rec.KibanaCalls)
			}
		})
	}
}

func TestQueryMetadataAndRedactedDiagnostics(t *testing.T) {
	rec := &auditlog.Record{}
	ctx, cancel := context.WithTimeout(auditlog.WithRecord(context.Background(), rec), 2*time.Second)
	defer cancel()
	c := fixtureClient(t, `{"responses":[{"hits":{"total":{"value":1,"relation":"eq"},"hits":[{"_index":"logs","_id":"one","_source":{}}]}}]}`)
	result := c.Query(ctx, Filters{Sender: "private@example.org", Subject: "private-subject", TimeRange: TimeRange{FromMs: 100, ToMs: 3100}}, QueryOptions{Stage: "candidate", PageSize: 17, Offset: 2})
	if !result.OK || !result.TotalExact || len(result.HitIDs) != 1 || result.HitIDs[0] != "logs\x00one" {
		t.Fatalf("metadata: %+v", result)
	}
	call := rec.KibanaCalls[0]
	if call.Stage != "candidate" || call.BudgetMS <= 0 || call.BudgetMS > 2000 || !call.TotalExact {
		t.Fatalf("audit: %+v", call)
	}
	b, _ := json.Marshal(call)
	for _, secret := range []string{"private@example.org", "private-subject", "secret"} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("audit leaks %q", secret)
		}
	}
	if len(call.QueryHash) != 64 || !strings.Contains(call.QueryShape, "size=17") || !strings.Contains(call.QueryShape, "window_ms=3000") {
		t.Fatalf("shape/hash: %+v", call)
	}
}

func TestTotalExact(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		exact bool
	}{
		{"3", true}, {"-1", false}, {"1.5", false}, {"1e40", false}, {`{"value":3,"relation":"eq"}`, true}, {`{"value":3,"relation":"gte"}`, false},
		{`{"value":3}`, false}, {"null", false},
	} {
		var value any
		_ = json.Unmarshal([]byte(tc.raw), &value)
		got, _ := totalExact(value)
		if got != tc.exact {
			t.Fatalf("%s exact=%v", tc.raw, got)
		}
	}
}

func TestTracePreservesPartialFailures(t *testing.T) {
	result := fixtureClient(t, `{"responses":[{"timed_out":true,"hits":{"hits":[]}},{"hits":{"hits":[]}}]}`).Trace(context.Background(), "tid", TimeRange{})
	if !result.OK || len(result.PartialFailures) == 0 {
		t.Fatalf("trace: %+v", result)
	}
}

func TestResponseSizeLimit(t *testing.T) {
	result := fixtureClient(t, strings.Repeat(" ", (8<<20)+1)).Query(context.Background(), Filters{}, QueryOptions{})
	if result.OK || !strings.Contains(result.Message, "8 MiB") {
		t.Fatalf("oversized response: %+v", result)
	}
}

type clientTransportFunc func(*http.Request) (*http.Response, error)

func (f clientTransportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type brokenResponseReader struct{}

func (brokenResponseReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (brokenResponseReader) Close() error             { return nil }

func TestResponseReadFailure(t *testing.T) {
	c := &Client{User: "user", Pass: "secret", HTTPClient: &http.Client{Transport: clientTransportFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: brokenResponseReader{}, Header: make(http.Header)}, nil
	})}}
	result := c.Query(context.Background(), Filters{}, QueryOptions{})
	if result.OK || result.ErrorKind != "upstream_error" {
		t.Fatalf("read failure: %+v", result)
	}
}

func TestClientHonorsParentDeadlineAndCancellation(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprint(canceled), func(t *testing.T) {
			rec := &auditlog.Record{}
			ctx, cancel := context.WithTimeout(auditlog.WithRecord(context.Background(), rec), 20*time.Millisecond)
			defer cancel()
			if canceled {
				cancel()
			}
			c := &Client{User: "user", Pass: "secret", HTTPClient: &http.Client{Transport: clientTransportFunc(func(r *http.Request) (*http.Response, error) {
				<-r.Context().Done()
				return nil, r.Context().Err()
			})}}
			result := c.Query(ctx, Filters{}, QueryOptions{})
			want := "timeout"
			if canceled {
				want = "canceled"
			}
			if result.ErrorKind != want {
				t.Fatalf("got %+v want %s", result, want)
			}
			if len(rec.KibanaCalls) != 1 || rec.KibanaCalls[0].BudgetMS > 20 || rec.KibanaCalls[0].OK {
				t.Fatalf("audit: %+v", rec.KibanaCalls)
			}
		})
	}
}

func TestUpstreamTimeoutIncludesNarrowingAdvice(t *testing.T) {
	result := fixtureClient(t, `{"responses":[{"timed_out":true,"hits":{"total":0,"hits":[]}}]}`).Query(context.Background(), Filters{}, QueryOptions{})
	if result.OK || !strings.Contains(result.Message, "前后 1 小时") || !strings.Contains(result.Message, "不能据此判断没有记录") {
		t.Fatalf("missing timeout advice: %+v", result)
	}
}
