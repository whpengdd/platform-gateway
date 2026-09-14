package cklogs

import "context"

func (s *Service) queryProxy(ctx context.Context, filters Filters, derived derivedQuery, page, pageSize int) DeliveryResult {
	countRes := s.Client.Query(ctx, filters, QueryOptions{Dataset: "delivery_proxy", CountOnly: true})
	if !countRes.OK {
		return proxyFailure(countRes)
	}
	if countRes.Total > HardSize {
		return DeliveryResult{
			Status:      "error",
			Provider:    "log_platform",
			Code:        "CK_LOGS_PROXY_TRUNCATED",
			Total:       ptrInt(countRes.Total),
			Entries:     []Entry{},
			Limitations: []string{"PROXY 原始日志命中 " + itoa(countRes.Total) + " 条，超过平台最多返回 " + itoa(HardSize) + " 条的上限，无法完成全量聚合与准确分页；本次不返回不完整结果。"},
			Truncated:   ptrBool(true),
			CountOnly:   ptrBool(false),
			HasMore:     ptrBool(false),
		}
	}

	queryFilters := filters
	candidateNote := ""
	if countRes.Total == 0 && derived.Recipient != "" && derived.Sender != derived.Recipient {
		queryFilters.Recipient = ""
		candidateCount := s.Client.Query(ctx, queryFilters, QueryOptions{Dataset: "delivery_proxy", CountOnly: true})
		if !candidateCount.OK {
			return proxyFailure(candidateCount)
		}
		if candidateCount.Total > HardSize {
			return proxyRecipientFallbackUnresolved(candidateCount.Total, 0)
		}
		countRes = candidateCount
		candidateNote = "收件人精确条件未直接命中；已按其余条件取候选，并对 PROXY 收件人做本地完整地址核对。"
	}
	if countRes.Total == 0 {
		return proxyPageResult(nil, page, pageSize, nil)
	}

	details := s.Client.Query(ctx, queryFilters, QueryOptions{Dataset: "delivery_proxy", PageSize: HardSize})
	if !details.OK {
		return proxyFailure(details)
	}
	if countRes.Total > len(details.Entries) || details.Total > len(details.Entries) {
		if queryFilters.Recipient == "" && filters.Recipient != "" {
			return proxyRecipientFallbackUnresolved(countRes.Total, len(details.Entries))
		}
		return DeliveryResult{
			Status:      "error",
			Provider:    "log_platform",
			Code:        "CK_LOGS_PROXY_TRUNCATED",
			Total:       ptrInt(countRes.Total),
			Entries:     []Entry{},
			Limitations: []string{"PROXY 明细未完整取回，无法完成本地精确过滤与聚合；本次不返回不完整结果。"},
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
	if candidateNote != "" {
		limitations = append(limitations, candidateNote)
	}
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

func proxyRecipientFallbackUnresolved(total, returned int) DeliveryResult {
	return DeliveryResult{
		Status:   "error",
		Provider: "log_platform",
		Code:     "CK_LOGS_PROXY_RECIPIENT_FALLBACK_UNRESOLVED",
		Entries:  []Entry{},
		Limitations: []string{
			"PROXY 收件人精确查询未命中，候选共 " + itoa(total) + " 条但明细只返回 " + itoa(returned) + " 条，无法确认目标收件人是否存在；不能据此断言无记录。",
		},
		Truncated: ptrBool(total > returned),
		CountOnly: ptrBool(false),
		HasMore:   ptrBool(false),
	}
}
