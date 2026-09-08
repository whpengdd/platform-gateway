package httpapi

import (
	"errors"
	"testing"
	"time"
)

func TestParseTimeRangePolicy(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)

	t.Run("defaults to recent 24 hours", func(t *testing.T) {
		got, err := parseTimeRange(nil, now)
		if err != nil || got.FromMs != now.Add(-24*time.Hour).UnixMilli() || got.ToMs != now.UnixMilli() {
			t.Fatalf("got=%+v err=%v", got, err)
		}
	})

	t.Run("allows seven days at 30-day lookback boundary", func(t *testing.T) {
		tr := &timeRangeJSON{
			From: now.Add(-30 * 24 * time.Hour).Format(time.RFC3339Nano),
			To:   now.Add(-23 * 24 * time.Hour).Format(time.RFC3339Nano),
		}
		if _, err := parseTimeRange(tr, now); err != nil {
			t.Fatal(err)
		}
	})

	for _, tc := range []struct {
		name string
		tr   *timeRangeJSON
		code string
	}{
		{
			name: "rejects older than 30 days",
			tr: &timeRangeJSON{
				From: now.Add(-30*24*time.Hour - time.Millisecond).Format(time.RFC3339Nano),
				To:   now.Add(-29 * 24 * time.Hour).Format(time.RFC3339Nano),
			},
			code: "time_range_too_old",
		},
		{
			name: "rejects wider than seven days",
			tr: &timeRangeJSON{
				From: now.Add(-8 * 24 * time.Hour).Format(time.RFC3339Nano),
				To:   now.Format(time.RFC3339Nano),
			},
			code: "time_range_too_large",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseTimeRange(tc.tr, now)
			var apiErr *apiError
			if !errors.As(err, &apiErr) || apiErr.Code != tc.code {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestParsePeer_requiresNonEmptyFilter(t *testing.T) {
	for _, raw := range []string{"", "   "} {
		_, err := parsePeer(raw)
		var apiErr *apiError
		if !errors.As(err, &apiErr) || apiErr.Code != "invalid_peer_filter" {
			t.Fatalf("raw=%q err=%v", raw, err)
		}
	}
}

func TestParseAccount_requiresWholeStringMailbox(t *testing.T) {
	got, err := parseAccount("User@Example.com")
	if err != nil || got != "user@example.com" {
		t.Fatalf("got=%q err=%v", got, err)
	}
	if _, err := parseAccount(""); err == nil {
		t.Fatal("empty")
	}
	if _, err := parseAccount("please check a@b.com"); err == nil {
		t.Fatal("prose should be rejected")
	}
	if _, err := parseAccount("a@b"); err == nil {
		t.Fatal("single-label domain should be rejected")
	}
	if _, err := parseAccount("a@b.com extra"); err == nil {
		t.Fatal("trailing text should be rejected")
	}
}
