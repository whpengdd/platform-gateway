package cklogs

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var addrRe = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+`)

func ExtractAddr(value any) string {
	first := value
	if arr, ok := value.([]any); ok {
		if len(arr) == 0 {
			return ""
		}
		first = arr[0]
	}
	if arr, ok := value.([]string); ok {
		if len(arr) == 0 {
			return ""
		}
		first = arr[0]
	}
	if first == nil {
		return ""
	}
	s := strings.TrimSpace(asString(first))
	if s == "" {
		return ""
	}
	if m := addrRe.FindString(s); m != "" {
		return strings.ToLower(m)
	}
	return s
}

func asString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprint(t)
	}
}

func asFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case string:
		n, err := strconv.ParseFloat(t, 64)
		if err != nil {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}
