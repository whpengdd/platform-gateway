// Package jql checks fragment boundaries; Jira remains the semantic authority.
package jql

import (
	"errors"
	"regexp"
	"strings"
	"unicode"
)

var errFragment = errors.New("invalid filterJql structure")

func Validate(s string) error {
	if len(s) > 8192 {
		return errFragment
	}
	depth := 0
	var quote rune
	escape := false
	var outside strings.Builder
	for _, c := range s {
		if unicode.IsControl(c) && c != '\t' && c != '\n' && c != '\r' {
			return errFragment
		}
		if quote != 0 {
			if escape {
				escape = false
				continue
			}
			if c == '\\' {
				escape = true
				continue
			}
			if c == quote {
				quote = 0
			}
			outside.WriteRune(' ')
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
			outside.WriteRune(' ')
		case '(':
			depth++
			outside.WriteRune(c)
		case ')':
			depth--
			if depth < 0 {
				return errFragment
			}
			outside.WriteRune(c)
		case ';', '\\':
			return errFragment
		default:
			outside.WriteRune(c)
		}
	}
	if depth != 0 || quote != 0 || escape {
		return errFragment
	}
	if regexp.MustCompile(`(?i)\border\s+by\b`).MatchString(outside.String()) || strings.Contains(outside.String(), "/*") || strings.Contains(outside.String(), "//") {
		return errFragment
	}
	return nil
}
func Literal(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
func Scope(project, filter, query string) string {
	s := "(project = " + Literal(project) + ")"
	if strings.TrimSpace(filter) != "" {
		s += " AND (" + filter + ")"
	}
	if query != "" {
		s += " AND (" + query + ")"
	}
	return s
}

// Label consumes the complete finite labels-equality grammar, including wrappers.
func Label(s string) (string, bool) {
	if Validate(s) != nil {
		return "", false
	}
	s = strings.TrimSpace(s)
	n := 0
	for strings.HasPrefix(s, "(") {
		n++
		s = strings.TrimSpace(s[1:])
	}
	for i := 0; i < n; i++ {
		if !strings.HasSuffix(s, ")") {
			return "", false
		}
		s = strings.TrimSpace(s[:len(s)-1])
	}
	if len(s) < 6 || !strings.EqualFold(s[:6], "labels") {
		return "", false
	}
	s = strings.TrimSpace(s[6:])
	if !strings.HasPrefix(s, "=") {
		return "", false
	}
	s = strings.TrimSpace(s[1:])
	if s == "" {
		return "", false
	}
	if s[0] == '"' || s[0] == '\'' {
		q := s[0]
		var b strings.Builder
		for i := 1; i < len(s); i++ {
			if s[i] == q {
				return b.String(), i == len(s)-1 && b.Len() > 0
			}
			if s[i] == '\\' {
				i++
				if i >= len(s) || s[i] != q && s[i] != '\\' {
					return "", false
				}
			}
			b.WriteByte(s[i])
		}
		return "", false
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(s) || strings.EqualFold(s, "empty") || strings.EqualFold(s, "null") {
		return "", false
	}
	return s, true
}
