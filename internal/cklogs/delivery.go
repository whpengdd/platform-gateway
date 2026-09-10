package cklogs

import (
	"context"
	"strings"
)

func isOutboundSentFolderCopy(entry Entry, direction string) bool {
	return (direction == "outbound" || direction == "inbound") && strings.EqualFold(strings.TrimSpace(entry.Cmd), "local") &&
		(strings.TrimSpace(entry.FolderID) == "3" || strings.TrimSpace(entry.FolderName) == "已发送" ||
			strings.TrimSpace(entry.StatusText) == "已投递到已发送")
}

func (s *Service) QuerySelfServiceDelivery(ctx context.Context, in DeliveryQuery) DeliveryResult {
	if s == nil || s.Client == nil || !s.Client.Configured() {
		return notConfiguredDelivery()
	}
	derived := deriveQuery(in)
	page := in.Page
	if page < 1 {
		page = 1
	}
	pageSize := in.PageSize
	if pageSize < 1 {
		pageSize = HardSize
	}
	if pageSize > HardSize {
		pageSize = HardSize
	}
	if in.CountOnly {
		da := s.Client.Query(ctx, Filters{
			Sender:    derived.Sender,
			Recipient: derived.Recipient,
			TimeRange: in.TimeRange,
		}, QueryOptions{Dataset: "delivery_agent", CountOnly: true})
		if !da.OK {
			msg := "DATRANS 查询失败：" + da.Message
			if da.ErrorKind == "timeout" {
				msg = timeoutMessage()
			}
			return deliveryError(ckLogsCode(da.ErrorKind), msg, nil)
		}
		total := da.Total
		if total == 0 {
			mta := s.Client.Query(ctx, Filters{
				Sender:    derived.Sender,
				Recipient: derived.Recipient,
				TimeRange: in.TimeRange,
			}, QueryOptions{CountOnly: true})
			if !mta.OK {
				return mtaFailure(mta)
			}
			total = mta.Total
		}
		return DeliveryResult{
			Status:      "ok",
			Provider:    "log_platform",
			Total:       ptrInt(total),
			Entries:     []Entry{},
			Limitations: []string{"本次为计数查询（countOnly），只返回命中总数、不含明细。"},
			CountOnly:   ptrBool(true),
			Page:        page,
			PageSize:    pageSize,
			HasMore:     ptrBool(false),
		}
	}

	da := s.Client.Query(ctx, Filters{
		Sender:    derived.Sender,
		Recipient: derived.Recipient,
		TimeRange: in.TimeRange,
	}, QueryOptions{Dataset: "delivery_agent", PageSize: HardSize})
	if !da.OK {
		msg := "DATRANS 查询失败：" + da.Message
		if da.ErrorKind == "timeout" {
			msg = timeoutMessage()
		}
		return deliveryError(ckLogsCode(da.ErrorKind), msg, nil)
	}
	if da.Total > len(da.Entries) {
		return DeliveryResult{
			Status:      "error",
			Provider:    "log_platform",
			Code:        "CK_LOGS_DA_TRUNCATED",
			Total:       ptrInt(da.Total),
			Entries:     []Entry{},
			Limitations: []string{"DATRANS 明细最多返回 " + itoa(HardSize) + " 条，本次结果已截断；不返回不完整结果。请缩小时间范围或增加收件人、主题等条件后重试。"},
			Truncated:   ptrBool(true),
			CountOnly:   ptrBool(false),
			Page:        page,
			PageSize:    pageSize,
			HasMore:     ptrBool(false),
		}
	}

	var matching []Entry
	for _, entry := range da.Entries {
		if matchesDeliveryConditions(entry, derived) && !isOutboundSentFolderCopy(entry, in.Direction) {
			matching = append(matching, entry)
		}
	}
	var withTid []Entry
	for _, entry := range matching {
		if strings.TrimSpace(entry.Tid) != "" {
			withTid = append(withTid, entry)
		}
	}
	daRows := aggregatePeerRows(withTid, in.Direction, "da")
	if len(daRows) == 0 {
		mtaFilters := Filters{
			Sender:    derived.Sender,
			Recipient: derived.Recipient,
			Subject:   derived.Subject,
			MsgID:     derived.MsgID,
			TimeRange: in.TimeRange,
		}
		mta := s.queryMTA(ctx, mtaFilters, derived, true)
		if mta.Status != "ok" {
			return mta
		}
		var matchedMTA []Entry
		for _, entry := range mta.Entries {
			if matchesDeliveryConditions(entry, derived) {
				matchedMTA = append(matchedMTA, entry)
			}
		}
		mtaRows := aggregatePeerRows(matchedMTA, in.Direction, "mta")
		offset := (page - 1) * pageSize
		end := offset + pageSize
		if offset > len(mtaRows) {
			offset = len(mtaRows)
		}
		if end > len(mtaRows) {
			end = len(mtaRows)
		}
		lim := append([]string{}, mta.Limitations...)
		lim = append(lim, "DATRANS 聚合后零命中，已使用相同发件人、收件人、主题和时间条件回查 MTA。")
		slice := mtaRows[offset:end]
		if slice == nil {
			slice = []Entry{}
		}
		return DeliveryResult{
			Status:      "ok",
			Provider:    "log_platform",
			Code:        mta.Code,
			Total:       ptrInt(len(mtaRows)),
			Entries:     slice,
			Limitations: lim,
			CountOnly:   ptrBool(false),
			Page:        page,
			PageSize:    pageSize,
			HasMore:     ptrBool(len(mtaRows) > offset+pageSize),
			Truncated:   ptrBool(false),
		}
	}

	type group struct {
		row  Entry
		tids []string
		seen map[string]struct{}
	}
	groups := []*group{}
	index := map[string]*group{}
	for _, row := range daRows {
		tid := strings.TrimSpace(row.Tid)
		aggregateID := messageKey(row, "tid:"+strings.ToLower(tid))
		key := aggregateID + "\x00" + row.Peer
		g, ok := index[key]
		if !ok {
			g = &group{row: row, seen: map[string]struct{}{}}
			index[key] = g
			groups = append(groups, g)
		}
		if tid != "" {
			if _, exists := g.seen[tid]; !exists {
				g.seen[tid] = struct{}{}
				g.tids = append(g.tids, tid)
			}
		}
		rowHasStatus := row.DeliveryStatus != "unknown"
		currentHasStatus := g.row.DeliveryStatus != "unknown"
		isNewer := row.TimestampISO >= g.row.TimestampISO
		if (rowHasStatus && !currentHasStatus) || (rowHasStatus == currentHasStatus && isNewer) {
			g.row = row
		}
	}

	var unknownTids []string
	unknownSeen := map[string]struct{}{}
	for _, g := range groups {
		if g.row.DeliveryStatus == "unknown" && isDeliveryAgentMainAction(g.row) && len(g.tids) == 1 {
			tid := g.tids[0]
			if _, ok := unknownSeen[tid]; ok {
				continue
			}
			unknownSeen[tid] = struct{}{}
			unknownTids = append(unknownTids, tid)
		}
	}

	// 串行 tid 回查没有上限会把并发槽占到 N*超时；截断或超时后未回查的行保持 unknown，并写 limitation。
	const maxUnknownTidLookups = 8
	lim := []string{}
	if len(unknownTids) > maxUnknownTidLookups {
		unknownTids = unknownTids[:maxUnknownTidLookups]
		lim = append(lim, "未知状态记录的 MTA 回查已达上限（"+itoa(maxUnknownTidLookups)+" 条），其余保持 unknown，不能据此断定投递结果。")
	}
	mtaByTid := map[string]DeliveryResult{}
	lookupBudgetExhausted := false
	for _, tid := range unknownTids {
		if ctx.Err() != nil {
			lookupBudgetExhausted = true
			break
		}
		res := s.queryMTA(ctx, Filters{MsgID: tid, TimeRange: in.TimeRange}, derivedQuery{MsgID: tid}, true)
		if res.Status != "ok" {
			if res.Code == "CK_LOGS_TIMEOUT" {
				lookupBudgetExhausted = true
				break
			}
			return res
		}
		mtaByTid[tid] = res
	}
	if lookupBudgetExhausted {
		lim = append(lim, "部分未知状态记录的 MTA 回查超时，对应记录保持 unknown，不能据此断定投递结果。")
	}

	rows := make([]Entry, 0, len(groups))
	for _, g := range groups {
		row := g.row
		if row.DeliveryStatus != "unknown" || len(g.tids) != 1 {
			rows = append(rows, row)
			continue
		}
		tid := g.tids[0]
		mta := mtaByTid[tid]
		var filtered []Entry
		for _, entry := range mta.Entries {
			if strings.TrimSpace(entry.Tid) == tid && matchesDeliveryConditions(entry, derived) {
				filtered = append(filtered, entry)
			}
		}
		resolvedList := aggregatePeerRows(filtered, in.Direction, "mta")
		var resolved *Entry
		for i := range resolvedList {
			if resolvedList[i].Peer == row.Peer && resolvedList[i].DeliveryStatus != "unknown" {
				resolved = &resolvedList[i]
				break
			}
		}
		if resolved == nil {
			rows = append(rows, row)
			continue
		}
		row.StatusSource = "mta"
		row.DeliveryStatus = resolved.DeliveryStatus
		row.StatusText = resolved.StatusText
		row.FailureReason = resolved.FailureReason
		rows = append(rows, row)
	}
	filteredRows := rows[:0]
	for _, row := range rows {
		if !isOutboundSentFolderCopy(row, in.Direction) {
			filteredRows = append(filteredRows, row)
		}
	}
	rows = filteredRows
	sortEntriesDesc(rows)
	offset := (page - 1) * pageSize
	end := offset + pageSize
	if offset > len(rows) {
		offset = len(rows)
	}
	if end > len(rows) {
		end = len(rows)
	}
	slice := rows[offset:end]
	if slice == nil {
		slice = []Entry{}
	}
	return DeliveryResult{
		Status:      "ok",
		Provider:    "log_platform",
		Total:       ptrInt(len(rows)),
		Entries:     slice,
		Limitations: lim,
		Truncated:   ptrBool(false),
		CountOnly:   ptrBool(false),
		Page:        page,
		PageSize:    pageSize,
		HasMore:     ptrBool(len(rows) > (page-1)*pageSize+pageSize),
	}
}
