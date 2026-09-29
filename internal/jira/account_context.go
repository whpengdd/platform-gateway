package jira

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"
)

const accountRetryDelay = 5 * time.Second

// ClientOption configures an authenticated Jira client before it is used.
type ClientOption func(*Client) error

// WithAccountCacheTTL configures retained account snapshots. Zero retains only
// context history and shares an in-flight refresh with its existing waiters.
func WithAccountCacheTTL(ttl time.Duration) ClientOption {
	return func(c *Client) error {
		if ttl < 0 || ttl > time.Hour {
			return errors.New("invalid jira.accountCacheTtlSeconds: expected duration from zero through one hour")
		}
		c.accounts.ttl = ttl
		return nil
	}
}

type accountSnapshot struct {
	Bot        map[string]any
	Location   *time.Location
	Generation uint64
}

type accountFingerprint struct {
	identity string
	timezone string
	resolved bool
}

type accountRefresh struct {
	done      chan struct{}
	snapshot  accountSnapshot
	err       error
	cancelled bool
}

type accountCache struct {
	mu                 sync.Mutex
	ttl                time.Duration
	now                func() time.Time
	snapshot           accountSnapshot
	hasSnapshot        bool
	fingerprint        accountFingerprint
	expires            time.Time
	retryAfter         time.Time
	timezoneRetryAfter time.Time
	flight             *accountRefresh
}

// accountContext never serves expired data on upstream failure. Identity-only
// consumers may still use a fresh identity while timezone recovery is failing.
// The refresh owner performs its HTTP request synchronously in its own context;
// live waiters can elect a replacement owner when that context is cancelled.
func (c *Client) accountContext(ctx context.Context, requireTimezone bool) (accountSnapshot, error) {
	for {
		if ctx.Err() != nil {
			return accountSnapshot{}, unavailable()
		}
		a := &c.accounts
		a.mu.Lock()
		now := a.now()
		fresh := a.hasSnapshot && a.ttl > 0 && now.Before(a.expires)
		if fresh && (!requireTimezone || a.snapshot.Location != nil) {
			snapshot := copyAccountSnapshot(a.snapshot)
			a.mu.Unlock()
			return snapshot, nil
		}
		if now.Before(a.retryAfter) {
			a.mu.Unlock()
			return accountSnapshot{}, unavailable()
		}
		if requireTimezone && now.Before(a.timezoneRetryAfter) {
			// Negative timezone metadata has its own retry lifetime, even when
			// successful snapshots are disabled or have already expired. Keep
			// the observed generation for cursor checks, not expired identity.
			snapshot := accountSnapshot{Generation: a.snapshot.Generation}
			if fresh {
				snapshot = copyAccountSnapshot(a.snapshot)
			}
			a.mu.Unlock()
			return snapshot, failure(503, "timezone_unavailable")
		}
		if flight := a.flight; flight != nil {
			a.mu.Unlock()
			select {
			case <-ctx.Done():
				return accountSnapshot{}, unavailable()
			case <-flight.done:
				if ctx.Err() != nil {
					return accountSnapshot{}, unavailable()
				}
				if flight.cancelled {
					continue
				}
				return accountRefreshResult(flight, requireTimezone)
			}
		}
		flight := &accountRefresh{done: make(chan struct{})}
		a.flight = flight
		a.mu.Unlock()

		snapshot, fingerprint, err := c.loadAccountContext(ctx)

		a.mu.Lock()
		if ctx.Err() != nil {
			// Cancelling an operation must not poison the cache for other operations.
			flight.cancelled = true
			flight.err = unavailable()
		} else if err != nil {
			a.retryAfter = a.now().Add(accountRetryDelay)
			flight.err = err
		} else {
			snapshot.Generation = a.snapshot.Generation
			if a.hasSnapshot && fingerprint != a.fingerprint {
				snapshot.Generation++
			}
			a.snapshot = snapshot
			a.fingerprint = fingerprint
			a.hasSnapshot = true
			completed := a.now()
			a.expires = completed.Add(a.ttl)
			a.retryAfter = time.Time{}
			a.timezoneRetryAfter = time.Time{}
			if snapshot.Location == nil {
				a.timezoneRetryAfter = completed.Add(accountRetryDelay)
			}
			flight.snapshot = snapshot
		}
		a.flight = nil
		close(flight.done)
		a.mu.Unlock()
		return accountRefreshResult(flight, requireTimezone)
	}
}

func (c *Client) loadAccountContext(ctx context.Context) (accountSnapshot, accountFingerprint, error) {
	var raw map[string]any
	if err := c.JSON(ctx, "GET", "myself", nil, nil, &raw, false); err != nil {
		return accountSnapshot{}, accountFingerprint{}, err
	}
	bot := make(map[string]any)
	for _, field := range []string{"accountId", "name", "key", "displayName", "emailAddress"} {
		if value, ok := raw[field].(string); ok {
			bot[field] = value
		}
	}
	identity := ""
	for _, field := range []string{"accountId", "key", "name"} {
		if value := str(bot[field]); strings.TrimSpace(value) != "" {
			identity = field + ":" + value
			break
		}
	}
	if identity == "" {
		return accountSnapshot{}, accountFingerprint{}, unavailable()
	}
	zone := str(raw["timeZone"])
	var location *time.Location
	if strings.TrimSpace(zone) != "" && zone != "Local" {
		location, _ = time.LoadLocation(zone)
	}
	return accountSnapshot{Bot: bot, Location: location}, accountFingerprint{identity: identity, timezone: zone, resolved: location != nil}, nil
}

func accountRefreshResult(flight *accountRefresh, requireTimezone bool) (accountSnapshot, error) {
	if flight.err != nil {
		return accountSnapshot{}, flight.err
	}
	snapshot := copyAccountSnapshot(flight.snapshot)
	if requireTimezone && snapshot.Location == nil {
		return snapshot, failure(503, "timezone_unavailable")
	}
	return snapshot, nil
}

func copyAccountSnapshot(snapshot accountSnapshot) accountSnapshot {
	bot := make(map[string]any, len(snapshot.Bot))
	for key, value := range snapshot.Bot {
		bot[key] = value
	}
	snapshot.Bot = bot
	return snapshot
}

func (c *Client) accountGeneration() uint64 {
	c.accounts.mu.Lock()
	defer c.accounts.mu.Unlock()
	return c.accounts.snapshot.Generation
}
