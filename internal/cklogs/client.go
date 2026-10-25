package cklogs

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
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
	return c != nil && c.User != "" && c.Pass != ""
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
	idx := c.Index
	if idx == "" {
		return defaultIndex
	}
	return idx
}

type kibanaMSearch struct {
	Responses []kibanaResponse `json:"responses"`
}

type kibanaResponse struct {
	Error    json.RawMessage `json:"error"`
	TimedOut bool            `json:"timed_out"`
	Shards   struct {
		Failed int `json:"failed"`
	} `json:"_shards"`
	Hits *struct {
		Total any `json:"total"`
		Hits  []struct {
			Source map[string]any `json:"_source"`
			Index  string         `json:"_index"`
			ID     string         `json:"_id"`
		} `json:"hits"`
	} `json:"hits"`
}

func (c *Client) msearch(ctx context.Context, body string, timeout time.Duration, opts QueryOptions) (*kibanaMSearch, *QueryResult) {
	if opts.Stage == "" {
		opts.Stage = opts.Dataset
		if opts.Stage == "" {
			opts.Stage = "mta"
		}
		if opts.CountOnly {
			opts.Stage += "_count"
		} else if opts.Offset > 0 {
			opts.Stage += "_page"
		} else {
			opts.Stage += "_detail"
		}
	}
	call := auditlog.KibanaCall{
		Index:      msearchIndexes(body),
		Stage:      opts.Stage,
		Dataset:    opts.Dataset,
		QueryHash:  fmt.Sprintf("%x", sha256.Sum256([]byte(body))),
		QueryShape: queryShape(body),
		CountOnly:  opts.CountOnly,
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
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < timeout {
		timeout = time.Until(deadline)
	}
	call.BudgetMS = max(int64(0), timeout.Milliseconds())
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
			return nil, &QueryResult{ErrorKind: "timeout", Message: timeoutMessage()}
		}
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			call.ErrorKind = "canceled"
			return nil, &QueryResult{ErrorKind: "canceled", Message: "ck_logs request canceled"}
		}
		call.ErrorKind = "network_error"
		return nil, &QueryResult{ErrorKind: "network_error", Message: err.Error()}
	}
	defer resp.Body.Close()
	call.HTTPStatus = resp.StatusCode
	const maxResponseBytes = 8 << 20
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if readErr != nil && ctx.Err() != nil {
		kind := "canceled"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			kind = "timeout"
		}
		call.ErrorKind = kind
		return nil, &QueryResult{ErrorKind: kind, Message: readInterruptedMessage(kind)}
	}
	if readErr != nil || len(raw) > maxResponseBytes {
		call.ErrorKind = "upstream_error"
		return nil, &QueryResult{ErrorKind: "upstream_error", Message: "ck_logs response incomplete or exceeds 8 MiB"}
	}
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
	call.OK = len(data.Responses) > 0
	if opts.Stage != "trace" && len(data.Responses) != 1 {
		call.OK = false
	}
	for _, r := range data.Responses {
		if responseFailure(r) != "" {
			call.OK = false
			call.ErrorKind = "upstream_error"
		}
	}
	if !call.OK {
		call.ErrorKind = "upstream_error"
	}
	if len(data.Responses) > 0 && data.Responses[0].Hits != nil {
		call.TotalExact, _ = totalExact(data.Responses[0].Hits.Total)
	}
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
	data, fail := c.msearch(ctx, body, c.timeout(), opts)
	if fail != nil {
		return *fail
	}
	if len(data.Responses) != 1 {
		return QueryResult{ErrorKind: "upstream_error", Message: "ck_logs response error: null"}
	}
	r := data.Responses[0]
	if msg := responseFailure(r); msg != "" {
		return QueryResult{ErrorKind: "upstream_error", Message: msg}
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
	exact, relation := totalExact(r.Hits.Total)
	ids := make([]string, len(hits))
	for i, hit := range hits {
		if hit.ID != "" {
			ids[i] = hit.Index + "\x00" + hit.ID
		}
	}
	return QueryResult{OK: true, Entries: entries, Total: total, TotalExact: exact, TotalRelation: relation, HitIDs: ids}
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
	data, fail := c.msearch(ctx, body, c.timeout(), opts)
	if fail != nil {
		return *fail, nil
	}
	if len(data.Responses) != 1 {
		return QueryResult{ErrorKind: "upstream_error", Message: "ck_logs response error: null"}, nil
	}
	r := data.Responses[0]
	if msg := responseFailure(r); msg != "" {
		return QueryResult{ErrorKind: "upstream_error", Message: msg}, nil
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
	exact, relation := totalExact(r.Hits.Total)
	return QueryResult{OK: true, Total: total, TotalExact: exact, TotalRelation: relation}, entries
}

func (c *Client) Trace(ctx context.Context, tid string, tr TimeRange) TraceResult {
	body, err := BuildTraceBody(tid, tr)
	if err != nil {
		return TraceResult{ErrorKind: "network_error", Message: err.Error()}
	}
	data, fail := c.msearch(ctx, body, c.timeout(), QueryOptions{Stage: "trace"})
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
		if msg := responseFailure(r); msg != "" {
			partial = append(partial, PartialFailure{Source: meta.Source, Index: meta.Index, Message: msg})
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

// responseFailure treats incomplete search results as failures, never empty success.
func responseFailure(r kibanaResponse) string {
	if len(r.Error) > 0 && string(r.Error) != "null" {
		return "ck_logs subquery failed"
	}
	if r.TimedOut {
		return timeoutMessage()
	}
	if r.Shards.Failed > 0 {
		return "ck_logs subquery has failed shards"
	}
	if r.Hits == nil {
		return "ck_logs response missing hits"
	}
	return ""
}

func validTotal(v any) bool {
	n, ok := asFloat(v)
	return ok && !math.IsNaN(n) && !math.IsInf(n, 0) && n >= 0 && math.Trunc(n) == n && n < float64(uint64(1)<<(strconv.IntSize-1))
}

func totalExact(v any) (bool, string) {
	switch t := v.(type) {
	case float64, int:
		return validTotal(v), "eq"
	case map[string]any:
		relation, _ := t["relation"].(string)
		return validTotal(t["value"]) && relation == "eq", relation
	}
	return false, ""
}

// queryShape records bounded structural diagnostics only. Literal filter values and index
// names and credentials never enter this summary; the hash identifies the DSL.
func queryShape(body string) string {
	var shapes []string
	lines := strings.Split(strings.TrimSpace(body), "\n")
	for i := 1; i < len(lines) && len(shapes) < 12; i += 2 {
		var q map[string]any
		if json.Unmarshal([]byte(lines[i]), &q) != nil {
			continue
		}
		ops := map[string]bool{}
		collectQueryShape(q["query"], ops, 0)
		parts := make([]string, 0, len(ops))
		for key := range ops {
			parts = append(parts, key)
		}
		sort.Strings(parts)
		shapes = append(shapes, fmt.Sprintf("size=%v,from=%v,sort=%t,query=%t,clauses=[%s]", shapeNumber(q["size"]), shapeNumber(q["from"]), q["sort"] != nil, q["query"] != nil, strings.Join(parts, ",")))

	}
	summary := strings.Join(shapes, ";")
	if len(summary) > 2048 {
		summary = summary[:2048] + "[clipped]"
	}
	return summary
}

func shapeNumber(v any) int {
	n, ok := v.(float64)
	if !ok || n < 0 || n > 1e9 {
		return 0
	}
	return int(n)
}

func collectQueryShape(v any, out map[string]bool, depth int) {
	if depth > 16 || len(out) > 64 {
		return
	}
	switch t := v.(type) {
	case []any:
		for _, child := range t {
			collectQueryShape(child, out, depth+1)
		}
	case map[string]any:
		for key, child := range t {
			switch key {
			case "term", "match_phrase", "range", "wildcard", "terms", "regexp":
				if fields, ok := child.(map[string]any); ok {
					for field, bounds := range fields {
						// Only schema field identifiers are retained; never arbitrary keys.
						if safeShapeField(field) {
							out[key+":"+field] = true
						}
						if key == "range" {
							if b, ok := bounds.(map[string]any); ok {
								from, fok := asFloat(b["gte"])
								to, tok := asFloat(b["lt"])
								if !tok {
									to, tok = asFloat(b["lte"])
								}
								if fok && tok && to >= from {
									out[fmt.Sprintf("window_ms=%.0f", to-from)] = true
								}
							}
						}
					}
				}
			case "bool", "filter", "must", "must_not", "should":
				out[key] = true
				collectQueryShape(child, out, depth+1)
			}
		}
	}
}
func safeShapeField(field string) bool {
	switch field {
	case "@timestamp", "timestamp", "sender", "rcpt", "subject", "tid", "mailtid", "from", "to", "mailfrom", "cmd", "authuser", "loginname", "user", "func", "dn", "ip":
		return true
	}
	return false
}

func readInterruptedMessage(kind string) string {
	if kind == "timeout" {
		return timeoutMessage()
	}
	return "ck_logs response read interrupted"
}
