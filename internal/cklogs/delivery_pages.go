package cklogs

import "context"

// The console proxy honors `size` but silently ignores the `from` offset
// (verified against production on 2026-10-26: from=500 returns the leading
// 500 hits again). Full retrieval therefore uses one exact count followed by
// a single page of size=total instead of offset paging. This is still not a
// snapshot protocol: late writes can alter the window between count and
// detail, and the count/detail totals must agree.
//
// The 4000-entry guard keeps one full page under the 8 MiB response cap
// (measured ~1.5 KiB per DATRANS source document, ~28% headroom).
const maxDeliveryAgentEntries = 4000

func (s *Service) queryDeliveryAgentPages(ctx context.Context, filters Filters) (QueryResult, string) {
	if ctx.Err() != nil {
		kind := "network_error"
		if ctx.Err() == context.DeadlineExceeded {
			kind = "timeout"
		}
		return QueryResult{ErrorKind: kind, Message: ctx.Err().Error()}, ""
	}
	count := s.backend().Query(ctx, filters, QueryOptions{Dataset: "delivery_agent", CountOnly: true, Stage: "da_count"})
	if !count.OK {
		return count, ""
	}
	fail := func(message string) (QueryResult, string) { count.Entries = nil; return count, message }
	if !count.TotalExact {
		// Kibana 的不精确计数无法确认完整性，整体报错；LogBot 的 limit/partial
		// 来源状态带有已加载行，按 spec 返回这些数据并附 limitations（不冒充完整）。
		if !count.Truncated && !count.Partial {
			return fail("DATRANS 总数不精确，无法确认完整性")
		}
	}
	if count.TotalExact && count.Total > maxDeliveryAgentEntries {
		return fail("DATRANS 原始日志超过全量护栏（" + itoa(maxDeliveryAgentEntries) + " 条）")
	}
	if count.Total == 0 && !count.Truncated && !count.Partial {
		count.Entries = []Entry{}
		return count, ""
	}
	detail := s.backend().Query(ctx, filters, QueryOptions{Dataset: "delivery_agent", PageSize: count.Total, Stage: "da_full", FullSize: true})
	if !detail.OK {
		return detail, ""
	}
	if count.TotalExact && (!detail.TotalExact || detail.Total != count.Total) {
		return fail("DATRANS 计数与明细总数不一致")
	}
	if len(detail.Entries) != count.Total && count.TotalExact {
		return fail("DATRANS 全量取回返回条数不足或异常")
	}
	detail.Truncated = detail.Truncated || count.Truncated
	detail.Partial = detail.Partial || count.Partial
	detail.Limitations = mergeLimitations(count.Limitations, detail.Limitations)
	return detail, ""
}
