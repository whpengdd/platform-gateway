package config

import (
	"errors"
	"strings"
	"testing"
)

const logbotFixture = `{"tokens":[{"token":"inbound-credential","cklogs":"external"}],"cklogs":{"auth":{"type":"basic","username":"u-credential","password":"p-credential"}%s}}`

func TestLogBotProviderConfig(t *testing.T) {
	// 默认 kibana：不显式配置 provider 时行为不变。
	f, err := Parse([]byte(`{"tokens":[{"token":"inbound-credential","cklogs":"external"}],"cklogs":{"auth":{"type":"basic","username":"u","password":"p"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if f.CKLogs.Provider != "kibana" || f.CKLogs.LogBot != nil {
		t.Fatalf("default provider changed: %+v", f.CKLogs)
	}

	// 非法 provider 启动失败，错误指出字段且不 echo 取值。
	for _, extra := range []string{`,"provider":"elastic"`, `,"provider":"KIBANA "`} {
		_, err := Parse([]byte(`{"tokens":[{"token":"inbound-credential","cklogs":"external"}],"cklogs":{"auth":{"type":"basic","username":"u","password":"p"}` + extra + `}}`))
		if !errors.Is(err, ErrConfig) || !strings.Contains(err.Error(), "cklogs.provider") || !strings.Contains(err.Error(), "kibana or logbot") {
			t.Fatalf("unexpected error for %s: %v", extra, err)
		}
	}

	// logbot 缺凭据：允许启动（/ready 报 not_ready），不强制 logbot.auth。
	f, err = Parse([]byte(`{"tokens":[{"token":"inbound-credential","cklogs":"external"}],"cklogs":{"provider":"logbot"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if f.CKLogs.Provider != "logbot" || f.CKLogs.LogBot == nil {
		t.Fatalf("logbot section missing: %+v", f.CKLogs)
	}
	if f.CKLogs.LogBot.BaseURL != "https://logbot.icoremail.net" || f.CKLogs.LogBot.PollIntervalMS != 500 || f.CKLogs.LogBot.TimeoutMS != 90000 {
		t.Fatalf("logbot defaults: %+v", f.CKLogs.LogBot)
	}

	// login 鉴权通过。
	f, err = Parse([]byte(`{"tokens":[{"token":"inbound-credential","cklogs":"external"}],"cklogs":{"provider":"logbot","logbot":{"auth":{"type":"login","username":"svc-credential","password":"pw-credential"}}}}`))
	if err != nil || f.CKLogs.LogBot.Auth.Username == "" {
		t.Fatalf("login auth: %v", err)
	}

	// bearer 备选分支通过。
	if _, err = Parse([]byte(`{"tokens":[{"token":"inbound-credential","cklogs":"external"}],"cklogs":{"provider":"logbot","logbot":{"auth":{"type":"bearer","token":"svc-token-credential"}}}}`)); err != nil {
		t.Fatalf("bearer auth: %v", err)
	}

	cases := []struct{ extra, path, reason string }{
		{`,"logbot":{"auth":{"type":"basic","username":"a-credential","password":"b-credential"}}`, "cklogs.logbot.auth.type", "login or bearer"},
		{`,"logbot":{"auth":{"type":"login","username":"a-credential"}}`, "cklogs.logbot.auth.password", "required"},
		{`,"logbot":{"auth":{"type":"login","password":"b-credential"}}`, "cklogs.logbot.auth.username", "required"},
		{`,"logbot":{"auth":{"type":"bearer"}}`, "cklogs.logbot.auth.token", "required"},
		{`,"logbot":{"auth":{"type":"bearer","token":"changeme"}}`, "cklogs.logbot.auth.token", "placeholder"},
		{`,"logbot":{"auth":{"type":"bearer","token":" svc-credential"}}`, "cklogs.logbot.auth.token", "whitespace"},
		{`,"logbot":{"auth":{"type":"bearer","token":"svc-credential","username":"a-credential"}}`, "cklogs.logbot.auth", "must not include"},
		{`,"logbot":{"bogus":1}`, "cklogs.logbot", "unknown field"},
		{`,"logbot":{"baseUrl":"ftp://logbot.test"}`, "cklogs.logbot.baseUrl", "HTTP or HTTPS"},
		{`,"logbot":{"pollIntervalMs":50}`, "cklogs.logbot.pollIntervalMs", "between"},
		{`,"logbot":{"timeoutMs":0.5}`, "cklogs.logbot.timeoutMs", "integer"},
	}
	for _, tc := range cases {
		t.Run(tc.path+"/"+tc.reason, func(t *testing.T) {
			raw := `{"tokens":[{"token":"inbound-credential","cklogs":"external"}],"cklogs":{"provider":"logbot"` + tc.extra + `}}`
			_, err := Parse([]byte(raw))
			if !errors.Is(err, ErrConfig) || !strings.Contains(err.Error(), tc.path) || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("unexpected error: %v", err)
			}
			if strings.Contains(err.Error(), "-credential") {
				t.Fatalf("credential leaked: %v", err)
			}
		})
	}
}
