package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAttachmentOwnershipAndUpload(t *testing.T) {
	var downloads, writes atomic.Int32
	var s *Server
	s, _ = fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/secure/attachment/") {
			downloads.Add(1)
			io.WriteString(w, "hello")
			return
		}
		if strings.HasSuffix(r.URL.Path, "/attachments") {
			writes.Add(1)
			if r.Header.Get("X-Atlassian-Token") != "no-check" {
				t.Error("upload header")
			}
			mr, err := r.MultipartReader()
			if err != nil {
				t.Error(err)
			} else {
				part, err := mr.NextPart()
				if err != nil {
					t.Error(err)
				} else {
					b, _ := io.ReadAll(part)
					if string(b) != "hello" || part.FileName() != "a.txt" {
						t.Error("wrong upload")
					}
				}
			}
			io.WriteString(w, `[{"id":"30","filename":"a.txt","size":5,"mimeType":"text/plain"}]`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/search") || strings.HasSuffix(r.URL.Path, "/issue/1") {
			i := mockIssue()
			object(i["fields"])["attachment"] = []any{map[string]any{"id": "30", "filename": "a.txt", "size": 5, "mimeType": "text/plain", "content": s.client.base.String() + "/secure/attachment/30/a.txt"}}
			if strings.HasSuffix(r.URL.Path, "/search") {
				json.NewEncoder(w).Encode(map[string]any{"issues": []any{i}, "total": 1})
			} else {
				json.NewEncoder(w).Encode(i)
			}
			return
		}
		baseline(w, r)
	})
	w := call(s, "GET", root+"/issues/CS-1/attachments/99/content", "jira-a", "")
	if w.Code != 404 || downloads.Load() != 0 {
		t.Fatal(w.Code)
	}
	w = call(s, "GET", root+"/issues/CS-1/attachments/30/content", "jira-a", "")
	if w.Code != 200 || w.Body.String() != "hello" || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment") || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal(w.Code, w.Body.String())
	}
	upload := func(filename, body string, parts int) *httptest.ResponseRecorder {
		var b bytes.Buffer
		m := multipart.NewWriter(&b)
		for n := 0; n < parts; n++ {
			h := textproto.MIMEHeader{}
			h.Set("Content-Disposition", `form-data; name="file"; filename="`+filename+`"`)
			h.Set("Content-Type", "text/plain")
			part, _ := m.CreatePart(h)
			io.WriteString(part, body)
		}
		m.Close()
		r := httptest.NewRequest("POST", root+"/issues/CS-1/attachments", &b)
		r.Header.Set("Authorization", "Bearer jira-a")
		r.Header.Set("Content-Type", m.FormDataContentType())
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	w = upload("a.txt", "hello", 1)
	if w.Code != 201 || writes.Load() != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, tt := range []struct {
		name, body    string
		parts, status int
	}{{"../a.txt", "hello", 1, 400}, {"a.txt", "hello", 2, 400}, {"a.txt", strings.Repeat("x", MaxFile+1), 1, 413}, {"a.txt", "", 1, 400}} {
		w = upload(tt.name, tt.body, tt.parts)
		if w.Code != tt.status {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if writes.Load() != 1 {
		t.Fatal("invalid upload dispatched")
	}
}
func TestDownloadLargeAndRedirect(t *testing.T) {
	for _, mode := range []string{"large", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if mode == "redirect" {
					w.Header().Set("Location", "https://outside.invalid")
					w.WriteHeader(302)
				} else {
					io.WriteString(w, strings.Repeat("x", MaxFile+1))
				}
			})
			if _, err := c.download(context.Background(), c.base.String()+"/secure/attachment/1/a.txt", "1", "a.txt"); err == nil || calls.Load() != 1 {
				t.Fatal(err, calls.Load())
			}
		})
	}
}
