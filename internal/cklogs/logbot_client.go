package cklogs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
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
	// LogBotDisplayLimit 是 LogBot 每个来源的展示上限（实测 1000 行，达到时 status=limit）。
	LogBotDisplayLimit = 1000

	logbotDefaultPollInterval = 500 * time.Millisecond
	logbotMaxPollInterval     = 3 * time.Second
	logbotMaxResponseBytes    = 16 << 20
)

// LogBotClient 走 LogBot 异步任务 API：POST /api/query-jobs 提交表单，
// 轮询 GET /api/query-jobs/{job_id} 直到任务到达终态或预算耗尽。
//
// 鉴权以 POST /api/login 表单（username/password）换 cookie 为主，进程内缓存复用；
// 401/重定向时加锁重登一次并重试原请求。Bearer 服务令牌（Token 字段）保留为备选分支。
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
func (c *LogBotClient) rawDo(ctx context.Context, method, path string, form url.Values) (int, []byte, time.Duration, *QueryResult) {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL()+path, body)
	if err != nil {
		return 0, nil, 0, &QueryResult{ErrorKind: "network_error", Message: err.Error()}
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
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
func (c *LogBotClient) call(ctx context.Context, method, path string, form url.Values, record func(status int, dur time.Duration, errKind string)) (int, []byte, *QueryResult) {
	if c.Token == "" {
		if fail := c.ensureSession(ctx); fail != nil {
			return 0, nil, fail
		}
	}
	retried := false
	for {
		status, body, dur, fail := c.rawDo(ctx, method, path, form)
		errKind := ""
		if fail != nil {
			errKind = fail.ErrorKind
		}
		willRelogin := fail == nil && (status == http.StatusUnauthorized || isRedirectStatus(status)) && c.Token == "" && !retried
		if willRelogin {
			errKind = "auth_expired"
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
		return status, body, nil
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
	default:
		return ""
	}
}

type logbotJob struct {
	JobID   string `json:"job_id"`
	Status  string `json:"status"`
	Error   string `json:"error"`
	Message string `json:"message"`
	Result  *struct {
		Data        []map[string]any              `json:"data"`
		Sources     map[string]logbotSourceStatus `json:"sources"`
		ScanEndTime string                        `json:"scan_end_time"`
	} `json:"result"`
}

type logbotSourceStatus struct {
	Status         string `json:"status"`
	Loaded         int    `json:"loaded"`
	Error          string `json:"error"`
	ScannedThrough string `json:"scanned_through"`
	CoveredFrom    string `json:"covered_from"`
}

type logbotJobResult struct {
	Data    []map[string]any
	Sources map[string]logbotSourceStatus
}

var logbotKnownSources = map[string]bool{"DA": true, "MTA": true, "PROXYSVR": true, "webadmin": true}

var logbotKnownStatuses = map[string]bool{"complete": true, "limit": true, "partial": true, "queued": true, "running": true, "failed": true, "expired": true, "skipped_future": true}

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

func sourceStatusSummary(sources map[string]logbotSourceStatus) string {
	if len(sources) == 0 {
		return ""
	}
	parts := make([]string, 0, len(sources))
	for name, st := range sources {
		parts = append(parts, safeSourceName(name)+"="+safeSourceStatus(st.Status))
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// logbotAttempt 记录一次实际发出的 HTTP 请求，用于事后统一写审计。
type logbotAttempt struct {
	status  int
	dur     time.Duration
	errKind string
}

// emitAttempts 在 HTTP 状态映射与任务判定之后写审计：每次请求一条，
// 最后一条带上映射后的错误类型（403/429/4xx/5xx、失败任务、来源失败等）。
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

// submitJob 提交 query-jobs 表单，返回 job_id。
// 403/400/409/422 的说明以固定中文文本加 HTTP 状态为主，上游 detail 仅作脱敏后的附加信息。
func (c *LogBotClient) submitJob(ctx context.Context, form url.Values, sources []string) (string, *QueryResult) {
	src := strings.Join(sources, ",")
	var attempts []logbotAttempt
	status, body, fail := c.call(ctx, http.MethodPost, "/api/query-jobs", form, func(st int, dur time.Duration, errKind string) {
		attempts = append(attempts, logbotAttempt{status: st, dur: dur, errKind: errKind})
	})
	kind := ""
	var result *QueryResult
	var jobID string
	switch {
	case fail != nil:
		kind, result = fail.ErrorKind, fail
	case status == http.StatusOK:
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
		// 实测：暂停提供的日志类型返回 403「该日志类型暂停提供查询和导出」。
		msg := "LogBot 拒绝该查询（HTTP 403）：该日志类型或来源可能暂不支持查询，或当前账号无该来源权限"
		if d := sanitizeUpstreamText(logbotDetail(body), c.secrets()); d != "" {
			msg += "；上游说明：" + d
		}
		kind, result = "not_available", &QueryResult{ErrorKind: "not_available", Message: msg}
	case status == http.StatusTooManyRequests:
		kind, result = "rate_limited", &QueryResult{ErrorKind: "rate_limited", Message: "LogBot 限频（HTTP 429），请放慢提交频率稍后重试"}
	case status == http.StatusBadRequest, status == http.StatusUnprocessableEntity:
		msg := fmt.Sprintf("LogBot 拒绝查询表单（HTTP %d）：请检查时间范围和筛选组合", status)
		if d := sanitizeUpstreamText(logbotDetail(body), c.secrets()); d != "" {
			msg += "；上游说明：" + d
		}
		kind, result = "upstream_error", &QueryResult{ErrorKind: "upstream_error", Message: msg}
	case status == http.StatusConflict:
		msg := "LogBot 任务冲突（HTTP 409）：同条件活动任务仍在运行，请稍后重试"
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
func (c *LogBotClient) getJob(ctx context.Context, jobID string, sources []string) (*logbotJob, *QueryResult) {
	src := strings.Join(sources, ",")
	var attempts []logbotAttempt
	status, body, fail := c.call(ctx, http.MethodGet, "/api/query-jobs/"+url.PathEscape(jobID), nil, func(st int, dur time.Duration, errKind string) {
		attempts = append(attempts, logbotAttempt{status: st, dur: dur, errKind: errKind})
	})
	kind := ""
	var result *QueryResult
	var job logbotJob
	switch {
	case fail != nil:
		kind, result = fail.ErrorKind, fail
	case status == http.StatusOK:
		var parsed struct {
			Job logbotJob `json:"job"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			kind, result = "upstream_error", &QueryResult{ErrorKind: "upstream_error", Message: "LogBot 任务响应解析失败"}
			break
		}
		job = parsed.Job
		if job.JobID == "" {
			job.JobID = jobID
		}
		if job.Status != "" && job.Status != "queued" && job.Status != "running" && job.Status != "success" {
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
	srcStatus := ""
	if status == http.StatusOK {
		srcStatus = sourceStatusSummaryOf(body)
	}
	emitAttempts(ctx, "poll", src, jobID, srcStatus, kind, attempts)
	if result != nil {
		return nil, result
	}
	return &job, nil
}

// sourceStatusSummaryOf 尽力从轮询响应体提取来源状态用于审计；失败时返回空。
func sourceStatusSummaryOf(body []byte) string {
	var parsed struct {
		Job struct {
			Result *struct {
				Sources map[string]logbotSourceStatus `json:"sources"`
			} `json:"result"`
		} `json:"job"`
	}
	if json.Unmarshal(body, &parsed) != nil || parsed.Job.Result == nil {
		return ""
	}
	return sourceStatusSummary(parsed.Job.Result.Sources)
}

// runQuery 提交任务并轮询到终态；轮询总时长受操作预算（ctx 与 Timeout 的较小者）约束。
func (c *LogBotClient) runQuery(ctx context.Context, form url.Values, sources []string) (*logbotJobResult, *QueryResult) {
	timeout := c.timeout()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	jobID, fail := c.submitJob(ctx, form, sources)
	if fail != nil {
		return nil, fail
	}
	interval := c.pollInterval()
	for {
		job, fail := c.getJob(ctx, jobID, sources)
		if fail != nil {
			return nil, fail
		}
		switch job.Status {
		case "queued", "running", "":
			// 继续轮询
		case "success":
			if job.Result == nil {
				return nil, &QueryResult{ErrorKind: "upstream_error", Message: "LogBot 任务成功但缺少 result"}
			}
			c.auditSourceOutcome(ctx, jobID, sources, job.Result.Sources)
			return &logbotJobResult{Data: job.Result.Data, Sources: job.Result.Sources}, nil
		default:
			msg := strings.TrimSpace(job.Error)
			if msg == "" {
				msg = strings.TrimSpace(job.Message)
			}
			msg = sanitizeUpstreamText(msg, c.secrets())
			if msg == "" {
				msg = "status=" + job.Status
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
}

// auditSourceOutcome 在任务成功返回后补一条来源级审计：来源 failed/expired
// 全部失败记 upstream_error，部分失败记 partial_failure，其余为正常结果快照。
func (c *LogBotClient) auditSourceOutcome(ctx context.Context, jobID string, sources []string, got map[string]logbotSourceStatus) {
	failed := 0
	for _, name := range sources {
		st, ok := got[name]
		if !ok || st.Status == "failed" || st.Status == "expired" {
			failed++
		}
	}
	kind := ""
	switch {
	case failed == len(sources) && len(sources) > 0:
		kind = "upstream_error"
	case failed > 0:
		kind = "partial_failure"
	}
	if rec := auditlog.From(ctx); rec != nil {
		rec.AddLogBot(auditlog.LogBotCall{
			Stage: "result", Sources: strings.Join(sources, ","), JobID: jobID,
			SourceStatus: sourceStatusSummary(got), ErrorKind: kind, OK: kind == "",
		})
	}
}

// assessSources 按 job.result.sources 逐来源判断完整性。
// 返回的 QueryResult 填充 Total/Truncated/Partial/Limitations；所有来源都失败时返回 fail。
// secrets 用于上游错误文本脱敏。
func assessSources(sources []string, got map[string]logbotSourceStatus, total int, secrets []string) (QueryResult, *QueryResult) {
	res := QueryResult{OK: true, Total: total}
	var failed []string
	for _, name := range sources {
		st, ok := got[name]
		if !ok {
			failed = append(failed, safeSourceName(name)+"（无来源结果）")
			continue
		}
		name := safeSourceName(name)
		switch st.Status {
		case "complete":
			// 该来源已查完，loaded=0 才能解释为无记录。
		case "limit":
			res.Truncated = true
			res.Limitations = append(res.Limitations, "来源 "+name+" 已达展示上限（已加载 "+itoa(st.Loaded)+" 条），该时段内可能还有更多匹配记录；需要全量结果请缩小时间窗，或由人工走 LogBot 完整导出。")
		case "partial", "queued", "running":
			res.Partial = true
			through := sanitizeUpstreamText(st.ScannedThrough, secrets)
			note := "来源 " + name + " 仍有时段未扫描完成，当前结果不完整，这不等于「没有记录」"
			if through != "" {
				note += "；已扫描至 " + through
			}
			res.Limitations = append(res.Limitations, note+"。")
		case "failed", "expired":
			errMsg := sanitizeUpstreamText(st.Error, secrets)
			if errMsg == "" {
				errMsg = safeSourceStatus(st.Status)
			}
			failed = append(failed, name+"（"+errMsg+"）")
		case "skipped_future":
			// 未来时段未扫描，与 partial 一样不能解释为「精确无记录」。
			res.Partial = true
			res.Limitations = append(res.Limitations, "来源 "+name+"：查询时段在未来，LogBot 跳过读取；该时段尚未扫描，不能据此断言没有记录。")
		default:
			failed = append(failed, name+"（未知来源状态 "+safeSourceStatus(st.Status)+"）")
		}
	}
	if len(failed) > 0 && len(failed) == len(sources) {
		return QueryResult{}, &QueryResult{ErrorKind: "upstream_error", Message: "LogBot 所需来源全部失败：" + strings.Join(failed, "；")}
	}
	for _, f := range failed {
		res.Partial = true
		res.Limitations = append(res.Limitations, "来源 "+f+" 查询失败，该来源记录缺失是「未查到」而非「无记录」。")
	}
	res.TotalExact = !res.Truncated && !res.Partial
	if res.TotalExact {
		res.TotalRelation = "eq"
	}
	return res, nil
}
