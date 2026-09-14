package strictjson

import (
	"strings"
	"testing"
)

func TestExactKeysAndAmbiguity(t *testing.T) {
	for _, b := range []string{`{"Token":"x"}`, `{"token":"x","TOKEN":"y"}`, `{"token":"x","token":"y"}`, `{"token":"x"} {}`, `{"token":"x","child":{"Field":1}}`, strings.Repeat("[", 66) + strings.Repeat("]", 66)} {
		var dst struct {
			Token string `json:"token"`
			Child struct {
				Field int `json:"field"`
			} `json:"child"`
		}
		if Decode([]byte(b), &dst) == nil {
			t.Fatal("accepted", b)
		}
	}
}

func TestNumbersMustRoundTripWithoutChangingValue(t *testing.T) {
	for _, raw := range []string{"9007199254740992", "9007199254740994", "1.2300", "1e2", "0.1", "5e-324", "-0", "0.000"} {
		var v map[string]any
		if err := Decode([]byte(`{"value":`+raw+`}`), &v); err != nil {
			t.Errorf("representable %s: %v", raw, err)
		}
	}
	for _, raw := range []string{"9007199254740993", "-9007199254740993", "0.10000000000000001", "1e-999", "1e999", "1e99999999", strings.Repeat("0", 1025) + ".1"} {
		var v map[string]any
		if err := Decode([]byte(`{"value":`+raw+`}`), &v); err == nil {
			t.Errorf("lossy number accepted: %s", raw)
		}
	}
}
