package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"platform-gateway/internal/auditlog"
	"platform-gateway/internal/strictjson"
	"strings"
	"sync/atomic"
	"time"
)

const MaxJSON = 4 << 20
const MaxFile = 10 << 20
const MaxBody = 256 << 10

var ContentTypes = []string{"image/png", "image/jpeg", "application/pdf", "text/plain"}

type Error struct {
	Status   int
	Code     string
	Upstream int
}

func (e *Error) Error() string               { return e.Code }
func failure(status int, code string) *Error { return &Error{Status: status, Code: code} }
func unavailable() *Error                    { return failure(503, "upstream_unavailable") }

type Client struct {
	base               *url.URL
	http               *http.Client
	bearer, user, pass string
}

func NewClient(base, bearer, user, pass string) (*Client, error) {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || strings.ContainsAny(u.Path, "\\%") {
		return nil, errors.New("invalid jira.baseUrl: expected an absolute HTTP or HTTPS URL without userinfo, query, fragment or unsafe path encoding")
	}
	for _, seg := range strings.Split(u.Path, "/") {
		if seg == "." || seg == ".." {
			return nil, errors.New("invalid jira.baseUrl: dot path segments are not allowed")
		}
	}
	if (bearer != "") == (user != "" || pass != "") || bearer == "" && (user == "" || pass == "") || strings.ContainsAny(bearer+user+pass, "\r\n") {
		return nil, errors.New("configure exactly one Jira authentication method")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return &Client{base: u, bearer: bearer, user: user, pass: pass, http: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 30 * time.Second, IdleConnTimeout: 90 * time.Second, MaxIdleConns: 8}}}, nil
}
func (c *Client) endpoint(path string, query url.Values) string {
	u := *c.base
	u.Path += "/rest/api/2/" + path
	u.RawQuery = query.Encode()
	return u.String()
}
func (c *Client) JSON(ctx context.Context, method, path string, query url.Values, input, output any, write bool) error {
	var b []byte
	var err error
	if input != nil {
		b, err = json.Marshal(input)
		if err != nil {
			return failure(400, "invalid_body")
		}
	}
	data, err := c.request(ctx, method, c.endpoint(path, query), "application/json", b, write, MaxJSON)
	if err != nil {
		return err
	}
	if output != nil && (strictjson.CheckNumbers(data) != nil || json.Unmarshal(data, output) != nil) {
		if write {
			return failure(502, "outcome_unknown")
		}
		return unavailable()
	}
	return nil
}
func (c *Client) request(ctx context.Context, method, target, contentType string, b []byte, write bool, max int64) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, unavailable()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(b))
	if err != nil {
		return nil, unavailable()
	}
	// No replay body or idempotency headers: transport must not replay writes.
	req.GetBody = nil
	req.Header.Set("Accept", "application/json")
	if len(b) > 0 {
		req.Header.Set("Content-Type", contentType)
	}
	if strings.HasPrefix(contentType, "multipart/") {
		req.Header.Set("X-Atlassian-Token", "no-check")
	}
	if c.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearer)
	} else {
		req.SetBasicAuth(c.user, c.pass)
	}
	var dispatched atomic.Bool
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{WroteHeaders: func() { dispatched.Store(true) }, WroteRequest: func(httptrace.WroteRequestInfo) { dispatched.Store(true) }}))
	resp, err := c.http.Do(req)
	if err != nil {
		if write && dispatched.Load() {
			return nil, failure(502, "outcome_unknown")
		}
		return nil, unavailable()
	}
	defer resp.Body.Close()
	if rec := auditlog.From(ctx); rec != nil {
		rec.SetJiraUpstream(resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		e := unavailable()
		e.Upstream = resp.StatusCode
		if write {
			e.Status = 502
			e.Code = "upstream_rejected"
			if resp.StatusCode >= 500 {
				e.Code = "outcome_unknown"
			}
		}
		return nil, e
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil || int64(len(data)) > max {
		if write {
			return nil, failure(502, "outcome_unknown")
		}
		return nil, unavailable()
	}
	return data, nil
}

type Issue struct {
	ID     string         `json:"id"`
	Key    string         `json:"key"`
	Fields map[string]any `json:"fields"`
}
type SearchResult struct {
	Issues []Issue `json:"issues"`
	Start  int     `json:"startAt"`
	Total  int     `json:"total"`
}

func (c *Client) Search(ctx context.Context, jql string, fields []string, start, limit int) (SearchResult, error) {
	var out SearchResult
	err := c.JSON(ctx, "POST", "search", nil, map[string]any{"jql": jql, "fields": fields, "startAt": start, "maxResults": limit, "validateQuery": true}, &out, false)
	return out, err
}
func (c *Client) Issue(ctx context.Context, id string, fields []string) (Issue, error) {
	var out Issue
	err := c.JSON(ctx, "GET", "issue/"+url.PathEscape(id), url.Values{"fields": {strings.Join(fields, ",")}}, nil, &out, false)
	return out, err
}
func (c *Client) download(ctx context.Context, raw, id, filename string) ([]byte, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != c.base.Scheme || u.Host != c.base.Host || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, unavailable()
	}
	expected := c.base.Path + "/secure/attachment/" + id + "/" + filename
	if u.Path != expected || strings.Contains(u.Path, "\\") || strings.Contains(filename, "%") {
		return nil, unavailable()
	}
	// Decode exactly once and reject alternative encodings of path separators/traversal.
	if u.EscapedPath() != strings.ReplaceAll(url.PathEscape(expected), "%2F", "/") {
		return nil, unavailable()
	}
	return c.request(ctx, "GET", u.String(), "", nil, false, MaxFile)
}
