package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
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
	maxConc, queueSize, queueWaitMS := 2, 32, 30000
	if authFile.Enabled("cklogs") {
		maxConc, err = envIntMin("CKLOGS_MAX_CONCURRENCY", 2, 1)
		if err != nil {
			return config{}, err
		}
		queueSize, err = envIntMin("CKLOGS_QUEUE_SIZE", 32, 0)
		if err != nil {
			return config{}, err
		}
		queueWaitMS, err = envIntMin("CKLOGS_QUEUE_WAIT_MS", 30000, 1)
		if err != nil {
			return config{}, err
		}
		if int64(queueWaitMS) > int64((1<<63-1)/time.Millisecond) {
			return config{}, fmt.Errorf("CKLOGS_QUEUE_WAIT_MS exceeds the supported duration")
		}
	}
	cidrs, err := auth.ParseCIDRs(os.Getenv("GATEWAY_ALLOW_CIDRS"))
	if err != nil {
		return config{}, err
	}
	if authFile.Enabled("cklogs") && (strings.TrimSpace(os.Getenv("CK_LOGS_BASIC_USER")) == "" || strings.TrimSpace(os.Getenv("CK_LOGS_BASIC_PASS")) == "") {
		return config{}, fmt.Errorf("cklogs credentials required")
	}
	var jiraClient *jira.Client
	if authFile.Enabled("jira") {
		jiraClient, err = jira.NewClient(os.Getenv("JIRA_BASE_URL"), os.Getenv("JIRA_API_TOKEN"), os.Getenv("JIRA_BASIC_USER"), os.Getenv("JIRA_BASIC_PASSWORD"))
		if err != nil {
			return config{}, err
		}
	}
	return config{
		listen:     envOr("LISTEN_ADDR", ":8091"),
		authFile:   authFile,
		jiraClient: jiraClient,
		cidrs:      cidrs,
		logDir:     logDir(),
		ckBase:     envOr("CK_LOGS_BASE_URL", cklogs.DefaultBaseURL),
		ckUser:     strings.TrimSpace(os.Getenv("CK_LOGS_BASIC_USER")),
		ckPass:     strings.TrimSpace(os.Getenv("CK_LOGS_BASIC_PASS")),
		ckIndex:    envOr("CK_LOGS_INDEX", "mtatrans_distributed"),
		ckTimeout:  time.Duration(envInt("CK_LOGS_TIMEOUT_MS", 60000)) * time.Millisecond,
		maxConc:    maxConc,
		queueSize:  queueSize,
		queueWait:  time.Duration(queueWaitMS) * time.Millisecond,
	}, nil
}

func logDir() string {
	raw := strings.TrimSpace(os.Getenv("GATEWAY_LOG_DIR"))
	if raw == "off" || raw == "-" {
		return ""
	}
	if raw == "" {
		return "logs"
	}
	return raw
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return n
}

func envIntMin(key string, def, min int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < min {
		return 0, fmt.Errorf("%s must be an integer >= %d", key, min)
	}
	return n, nil
}
