package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStrictFile(t *testing.T) {
	valid := `{"cklogs":{"auth":{"type":"basic","username":"user","password":"pass"}},"tokens":[{"token":"secret-a","cklogs":"external"},{"token":"secret-b","jiraProjects":["CS"]}],"jira":{"baseUrl":"https://jira.example.test/context","auth":{"type":"bearer","token":"upstream"},"projects":{"CS":{}}}}`
	if _, err := Parse([]byte(valid)); err != nil {
		t.Fatal(err)
	}
	for _, b := range []string{
		`{}`, `null`, `{"cklogs":{"auth":{"type":"basic","username":"user","password":"pass"}},"tokens":[]}`, valid + ` {}`, strings.Replace(valid, `"tokens":`, `"unknown":1,"tokens":`, 1), strings.Replace(valid, `"token":"secret-a"`, `"token":"a","token":"b"`, 1), strings.Replace(valid, "secret-b", "secret-a", 1), strings.Replace(valid, "secret-a", "<token>", 1), strings.Replace(valid, "secret-a", "a b", 1), strings.Replace(valid, `"cklogs":"external"`, `"cklogs":"external","jiraProjects":["CS"]`, 1), strings.Replace(valid, `["CS"]`, `["IT"]`, 1), strings.Replace(valid, `["CS"]`, `["*"]`, 1), strings.Replace(valid, `"CS":{}`, `"CS":{"readFields":["issuelinks"]}`, 1), strings.Replace(valid, `"CS":{}`, `"CS":{"filters":[]}`, 1), strings.Replace(valid, `"CS":{}`, `"CS":{"createDefaults":{"reporter":"x"}}`, 1),
	} {
		if _, err := Parse([]byte(b)); err == nil {
			t.Errorf("accepted invalid configuration")
		} else if strings.Contains(err.Error(), "secret") {
			t.Fatal("secret leaked")
		}
	}
}
func TestLoad(t *testing.T) {

	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("GATEWAY_CONFIG_FILE", path)
	if _, err := Load(); err == nil {
		t.Fatal("missing accepted")
	}
	if err := os.WriteFile(path, []byte(`{"cklogs":{"auth":{"type":"basic","username":"user","password":"pass"}},"tokens":[{"token":"abc","cklogs":"internal"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"GATEWAY_AUTH_FILE", "GATEWAY_TOKEN_INTERNAL", "JIRA_BASE_URL", "CK_LOGS_BASIC_PASS"} {
		t.Run(k, func(t *testing.T) {
			t.Setenv(k, "secret")
			if _, err := Load(); err != nil {
				t.Fatal("stale environment rejected")
			}
		})
	}
}

func TestRejectNullAndBothDomains(t *testing.T) {
	for _, entry := range []string{`"cklogs":null,"jiraProjects":["CS"]`, `"cklogs":"","jiraProjects":["CS"]`, `"cklogs":"internal","jiraProjects":null`} {
		if _, err := Parse([]byte(`{"cklogs":{"auth":{"type":"basic","username":"user","password":"pass"}},"tokens":[{"token":"secret",` + entry + `}],"jira":{"baseUrl":"https://jira.example.test/context","auth":{"type":"bearer","token":"upstream"},"projects":{"CS":{}}}}`)); err == nil {
			t.Fatal("ambiguous domain accepted")
		}
	}
}
func TestDefaultPath(t *testing.T) {

	t.Setenv("GATEWAY_CONFIG_FILE", "")
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)
	if err := os.WriteFile("config.json", []byte(`{"cklogs":{"auth":{"type":"basic","username":"user","password":"pass"}},"tokens":[{"token":"abc","cklogs":"external"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
}

func TestRejectLossyNumericDefaults(t *testing.T) {
	for _, value := range []string{"9007199254740993", "0.10000000000000001"} {
		raw := `{"cklogs":{"auth":{"type":"basic","username":"user","password":"pass"}},"tokens":[{"token":"test","jiraProjects":["CS"]}],"jira":{"baseUrl":"https://jira.example.test/context","auth":{"type":"bearer","token":"upstream"},"projects":{"CS":{"createDefaults":{"customfield_1":` + value + `}}}}}`
		if _, err := Parse([]byte(raw)); err == nil {
			t.Fatal("changed default accepted", value)
		}
	}
}
