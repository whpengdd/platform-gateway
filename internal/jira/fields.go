package jira

import (
	"context"
	"net/url"
	"platform-gateway/internal/config"
	"platform-gateway/internal/jql"
	"reflect"
	"strconv"
	"strings"
)

type Schema struct {
	Type   string `json:"type"`
	Items  string `json:"items"`
	Custom string `json:"custom"`
}
type Field struct {
	ID         string   `json:"fieldId"`
	Required   bool     `json:"required"`
	Default    bool     `json:"hasDefaultValue"`
	Schema     Schema   `json:"schema"`
	Allowed    []any    `json:"allowedValues"`
	Operations []string `json:"operations"`
}

func (s Schema) supported() bool {
	t := s.Type
	if t == "array" {
		t = s.Items
	}
	switch t {
	case "string", "number", "boolean", "option", "user", "priority", "component", "version":
	default:
		return false
	}
	if s.Custom != "" {
		if !strings.HasPrefix(s.Custom, "com.atlassian.jira.plugin.system.customfieldtypes:") {
			return false
		}
		switch strings.TrimPrefix(s.Custom, "com.atlassian.jira.plugin.system.customfieldtypes:") {
		case "textfield", "textarea", "float", "select", "multiselect", "radiobuttons", "multicheckboxes", "userpicker", "multiuserpicker", "labels", "datepicker", "datetime":
		default:
			return false
		}
	}
	return true
}
func object(v any) map[string]any { m, _ := v.(map[string]any); return m }
func str(v any) string            { s, _ := v.(string); return s }
func person(v any) any {
	if v == nil {
		return nil
	}
	m := object(v)
	id := str(m["accountId"])
	if id == "" {
		id = str(m["key"])
	}
	if id == "" {
		id = str(m["name"])
	}
	if id == "" {
		return nil
	}
	return map[string]any{"identity": id, "displayName": str(m["displayName"]), "email": nullable(str(m["emailAddress"]))}
}
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func projectValue(v any, s Schema) (any, bool) {
	if v == nil {
		return nil, true
	}
	if !s.supported() {
		return nil, false
	}
	if s.Type == "array" {
		a, ok := v.([]any)
		if !ok || len(a) > 1000 {
			return nil, false
		}
		out := []any{}
		for _, item := range a {
			x, ok := projectValue(item, Schema{Type: s.Items})
			if !ok {
				return nil, false
			}
			out = append(out, x)
		}
		return out, true
	}
	switch s.Type {
	case "string":
		v, ok := v.(string)
		return v, ok && len(v) <= 32768
	case "number":
		v, ok := v.(float64)
		return v, ok
	case "boolean":
		v, ok := v.(bool)
		return v, ok
	case "user":
		p := person(v)
		return p, p != nil
	default:
		m := object(v)
		id := str(m["id"])
		value := str(m["value"])
		if value == "" {
			value = str(m["name"])
		}
		if id == "" && value == "" {
			return nil, false
		}
		return map[string]any{"id": id, "value": value}, true
	}
}
func validateValue(v any, f Field) bool {
	if v == nil {
		return !f.Required
	}
	if f.Required {
		if value, ok := v.(string); ok && strings.TrimSpace(value) == "" {
			return false
		}
		if value, ok := v.([]any); ok && len(value) == 0 {
			return false
		}
	}
	if _, ok := projectValue(v, f.Schema); !ok {
		return false
	}
	if f.Schema.Type == "array" {
		for _, x := range v.([]any) {
			g := f
			g.Schema.Type = f.Schema.Items
			if !validateValue(x, g) {
				return false
			}
		}
		return true
	}
	if m := object(v); m != nil {
		for k := range m {
			if f.Schema.Type == "user" {
				if k != "accountId" && k != "name" && k != "key" {
					return false
				}
			} else if k != "id" && k != "value" {
				return false
			}
		}
	}
	if len(f.Allowed) > 0 {
		for _, a := range f.Allowed {
			if reflect.DeepEqual(a, v) {
				return true
			}
			m, n := object(v), object(a)
			if m != nil && n != nil && len(m) > 0 {
				match := true
				for key, value := range m {
					if !reflect.DeepEqual(value, n[key]) {
						match = false
						break
					}
				}
				if match {
					return true
				}
			}
		}
		return false
	}
	return true
}
func (c *Client) metadata(ctx context.Context, key, typeID string) (map[string]Field, error) {
	var old struct {
		Projects []struct {
			Key   string `json:"key"`
			Types []struct {
				ID     string           `json:"id"`
				Fields map[string]Field `json:"fields"`
			} `json:"issuetypes"`
		} `json:"projects"`
	}
	err := c.JSON(ctx, "GET", "issue/createmeta", url.Values{"projectKeys": {key}, "issuetypeIds": {typeID}, "expand": {"projects.issuetypes.fields"}}, nil, &old, false)
	if err == nil {
		for _, p := range old.Projects {
			if p.Key == key {
				for _, t := range p.Types {
					if t.ID == typeID && len(t.Fields) > 0 {
						return t.Fields, nil
					}
				}
			}
		}
		return nil, unavailable()
	}
	e, ok := err.(*Error)
	if !ok || e.Upstream != 404 && e.Upstream != 405 && e.Upstream != 410 {
		return nil, err
	}
	out := map[string]Field{}
	for start := 0; start < 2000; {
		var page struct {
			Values []Field `json:"values"`
			Fields []Field `json:"fields"`
			Start  int     `json:"startAt"`
			Total  int     `json:"total"`
			Last   bool    `json:"isLast"`
		}
		if err := c.JSON(ctx, "GET", "issue/createmeta/"+key+"/issuetypes/"+typeID, url.Values{"startAt": {strconv.Itoa(start)}, "maxResults": {"100"}}, nil, &page, false); err != nil {
			return nil, err
		}
		fields := page.Values
		if fields == nil {
			fields = page.Fields
		}
		if len(fields) == 0 || page.Start != start {
			return nil, unavailable()
		}
		for _, f := range fields {
			if f.ID == "" {
				return nil, unavailable()
			}
			if _, ok := out[f.ID]; ok {
				return nil, unavailable()
			}
			out[f.ID] = f
		}
		start += len(fields)
		if page.Last || page.Total > 0 && start >= page.Total {
			return out, nil
		}
	}
	return nil, unavailable()
}
func (c *Client) readSchemas(ctx context.Context, p config.Project) (map[string]Schema, error) {
	out := map[string]Schema{}
	needed := false
	for _, f := range p.ReadFields {
		if config.CustomField.MatchString(f) {
			needed = true
		}
	}
	if !needed {
		return out, nil
	}
	var all []struct {
		ID     string `json:"id"`
		Schema Schema `json:"schema"`
	}
	if err := c.JSON(ctx, "GET", "field", nil, nil, &all, false); err != nil {
		return nil, err
	}
	for _, f := range all {
		out[f.ID] = f.Schema
	}
	for _, f := range p.ReadFields {
		if config.CustomField.MatchString(f) && !out[f].supported() {
			return nil, failure(503, "field_type_unavailable")
		}
	}
	return out, nil
}
func createProof(p config.Project) bool {
	if strings.TrimSpace(p.FilterJQL) == "" {
		return true
	}
	label, ok := jql.Label(p.FilterJQL)
	if !ok {
		return false
	}
	a, ok := p.CreateDefaults["labels"].([]any)
	if !ok {
		return false
	}
	for _, v := range a {
		if v == label {
			return true
		}
	}
	return false
}
func projectIssue(i Issue, key string, p config.Project, schemas map[string]Schema) (any, error) {
	if str(object(i.Fields["project"])["key"]) != key || !config.NumericID.MatchString(i.ID) || !issueKey.MatchString(i.Key) {
		return nil, failure(404, "resource_not_available")
	}
	fields := map[string]any{}
	for _, f := range p.ReadFields {
		v, exists := i.Fields[f]
		if !exists {
			continue
		}
		if v == nil {
			fields[f] = nil
			continue
		}
		switch f {
		case "summary", "description", "updated":
			s, ok := v.(string)
			if !ok || len(s) > 32768 {
				return nil, unavailable()
			}
			fields[f] = s
		case "status":
			m := object(v)
			if m == nil {
				return nil, unavailable()
			}
			fields[f] = map[string]any{"id": str(m["id"]), "name": str(m["name"])}
		case "reporter", "creator", "assignee":
			fields[f] = person(v)
		case "attachment":
			a, ok := v.([]any)
			if !ok {
				return nil, unavailable()
			}
			out := []any{}
			for _, v := range a {
				m, ok := attachmentProjection(v)
				if !ok {
					return nil, unavailable()
				}
				out = append(out, m)
			}
			fields[f] = out
		default:
			x, ok := projectValue(v, schemas[f])
			if !ok {
				return nil, unavailable()
			}
			fields[f] = x
		}
	}
	return map[string]any{"projectId": key, "issueKey": i.Key, "fields": fields}, nil
}
func attachmentProjection(v any) (map[string]any, bool) {
	m := object(v)
	id := str(m["id"])
	size, ok := m["size"].(float64)
	if !config.NumericID.MatchString(id) || !ok || size < 0 {
		return nil, false
	}
	return map[string]any{"id": id, "filename": str(m["filename"]), "size": size, "mimeType": str(m["mimeType"])}, true
}

func (f Field) settable() bool {
	for _, op := range f.Operations {
		if op == "set" {
			return true
		}
	}
	return false
}
