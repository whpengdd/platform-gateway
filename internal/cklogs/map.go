package cklogs

import (
	"encoding/json"
	"strings"
	"time"
)

var systemFolders = map[string]string{
	"1": "收件箱",
	"2": "草稿箱",
	"3": "已发送",
	"4": "已删除",
	"5": "垃圾邮件",
	"6": "病毒邮件",
	"7": "广告邮件",
	"9": "文件中转站",
}

var cacTags = map[string]string{
	"1": "正常邮件",
	"2": "订阅邮件",
	"3": "普通垃圾邮件",
	"4": "广告",
	"5": "色情、赌博、暴力",
	"6": "谣言、反动",
	"7": "钓鱼邮件",
	"8": "CAC 封锁/拦截",
	"9": "病毒邮件",
}

var cacHighRisk = map[string]bool{"7": true, "9": true, "5": true}

var otherlogKeys = []string{
	"eval", "ultimatecause", "rulename", "senderrulename",
	"deliverreason", "delivered", "graylist", "syswhitelist",
	"dkimsigresults", "dkimverifyresult",
	"usrtodaycnt", "usrquartercnt",
}

const evalMax = 400

func toIso(v any) string {
	n, ok := asFloat(v)
	if !ok || n <= 0 {
		return ""
	}
	return time.UnixMilli(int64(n)).UTC().Format("2006-01-02T15:04:05.000Z")
}

func normalizeFolderValue(value any) string {
	if values, ok := value.([]any); ok {
		for _, item := range values {
			if normalized := normalizeFolderValue(item); normalized != "" {
				return normalized
			}
		}
		return ""
	}
	return strings.TrimSpace(asString(value))
}

func FolderName(folderID any) string {
	raw := normalizeFolderValue(folderID)
	if raw == "" {
		return ""
	}
	if name, ok := systemFolders[raw]; ok {
		return name
	}
	if len(raw) >= 6 {
		return "用户自建文件夹"
	}
	return ""
}

func cacVerdict(tag any) string {
	raw := strings.TrimSpace(asString(tag))
	if raw == "" {
		return ""
	}
	if v, ok := cacTags[raw]; ok {
		return v
	}
	return ""
}

func parseOtherlogRaw(raw any) map[string]any {
	s := strings.TrimSpace(asString(raw))
	if s == "" || s == "null" {
		return nil
	}
	var parsed any
	if err := json.Unmarshal([]byte(s), &parsed); err != nil {
		return nil
	}
	m, ok := parsed.(map[string]any)
	if !ok {
		return nil
	}
	return m
}

func parseOtherlog(raw any) map[string]string {
	parsed := parseOtherlogRaw(raw)
	if parsed == nil {
		return nil
	}
	signals := map[string]string{}
	for _, key := range otherlogKeys {
		v := strings.TrimSpace(asString(parsed[key]))
		if v == "" {
			continue
		}
		if key == "eval" && len(v) > evalMax {
			v = v[:evalMax] + "…(已截断)"
		}
		signals[key] = v
	}
	if len(signals) == 0 {
		return nil
	}
	return signals
}

func mapHitToEntry(src map[string]any) Entry {
	if src == nil {
		src = map[string]any{}
	}
	var rcpt []string
	switch v := src["rcpt"].(type) {
	case []any:
		for _, item := range v {
			s := asString(item)
			if s != "" {
				rcpt = append(rcpt, s)
			}
		}
	case []string:
		for _, item := range v {
			if item != "" {
				rcpt = append(rcpt, item)
			}
		}
	default:
		if s := asString(v); s != "" {
			rcpt = []string{s}
		}
	}
	other := parseOtherlogRaw(src["otherlog"])
	signals := parseOtherlog(src["otherlog"])
	messageID := asString(firstNonEmpty(src["messageId"], src["messageid"], src["hdrmsgid"]))
	if messageID == "" && other != nil {
		messageID = asString(other["hdrmsgid"])
	}
	cactag := asString(src["cactag"])
	return Entry{
		Source:       "mta",
		Tid:          asString(src["tid"]),
		Mid:          asString(src["mid"]),
		MessageID:    messageID,
		TimestampISO: toIso(src["timestamp"]),
		Sender:       asString(src["sender"]),
		Recipients:   rcpt,
		Subject:      asString(src["subject"]),
		Cmd:          asString(src["cmd"]),
		Result:       asString(src["result"]),
		Respond:      asString(src["respond"]),
		Errinfo:      asString(src["errinfo"]),
		ClientIP:     asString(firstNonEmpty(src["clientip"], src["ip"])),
		Score:        asString(src["score"]),
		Commresult:   asString(src["commresult"]),
		Spamfng:      asString(src["spamfng"]),
		Cactag:       cactag,
		CacVerdict:   cacVerdict(src["cactag"]),
		CacHighRisk:  cacHighRisk[strings.TrimSpace(cactag)],
		Datarulename: asString(src["datarulename"]),
		Cntrulename:  asString(src["cntrulename"]),
		Blackip:      asString(src["blackip"]),
		Debuginfo:    asString(src["debuginfo"]),
		Signals:      signals,
	}
}

func mapDeliveryAgentHit(src map[string]any) Entry {
	if src == nil {
		src = map[string]any{}
	}
	other := parseOtherlogRaw(src["otherlog"])
	to := asString(src["to"])
	var rcpt []string
	if to != "" {
		rcpt = []string{to}
	}
	cmd := asString(src["cmd"])
	bounce := ""
	if strings.ToLower(cmd) == "bounce" {
		bounce = asString(src["desc"])
	}
	result := src["result"]
	if result == nil {
		result = src["resultmessage"]
	}
	messageID := ""
	if other != nil {
		messageID = asString(other["hdrmsgid"])
	}
	return Entry{
		Source:       "da",
		Tid:          asString(src["tid"]),
		TimestampISO: toIso(src["timestamp"]),
		Sender:       asString(src["mailfrom"]),
		Recipients:   rcpt,
		Subject:      asString(src["subject"]),
		Cmd:          cmd,
		Result:       asString(result),
		Errinfo:      asString(firstNonEmpty(src["errinfo"], src["desc"])),
		BounceReason: bounce,
		Commresult:   asString(src["commresult"]),
		FolderID:     normalizeFolderValue(src["folderid"]),
		FolderName:   FolderName(src["folderid"]),
		Channel:      strings.ToLower(strings.TrimSpace(cmd)),
		Proxy:        asString(src["proxy"]),
		Mid:          asString(src["mid"]),
		MessageID:    messageID,
	}
}

func mapDeliveryPipelineHit(src map[string]any) Entry {
	if src == nil {
		src = map[string]any{}
	}
	other := parseOtherlogRaw(src["otherlog"])
	to := asString(src["to"])
	var rcpt []string
	if to != "" {
		rcpt = []string{to}
	}
	proxy := asString(src["proxy"])
	if proxy == "" && other != nil {
		proxy = asString(firstNonEmpty(other["sdnchnid"], other["sdnchnname"]))
	}
	return Entry{
		Source:       "da_pipeline",
		Tid:          asString(src["mailtid"]),
		TimestampISO: toIso(src["timestamp"]),
		Sender:       asString(firstNonEmpty(src["from"], src["mailfrom"])),
		Recipients:   rcpt,
		Subject:      asString(src["subject"]),
		Channel:      asString(src["channel"]),
		State:        asString(src["state"]),
		Result:       asString(firstNonEmpty(src["result"], src["resultmessage"])),
		Errinfo:      asString(firstNonEmpty(src["errinfo"], src["desc"])),
		Proxy:        proxy,
	}
}

func mapAuthHitToEntry(src map[string]any, ds *Dataset) AuthEntry {
	if src == nil {
		src = map[string]any{}
	}
	other := parseOtherlogRaw(src["otherlog"])
	protocol := ""
	if ds != nil {
		protocol = ds.Protocol
	}
	okRaw := false
	failReason := ""
	if protocol == "smtp" {
		okRaw = strings.ToLower(asString(src["result"])) != "failed"
		failReason = asString(firstNonEmpty(src["errinfo"], src["debuginfo"]))
	} else {
		okRaw = asString(src["loginok"]) == "1"
		if other != nil {
			failReason = asString(other["info"])
		}
	}
	account := asString(firstNonEmpty(src["loginname"], src["authuser"], src["user"]))
	if account == "" && other != nil {
		account = asString(other["uid"])
	}
	deviceID := ""
	riskIP := asString(src["riskip"])
	if other != nil {
		if deviceID == "" {
			deviceID = asString(other["deviceid"])
		}
		if riskIP == "" {
			riskIP = asString(other["riskip"])
		}
	}
	return AuthEntry{
		Protocol:     protocol,
		TimestampISO: toIso(src["timestamp"]),
		Account:      account,
		LoginOK:      okRaw,
		FailReason:   failReason,
		IP:           asString(firstNonEmpty(src["ip"], src["clientip"], src["remote"])),
		AuthType:     asString(src["authtype"]),
		RiskIP:       riskIP,
		AuthFailCnt:  asString(src["authfailcnt"]),
		DeviceID:     deviceID,
		Domain:       asString(src["domain"]),
	}
}

func mapTraceHit(src map[string]any, source string) TraceEvent {
	if src == nil {
		src = map[string]any{}
	}
	other := parseOtherlogRaw(src["otherlog"])
	base := TraceEvent{
		Source:       source,
		TimestampISO: toIso(src["timestamp"]),
		DelayMs:      asString(firstNonEmpty(src["delay"], src["delaytime"])),
	}
	switch source {
	case "mta":
		var rcpt []string
		if arr, ok := src["rcpt"].([]any); ok {
			for _, item := range arr {
				s := asString(item)
				if s != "" {
					rcpt = append(rcpt, s)
				}
			}
		}
		base.Stage = asString(src["cmd"])
		base.Result = asString(src["result"])
		base.RemoteResponse = asString(src["respond"])
		base.Errinfo = asString(src["errinfo"])
		base.Sender = asString(src["sender"])
		base.Recipients = rcpt
		base.SpamVerdict = asString(src["commresult"])
		return base
	case "pipeline":
		to := asString(src["to"])
		var rcpt []string
		if to != "" {
			rcpt = []string{to}
		}
		base.Stage = asString(src["channel"])
		base.State = asString(src["state"])
		base.Result = asString(firstNonEmpty(src["result"], src["resultmessage"]))
		base.Recipients = rcpt
		return base
	default:
		to := asString(src["to"])
		var rcpt []string
		if to != "" {
			rcpt = []string{to}
		}
		bounce := ""
		if asString(src["cmd"]) == "bounce" {
			bounce = asString(src["desc"])
		}
		deliverReason := ""
		if other != nil {
			deliverReason = asString(other["deliverreason"])
		}
		base.Stage = asString(src["cmd"])
		base.Result = asString(src["result"])
		base.FolderID = normalizeFolderValue(src["folderid"])
		base.FolderName = FolderName(src["folderid"])
		base.DeliverReason = deliverReason
		base.BounceReason = bounce
		base.Desc = asString(src["desc"])
		base.Sender = asString(src["mailfrom"])
		base.Recipients = rcpt
		base.RuleName = asString(src["sdnrulename"])
		base.WhiteType = asString(src["whitetype"])
		base.Mid = asString(src["mid"])
		return base
	}
}

func firstNonEmpty(values ...any) any {
	for _, v := range values {
		if asString(v) != "" {
			return v
		}
	}
	return nil
}

func sourceMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}
