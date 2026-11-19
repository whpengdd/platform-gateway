package httpapi

import (
	"io"
	"net/http"
	"net/url"
	"time"

	"platform-gateway/internal/auditlog"
	"platform-gateway/internal/auth"
	"platform-gateway/internal/config"
)

// customerLookup exposes the BFF's shaped customer profile to CS project callers.
func customerLookup(checker *auth.Checker, cfg *config.CustomerLookup) http.Handler {
	client := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := auditlog.NewRecord(r, time.Now(), time.UTC).RequestID
		w.Header().Set("X-Request-Id", id)
		fail := func(status int, code string) {
			writeJSON(w, status, map[string]string{"code": code, "error": code, "requestId": id})
		}
		p, status, code := checker.Authenticate(r)
		if status != 0 {
			fail(status, code)
			return
		}
		if !p.Allows("CS") {
			fail(http.StatusForbidden, "project_access_forbidden")
			return
		}
		if r.Method != http.MethodGet {
			fail(http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		if cfg == nil {
			fail(http.StatusServiceUnavailable, "upstream_unavailable")
			return
		}
		values, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			fail(http.StatusBadRequest, "invalid_parameter")
			return
		}
		allowed := map[string]bool{"senderEmail": true, "email": true, "domain": true, "name": true, "phone": true, "q": true, "keyword": true, "type": true}
		for key := range values {
			if !allowed[key] {
				fail(http.StatusBadRequest, "invalid_parameter")
				return
			}
		}
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, cfg.BaseURL+"/api/ops/jira-modal/lookup?"+values.Encode(), nil)
		if err != nil {
			fail(http.StatusServiceUnavailable, "upstream_unavailable")
			return
		}
		req.Header.Set("X-API-Key", cfg.APIKey)
		resp, err := client.Do(req)
		if err != nil {
			fail(http.StatusBadGateway, "upstream_unavailable")
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 && resp.StatusCode != http.StatusBadRequest {
			fail(http.StatusBadGateway, "upstream_unavailable")
			return
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20+1))
		if err != nil || len(body) > 4<<20 {
			fail(http.StatusBadGateway, "upstream_unavailable")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
	})
}
