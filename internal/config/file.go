package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"platform-gateway/internal/jql"
	"platform-gateway/internal/strictjson"
	"regexp"
	"sort"
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
	Jira   *Jira   `json:"jira,omitempty"`
	CKLogs CKLogs  `json:"cklogs,omitempty"`
	Server Server  `json:"server,omitempty"`
	Audit  Audit   `json:"audit,omitempty"`
}

var ProjectKey = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
var CustomField = regexp.MustCompile(`^customfield_[0-9]+$`)
var NumericID = regexp.MustCompile(`^[1-9][0-9]{0,63}$`)
var DefaultReadFields = []string{"summary", "status", "updated", "reporter", "creator", "assignee"}
var ErrConfig = errors.New("invalid gateway application configuration")

func Load() (*File, error) {
	path := os.Getenv("GATEWAY_CONFIG_FILE")
	if path == "" {
		path = "./config.json"
	}

	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read gateway application file %q: %w", path, err)
	}
	f, err := Parse(b)
	if err != nil {
		return nil, fmt.Errorf("configuration file %q: %w", path, err)
	}
	return f, nil
}
func Parse(b []byte) (*File, error) {
	f := defaults()
	if len(b) > 1<<20 {
		return nil, invalid("$", "file exceeds 1 MiB limit")
	}
	if err := validateJSON(b); err != nil {
		return nil, err
	}
	if strictjson.DecodeNumbers(b, &f) != nil {
		return nil, invalid("$", "JSON contains an unsupported value or number")
	}
	if len(f.Tokens) == 0 {
		return nil, invalid("tokens", "must contain at least one token entry")
	}
	var raw struct {
		Tokens []map[string]json.RawMessage `json:"tokens"`
	}
	if json.Unmarshal(b, &raw) != nil {
		return nil, invalid("tokens", "must be an array of token objects")
	}
	for i, t := range raw.Tokens {
		_, ck := t["cklogs"]
		_, jira := t["jiraProjects"]
		if ck == jira {
			return nil, invalid(fmt.Sprintf("tokens[%d]", i), "must set exactly one of cklogs or jiraProjects")
		}
		if ck && string(t["cklogs"]) == `""` {
			return nil, invalid(fmt.Sprintf("tokens[%d].cklogs", i), "must be external or internal")
		}
	}
	seen := map[string]bool{}
	for i, t := range f.Tokens {
		path := fmt.Sprintf("tokens[%d]", i)
		lower := strings.ToLower(t.Token)
		if t.Token == "" || strings.IndexFunc(t.Token, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 || strings.ContainsAny(t.Token, "<>") || strings.Contains(lower, "replace") || strings.Contains(lower, "changeme") || strings.Contains(lower, "change-me") || strings.Contains(lower, "your-token") || strings.Contains(lower, "placeholder") {
			return nil, invalid(path+".token", "must be nonempty, contain no whitespace/control characters, and not be a placeholder")
		}
		if seen[t.Token] {
			return nil, invalid(path+".token", "duplicates an earlier token")
		}
		seen[t.Token] = true
		if t.CKLogs != "" {
			if (t.CKLogs != "external" && t.CKLogs != "internal") || t.JiraProjects != nil {
				return nil, invalid(path+".cklogs", "must be external or internal, without jiraProjects")
			}
		} else {
			if len(t.JiraProjects) == 0 || f.Jira == nil {
				return nil, invalid(path+".jiraProjects", "must be nonempty and requires jira configuration")
			}
			projects := map[string]bool{}
			for j, key := range t.JiraProjects {
				projectPath := fmt.Sprintf("%s.jiraProjects[%d]", path, j)
				if !ProjectKey.MatchString(key) || projects[key] {
					return nil, invalid(projectPath, "must be a unique project key matching [A-Z][A-Z0-9_]{0,63}")
				}
				if _, ok := f.Jira.Projects[key]; !ok {
					return nil, invalid(projectPath, "references a project missing from jira.projects")
				}
				projects[key] = true
			}
		}
	}
	if f.Jira != nil {
		keys := make([]string, 0, len(f.Jira.Projects))
		for key := range f.Jira.Projects {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			p := f.Jira.Projects[key]
			if !ProjectKey.MatchString(key) {
				return nil, invalid("jira.projects", "project keys must match [A-Z][A-Z0-9_]{0,63}")
			}
			path := "jira.projects." + key
			if jql.Validate(p.FilterJQL) != nil {
				return nil, invalid(path+".filterJql", "invalid JQL filter structure; ORDER BY is not allowed")
			}
			if p.IssueTypeID != "" && !NumericID.MatchString(p.IssueTypeID) {
				return nil, invalid(path+".issueTypeId", "must be a positive numeric ID of at most 64 digits")
			}
			if p.ReadFields == nil {
				p.ReadFields = append([]string(nil), DefaultReadFields...)
			}
			seenFields := map[string]bool{}
			for i, field := range p.ReadFields {
				if !ReadField(field) || seenFields[field] {
					return nil, invalid(fmt.Sprintf("%s.readFields[%d]", path, i), "unsupported or duplicate read field")
				}
				seenFields[field] = true
			}
			seenFields = map[string]bool{}
			for i, field := range p.CreateFields {
				if !CreateField(field) || seenFields[field] {
					return nil, invalid(fmt.Sprintf("%s.createFields[%d]", path, i), "unsupported or duplicate create field")
				}
				seenFields[field] = true
			}
			defaultsJSON, err := json.Marshal(p.CreateDefaults)
			if err != nil || strictjson.CheckNumbers(defaultsJSON) != nil {
				return nil, invalid(path+".createDefaults", "contains a number that cannot round-trip without precision loss")
			}
			for field := range p.CreateDefaults {
				if !CreateField(field) {
					return nil, invalid(path+".createDefaults", "contains an unsupported or reserved create field")
				}
			}
			f.Jira.Projects[key] = p
		}
	}
	if err := f.validateApplication(b); err != nil {
		return nil, err
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
