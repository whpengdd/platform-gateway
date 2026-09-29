package jira

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestSearchUsesAccountTimezone(t *testing.T) {
	for _, tc := range []struct{ name, zone, from, to, want string }{
		{"Shanghai", "Asia/Shanghai", "2026-09-29T10:00:45.123+08:00", "2026-09-29T11:00:59.999+08:00", `updated >= "2026-09-29 10:00" AND updated <= "2026-09-29 11:00"`},
		{"EquivalentUTC", "Asia/Shanghai", "2026-09-29T02:00:45.123Z", "2026-09-29T03:00:59.999Z", `updated >= "2026-09-29 10:00" AND updated <= "2026-09-29 11:00"`},
		{"EquivalentNegativeOffset", "Asia/Shanghai", "2026-09-28T22:00:45.123-04:00", "2026-09-28T23:00:59.999-04:00", `updated >= "2026-09-29 10:00" AND updated <= "2026-09-29 11:00"`},
		{"UTC", "UTC", "2026-09-29T10:00:45.123+08:00", "2026-09-29T11:00:59.999+08:00", `updated >= "2026-09-29 02:00" AND updated <= "2026-09-29 03:00"`},
		{"CrossDay", "UTC", "2026-09-29T00:00:00+08:00", "2026-09-29T01:00:00+08:00", `updated >= "2026-09-28 16:00" AND updated <= "2026-09-28 17:00"`},
		{"HalfHour", "Asia/Kolkata", "2026-09-29T02:00:00Z", "2026-09-29T03:00:00Z", `updated >= "2026-09-29 07:30" AND updated <= "2026-09-29 08:30"`},
		{"SeasonalWinter", "America/New_York", "2026-01-01T10:00:00Z", "2026-01-01T11:00:00Z", `updated >= "2026-01-01 05:00" AND updated <= "2026-01-01 06:00"`},
		{"SeasonalSummer", "America/New_York", "2026-07-01T10:00:00Z", "2026-07-01T11:00:00Z", `updated >= "2026-07-01 06:00" AND updated <= "2026-07-01 07:00"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var queries []string
			s, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/myself") {
					json.NewEncoder(w).Encode(map[string]string{"name": "bot", "timeZone": tc.zone})
					return
				}
				if strings.HasSuffix(r.URL.Path, "/search") {
					var req struct {
						JQL string `json:"jql"`
					}
					json.NewDecoder(r.Body).Decode(&req)
					if strings.Contains(req.JQL, "updated >=") {
						queries = append(queries, req.JQL)
					}
				}
				baseline(w, r)
			})
			body, _ := json.Marshal(Search{From: tc.from, To: tc.to})
			w := call(s, "POST", root+"/issues/search", "jira-a", string(body))
			want := `(project = "CS") AND (labels = zammad) AND (` + tc.want + `) ORDER BY updated ASC, key ASC`
			if w.Code != 200 || len(queries) != 1 || queries[0] != want {
				t.Fatalf("status=%d body=%s queries=%v; want %s", w.Code, w.Body.String(), queries, want)
			}
		})
	}
}

func TestSearchRejectsRepeatedLocalMinutes(t *testing.T) {
	for _, tc := range []struct {
		zone, from, to string
		status         int
	}{
		{"America/New_York", "2026-11-01T01:30:00-04:00", "2026-11-01T03:00:00-05:00", 400},
		{"America/New_York", "2026-11-01T01:30:00-05:00", "2026-11-01T03:00:00-05:00", 400},
		{"America/New_York", "2026-11-01T00:00:00-04:00", "2026-11-01T01:30:00-05:00", 400},
		{"Australia/Lord_Howe", "2026-04-05T01:45:00+11:00", "2026-04-05T03:00:00+10:30", 400},
		{"Australia/Lord_Howe", "2026-04-05T01:45:00+10:30", "2026-04-05T03:00:00+10:30", 400},
		{"Europe/Paris", "1911-03-10T23:50:45Z", "1911-03-11T01:00:00Z", 400},
		{"Europe/Paris", "1911-03-10T23:41:24Z", "1911-03-11T01:00:00Z", 400},
		{"Asia/Colombo", "1880-01-01T06:41:00Z", "1880-01-01T07:41:00Z", 200},
		{"Asia/Kolkata", "1854-06-27T06:10:00Z", "1854-06-27T07:10:00Z", 200},
		{"Asia/Tbilisi", "1924-05-02T09:00:00Z", "1924-05-02T10:00:00Z", 200},
		{"America/St_Johns", "1935-03-30T16:00:00Z", "1935-03-30T17:00:00Z", 200},
		{"America/New_York", "2026-11-01T00:30:00-04:00", "2026-11-01T03:00:00-05:00", 200},
		{"America/New_York", "2026-03-08T01:30:00-05:00", "2026-03-08T03:30:00-04:00", 200},
	} {
		t.Run(tc.zone+tc.from+tc.to, func(t *testing.T) {
			business := 0
			s, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/myself") {
					json.NewEncoder(w).Encode(map[string]string{"name": "bot", "timeZone": tc.zone})
					return
				}
				if strings.HasSuffix(r.URL.Path, "/search") {
					var req struct {
						JQL string `json:"jql"`
					}
					json.NewDecoder(r.Body).Decode(&req)
					if strings.Contains(req.JQL, "updated >=") {
						business++
					}
				}
				baseline(w, r)
			})
			body, _ := json.Marshal(Search{From: tc.from, To: tc.to})
			w := call(s, "POST", root+"/issues/search", "jira-a", string(body))
			if w.Code != tc.status {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			if tc.status == 400 && (business != 0 || !strings.Contains(w.Body.String(), "ambiguous_time_range")) {
				t.Fatal(business, w.Body.String())
			}
		})
	}
}
