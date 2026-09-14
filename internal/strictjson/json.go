// Package strictjson rejects ambiguous JSON before decoding typed input.
package strictjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

var ErrInvalid = errors.New("invalid JSON")

func Decode(b []byte, dst any) error {
	if !utf8.Valid(b) {
		return ErrInvalid
	}
	if CheckNumbers(b) != nil {
		return ErrInvalid
	}
	var tree any
	if json.Unmarshal(b, &tree) != nil || !exactKeys(tree, reflect.TypeOf(dst)) {
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return ErrInvalid
	}
	return nil
}
func value(d *json.Decoder, depth int) error {
	if depth > 64 {
		return ErrInvalid
	}
	t, err := d.Token()
	if err != nil {
		return err
	}
	if n, ok := t.(json.Number); ok && !numberRoundTrips(n.String()) {
		return ErrInvalid
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			k, err := d.Token()
			if err != nil {
				return err
			}
			key, ok := k.(string)
			if !ok || seen[key] {
				return ErrInvalid
			}
			seen[key] = true
			if err := value(d, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := value(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return ErrInvalid
	}
	_, err = d.Token()
	return err
}

// encoding/json accepts case-insensitive aliases; the public contract does not.
func exactKeys(v any, t reflect.Type) bool {
	if t == nil {
		return false
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if v == nil || t == reflect.TypeOf(json.RawMessage{}) {
		return true
	}
	switch t.Kind() {
	case reflect.Struct:
		m, ok := v.(map[string]any)
		if !ok {
			return false
		}
		fields := map[string]reflect.Type{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = f.Name
			}
			fields[name] = f.Type
		}
		for key, value := range m {
			typ, ok := fields[key]
			if !ok || !exactKeys(value, typ) {
				return false
			}
		}
	case reflect.Map:
		m, ok := v.(map[string]any)
		if !ok {
			return false
		}
		for _, value := range m {
			if !exactKeys(value, t.Elem()) {
				return false
			}
		}
	case reflect.Slice, reflect.Array:
		a, ok := v.([]any)
		if !ok {
			return false
		}
		for _, value := range a {
			if !exactKeys(value, t.Elem()) {
				return false
			}
		}
	}
	return true
}

// CheckNumbers rejects numbers that float64 decoding and JSON re-encoding would
// silently change. It also rejects duplicate keys and trailing documents. Jira
// upstream responses use this without imposing our input field whitelist.
func CheckNumbers(b []byte) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := value(d, 0); err != nil {
		return ErrInvalid
	}
	if _, err := d.Token(); err != io.EOF {
		return ErrInvalid
	}
	return nil
}
func numberRoundTrips(raw string) bool {
	// Bound exact-comparison work as well as accepted numeric precision.
	if len(raw) > 1024 {
		return false
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return false
	}
	if i := strings.IndexAny(raw, "eE"); i >= 0 {
		exp, err := strconv.ParseInt(raw[i+1:], 10, 32)
		if err != nil || exp < -2048 || exp > 2048 {
			return false
		}
	}
	encoded, err := json.Marshal(f)
	if err != nil {
		return false
	}
	original, ok := new(big.Rat).SetString(raw)
	if !ok {
		return false
	}
	roundTrip, ok := new(big.Rat).SetString(string(encoded))
	return ok && original.Cmp(roundTrip) == 0
}
