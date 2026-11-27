package auditlog

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Record is one inbound /v1 request. It never stores tokens, passwords, or Kibana hit documents.
type Record struct {
	mu sync.Mutex

	Backend          string         `json:"backend,omitempty"`
	Project          string         `json:"project,omitempty"`
	TokenFingerprint string         `json:"token_fingerprint,omitempty"`
	UpstreamStatus   int            `json:"upstream_status,omitempty"`
	TS               string         `json:"ts"`
	Event            string         `json:"event"`
	RequestID        string         `json:"request_id"`
	ClientIP         string         `json:"client_ip,omitempty"`
	Operation        string         `json:"operation"`
	Auth             string         `json:"auth,omitempty"`
	TokenClass       string         `json:"token_class,omitempty"`
	Query            map[string]any `json:"query,omitempty"`
	QueueWaitMS      int64          `json:"queue_wait_ms"`
	InFlight         int            `json:"in_flight,omitempty"`
	QueueDepth       int            `json:"queue_depth,omitempty"`
	KibanaCalls      []KibanaCall   `json:"kibana_calls"`
	LogBotCalls      []LogBotCall   `json:"logbot_calls"`
	HTTPStatus       int            `json:"http_status"`
	ResultStatus     string         `json:"result_status,omitempty"`
	ResultCode       string         `json:"result_code,omitempty"`
	ResultTotal      *int           `json:"result_total,omitempty"`
	Returned         int            `json:"returned,omitempty"`
	Limitations      []string       `json:"limitations,omitempty"`
	DurationMS       int64          `json:"duration_ms"`
	Error            string         `json:"error,omitempty"`
}

type KibanaCall struct {
	Stage      string `json:"stage,omitempty"`
	BudgetMS   int64  `json:"budget_ms"`
	QueryShape string `json:"query_shape,omitempty"`
	QueryHash  string `json:"query_hash,omitempty"`
	TotalExact bool   `json:"total_exact"`
	Index      string `json:"index,omitempty"`
	Dataset    string `json:"dataset,omitempty"`
	CountOnly  bool   `json:"count_only,omitempty"`
	DurationMS int64  `json:"duration_ms"`
	OK         bool   `json:"ok"`
	HTTPStatus int    `json:"http_status,omitempty"`
	ErrorKind  string `json:"error_kind,omitempty"`
	Total      int    `json:"total,omitempty"`
	Entries    int    `json:"entries,omitempty"`
}

// LogBotCall 记录一次 LogBot HTTP 调用（登录/提交任务/轮询）。
// 绝不包含账号、密码、cookie 或日志行正文。
type LogBotCall struct {
	Stage        string `json:"stage"` // login | submit | poll
	Sources      string `json:"sources,omitempty"`
	HTTPStatus   int    `json:"http_status,omitempty"`
	DurationMS   int64  `json:"duration_ms"`
	JobID        string `json:"job_id,omitempty"`
	SourceStatus string `json:"source_status,omitempty"` // 如 "DA=complete,MTA=limit"
	ErrorKind    string `json:"error_kind,omitempty"`
	OK           bool   `json:"ok"`
}

// maxLogBotCalls 防止长轮询把审计记录撑大；超出后只保留尾部状态。
// LogBot 多分窗查询单请求可达 40-80 次调用（6 个日切 job x submit/poll/results），
// 32 会把重试与终态挤出可视范围，128 覆盖 7 天窗口的最坏 fan-out。
const maxLogBotCalls = 128

func NewRecord(r *http.Request, now time.Time, loc *time.Location) *Record {
	if loc == nil {
		loc = time.UTC
	}
	id := strings.TrimSpace(r.Header.Get("X-Request-Id"))
	if id == "" {
		id = newRequestID()
	}
	return &Record{
		TS:          now.In(loc).Format("2006-01-02T15:04:05.000-07:00"),
		Event:       "gateway_request",
		RequestID:   id,
		ClientIP:    clientIP(r),
		Operation:   strings.TrimPrefix(r.URL.Path, "/"),
		KibanaCalls: []KibanaCall{},
	}
}

func (rec *Record) SetAuth(outcome string) {
	if rec == nil {
		return
	}
	rec.mu.Lock()
	rec.Auth = outcome
	rec.mu.Unlock()
}

func (rec *Record) SetTokenClass(class string) {
	if rec == nil || class == "" {
		return
	}
	rec.mu.Lock()
	rec.TokenClass = class
	rec.mu.Unlock()
}

func (rec *Record) SetQuery(q map[string]any) {
	if rec == nil {
		return
	}
	rec.mu.Lock()
	rec.Query = q
	rec.mu.Unlock()
}

func (rec *Record) SetQueueWait(d time.Duration) {
	if rec == nil {
		return
	}
	rec.mu.Lock()
	rec.QueueWaitMS = d.Milliseconds()
	rec.mu.Unlock()
}

func (rec *Record) SetQueueSnapshot(inFlight, depth int) {
	if rec == nil {
		return
	}
	rec.mu.Lock()
	rec.InFlight = inFlight
	rec.QueueDepth = depth
	rec.mu.Unlock()
}

func (rec *Record) AddKibana(call KibanaCall) {
	if rec == nil {
		return
	}
	rec.mu.Lock()
	rec.KibanaCalls = append(rec.KibanaCalls, call)
	rec.mu.Unlock()
}

func (rec *Record) AddLogBot(call LogBotCall) {
	if rec == nil {
		return
	}
	rec.mu.Lock()
	if len(rec.LogBotCalls) >= maxLogBotCalls {
		// 保留最后一条（终态）之外的最早记录，避免长轮询刷屏。
		rec.LogBotCalls = append(rec.LogBotCalls[:maxLogBotCalls-1], call)
	} else {
		rec.LogBotCalls = append(rec.LogBotCalls, call)
	}
	rec.mu.Unlock()
}

func (rec *Record) SetHTTPStatus(status int) {
	if rec == nil {
		return
	}
	rec.mu.Lock()
	rec.HTTPStatus = status
	rec.mu.Unlock()
}

func (rec *Record) SetResult(status, code string, total *int, returned int, limitations []string) {
	if rec == nil {
		return
	}
	rec.mu.Lock()
	rec.ResultStatus = status
	rec.ResultCode = code
	rec.ResultTotal = total
	rec.Returned = returned
	rec.Limitations = clipStrings(limitations, 20, 400)
	rec.mu.Unlock()
}

func (rec *Record) SetError(code string) {
	if rec == nil || code == "" {
		return
	}
	rec.mu.Lock()
	rec.Error = code
	rec.mu.Unlock()
}

func (rec *Record) SetDuration(d time.Duration) {
	if rec == nil {
		return
	}
	rec.mu.Lock()
	rec.DurationMS = d.Milliseconds()
	rec.mu.Unlock()
}

func (rec *Record) Marshal() []byte {
	if rec == nil {
		return nil
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.KibanaCalls == nil {
		rec.KibanaCalls = []KibanaCall{}
	}
	if rec.LogBotCalls == nil {
		rec.LogBotCalls = []LogBotCall{}
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return nil
	}
	return append(b, '\n')
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		return strings.TrimSpace(r.RemoteAddr)
	}
	return host
}

func newRequestID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func clipStrings(in []string, maxN, maxLen int) []string {
	if len(in) == 0 {
		return nil
	}
	if len(in) > maxN {
		in = in[:maxN]
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if len(s) > maxLen {
			s = s[:maxLen]
		}
		out = append(out, s)
	}
	return out
}

func (rec *Record) SetJira(project, fingerprint, operation string) {
	if rec == nil {
		return
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.Backend = "jira"
	rec.Project = project
	rec.TokenFingerprint = fingerprint
	rec.Operation = operation
	rec.Query = nil
}
func (rec *Record) SetJiraUpstream(status int) {
	if rec == nil {
		return
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.UpstreamStatus = status
}
