package config

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type CustomerLookup struct {
	BaseURL string `json:"baseUrl"`
	APIKey  string `json:"apiKey"`
}

type UpstreamAuth struct {
	Type     string `json:"type"`
	Token    string `json:"token,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}
type Jira struct {
	BaseURL                string             `json:"baseUrl"`
	Auth                   *UpstreamAuth      `json:"auth"`
	Projects               map[string]Project `json:"projects"`
	AccountCacheTtlSeconds int64              `json:"accountCacheTtlSeconds"`
	MaxFieldBytes          int64              `json:"maxFieldBytes"`
}
type CKLogs struct {
	BaseURL   string        `json:"baseUrl"`
	Provider  string        `json:"provider"`
	Auth      *UpstreamAuth `json:"auth"`
	Index     string        `json:"index"`
	TimeoutMS int64         `json:"timeoutMs"`
	Queue     Queue         `json:"queue"`
	LogBot    *LogBot       `json:"logbot,omitempty"`
}

// LogBot 是 cklogs.provider=logbot 时的后端配置。
// auth.type=login（username+password 表单登录换 cookie，实测可用）；
// auth.type=bearer（管理员发放的服务令牌）保留为备选。
type LogBot struct {
	BaseURL        string        `json:"baseUrl"`
	Auth           *UpstreamAuth `json:"auth,omitempty"`
	PollIntervalMS int64         `json:"pollIntervalMs"`
	TimeoutMS      int64         `json:"timeoutMs"`
}
type Queue struct {
	MaxConcurrency int   `json:"maxConcurrency"`
	Size           int   `json:"size"`
	WaitTimeoutMS  int64 `json:"waitTimeoutMs"`
}
type Server struct {
	ListenAddr string   `json:"listenAddr"`
	AllowCIDRs []string `json:"allowCidrs"`
}
type Audit struct {
	Enabled bool   `json:"enabled"`
	Dir     string `json:"dir"`
}

const MaxTimeoutMS = int64((1<<63 - 1 - 5*time.Second) / time.Millisecond)
const MaxWaitTimeoutMS = int64((1<<63 - 1) / time.Millisecond)

func defaults() File {
	return File{Server: Server{ListenAddr: ":8091", AllowCIDRs: []string{}}, Audit: Audit{Enabled: true, Dir: "logs"}, CKLogs: CKLogs{BaseURL: "https://ck-logs.icoremail.net", Provider: "kibana", Index: "mtatrans_distributed", TimeoutMS: 60000, Queue: Queue{MaxConcurrency: 2, Size: 32, WaitTimeoutMS: 30000}}}
}

func validURL(raw string, jira bool) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") || (u.Scheme != "https" && u.Scheme != "http") {
		return false
	}
	if port := u.Port(); port != "" {
		n, e := strconv.Atoi(port)
		if e != nil || n < 1 || n > 65535 {
			return false
		}
	}
	if jira {
		if u.RawPath != "" || strings.ContainsAny(u.Path, "\\%") {
			return false
		}
		for _, part := range strings.Split(u.Path, "/") {
			if part == "." || part == ".." {
				return false
			}
		}
	}
	return true
}
func validateAuth(a *UpstreamAuth, raw map[string]json.RawMessage, path string, jira bool) error {
	if a == nil {
		return nil
	}
	for _, field := range []struct{ name, value string }{{"token", a.Token}, {"username", a.Username}, {"password", a.Password}} {
		if strings.ContainsAny(field.value, "\r\n") {
			return invalid(path+"."+field.name, "must not contain CR or LF")
		}
	}
	_, token := raw["token"]
	_, user := raw["username"]
	_, pass := raw["password"]
	switch a.Type {
	case "bearer":
		if !jira {
			return invalid(path+".type", "must be basic for cklogs")
		}
		if user || pass {
			return invalid(path, "bearer authentication must not include username or password")
		}
		if a.Token == "" {
			return invalid(path+".token", "required for bearer authentication")
		}
	case "basic":
		if token {
			return invalid(path, "basic authentication must not include token")
		}
		if a.Username == "" {
			return invalid(path+".username", "required for basic authentication")
		}
		if a.Password == "" {
			return invalid(path+".password", "required for basic authentication")
		}
	default:
		return invalid(path+".type", "must be basic or bearer (cklogs supports only basic)")
	}
	return nil
}

// validateLogBotAuth 校验 cklogs.logbot.auth：login（表单登录）或 bearer（服务令牌）。
// 令牌拒绝占位符与空白；错误信息不 echo 任何凭据取值。
func validateLogBotAuth(a *UpstreamAuth, raw map[string]json.RawMessage, path string) error {
	if a == nil {
		return nil
	}
	for _, field := range []struct{ name, value string }{{"token", a.Token}, {"username", a.Username}, {"password", a.Password}} {
		if strings.ContainsAny(field.value, "\r\n") {
			return invalid(path+"."+field.name, "must not contain CR or LF")
		}
	}
	_, token := raw["token"]
	_, user := raw["username"]
	_, pass := raw["password"]
	switch a.Type {
	case "login":
		if token {
			return invalid(path, "login authentication must not include token")
		}
		if a.Username == "" {
			return invalid(path+".username", "required for login authentication")
		}
		if a.Password == "" {
			return invalid(path+".password", "required for login authentication")
		}
	case "bearer":
		if user || pass {
			return invalid(path, "bearer authentication must not include username or password")
		}
		if a.Token == "" {
			return invalid(path+".token", "required for bearer authentication")
		}
		t := strings.TrimSpace(a.Token)
		lower := strings.ToLower(t)
		if t != a.Token || strings.Contains(lower, "replace") || strings.Contains(lower, "changeme") || strings.Contains(lower, "change-me") || strings.Contains(lower, "your-token") || strings.Contains(lower, "placeholder") {
			return invalid(path+".token", "must contain no surrounding whitespace and not be a placeholder")
		}
	default:
		return invalid(path+".type", "must be login or bearer")
	}
	return nil
}

func (f *File) validateApplication(b []byte) error {
	var raw struct {
		Jira struct {
			BaseURL                *string                    `json:"baseUrl"`
			Auth                   map[string]json.RawMessage `json:"auth"`
			AccountCacheTtlSeconds *int64                     `json:"accountCacheTtlSeconds"`
			MaxFieldBytes          *int64                     `json:"maxFieldBytes"`
		} `json:"jira"`
		CKLogs struct {
			Auth   map[string]json.RawMessage `json:"auth"`
			LogBot struct {
				Auth map[string]json.RawMessage `json:"auth"`
			} `json:"logbot"`
		} `json:"cklogs"`
	}
	if json.Unmarshal(b, &raw) != nil {
		return invalid("$", "invalid JSON structure")
	}
	if f.CustomerLookup != nil {
		if !validURL(f.CustomerLookup.BaseURL, true) {
			return invalid("customerLookup.baseUrl", "must be an absolute HTTP or HTTPS URL without query, fragment, userinfo or unsafe path")
		}
		if f.CustomerLookup.APIKey == "" || strings.ContainsAny(f.CustomerLookup.APIKey, "\r\n") {
			return invalid("customerLookup.apiKey", "required and must not contain CR or LF")
		}
		f.CustomerLookup.BaseURL = strings.TrimRight(f.CustomerLookup.BaseURL, "/")
	}
	if f.Jira != nil {
		if raw.Jira.AccountCacheTtlSeconds == nil {
			f.Jira.AccountCacheTtlSeconds = 300
		}
		if f.Jira.AccountCacheTtlSeconds < 0 || f.Jira.AccountCacheTtlSeconds > 3600 {
			return invalid("jira.accountCacheTtlSeconds", "must be between 0 and 3600 seconds")
		}
		if raw.Jira.MaxFieldBytes == nil {
			f.Jira.MaxFieldBytes = 65536
		}
		if f.Jira.MaxFieldBytes < 1024 || f.Jira.MaxFieldBytes > 4<<20 {
			return invalid("jira.maxFieldBytes", "must be between 1024 and 4194304 bytes")
		}
		if raw.Jira.BaseURL != nil && !validURL(f.Jira.BaseURL, true) {
			return invalid("jira.baseUrl", "must be an absolute HTTP or HTTPS URL with a valid host and port, without userinfo, query, fragment, encoded path or dot segments")
		}
		if err := validateAuth(f.Jira.Auth, raw.Jira.Auth, "jira.auth", true); err != nil {
			return err
		}
		f.Jira.BaseURL = strings.TrimRight(f.Jira.BaseURL, "/")
	}
	if f.Enabled("jira") {
		if f.Jira == nil || f.Jira.BaseURL == "" {
			return invalid("jira.baseUrl", "required when a Jira token is configured")
		}
		if f.Jira.Auth == nil {
			return invalid("jira.auth", "required when a Jira token is configured")
		}
	}
	c := f.CKLogs
	if !validURL(c.BaseURL, false) {
		return invalid("cklogs.baseUrl", "must be an absolute HTTP or HTTPS URL with a valid host and port, without userinfo, query or fragment")
	}
	if c.Index == "" {
		return invalid("cklogs.index", "must not be empty")
	}
	provider := c.Provider
	if provider != "kibana" && provider != "logbot" {
		return invalid("cklogs.provider", "must be kibana or logbot")
	}
	if provider == "logbot" && c.LogBot == nil {
		// 允许只切 provider 不填账号：/ready 报 not_ready，请求返回 not_available。
		c.LogBot = &LogBot{}
		f.CKLogs.LogBot = c.LogBot
	}
	if lb := c.LogBot; lb != nil {
		if lb.BaseURL == "" {
			lb.BaseURL = "https://logbot.icoremail.net"
		}
		if !validURL(lb.BaseURL, false) {
			return invalid("cklogs.logbot.baseUrl", "must be an absolute HTTP or HTTPS URL with a valid host and port, without userinfo, query or fragment")
		}
		lb.BaseURL = strings.TrimRight(lb.BaseURL, "/")
		if lb.PollIntervalMS == 0 {
			lb.PollIntervalMS = 500
		}
		if lb.PollIntervalMS < 100 || lb.PollIntervalMS > 60000 {
			return invalid("cklogs.logbot.pollIntervalMs", "must be between 100 and 60000 milliseconds")
		}
		if lb.TimeoutMS == 0 {
			lb.TimeoutMS = 90000
		}
		if lb.TimeoutMS < 1 || lb.TimeoutMS > MaxTimeoutMS {
			return invalid("cklogs.logbot.timeoutMs", fmt.Sprintf("must be between 1 and %d milliseconds", MaxTimeoutMS))
		}
		if err := validateLogBotAuth(lb.Auth, raw.CKLogs.LogBot.Auth, "cklogs.logbot.auth"); err != nil {
			return err
		}
	}
	if err := validateAuth(c.Auth, raw.CKLogs.Auth, "cklogs.auth", false); err != nil {
		return err
	}
	if f.Enabled("cklogs") && provider == "kibana" && c.Auth == nil {
		return invalid("cklogs.auth", "required when a cklogs token is configured and cklogs.provider is kibana")
	}
	if c.TimeoutMS < 1 || c.TimeoutMS > MaxTimeoutMS {
		return invalid("cklogs.timeoutMs", fmt.Sprintf("must be between 1 and %d milliseconds", MaxTimeoutMS))
	}
	if c.Queue.WaitTimeoutMS < 1 || c.Queue.WaitTimeoutMS > MaxWaitTimeoutMS {
		return invalid("cklogs.queue.waitTimeoutMs", fmt.Sprintf("must be between 1 and %d milliseconds", MaxWaitTimeoutMS))
	}
	if c.Queue.MaxConcurrency < 1 {
		return invalid("cklogs.queue.maxConcurrency", "must be at least 1")
	}
	if c.Queue.Size < 0 {
		return invalid("cklogs.queue.size", "must be at least 0")
	}
	host, port, err := net.SplitHostPort(f.Server.ListenAddr)
	if err != nil {
		return invalid("server.listenAddr", "must be host:port or :port (bracket IPv6 addresses)")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return invalid("server.listenAddr", "port must be between 1 and 65535")
	}
	if host != "" && net.ParseIP(host) == nil && !validHostname(host) {
		return invalid("server.listenAddr", "host must be a valid IP address or hostname")
	}
	for i, cidr := range f.Server.AllowCIDRs {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return invalid(fmt.Sprintf("server.allowCidrs[%d]", i), "must be a valid IPv4 or IPv6 CIDR")
		}
	}
	if f.Audit.Enabled && f.Audit.Dir == "" {
		return invalid("audit.dir", "must not be empty when audit.enabled is true")
	}
	return nil
}

// Warnings contain fixed text only, never URLs or credentials.
func (f *File) Warnings() []string {
	if f.Enabled("jira") && f.Jira != nil && strings.HasPrefix(f.Jira.BaseURL, "http://") {
		return []string{"WARNING: jira.baseUrl uses HTTP; Jira credentials and request/response data are transmitted without TLS encryption; use HTTPS when available"}
	}
	return nil
}

func validHostname(host string) bool {
	if len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
