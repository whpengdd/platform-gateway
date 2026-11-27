package cklogs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"platform-gateway/internal/auditlog"
)

const (
	// LogBotDefaultBaseURL 是 LogBot 服务的默认地址（内网 172.16.1.10，部署机 hosts 解析）。
	LogBotDefaultBaseURL = "https://logbot.icoremail.net"
	// LogBotDisplayLimit 是请求侧 max_results / 上游快照的行上限（实测 snapshot 上限 1000 行）。
	LogBotDisplayLimit = 1000
	// logbotAgentMaxWindow 是 agent API 单次任务的时间窗上限（实测 24h00m00s 放行、+1s 即 400）。
	logbotAgentMaxWindow = 24 * time.Hour

	logbotDefaultPollInterval = 500 * time.Millisecond
	logbotMaxPollInterval     = 3 * time.Second
	// logbot 限频重试默认值：429 时按 base×2^n + jitter 退避，
	// 最多重试 maxRetries 次；预算受 ctx 剩余时间约束（见 call）。
	logbotDefaultRateLimitRetries = 3
	logbotDefaultRateLimitBackoff = 500 * time.Millisecond
	logbotRateLimitBackoffCap     = 4 * time.Second
	logbotRateLimitRetryMargin    = 3 * time.Second
	logbotMaxResponseBytes        = 16 << 20
	// logbotResultsPageSize 是 results 端点实测上限（page_size=100 放行、200 即 400）。
	logbotResultsPageSize = 100
)

// LogBotClient 走 LogBot agent 异步任务 API：
// POST /api/agent/log-query/jobs 提交 JSON 任务，轮询 GET /jobs/{job_id}
// 到终态后经 GET /jobs/{job_id}/results 按 page_token 翻页取回行。
// 单次任务时间窗上限 24h；调用方负责把更长窗口切分。
//
// 鉴权以管理员发放的 Bearer 服务令牌为主（auth.type=bearer）；
// 表单登录（username/password）换 cookie 作为备选，401/重定向时加锁重登一次。
// 账号、密码、cookie、令牌绝不进入日志、审计或响应。
type LogBotClient struct {
	BaseURL      string
	Username     string
	Password     string
	Token        string
	PollInterval time.Duration
	Timeout      time.Duration
	// HTTPClient 必须不跟随重定向（302 会把请求带去 SSO 登录页）；
	// 为空时使用内部构造的不跳转客户端。
	HTTPClient *http.Client
	// RateLimitMaxRetries/RateLimitBackoff 控制 429 重试：
	// 0 表示用默认值（3 次 / 500ms 起步）；负数禁用重试。
	RateLimitMaxRetries int
	RateLimitBackoff    time.Duration

	mu      sync.Mutex
	cookies []*http.Cookie
}

func (c *LogBotClient) Configured() bool {
	return c != nil && (c.Token != "" || (c.Username != "" && c.Password != ""))
}

func (c *LogBotClient) timeout() time.Duration {
	if c != nil && c.Timeout > 0 {
		return c.Timeout
	}
	return DefaultOpTimeout
}

func (c *LogBotClient) pollInterval() time.Duration {
	if c != nil && c.PollInterval > 0 {
		return c.PollInterval
	}
	return logbotDefaultPollInterval
}

func (c *LogBotClient) rateLimitRetries() int {
	if c.RateLimitMaxRetries < 0 {
		return 0
	}
	if c.RateLimitMaxRetries > 0 {
		return c.RateLimitMaxRetries
	}
	return logbotDefaultRateLimitRetries
}

func (c *LogBotClient) rateLimitBackoff() time.Duration {
	if c.RateLimitBackoff > 0 {
		return c.RateLimitBackoff
	}
	return logbotDefaultRateLimitBackoff
}

// rateLimitSleep 计算第 n 次（0 起）429 重试的退避时长：base×2^n 封顶，
// 加 ±25% jitter 避免多客户端同拍。睡眠时间不可超过 ctx 剩余预算减去余量
// （无 deadline 时不受限），返回 ok=false 表示预算不足、不应重试。
func (c *LogBotClient) rateLimitSleep(ctx context.Context, n int) (time.Duration, bool) {
	d := c.rateLimitBackoff() << n
	if d > logbotRateLimitBackoffCap {
		d = logbotRateLimitBackoffCap
	}
	jitter := time.Duration(rand.Float64() * float64(d) / 2)
	d = d - time.Duration(float64(d)/4) + jitter
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining < d+logbotRateLimitRetryMargin {
			return 0, false
		}
	}
	return d, true
}

func (c *LogBotClient) baseURL() string {
	u := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if u == "" {
		return LogBotDefaultBaseURL
	}
	return u
}

func (c *LogBotClient) http() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	// 默认客户端禁止跟随重定向：未认证时 LogBot 会 302 到 SSO 登录页，
	// 跟随跳转会把查询参数泄漏给 SSO。
	return &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func isRedirectStatus(status int) bool {
	switch status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	}
	return false
}

// rawDo 执行一次 HTTP 调用（已带鉴权），返回状态码与响应体；只在网络/超时/读失败时返回 fail。
// 调用方负责审计。
func (c *LogBotClient) rawDo(ctx context.Context, method, path string, body []byte, contentType string) (int, []byte, time.Duration, *QueryResult) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL()+path, rd)
	if err != nil {
		return 0, nil, 0, &QueryResult{ErrorKind: "network_error", Message: err.Error()}
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	} else {
		c.mu.Lock()
		cookies := append([]*http.Cookie(nil), c.cookies...)
		c.mu.Unlock()
		for _, ck := range cookies {
			req.AddCookie(ck)
		}
	}
	started := time.Now()
	resp, err := c.http().Do(req)
	dur := time.Since(started)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return 0, nil, dur, &QueryResult{ErrorKind: "timeout", Message: timeoutMessage()}
		}
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			return 0, nil, dur, &QueryResult{ErrorKind: "canceled", Message: "logbot request canceled"}
		}
		return 0, nil, dur, &QueryResult{ErrorKind: "network_error", Message: err.Error()}
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, logbotMaxResponseBytes+1))
	if readErr != nil || len(raw) > logbotMaxResponseBytes {
		return resp.StatusCode, nil, dur, &QueryResult{ErrorKind: "upstream_error", Message: "LogBot 响应不完整或超过大小上限"}
	}
	return resp.StatusCode, raw, dur, nil
}

// login 用表单登录换取会话 cookie。调用时必须已持有 c.mu，或确认无需去重（见 ensureSession/relogin）。
// 返回非 nil 表示失败；错误信息不含凭据。
func (c *LogBotClient) login(ctx context.Context) *QueryResult {
	form := url.Values{"username": {c.Username}, "password": {c.Password}}
	call := auditlog.LogBotCall{Stage: "login"}
	started := time.Now()
	defer func() {
		call.DurationMS = time.Since(started).Milliseconds()
		if rec := auditlog.From(ctx); rec != nil {
			rec.AddLogBot(call)
		}
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL()+"/api/login", strings.NewReader(form.Encode()))
	if err != nil {
		call.ErrorKind = "network_error"
		return &QueryResult{ErrorKind: "network_error", Message: err.Error()}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http().Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			call.ErrorKind = "timeout"
			return &QueryResult{ErrorKind: "timeout", Message: timeoutMessage()}
		}
		call.ErrorKind = "network_error"
		return &QueryResult{ErrorKind: "network_error", Message: err.Error()}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	call.HTTPStatus = resp.StatusCode
	if resp.StatusCode != http.StatusOK || isRedirectStatus(resp.StatusCode) {
		call.ErrorKind = "auth_error"
		return &QueryResult{ErrorKind: "auth_error", Message: fmt.Sprintf("LogBot 登录失败：HTTP %d", resp.StatusCode)}
	}
	cookies := resp.Cookies()
	if len(cookies) == 0 {
		call.ErrorKind = "auth_error"
		return &QueryResult{ErrorKind: "auth_error", Message: "LogBot 登录响应未携带会话 cookie"}
	}
	c.cookies = cookies
	call.OK = true
	return nil
}

// ensureSession 保证有缓存会话（仅登录模式）；已缓存则直接复用。
func (c *LogBotClient) ensureSession(ctx context.Context) *QueryResult {
	if c.Token != "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.cookies) > 0 {
		return nil
	}
	return c.login(ctx)
}

// relogin 加锁强制重登一次（会话失效场景）。
func (c *LogBotClient) relogin(ctx context.Context) *QueryResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.login(ctx)
}

// call 执行一次带鉴权的业务请求；登录模式下 401/重定向时加锁重登一次并重试原请求。
// 每次实际发出的 HTTP 请求通过 record 回调记录（状态、耗时、网络层错误），
// 由调用方在完成状态映射/任务判定后统一写审计（错误类型以映射结果为准）。
func (c *LogBotClient) call(ctx context.Context, method, path string, body []byte, contentType string, record func(status int, dur time.Duration, errKind string)) (int, []byte, *QueryResult) {
	if c.Token == "" {
		if fail := c.ensureSession(ctx); fail != nil {
			return 0, nil, fail
		}
	}
	retried := false
	rateRetries := 0
	for {
		status, raw, dur, fail := c.rawDo(ctx, method, path, body, contentType)
		errKind := ""
		if fail != nil {
			errKind = fail.ErrorKind
		}
		willRelogin := fail == nil && (status == http.StatusUnauthorized || isRedirectStatus(status)) && c.Token == "" && !retried
		if willRelogin {
			errKind = "auth_expired"
		}
		is429 := fail == nil && status == http.StatusTooManyRequests
		if is429 {
			errKind = "rate_limited"
		}
		if record != nil {
			record(status, dur, errKind)
		}
		if fail != nil {
			return status, nil, fail
		}
		if willRelogin {
			retried = true
			if fail := c.relogin(ctx); fail != nil {
				return status, nil, fail
			}
			continue
		}
		if is429 && rateRetries < c.rateLimitRetries() {
			if sleep, ok := c.rateLimitSleep(ctx, rateRetries); ok {
				rateRetries++
				timer := time.NewTimer(sleep)
				select {
				case <-ctx.Done():
					timer.Stop()
					// 上下文到期/取消：不再重试，把 429 交还调用方如实映射。
				case <-timer.C:
					continue
				}
			}
		}
		return status, raw, nil
	}
}

// logbotSensitiveKV 匹配 key=value / key: value 形式的敏感片段，整体脱敏。
var logbotSensitiveKV = regexp.MustCompile(`(?i)(cookie|set-cookie|password|passwd|secret|token|authorization|session)[\w-]{0,20}\s*[:=]\s*[^\s;,]{1,256}`)

// logbotBearerRe 匹配游离的 Bearer 令牌片段（如 "Bearer abc.def"）。
var logbotBearerRe = regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/=-]{4,}`)

// sanitizeUpstreamText 处理一切进入响应/审计的上游文本：去除控制字符、
// 脱敏已知敏感模式与当前凭据字面值、限长 200 字符。
func sanitizeUpstreamText(s string, secrets []string) string {
	if s == "" {
		return ""
	}
	s = strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return ' '
		}
		if unicode.IsControl(r) || r == utf8.RuneError {
			return -1
		}
		return r
	}, s)
	for _, sec := range secrets {
		if sec != "" {
			s = strings.ReplaceAll(s, sec, "***")
		}
	}
	// 先吃 Bearer 令牌，再吃 key=value：避免 "authorization: Bearer x" 只剩裸令牌。
	s = logbotBearerRe.ReplaceAllString(s, "***")
	s = logbotSensitiveKV.ReplaceAllString(s, "***")
	const maxRunes = 200
	if utf8.RuneCountInString(s) > maxRunes {
		s = string([]rune(s)[:maxRunes]) + "…"
	}
	return strings.TrimSpace(s)
}

// secrets 返回当前配置与会话中的敏感字面值，用于上游文本脱敏。
func (c *LogBotClient) secrets() []string {
	out := []string{}
	if c.Username != "" {
		out = append(out, c.Username)
	}
	if c.Password != "" {
		out = append(out, c.Password)
	}
	if c.Token != "" {
		out = append(out, c.Token)
	}
	c.mu.Lock()
	for _, ck := range c.cookies {
		if ck.Value != "" {
			out = append(out, ck.Value)
		}
	}
	c.mu.Unlock()
	return out
}

// logbotDetail 提取 LogBot 错误响应的 detail 字段（字符串或 FastAPI 422 的对象列表）。
func logbotDetail(body []byte) string {
	var parsed struct {
		Detail any `json:"detail"`
	}
	if json.Unmarshal(body, &parsed) != nil {
		return ""
	}
	switch v := parsed.Detail.(type) {
	case string:
		return strings.TrimSpace(v)
	case []any:
		// FastAPI 校验错误是对象列表；只取每条 msg，丢弃 input 等可能回显的内容。
		msgs := make([]string, 0, len(v))
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				if msg, ok := m["msg"].(string); ok && strings.TrimSpace(msg) != "" {
					msgs = append(msgs, strings.TrimSpace(msg))
				}
			}
		}
		return strings.Join(msgs, "; ")
	default:
		return ""
	}
}

// ── agent API 请求/响应结构 ──────────────────────────────────────────

// logbotAgentRequest 是 POST /api/agent/log-query/jobs 的 JSON 请求体。
// start_time/end_time 为无时区上海本地时间 "YYYY-MM-DD HH:MM:SS"，区间 [start,end)。
type logbotAgentRequest struct {
	QueryType    string         `json:"query_type"`
	PlatformMode string         `json:"platform_mode,omitempty"`
	StartTime    string         `json:"start_time"`
	EndTime      string         `json:"end_time"`
	Filters      map[string]any `json:"filters,omitempty"`
	LogSources   []string       `json:"log_sources,omitempty"`
	PageSize     int            `json:"page_size,omitempty"`
	MaxResults   int            `json:"max_results,omitempty"`
	Purpose      string         `json:"purpose"`
}

type logbotAgentJob struct {
	JobID       string `json:"job_id"`
	Status      string `json:"status"`
	Error       string `json:"error"`
	Message     string `json:"message"`
	ResultCount *int   `json:"result_count"`
}

type logbotAgentResults struct {
	Status            string           `json:"status"`
	Rows              []map[string]any `json:"rows"`
	TotalCount        int              `json:"total_count"`
	LimitsApplied     map[string]any   `json:"limits_applied"`
	NextPageToken     string           `json:"next_page_token"`
	SnapshotTruncated bool             `json:"snapshot_truncated"`
	RetainedWindow    map[string]any   `json:"retained_window"`
}

// logbotJobResult 是一次（可能跨多个时间窗分段的）agent 查询的归并结果。
type logbotJobResult struct {
	Rows       []map[string]any
	Total      int  // 各分段 total_count 之和（明细模式下为去重后行数）
	TotalExact bool // 快照未截断、全部分段完成时才为真
	Truncated  bool // 任一分段 snapshot_truncated 或命中行上限
}

var logbotKnownSources = map[string]bool{"DA": true, "MTA": true, "PROXYSVR": true, "webadmin": true, "user_operation": true}

var logbotKnownStatuses = map[string]bool{"success": true, "queued": true, "running": true, "failed": true, "expired": true, "canceled": true}

// safeSourceName 只放行白名单来源名，其余一律记为 unknown，避免上游反射内容进入审计/响应。
func safeSourceName(name string) string {
	if logbotKnownSources[name] {
		return name
	}
	return "unknown"
}

// safeSourceStatus 只放行已知状态枚举，其余记为 unknown。
func safeSourceStatus(status string) string {
	if logbotKnownStatuses[status] {
		return status
	}
	return "unknown"
}

// logbotAttempt 记录一次实际发出的 HTTP 请求，用于事后统一写审计。
type logbotAttempt struct {
	status  int
	dur     time.Duration
	errKind string
}

// emitAttempts 在 HTTP 状态映射与任务判定之后写审计：每次请求一条，
// 最后一条带上映射后的错误类型（403/429/4xx/5xx、失败任务等）。
func emitAttempts(ctx context.Context, stage, src, jobID, srcStatus, finalKind string, attempts []logbotAttempt) {
	rec := auditlog.From(ctx)
	if rec == nil {
		return
	}
	for i, a := range attempts {
		errKind := a.errKind
		if i == len(attempts)-1 && finalKind != "" {
			errKind = finalKind
		}
		rec.AddLogBot(auditlog.LogBotCall{
			Stage: stage, Sources: src, HTTPStatus: a.status,
			DurationMS: a.dur.Milliseconds(), JobID: jobID,
			SourceStatus: srcStatus, ErrorKind: errKind,
			OK: errKind == "" && a.status == http.StatusOK,
		})
	}
}

// submitJob 提交 agent 查询任务，返回 job_id。成功响应为 HTTP 202。
func (c *LogBotClient) submitJob(ctx context.Context, req logbotAgentRequest, sources []string) (string, *QueryResult) {
	src := strings.Join(sources, ",")
	payload, err := json.Marshal(req)
	if err != nil {
		return "", &QueryResult{ErrorKind: "upstream_error", Message: "LogBot 请求序列化失败"}
	}
	var attempts []logbotAttempt
	status, body, fail := c.call(ctx, http.MethodPost, "/api/agent/log-query/jobs", payload, "application/json", func(st int, dur time.Duration, errKind string) {
		attempts = append(attempts, logbotAttempt{status: st, dur: dur, errKind: errKind})
	})
	kind := ""
	var result *QueryResult
	var jobID string
	switch {
	case fail != nil:
		kind, result = fail.ErrorKind, fail
	case status == http.StatusAccepted || status == http.StatusOK:
		var parsed struct {
			JobID string `json:"job_id"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil || parsed.JobID == "" {
			kind, result = "upstream_error", &QueryResult{ErrorKind: "upstream_error", Message: "LogBot 提交响应缺少 job_id"}
		} else {
			jobID = parsed.JobID
		}
	case status == http.StatusUnauthorized || isRedirectStatus(status):
		kind, result = "auth_error", &QueryResult{ErrorKind: "auth_error", Message: fmt.Sprintf("LogBot 鉴权失败：HTTP %d", status)}
	case status == http.StatusForbidden:
		msg := "LogBot 拒绝该查询（HTTP 403）：该日志类型或来源可能暂不支持查询，或当前账号无该来源权限"
		if d := sanitizeUpstreamText(logbotDetail(body), c.secrets()); d != "" {
			msg += "；上游说明：" + d
		}
		kind, result = "not_available", &QueryResult{ErrorKind: "not_available", Message: msg}
	case status == http.StatusTooManyRequests:
		kind, result = "rate_limited", &QueryResult{ErrorKind: "rate_limited", Message: "LogBot 限频（HTTP 429），请放慢提交频率稍后重试"}
	case status == http.StatusBadRequest, status == http.StatusUnprocessableEntity:
		msg := fmt.Sprintf("LogBot 拒绝查询请求（HTTP %d）：请检查时间范围（单任务上限 24h）和筛选组合", status)
		if d := sanitizeUpstreamText(logbotDetail(body), c.secrets()); d != "" {
			msg += "；上游说明：" + d
		}
		kind, result = "upstream_error", &QueryResult{ErrorKind: "upstream_error", Message: msg}
	default:
		kind, result = "upstream_error", &QueryResult{ErrorKind: "upstream_error", Message: fmt.Sprintf("LogBot 提交任务失败：HTTP %d", status)}
	}
	emitAttempts(ctx, "submit", src, jobID, "", kind, attempts)
	return jobID, result
}

// getJob 轮询一次任务状态。HTTP 200 但任务到达失败终态时，审计同样带 upstream_error。
func (c *LogBotClient) getJob(ctx context.Context, jobID string, sources []string) (*logbotAgentJob, *QueryResult) {
	src := strings.Join(sources, ",")
	var attempts []logbotAttempt
	status, body, fail := c.call(ctx, http.MethodGet, "/api/agent/log-query/jobs/"+url.PathEscape(jobID), nil, "", func(st int, dur time.Duration, errKind string) {
		attempts = append(attempts, logbotAttempt{status: st, dur: dur, errKind: errKind})
	})
	kind := ""
	var result *QueryResult
	var job logbotAgentJob
	switch {
	case fail != nil:
		kind, result = fail.ErrorKind, fail
	case status == http.StatusOK:
		if err := json.Unmarshal(body, &job); err != nil {
			kind, result = "upstream_error", &QueryResult{ErrorKind: "upstream_error", Message: "LogBot 任务响应解析失败"}
			break
		}
		if job.JobID == "" {
			job.JobID = jobID
		}
		switch job.Status {
		case "", "queued", "running", "success":
		default:
			kind = "upstream_error"
		}
	case status == http.StatusUnauthorized || isRedirectStatus(status):
		kind, result = "auth_error", &QueryResult{ErrorKind: "auth_error", Message: fmt.Sprintf("LogBot 鉴权失败：HTTP %d", status)}
	case status == http.StatusForbidden:
		kind, result = "auth_error", &QueryResult{ErrorKind: "auth_error", Message: "LogBot 拒绝读取任务（HTTP 403）"}
	case status == http.StatusNotFound:
		kind, result = "upstream_error", &QueryResult{ErrorKind: "upstream_error", Message: "LogBot 任务不存在或已清理（HTTP 404）"}
	case status == http.StatusTooManyRequests:
		kind, result = "rate_limited", &QueryResult{ErrorKind: "rate_limited", Message: "LogBot 限频（HTTP 429），请放慢提交频率稍后重试"}
	default:
		kind, result = "upstream_error", &QueryResult{ErrorKind: "upstream_error", Message: fmt.Sprintf("LogBot 轮询失败：HTTP %d", status)}
	}
	emitAttempts(ctx, "poll", src, jobID, safeSourceStatus(job.Status), kind, attempts)
	if result != nil {
		return nil, result
	}
	return &job, nil
}

// fetchResults 按 page_token 翻页取回任务结果，直到游标耗尽或达到 maxRows。
// 返回聚合后的结果；lastPage 记录最后一页的完整性标志。
func (c *LogBotClient) fetchResults(ctx context.Context, jobID string, sources []string, maxRows int) (*logbotAgentResults, *QueryResult) {
	src := strings.Join(sources, ",")
	merged := &logbotAgentResults{Rows: []map[string]any{}}
	pageToken := ""
	for {
		path := "/api/agent/log-query/jobs/" + url.PathEscape(jobID) + "/results?page_size=" + itoa(logbotResultsPageSize)
		if pageToken != "" {
			path += "&page_token=" + url.QueryEscape(pageToken)
		}
		var attempts []logbotAttempt
		status, body, fail := c.call(ctx, http.MethodGet, path, nil, "", func(st int, dur time.Duration, errKind string) {
			attempts = append(attempts, logbotAttempt{status: st, dur: dur, errKind: errKind})
		})
		kind := ""
		var result *QueryResult
		var page logbotAgentResults
		switch {
		case fail != nil:
			kind, result = fail.ErrorKind, fail
		case status == http.StatusOK:
			if err := json.Unmarshal(body, &page); err != nil {
				kind, result = "upstream_error", &QueryResult{ErrorKind: "upstream_error", Message: "LogBot 结果响应解析失败"}
			}
		case status == http.StatusUnauthorized || isRedirectStatus(status):
			kind, result = "auth_error", &QueryResult{ErrorKind: "auth_error", Message: fmt.Sprintf("LogBot 鉴权失败：HTTP %d", status)}
		case status == http.StatusNotFound:
			kind, result = "upstream_error", &QueryResult{ErrorKind: "upstream_error", Message: "LogBot 任务结果不存在或已清理（HTTP 404）"}
		case status == http.StatusTooManyRequests:
			kind, result = "rate_limited", &QueryResult{ErrorKind: "rate_limited", Message: "LogBot 限频（HTTP 429），请放慢提交频率稍后重试"}
		default:
			kind, result = "upstream_error", &QueryResult{ErrorKind: "upstream_error", Message: fmt.Sprintf("LogBot 读取结果失败：HTTP %d", status)}
		}
		emitAttempts(ctx, "results", src, jobID, safeSourceStatus(page.Status), kind, attempts)
		if result != nil {
			return nil, result
		}
		merged.Rows = append(merged.Rows, page.Rows...)
		merged.TotalCount = page.TotalCount
		merged.LimitsApplied = page.LimitsApplied
		merged.RetainedWindow = page.RetainedWindow
		merged.SnapshotTruncated = merged.SnapshotTruncated || page.SnapshotTruncated
		merged.Status = page.Status
		pageToken = page.NextPageToken
		if pageToken == "" || len(merged.Rows) >= maxRows {
			break
		}
	}
	if len(merged.Rows) > maxRows {
		merged.Rows = merged.Rows[:maxRows]
		// maxRows==1 是 countOnly 模式：只取总数，截断不是上游截断语义。
		if maxRows > 1 {
			merged.SnapshotTruncated = true
		}
	}
	return merged, nil
}

// runQuery 提交 agent 任务、轮询到终态并取回结果。
// wantRows=false（countOnly）时只取一页结果拿 total_count，不翻页拉明细。
// 轮询与取数总时长受操作预算（ctx 与 Timeout 的较小者）约束。
func (c *LogBotClient) runQuery(ctx context.Context, req logbotAgentRequest, sources []string, maxRows int, wantRows bool) (*logbotJobResult, *QueryResult) {
	timeout := c.timeout()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	jobID, fail := c.submitJob(ctx, req, sources)
	if fail != nil {
		return nil, fail
	}
	interval := c.pollInterval()
	var job *logbotAgentJob
	for {
		var fail *QueryResult
		job, fail = c.getJob(ctx, jobID, sources)
		if fail != nil {
			return nil, fail
		}
		switch job.Status {
		case "queued", "running", "":
			// 继续轮询
		case "success":
			goto DONE
		default:
			msg := strings.TrimSpace(job.Error)
			if msg == "" {
				msg = strings.TrimSpace(job.Message)
			}
			msg = sanitizeUpstreamText(msg, c.secrets())
			if msg == "" {
				msg = "status=" + safeSourceStatus(job.Status)
			}
			return nil, &QueryResult{ErrorKind: "upstream_error", Message: "LogBot 任务失败：" + msg}
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, &QueryResult{ErrorKind: "timeout", Message: timeoutMessage()}
			}
			return nil, &QueryResult{ErrorKind: "canceled", Message: "logbot request canceled"}
		case <-timer.C:
		}
		interval *= 2
		if interval > logbotMaxPollInterval {
			interval = logbotMaxPollInterval
		}
	}
DONE:
	limit := maxRows
	if limit <= 0 || limit > LogBotDisplayLimit {
		limit = LogBotDisplayLimit
	}
	if !wantRows {
		limit = 1
	}
	res, fail := c.fetchResults(ctx, jobID, sources, limit)
	if fail != nil {
		return nil, fail
	}
	c.auditJobOutcome(ctx, jobID, sources, res)
	out := &logbotJobResult{Rows: res.Rows, Truncated: res.SnapshotTruncated, TotalExact: !res.SnapshotTruncated}
	if wantRows {
		out.Total = len(res.Rows)
	} else {
		out.Total = res.TotalCount
	}
	return out, nil
}

// auditJobOutcome 在任务成功返回后补一条结果级审计快照。
func (c *LogBotClient) auditJobOutcome(ctx context.Context, jobID string, sources []string, res *logbotAgentResults) {
	kind := ""
	if res.SnapshotTruncated {
		kind = "partial_failure"
	}
	if rec := auditlog.From(ctx); rec != nil {
		rec.AddLogBot(auditlog.LogBotCall{
			Stage: "result", Sources: strings.Join(sources, ","), JobID: jobID,
			SourceStatus: safeSourceStatus(res.Status), ErrorKind: kind, OK: kind == "",
		})
	}
}

// logbotResultToQuery 把 agent 结果映射为 QueryResult：按 log_source 过滤、
// 整行去重后映射 Entry；snapshot_truncated 映射为 Truncated+limitation，
// DA 缺分组键的限制说明另计。TotalExact 为假时不标 eq，调用方据此
// 区分「精确无记录」与「快照不完整」。
func logbotResultToQuery(res *logbotJobResult, source string, countOnly bool, secrets []string) QueryResult {
	var rows []map[string]any
	seen := map[string]bool{}
	for _, row := range res.Rows {
		if rowSource := asString(row["log_source"]); rowSource != "" && !strings.EqualFold(rowSource, source) {
			continue
		}
		key := logbotRowKey(row, source)
		if seen[key] {
			continue
		}
		seen[key] = true
		rows = append(rows, row)
	}
	unkeyed := 0
	if source == logbotSourceDA {
		rows, unkeyed = mergeLogbotDARows(rows)
	}
	var entries []Entry
	if !countOnly {
		entries = make([]Entry, 0, len(rows))
		for _, row := range rows {
			entries = append(entries, mapLogBotRow(row, source))
		}
		sortEntriesDesc(entries)
	}
	out := QueryResult{OK: true, Entries: entries}
	if countOnly {
		out.Total = res.Total
	} else {
		out.Total = len(rows)
	}
	if res.Truncated {
		out.Truncated = true
		out.Limitations = append(out.Limitations, "结果命中 LogBot 快照上限（单任务约 1000 行），该时段内可能还有更多匹配记录；需要全量结果请缩小时间窗。这不等于「没有记录」。")
	}
	if unkeyed > 0 {
		out.Limitations = append(out.Limitations, "LogBot agent 接口的 DA 行不含 message-id（hdrmsgid）与投递关联键（commrefid），同一封邮件的多次投递动作无法归并，已按行逐条保留；条数不代表邮件封数。")
	}
	if source == logbotSourceDA {
		for _, row := range rows {
			if strings.TrimSpace(asString(row["cmd"])) == "" {
				out.Limitations = append(out.Limitations, "LogBot agent 接口的 DA 行不含 cmd 列，无法区分投递动作类型（local/remote/antivirus），投递状态保持 unknown 不臆断。")
				break
			}
		}
	}
	out.TotalExact = res.TotalExact && !out.Truncated
	if out.TotalExact {
		out.TotalRelation = "eq"
	}
	return out
}
