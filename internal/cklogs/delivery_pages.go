package cklogs

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"time"
)

// This is an offset protocol, not a snapshot protocol. Strictly separated
// timestamp boundaries avoid unstable ties without assuming an undocumented
// unique sortable field. Late writes can still alter an otherwise valid window.
const maxDeliveryAgentEntries = 5000

func (s *Service) queryDeliveryAgentPages(ctx context.Context, filters Filters) (QueryResult, string) {
	var collected []Entry
	var first QueryResult
	seenPages := map[[32]byte]bool{}
	seenIDs := map[string]bool{}
	var previous time.Time
	for offset := 0; offset < maxDeliveryAgentEntries; {
		if ctx.Err() != nil {
			kind := "network_error"
			if ctx.Err() == context.DeadlineExceeded {
				kind = "timeout"
			}
			return QueryResult{ErrorKind: kind, Message: ctx.Err().Error()}, ""
		}
		current := s.Client.Query(ctx, filters, QueryOptions{Dataset: "delivery_agent", PageSize: HardSize, Offset: offset, Stage: "da_page"})
		if !current.OK {
			return current, ""
		}
		if offset == 0 {
			first = current
		}
		fail := func(message string) (QueryResult, string) { first.Entries = nil; return first, message }
		if !current.TotalExact {
			return fail("DATRANS 总数不精确，无法确认完整性")
		}
		if current.Total > maxDeliveryAgentEntries {
			return fail("DATRANS 原始日志超过全量护栏（" + itoa(maxDeliveryAgentEntries) + " 条）")
		}
		if current.Total != first.Total {
			return fail("DATRANS 分页期间总数变化")
		}
		expected := first.Total - offset
		if expected > HardSize {
			expected = HardSize
		}
		if len(current.Entries) != expected {
			return fail("DATRANS 分页返回条数不足或异常")
		}
		if first.Total > HardSize {
			raw, _ := json.Marshal(current.Entries)
			digest := sha256.Sum256(raw)
			if seenPages[digest] {
				return fail("DATRANS 重复分页，无法确认完整性")
			}
			seenPages[digest] = true
			for _, id := range current.HitIDs {
				if id == "" {
					continue
				}
				if seenIDs[id] {
					return fail("DATRANS 跨页出现重复日志标识")
				}
				seenIDs[id] = true
			}
			for i, entry := range current.Entries {
				stamp, err := time.Parse(time.RFC3339Nano, entry.TimestampISO)
				if err != nil {
					return fail("DATRANS 分页缺少有效排序时间")
				}
				if i == 0 && offset > 0 && !stamp.Before(previous) {
					return fail("DATRANS 分页边界时间重复或顺序变化，无法安全取全")
				}
				if i > 0 && stamp.After(previous) {
					return fail("DATRANS 分页排序异常")
				}
				previous = stamp
			}
		}
		collected = append(collected, current.Entries...)
		offset += len(current.Entries)
		if offset == first.Total {
			first.Entries = collected
			return first, ""
		}
	}
	first.Entries = nil
	return first, "DATRANS 分页达到全量护栏"
}
