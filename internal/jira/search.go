package jira

import (
	"encoding/json"
	"net/http"
	"platform-gateway/internal/jql"
	"platform-gateway/internal/strictjson"
	"strings"
	"time"
)

type Search struct {
	From   string   `json:"updatedFrom,omitempty"`
	To     string   `json:"updatedTo,omitempty"`
	Keys   []string `json:"issueKeys,omitempty"`
	Text   string   `json:"text,omitempty"`
	Limit  int      `json:"limit,omitempty"`
	Cursor string   `json:"cursor,omitempty"`
}

func (q *Search) validate() (time.Time, time.Time, error) {
	var from, to time.Time
	if q.Limit == 0 {
		q.Limit = 50
	}
	if q.Limit < 1 || q.Limit > 100 || len(q.Keys) > 100 || len(q.Text) > 256 {
		return from, to, failure(400, "invalid_parameter")
	}
	if q.From != "" || q.To != "" {
		var e1, e2 error
		from, e1 = time.Parse(time.RFC3339, q.From)
		to, e2 = time.Parse(time.RFC3339, q.To)
		if e1 != nil || e2 != nil || !to.After(from) || to.Sub(from) > 30*24*time.Hour {
			return from, to, failure(400, "invalid_parameter")
		}
	}
	for _, key := range q.Keys {
		if !issueKey.MatchString(key) {
			return from, to, failure(400, "invalid_parameter")
		}
	}
	return from, to, nil
}

// query uses the already validated absolute bounds and one account snapshot.
func (q *Search) query(from, to time.Time, loc *time.Location) (string, error) {
	clauses := []string{}
	if q.From != "" {
		if loc == nil {
			return "", failure(503, "timezone_unavailable")
		}
		from, to = from.In(loc), to.In(loc)
		if repeatedLocalMinute(from) || repeatedLocalMinute(to) {
			return "", failure(400, "ambiguous_time_range")
		}
		clauses = append(clauses, "updated >= "+jql.Literal(from.Format(jqlTimeLayout)), "updated <= "+jql.Literal(to.Format(jqlTimeLayout)))
	}
	if len(q.Keys) > 0 {
		keys := []string{}
		for _, key := range q.Keys {
			keys = append(keys, jql.Literal(key))
		}
		clauses = append(clauses, "key IN ("+strings.Join(keys, ",")+")")
	}
	if q.Text != "" {
		clauses = append(clauses, "text ~ "+jql.Literal(q.Text))
	}
	return strings.Join(clauses, " AND "), nil
}
func (s *Server) search(r *http.Request, key string, p *project) (any, error) {
	var raw map[string]json.RawMessage
	if err := decode(r, &raw); err != nil {
		return nil, err
	}
	b, _ := json.Marshal(raw)
	var q Search
	if strictjson.Decode(b, &q) != nil {
		return nil, failure(400, "invalid_body")
	}
	if _, ok := raw["cursor"]; ok && (len(raw) != 1 || q.Cursor == "") {
		return nil, failure(400, "invalid_cursor")
	}
	if _, ok := raw["limit"]; ok && q.Limit == 0 {
		return nil, failure(400, "invalid_parameter")
	}
	for _, v := range raw {
		if string(v) == "null" {
			return nil, failure(400, "invalid_body")
		}
	}
	_, fromPresent := raw["updatedFrom"]
	_, toPresent := raw["updatedTo"]
	if fromPresent != toPresent || fromPresent && (q.From == "" || q.To == "") {
		return nil, failure(400, "invalid_parameter")
	}
	start := 0
	var expiry int64
	var cursorGeneration uint64
	continuation := q.Cursor != ""
	if q.Cursor != "" {
		if q.From != "" || q.To != "" || q.Keys != nil || q.Text != "" || q.Limit != 0 {
			return nil, failure(400, "invalid_cursor")
		}
		c, err := s.decodeCursor(q.Cursor, "search", key, "")
		if err != nil {
			return nil, err
		}
		q = c.Query
		start = c.Start
		expiry = c.Expires
		cursorGeneration = c.AccountGeneration
	}
	from, to, err := q.validate()
	if err != nil {
		return nil, err
	}
	generation := s.client.accountGeneration()
	if continuation && generation != cursorGeneration {
		return nil, failure(410, "cursor_expired")
	}
	var loc *time.Location
	if q.From != "" {
		snapshot, err := s.client.accountContext(r.Context(), true)
		// A timezone error carries the last observed generation even when its
		// identity has expired. Transport failures carry no usable context.
		accountErr, isAccountErr := err.(*Error)
		if err != nil && (!isAccountErr || accountErr.Code != "timezone_unavailable") {
			return nil, err
		}
		generation, loc = snapshot.Generation, snapshot.Location
		if continuation && generation != cursorGeneration {
			return nil, failure(410, "cursor_expired")
		}
		if err != nil {
			return nil, err
		}
	}
	query, err := q.query(from, to, loc)
	if err != nil {
		return nil, err
	}
	schemas, err := s.client.readSchemas(r.Context(), p.Project)
	if err != nil {
		return nil, err
	}
	result, err := s.client.Search(r.Context(), jql.Scope(key, p.FilterJQL, query)+" ORDER BY updated ASC, key ASC", append([]string{"project"}, p.ReadFields...), start, q.Limit)
	if err != nil {
		return nil, err
	}
	if result.Start != start || len(result.Issues) > q.Limit {
		return nil, unavailable()
	}
	items := []any{}
	for _, i := range result.Issues {
		item, err := projectIssue(i, key, p.Project, schemas)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	var next any
	if len(result.Issues) > 0 && start+len(result.Issues) < result.Total {
		next = s.nextCursor("search", key, "", q, start+len(result.Issues), expiry, generation)
	}
	return map[string]any{"items": items, "nextCursor": next}, nil
}
