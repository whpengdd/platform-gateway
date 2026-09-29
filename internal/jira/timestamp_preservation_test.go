package jira

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCreateAndReadPreserveTimestampStrings(t *testing.T) {
	for _, timestamp := range []string{"2026-09-29T10:00:45.123+08:00", "2026-09-29T10:00:45.123+0800", "2026-09-29T02:00:45.123Z", "2026-09-29"} {
		t.Run(timestamp, func(t *testing.T) {
			var submitted map[string]any
			var writes atomic.Int32
			s, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/jira/rest/api/2/myself":
					json.NewEncoder(w).Encode(map[string]string{"name": "bot", "displayName": "Bot", "timeZone": "America/New_York"})
					return
				case "/jira/rest/api/2/issue/createmeta":
					metadata, _ := json.Marshal(mockMetadata())
					body := strings.ReplaceAll(string(metadata), `"customfield_1":{"operations":["set"],"schema":{"type":"string"}}`, `"customfield_1":{"operations":["set"],"schema":{"type":"string","custom":"com.atlassian.jira.plugin.system.customfieldtypes:datetime"}}`)
					w.Write([]byte(body))
					return
				case "/jira/rest/api/2/issue":
					writes.Add(1)
					var body struct {
						Fields map[string]any `json:"fields"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					submitted = body.Fields
				case "/jira/rest/api/2/search":
					i := mockIssue()
					fields := i["fields"].(map[string]any)
					fields["updated"], fields["customfield_1"] = timestamp, timestamp
					json.NewEncoder(w).Encode(map[string]any{"startAt": 0, "total": 1, "issues": []any{i}})
					return
				case "/jira/rest/api/2/issue/1/comment/11":
					json.NewEncoder(w).Encode(map[string]string{"id": "11", "body": "source " + timestamp, "created": timestamp, "updated": timestamp})
					return
				}
				baseline(w, r)
			})
			p := s.projects["CS"]
			p.ReadFields = append(p.ReadFields, "updated")
			body, _ := json.Marshal(map[string]any{"fields": map[string]string{"summary": "timestamp preservation", "description": "Source: " + timestamp, "customfield_1": timestamp}})
			w := call(s, "POST", root+"/issues", "jira-a", string(body))
			if w.Code != 201 || submitted["customfield_1"] != timestamp || submitted["description"] != "Source: "+timestamp {
				t.Fatal(w.Code, w.Body.String(), submitted)
			}
			for _, field := range []string{"created", "updated"} {
				if _, ok := submitted[field]; ok {
					t.Fatal("gateway synthesized timestamp", field)
				}
				body, _ = json.Marshal(map[string]any{"fields": map[string]string{"summary": "reserved", field: timestamp}})
				w = call(s, "POST", root+"/issues", "jira-a", string(body))
				if w.Code != 400 || writes.Load() != 1 {
					t.Fatal(w.Code, w.Body.String())
				}
			}
			w = call(s, "GET", root+"/issues/CS-1", "jira-a", "")
			var issue struct {
				Fields map[string]any `json:"fields"`
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &issue) != nil || issue.Fields["updated"] != timestamp || issue.Fields["customfield_1"] != timestamp {
				t.Fatal(w.Code, w.Body.String())
			}
			w = call(s, "GET", root+"/issues/CS-1/comments/11", "jira-a", "")
			var comment map[string]any
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &comment) != nil || comment["createdAt"] != timestamp || comment["updatedAt"] != timestamp || comment["body"] != "source "+timestamp {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
}

func TestCreationCapabilitiesAndSearchShareAccountSnapshot(t *testing.T) {
	var profiles atomic.Int32
	s, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/myself") {
			profiles.Add(1)
		}
		baseline(w, r)
	})
	for _, request := range []struct {
		method, path, body string
		status             int
	}{
		{"GET", root + "/capabilities", "", 200},
		{"POST", root + "/issues", `{"fields":{"summary":"shared identity"}}`, 201},
		{"POST", root + "/issues/search", timedSearchBody, 200},
		{"GET", root + "/capabilities", "", 200},
	} {
		w := call(s, request.method, request.path, "jira-a", request.body)
		if w.Code != request.status {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if profiles.Load() != 1 {
		t.Fatalf("myself reads=%d, want1", profiles.Load())
	}
}
