package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"platform-gateway/internal/auditlog"
	"platform-gateway/internal/auth"
	"platform-gateway/internal/backend"
	"platform-gateway/internal/cklogs"
	"platform-gateway/internal/queue"
)

type Config struct {
	Auth   *auth.Checker
	CK     *cklogs.Service
	Gate   backend.Gate
	Audit  *auditlog.Writer
	CKUser string
	CKPass string
	Now    func() time.Time
}

type Server struct {
	auth   *auth.Checker
	ck     *cklogs.Service
	gate   backend.Gate
	audit  *auditlog.Writer
	ckUser string
	ckPass string
	now    func() time.Time
}

func New(cfg Config) http.Handler {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	s := &Server{
		auth:   cfg.Auth,
		ck:     cfg.CK,
		gate:   cfg.Gate,
		audit:  cfg.Audit,
		ckUser: cfg.CKUser,
		ckPass: cfg.CKPass,
		now:    now,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /ready", s.handleReady)
	mux.HandleFunc("POST /v1/cklogs/delivery", s.handleDelivery)
	mux.HandleFunc("POST /v1/cklogs/message", s.handleMessage)
	mux.HandleFunc("POST /v1/cklogs/login", s.handleLogin)
	mux.HandleFunc("POST /v1/cklogs/analysis/delivery", s.handleAnalysisDelivery)
	mux.HandleFunc("POST /v1/cklogs/analysis/auth", s.handleAnalysisAuth)
	mux.HandleFunc("POST /v1/cklogs/analysis/ops", s.handleAnalysisOps)
	return s.withAudit(mux)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	echoRID(w, r)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	echoRID(w, r)
	if s.auth != nil && s.auth.Ready() && s.ckUser != "" && s.ckPass != "" {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	writeJSON(w, http.StatusServiceUnavailable, map[string]string{
		"status": "not_ready",
		"error":  "GATEWAY_TOKEN_EXTERNAL / GATEWAY_TOKEN_INTERNAL / GATEWAY_AUTH_TOKENS 或 CK_LOGS_BASIC_USER/PASS 未配置",
	})
}

func (s *Server) withAudit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.audit == nil || !strings.HasPrefix(r.URL.Path, "/v1/") {
			next.ServeHTTP(w, r)
			return
		}
		started := time.Now()
		rec := auditlog.NewRecord(r, s.now(), auditlog.Shanghai())
		if r.Header.Get("X-Request-Id") == "" {
			r.Header.Set("X-Request-Id", rec.RequestID)
		}
		rw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r.WithContext(auditlog.WithRecord(r.Context(), rec)))
		rec.SetHTTPStatus(rw.status)
		rec.SetDuration(time.Since(started))
		if q, ok := s.gate.(*queue.FIFO); ok {
			inFlight, depth := q.Snapshot()
			rec.SetQueueSnapshot(inFlight, depth)
		}
		s.audit.Emit(rec)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (s *Server) checkAuth(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	echoRID(w, r)
	rec := auditlog.From(r.Context())
	if s.auth == nil {
		if rec != nil {
			rec.SetAuth("unauthorized")
		}
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "未授权", "code": "unauthorized"})
		return r, false
	}
	status, code, msg, class := s.auth.CheckClass(r)
	if status != 0 {
		if rec != nil {
			rec.SetAuth(code)
		}
		writeJSON(w, status, map[string]string{"error": msg, "code": code})
		return r, false
	}
	r = r.WithContext(auth.WithClass(r.Context(), class))
	if rec != nil {
		rec.SetAuth("ok")
		rec.SetTokenClass(string(class))
	}
	return r, true
}

func (s *Server) requireInternal(w http.ResponseWriter, r *http.Request) bool {
	if auth.ClassFrom(r.Context()) == auth.ClassInternal {
		return true
	}
	if rec := auditlog.From(r.Context()); rec != nil {
		rec.SetAuth("token_scope_forbidden")
		rec.SetError("token_scope_forbidden")
	}
	writeJSON(w, http.StatusForbidden, map[string]string{
		"error": "当前 token 不能调用客服分析接口",
		"code":  "token_scope_forbidden",
	})
	return false
}

func (s *Server) runCklogs(w http.ResponseWriter, r *http.Request, op func(ctx context.Context) error) bool {
	rec := auditlog.From(r.Context())
	waitStart := time.Now()
	bounded := func(ctx context.Context) error {
		if rec != nil {
			rec.SetQueueWait(time.Since(waitStart))
		}
		// 排队之后才起算，避免串行 _msearch 把并发槽占到 N 倍单次超时。
		timeout := cklogs.DefaultOpTimeout
		if s.ck != nil {
			timeout = s.ck.OpTimeout()
		}
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return op(ctx)
	}
	gate := s.gate
	if gate == nil {
		if err := bounded(r.Context()); err != nil {
			if rec != nil {
				rec.SetError("internal_error")
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error(), "code": "internal_error"})
			return false
		}
		return true
	}
	err := gate.Run(r.Context(), bounded)
	if rec != nil && err != nil {
		rec.SetQueueWait(time.Since(waitStart))
	}
	if err == nil {
		return true
	}
	var busy *backend.BusyError
	if errors.As(err, &busy) {
		if rec != nil {
			rec.SetError("gateway_busy")
		}
		sec := int(busy.RetryAfter.Seconds())
		if sec < 1 {
			sec = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(sec))
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "日志网关繁忙，请稍后重试",
			"code":  "gateway_busy",
		})
		return false
	}
	var wait *backend.WaitTimeoutError
	if errors.As(err, &wait) {
		if rec != nil {
			rec.SetError("gateway_queue_timeout")
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "等待日志网关队列超时，请稍后重试",
			"code":  "gateway_queue_timeout",
		})
		return false
	}
	if rec != nil {
		rec.SetError("internal_error")
	}
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error(), "code": "internal_error"})
	return false
}

func echoRID(w http.ResponseWriter, r *http.Request) {
	if id := r.Header.Get("X-Request-Id"); id != "" {
		w.Header().Set("X-Request-Id", id)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeAPIError(w http.ResponseWriter, err error) {
	var ae *apiError
	if errors.As(err, &ae) {
		writeJSON(w, ae.Status, map[string]string{"error": ae.Msg, "code": ae.Code})
		return
	}
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error(), "code": "invalid_body"})
}
