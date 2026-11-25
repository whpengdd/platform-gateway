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
	if !s.configured() {
		return notConfiguredDeliveryFor(s.backendName())
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
		countLim := []string{}
		da := s.backend().Query(ctx, Filters{
			Sender:    derived.Sender,
			Recipient: derived.Recipient,
			TimeRange: in.TimeRange,
		}, QueryOptions{Dataset: "delivery_agent", CountOnly: true, Stage: "da_count"})
		if !da.OK {
			if da.ErrorKind == "not_available" {
				return backendUnavailableDelivery(da.Message)
			}
			msg := "DATRANS 查询失败：" + da.Message
			if da.ErrorKind == "timeout" {
				msg = timeoutMessage()
			}
			return deliveryError(ckLogsCode(da.ErrorKind), msg, nil)
		}
		countLim = append(countLim, da.Limitations...)
		total := da.Total
		if total == 0 {
			mta := s.backend().Query(ctx, Filters{
				Sender:    derived.Sender,
				Recipient: derived.Recipient,
				TimeRange: in.TimeRange,
			}, QueryOptions{CountOnly: true, Stage: "mta_count"})
			if !mta.OK {
				return mtaFailure(mta)
			}
			countLim = append(countLim, mta.Limitations...)
			total = mta.Total
		}
		if total == 0 && in.Direction == "inbound" {
			proxy := s.backend().Query(ctx, Filters{
				Sender:    derived.Sender,
				Recipient: derived.Recipient,
				TimeRange: in.TimeRange,
			}, QueryOptions{Dataset: "delivery_proxy", CountOnly: true, Stage: "proxy_count"})
			if !proxy.OK {
				return proxyFailure(proxy)
			}
			countLim = append(countLim, proxy.Limitations...)
			total = proxy.Total
		}
		lim := append([]string{"本次为计数查询（countOnly），只返回命中总数、不含明细。"}, countLim...)
		return DeliveryResult{
			Status:      "ok",
			Provider:    "log_platform",
			Total:       ptrInt(total),
			Entries:     []Entry{},
			Limitations: lim,
			CountOnly:   ptrBool(true),
			Page:        page,
			PageSize:    pageSize,
			HasMore:     ptrBool(false),
		}
	}

	da, incomplete := s.queryDeliveryAgentPages(ctx, Filters{
		Sender: derived.Sender, Recipient: derived.Recipient, TimeRange: in.TimeRange,
	})
	if !da.OK {
		if da.ErrorKind == "not_available" {
			return backendUnavailableDelivery(da.Message)
		}
		msg := "DATRANS 查询失败：" + da.Message
		if da.ErrorKind == "timeout" {
			msg = timeoutMessage()
		}
		return deliveryError(ckLogsCode(da.ErrorKind), msg, nil)
	}
	if incomplete != "" {
		return DeliveryResult{
			Status: "error", Provider: "log_platform", Code: "CK_LOGS_DA_TRUNCATED",
			Total: ptrInt(da.Total), Entries: []Entry{},
			Limitations: []string{incomplete + "；不返回不完整结果。" + narrowQueryAdvice},
			Truncated:   ptrBool(true), CountOnly: ptrBool(false), Page: page,
			PageSize: pageSize, HasMore: ptrBool(false),
		}
	}

	var matching []Entry
	for _, entry := range da.Entries {
		if matchesDeliveryConditions(entry, derived) && !isOutboundSentFolderCopy(entry, in.Direction) {
			matching = append(matching, entry)
		}
	}
	// Kibana 的 DA 行带 tid，按 tid 分组聚合同一封邮件的多条命令行；
	// LogBot 的行没有 tid，本地聚合改为逐行（行已按 cmd=local/remote 过滤去重），
	// 分组键退化为 message-id/记录序号，语义与 aggregatePeerRows 的 tid 空值回退一致。
	var daRows []Entry
	if s.backendName() == "logbot" {
		daRows = aggregatePeerRows(matching, in.Direction, "da")
	} else {
		var withTid []Entry
		for _, entry := range matching {
			if strings.TrimSpace(entry.Tid) != "" {
				withTid = append(withTid, entry)
			}
		}
		daRows = aggregatePeerRows(withTid, in.Direction, "da")
	}
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
		lim := append([]string{}, mta.Limitations...)
		lim = append(lim, "DATRANS 聚合后零命中，已使用相同发件人、收件人、主题和时间条件回查 MTA。")
		if s.backendName() == "logbot" {
			for _, r := range mtaRows {
				if r.DeliveryStatus == "unknown" {
					lim = append(lim, "LogBot 的 MTA 行不包含投递结果与对端响应，deliveryStatus=unknown 的记录无法判读投递结果与失败原因，需要人工核查。")
					break
				}
			}
		}
		if in.Direction == "inbound" && len(mtaRows) == 0 {
			proxy := s.queryProxy(ctx, Filters{
				Sender:    derived.Sender,
				Recipient: derived.Recipient,
				TimeRange: in.TimeRange,
			}, derived, page, pageSize)
			if proxy.Status != "ok" {
				return proxy
			}
			proxy.Limitations = append(lim, proxy.Limitations...)
			if proxy.Total != nil && *proxy.Total == 0 {
				proxy.Limitations = []string{
					"DATRANS 聚合后零命中，已使用相同发件人、收件人、主题和时间条件回查 MTA。",
					"在所选时间范围及查询条件内，未发现发件方向我方系统投递该邮件",
				}
			}
			return proxy
		}
		out := proxyPageResult(mtaRows, page, pageSize, lim)
		if mta.Truncated != nil {
			out.Truncated = mta.Truncated
		}
		return out
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
	lim := append([]string{}, da.Limitations...)
	if s.backendName() == "logbot" {
		for _, e := range matching {
			if strings.TrimSpace(e.Tid) == "" && strings.TrimSpace(e.Mid) == "" && strings.TrimSpace(e.MessageID) == "" {
				lim = append(lim, "LogBot 的 DA 行没有 tid，部分行也缺 message-id：同一封邮件的 local/remote 命令行可能无法合并，列表可能按命令行分行显示，条数不代表邮件封数。")
				break
			}
		}
		for _, g := range groups {
			if g.row.DeliveryStatus == "unknown" && isDeliveryAgentMainAction(g.row) {
				lim = append(lim, "LogBot 不提供 tid 列，无法按 tid 回查 MTA 补判状态；deliveryStatus=unknown 的记录保持未知，不能据此断定投递结果。")
				break
			}
		}
	}
	if da.Total > HardSize {
		lim = append(lim, "DATRANS 已在计数后单页全量取回；上游未提供快照，查询期间的迟到或变更日志可能影响计数与明细的一致性。")
	}
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
		Truncated:   ptrBool(da.Truncated),
		CountOnly:   ptrBool(false),
		Page:        page,
		PageSize:    pageSize,
		HasMore:     ptrBool(len(rows) > (page-1)*pageSize+pageSize),
	}
}
