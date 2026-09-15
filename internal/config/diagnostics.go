package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"
)

func invalid(path, reason string) error {
	return fmt.Errorf("%w: %s: %s", ErrConfig, path, reason)
}

// Walk the typed schema before decoding to locate errors without echoing input
// values or arbitrary keys (which may themselves contain pasted credentials).
func validateJSON(b []byte) error {
	if !utf8.Valid(b) {
		return invalid("$", "JSON must be valid UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := jsonValue(d, reflect.TypeOf(File{}), "$", 0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return jsonError(d, "$", "unexpected content after the JSON document")
	}
	return nil
}
func jsonError(d *json.Decoder, path, reason string) error {
	return invalid(path, fmt.Sprintf("%s (near byte %d)", reason, d.InputOffset()+1))
}
func jsonValue(d *json.Decoder, typ reflect.Type, path string, depth int) error {
	if depth > 64 {
		return jsonError(d, path, "JSON nesting exceeds 64 levels")
	}
	for typ != nil && typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ != nil && typ.Kind() == reflect.Interface {
		typ = nil
	}
	token, err := d.Token()
	if err != nil {
		return jsonError(d, path, "malformed or incomplete JSON")
	}
	if token == nil {
		if typ != nil {
			return jsonError(d, path, "null is not allowed; omit optional settings to use defaults")
		}
		return nil
	}
	if delim, ok := token.(json.Delim); ok {
		switch delim {
		case '{':
			if typ != nil && typ.Kind() != reflect.Struct && typ.Kind() != reflect.Map {
				return jsonError(d, path, "expected "+jsonType(typ))
			}
			fields := map[string]reflect.Type{}
			if typ != nil && typ.Kind() == reflect.Struct {
				for i := 0; i < typ.NumField(); i++ {
					f := typ.Field(i)
					fields[strings.Split(f.Tag.Get("json"), ",")[0]] = f.Type
				}
			}
			seen := map[string]bool{}
			for d.More() {
				keyToken, err := d.Token()
				if err != nil {
					return jsonError(d, path, "malformed JSON object key")
				}
				key, ok := keyToken.(string)
				if !ok {
					return jsonError(d, path, "expected an object key")
				}
				childPath, childType := path, reflect.Type(nil)
				if typ != nil && typ.Kind() == reflect.Struct {
					var known bool
					childType, known = fields[key]
					if !known {
						return jsonError(d, path, "unknown field; field names are case-sensitive")
					}
					childPath = key
					if path != "$" {
						childPath = path + "." + key
					}
				} else if typ != nil && typ.Kind() == reflect.Map {
					childType = typ.Elem()
					// Only validated project IDs are included; arbitrary default-field keys
					// and all user-supplied values stay out of diagnostic messages.
					if path == "jira.projects" && ProjectKey.MatchString(key) {
						childPath = path + "." + key
					}
				}
				if seen[key] {
					return jsonError(d, childPath, "duplicate object key")
				}
				seen[key] = true
				if err := jsonValue(d, childType, childPath, depth+1); err != nil {
					return err
				}
			}
			if end, err := d.Token(); err != nil || end != json.Delim('}') {
				return jsonError(d, path, "unterminated JSON object")
			}
			return nil
		case '[':
			if typ != nil && typ.Kind() != reflect.Slice {
				return jsonError(d, path, "expected "+jsonType(typ))
			}
			var childType reflect.Type
			if typ != nil {
				childType = typ.Elem()
			}
			for i := 0; d.More(); i++ {
				if err := jsonValue(d, childType, fmt.Sprintf("%s[%d]", path, i), depth+1); err != nil {
					return err
				}
			}
			if end, err := d.Token(); err != nil || end != json.Delim(']') {
				return jsonError(d, path, "unterminated JSON array")
			}
			return nil
		default:
			return jsonError(d, path, "unexpected JSON delimiter")
		}
	}
	if typ != nil {
		raw, err := json.Marshal(token)
		if err != nil || json.Unmarshal(raw, reflect.New(typ).Interface()) != nil {
			return jsonError(d, path, "expected "+jsonType(typ))
		}
	}
	return nil
}
func jsonType(t reflect.Type) string {
	switch t.Kind() {
	case reflect.Struct, reflect.Map:
		return "an object"
	case reflect.Slice:
		return "an array"
	case reflect.String:
		return "a string"
	case reflect.Bool:
		return "a boolean"
	case reflect.Int, reflect.Int64:
		return fmt.Sprintf("an integer within the signed %d-bit range", t.Bits())
	default:
		return "a value of the declared type"
	}
}
