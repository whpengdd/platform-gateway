package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"platform-gateway/internal/config"
	"strings"
)

type Class string

const (
	ClassNone     Class = ""
	ClassExternal Class = "external"
	ClassInternal Class = "internal"
)

type tokenEntry struct {
	sha      [32]byte
	class    Class
	projects []string
}

type Checker struct {
	entries []tokenEntry
	cidrs   []*net.IPNet
}

type classCtxKey struct{}

func WithClass(ctx context.Context, class Class) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, classCtxKey{}, class)
}

func ClassFrom(ctx context.Context) Class {
	if ctx == nil {
		return ClassNone
	}
	class, _ := ctx.Value(classCtxKey{}).(Class)
	return class
}

// New treats every token as external (legacy GATEWAY_AUTH_TOKENS).
func New(tokens []string, cidrs []*net.IPNet) *Checker {
	return NewWithClasses(tokens, nil, cidrs)
}

func NewWithClasses(external, internal []string, cidrs []*net.IPNet) *Checker {
	c := &Checker{cidrs: cidrs}
	for _, tok := range internal {
		c.add(tok, ClassInternal)
	}
	for _, tok := range external {
		c.add(tok, ClassExternal)
	}
	return c
}

func (c *Checker) add(tok string, class Class) {
	tok = strings.TrimSpace(tok)
	if tok == "" || c == nil {
		return
	}
	sum := sha256.Sum256([]byte(tok))
	for i := range c.entries {
		if subtle.ConstantTimeCompare(c.entries[i].sha[:], sum[:]) == 1 {
			if class == ClassInternal {
				c.entries[i].class = ClassInternal
			}
			return
		}
	}
	c.entries = append(c.entries, tokenEntry{sha: sum, class: class})
}

func ParseTokens(raw string) []string {
	var out []string
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func ParseCIDRs(raw string) ([]*net.IPNet, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var out []*net.IPNet
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		_, n, err := net.ParseCIDR(p)
		if err != nil {
			ip := net.ParseIP(p)
			if ip == nil {
				return nil, err
			}
			if ip.To4() != nil {
				_, n, err = net.ParseCIDR(ip.String() + "/32")
			} else {
				_, n, err = net.ParseCIDR(ip.String() + "/128")
			}
			if err != nil {
				return nil, err
			}
		}
		out = append(out, n)
	}
	return out, nil
}

func (c *Checker) Ready() bool {
	return c != nil && len(c.entries) > 0
}

// Check returns HTTP status 0 when the request is allowed.
func (c *Checker) Check(r *http.Request) (status int, code, msg string) {
	status, code, msg, _ = c.CheckClass(r)
	return
}

func (c *Checker) CheckClass(r *http.Request) (status int, code, msg string, class Class) {
	if c == nil || len(c.entries) == 0 {
		return http.StatusUnauthorized, "unauthorized", "未授权", ClassNone
	}
	if len(c.cidrs) > 0 {
		ip := requestIP(r)
		if ip == nil || !ipAllowed(ip, c.cidrs) {
			return http.StatusForbidden, "forbidden", "来源地址不在允许范围", ClassNone
		}
	}
	tok, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		return http.StatusUnauthorized, "unauthorized", "未授权", ClassNone
	}
	class, ok = c.classOf(tok)
	if !ok {
		return http.StatusUnauthorized, "unauthorized", "未授权", ClassNone
	}
	return 0, "", "", class
}

func (c *Checker) classOf(got string) (Class, bool) {
	sum := sha256.Sum256([]byte(got))
	var found Class
	matched := 0
	for _, entry := range c.entries {
		if subtle.ConstantTimeCompare(sum[:], entry.sha[:]) == 1 {
			found = entry.class
			matched = 1
		}
	}
	return found, matched == 1
}

func bearerToken(h string) (string, bool) {
	h = strings.TrimSpace(h)
	const prefix = "Bearer "
	if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	tok := strings.TrimSpace(h[len(prefix):])
	if tok == "" {
		return "", false
	}
	return tok, true
}

func requestIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return net.ParseIP(r.RemoteAddr)
	}
	return net.ParseIP(host)
}

func ipAllowed(ip net.IP, cidrs []*net.IPNet) bool {
	for _, n := range cidrs {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// Principal carries one service domain; credentials are retained only as digests.
type Principal struct {
	Class       Class
	Projects    []string
	Fingerprint string
}

func NewFromFile(f *config.File, cidrs []*net.IPNet) *Checker {
	c := &Checker{cidrs: cidrs}
	for _, t := range f.Tokens {
		c.entries = append(c.entries, tokenEntry{sha: sha256.Sum256([]byte(t.Token)), class: Class(t.CKLogs), projects: append([]string(nil), t.JiraProjects...)})
	}
	return c
}
func (c *Checker) Authenticate(r *http.Request) (Principal, int, string) {
	status, code, _, _ := c.CheckClass(r)
	if status != 0 {
		return Principal{}, status, code
	}
	tok, _ := bearerToken(r.Header.Get("Authorization"))
	sum := sha256.Sum256([]byte(tok))
	var p Principal
	for _, e := range c.entries {
		if subtle.ConstantTimeCompare(sum[:], e.sha[:]) == 1 {
			p = Principal{Class: e.class, Projects: append([]string(nil), e.projects...), Fingerprint: hex.EncodeToString(sum[:8])}
		}
	}
	return p, 0, ""
}
func (p Principal) Allows(project string) bool {
	for _, key := range p.Projects {
		if key == project {
			return true
		}
	}
	return false
}
func (c *Checker) HasCKLogs() bool {
	if c != nil {
		for _, e := range c.entries {
			if e.class != ClassNone {
				return true
			}
		}
	}
	return false
}
