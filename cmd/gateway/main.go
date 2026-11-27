package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"platform-gateway/internal/auditlog"
	"platform-gateway/internal/auth"
	"platform-gateway/internal/cklogs"
	fileconfig "platform-gateway/internal/config"
	"platform-gateway/internal/httpapi"
	"platform-gateway/internal/jira"
	"platform-gateway/internal/queue"
)

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}
	for _, warning := range cfg.authFile.Warnings() {
		log.Print(warning)
	}
	// All routes share the same upstream log budget, including internal analysis calls.
	gate := queue.New("cklogs", cfg.maxConc, cfg.queueSize, cfg.queueWait)
	var ckService *cklogs.Service
	var ckReady func() bool
	if cfg.ckProvider == "logbot" {
		log.Printf("LogBot queue: max_concurrency=%d queue_size=%d wait_timeout=%s", cfg.maxConc, cfg.queueSize, cfg.queueWait)
		lb := &cklogs.LogBotClient{
			BaseURL:      cfg.lbBase,
			Username:     cfg.lbUser,
			Password:     cfg.lbPass,
			Token:        cfg.lbToken,
			PollInterval: cfg.lbPollInterval,
			Timeout:      cfg.lbTimeout,
			// LogBot 未认证时会 302 到 SSO 登录页，必须禁止跟随重定向。
			HTTPClient: newNoRedirectHTTPClient(cfg.lbTimeout),
		}
		ckService = cklogs.NewLogBotServiceWithRequestTimeout(lb, cfg.ckTimeout)
		ckReady = lb.Configured
		log.Printf("cklogs provider: logbot (base_url configured=%t, auth configured=%t)", cfg.lbBase != "", lb.Configured())
	} else {
		log.Printf("Kibana queue: max_concurrency=%d queue_size=%d wait_timeout=%s", cfg.maxConc, cfg.queueSize, cfg.queueWait)
		client := &cklogs.Client{
			BaseURL:    cfg.ckBase,
			User:       cfg.ckUser,
			Pass:       cfg.ckPass,
			Index:      cfg.ckIndex,
			Timeout:    cfg.ckTimeout,
			HTTPClient: newHTTPClient(cfg.ckTimeout),
		}
		ckService = cklogs.NewService(client)
		user, pass := cfg.ckUser, cfg.ckPass
		ckReady = func() bool { return user != "" && pass != "" }
	}
	audit, err := auditlog.NewWriter(cfg.logDir)
	if err != nil {
		log.Fatalf("audit log: %v", err)
	}
	if audit != nil {
		defer audit.Close()
		log.Printf("audit jsonl dir %s", cfg.logDir)
	}
	checker := auth.NewFromFile(cfg.authFile, cfg.cidrs)
	var jiraHandler http.Handler
	if cfg.jiraClient != nil {
		jiraHandler, err = jira.NewServer(cfg.jiraClient, checker, cfg.authFile.Jira.Projects, int(cfg.authFile.Jira.MaxFieldBytes))
		if err != nil {
			log.Fatal(err)
		}
	}
	handler := httpapi.New(httpapi.Config{
		Jira:           jiraHandler,
		CustomerLookup: cfg.authFile.CustomerLookup,
		Auth:           checker,
		CK:             ckService,
		Gate:           gate,
		Audit:          audit,
		CKUser:         cfg.ckUser,
		CKPass:         cfg.ckPass,
		CKReady:        ckReady,
	})
	srv := &http.Server{
		Addr:              cfg.listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("shutdown: %v", err)
		}
	}()
	log.Printf("platform-gateway listening on %s", cfg.listen)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func newHTTPClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = cklogs.DefaultTimeoutMS * time.Millisecond
	}
	return &http.Client{
		Timeout: timeout + 5*time.Second,
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          32,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}
}

// newNoRedirectHTTPClient 与 newHTTPClient 相同，但禁止跟随重定向：
// LogBot 未认证时会 302 到 SSO 登录页，跟随跳转会把查询条件泄漏给 SSO。
func newNoRedirectHTTPClient(timeout time.Duration) *http.Client {
	c := newHTTPClient(timeout)
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return c
}

type config struct {
	listen         string
	authFile       *fileconfig.File
	jiraClient     *jira.Client
	cidrs          []*net.IPNet
	logDir         string
	ckProvider     string
	ckBase         string
	ckUser         string
	ckPass         string
	ckIndex        string
	ckTimeout      time.Duration
	lbBase         string
	lbUser         string
	lbPass         string
	lbToken        string
	lbPollInterval time.Duration
	lbTimeout      time.Duration
	maxConc        int
	queueSize      int
	queueWait      time.Duration
}

func loadConfig() (config, error) {
	authFile, err := fileconfig.Load()
	if err != nil {
		return config{}, err
	}
	cidrs, err := auth.ParseCIDRs(strings.Join(authFile.Server.AllowCIDRs, ","))
	if err != nil {
		return config{}, err
	}
	var jiraClient *jira.Client
	if authFile.Enabled("jira") {
		j := authFile.Jira
		jiraClient, err = jira.NewClient(j.BaseURL, j.Auth.Token, j.Auth.Username, j.Auth.Password, jira.WithAccountCacheTTL(time.Duration(j.AccountCacheTtlSeconds)*time.Second))
		if err != nil {
			return config{}, err
		}
	}
	c := authFile.CKLogs
	var user, pass string
	if c.Auth != nil {
		user, pass = c.Auth.Username, c.Auth.Password
	}
	var lbBase, lbUser, lbPass, lbToken string
	var lbPollInterval, lbTimeout time.Duration
	if c.LogBot != nil {
		lbBase = c.LogBot.BaseURL
		lbPollInterval = time.Duration(c.LogBot.PollIntervalMS) * time.Millisecond
		lbTimeout = time.Duration(c.LogBot.TimeoutMS) * time.Millisecond
		if c.LogBot.Auth != nil {
			switch c.LogBot.Auth.Type {
			case "login":
				lbUser, lbPass = c.LogBot.Auth.Username, c.LogBot.Auth.Password
			case "bearer":
				lbToken = c.LogBot.Auth.Token
			}
		}
	}
	dir := ""
	if authFile.Audit.Enabled {
		dir = authFile.Audit.Dir
	}
	return config{
		listen: authFile.Server.ListenAddr, authFile: authFile, jiraClient: jiraClient,
		cidrs: cidrs, logDir: dir, ckProvider: c.Provider, ckBase: c.BaseURL, ckUser: user, ckPass: pass,
		ckIndex: c.Index, ckTimeout: time.Duration(c.TimeoutMS) * time.Millisecond,
		lbBase: lbBase, lbUser: lbUser, lbPass: lbPass, lbToken: lbToken,
		lbPollInterval: lbPollInterval, lbTimeout: lbTimeout,
		maxConc: c.Queue.MaxConcurrency, queueSize: c.Queue.Size,
		queueWait: time.Duration(c.Queue.WaitTimeoutMS) * time.Millisecond,
	}, nil
}
