package jira

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"platform-gateway/internal/config"
	"platform-gateway/internal/strictjson"
	"strings"
	"unicode"
)

func validFilename(name string) bool {
	return name != "" && name != "." && name != ".." && len(name) <= 255 && !strings.ContainsAny(name, "/\\%") && strings.IndexFunc(name, unicode.IsControl) < 0
}
func allowedType(t string) bool {
	for _, v := range ContentTypes {
		if v == t {
			return true
		}
	}
	return false
}
func (s *Server) upload(r *http.Request, p *project, i Issue) (any, error) {
	if r.ContentLength > 11<<20 {
		return nil, failure(413, "payload_too_large")
	}
	r.Body = http.MaxBytesReader(nil, r.Body, 11<<20)
	reader, err := r.MultipartReader()
	if err != nil {
		return nil, failure(415, "unsupported_media_type")
	}
	part, err := reader.NextPart()
	if err != nil {
		return nil, failure(400, "invalid_body")
	}
	_, params, err := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
	filename := params["filename"]
	if err != nil || part.FormName() != "file" || !validFilename(filename) {
		return nil, failure(400, "invalid_parameter")
	}
	contentType, _, err := mime.ParseMediaType(part.Header.Get("Content-Type"))
	if err != nil || !allowedType(contentType) {
		return nil, failure(415, "unsupported_media_type")
	}
	data, err := io.ReadAll(io.LimitReader(part, MaxFile+1))
	if err != nil || len(data) > MaxFile {
		return nil, failure(413, "payload_too_large")
	}
	if len(data) == 0 {
		return nil, failure(400, "invalid_body")
	}
	detected, _, _ := mime.ParseMediaType(http.DetectContentType(data))
	if detected != contentType {
		return nil, failure(415, "unsupported_media_type")
	}
	if _, err := reader.NextPart(); err != io.EOF {
		return nil, multipartReadError(err)
	}
	// MIME EOF is the closing boundary, not HTTP EOF. Count the remaining body
	// (including epilogue) under MaxBytesReader and the operation read deadline.
	if _, err := io.Copy(io.Discard, r.Body); err != nil {
		return nil, multipartReadError(err)
	}
	p.budget.mu.Lock()
	if p.budget.bytes+int64(len(data)) > 200<<20 {
		p.budget.mu.Unlock()
		return nil, failure(429, "rate_limited")
	}
	p.budget.bytes += int64(len(data))
	p.budget.mu.Unlock()
	var b bytes.Buffer
	writer := multipart.NewWriter(&b)
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": "file", "filename": filename}))
	header.Set("Content-Type", contentType)
	dst, err := writer.CreatePart(header)
	if err != nil {
		return nil, failure(400, "invalid_body")
	}
	dst.Write(data)
	writer.Close()
	raw, err := s.client.request(r.Context(), "POST", s.client.endpoint("issue/"+i.ID+"/attachments", nil), writer.FormDataContentType(), b.Bytes(), true, MaxJSON)
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	if strictjson.CheckNumbers(raw) != nil || json.Unmarshal(raw, &out) != nil || len(out) != 1 || !config.NumericID.MatchString(str(out[0]["id"])) || str(out[0]["filename"]) != filename || out[0]["size"] != float64(len(data)) {
		return nil, failure(502, "outcome_unknown")
	}
	// A follow-up on the stable parent confirms that the returned child belongs to it.
	parent, err := s.client.Issue(r.Context(), i.ID, []string{"project", "attachment"})
	if err != nil || parent.ID != i.ID || !sameProject(i, parent) {
		return nil, failure(502, "outcome_unknown")
	}
	found := false
	for _, v := range attachmentList(parent) {
		if str(object(v)["id"]) == str(out[0]["id"]) {
			found = true
		}
	}
	if !found {
		return nil, failure(502, "outcome_unknown")
	}
	return map[string]any{"attachmentId": out[0]["id"], "filename": filename, "size": len(data), "mimeType": contentType}, nil
}
func sameProject(a, b Issue) bool {
	return str(object(a.Fields["project"])["key"]) == str(object(b.Fields["project"])["key"])
}
func attachmentList(i Issue) []any { a, _ := i.Fields["attachment"].([]any); return a }
func (s *Server) download(ctx context.Context, i Issue, id string) ([]byte, string, string, error) {
	for _, v := range attachmentList(i) {
		m := object(v)
		if str(m["id"]) != id {
			continue
		}
		filename := str(m["filename"])
		contentType := str(m["mimeType"])
		size, ok := m["size"].(float64)
		if !validFilename(filename) || !allowedType(contentType) || !ok || size < 0 || size > MaxFile {
			return nil, "", "", unavailable()
		}
		data, err := s.client.download(ctx, str(m["content"]), id, filename)
		if err != nil {
			return nil, "", "", err
		}
		if float64(len(data)) != size {
			return nil, "", "", unavailable()
		}
		return data, filename, contentType, nil
	}
	return nil, "", "", failure(404, "resource_not_available")
}

func multipartReadError(err error) error {
	var large *http.MaxBytesError
	if errors.As(err, &large) {
		return failure(413, "payload_too_large")
	}
	return failure(400, "invalid_body")
}
