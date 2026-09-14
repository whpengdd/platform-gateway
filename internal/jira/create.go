package jira

import (
	"context"
	"net/http"
	"platform-gateway/internal/config"
	"reflect"
	"strings"
)

type creation struct {
	fields map[string]Field
	bot    map[string]any
	reason string
}

func (s *Server) creation(ctx context.Context, key string, p *project) creation {
	out := creation{reason: "metadata_unavailable"}
	var bot map[string]any
	if s.client.JSON(ctx, "GET", "myself", nil, nil, &bot, false) != nil || person(bot) == nil {
		return out
	}
	out.bot = bot
	if p.IssueTypeID == "" || len(p.CreateFields) == 0 {
		out.reason = "creation_not_configured"
		return out
	}
	if !createProof(p.Project) {
		out.reason = "filter_not_provable"
		return out
	}
	fields, err := s.client.metadata(ctx, key, p.IssueTypeID)
	if err != nil {
		return out
	}
	out.fields = fields
	allowed := map[string]bool{}
	for _, f := range p.CreateFields {
		allowed[f] = true
	}
	for f := range p.CreateDefaults {
		allowed[f] = true
	}
	for f := range allowed {
		meta, ok := fields[f]
		if !ok || !meta.Schema.supported() || !meta.settable() {
			out.reason = "unsupported_create_field"
			return out
		}
		if v, ok := p.CreateDefaults[f]; ok && (!validateValue(v, meta) || f == "summary" && (strings.TrimSpace(str(v)) == "" || len(str(v)) > 1024)) {
			out.reason = "invalid_create_defaults"
			return out
		}
	}
	for f, m := range fields {
		if m.Required && !m.Default && !allowed[f] && f != "project" && f != "issuetype" && f != "reporter" {
			out.reason = "required_field_unavailable"
			return out
		}
	}
	// Reporter must be explicitly settable for the fixed bot identity.
	if meta, ok := fields["reporter"]; !ok || meta.Schema.Type != "user" || !meta.settable() {
		out.reason = "reporter_unavailable"
		return out
	}
	out.reason = ""
	return out
}
func (s *Server) capabilities(ctx context.Context, key string, p *project) (any, error) {
	c := s.creation(ctx, key, p)
	fields := []any{}
	if c.reason == "" {
		for _, id := range p.CreateFields {
			m := c.fields[id]
			f := map[string]any{"id": id, "type": m.Schema.Type, "required": m.Required}
			if id == "summary" {
				f["maxBytes"] = 1024
			} else if m.Schema.Type == "string" {
				f["maxBytes"] = 32768
			}
			if len(m.Allowed) > 0 {
				values := []any{}
				for _, v := range m.Allowed {
					schema := m.Schema
					if schema.Type == "array" {
						schema.Type = schema.Items
					}
					x, ok := projectValue(v, schema)
					if ok {
						values = append(values, x)
					}
				}
				f["allowedValues"] = values
			}
			fields = append(fields, f)
		}
	}
	return map[string]any{"projectId": key, "createEnabled": c.reason == "", "createDisabledReason": nullable(c.reason), "issueTypeId": p.IssueTypeID, "createFields": fields, "readFields": p.ReadFields, "bot": person(c.bot), "attachmentContentTypes": ContentTypes, "attachmentMaxBytes": MaxFile}, nil
}
func (s *Server) create(r *http.Request, key string, p *project) (any, error) {
	var req struct {
		Fields map[string]any `json:"fields"`
	}
	if err := decode(r, &req); err != nil {
		return nil, err
	}
	if req.Fields == nil {
		return nil, failure(400, "invalid_body")
	}
	allowed := map[string]bool{}
	for _, f := range p.CreateFields {
		allowed[f] = true
	}
	for f := range p.CreateDefaults {
		allowed[f] = true
	}
	for f, v := range req.Fields {
		if !allowed[f] || !config.CreateField(f) {
			return nil, failure(400, "invalid_body")
		}
		if fixed, ok := p.CreateDefaults[f]; ok && !reflect.DeepEqual(fixed, v) {
			return nil, failure(400, "create_default_conflict")
		}
	}
	c := s.creation(r.Context(), key, p)
	if c.reason != "" {
		return nil, failure(403, "operation_not_available")
	}
	for f, v := range p.CreateDefaults {
		req.Fields[f] = v
	}
	for f, v := range req.Fields {
		if !validateValue(v, c.fields[f]) {
			return nil, failure(400, "invalid_body")
		}
		if f == "summary" {
			x, ok := v.(string)
			if !ok || strings.TrimSpace(x) == "" || len(x) > 1024 {
				return nil, failure(400, "invalid_body")
			}
		}
	}
	for f, m := range c.fields {
		if m.Required && !m.Default && f != "project" && f != "issuetype" && f != "reporter" {
			v, ok := req.Fields[f]
			if !ok || v == nil {
				return nil, failure(400, "invalid_body")
			}
		}
	}
	reporter := map[string]any{}
	if id := str(c.bot["accountId"]); id != "" {
		reporter["accountId"] = id
	} else if name := str(c.bot["name"]); name != "" {
		reporter["name"] = name
	} else {
		reporter["key"] = str(c.bot["key"])
	}
	req.Fields["project"] = map[string]any{"key": key}
	req.Fields["issuetype"] = map[string]any{"id": p.IssueTypeID}
	req.Fields["reporter"] = reporter
	var out Issue
	if err := s.client.JSON(r.Context(), "POST", "issue", nil, req, &out, true); err != nil {
		return nil, err
	}
	if !config.NumericID.MatchString(out.ID) || !issueKey.MatchString(out.Key) {
		return nil, failure(502, "outcome_unknown")
	}
	fields := []string{"project", "issuetype", "reporter"}
	for f := range p.CreateDefaults {
		fields = append(fields, f)
	}
	actual, err := s.client.Issue(r.Context(), out.ID, fields)
	if err != nil || actual.ID != out.ID || actual.Key != out.Key || str(object(actual.Fields["project"])["key"]) != key || str(object(actual.Fields["issuetype"])["id"]) != p.IssueTypeID || !reflect.DeepEqual(person(actual.Fields["reporter"]), person(c.bot)) {
		return nil, failure(502, "outcome_unknown")
	}
	for f, v := range p.CreateDefaults {
		if !fixedMatches(v, actual.Fields[f]) {
			return nil, failure(502, "outcome_unknown")
		}
	}
	return map[string]any{"projectId": key, "issueKey": out.Key, "browseUrl": s.client.base.String() + "/browse/" + out.Key}, nil
}

// Jira adds display metadata to option/user values; compare every fixed input key.
func fixedMatches(expected, actual any) bool {
	switch e := expected.(type) {
	case map[string]any:
		a := object(actual)
		if a == nil {
			return false
		}
		for k, v := range e {
			if !fixedMatches(v, a[k]) {
				return false
			}
		}
		return true
	case []any:
		a, ok := actual.([]any)
		if !ok || len(e) != len(a) {
			return false
		}
		for i, v := range e {
			if !fixedMatches(v, a[i]) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(expected, actual)
	}
}
