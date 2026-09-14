package config

import (
	"encoding/json"
	"errors"
	"os"
	"platform-gateway/internal/jql"
	"platform-gateway/internal/strictjson"
	"regexp"
	"strings"
	"unicode"
)

type Token struct {
	Token        string   `json:"token"`
	CKLogs       string   `json:"cklogs,omitempty"`
	JiraProjects []string `json:"jiraProjects,omitempty"`
}
type Project struct {
	FilterJQL      string         `json:"filterJql,omitempty"`
	IssueTypeID    string         `json:"issueTypeId,omitempty"`
	CreateDefaults map[string]any `json:"createDefaults,omitempty"`
	CreateFields   []string       `json:"createFields,omitempty"`
	ReadFields     []string       `json:"readFields,omitempty"`
}
type File struct {
	Tokens []Token `json:"tokens"`
	Jira   *struct {
		Projects map[string]Project `json:"projects"`
	} `json:"jira,omitempty"`
}

var ProjectKey = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
var CustomField = regexp.MustCompile(`^customfield_[0-9]+$`)
var NumericID = regexp.MustCompile(`^[1-9][0-9]{0,63}$`)
var DefaultReadFields = []string{"summary", "status", "updated", "reporter", "creator", "assignee"}
var ErrConfig = errors.New("invalid gateway authorization configuration")
var LegacyKeys = []string{"GATEWAY_TOKEN_EXTERNAL", "GATEWAY_TOKEN_INTERNAL", "GATEWAY_AUTH_TOKENS", "GATEWAY_JIRA_TOKENS", "GATEWAY_JIRA_TOKENS_FILE"}

func Load() (*File, error) {
	for _, key := range LegacyKeys {
		if os.Getenv(key) != "" {
			return nil, errors.New("legacy inbound authorization environment conflicts with file configuration")
		}
	}
	path := os.Getenv("GATEWAY_AUTH_FILE")
	if path == "" {
		path = "./config.json"
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("cannot read gateway authorization file")
	}
	return Parse(b)
}
func Parse(b []byte) (*File, error) {
	var f File
	if len(b) > 1<<20 || strictjson.Decode(b, &f) != nil || len(f.Tokens) == 0 {
		return nil, ErrConfig
	}
	var raw struct {
		Tokens []map[string]json.RawMessage `json:"tokens"`
	}
	if json.Unmarshal(b, &raw) != nil {
		return nil, ErrConfig
	}
	for _, t := range raw.Tokens {
		_, ck := t["cklogs"]
		_, jira := t["jiraProjects"]
		if ck == jira || string(t["cklogs"]) == "null" || string(t["jiraProjects"]) == "null" {
			return nil, ErrConfig
		}
	}
	seen := map[string]bool{}
	for _, t := range f.Tokens {
		lower := strings.ToLower(t.Token)
		if t.Token == "" || strings.IndexFunc(t.Token, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 || strings.ContainsAny(t.Token, "<>") || strings.Contains(lower, "replace") || strings.Contains(lower, "changeme") || strings.Contains(lower, "change-me") || strings.Contains(lower, "your-token") || strings.Contains(lower, "placeholder") || seen[t.Token] {
			return nil, ErrConfig
		}
		seen[t.Token] = true
		if t.CKLogs != "" {
			if (t.CKLogs != "external" && t.CKLogs != "internal") || t.JiraProjects != nil {
				return nil, ErrConfig
			}
		} else {
			if len(t.JiraProjects) == 0 || f.Jira == nil {
				return nil, ErrConfig
			}
			projects := map[string]bool{}
			for _, key := range t.JiraProjects {
				if !ProjectKey.MatchString(key) || projects[key] {
					return nil, ErrConfig
				}
				if _, ok := f.Jira.Projects[key]; !ok {
					return nil, ErrConfig
				}
				projects[key] = true
			}
		}
	}
	if f.Jira != nil {
		for key, p := range f.Jira.Projects {
			if jql.Validate(p.FilterJQL) != nil {
				return nil, ErrConfig
			}
			if !ProjectKey.MatchString(key) || (p.IssueTypeID != "" && !NumericID.MatchString(p.IssueTypeID)) {
				return nil, ErrConfig
			}
			if p.ReadFields == nil {
				p.ReadFields = append([]string(nil), DefaultReadFields...)
			}
			seenFields := map[string]bool{}
			for _, field := range p.ReadFields {
				if !ReadField(field) || seenFields[field] {
					return nil, ErrConfig
				}
				seenFields[field] = true
			}
			seenFields = map[string]bool{}
			for _, field := range p.CreateFields {
				if !CreateField(field) || seenFields[field] {
					return nil, ErrConfig
				}
				seenFields[field] = true
			}
			for field := range p.CreateDefaults {
				if !CreateField(field) {
					return nil, ErrConfig
				}
			}
			f.Jira.Projects[key] = p
		}
	}
	return &f, nil
}
func ReadField(s string) bool {
	switch s {
	case "summary", "description", "updated", "status", "reporter", "creator", "assignee", "attachment":
		return true
	}
	return CustomField.MatchString(s)
}
func CreateField(s string) bool {
	switch s {
	case "summary", "description", "labels", "priority", "components", "fixVersions", "versions", "environment", "duedate":
		return true
	}
	return CustomField.MatchString(s)
}
func (f *File) Enabled(service string) bool {
	for _, t := range f.Tokens {
		if service == "cklogs" && t.CKLogs != "" || service == "jira" && len(t.JiraProjects) > 0 {
			return true
		}
	}
	return false
}
