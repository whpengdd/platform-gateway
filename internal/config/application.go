package config

import (
	"encoding/json"
	"net"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"
)

type UpstreamAuth struct {
	Type     string `json:"type"`
	Token    string `json:"token,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}
type Jira struct {
	BaseURL  string             `json:"baseUrl"`
	Auth     *UpstreamAuth      `json:"auth"`
	Projects map[string]Project `json:"projects"`
}
type CKLogs struct {
	BaseURL   string        `json:"baseUrl"`
	Auth      *UpstreamAuth `json:"auth"`
	Index     string        `json:"index"`
	TimeoutMS int64         `json:"timeoutMs"`
	Queue     Queue         `json:"queue"`
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
	return File{Server: Server{ListenAddr: ":8091", AllowCIDRs: []string{}}, Audit: Audit{Enabled: true, Dir: "logs"}, CKLogs: CKLogs{BaseURL: "https://ck-logs.icoremail.net", Index: "mtatrans_distributed", TimeoutMS: 60000, Queue: Queue{MaxConcurrency: 2, Size: 32, WaitTimeoutMS: 30000}}}
}

// Reject null for typed settings, while retaining arbitrary JSON in createDefaults.
func typedValuesPresent(b []byte) bool {
	var tree any
	if json.Unmarshal(b, &tree) != nil {
		return false
	}
	return nonNull(tree, reflect.TypeOf(File{}))
}
func nonNull(v any, t reflect.Type) bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() == reflect.Interface {
		return true
	}
	if v == nil {
		return false
	}
	switch t.Kind() {
	case reflect.Struct:
		m, ok := v.(map[string]any)
		if !ok {
			return false
		}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			key := strings.Split(f.Tag.Get("json"), ",")[0]
			if x, ok := m[key]; ok && !nonNull(x, f.Type) {
				return false
			}
		}
	case reflect.Map:
		for _, x := range v.(map[string]any) {
			if !nonNull(x, t.Elem()) {
				return false
			}
		}
	case reflect.Slice:
		for _, x := range v.([]any) {
			if !nonNull(x, t.Elem()) {
				return false
			}
		}
	}
	return true
}
func validURL(raw string, jira bool) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") || (u.Scheme != "https" && (jira || u.Scheme != "http")) {
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
func validAuth(a *UpstreamAuth, raw map[string]json.RawMessage, jira bool) bool {
	if a == nil {
		return true
	}
	if strings.ContainsAny(a.Token+a.Username+a.Password, "\r\n") {
		return false
	}
	_, token := raw["token"]
	_, user := raw["username"]
	_, pass := raw["password"]
	switch a.Type {
	case "bearer":
		return jira && a.Token != "" && !user && !pass
	case "basic":
		return !token && a.Username != "" && a.Password != ""
	}
	return false
}
func (f *File) validateApplication(b []byte) error {
	var raw struct {
		Jira struct {
			BaseURL *string                    `json:"baseUrl"`
			Auth    map[string]json.RawMessage `json:"auth"`
		} `json:"jira"`
		CKLogs struct {
			Auth map[string]json.RawMessage `json:"auth"`
		} `json:"cklogs"`
	}
	if json.Unmarshal(b, &raw) != nil {
		return ErrConfig
	}
	if f.Jira != nil {
		if (raw.Jira.BaseURL != nil && !validURL(f.Jira.BaseURL, true)) || !validAuth(f.Jira.Auth, raw.Jira.Auth, true) {
			return ErrConfig
		}
		f.Jira.BaseURL = strings.TrimRight(f.Jira.BaseURL, "/")
	}
	if f.Enabled("jira") && (f.Jira == nil || f.Jira.BaseURL == "" || f.Jira.Auth == nil) {
		return ErrConfig
	}
	c := f.CKLogs
	if !validURL(c.BaseURL, false) || c.Index == "" || !validAuth(c.Auth, raw.CKLogs.Auth, false) || (f.Enabled("cklogs") && c.Auth == nil) || c.TimeoutMS < 1 || c.TimeoutMS > MaxTimeoutMS || c.Queue.WaitTimeoutMS < 1 || c.Queue.WaitTimeoutMS > MaxWaitTimeoutMS || c.Queue.MaxConcurrency < 1 || c.Queue.Size < 0 {
		return ErrConfig
	}
	host, port, err := net.SplitHostPort(f.Server.ListenAddr)
	if err != nil {
		return ErrConfig
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 || (host != "" && net.ParseIP(host) == nil && !validHostname(host)) {
		return ErrConfig
	}
	for _, cidr := range f.Server.AllowCIDRs {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return ErrConfig
		}
	}
	if f.Audit.Enabled && f.Audit.Dir == "" {
		return ErrConfig
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
