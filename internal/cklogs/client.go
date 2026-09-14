package cklogs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"platform-gateway/internal/auditlog"
)

const (
	DefaultBaseURL   = "https://ck-logs.icoremail.net"
	DefaultTimeoutMS = 60_000
)

type Client struct {
	BaseURL    string
	User       string
	Pass       string
	Index      string
	Timeout    time.Duration
	HTTPClient *http.Client
}

func (c *Client) Configured() bool {
	return c != nil && strings.TrimSpace(c.User) != "" && strings.TrimSpace(c.Pass) != ""
}

func (c *Client) http() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

func (c *Client) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return DefaultTimeoutMS * time.Millisecond
}

func (c *Client) baseURL() string {
	u := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if u == "" {
		return DefaultBaseURL
	}
	return u
}

func (c *Client) index() string {
	idx := strings.TrimSpace(c.Index)
	if idx == "" {
		return defaultIndex
	}
	return idx
}

type kibanaMSearch struct {
	Responses []kibanaResponse `json:"responses"`
}

type kibanaResponse struct {
	Error json.RawMessage `json:"error"`
	Hits  *struct {
		Total any `json:"total"`
		Hits  []struct {
			Source map[string]any `json:"_source"`
		} `json:"hits"`
	} `json:"hits"`
}

func (c *Client) msearch(ctx context.Context, body string, timeout time.Duration) (*kibanaMSearch, *QueryResult) {
	call := auditlog.KibanaCall{
		Index:     msearchIndexes(body),
		CountOnly: strings.Contains(body, `"size":0`),
	}
	started := time.Now()
	defer func() {
		call.DurationMS = time.Since(started).Milliseconds()
		if rec := auditlog.From(ctx); rec != nil {
			rec.AddKibana(call)
		}
	}()
	if !c.Configured() {
		call.ErrorKind = "not_configured"
		return nil, &QueryResult{ErrorKind: "not_configured", Message: "CK_LOGS_BASIC_USER/CK_LOGS_BASIC_PASS 未配置"}
	}
	if timeout <= 0 {
		timeout = c.timeout()
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	url := c.baseURL() + "/api/console/proxy?path=/_msearch&method=POST"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		call.ErrorKind = "network_error"
		return nil, &QueryResult{ErrorKind: "network_error", Message: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("kbn-xsrf", "true")
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(c.User+":"+c.Pass)))
	resp, err := c.http().Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			call.ErrorKind = "timeout"
			return nil, &QueryResult{ErrorKind: "timeout", Message: fmt.Sprintf("ck_logs timeout after %dms", timeout.Milliseconds())}
		}
		call.ErrorKind = "network_error"
		return nil, &QueryResult{ErrorKind: "network_error", Message: err.Error()}
	}
	defer resp.Body.Close()
	call.HTTPStatus = resp.StatusCode
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		call.ErrorKind = "auth_error"
		return nil, &QueryResult{ErrorKind: "auth_error", Message: fmt.Sprintf("ck_logs auth failed: HTTP %d", resp.StatusCode)}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		call.ErrorKind = "upstream_error"
		return nil, &QueryResult{ErrorKind: "upstream_error", Message: fmt.Sprintf("ck_logs HTTP %d", resp.StatusCode)}
	}
	var data kibanaMSearch
	if err := json.Unmarshal(raw, &data); err != nil {
		call.ErrorKind = "network_error"
		return nil, &QueryResult{ErrorKind: "network_error", Message: err.Error()}
	}
	if len(data.Responses) > 0 && data.Responses[0].Hits != nil {
		hits := data.Responses[0].Hits.Hits
		call.Entries = len(hits)
		call.Total = readTotal(data.Responses[0].Hits.Total, len(hits))
	}
	call.OK = true
	return &data, nil
}

func msearchIndexes(body string) string {
	seen := map[string]struct{}{}
	var names []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var header struct {
			Index string `json:"index"`
		}
		if json.Unmarshal([]byte(line), &header) != nil || header.Index == "" {
			continue
		}
		if _, ok := seen[header.Index]; ok {
			continue
		}
		seen[header.Index] = struct{}{}
		names = append(names, header.Index)
	}
	return strings.Join(names, ",")
}

func (c *Client) Query(ctx context.Context, filters Filters, opts QueryOptions) QueryResult {
	var ds *Dataset
	if opts.Dataset != "" {
		ds = ResolveDataset(opts.Dataset)
		if ds == nil {
			return QueryResult{ErrorKind: "unknown_dataset", Message: "未知 dataset：" + opts.Dataset}
		}
		if ds.Unavailable != "" {
			return QueryResult{ErrorKind: ds.Unavailable, Message: UnavailableNote(ds.Unavailable)}
		}
	}
	var body string
	var err error
	if ds != nil && ds.Kind != "delivery" {
		body, err = BuildDatasetBody(ds, filters, opts)
	} else {
		body, err = BuildMsearchBody(filters, c.index(), opts)
	}
	if err != nil {
		return QueryResult{ErrorKind: "network_error", Message: err.Error()}
	}
	data, fail := c.msearch(ctx, body, c.timeout())
	if fail != nil {
		return *fail
	}
	if len(data.Responses) == 0 {
		return QueryResult{ErrorKind: "upstream_error", Message: "ck_logs response error: null"}
	}
	r := data.Responses[0]
	if len(r.Error) > 0 && string(r.Error) != "null" {
		msg := string(r.Error)
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return QueryResult{ErrorKind: "upstream_error", Message: "ck_logs response error: " + msg}
	}
	if r.Hits == nil {
		return QueryResult{OK: true, Entries: []Entry{}, Total: 0}
	}
	hits := r.Hits.Hits
	total := readTotal(r.Hits.Total, len(hits))
	entries := make([]Entry, 0, len(hits))
	for _, h := range hits {
		src := h.Source
		if src == nil {
			src = map[string]any{}
		}
		switch {
		case ds != nil && ds.Kind == "delivery_agent":
			entries = append(entries, mapDeliveryAgentHit(src))
		case ds != nil && ds.Kind == "delivery_pipeline":
			entries = append(entries, mapDeliveryPipelineHit(src))
		case ds != nil && ds.Kind == "delivery_proxy":
			entries = append(entries, mapDeliveryProxyHit(src))
		default:
			entries = append(entries, mapHitToEntry(src))
		}
	}
	return QueryResult{OK: true, Entries: entries, Total: total}
}

func (c *Client) QueryAuth(ctx context.Context, filters Filters, opts QueryOptions) (QueryResult, []AuthEntry) {
	ds := ResolveDataset(opts.Dataset)
	if ds == nil {
		return QueryResult{ErrorKind: "unknown_dataset", Message: "未知 dataset：" + opts.Dataset}, nil
	}
	if ds.Unavailable != "" {
		return QueryResult{ErrorKind: ds.Unavailable, Message: UnavailableNote(ds.Unavailable)}, nil
	}
	body, err := BuildDatasetBody(ds, filters, opts)
	if err != nil {
		return QueryResult{ErrorKind: "network_error", Message: err.Error()}, nil
	}
	data, fail := c.msearch(ctx, body, c.timeout())
	if fail != nil {
		return *fail, nil
	}
	if len(data.Responses) == 0 {
		return QueryResult{ErrorKind: "upstream_error", Message: "ck_logs response error: null"}, nil
	}
	r := data.Responses[0]
	if len(r.Error) > 0 && string(r.Error) != "null" {
		msg := string(r.Error)
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return QueryResult{ErrorKind: "upstream_error", Message: "ck_logs response error: " + msg}, nil
	}
	if r.Hits == nil {
		return QueryResult{OK: true, Total: 0}, []AuthEntry{}
	}
	hits := r.Hits.Hits
	total := readTotal(r.Hits.Total, len(hits))
	entries := make([]AuthEntry, 0, len(hits))
	for _, h := range hits {
		src := h.Source
		if src == nil {
			src = map[string]any{}
		}
		entries = append(entries, mapAuthHitToEntry(src, ds))
	}
	return QueryResult{OK: true, Total: total}, entries
}

func (c *Client) Trace(ctx context.Context, tid string, tr TimeRange) TraceResult {
	body, err := BuildTraceBody(tid, tr)
	if err != nil {
		return TraceResult{ErrorKind: "network_error", Message: err.Error()}
	}
	data, fail := c.msearch(ctx, body, c.timeout())
	if fail != nil {
		return TraceResult{ErrorKind: fail.ErrorKind, Message: fail.Message}
	}
	responses := data.Responses
	var timeline []TraceEvent
	var partial []PartialFailure
	for i, meta := range traceSources {
		if i >= len(responses) {
			partial = append(partial, PartialFailure{Source: meta.Source, Index: meta.Index, Message: "no response"})
			continue
		}
		r := responses[i]
		if len(r.Error) > 0 && string(r.Error) != "null" {
			msg := string(r.Error)
			if len(msg) > 200 {
				msg = msg[:200]
			}
			partial = append(partial, PartialFailure{Source: meta.Source, Index: meta.Index, Message: msg})
			continue
		}
		if r.Hits == nil {
			continue
		}
		for _, h := range r.Hits.Hits {
			src := h.Source
			if src == nil {
				src = map[string]any{}
			}
			timeline = append(timeline, mapTraceHit(src, meta.Source))
		}
	}
	sortTrace(timeline)
	return TraceResult{OK: true, Timeline: timeline, PartialFailures: partial}
}

func readTotal(v any, hitCount int) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case map[string]any:
		if n, ok := asFloat(t["value"]); ok {
			return int(n)
		}
	}
	return hitCount
}

func sortTrace(events []TraceEvent) {
	for i := 1; i < len(events); i++ {
		for j := i; j > 0 && events[j-1].TimestampISO > events[j].TimestampISO; j-- {
			events[j-1], events[j] = events[j], events[j-1]
		}
	}
}

func ckLogsCode(kind string) string {
	if kind == "" {
		kind = "unknown"
	}
	return "CK_LOGS_" + strings.ToUpper(kind)
}
