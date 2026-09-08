package httpapi

import (
	"context"
	"net/http"
	"strings"

	"platform-gateway/internal/auditlog"
	"platform-gateway/internal/cklogs"
)

func (s *Server) handleDelivery(w http.ResponseWriter, r *http.Request) {
	var ok bool
	if r, ok = s.checkAuth(w, r); !ok {
		return
	}
	var req deliveryRequest
	if err := decodeJSON(r, &req); err != nil {
		writeAPIError(w, err)
		return
	}
	direction := strings.TrimSpace(req.Direction)
	if direction != "outbound" && direction != "inbound" {
		writeAPIError(w, &apiError{http.StatusBadRequest, "invalid_direction", "direction 必须是 outbound 或 inbound"})
		return
	}
	account, err := parseAccount(req.Account)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	peer, err := parsePeer(req.Peer)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	tr, err := parseTimeRange(req.TimeRange, s.now())
	if err != nil {
		writeAPIError(w, err)
		return
	}
	page, pageSize, err := readPage(req.Page, req.PageSize)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	sender, recipient, senderDomain, recipientDomain := buildDeliveryQuery(direction, account, peer)
	countOnly := req.CountOnly != nil && *req.CountOnly
	q := cklogs.DeliveryQuery{
		Direction:       direction,
		Sender:          sender,
		Recipient:       recipient,
		SenderDomain:    senderDomain,
		RecipientDomain: recipientDomain,
		Subject:         req.Subject,
		TimeRange:       tr,
		Page:            page,
		PageSize:        pageSize,
		CountOnly:       countOnly,
	}
	if rec := auditlog.From(r.Context()); rec != nil {
		rec.SetQuery(map[string]any{
			"direction":       direction,
			"account":         account,
			"peer":            strings.TrimSpace(req.Peer),
			"subject":         req.Subject,
			"time_range_from": tr.FromMs,
			"time_range_to":   tr.ToMs,
			"page":            page,
			"page_size":       pageSize,
			"count_only":      countOnly,
		})
	}
	var out cklogs.DeliveryResult
	ok = s.runCklogs(w, r, func(ctx context.Context) error {
		out = s.ck.QuerySelfServiceDelivery(ctx, q)
		return nil
	})
	if !ok {
		return
	}
	if rec := auditlog.From(r.Context()); rec != nil {
		rec.SetResult(out.Status, out.Code, out.Total, len(out.Entries), out.Limitations)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleMessage(w http.ResponseWriter, r *http.Request) {
	var ok bool
	if r, ok = s.checkAuth(w, r); !ok {
		return
	}
	var req messageRequest
	if err := decodeJSON(r, &req); err != nil {
		writeAPIError(w, err)
		return
	}
	tid := strings.TrimSpace(req.Tid)
	if tid == "" {
		writeAPIError(w, &apiError{http.StatusBadRequest, "missing_tid", "tid 不能为空"})
		return
	}
	tr, err := parseTimeRange(req.TimeRange, s.now())
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if rec := auditlog.From(r.Context()); rec != nil {
		rec.SetQuery(map[string]any{"tid": tid, "time_range_from": tr.FromMs, "time_range_to": tr.ToMs})
	}
	var out cklogs.MessageResult
	ok = s.runCklogs(w, r, func(ctx context.Context) error {
		out = s.ck.TraceByTid(ctx, cklogs.MessageQuery{Tid: tid, TimeRange: tr})
		return nil
	})
	if !ok {
		return
	}
	if rec := auditlog.From(r.Context()); rec != nil {
		rec.SetResult(out.Status, out.Code, nil, len(out.Timeline), out.Limitations)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var ok bool
	if r, ok = s.checkAuth(w, r); !ok {
		return
	}
	var req loginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeAPIError(w, err)
		return
	}
	account, err := parseAccount(req.Account)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	tr, err := parseTimeRange(req.TimeRange, s.now())
	if err != nil {
		writeAPIError(w, err)
		return
	}
	countOnly := req.CountOnly != nil && *req.CountOnly
	if rec := auditlog.From(r.Context()); rec != nil {
		rec.SetQuery(map[string]any{
			"account":         account,
			"client_ip":       strings.TrimSpace(req.ClientIP),
			"time_range_from": tr.FromMs,
			"time_range_to":   tr.ToMs,
			"count_only":      countOnly,
		})
	}
	var out cklogs.LoginResult
	ok = s.runCklogs(w, r, func(ctx context.Context) error {
		out = s.ck.QueryLogin(ctx, account, strings.TrimSpace(req.ClientIP), tr, countOnly)
		return nil
	})
	if !ok {
		return
	}
	if rec := auditlog.From(r.Context()); rec != nil {
		returned := 0
		var status, code string
		var limitations []string
		for _, item := range out.Results {
			returned += len(item.Entries)
			if status == "" {
				status = item.Status
				code = item.Code
			}
			limitations = append(limitations, item.Limitations...)
		}
		rec.SetResult(status, code, nil, returned, limitations)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAnalysisDelivery(w http.ResponseWriter, r *http.Request) {
	var ok bool
	if r, ok = s.checkAuth(w, r); !ok {
		return
	}
	if !s.requireInternal(w, r) {
		return
	}
	var req analysisDeliveryRequest
	if err := decodeJSON(r, &req); err != nil {
		writeAPIError(w, err)
		return
	}
	direction := strings.TrimSpace(req.Direction)
	if direction == "" {
		direction = "both"
	}
	if direction != "outbound" && direction != "inbound" && direction != "both" {
		writeAPIError(w, &apiError{http.StatusBadRequest, "invalid_direction", "direction 必须是 outbound、inbound 或 both"})
		return
	}
	sender, err := parseOptionalMailbox(req.Sender, "sender")
	if err != nil {
		writeAPIError(w, err)
		return
	}
	recipient, err := parseOptionalMailbox(req.Recipient, "recipient")
	if err != nil {
		writeAPIError(w, err)
		return
	}
	domain, err := parseOptionalDomain(req.Domain)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	tr, err := parseTimeRange(req.TimeRange, s.now())
	if err != nil {
		writeAPIError(w, err)
		return
	}
	countOnly := req.CountOnly != nil && *req.CountOnly
	q := cklogs.DeliveryQuery{
		Direction: direction,
		Sender:    sender,
		Recipient: recipient,
		Domain:    domain,
		Subject:   strings.TrimSpace(req.Subject),
		MsgID:     strings.TrimSpace(req.MsgID),
		TimeRange: tr,
		CountOnly: countOnly,
	}
	if rec := auditlog.From(r.Context()); rec != nil {
		rec.SetQuery(map[string]any{
			"direction":       direction,
			"sender":          sender,
			"recipient":       recipient,
			"domain":          domain,
			"subject":         q.Subject,
			"msg_id":          q.MsgID,
			"time_range_from": tr.FromMs,
			"time_range_to":   tr.ToMs,
			"count_only":      countOnly,
		})
	}
	var out cklogs.DeliveryResult
	ok = s.runCklogs(w, r, func(ctx context.Context) error {
		out = s.ck.QueryAnalysisDelivery(ctx, q)
		return nil
	})
	if !ok {
		return
	}
	if rec := auditlog.From(r.Context()); rec != nil {
		rec.SetResult(out.Status, out.Code, out.Total, len(out.Entries), out.Limitations)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAnalysisAuth(w http.ResponseWriter, r *http.Request) {
	s.handleAnalysisAccount(w, r, "auth")
}

func (s *Server) handleAnalysisOps(w http.ResponseWriter, r *http.Request) {
	s.handleAnalysisAccount(w, r, "ops")
}

func (s *Server) handleAnalysisAccount(w http.ResponseWriter, r *http.Request, kind string) {
	var ok bool
	if r, ok = s.checkAuth(w, r); !ok {
		return
	}
	if !s.requireInternal(w, r) {
		return
	}
	var req analysisAccountRequest
	if err := decodeJSON(r, &req); err != nil {
		writeAPIError(w, err)
		return
	}
	account, err := parseAccount(req.Account)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	protocol, err := parseAnalysisProtocol(kind, req.Protocol)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	op, err := parseAnalysisOp(kind, req.Op)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	tr, err := parseTimeRange(req.TimeRange, s.now())
	if err != nil {
		writeAPIError(w, err)
		return
	}
	countOnly := req.CountOnly != nil && *req.CountOnly
	dataset := analysisDataset(kind, protocol)
	if rec := auditlog.From(r.Context()); rec != nil {
		rec.SetQuery(map[string]any{
			"account":         account,
			"protocol":        protocol,
			"op":              op,
			"dataset":         dataset,
			"time_range_from": tr.FromMs,
			"time_range_to":   tr.ToMs,
			"count_only":      countOnly,
		})
	}
	var out cklogs.AccountResult
	ok = s.runCklogs(w, r, func(ctx context.Context) error {
		out = s.ck.QueryAccountLog(ctx, cklogs.AccountQuery{
			Dataset:   dataset,
			Account:   account,
			Op:        op,
			TimeRange: tr,
			CountOnly: countOnly,
		})
		return nil
	})
	if !ok {
		return
	}
	if rec := auditlog.From(r.Context()); rec != nil {
		rec.SetResult(out.Status, out.Code, out.Total, len(out.Entries), out.Limitations)
	}
	writeJSON(w, http.StatusOK, out)
}
