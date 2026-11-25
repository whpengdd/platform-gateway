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
	if s.backendName() == "logbot" {
		// LogBot 实测没有 tid 列，也不支持按 tid 筛选，无法串联投递轨迹。
		return emptyMsg("not_available", "log_platform", "", []string{"当前日志后端（LogBot）不提供 tid 列，也不支持按 tid 筛选，按 tid 串联投递轨迹（/v1/cklogs/message）不可用，未执行任何查询。注意：这不等于「没有记录」；可按发件人/收件人/时间窗改用 delivery 查询。"})
	}
	if !s.configured() {
		return emptyMsg("not_available", "none", "", []string{notConfiguredMessage(s.backendName())})
	}
	res := s.backend().Trace(ctx, tid, in.TimeRange)
	if !res.OK {
		if res.ErrorKind == "not_available" {
			return emptyMsg("not_available", "log_platform", "", []string{res.Message})
		}
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
