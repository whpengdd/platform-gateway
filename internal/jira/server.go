package jira

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"platform-gateway/internal/auditlog"
	"platform-gateway/internal/auth"
	"platform-gateway/internal/backend"
	"platform-gateway/internal/config"
	"platform-gateway/internal/jql"
	"platform-gateway/internal/queue"
	"platform-gateway/internal/strictjson"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

var issueKey = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}-[1-9][0-9]{0,63}$`)
var requestID = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

type validation struct {
	mu                      sync.Mutex
	valid, invalid, running bool
	retry                   time.Time
}
type project struct {
	config.Project
	validation validation
	budget     budget
}
type Server struct {
	client               *Client
	auth                 *auth.Checker
	projects             map[string]*project
	gate                 backend.Gate
	secret               [32]byte
	digest               string
	now                  func() time.Time
	responseWriteTimeout time.Duration
}

func NewServer(c *Client, a *auth.Checker, policies map[string]config.Project) (*Server, error) {
	s := &Server{client: c, auth: a, projects: map[string]*project{}, gate: queue.New("jira", 2, 32, 30*time.Second), now: time.Now, responseWriteTimeout: 30 * time.Second}
	if _, err := rand.Read(s.secret[:]); err != nil {
		return nil, err
	}
	b, _ := json.Marshal(policies)
	hash := sha256.Sum256(b)
	s.digest = hex.EncodeToString(hash[:])
	for key, p := range policies {
		if jql.Validate(p.FilterJQL) != nil {
			return nil, config.ErrConfig
		}
		s.projects[key] = &project{Project: p}
	}
	return s, nil
}
func (s *Server) validate(ctx context.Context, key string, p *project) error {
	v := &p.validation
	v.mu.Lock()
	if v.valid {
		v.mu.Unlock()
		return nil
	}
	if v.invalid || v.running || s.now().Before(v.retry) {
		v.mu.Unlock()
		return unavailable()
	}
	v.running = true
	v.mu.Unlock()
	_, err := s.client.Search(ctx, jql.Scope(key, p.FilterJQL, ""), []string{"project"}, 0, 1)
	v.mu.Lock()
	defer v.mu.Unlock()
	v.running = false
	if err == nil {
		v.valid = true
		return nil
	}
	var e *Error
	if errors.As(err, &e) && e.Upstream == 400 {
		v.invalid = true
	} else {
		v.retry = s.now().Add(5 * time.Second)
	}
	return unavailable()
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// The upstream FIFO ends when Jira work finishes. Bound client output separately.
	w = &deadlineWriter{ResponseWriter: w, timeout: s.responseWriteTimeout}

	id := r.Header.Get("X-Request-Id")
	if !requestID.MatchString(id) {
		var b [16]byte
		rand.Read(b[:])
		id = hex.EncodeToString(b[:])
	}
	w.Header().Set("X-Request-Id", id)
	respondError := func(err error) {
		e := unavailable()
		var given *Error
		if errors.As(err, &given) {
			e = given
		}
		var busy *backend.BusyError
		if errors.As(err, &busy) {
			e = failure(503, "gateway_busy")
		}
		var wait *backend.WaitTimeoutError
		if errors.As(err, &wait) {
			e = failure(503, "gateway_queue_timeout")
		}
		if e.Status == 429 {
			w.Header().Set("Retry-After", "60")
		}
		if rec := auditlog.From(r.Context()); rec != nil {
			rec.SetError(e.Code)
			if e.Upstream != 0 {
				rec.SetJiraUpstream(e.Upstream)
			}
		}
		sendJSON(w, e.Status, map[string]any{"code": e.Code, "error": e.Code, "requestId": id})
	}
	if rec := auditlog.From(r.Context()); rec != nil {
		rec.SetJira("", "", "jira_request")
	}
	principal, status, code := s.auth.Authenticate(r)
	if status != 0 {
		respondError(failure(status, code))
		return
	}
	if len(principal.Projects) == 0 {
		respondError(failure(403, "token_scope_forbidden"))
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/jira/projects"), "/")
	if len(parts) == 1 && parts[0] == "" && r.Method == "GET" {
		if r.URL.RawQuery != "" {
			respondError(failure(400, "invalid_parameter"))
			return
		}
		keys := append([]string(nil), principal.Projects...)
		sort.Strings(keys)
		out := []any{}
		for _, key := range keys {
			out = append(out, map[string]string{"projectId": key})
		}
		sendJSON(w, 200, map[string]any{"items": out})
		return
	}
	if len(parts) < 3 || parts[0] != "" {
		respondError(failure(404, "resource_not_available"))
		return
	}
	key := parts[1]
	p := s.projects[key]
	if !principal.Allows(key) || p == nil {
		respondError(failure(403, "project_access_forbidden"))
		return
	}
	path := parts[2:]
	op := ""
	parent := ""
	child := ""
	write := false
	create := false
	switch {
	case len(path) == 1 && path[0] == "capabilities" && r.Method == "GET":
		op = "capabilities"
	case len(path) == 1 && path[0] == "issues" && r.Method == "POST":
		op = "create"
		write = true
		create = true
	case len(path) == 2 && path[0] == "issues" && path[1] == "search" && r.Method == "POST":
		op = "search"
	case len(path) >= 2 && path[0] == "issues" && issueKey.MatchString(path[1]):
		parent = path[1]
		switch {
		case len(path) == 2 && r.Method == "GET":
			op = "issue"
		case len(path) == 3 && path[2] == "comments" && (r.Method == "GET" || r.Method == "POST"):
			op = "comments"
			write = r.Method == "POST"
		case len(path) == 4 && path[2] == "comments" && config.NumericID.MatchString(path[3]) && r.Method == "GET":
			op = "comment"
			child = path[3]
		case len(path) == 3 && path[2] == "attachments" && r.Method == "POST":
			op = "upload"
			write = true
		case len(path) == 5 && path[2] == "attachments" && config.NumericID.MatchString(path[3]) && path[4] == "content" && r.Method == "GET":
			op = "download"
			child = path[3]
		}
	}
	if op == "" {
		respondError(failure(404, "resource_not_available"))
		return
	}
	if rec := auditlog.From(r.Context()); rec != nil {
		rec.SetJira(key, principal.Fingerprint, op)
	}
	if !(op == "comments" && !write) && r.URL.RawQuery != "" {
		respondError(failure(400, "invalid_parameter"))
		return
	}
	var result any
	status = 200
	if write {
		status = 201
	}
	var file []byte
	var filename, contentType string
	err := s.gate.Run(r.Context(), func(ctx context.Context) error {
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(30 * time.Second))
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		if !p.budget.take(s.now(), write, create, 0) {
			return failure(429, "rate_limited")
		}
		if err := s.validate(ctx, key, p); err != nil {
			return err
		}
		var err error
		switch op {
		case "capabilities":
			result, err = s.capabilities(ctx, key, p)
		case "search":
			result, err = s.search(r, key, p)
		case "create":
			result, err = s.create(r, key, p)
		default:
			fields := append([]string{"project"}, p.ReadFields...)
			if op == "download" || op == "upload" {
				fields = append(fields, "attachment")
			}
			issue, e := s.authorize(ctx, key, p, parent, fields)
			if e != nil {
				return e
			}
			switch op {
			case "issue":
				schemas, e := s.client.readSchemas(ctx, p.Project)
				if e != nil {
					return e
				}
				result, err = projectIssue(issue, key, p.Project, schemas)
			case "comments", "comment":
				result, err = s.comments(r, key, p, issue, child, write)
			case "upload":
				result, err = s.upload(r, p, issue)
			case "download":
				file, filename, contentType, err = s.download(ctx, issue, child)
			}
		}
		return err
	})
	if err != nil {
		respondError(err)
		return
	}
	if op == "download" {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(200)
		w.Write(file)
		return
	}
	sendJSON(w, status, result)
}
func sendJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil || len(b) > MaxJSON {
		status = 503
		b, _ = json.Marshal(map[string]string{"code": "upstream_unavailable", "error": "upstream_unavailable", "requestId": w.Header().Get("X-Request-Id")})
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(b)
}
func decode(r *http.Request, dst any) error {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return failure(415, "unsupported_media_type")
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, MaxBody+1))
	if err != nil {
		return failure(400, "invalid_body")
	}
	if len(b) > MaxBody {
		return failure(413, "payload_too_large")
	}
	if strictjson.Decode(b, dst) != nil || strings.TrimSpace(string(b)) == "null" {
		return failure(400, "invalid_body")
	}
	return nil
}
func (s *Server) authorize(ctx context.Context, key string, p *project, id string, fields []string) (Issue, error) {
	result, err := s.client.Search(ctx, jql.Scope(key, p.FilterJQL, "key = "+jql.Literal(id)), fields, 0, 2)
	if err != nil {
		return Issue{}, err
	}
	if len(result.Issues) != 1 {
		return Issue{}, failure(404, "resource_not_available")
	}
	i := result.Issues[0]
	if str(object(i.Fields["project"])["key"]) != key || !config.NumericID.MatchString(i.ID) || !issueKey.MatchString(i.Key) {
		return Issue{}, failure(404, "resource_not_available")
	}
	return i, nil
}

// Start the response deadline on first output, including error responses. The
// connection deadline (unlike a context) can interrupt a blocked socket write.
type deadlineWriter struct {
	http.ResponseWriter
	timeout time.Duration
	once    sync.Once
}

func (w *deadlineWriter) start() {
	w.once.Do(func() { _ = http.NewResponseController(w.ResponseWriter).SetWriteDeadline(time.Now().Add(w.timeout)) })
}
func (w *deadlineWriter) WriteHeader(status int)      { w.start(); w.ResponseWriter.WriteHeader(status) }
func (w *deadlineWriter) Write(b []byte) (int, error) { w.start(); return w.ResponseWriter.Write(b) }
func (w *deadlineWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
