package main

import (
	"testing"
	"time"
)

func TestLoadConfigLogBotProvider(t *testing.T) {
	// 默认 provider 是 kibana，行为不变。
	cfg, err := loadTestConfig(t, `{`+ckFixture+`}}`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ckProvider != "kibana" || cfg.ckUser == "" || cfg.lbUser != "" {
		t.Fatalf("default provider: %+v", cfg)
	}
	// logbot + login 鉴权。
	cfg, err = loadTestConfig(t, `{"tokens":[{"token":"ck-test","cklogs":"internal"}],"cklogs":{"provider":"logbot","logbot":{"baseUrl":"https://logbot.example.test","auth":{"type":"login","username":"unit-svc","password":"unit-pw"},"pollIntervalMs":800,"timeoutMs":45000}}}`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ckProvider != "logbot" || cfg.lbBase != "https://logbot.example.test" || cfg.lbUser != "unit-svc" || cfg.lbPass != "unit-pw" ||
		cfg.lbPollInterval != 800*time.Millisecond || cfg.lbTimeout != 45*time.Second {
		t.Fatalf("logbot login wiring: %+v", cfg)
	}
	// logbot + bearer 备选分支。
	cfg, err = loadTestConfig(t, `{"tokens":[{"token":"ck-test","cklogs":"internal"}],"cklogs":{"provider":"logbot","logbot":{"auth":{"type":"bearer","token":"unit-token"}}}}`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.lbToken != "unit-token" || cfg.lbUser != "" {
		t.Fatalf("logbot bearer wiring: %+v", cfg)
	}
	// logbot 缺凭据允许加载（/ready 报 not_ready，请求 not_available），默认值补齐。
	cfg, err = loadTestConfig(t, `{"tokens":[{"token":"ck-test","cklogs":"internal"}],"cklogs":{"provider":"logbot"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.lbUser != "" || cfg.lbToken != "" || cfg.lbBase != "https://logbot.icoremail.net" ||
		cfg.lbPollInterval != 500*time.Millisecond || cfg.lbTimeout != 90*time.Second {
		t.Fatalf("logbot defaults: %+v", cfg)
	}
	// 非法 provider 拒绝启动。
	if _, err = loadTestConfig(t, `{"tokens":[{"token":"ck-test","cklogs":"internal"}],"cklogs":{"provider":"elastic"}}`); err == nil {
		t.Fatal("invalid provider accepted")
	}
}
