package cklogs

import (
	"context"
	"strings"
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
		res := s.Client.Query(ctx, filters, QueryOptions{CountOnly: true})
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
	countRes := s.Client.Query(ctx, filters, QueryOptions{CountOnly: true})
	if !countRes.OK {
		return mtaFailure(countRes)
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
			fallbackFilters := filters
			fallbackFilters.Recipient = ""
			fallbackCount := s.Client.Query(ctx, fallbackFilters, QueryOptions{CountOnly: true})
			if !fallbackCount.OK {
				return mtaFailure(fallbackCount)
			}
			if fallbackCount.Total > 0 {
				fallbackDetails := s.Client.Query(ctx, fallbackFilters, QueryOptions{PageSize: HardSize})
				if !fallbackDetails.OK {
					return DeliveryResult{
						Status:   "error",
						Provider: "log_platform",
						Code:     ckLogsCode(fallbackDetails.ErrorKind),
						Entries:  []Entry{},
						Limitations: []string{
							"收件人精确查询未命中，候选回查也未完成（" + fallbackDetails.ErrorKind + ": " + fallbackDetails.Message + "）；不能据此断言无记录。",
						},
					}
				}
				var matched []Entry
				for _, entry := range fallbackDetails.Entries {
					if entryHasRecipient(entry, derived.Recipient) {
						matched = append(matched, entry)
					}
				}
				if len(matched) > 0 {
					return DeliveryResult{
						Status:      "ok",
						Provider:    "log_platform",
						Total:       ptrInt(len(matched)),
						Entries:     matched,
						Limitations: []string{"收件人精确条件未直接命中；已按发件人/主题/时间取候选，并对 CK 分号拼接收件人做本地完整地址核对。"},
						CountOnly:   ptrBool(false),
						HasMore:     ptrBool(fallbackCount.Total > len(fallbackDetails.Entries)),
					}
				}
				if fallbackCount.Total > len(fallbackDetails.Entries) {
					return DeliveryResult{
						Status:   "error",
						Provider: "log_platform",
						Code:     "CK_LOGS_RECIPIENT_FALLBACK_UNRESOLVED",
						Entries:  []Entry{},
						Limitations: []string{
							"收件人精确查询未命中，候选共 " + itoa(fallbackCount.Total) + " 条但明细只返回 " + itoa(len(fallbackDetails.Entries)) + " 条，无法确认目标收件人是否存在；不能据此断言无记录。",
						},
					}
				}
			}
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
	res := s.Client.Query(ctx, filters, QueryOptions{PageSize: HardSize})
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

func entryHasRecipient(entry Entry, target string) bool {
	expected := ExtractAddr(target)
	if expected == "" {
		return false
	}
	for _, value := range entry.Recipients {
		for _, part := range strings.Split(strings.ReplaceAll(value, ";", ","), ",") {
			if ExtractAddr(part) == expected {
				return true
			}
		}
	}
	return false
}
