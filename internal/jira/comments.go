package jira

import (
	"net/http"
	"net/url"
	"platform-gateway/internal/config"
	"strconv"
	"strings"
)

type Comment struct {
	ID         string `json:"id"`
	Body       string `json:"body"`
	Created    string `json:"created"`
	Updated    string `json:"updated"`
	Author     any    `json:"author"`
	Visibility any    `json:"visibility"`
}

func commentProjection(c Comment) (any, error) {
	if c.Visibility != nil || !config.NumericID.MatchString(c.ID) {
		return nil, failure(404, "resource_not_available")
	}
	if len(c.Body) > 32768 {
		return nil, unavailable()
	}
	return map[string]any{"id": c.ID, "body": c.Body, "createdAt": c.Created, "updatedAt": c.Updated, "author": person(c.Author)}, nil
}
func (s *Server) comments(r *http.Request, key string, p *project, i Issue, child string, write bool) (any, error) {
	path := "issue/" + i.ID + "/comment"
	if write {
		var req struct {
			Body string `json:"body"`
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		if strings.TrimSpace(req.Body) == "" || len(req.Body) > 32768 {
			return nil, failure(400, "invalid_body")
		}
		var out Comment
		if err := s.client.JSON(r.Context(), "POST", path, nil, req, &out, true); err != nil {
			return nil, err
		}
		if !config.NumericID.MatchString(out.ID) || out.Visibility != nil {
			return nil, failure(502, "outcome_unknown")
		}
		return map[string]string{"commentId": out.ID}, nil
	}
	if child != "" {
		var out Comment
		if err := s.client.JSON(r.Context(), "GET", path+"/"+child, nil, nil, &out, false); err != nil {
			if e, ok := err.(*Error); ok && e.Upstream == 404 {
				return nil, failure(404, "resource_not_available")
			}
			return nil, err
		}
		if out.ID != child {
			return nil, failure(404, "resource_not_available")
		}
		return commentProjection(out)
	}
	params, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, failure(400, "invalid_parameter")
	}
	for k, v := range params {
		if len(v) != 1 || k != "limit" && k != "cursor" {
			return nil, failure(400, "invalid_parameter")
		}
	}
	start, limit := 0, 50
	var expiry int64
	if raw, ok := params["cursor"]; ok {
		if len(params) != 1 {
			return nil, failure(400, "invalid_cursor")
		}
		c, err := s.decodeCursor(raw[0], "comments", key, i.ID)
		if err != nil {
			return nil, err
		}
		start = c.Start
		limit = c.Query.Limit
		expiry = c.Expires
	} else if raw, ok := params["limit"]; ok {
		limit, err = strconv.Atoi(raw[0])
		if err != nil || limit < 1 || limit > 100 {
			return nil, failure(400, "invalid_parameter")
		}
	}
	var out struct {
		Comments []Comment `json:"comments"`
		Total    int       `json:"total"`
		Start    int       `json:"startAt"`
	}
	if err := s.client.JSON(r.Context(), "GET", path, url.Values{"startAt": {strconv.Itoa(start)}, "maxResults": {strconv.Itoa(limit)}, "orderBy": {"created"}}, nil, &out, false); err != nil {
		return nil, err
	}
	if out.Start != start || len(out.Comments) > limit {
		return nil, unavailable()
	}
	items := []any{}
	for _, c := range out.Comments {
		if c.Visibility != nil {
			continue
		}
		item, err := commentProjection(c)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	var next any
	if len(out.Comments) > 0 && start+len(out.Comments) < out.Total {
		next = s.nextCursor("comments", key, i.ID, Search{Limit: limit}, start+len(out.Comments), expiry)
	}
	return map[string]any{"items": items, "nextCursor": next}, nil
}
