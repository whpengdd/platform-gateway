package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"platform-gateway/internal/cklogs"
)

const (
	maxBodyBytes    = 4096
	defaultWindow   = 24 * time.Hour
	maxLookback     = 30 * 24 * time.Hour
	maxWindow       = 7 * 24 * time.Hour
	defaultPage     = 1
	defaultPageSize = 20
	maxPageSize     = 500
)

type apiError struct {
	Status int
	Code   string
	Msg    string
}

func (e *apiError) Error() string { return e.Msg }

type timeRangeJSON struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type deliveryRequest struct {
	Direction string         `json:"direction"`
	Account   string         `json:"account"`
	Peer      string         `json:"peer"`
	Subject   string         `json:"subject"`
	TimeRange *timeRangeJSON `json:"timeRange"`
	Page      *int           `json:"page"`
	PageSize  *int           `json:"pageSize"`
	CountOnly *bool          `json:"countOnly"`
}

type messageRequest struct {
	Tid       string         `json:"tid"`
	TimeRange *timeRangeJSON `json:"timeRange"`
}

type loginRequest struct {
	Account   string         `json:"account"`
	ClientIP  string         `json:"clientIp"`
	TimeRange *timeRangeJSON `json:"timeRange"`
	CountOnly *bool          `json:"countOnly"`
}

type analysisDeliveryRequest struct {
	Direction string         `json:"direction"`
	Sender    string         `json:"sender"`
	Recipient string         `json:"recipient"`
	Domain    string         `json:"domain"`
	Subject   string         `json:"subject"`
	MsgID     string         `json:"msgId"`
	TimeRange *timeRangeJSON `json:"timeRange"`
	CountOnly *bool          `json:"countOnly"`
}

type analysisAccountRequest struct {
	Account   string         `json:"account"`
	Protocol  string         `json:"protocol"`
	Op        string         `json:"op"`
	TimeRange *timeRangeJSON `json:"timeRange"`
	CountOnly *bool          `json:"countOnly"`
}

var (
	addrFindRe    = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	domainRe      = regexp.MustCompile(`^(?:[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)+[A-Za-z]{2,}$`)
	localPartRe   = regexp.MustCompile(`^[A-Za-z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+$`)
	domainLabelRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
)

func decodeJSON(r *http.Request, dst any) error {
	if r.Body == nil {
		return &apiError{http.StatusBadRequest, "invalid_body", "request body is required"}
	}
	defer r.Body.Close()
	limited := io.LimitReader(r.Body, maxBodyBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return &apiError{http.StatusBadRequest, "invalid_body", "无法读取请求体"}
	}
	if len(raw) > maxBodyBytes {
		return &apiError{http.StatusBadRequest, "invalid_body", "请求体过大"}
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return &apiError{http.StatusBadRequest, "invalid_body", "request body is required"}
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return mapJSONError(err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return &apiError{http.StatusBadRequest, "invalid_body", "request body 含有多余内容"}
	}
	return nil
}

func mapJSONError(err error) error {
	msg := err.Error()
	if strings.HasPrefix(msg, "json: unknown field ") {
		field := strings.Trim(strings.TrimPrefix(msg, "json: unknown field "), `"`)
		return &apiError{http.StatusBadRequest, "unknown_field", "unknown field: " + field}
	}
	var ue *json.UnmarshalTypeError
	if errors.As(err, &ue) {
		return &apiError{http.StatusBadRequest, "invalid_body", "字段 " + ue.Field + " 类型无效"}
	}
	var se *json.SyntaxError
	if errors.As(err, &se) {
		return &apiError{http.StatusBadRequest, "invalid_body", "JSON 无法解析"}
	}
	return &apiError{http.StatusBadRequest, "invalid_body", "JSON 无法解析"}
}

func parseTimeRange(tr *timeRangeJSON, now time.Time) (cklogs.TimeRange, error) {
	var fromVal, toVal *time.Time
	if tr != nil {
		if strings.TrimSpace(tr.From) != "" {
			t, err := parseBoundary(tr.From, "from")
			if err != nil {
				return cklogs.TimeRange{}, err
			}
			fromVal = &t
		}
		if strings.TrimSpace(tr.To) != "" {
			t, err := parseBoundary(tr.To, "to")
			if err != nil {
				return cklogs.TimeRange{}, err
			}
			toVal = &t
		}
	}
	end := now
	if toVal != nil {
		end = *toVal
	}
	start := now.Add(-defaultWindow)
	if fromVal != nil {
		start = *fromVal
	} else if toVal != nil {
		start = end.Add(-defaultWindow)
	}
	if !start.Before(end) {
		return cklogs.TimeRange{}, &apiError{http.StatusBadRequest, "invalid_time_range", "时间范围的开始时间必须早于结束时间"}
	}
	if start.After(now) || end.After(now) {
		return cklogs.TimeRange{}, &apiError{http.StatusBadRequest, "invalid_time_range", "时间范围不能晚于当前时间"}
	}
	if start.Before(now.Add(-maxLookback)) {
		return cklogs.TimeRange{}, &apiError{http.StatusBadRequest, "time_range_too_old", "查询时间不能早于最近 30 天"}
	}
	if end.Sub(start) > maxWindow {
		return cklogs.TimeRange{}, &apiError{http.StatusBadRequest, "time_range_too_large", "单次查询窗口不能超过 7 天（168 小时）"}
	}
	return cklogs.TimeRange{FromMs: start.UnixMilli(), ToMs: end.UnixMilli()}, nil
}

func parseBoundary(value, label string) (time.Time, error) {
	text := strings.TrimSpace(value)
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.000Z07:00",
		"2006-01-02T15:04:05Z07:00",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, text); err == nil {
			return t, nil
		}
	}
	return time.Time{}, &apiError{http.StatusBadRequest, "invalid_time_range", label + " 不是有效的时间"}
}

func readPage(page, pageSize *int) (int, int, error) {
	p := defaultPage
	ps := defaultPageSize
	if page != nil {
		p = *page
	}
	if pageSize != nil {
		ps = *pageSize
	}
	if p < 1 || ps < 1 || ps > maxPageSize {
		return 0, 0, &apiError{http.StatusBadRequest, "invalid_pagination", "分页参数无效：page 必须为正整数，pageSize 必须为 1-500"}
	}
	return p, ps, nil
}

func parseOptionalMailbox(raw, field string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	addr, err := parseAccount(raw)
	if err == nil {
		return addr, nil
	}
	var ae *apiError
	if errors.As(err, &ae) && ae.Code == "missing_account" {
		return "", nil
	}
	if errors.As(err, &ae) {
		return "", &apiError{ae.Status, "invalid_" + field, field + " 必须是完整且合法的邮箱地址"}
	}
	return "", err
}

func parseOptionalDomain(raw string) (string, error) {
	domain := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(raw, "@")))
	if domain == "" {
		return "", nil
	}
	if !domainRe.MatchString(domain) {
		return "", &apiError{http.StatusBadRequest, "invalid_domain", "domain 必须是合法域名"}
	}
	return domain, nil
}

func parseAnalysisProtocol(kind, raw string) (string, error) {
	protocol := strings.ToLower(strings.TrimSpace(raw))
	if protocol == "" {
		return "", &apiError{http.StatusBadRequest, "missing_protocol", "protocol 不能为空"}
	}
	if kind == "ops" {
		switch protocol {
		case "webmail", "pop3", "imap":
			return protocol, nil
		}
		return "", &apiError{http.StatusBadRequest, "invalid_protocol", "protocol 必须是 webmail、pop3 或 imap"}
	}
	switch protocol {
	case "pop3", "smtp", "webmail", "imap":
		return protocol, nil
	}
	return "", &apiError{http.StatusBadRequest, "invalid_protocol", "protocol 必须是 pop3、smtp、webmail 或 imap"}
}

func parseAnalysisOp(kind, raw string) (string, error) {
	if kind != "ops" {
		if strings.TrimSpace(raw) != "" {
			return "", &apiError{http.StatusBadRequest, "unknown_field", "unknown field: op"}
		}
		return "", nil
	}
	op := strings.ToLower(strings.TrimSpace(raw))
	if op == "" {
		return "all", nil
	}
	switch op {
	case "delete", "forward", "empty_folder", "recall", "all":
		return op, nil
	}
	return "", &apiError{http.StatusBadRequest, "invalid_op", "op 必须是 delete、forward、empty_folder、recall 或 all"}
}

func analysisDataset(kind, protocol string) string {
	if kind == "ops" {
		return "mail_ops_" + protocol
	}
	return "auth_" + protocol
}

func parseAccount(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", &apiError{http.StatusBadRequest, "missing_account", "account 不能为空"}
	}
	invalid := &apiError{http.StatusBadRequest, "invalid_account", "account 必须是完整且合法的邮箱地址"}
	if len(s) > 320 {
		return "", invalid
	}
	for _, r := range s {
		if r <= 0x20 || r == 0x7f || strings.ContainsRune(`<>(),;:"'`, r) {
			return "", invalid
		}
	}
	at := strings.IndexByte(s, '@')
	if at <= 0 || at != strings.LastIndexByte(s, '@') || at == len(s)-1 {
		return "", invalid
	}
	local := s[:at]
	domain := strings.ToLower(s[at+1:])
	if len(local) > 64 || !localPartRe.MatchString(local) {
		return "", invalid
	}
	if len(domain) > 253 || strings.Contains(domain, "..") {
		return "", invalid
	}
	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		return "", invalid
	}
	for _, label := range labels {
		if !domainLabelRe.MatchString(label) {
			return "", invalid
		}
	}
	return strings.ToLower(local) + "@" + domain, nil
}

type peerFilter struct {
	Address string
	Domain  string
}

func parsePeer(raw string) (peerFilter, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return peerFilter{}, &apiError{http.StatusBadRequest, "invalid_peer_filter", "对方地址不能为空，请输入完整邮箱或域名"}
	}
	address := normalizeAddress(raw)
	if address != "" && address == strings.ToLower(raw) {
		return peerFilter{Address: address}, nil
	}
	domain := strings.ToLower(strings.TrimPrefix(raw, "@"))
	if domainRe.MatchString(domain) {
		return peerFilter{Domain: domain}, nil
	}
	return peerFilter{}, &apiError{http.StatusBadRequest, "invalid_peer_filter", "对方地址格式不正确，请输入完整邮箱或域名"}
}

func normalizeAddress(value string) string {
	raw := strings.TrimSpace(value)
	raw = strings.TrimLeft(raw, `<"' `)
	raw = strings.TrimRight(raw, `>"' ,;：:`)
	if raw == "" {
		return ""
	}
	m := addrFindRe.FindString(raw)
	if m == "" {
		return ""
	}
	return strings.ToLower(m)
}

func buildDeliveryQuery(direction, account string, peer peerFilter) (sender, recipient, senderDomain, recipientDomain string) {
	if direction == "inbound" {
		recipient = account
		if peer.Address != "" {
			sender = peer.Address
		}
		if peer.Domain != "" {
			senderDomain = peer.Domain
		}
		return
	}
	sender = account
	if peer.Address != "" {
		recipient = peer.Address
	}
	if peer.Domain != "" {
		recipientDomain = peer.Domain
	}
	return
}
