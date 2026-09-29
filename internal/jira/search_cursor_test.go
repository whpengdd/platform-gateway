package jira

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type timezoneSearchHarness struct {
	s             *Server
	now           atomic.Int64
	zone          atomic.Value
	identity      atomic.Value
	profileStatus atomic.Int32
	profiles      atomic.Int32
	searches      atomic.Int32
	onSearch      func()
}

func newTimezoneSearchHarness(t *testing.T) *timezoneSearchHarness {
	t.Helper()
	h := &timezoneSearchHarness{}
	h.now.Store(1800000000)
	h.zone.Store("UTC")
	h.identity.Store("bot")
	h.s, _ = fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/myself") {
			h.profiles.Add(1)
			if status := h.profileStatus.Load(); status != 0 {
				w.WriteHeader(int(status))
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"name": h.identity.Load().(string), "timeZone": h.zone.Load().(string)})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/search") {
			var req struct {
				JQL   string `json:"jql"`
				Start int    `json:"startAt"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			if strings.Contains(req.JQL, "ORDER BY updated ASC") {
				h.searches.Add(1)
				if h.onSearch != nil {
					h.onSearch()
				}
				json.NewEncoder(w).Encode(map[string]any{"startAt": req.Start, "total": 2, "issues": []any{mockIssue()}})
				return
			}
		}
		baseline(w, r)
	})
	h.s.now = func() time.Time { return time.Unix(h.now.Load(), 0) }
	h.s.client.accounts.now = h.s.now
	return h
}

const timedSearchBody = `{"updatedFrom":"2026-09-29T10:00:00+08:00","updatedTo":"2026-09-29T11:00:00+08:00","limit":1}`

func searchCursor(t *testing.T, s *Server, body string) string {
	t.Helper()
	w := call(s, "POST", root+"/issues/search", "jira-a", body)
	var page struct {
		Cursor string `json:"nextCursor"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || page.Cursor == "" {
		t.Fatalf("page=%d %s", w.Code, w.Body.String())
	}
	return page.Cursor
}

func cursorBody(raw string) string {
	b, _ := json.Marshal(map[string]string{"cursor": raw})
	return string(b)
}

func TestSearchCursorAccountChanges(t *testing.T) {
	for _, mode := range []string{"same", "timezone", "missing", "identity", "transport"} {
		t.Run(mode, func(t *testing.T) {
			h := newTimezoneSearchHarness(t)
			raw := searchCursor(t, h.s, timedSearchBody)
			h.now.Add(301)
			want := 410
			switch mode {
			case "same":
				want = 200
			case "timezone":
				h.zone.Store("Asia/Shanghai")
			case "missing":
				h.zone.Store("")
			case "identity":
				h.identity.Store("other-bot")
			case "transport":
				h.profileStatus.Store(503)
				want = 503
			}
			w := call(h.s, "POST", root+"/issues/search", "jira-a", cursorBody(raw))
			if w.Code != want {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			if want == 410 && !strings.Contains(w.Body.String(), "cursor_expired") {
				t.Fatal(w.Body.String())
			}
			if h.profiles.Load() != 2 {
				t.Fatal("did not refresh at TTL", h.profiles.Load())
			}
			if want != 200 && h.searches.Load() != 1 {
				t.Fatal("executed invalid continuation")
			}
			if mode == "missing" {
				w = call(h.s, "POST", root+"/issues/search", "jira-a", timedSearchBody)
				if w.Code != 503 || !strings.Contains(w.Body.String(), "timezone_unavailable") {
					t.Fatal(w.Code, w.Body.String())
				}
			}
		})
	}
}

func TestSearchCursorInitialBaselineAndNoTime(t *testing.T) {
	h := newTimezoneSearchHarness(t)
	raw := searchCursor(t, h.s, `{"limit":1}`)
	if h.profiles.Load() != 0 {
		t.Fatal("unbounded search fetched myself")
	}
	if _, err := h.s.client.accountContext(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	w := call(h.s, "POST", root+"/issues/search", "jira-a", cursorBody(raw))
	if w.Code != 200 || h.profiles.Load() != 1 {
		t.Fatal(w.Code, w.Body.String(), h.profiles.Load())
	}
	raw = searchCursor(t, h.s, `{"limit":1}`)
	comment := h.s.nextCursor("comments", "CS", "1", Search{Limit: 1}, 1, 0, 0)
	h.now.Add(301)
	h.zone.Store("Asia/Shanghai")
	if _, err := h.s.client.accountContext(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	before := h.searches.Load()
	w = call(h.s, "POST", root+"/issues/search", "jira-a", cursorBody(raw))
	if w.Code != 410 || h.searches.Load() != before || h.profiles.Load() != 2 {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err := h.s.decodeCursor(comment, "comments", "CS", "1"); err != nil {
		t.Fatal("comment cursor expired on timezone change", err)
	}
}

func TestKnownExpiredSearchCursorDoesNotRefreshProfile(t *testing.T) {
	h := newTimezoneSearchHarness(t)
	raw := searchCursor(t, h.s, timedSearchBody)
	h.now.Add(301)
	h.zone.Store("Asia/Shanghai")
	if _, err := h.s.client.accountContext(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	h.now.Add(301)
	h.profileStatus.Store(503)
	w := call(h.s, "POST", root+"/issues/search", "jira-a", cursorBody(raw))
	if w.Code != 410 || h.profiles.Load() != 2 || h.searches.Load() != 1 {
		t.Fatal(w.Code, w.Body.String(), h.profiles.Load(), h.searches.Load())
	}
}

func TestSearchCursorUsesQuerySnapshotDuringRefresh(t *testing.T) {
	h := newTimezoneSearchHarness(t)
	entered, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	h.onSearch = func() { close(entered); <-release }
	done := make(chan string, 1)
	go func() {
		w := call(h.s, "POST", root+"/issues/search", "jira-a", timedSearchBody)
		done <- w.Body.String()
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("search did not reach upstream")
	}
	h.now.Add(301)
	h.zone.Store("Asia/Shanghai")
	_, err := h.s.client.accountContext(context.Background(), true)
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Cursor string `json:"nextCursor"`
	}
	var response string
	select {
	case response = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("search did not complete")
	}
	if err := json.Unmarshal([]byte(response), &page); err != nil || page.Cursor == "" {
		t.Fatal("missing cursor", err)
	}
	c, err := h.s.decodeCursor(page.Cursor, "search", "CS", "")
	if err != nil || c.AccountGeneration != 0 || h.s.client.accountGeneration() != 1 {
		t.Fatal("cursor did not retain query snapshot", c, err)
	}
	w := call(h.s, "POST", root+"/issues/search", "jira-a", cursorBody(page.Cursor))
	if w.Code != 410 || h.searches.Load() != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestCommentContinuationStillChecksParentAfterTimezoneChange(t *testing.T) {
	var denied atomic.Bool
	var parents atomic.Int32
	var zone atomic.Value
	zone.Store("UTC")
	s, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/jira/rest/api/2/myself":
			json.NewEncoder(w).Encode(map[string]string{"name": "bot", "timeZone": zone.Load().(string)})
			return
		case "/jira/rest/api/2/search":
			parents.Add(1)
			if denied.Load() {
				w.Write([]byte(`{"issues":[],"total":0}`))
				return
			}
		case "/jira/rest/api/2/issue/1/comment":
			if r.URL.Query().Get("startAt") == "2" {
				w.Write([]byte(`{"startAt":2,"total":3,"comments":[{"id":"13","body":"visible continuation"}]}`))
			} else {
				w.Write([]byte(`{"startAt":0,"total":3,"comments":[{"id":"11","body":"visible"},{"id":"12","body":"PRIVATE","visibility":{"type":"group","value":"staff"}}]}`))
			}
			return
		}
		baseline(w, r)
	})
	if err := WithAccountCacheTTL(0)(s.client); err != nil {
		t.Fatal(err)
	}
	if _, err := s.client.accountContext(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	w := call(s, "GET", root+"/issues/CS-1/comments?limit=2", "jira-a", "")
	var page struct {
		Cursor string `json:"nextCursor"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || page.Cursor == "" || strings.Contains(w.Body.String(), "PRIVATE") {
		t.Fatal(w.Code, w.Body.String())
	}
	zone.Store("Asia/Shanghai")
	if _, err := s.client.accountContext(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	before := parents.Load()
	path := root + "/issues/CS-1/comments?cursor=" + url.QueryEscape(page.Cursor)
	w = call(s, "GET", path, "jira-a", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "visible continuation") || parents.Load() != before+1 {
		t.Fatal(w.Code, w.Body.String(), parents.Load())
	}
	denied.Store(true)
	w = call(s, "GET", path, "jira-a", "")
	if w.Code != 404 || parents.Load() != before+2 {
		t.Fatal(w.Code, w.Body.String(), parents.Load())
	}
}

func TestSearchValidationAndTimezoneFailureIsolation(t *testing.T) {
	h := newTimezoneSearchHarness(t)
	for _, body := range []string{
		`{"updatedFrom":"2026-09-29T10:00:00","updatedTo":"2026-09-29T11:00:00"}`,
		`{"updatedFrom":"2026-09-29T10:00:00Z","updatedTo":"2026-09-29T11:00:00+08:00"}`,
		`{"updatedFrom":"2026-09-01T00:00:00Z","updatedTo":"2026-10-01T00:00:01Z"}`,
	} {
		w := call(h.s, "POST", root+"/issues/search", "jira-a", body)
		if w.Code != 400 || h.profiles.Load() != 0 || h.searches.Load() != 0 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	h.zone.Store("")
	w := call(h.s, "POST", root+"/issues/search", "jira-a", timedSearchBody)
	if w.Code != 503 || !strings.Contains(w.Body.String(), "timezone_unavailable") || h.searches.Load() != 0 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call(h.s, "POST", root+"/issues/search", "jira-a", `{}`)
	if w.Code != 200 || h.profiles.Load() != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call(h.s, "GET", root+"/issues/CS-1", "jira-a", "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	// Absolute ordering, rather than the two displayed clock values, is valid.
	h.now.Add(6)
	h.zone.Store("UTC")
	w = call(h.s, "POST", root+"/issues/search", "jira-a", `{"updatedFrom":"2026-09-29T10:00:00+08:00","updatedTo":"2026-09-29T03:00:00Z"}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestSearchShortTTLTimezoneBackoff(t *testing.T) {
	for _, ttl := range []time.Duration{0, time.Second, 4 * time.Second, 5 * time.Second, 300 * time.Second} {
		t.Run(ttl.String(), func(t *testing.T) {
			h := newTimezoneSearchHarness(t)
			if err := WithAccountCacheTTL(ttl)(h.s.client); err != nil {
				t.Fatal(err)
			}
			h.zone.Store("")
			for _, delta := range []int64{0, 1, 3} {
				h.now.Add(delta)
				w := call(h.s, "POST", root+"/issues/search", "jira-a", timedSearchBody)
				if w.Code != 503 || !strings.Contains(w.Body.String(), "timezone_unavailable") || h.profiles.Load() != 1 || h.searches.Load() != 0 {
					t.Fatal(w.Code, w.Body.String(), h.profiles.Load(), h.searches.Load())
				}
			}
			h.now.Add(1)
			h.zone.Store("UTC")
			w := call(h.s, "POST", root+"/issues/search", "jira-a", timedSearchBody)
			if w.Code != 200 || h.profiles.Load() != 2 || h.searches.Load() != 1 {
				t.Fatal(w.Code, w.Body.String(), h.profiles.Load(), h.searches.Load())
			}
		})
	}
}
