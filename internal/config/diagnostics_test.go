package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestActionableConfigurationErrors(t *testing.T) {
	base := `{"tokens":[{"token":"inbound-credential","jiraProjects":["CS"]}],"jira":{"baseUrl":"https://jira.test","auth":{"type":"basic","username":"bot-credential","password":"password-credential"},"projects":{"CS":{}}}}`
	cases := []struct{ old, new, path, reason string }{
		{`"https://jira.test"`, `"ftp://jira.test"`, "jira.baseUrl", "HTTP or HTTPS"},
		{`"username":"bot-credential",`, ``, "jira.auth.username", "required"},
		{`"password":"password-credential"`, `"password":""`, "jira.auth.password", "required"},
		{`"password":"password-credential"`, `"password":"password-credential\n"`, "jira.auth.password", "CR or LF"},
		{`"type":"basic"`, `"type":"unsupported-credential"`, "jira.auth.type", "must be"},
		{`"type":"basic"`, `"type":"basic","token":"upstream-credential"`, "jira.auth", "must not include token"},
		{`"inbound-credential"`, `""`, "tokens[0].token", "nonempty"},
		{`"inbound-credential"`, `"inbound-credential "`, "tokens[0].token", "whitespace"},
		{`"inbound-credential"`, `"<inbound-credential>"`, "tokens[0].token", "placeholder"},
		{`"jiraProjects":["CS"]`, `"jiraProjects":["CS"],"cklogs":"external"`, "tokens[0]", "exactly one"},
		{`["CS"]`, `["IT"]`, "tokens[0].jiraProjects[0]", "missing"},
		{`["CS"]`, `["CS","CS"]`, "tokens[0].jiraProjects[1]", "unique"},
		{`"CS":{}`, `"CS":{"issueTypeId":"bad-credential"}`, "jira.projects.CS.issueTypeId", "numeric ID"},
		{`"CS":{}`, `"CS":{"filterJql":"ORDER BY password-credential"}`, "jira.projects.CS.filterJql", "JQL"},
		{`"CS":{}`, `"CS":{"readFields":["comment"]}`, "jira.projects.CS.readFields[0]", "unsupported"},
		{`"CS":{}`, `"CS":{"createFields":["reporter"]}`, "jira.projects.CS.createFields[0]", "unsupported"},
		{`"CS":{}`, `"CS":{"createDefaults":{"reporter":"password-credential"}}`, "jira.projects.CS.createDefaults", "reserved"},
		{`"CS":{}`, `"CS":{"createDefaults":{"customfield_1":9007199254740993}}`, "jira.projects.CS.createDefaults", "precision"},
		{`"CS":{}`, `"CS":{"readFields":null}`, "jira.projects.CS.readFields", "null"},
		{`"CS":{}`, `"CS":{"readFields":[null]}`, "jira.projects.CS.readFields[0]", "null"},
		{`"CS":{}`, `"CS":{"readFields":false}`, "jira.projects.CS.readFields", "array"},
		{`"CS":{}`, `"CS":{"password-credential":1}`, "jira.projects.CS", "unknown field"},
		{`"CS":{}`, `"CS":{"ReadFields":[]}`, "jira.projects.CS", "case-sensitive"},
		{`"password":"password-credential"`, `"password":12`, "jira.auth.password", "string"},
		{`"password":"password-credential"`, `"password":"password-credential","password":"other-credential"`, "jira.auth.password", "duplicate"},
		{`"password":"password-credential"`, `"password":{"password-credential":1}`, "jira.auth.password", "string"},
	}
	for _, tc := range cases {
		t.Run(tc.path+"/"+tc.reason, func(t *testing.T) {
			_, err := Parse([]byte(strings.Replace(base, tc.old, tc.new, 1)))
			if !errors.Is(err, ErrConfig) || !strings.Contains(err.Error(), tc.path) || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("unexpected error: %v", err)
			}
			if strings.Contains(err.Error(), "credential") {
				t.Fatalf("credential leaked: %v", err)
			}
		})
	}
	for _, tc := range []struct{ extra, path, reason string }{
		{`,"timeoutMs":0`, "cklogs.timeoutMs", "between"},
		{`,"timeoutMs":1.5`, "cklogs.timeoutMs", "integer"},
		{`,"queue":{"size":9223372036854775808}`, "cklogs.queue.size", "integer"},
		{`,"queue":{"size":-1}`, "cklogs.queue.size", "at least 0"},
		{`,"queue":{"maxConcurrency":0}`, "cklogs.queue.maxConcurrency", "at least 1"},
		{`,"queue":{"waitTimeoutMs":0}`, "cklogs.queue.waitTimeoutMs", "between"},
	} {
		_, err := Parse([]byte(fmt.Sprintf(appFixture, tc.extra, "")))
		if err == nil || !strings.Contains(err.Error(), tc.path) || !strings.Contains(err.Error(), tc.reason) {
			t.Fatalf("unexpected error: %v", err)
		}
	}
}

func TestJSONDiagnosticsDoNotEchoInput(t *testing.T) {
	for _, raw := range []string{
		`{"password-credential":1}`, `{"tokens":[{"token":"password-credential"`,
		`{"tokens":[],"password-credential":1,"password-credential":2}`, `null`, `{} {}`, `{]`,
		"{\xff}", strings.Repeat("[", 66) + strings.Repeat("]", 66),
	} {
		_, err := Parse([]byte(raw))
		if !errors.Is(err, ErrConfig) || strings.Contains(err.Error(), "credential") || err.Error() == ErrConfig.Error() {
			t.Fatalf("unsafe or unhelpful diagnostic: %v", err)
		}
	}
}

func TestHTTPJiraWarning(t *testing.T) {
	for _, scheme := range []string{"http", "https"} {
		raw := fmt.Sprintf(`{"tokens":[{"token":"inbound-credential","jiraProjects":["CS"]}],"jira":{"baseUrl":"%s://jira.test/private-credential","auth":{"type":"bearer","token":"upstream-credential"},"projects":{"CS":{}}}}`, scheme)
		f, err := Parse([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		warnings := f.Warnings()
		if scheme == "http" {
			if len(warnings) != 1 || !strings.Contains(warnings[0], "jira.baseUrl uses HTTP") || strings.Contains(warnings[0], "-credential") {
				t.Fatalf("unexpected warnings: %v", warnings)
			}
		} else if len(warnings) != 0 {
			t.Fatal(warnings)
		}
	}
	f, err := Parse([]byte(fmt.Sprintf(appFixture, "", `,"jira":{"baseUrl":"http://jira.test"}`)))
	if err != nil || len(f.Warnings()) != 0 {
		t.Fatalf("disabled Jira warning: %v", err)
	}
}

func TestLoadErrorIncludesFileAndField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte(`{"tokens":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GATEWAY_CONFIG_FILE", path)
	_, err := Load()
	if !errors.Is(err, ErrConfig) || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "tokens") {
		t.Fatal(err)
	}
}
