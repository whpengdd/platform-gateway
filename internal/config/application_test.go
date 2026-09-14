package config

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const appFixture = `{"tokens":[{"token":"secret","cklogs":"external"}],"cklogs":{"auth":{"type":"basic","username":"u","password":"p"}%s}%s}`

func TestApplicationValidation(t *testing.T) {
	for _, extra := range []string{`,"timeoutMs":0`, `,"timeoutMs":-1`, `,"timeoutMs":1.5`, `,"timeoutMs":9223372031855`, `,"queue":{"waitTimeoutMs":9223372036855}`, `,"queue":{"waitTimeoutMs":0}`, `,"queue":{"size":-1}`, `,"queue":{"maxConcurrency":0}`, `,"queue":{"size":9223372036854775808}`, `,"index":""`, `,"baseUrl":""`, `,"baseUrl":"https://u:p@host"`, `,"queue":null`, `,"timeoutMs":null`, `,"extra":1`} {
		if _, err := Parse([]byte(fmt.Sprintf(appFixture, extra, ""))); err == nil {
			t.Errorf("accepted %s", extra)
		}
	}
	for _, extra := range []string{`,"server":null`, `,"server":{"listenAddr":""}`, `,"server":{"listenAddr":":99999"}`, `,"server":{"allowCidrs":["bad"]}`, `,"server":{"allowCidrs":[null]}`, `,"audit":{"enabled":null}`, `,"audit":{"dir":""}`, `,"audit":{"enabled":"false"}`, `,"jira":{"auth":{"type":"basic"}}`, `,"jira":{"baseUrl":"http://jira.test"}`} {
		if _, err := Parse([]byte(fmt.Sprintf(appFixture, "", extra))); err == nil {
			t.Errorf("accepted %s", extra)
		}
	}
	for _, extra := range []string{fmt.Sprintf(`,"timeoutMs":%d`, MaxTimeoutMS), fmt.Sprintf(`,"queue":{"waitTimeoutMs":%d}`, MaxWaitTimeoutMS), `,"queue":{"size":0}`} {
		if _, err := Parse([]byte(fmt.Sprintf(appFixture, extra, ""))); err != nil {
			t.Fatal(extra, err)
		}
	}
	if _, err := Parse([]byte(strings.Repeat(" ", 1<<20) + fmt.Sprintf(appFixture, "", ""))); err == nil {
		t.Fatal("oversize accepted")
	}
}
func TestAuthenticationAndPaths(t *testing.T) {
	base := `{"tokens":[{"token":"secret","jiraProjects":["CS"]}],"jira":{"baseUrl":%q,"auth":%s,"projects":{"CS":{}}}}`
	for _, a := range []string{`{"type":"bearer","token":"tok","username":""}`, `{"type":"basic","username":"u"}`, `{"type":"basic","username":"u","password":"p","token":""}`, `{"type":"bearer","token":"x\ny"}`} {
		if _, err := Parse([]byte(fmt.Sprintf(base, "https://jira.test/jira", a))); err == nil {
			t.Fatal("mixed/incomplete auth accepted")
		}
	}
	for _, u := range []string{"http://jira.test", "https://jira.test/a/../b", "https://jira.test/%2f", "https://jira.test/?", "https://jira.test/#", "https://u:p@jira.test"} {
		if _, err := Parse([]byte(fmt.Sprintf(base, u, `{"type":"bearer","token":"tok"}`))); err == nil {
			t.Fatal("unsafe URL accepted", u)
		}
	}
	f, err := Parse([]byte(fmt.Sprintf(base, "https://jira.test/jira/", `{"type":"basic","username":"u","password":" $'\" "}`)))
	if err != nil || f.Jira.BaseURL != "https://jira.test/jira" || f.Jira.Auth.Password != " $'\" " {
		t.Fatal("credential/path changed", err)
	}
}

func TestDefaultNumberAndRelativeAudit(t *testing.T) {
	raw := `{"tokens":[{"token":"secret","jiraProjects":["CS"]}],"jira":{"baseUrl":"https://jira.test","auth":{"type":"bearer","token":"upstream"},"projects":{"CS":{"createDefaults":{"customfield_1":1.0}}}},"audit":{"dir":"relative/logs"}}`
	f, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if f.Audit.Dir != "relative/logs" {
		t.Fatal("relative path rewritten")
	}
	if n, ok := f.Jira.Projects["CS"].CreateDefaults["customfield_1"].(json.Number); !ok || n.String() != "1.0" {
		t.Fatal("number changed")
	}
}

func TestRuntimeIntegerQueueBoundary(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	raw := fmt.Sprintf(appFixture, fmt.Sprintf(`,"queue":{"size":%d,"maxConcurrency":%d}`, maxInt, maxInt), "")
	f, err := Parse([]byte(raw))
	if err != nil || f.CKLogs.Queue.Size != maxInt || f.CKLogs.Queue.MaxConcurrency != maxInt {
		t.Fatal("representable runtime integer rejected", err)
	}
}
