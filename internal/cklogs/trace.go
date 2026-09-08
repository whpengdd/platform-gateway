package cklogs

import "context"

func (s *Service) TraceByTid(ctx context.Context, in MessageQuery) MessageResult {
	tid := in.Tid
	emptyMsg := func(status, provider, code string, lim []string) MessageResult {
		return MessageResult{
			Status:          status,
			Provider:        provider,
			Code:            code,
			Timeline:        []TraceEvent{},
			PartialFailures: []string{},
			Limitations:     lim,
		}
	}
	if tid == "" {
		return emptyMsg("not_available", "none", "", []string{"缺少事务号 tid，跨表追踪无法关联（tid 应取自 query_delivery_log 返回的 entries[].tid），未执行查询。"})
	}
	if s == nil || s.Client == nil || !s.Client.Configured() {
		return emptyMsg("not_available", "none", "", []string{"日志平台凭据未配置（CK_LOGS_BASIC_USER/CK_LOGS_BASIC_PASS），未执行任何查询。"})
	}
	res := s.Client.Trace(ctx, tid, in.TimeRange)
	if !res.OK {
		return emptyMsg("error", "log_platform", ckLogsCode(res.ErrorKind), []string{"跨表追踪失败：" + res.Message})
	}
	lim := []string{}
	var partialSources []string
	for _, f := range res.PartialFailures {
		lim = append(lim, f.Index+" 未查成（"+f.Message+"）——该环节记录缺失是「未查到」而非「无记录」，不得据此下结论。")
		partialSources = append(partialSources, f.Source)
	}
	if len(res.PartialFailures) >= 3 {
		if partialSources == nil {
			partialSources = []string{}
		}
		return MessageResult{
			Status:          "error",
			Provider:        "log_platform",
			Code:            "CK_LOGS_PARTIAL_FAILURE",
			Timeline:        []TraceEvent{},
			PartialFailures: partialSources,
			Limitations:     lim,
		}
	}
	var localRow *TraceEvent
	var bounceRow *TraceEvent
	for i := range res.Timeline {
		e := &res.Timeline[i]
		if localRow == nil && e.Source == "delivery_agent" && e.FolderID != "" {
			localRow = e
		}
		if bounceRow == nil && e.BounceReason != "" {
			bounceRow = e
		}
	}
	if partialSources == nil {
		partialSources = []string{}
	}
	if lim == nil {
		lim = []string{}
	}
	if res.Timeline == nil {
		res.Timeline = []TraceEvent{}
	}
	out := MessageResult{
		Status:          "ok",
		Provider:        "log_platform",
		Tid:             tid,
		Timeline:        res.Timeline,
		PartialFailures: partialSources,
		Limitations:     lim,
	}
	if localRow != nil {
		out.DeliveredFolderID = localRow.FolderID
		out.DeliveredFolderName = FolderName(localRow.FolderID)
		out.DeliverReason = localRow.DeliverReason
	}
	if bounceRow != nil {
		out.BounceReason = bounceRow.BounceReason
	}
	return out
}
