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
	// All routes share the same Kibana budget, including internal analysis calls.
	gate := queue.New("cklogs", cfg.maxConc, cfg.queueSize, cfg.queueWait)
	log.Printf("Kibana queue: max_concurrency=%d queue_size=%d wait_timeout=%s", cfg.maxConc, cfg.queueSize, cfg.queueWait)
	client := &cklogs.Client{
		BaseURL:    cfg.ckBase,
		User:       cfg.ckUser,
		Pass:       cfg.ckPass,
		Index:      cfg.ckIndex,
		Timeout:    cfg.ckTimeout,
		HTTPClient: newHTTPClient(cfg.ckTimeout),
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
		jiraHandler, err = jira.NewServer(cfg.jiraClient, checker, cfg.authFile.Jira.Projects)
		if err != nil {
			log.Fatal(err)
		}
	}
	handler := httpapi.New(httpapi.Config{
		Jira:   jiraHandler,
		Auth:   checker,
		CK:     cklogs.NewService(client),
		Gate:   gate,
		Audit:  audit,
		CKUser: cfg.ckUser,
		CKPass: cfg.ckPass,
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

type config struct {
	listen     string
	authFile   *fileconfig.File
	jiraClient *jira.Client
	cidrs      []*net.IPNet
	logDir     string
	ckBase     string
	ckUser     string
	ckPass     string
	ckIndex    string
	ckTimeout  time.Duration
	maxConc    int
	queueSize  int
	queueWait  time.Duration
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
		jiraClient, err = jira.NewClient(j.BaseURL, j.Auth.Token, j.Auth.Username, j.Auth.Password)
		if err != nil {
			return config{}, err
		}
	}
	c := authFile.CKLogs
	var user, pass string
	if c.Auth != nil {
		user, pass = c.Auth.Username, c.Auth.Password
	}
	dir := ""
	if authFile.Audit.Enabled {
		dir = authFile.Audit.Dir
	}
	return config{
		listen: authFile.Server.ListenAddr, authFile: authFile, jiraClient: jiraClient,
		cidrs: cidrs, logDir: dir, ckBase: c.BaseURL, ckUser: user, ckPass: pass,
		ckIndex: c.Index, ckTimeout: time.Duration(c.TimeoutMS) * time.Millisecond,
		maxConc: c.Queue.MaxConcurrency, queueSize: c.Queue.Size,
		queueWait: time.Duration(c.Queue.WaitTimeoutMS) * time.Millisecond,
	}, nil
}
