package cklogs

import (
	"encoding/json"
	"strings"
	"time"

	"platform-gateway/internal/auditlog"
)

// logbot_map.go 把 LogBot 行（map 列结构）映射为 Kibana _source 形状的 map，
// 再复用 map.go 的现有转换函数（toIso、parseOtherlog、FolderName、cacVerdict）。
// LogBot 未提供的字段（tid、mid、respond、errinfo、score、spamfng、blackip、debuginfo）
// 不填入猜测值，直接缺省。映射依据 design.md「实测结论」与 /tmp/logbot-samples 样本。

// logbotTimestamp 把 LogBot 的 "+08:00" 字符串时间转为 epoch 毫秒（toIso 的输入形态）。
// 返回 0 表示无法解析，调用方将省略该字段。
func logbotTimestamp(v any) float64 {
	s := strings.TrimSpace(asString(v))
	if s == "" {
		return 0
	}
	if t, err := time.Parse("2006-01-02 15:04:05-07:00", s); err == nil {
		return float64(t.UnixMilli())
	}
	// 少数响应（如 sources.window）是不带时区的上海本地时间。
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", s, auditlog.Shanghai()); err == nil {
		return float64(t.UnixMilli())
	}
	return 0
}

// logbotOtherlog 规整 otherlog：LogBot 返回的是对象，而 map.go 的 parseOtherlogRaw
// 期望 JSON 字符串，这里重新序列化以复用同一解析路径。
func logbotOtherlog(v any) any {
	switch t := v.(type) {
	case map[string]any:
		raw, err := json.Marshal(t)
		if err != nil {
			return nil
		}
		return string(raw)
	case string:
		return t
	}
	return nil
}

func logbotPut(dst map[string]any, key string, v any) {
	if v == nil {
		return
	}
	if s, ok := v.(string); ok && strings.TrimSpace(s) == "" {
		return
	}
	dst[key] = v
}

// logbotRowToKibanaSource 把一行 LogBot 数据转成对应 Kibana dataset 的 _source map。
// source 取 "DA" / "MTA" / "PROXYSVR"。未知列被忽略。
func logbotRowToKibanaSource(row map[string]any, source string) map[string]any {
	if row == nil {
		return map[string]any{}
	}
	out := map[string]any{}
	if ts := logbotTimestamp(row["timestamp"]); ts > 0 {
		out["timestamp"] = ts
	}
	logbotPut(out, "otherlog", logbotOtherlog(row["otherlog"]))
	switch source {
	case "DA":
		// datrans：mapDeliveryAgentHit 读取 mailfrom/to/subject/cmd/result/commresult/
		// folderid/desc/otherlog.hdrmsgid。
		logbotPut(out, "mailfrom", firstNonEmpty(row["mailfrom"], row["hdrfrom"]))
		logbotPut(out, "to", row["to"])
		logbotPut(out, "subject", row["subject"])
		logbotPut(out, "cmd", row["cmd"])
		logbotPut(out, "result", firstNonEmpty(row["result"], row["resultmessage"]))
		logbotPut(out, "commresult", row["commresult"])
		logbotPut(out, "folderid", row["folderid"])
		logbotPut(out, "desc", row["desc"])
		logbotPut(out, "cactag", row["cactag"])
		logbotPut(out, "datarulename", row["datarulename"])
		logbotPut(out, "cntrulename", row["cntrulename"])
		logbotPut(out, "clientip", firstNonEmpty(row["clientip"], row["ip"]))
	case "MTA":
		// mtatrans：mapHitToEntry 读取 sender/rcpt/subject/cmd/result/commresult/
		// clientip/cactag/datarulename/cntrulename/otherlog。
		logbotPut(out, "sender", firstNonEmpty(row["sender"], row["mailfrom"]))
		if rcpt, ok := row["rcpt"].([]any); ok {
			out["rcpt"] = rcpt
		} else {
			logbotPut(out, "rcpt", row["rcpt"])
		}
		logbotPut(out, "subject", row["subject"])
		logbotPut(out, "cmd", row["cmd"])
		logbotPut(out, "result", row["result"])
		logbotPut(out, "commresult", row["commresult"])
		logbotPut(out, "clientip", firstNonEmpty(row["clientip"], row["ip"]))
		logbotPut(out, "cactag", row["cactag"])
		logbotPut(out, "datarulename", row["datarulename"])
		logbotPut(out, "cntrulename", row["cntrulename"])
	case "PROXYSVR":
		// proxytrans：mapDeliveryProxyHit 读取 from/to/subject/result/desc/ip/log_source。
		logbotPut(out, "from", firstNonEmpty(row["from"], row["mailfrom"]))
		logbotPut(out, "to", row["to"])
		logbotPut(out, "subject", row["subject"])
		logbotPut(out, "result", row["result"])
		logbotPut(out, "desc", row["desc"])
		logbotPut(out, "ip", firstNonEmpty(row["ip"], row["clientip"]))
		logbotPut(out, "log_source", row["log_source"])
	}
	return out
}

// mapLogBotRow 把 LogBot 行直接映射为 Entry（复用 map.go 的 Kibana 转换函数）。
func mapLogBotRow(row map[string]any, source string) Entry {
	src := logbotRowToKibanaSource(row, source)
	switch source {
	case "DA":
		return mapDeliveryAgentHit(src)
	case "PROXYSVR":
		return mapDeliveryProxyHit(src)
	default:
		return mapHitToEntry(src)
	}
}

// logbotDAMainCmd 报告 DA 行是否为主命令行：实测 LogBot 的 DA 一次投递包含
// antivirus、local、remote 和空 cmd 的汇总行，业务对齐只取 local/remote。
func logbotDAMainCmd(row map[string]any) bool {
	cmd := strings.ToLower(strings.TrimSpace(asString(row["cmd"])))
	return cmd == "local" || cmd == "remote"
}

// logbotRowKey 用于 LogBot 行去重（同一来源可能返回重复行）。
func logbotRowKey(row map[string]any, source string) string {
	parts := []string{
		source,
		asString(row["timestamp"]),
		asString(firstNonEmpty(row["mailfrom"], row["sender"], row["from"], row["hdrfrom"])),
		asString(row["to"]),
		asString(row["subject"]),
		strings.ToLower(strings.TrimSpace(asString(row["cmd"]))),
		asString(row["result"]),
		asString(row["folderid"]),
	}
	if other, ok := row["otherlog"].(map[string]any); ok {
		// 不同邮件可能同秒同发件人同主题同收件人，不把去重键放宽到跨邮件命中。
		parts = append(parts, asString(other["hdrmsgid"]), asString(other["commrefid"]))
	}
	if rcpt, ok := row["rcpt"].([]any); ok {
		parts = append(parts, asString(rcpt))
	}
	if source == logbotSourceDA {
		other := logbotDAOtherlog(row)
		if strings.TrimSpace(asString(other["hdrmsgid"])) == "" && strings.TrimSpace(asString(other["commrefid"])) == "" {
			// 无键行不能靠公共字段合并（实测同秒同发件人同主题的不同邮件存在）：
			// 序列化整行，只有完全重复的行才折叠。
			if raw, err := json.Marshal(row); err == nil {
				parts = append(parts, "fullrow", string(raw))
			}
		}
	}
	return strings.Join(parts, "\x00")
}

// logbotDAOtherlog 取出行的 otherlog 对象。
func logbotDAOtherlog(row map[string]any) map[string]any {
	other, _ := row["otherlog"].(map[string]any)
	return other
}

// logbotDAGroupKey 返回 DA 行的投递分组键，用于把同一次投递的 local/remote
// 命令行合并为一条记录。回退链：
//
//	hdrmsgid → commrefid → 单行（返回 "" 表示无可靠键）
//
// hdrmsgid 即邮件 message-id，实测（2026-11-24，149 网关对照 40 条
// local/remote 行）覆盖率高且与 Kibana messageId 逐行一致。commrefid 是
// 投递关联键：实测每个 commrefid 至多对应一个 hdrmsgid，不同投递不复用，
// 作为次选键不会把不同邮件合并。hdr 行与 commrefid 相同的无 hdr 行属于
// 同一投递，后者归入 hdr 组并继承其 message-id 供下游聚合。
// 两键皆缺时不做时间+发件人+主题的启发式合并：实测同秒同主题发往同一
// 收件人的不同邮件存在，启发式键误并不为零。
func logbotDAGroupKey(row map[string]any, crefToHdr map[string]string) string {
	other := logbotDAOtherlog(row)
	if hdr := strings.TrimSpace(asString(other["hdrmsgid"])); hdr != "" {
		return "hdr\x00" + hdr
	}
	if cref := strings.TrimSpace(asString(other["commrefid"])); cref != "" {
		if hdr, ok := crefToHdr[cref]; ok {
			return "hdr\x00" + hdr
		}
		return "cref\x00" + cref
	}
	return ""
}

// logbotDAGroupHdr 返回组的 message-id：组内 hdr 行的 hdrmsgid。
func logbotDAGroupHdr(key string) string {
	if hdr, ok := strings.CutPrefix(key, "hdr\x00"); ok {
		return hdr
	}
	return ""
}

// logbotDARowTo 规整 DA 行收件人（aggregatePeerRows 按收件人拆 peer，
// 合并必须在同一收件人内进行）。
func logbotDARowTo(row map[string]any) string {
	return strings.ToLower(strings.TrimSpace(asString(row["to"])))
}

// logbotDAPickRow 在一组（同投递、同收件人）行里挑代表行，语义对齐
// aggregatePeerRows 的替换规则：有投递状态映射的行优先于 unknown，
// 同优先级取时间较新的行。
func logbotDAPickRow(rows []map[string]any) map[string]any {
	best := rows[0]
	bestEntry := mapLogBotRow(best, logbotSourceDA)
	_, bestKnown := deliveryAgentStatus(bestEntry)
	for _, row := range rows[1:] {
		entry := mapLogBotRow(row, logbotSourceDA)
		_, known := deliveryAgentStatus(entry)
		if (known && !bestKnown) || (known == bestKnown && entry.TimestampISO >= bestEntry.TimestampISO) {
			best, bestEntry, bestKnown = row, entry, known
		}
	}
	return best
}

// logbotDAFillHdr 返回把 hdrmsgid 写入 otherlog 的行副本；原行不修改。
func logbotDAFillHdr(row map[string]any, hdr string) map[string]any {
	other := map[string]any{}
	for k, v := range logbotDAOtherlog(row) {
		other[k] = v
	}
	other["hdrmsgid"] = hdr
	out := map[string]any{}
	for k, v := range row {
		out[k] = v
	}
	out["otherlog"] = other
	return out
}

// mergeLogbotDARows 合并 DA 行：同一次投递、同一收件人的多条命令行折成
// 一条代表行。无分组键的行保持单行并计入返回的 unkeyed，调用方据此
// 补 limitations。代表行缺 hdrmsgid 而组有 message-id 时补回该值（同组
// 实测至多一个 hdrmsgid，是同封邮件的真实字段），让下游 message-id
// 聚合仍能把整封邮件聚成一条。
func mergeLogbotDARows(rows []map[string]any) (merged []map[string]any, unkeyed int) {
	crefToHdr := map[string]string{}
	for _, row := range rows {
		other := logbotDAOtherlog(row)
		hdr, cref := strings.TrimSpace(asString(other["hdrmsgid"])), strings.TrimSpace(asString(other["commrefid"]))
		if hdr != "" && cref != "" {
			crefToHdr[cref] = hdr
		}
	}
	type slot struct{ rows []map[string]any }
	groups := map[string]map[string]*slot{}
	var order []struct{ group, to string }
	for _, row := range rows {
		key := logbotDAGroupKey(row, crefToHdr)
		if key == "" {
			unkeyed++
			merged = append(merged, row)
			continue
		}
		to := logbotDARowTo(row)
		byTo, ok := groups[key]
		if !ok {
			byTo = map[string]*slot{}
			groups[key] = byTo
		}
		s, ok := byTo[to]
		if !ok {
			s = &slot{}
			byTo[to] = s
			order = append(order, struct{ group, to string }{key, to})
		}
		s.rows = append(s.rows, row)
	}
	for _, o := range order {
		rep := logbotDAPickRow(groups[o.group][o.to].rows)
		if hdr := logbotDAGroupHdr(o.group); hdr != "" {
			if strings.TrimSpace(asString(logbotDAOtherlog(rep)["hdrmsgid"])) == "" {
				rep = logbotDAFillHdr(rep, hdr)
			}
		}
		merged = append(merged, rep)
	}
	return merged, unkeyed
}
