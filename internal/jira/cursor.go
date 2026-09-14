package jira

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

type cursor struct {
	Operation string `json:"o"`
	Project   string `json:"p"`
	Parent    string `json:"i,omitempty"`
	Query     Search `json:"q"`
	Start     int    `json:"s"`
	Config    string `json:"c"`
	Expires   int64  `json:"e"`
}

func (s *Server) encodeCursor(c cursor) string {
	b, _ := json.Marshal(c)
	m := hmac.New(sha256.New, s.secret[:])
	m.Write(b)
	return base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
func (s *Server) decodeCursor(raw, op, key, parent string) (cursor, error) {
	var c cursor
	if len(raw) > 32768 {
		return c, failure(400, "invalid_cursor")
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 2 {
		return c, failure(400, "invalid_cursor")
	}
	b, e := base64.RawURLEncoding.DecodeString(parts[0])
	if e != nil {
		return c, failure(400, "invalid_cursor")
	}
	sig, e := base64.RawURLEncoding.DecodeString(parts[1])
	if e != nil {
		return c, failure(400, "invalid_cursor")
	}
	m := hmac.New(sha256.New, s.secret[:])
	m.Write(b)
	if !hmac.Equal(sig, m.Sum(nil)) || json.Unmarshal(b, &c) != nil || c.Operation != op || c.Project != key || c.Parent != parent || c.Start < 0 {
		return c, failure(400, "invalid_cursor")
	}
	if c.Config != s.digest || s.now().Unix() >= c.Expires {
		return c, failure(410, "cursor_expired")
	}
	return c, nil
}
func (s *Server) nextCursor(op, key, parent string, q Search, start int, expires int64) string {
	if expires == 0 {
		expires = s.now().Add(15 * time.Minute).Unix()
	}
	return s.encodeCursor(cursor{Operation: op, Project: key, Parent: parent, Query: q, Start: start, Config: s.digest, Expires: expires})
}
