package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestChecker_RejectsMissingAndWrongToken(t *testing.T) {
	c := New([]string{"good-token", "rotate-token"}, nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/cklogs/delivery", nil)
	if status, _, _ := c.Check(req); status != http.StatusUnauthorized {
		t.Fatalf("missing token status=%d", status)
	}
	req.Header.Set("Authorization", "Bearer wrong")
	if status, _, _ := c.Check(req); status != http.StatusUnauthorized {
		t.Fatalf("wrong token status=%d", status)
	}
	req.Header.Set("Authorization", "Bearer rotate-token")
	if status, _, _ := c.Check(req); status != 0 {
		t.Fatalf("rotation token status=%d", status)
	}
}

func TestChecker_NotReadyWithoutTokens(t *testing.T) {
	c := New(nil, nil)
	if c.Ready() {
		t.Fatal("expected not ready")
	}
}

func TestChecker_ClassExternalVsInternal(t *testing.T) {
	c := NewWithClasses([]string{"ext-token"}, []string{"int-token"}, nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/cklogs/analysis/delivery", nil)
	req.Header.Set("Authorization", "Bearer ext-token")
	status, _, _, class := c.CheckClass(req)
	if status != 0 || class != ClassExternal {
		t.Fatalf("external status=%d class=%q", status, class)
	}
	req.Header.Set("Authorization", "Bearer int-token")
	status, _, _, class = c.CheckClass(req)
	if status != 0 || class != ClassInternal {
		t.Fatalf("internal status=%d class=%q", status, class)
	}
}

func TestChecker_DuplicateTokenKeepsInternal(t *testing.T) {
	c := NewWithClasses([]string{"shared"}, []string{"shared"}, nil)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer shared")
	_, _, _, class := c.CheckClass(req)
	if class != ClassInternal {
		t.Fatalf("class=%q", class)
	}
}
