package cklogs

import "context"

func (s *Service) queryProxy(ctx context.Context, filters Filters, derived derivedQuery, page, pageSize int) DeliveryResult {
	countRes := s.Client.Query(ctx, filters, QueryOptions{Dataset: "delivery_proxy", CountOnly: true, Stage: "proxy_exact_count"})
	if !countRes.OK {
		return proxyFailure(countRes)
	}
	if !countRes.TotalExact {
		return deliveryError("CK_LOGS_PROXY_TRUNCATED", "PROXY 总数不精确，无法确认完整性；"+narrowQueryAdvice, nil)
	}
	if countRes.Total > HardSize {
		return DeliveryResult{
			Status:      "error",
			Provider:    "log_platform",
			Code:        "CK_LOGS_PROXY_TRUNCATED",
			Total:       ptrInt(countRes.Total),
			Entries:     []Entry{},
			Limitations: []string{"PROXY 原始日志命中 " + itoa(countRes.Total) + " 条，超过平台最多返回 " + itoa(HardSize) + " 条的上限，无法完成全量聚合与准确分页；本次不返回不完整结果。" + narrowQueryAdvice},
			Truncated:   ptrBool(true),
			CountOnly:   ptrBool(false),
			HasMore:     ptrBool(false),
		}
	}

	if countRes.Total == 0 && derived.Recipient != "" && derived.Sender != derived.Recipient {
		// A term miss may hide a compound `to` value. Without a verified
		// complete token predicate, no account-scoped fallback is available.
		return recipientCoverageUnresolved("PROXY", "CK_LOGS_PROXY_RECIPIENT_FALLBACK_UNRESOLVED")
	}
	if countRes.Total == 0 {
		return proxyPageResult(nil, page, pageSize, nil)
	}

	details := s.Client.Query(ctx, filters, QueryOptions{Dataset: "delivery_proxy", PageSize: HardSize, Stage: "proxy_exact_detail"})
	if !details.OK {
		return proxyFailure(details)
	}
	if !details.TotalExact || countRes.Total != details.Total || details.Total != len(details.Entries) {
		return DeliveryResult{
			Status:      "error",
			Provider:    "log_platform",
			Code:        "CK_LOGS_PROXY_TRUNCATED",
			Total:       ptrInt(countRes.Total),
			Entries:     []Entry{},
			Limitations: []string{"PROXY 明细未完整取回，无法完成本地精确过滤与聚合；本次不返回不完整结果。" + narrowQueryAdvice},
			Truncated:   ptrBool(true),
			CountOnly:   ptrBool(false),
			HasMore:     ptrBool(false),
		}
	}
	matched := make([]Entry, 0, len(details.Entries))
	for _, entry := range details.Entries {
		if matchesProxyConditions(entry, derived) {
			matched = append(matched, entry)
		}
	}
	limitations := []string{}
	return proxyPageResult(aggregateProxyRows(matched), page, pageSize, limitations)
}

func proxyPageResult(rows []Entry, page, pageSize int, limitations []string) DeliveryResult {
	if rows == nil {
		rows = []Entry{}
	}
	offset := len(rows)
	if page-1 <= len(rows)/pageSize {
		offset = (page - 1) * pageSize
		if offset > len(rows) {
			offset = len(rows)
		}
	}
	end := len(rows)
	if pageSize <= len(rows)-offset {
		end = offset + pageSize
	}
	entries := rows[offset:end]
	if entries == nil {
		entries = []Entry{}
	}
	return DeliveryResult{
		Status:      "ok",
		Provider:    "log_platform",
		Total:       ptrInt(len(rows)),
		Entries:     entries,
		Limitations: limitations,
		Truncated:   ptrBool(false),
		CountOnly:   ptrBool(false),
		Page:        page,
		PageSize:    pageSize,
		HasMore:     ptrBool(len(rows) > offset+pageSize),
	}
}

func proxyFailure(res QueryResult) DeliveryResult {
	msg := "PROXY 日志查询失败：" + res.Message
	if res.ErrorKind == "timeout" {
		msg = timeoutMessage()
	}
	return deliveryError(ckLogsCode(res.ErrorKind), msg, nil)
}
