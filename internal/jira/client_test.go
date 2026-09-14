package jira

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"platform-gateway/internal/config"
	"strings"
	"sync/atomic"
	"testing"
)

func testClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	up := httptest.NewTLSServer(h)
	t.Cleanup(up.Close)
	c, err := NewClient(up.URL+"/jira", "upstream-secret", "", "")
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(up.Certificate())
	c.http.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return c
}
func TestTransport(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer upstream-secret" || r.URL.Path != "/jira/rest/api/2/issue/123" || r.URL.Query().Get("fields") != "a,b" {
			t.Error("incorrect request")
		}
		io.WriteString(w, `{"id":"123"}`)
	})
	if c.http.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("implicit proxy")
	}
	if _, err := c.Issue(context.Background(), "123", []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
	for _, base := range []string{"http://example.com", "https://user@example.com", "https://example.com/?url=x", "https://example.com/../x"} {
		if _, err := NewClient(base, "x", "", ""); err == nil {
			t.Fatal(base)
		}
	}
	if _, err := NewClient("https://example.com", "x", "user", "pass"); err == nil {
		t.Fatal("mixed auth")
	}
}
func TestLostWriteNoRetry(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		conn.Close()
	})
	var out any
	err := c.JSON(context.Background(), "POST", "issue", nil, map[string]string{"body": "secret"}, &out, true)
	e, ok := err.(*Error)
	if !ok || e.Code != "outcome_unknown" || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = c.JSON(ctx, "POST", "issue", nil, nil, &out, true)
	if err.(*Error).Code != "upstream_unavailable" || calls.Load() != 1 {
		t.Fatal(err)
	}
}
func TestRedirectLimitsAndRedaction(t *testing.T) {
	for _, mode := range []string{"redirect", "large", "reject"} {
		t.Run(mode, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "redirect":
					w.Header().Set("Location", "https://outside.invalid/secret")
					w.WriteHeader(302)
				case "large":
					io.WriteString(w, strings.Repeat("x", MaxJSON+1))
				case "reject":
					w.WriteHeader(400)
					io.WriteString(w, "upstream-secret private-body")
				}
			})
			var out any
			err := c.JSON(context.Background(), "POST", "issue", nil, map[string]string{"a": "b"}, &out, true)
			if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "private") {
				t.Fatal(err)
			}
		})
	}
}
func TestAttachmentURLs(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "abc") })
	base := c.base.String()
	for _, raw := range []string{base + "/secure/attachment/1/../x", base + "/secure/attachment/2/a.txt", base + "/secure/attachment/1/a.txt?x=y", strings.Replace(base, "https://", "https://user@", 1) + "/secure/attachment/1/a.txt", base + "/secure/attachment/1/%61.txt", base + "/secure/attachment/1/%2e%2e%2fx", "https://other.invalid/secure/attachment/1/a.txt"} {
		if _, err := c.download(context.Background(), raw, "1", "a.txt"); err == nil {
			t.Fatal(raw)
		}
	}
	if b, err := c.download(context.Background(), base+"/secure/attachment/1/a.txt", "1", "a.txt"); err != nil || string(b) != "abc" {
		t.Fatal(err)
	}
}
func TestBasicAndEncoding(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != "user" || p != "pass" || r.URL.Query().Get("text") != `a & "b"` {
			t.Error("encoding or auth")
		}
		io.WriteString(w, `{}`)
	})
	c.bearer = ""
	c.user = "user"
	c.pass = "pass"
	if err := c.JSON(context.Background(), "GET", "field", url.Values{"text": {`a & "b"`}}, nil, nil, false); err != nil {
		t.Fatal(err)
	}
}

func TestFileAuthenticationSharedContextSearch(t *testing.T) {
	for _, mode := range []string{"bearer", "basic"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/jira/rest/api/2/search" {
					t.Error("context path", r.URL.Path)
				}
				if mode == "bearer" {
					if r.Header.Get("Authorization") != "Bearer upstream-$token" {
						t.Error("bearer")
					}
				} else {
					user, pass, ok := r.BasicAuth()
					if !ok || user != "user" || pass != " $'\" " {
						t.Error("basic bytes")
					}
				}
				io.WriteString(w, `{"startAt":0,"total":0,"issues":[]}`)
			}))
			defer up.Close()
			auth := `{"type":"bearer","token":"upstream-$token"}`
			if mode == "basic" {
				auth = `{"type":"basic","username":"user","password":" $'\" "}`
			}
			f, err := config.Parse([]byte(`{"tokens":[{"token":"inbound","jiraProjects":["CS","IT"]}],"jira":{"baseUrl":"` + up.URL + `/jira/","auth":` + auth + `,"projects":{"CS":{},"IT":{}}}}`))
			if err != nil {
				t.Fatal(err)
			}
			a := f.Jira.Auth
			c, err := NewClient(f.Jira.BaseURL, a.Token, a.Username, a.Password)
			if err != nil {
				t.Fatal(err)
			}
			c.http = up.Client()
			for project := range f.Jira.Projects {
				if _, err := c.Search(context.Background(), "project = "+project, []string{"summary"}, 0, 10); err != nil {
					t.Fatal(err)
				}
			}
			if calls != 2 {
				t.Fatal("shared upstream calls", calls)
			}
		})
	}
}
