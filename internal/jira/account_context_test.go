package jira

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAccountContextSharedByCapabilitiesAndCreation(t *testing.T) {
	var calls atomic.Int32
	s, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/jira/rest/api/2/myself" {
			calls.Add(1)
		}
		baseline(w, r)
	})
	for _, project := range []string{"CS", "IT"} {
		w := call(s, "GET", "/v1/jira/projects/"+project+"/capabilities", "jira-b", "")
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w := call(s, "POST", root+"/issues", "jira-a", `{"fields":{"summary":"cached bot"}}`)
	if w.Code != 200 && w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	if calls.Load() != 1 {
		t.Fatalf("myself requests = %d, want one shared identity snapshot", calls.Load())
	}
}

func TestAccountMissingTimezoneStillAllowsCreation(t *testing.T) {
	for _, profile := range []string{`{"name":"bot","displayName":"Bot"}`, `{"name":"bot","displayName":"Bot","timeZone":"Local"}`} {
		t.Run(profile, func(t *testing.T) {
			s, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/jira/rest/api/2/myself" {
					io.WriteString(w, profile)
					return
				}
				baseline(w, r)
			})
			w := call(s, "GET", root+"/capabilities", "jira-a", "")
			if w.Code != 200 || !strings.Contains(w.Body.String(), `"createEnabled":true`) {
				t.Fatal(w.Code, w.Body.String())
			}
			w = call(s, "POST", root+"/issues", "jira-a", `{"fields":{"summary":"identity without timezone"}}`)
			if w.Code != 201 {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}

func TestAccountSnapshotLifetimeAndIsolation(t *testing.T) {
	h := newAccountHarness(t, time.Minute)
	first, err := h.client.accountContext(context.Background(), true)
	if err != nil || first.Location.String() != "Asia/Shanghai" || first.Generation != 0 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	if len(first.Bot) != 5 {
		t.Fatalf("unexpected cached profile fields: %v", first.Bot)
	}
	first.Bot["name"] = "changed"
	h.advance(59 * time.Second)
	second, err := h.client.accountContext(context.Background(), false)
	if err != nil || second.Bot["name"] != "bot" || h.calls.Load() != 1 {
		t.Fatalf("snapshot was not reusable and immutable: %+v %v calls=%d", second, err, h.calls.Load())
	}
	h.advance(time.Second)
	if _, err := h.client.accountContext(context.Background(), true); err != nil || h.calls.Load() != 2 {
		t.Fatalf("expiry err=%v calls=%d", err, h.calls.Load())
	}
	other, err := NewClient(h.client.base.String(), "different-secret", "", "", WithAccountCacheTTL(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = other.accountContext(context.Background(), true); err != nil || h.calls.Load() != 3 {
		t.Fatalf("client isolation err=%v calls=%d", err, h.calls.Load())
	}
}

func TestAccountTTLStartsAtResponseCompletion(t *testing.T) {
	h := newAccountHarness(t, time.Minute)
	h.onRequest = func() { h.advance(30 * time.Second) }
	if _, err := h.client.accountContext(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	h.advance(45 * time.Second)
	if _, err := h.client.accountContext(context.Background(), false); err != nil || h.calls.Load() != 1 {
		t.Fatalf("TTL starts before completion: %v calls=%d", err, h.calls.Load())
	}
	h.advance(15 * time.Second)
	if _, err := h.client.accountContext(context.Background(), false); err != nil || h.calls.Load() != 2 {
		t.Fatalf("TTL was not renewed: %v calls=%d", err, h.calls.Load())
	}
}

func TestAccountClientOptions(t *testing.T) {
	c, err := NewClient("https://jira.example.test", "token", "", "")
	if err != nil || c.accounts.ttl != 5*time.Minute {
		t.Fatalf("default TTL: %v", err)
	}
	for _, ttl := range []time.Duration{0, time.Second, time.Hour} {
		c, err = NewClient("https://jira.example.test", "token", "", "", WithAccountCacheTTL(ttl))
		if err != nil || c.accounts.ttl != ttl {
			t.Fatalf("TTL %v: %v", ttl, err)
		}
	}
	for _, ttl := range []time.Duration{-1, time.Hour + 1} {
		if _, err = NewClient("https://jira.example.test", "token", "", "", WithAccountCacheTTL(ttl)); err == nil || !strings.Contains(err.Error(), "jira.accountCacheTtlSeconds") {
			t.Fatalf("invalid TTL %v: %v", ttl, err)
		}
	}
}

func TestAccountGenerationTracksContext(t *testing.T) {
	h := newAccountHarness(t, 0)
	for i, tt := range []struct {
		profile    string
		generation uint64
	}{
		{`{"accountId":"a","name":"bot","timeZone":"America/New_York"}`, 0},
		{`{"accountId":"a","name":"renamed","displayName":"Updated","emailAddress":"changed@example.test","timeZone":"America/New_York"}`, 0},
		{`{"accountId":"b","name":"renamed","timeZone":"America/New_York"}`, 1},
		{`{"accountId":"b","timeZone":"UTC"}`, 2},
		{`{"accountId":"b"}`, 3},
		{`{"accountId":"b"}`, 3},
		{`{"accountId":"b","timeZone":"Invalid/Zone"}`, 4},
		{`{"accountId":"b","timeZone":"UTC"}`, 5},
	} {
		h.profile.Store(tt.profile)
		h.advance(180 * 24 * time.Hour)
		got, err := h.client.accountContext(context.Background(), false)
		if err != nil || got.Generation != tt.generation || h.client.accountGeneration() != tt.generation {
			t.Fatalf("step%d got=%+v err=%v", i, got, err)
		}
	}
	if h.calls.Load() != 8 {
		t.Fatalf("TTL zero did not refresh: %d", h.calls.Load())
	}
}

func TestAccountTimezoneUnavailableIsIndependent(t *testing.T) {
	for _, zone := range []string{``, `,"timeZone":""`, `,"timeZone":" "`, `,"timeZone":"Local"`, `,"timeZone":"Unknown/Place"`, `,"timeZone":"+08:00"`, `,"timeZone":8`} {
		t.Run(zone, func(t *testing.T) {
			h := newAccountHarness(t, time.Minute)
			h.profile.Store(`{"name":"bot"` + zone + `}`)
			identity, err := h.client.accountContext(context.Background(), false)
			if err != nil || identity.Bot["name"] != "bot" {
				t.Fatal(identity, err)
			}
			got, err := h.client.accountContext(context.Background(), true)
			assertAccountError(t, err, "timezone_unavailable")
			if got.Location != nil || got.Bot["name"] != "bot" || h.calls.Load() != 1 {
				t.Fatalf("bad partial snapshot %+v calls=%d", got, h.calls.Load())
			}
			h.profile.Store(`{"name":"bot","timeZone":"Asia/Kolkata"}`)
			h.advance(5 * time.Second)
			recovered, err := h.client.accountContext(context.Background(), true)
			if err != nil || recovered.Location.String() != "Asia/Kolkata" || recovered.Generation != 1 || h.calls.Load() != 2 {
				t.Fatalf("recovery %+v err=%v calls=%d", recovered, err, h.calls.Load())
			}
		})
	}
}

func TestAccountTimezoneRetryFailurePreservesIdentityLifetime(t *testing.T) {
	h := newAccountHarness(t, 20*time.Second)
	h.profile.Store(`{"name":"bot"}`)
	first, err := h.client.accountContext(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	h.advance(5 * time.Second)
	h.status.Store(503)
	got, err := h.client.accountContext(context.Background(), true)
	assertAccountError(t, err, "upstream_unavailable")
	if got.Bot != nil {
		t.Fatal("transport failure returned a stale snapshot")
	}
	h.advance(4 * time.Second)
	_, err = h.client.accountContext(context.Background(), true)
	assertAccountError(t, err, "upstream_unavailable")
	identity, err := h.client.accountContext(context.Background(), false)
	if err != nil || identity.Bot["name"] != "bot" || identity.Generation != first.Generation || h.calls.Load() != 2 {
		t.Fatalf("identity lost on timezone failure: %+v %v calls=%d", identity, err, h.calls.Load())
	}
	h.advance(11 * time.Second)
	_, err = h.client.accountContext(context.Background(), false)
	assertAccountError(t, err, "upstream_unavailable")
	if h.client.accountGeneration() != first.Generation || h.calls.Load() != 3 {
		t.Fatal("failed retry extended expiry or changed generation")
	}
	h.status.Store(200)
	h.profile.Store(`{"name":"bot","timeZone":"UTC"}`)
	h.advance(5 * time.Second)
	recovered, err := h.client.accountContext(context.Background(), true)
	if err != nil || recovered.Generation != first.Generation+1 || recovered.Location.String() != "UTC" {
		t.Fatal(recovered, err)
	}
}

func TestAccountTimezoneBackoffIndependentOfTTL(t *testing.T) {
	for _, ttl := range []time.Duration{0, time.Second, 4 * time.Second, 5 * time.Second, 300 * time.Second} {
		t.Run(ttl.String(), func(t *testing.T) {
			h := newAccountHarness(t, ttl)
			h.profile.Store(`{"name":"bot"}`)
			first, err := h.client.accountContext(context.Background(), true)
			assertAccountError(t, err, "timezone_unavailable")
			elapsed := time.Duration(0)
			for _, delta := range []time.Duration{time.Second, 3 * time.Second, 999 * time.Millisecond} {
				h.advance(delta)
				elapsed += delta
				got, err := h.client.accountContext(context.Background(), true)
				assertAccountError(t, err, "timezone_unavailable")
				if h.calls.Load() != 1 {
					t.Fatalf("timezone retry at %v: calls=%d, want 1", elapsed, h.calls.Load())
				}
				if got.Generation != first.Generation || got.Location != nil {
					t.Fatalf("backoff changed context: %+v", got)
				}
				if elapsed >= ttl && got.Bot != nil {
					t.Fatal("backoff returned an expired identity")
				}
				if elapsed < ttl && got.Bot["name"] != "bot" {
					t.Fatal("backoff dropped a fresh identity")
				}
			}
			h.advance(time.Millisecond) // Exactly the five-second retry boundary.
			h.profile.Store(`{"name":"bot","timeZone":"UTC"}`)
			recovered, err := h.client.accountContext(context.Background(), true)
			if err != nil || recovered.Location != time.UTC || recovered.Generation != first.Generation+1 || h.calls.Load() != 2 {
				t.Fatalf("boundary recovery: %+v err=%v calls=%d", recovered, err, h.calls.Load())
			}
			_, err = h.client.accountContext(context.Background(), true)
			want := int32(2)
			if ttl == 0 {
				want = 3
			}
			if err != nil || h.calls.Load() != want {
				t.Fatalf("healthy snapshot TTL changed: err=%v calls=%d want=%d", err, h.calls.Load(), want)
			}
		})
	}
}

func TestAccountIdentityRefreshDuringTimezoneBackoff(t *testing.T) {
	for _, ttl := range []time.Duration{0, time.Second, 4 * time.Second, 5 * time.Second, 300 * time.Second} {
		t.Run(ttl.String(), func(t *testing.T) {
			h := newAccountHarness(t, ttl)
			h.profile.Store(`{"name":"bot"}`)
			_, err := h.client.accountContext(context.Background(), true)
			assertAccountError(t, err, "timezone_unavailable")
			h.advance(time.Second)
			h.profile.Store(`{"name":"new-bot","timeZone":"UTC"}`)
			identity, err := h.client.accountContext(context.Background(), false)
			if err != nil {
				t.Fatal("timezone backoff blocked an identity consumer", err)
			}
			wantName, wantCalls := "bot", int32(1)
			if ttl <= time.Second {
				wantName, wantCalls = "new-bot", 2
				if identity.Location != time.UTC || identity.Generation != 1 {
					t.Fatal("identity refresh did not update timezone context", identity)
				}
			}
			if identity.Bot["name"] != wantName || h.calls.Load() != wantCalls {
				t.Fatalf("identity=%v calls=%d", identity.Bot, h.calls.Load())
			}
		})
	}
}

func TestAccountShortTTLTimezoneRetryTransportFailure(t *testing.T) {
	for _, ttl := range []time.Duration{0, time.Second, 4 * time.Second, 5 * time.Second, 300 * time.Second} {
		t.Run(ttl.String(), func(t *testing.T) {
			h := newAccountHarness(t, ttl)
			h.profile.Store(`{"name":"bot"}`)
			_, err := h.client.accountContext(context.Background(), true)
			assertAccountError(t, err, "timezone_unavailable")
			h.advance(5 * time.Second)
			h.status.Store(503)
			_, err = h.client.accountContext(context.Background(), true)
			assertAccountError(t, err, "upstream_unavailable")
			h.advance(time.Second)
			got, err := h.client.accountContext(context.Background(), true)
			assertAccountError(t, err, "upstream_unavailable")
			if got.Bot != nil || h.calls.Load() != 2 || h.client.accountGeneration() != 0 {
				t.Fatal("failed retry served stale data or changed history", got, h.calls.Load())
			}
			identity, err := h.client.accountContext(context.Background(), false)
			if ttl <= 5*time.Second {
				assertAccountError(t, err, "upstream_unavailable")
				if identity.Bot != nil {
					t.Fatal("expired identity was returned")
				}
			} else if err != nil || identity.Bot["name"] != "bot" {
				t.Fatal("fresh identity unavailable", identity, err)
			}
			h.advance(4 * time.Second)
			h.status.Store(200)
			h.profile.Store(`{"name":"bot","timeZone":"UTC"}`)
			_, err = h.client.accountContext(context.Background(), true)
			if err != nil || h.calls.Load() != 3 {
				t.Fatal("recovery failed", err, h.calls.Load())
			}
		})
	}
}

func TestAccountRefreshFailuresBackOffWithoutStaleData(t *testing.T) {
	for _, mode := range []string{"status", "invalid JSON", "invalid identity", "large response", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			h := newAccountHarness(t, time.Minute)
			if _, err := h.client.accountContext(context.Background(), true); err != nil {
				t.Fatal(err)
			}
			h.advance(time.Minute)
			switch mode {
			case "status":
				h.status.Store(403)
			case "invalid JSON":
				h.profile.Store(`{"name":`)
			case "invalid identity":
				h.profile.Store(`{"name":42,"timeZone":"UTC"}`)
			case "large response":
				h.profile.Store(`{"name":"bot","blob":"` + strings.Repeat("x", MaxJSON) + `"}`)
			case "timeout":
				h.block.Store(true)
				h.client.http.Timeout = 20 * time.Millisecond
			}
			got, err := h.client.accountContext(context.Background(), true)
			assertAccountError(t, err, "upstream_unavailable")
			if got.Bot != nil || got.Location != nil {
				t.Fatalf("served expired data: %+v", got)
			}
			h.advance(4 * time.Second)
			_, err = h.client.accountContext(context.Background(), false)
			assertAccountError(t, err, "upstream_unavailable")
			if h.calls.Load() != 2 {
				t.Fatalf("no failure backoff: %d", h.calls.Load())
			}
			h.block.Store(false)
			h.status.Store(200)
			h.profile.Store(`{"name":"bot","timeZone":"Asia/Shanghai"}`)
			h.advance(time.Second)
			if _, err = h.client.accountContext(context.Background(), true); err != nil || h.calls.Load() != 3 {
				t.Fatalf("no recovery: %v calls=%d", err, h.calls.Load())
			}
		})
	}
}

func TestAccountRefreshCoalescesIncludingZeroTTL(t *testing.T) {
	for _, ttl := range []time.Duration{0, time.Minute} {
		t.Run(ttl.String(), func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					close(started)
					<-release
				}
				io.WriteString(w, `{"name":"bot","timeZone":"UTC"}`)
			})
			c.accounts.ttl = ttl
			results := make(chan error, 9)
			go func() { _, err := c.accountContext(context.Background(), true); results <- err }()
			awaitAccountSignal(t, started)
			for i := 0; i < 8; i++ {
				ctx := &observedAccountContext{Context: context.Background(), waiting: make(chan struct{})}
				go func() { _, err := c.accountContext(ctx, true); results <- err }()
				awaitAccountSignal(t, ctx.waiting)
			}
			close(release)
			for i := 0; i < 9; i++ {
				if err := awaitAccountResult(t, results); err != nil {
					t.Fatal(err)
				}
			}
			if calls.Load() != 1 {
				t.Fatalf("concurrent batch made %d requests", calls.Load())
			}
			if ttl == 0 {
				if _, err := c.accountContext(context.Background(), true); err != nil || calls.Load() != 2 {
					t.Fatalf("zero TTL retained completed batch: %v requests=%d", err, calls.Load())
				}
			}
		})
	}
}

func TestAccountWaitingCallerCancellation(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		close(started)
		<-release
		io.WriteString(w, `{"name":"bot","timeZone":"UTC"}`)
	})
	owner := make(chan error, 1)
	go func() { _, err := c.accountContext(context.Background(), true); owner <- err }()
	awaitAccountSignal(t, started)
	base, cancel := context.WithCancel(context.Background())
	ctx := &observedAccountContext{Context: base, waiting: make(chan struct{})}
	waiter := make(chan error, 1)
	go func() { _, err := c.accountContext(ctx, true); waiter <- err }()
	awaitAccountSignal(t, ctx.waiting)
	cancel()
	assertAccountError(t, awaitAccountResult(t, waiter), "upstream_unavailable")
	close(release)
	if err := awaitAccountResult(t, owner); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
}

func TestAccountCancelledOwnerAllowsLiveWaiterToRefresh(t *testing.T) {
	started, cancelled := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(started)
			<-r.Context().Done()
			close(cancelled)
			return
		}
		io.WriteString(w, `{"name":"bot","timeZone":"UTC"}`)
	})
	ctx, cancel := context.WithCancel(context.Background())
	owner := make(chan error, 1)
	go func() { _, err := c.accountContext(ctx, true); owner <- err }()
	awaitAccountSignal(t, started)
	live := &observedAccountContext{Context: context.Background(), waiting: make(chan struct{})}
	waiter := make(chan error, 1)
	go func() { _, err := c.accountContext(live, true); waiter <- err }()
	awaitAccountSignal(t, live.waiting)
	cancel()
	assertAccountError(t, awaitAccountResult(t, owner), "upstream_unavailable")
	awaitAccountSignal(t, cancelled)
	if err := awaitAccountResult(t, waiter); err != nil {
		t.Fatal("owner cancellation was cached:", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("replacement refresh count=%d", calls.Load())
	}
}

type accountHarness struct {
	client    *Client
	calls     atomic.Int32
	clock     atomic.Int64
	profile   atomic.Value
	status    atomic.Int32
	block     atomic.Bool
	onRequest func()
}

func newAccountHarness(t *testing.T, ttl time.Duration) *accountHarness {
	t.Helper()
	h := &accountHarness{}
	h.profile.Store(`{"accountId":"account-1","key":"key-1","name":"bot","displayName":"Bot","emailAddress":"bot@example.test","timeZone":"Asia/Shanghai","groups":{"private":true},"avatarUrls":{"large":"SECRET"},"self":"SECRET"}`)
	h.status.Store(200)
	h.clock.Store(time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC).UnixNano())
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/jira/rest/api/2/myself" {
			t.Error("unexpected profile request", r.Method, r.URL.Path)
		}
		h.calls.Add(1)
		if h.block.Load() {
			<-r.Context().Done()
			return
		}
		if h.onRequest != nil {
			h.onRequest()
		}
		w.WriteHeader(int(h.status.Load()))
		io.WriteString(w, h.profile.Load().(string))
	}))
	t.Cleanup(up.Close)
	var err error
	h.client, err = NewClient(up.URL+"/jira", "secret", "", "", WithAccountCacheTTL(ttl))
	if err != nil {
		t.Fatal(err)
	}
	h.client.accounts.now = func() time.Time { return time.Unix(0, h.clock.Load()) }
	return h
}
func (h *accountHarness) advance(d time.Duration) { h.clock.Add(int64(d)) }
func assertAccountError(t *testing.T, err error, code string) {
	t.Helper()
	e, ok := err.(*Error)
	if !ok || e.Status != 503 || e.Code != code {
		t.Fatalf("error=%v, want 503 %s", err, code)
	}
}

type observedAccountContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *observedAccountContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}
func awaitAccountSignal(t *testing.T, c <-chan struct{}) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for account test signal")
	}
}
func awaitAccountResult(t *testing.T, c <-chan error) error {
	t.Helper()
	select {
	case e := <-c:
		return e
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for account result")
		return nil
	}
}

func TestAccountTimezoneLossDoesNotReuseOldLocation(t *testing.T) {
	h := newAccountHarness(t, time.Minute)
	first, err := h.client.accountContext(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	h.profile.Store(`{"accountId":"account-1","name":"bot"}`)
	h.advance(time.Minute)
	lost, err := h.client.accountContext(context.Background(), true)
	assertAccountError(t, err, "timezone_unavailable")
	if lost.Location != nil || lost.Bot["name"] != "bot" || lost.Generation != first.Generation+1 {
		t.Fatalf("old timezone retained or loss not versioned: %+v", lost)
	}
	if _, err = h.client.accountContext(context.Background(), false); err != nil || h.calls.Load() != 2 {
		t.Fatalf("fresh identity unavailable: %v calls=%d", err, h.calls.Load())
	}
}

func TestAccountConcurrentConsumersSharePartialSnapshot(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		close(started)
		<-release
		io.WriteString(w, `{"name":"bot"}`)
	})
	owner := make(chan error, 1)
	go func() { _, err := c.accountContext(context.Background(), true); owner <- err }()
	awaitAccountSignal(t, started)
	identityCtx := &observedAccountContext{Context: context.Background(), waiting: make(chan struct{})}
	identity := make(chan error, 1)
	go func() { _, err := c.accountContext(identityCtx, false); identity <- err }()
	awaitAccountSignal(t, identityCtx.waiting)
	close(release)
	assertAccountError(t, awaitAccountResult(t, owner), "timezone_unavailable")
	if err := awaitAccountResult(t, identity); err != nil {
		t.Fatal("identity consumer inherited timezone requirement:", err)
	}
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
}
