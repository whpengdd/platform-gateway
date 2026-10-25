package cklogs

import (
	"context"
)

func (s *Service) QueryAnalysisDelivery(ctx context.Context, in DeliveryQuery) DeliveryResult {
	if s == nil || s.Client == nil || !s.Client.Configured() {
		return notConfiguredDelivery()
	}
	derived := deriveQuery(in)
	filters := Filters{
		Sender:    derived.Sender,
		Recipient: derived.Recipient,
		Domain:    derived.Domain,
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
		res := s.Client.Query(ctx, filters, QueryOptions{CountOnly: true, Stage: "mta_exact_count"})
		if !res.OK {
			return mtaFailure(res)
		}
		return DeliveryResult{
			Status:      "ok",
			Provider:    "log_platform",
			Total:       ptrInt(res.Total),
			Entries:     []Entry{},
			Limitations: []string{"本次为计数查询（countOnly），只返回精确命中总数、不含明细；需要逐封判读时请另发一次不带 countOnly 的查询。"},
			CountOnly:   ptrBool(true),
			HasMore:     ptrBool(false),
		}
	}
	return s.queryMTA(ctx, filters, derived, false)
}

func (s *Service) queryMTA(ctx context.Context, filters Filters, derived derivedQuery, requireComplete bool) DeliveryResult {
	if !s.Client.Configured() {
		return notConfiguredDelivery()
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
	countRes := s.Client.Query(ctx, filters, QueryOptions{CountOnly: true, Stage: "mta_exact_count"})
	if !countRes.OK {
		return mtaFailure(countRes)
	}
	if requireComplete && !countRes.TotalExact {
		return deliveryError("CK_LOGS_MTA_TRUNCATED", "MTA 总数不精确，无法确认完整性；"+narrowQueryAdvice, nil)
	}
	if requireComplete && countRes.Total > HardSize {
		msg := "MTA 原始日志命中 " + itoa(countRes.Total) + " 条，超过平台最多返回 " + itoa(HardSize) + " 条的上限，无法完成全量聚合与准确分页；本次不返回不完整结果。请缩小时间范围或增加收件人、主题等条件后重试。"
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
	if countRes.Total == 0 {
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
			Limitations: []string{zeroNote},
			CountOnly:   ptrBool(false),
			HasMore:     ptrBool(false),
		}
	}
	res := s.Client.Query(ctx, filters, QueryOptions{PageSize: HardSize, Stage: "mta_exact_detail"})
	if !res.OK {
		if requireComplete {
			return mtaFailure(res)
		}
		return DeliveryResult{
			Status:   "ok",
			Provider: "log_platform",
			Total:    ptrInt(countRes.Total),
			Entries:  []Entry{},
			Limitations: []string{
				"精确命中 " + itoa(countRes.Total) + " 条，但明细查询未取回（" + res.ErrorKind + ": " + res.Message + "）——已知有记录、逐封内容不可用；不要据此断言「没有记录」。",
			},
			CountOnly: ptrBool(false),
			HasMore:   ptrBool(countRes.Total > 0),
		}
	}
	lim := []string{}
	if res.Total > len(res.Entries) {
		lim = append(lim, "命中 "+itoa(res.Total)+" 条，本页返回 "+itoa(len(res.Entries))+" 条（单页上限 "+itoa(HardSize)+"）。")
	}
	entries := res.Entries
	if entries == nil {
		entries = []Entry{}
	}
	if requireComplete && (!res.TotalExact || countRes.Total != res.Total || res.Total != len(entries)) {
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
	return DeliveryResult{
		Status:      "ok",
		Provider:    "log_platform",
		Total:       ptrInt(res.Total),
		Entries:     entries,
		Limitations: lim,
		CountOnly:   ptrBool(false),
		HasMore:     ptrBool(res.Total > len(entries)),
	}
}

func mtaFailure(res QueryResult) DeliveryResult {
	msg := "日志平台查询失败：" + res.Message
	if res.ErrorKind == "timeout" {
		msg = timeoutMessage()
	}
	return deliveryError(ckLogsCode(res.ErrorKind), msg, nil)
}
