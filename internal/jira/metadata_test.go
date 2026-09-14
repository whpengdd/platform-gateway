package jira

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestMetadataShapes(t *testing.T) {
	c := testClient(t, baseline)
	m, err := c.metadata(context.Background(), "CS", "10")
	if err != nil || !m["summary"].Required {
		t.Fatal(err)
	}
	c = testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/jira/rest/api/2/issue/createmeta" {
			w.WriteHeader(404)
			return
		}
		if r.URL.Path != "/jira/rest/api/2/issue/createmeta/CS/issuetypes/10" {
			t.Error("scope")
		}
		if r.URL.Query().Get("startAt") == "0" {
			io.WriteString(w, `{"startAt":0,"total":2,"values":[{"fieldId":"summary","required":true,"schema":{"type":"string"}}]}`)
		} else {
			io.WriteString(w, `{"startAt":1,"isLast":true,"values":[{"fieldId":"reporter","schema":{"type":"user"}}]}`)
		}
	})
	m, err = c.metadata(context.Background(), "CS", "10")
	if err != nil || len(m) != 2 {
		t.Fatal(err, m)
	}
	c = testClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"projects":[{"key":"IT","issuetypes":[{"id":"10","fields":{"summary":{}}}]}]}`)
	})
	if _, err := c.metadata(context.Background(), "CS", "10"); err == nil {
		t.Fatal("foreign metadata")
	}
}
func TestUnknownCustomTypesFailClosed(t *testing.T) {
	s, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/field") {
			io.WriteString(w, `[{"id":"customfield_1","schema":{"type":"string","custom":"plugin:issue-link"}}]`)
			return
		}
		baseline(w, r)
	})
	w := call(s, "GET", root+"/issues/CS-1", "jira-a", "")
	if w.Code != 503 {
		t.Fatal(w.Code, w.Body.String())
	}
	s, _ = fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/search") {
			i := mockIssue()
			object(i["fields"])["customfield_1"] = map[string]any{"secret": "nested"}
			json.NewEncoder(w).Encode(map[string]any{"issues": []any{i}, "total": 1})
			return
		}
		baseline(w, r)
	})
	w = call(s, "GET", root+"/issues/CS-1", "jira-a", "")
	if w.Code != 503 || strings.Contains(w.Body.String(), "nested") {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestCapabilitiesPruned(t *testing.T) {
	s, _ := fixture(t, baseline)
	w := call(s, "GET", root+"/capabilities", "jira-a", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"createEnabled":true`) || strings.Contains(w.Body.String(), "SECRET_URL") || strings.Contains(w.Body.String(), "filterJql") {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestUnknownCreateOperationsDisable(t *testing.T) {
	s, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/createmeta") {
			m := mockMetadata().(map[string]any)
			projects := m["projects"].([]any)
			types := object(projects[0])["issuetypes"].([]any)
			fields := object(object(types[0])["fields"])
			delete(object(fields["summary"]), "operations")
			json.NewEncoder(w).Encode(m)
			return
		}
		baseline(w, r)
	})
	w := call(s, "GET", root+"/capabilities", "jira-a", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"createEnabled":false`) {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestEnumAndRequiredValidation(t *testing.T) {
	f := Field{Required: true, Schema: Schema{Type: "option"}, Allowed: []any{map[string]any{"id": "1", "value": "allowed", "self": "hidden"}}}
	if !validateValue(map[string]any{"id": "1"}, f) || validateValue(map[string]any{"id": "1", "value": "wrong"}, f) {
		t.Fatal("enum validation")
	}
	if validateValue(" ", Field{Required: true, Schema: Schema{Type: "string"}}) {
		t.Fatal("empty required string")
	}
}
