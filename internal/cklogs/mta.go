package cklogs

import (
	"context"
)

func (s *Service) QueryAnalysisDelivery(ctx context.Context, in DeliveryQuery) DeliveryResult {
	if !s.configured() {
		return notConfiguredDeliveryFor(s.backendName())
	}
	derived := deriveQuery(in)
	filters := Filters{
		Sender:    derived.Sender,
		Recipient: derived.Recipient,
		Domain:    derived.Domain,
		Direction: in.Direction,
		Subject:   derived.Subject,
		MsgID:     derived.MsgID,
		TimeRange: in.TimeRange,
	}
	if in.CountOnly {
		if derived.Sender == "" && derived.Recipient == "" && derived.Domain == "" && derived.MsgID == "" && derived.Subject == "" {
			return DeliveryResult{
				Status:      "not_available",
				Provider:    "none",
				Entries:     []Entry{},
				Limitations: []string{"显式入参与邮件上下文均无法提取查询条件（sender/recipient/domain/subject/msgId 全空），未执行查询。"},
			}
		}
		res := s.backend().Query(ctx, filters, QueryOptions{CountOnly: true, Stage: "mta_exact_count"})
		if !res.OK {
			return mtaFailure(res)
		}
		lim := append([]string{"本次为计数查询（countOnly），只返回精确命中总数、不含明细；需要逐封判读时请另发一次不带 countOnly 的查询。"}, res.Limitations...)
		return DeliveryResult{
			Status:      "ok",
			Provider:    "log_platform",
			Total:       ptrInt(res.Total),
			Entries:     []Entry{},
			Limitations: lim,
			CountOnly:   ptrBool(true),
			HasMore:     ptrBool(false),
		}
	}
	return s.queryMTA(ctx, filters, derived, false)
}

func (s *Service) queryMTA(ctx context.Context, filters Filters, derived derivedQuery, requireComplete bool) DeliveryResult {
	if !s.configured() {
		return notConfiguredDeliveryFor(s.backendName())
	}
	if derived.Sender == "" && derived.Recipient == "" && derived.Domain == "" && derived.MsgID == "" && derived.Subject == "" {
		return DeliveryResult{
			Status:      "not_available",
			Provider:    "none",
			Entries:     []Entry{},
			Limitations: []string{"显式入参与邮件上下文均无法提取查询条件（sender/recipient/domain/subject/msgId 全空），未执行查询。"},
		}
	}
	sameSenderRecipient := derived.Sender != "" && derived.Recipient != "" && derived.Sender == derived.Recipient
	maxRows := s.backend().MaxDetailRows()
	if s.backendName() == "logbot" {
		// LogBot 明细响应同时带 rows 与 total_count，且 DA 路径已证实
		// count/detail 一致性假设不成立；先 count 只会把串行 job 数翻倍。
		return s.queryMTALogBotDirect(ctx, filters, derived, requireComplete, maxRows)
	}
	countRes := s.backend().Query(ctx, filters, QueryOptions{CountOnly: true, Stage: "mta_exact_count"})
	if !countRes.OK {
		return mtaFailure(countRes)
	}
	// Kibana 的不精确计数无法确认完整性，必须整体报错；LogBot 的
	// limit/partial 透传语义已移到前面的明细直查分支。
	incomplete := !countRes.TotalExact
	tolerated := incomplete && (countRes.Truncated || countRes.Partial)
	if requireComplete && incomplete && !tolerated {
		return deliveryError("CK_LOGS_MTA_TRUNCATED", "MTA 总数不精确，无法确认完整性；"+narrowQueryAdvice, nil)
	}
	if requireComplete && !tolerated && countRes.Total > maxRows {
		msg := "MTA 原始日志命中 " + itoa(countRes.Total) + " 条，超过平台最多返回 " + itoa(maxRows) + " 条的上限，无法完成全量聚合与准确分页；本次不返回不完整结果。请缩小时间范围或增加收件人、主题等条件后重试。"
		return DeliveryResult{
			Status:      "error",
			Provider:    "log_platform",
			Code:        "CK_LOGS_MTA_TRUNCATED",
			Total:       ptrInt(countRes.Total),
			Entries:     []Entry{},
			Limitations: []string{msg},
			Truncated:   ptrBool(true),
			CountOnly:   ptrBool(false),
			HasMore:     ptrBool(false),
		}
	}
	if countRes.Total == 0 && !tolerated {
		canRetryRecipient := !sameSenderRecipient && derived.Recipient != "" && (derived.Sender != "" || derived.Subject != "" || derived.MsgID != "")
		if canRetryRecipient {
			// No verified indexed token field exists for compound recipient values.
			// Removing Recipient loses the inbound account boundary; repeating the
			// same phrase query cannot prove compound-address coverage either.
			return recipientCoverageUnresolved("MTA", "CK_LOGS_RECIPIENT_FALLBACK_UNRESOLVED")
		}
		zeroNote := "该时间窗内按给定条件精确命中 0 条：我方日志无对应投递记录（非查询失败、非截断）。"
		if sameSenderRecipient {
			zeroNote = "发件人与收件人是同一个地址；该时间范围内没有查到发给自己的邮件。"
		}
		return DeliveryResult{
			Status:      "ok",
			Provider:    "log_platform",
			Total:       ptrInt(0),
			Entries:     []Entry{},
			Limitations: append([]string{zeroNote}, countRes.Limitations...),
			CountOnly:   ptrBool(false),
			HasMore:     ptrBool(false),
		}
	}
	res := s.backend().Query(ctx, filters, QueryOptions{PageSize: maxRows, Stage: "mta_exact_detail"})
	if !res.OK {
		if requireComplete {
			return mtaFailure(res)
		}
		return DeliveryResult{
			Status:   "ok",
			Provider: "log_platform",
			Total:    ptrInt(countRes.Total),
			Entries:  []Entry{},
			Limitations: append(append([]string{
				"精确命中 " + itoa(countRes.Total) + " 条，但明细查询未取回（" + res.ErrorKind + ": " + res.Message + "）——已知有记录、逐封内容不可用；不要据此断言「没有记录」。",
			}, countRes.Limitations...), res.Limitations...),
			CountOnly: ptrBool(false),
			HasMore:   ptrBool(countRes.Total > 0),
		}
	}
	lim := mergeLimitations(countRes.Limitations, res.Limitations)
	if res.Total > len(res.Entries) {
		lim = append(lim, "命中 "+itoa(res.Total)+" 条，本页返回 "+itoa(len(res.Entries))+" 条（单页上限 "+itoa(maxRows)+"）。")
	}
	entries := res.Entries
	if entries == nil {
		entries = []Entry{}
	}
	if requireComplete && !tolerated && (!res.TotalExact || countRes.Total != res.Total || res.Total != len(entries)) {
		return DeliveryResult{
			Status:      "error",
			Provider:    "log_platform",
			Code:        "CK_LOGS_MTA_TRUNCATED",
			Total:       ptrInt(countRes.Total),
			Entries:     []Entry{},
			Limitations: []string{"MTA 明细未完整取回，无法完成本地精确过滤与聚合；本次不返回不完整结果。" + narrowQueryAdvice},
			Truncated:   ptrBool(true),
			CountOnly:   ptrBool(false),
			HasMore:     ptrBool(false),
		}
	}
	out := DeliveryResult{
		Status:      "ok",
		Provider:    "log_platform",
		Total:       ptrInt(res.Total),
		Entries:     entries,
		Limitations: lim,
		CountOnly:   ptrBool(false),
		HasMore:     ptrBool(res.Total > len(entries)),
	}
	if countRes.Truncated || res.Truncated {
		out.Truncated = ptrBool(true)
	}
	return out
}

// queryMTALogBotDirect 是 LogBot 的 MTA 明细直查路径。完整性按三类真实信号判断：
// TotalExact/Truncated/Partial 是上游快照状态；RowsIncomplete 由后端按单个 job
// 比较上游 total_count 与该 job 实际取回原始行数后 OR 累积（不能用跨窗合计，
// 否则正负差额会相抵）。requireComplete 用这些数据做本地精确过滤/聚合，任何一类
// 不完整都必须拒绝；普通 analysis 则透传已取回行并附 limitations。
func (s *Service) queryMTALogBotDirect(ctx context.Context, filters Filters, derived derivedQuery, requireComplete bool, maxRows int) DeliveryResult {
	res := s.backend().Query(ctx, filters, QueryOptions{PageSize: maxRows, Stage: "mta_exact_detail"})
	if !res.OK {
		return mtaFailure(res)
	}
	incomplete := res.Truncated || res.Partial || !res.TotalExact || res.RowsIncomplete
	if requireComplete && incomplete {
		// 这里没有精确总数可填：res.Total 只是已取回并去重后的部分行数，
		// UpstreamTotal 又含重复/快照外行；具体数字已经写进 limitations。
		lim := append([]string{"MTA 明细未完整取回，无法完成本地精确过滤与聚合；本次不返回不完整结果。" + narrowQueryAdvice}, res.Limitations...)
		return DeliveryResult{
			Status:      "error",
			Provider:    "log_platform",
			Code:        "CK_LOGS_MTA_TRUNCATED",
			Entries:     []Entry{},
			Limitations: mergeLimitations(lim),
			Truncated:   ptrBool(true),
			CountOnly:   ptrBool(false),
			HasMore:     ptrBool(false),
		}
	}
	sameSenderRecipient := derived.Sender != "" && derived.Recipient != "" && derived.Sender == derived.Recipient
	if res.Total == 0 && !incomplete {
		canRetryRecipient := !sameSenderRecipient && derived.Recipient != "" && (derived.Sender != "" || derived.Subject != "" || derived.MsgID != "")
		if canRetryRecipient {
			// No verified indexed token field exists for compound recipient values.
			// Removing Recipient loses the inbound account boundary; repeating the
			// same phrase query cannot prove compound-address coverage either.
			return recipientCoverageUnresolved("MTA", "CK_LOGS_RECIPIENT_FALLBACK_UNRESOLVED")
		}
		zeroNote := "该时间窗内按给定条件精确命中 0 条：我方日志无对应投递记录（非查询失败、非截断）。"
		if sameSenderRecipient {
			zeroNote = "发件人与收件人是同一个地址；该时间范围内没有查到发给自己的邮件。"
		}
		return DeliveryResult{
			Status:      "ok",
			Provider:    "log_platform",
			Total:       ptrInt(0),
			Entries:     []Entry{},
			Limitations: append([]string{zeroNote}, res.Limitations...),
			CountOnly:   ptrBool(false),
			HasMore:     ptrBool(false),
		}
	}
	entries := res.Entries
	if entries == nil {
		entries = []Entry{}
	}
	out := DeliveryResult{
		Status:      "ok",
		Provider:    "log_platform",
		Total:       ptrInt(res.Total),
		Entries:     entries,
		Limitations: append([]string{}, res.Limitations...),
		CountOnly:   ptrBool(false),
		// 该 API 面不暴露 LogBot page token，调用方无法继续翻页；
		// 不完整信息只能由 Truncated+limitations 承载，hasMore 恒 false。
		HasMore: ptrBool(false),
	}
	if res.Truncated {
		out.Truncated = ptrBool(true)
	}
	return out
}

func mergeLimitations(groups ...[]string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, g := range groups {
		for _, s := range g {
			if s == "" || seen[s] {
				continue
			}
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func mtaFailure(res QueryResult) DeliveryResult {
	if res.ErrorKind == "not_available" {
		return backendUnavailableDelivery(res.Message)
	}
	msg := "日志平台查询失败：" + res.Message
	if res.ErrorKind == "timeout" {
		msg = timeoutMessage()
	}
	return deliveryError(ckLogsCode(res.ErrorKind), msg, nil)
}
