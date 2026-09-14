package cklogs

import (
	"regexp"
	"strings"
)

var (
	daMainCommands   = map[string]bool{"local": true, "remote": true, "dummy": true}
	daSuccessResults = map[string]bool{"0": true, "6": true, "7": true, "8": true, "10": true, "11": true}
	daFailedResults  = map[string]bool{"2": true, "3": true, "4": true, "5": true, "12": true}
)

var bouncePatterns = []struct {
	re   *regexp.Regexp
	text string
}{
	{regexp.MustCompile(`(?:user unknown|user not found|mailbox not found|recipient.*not found|unknown recipient)`), "收件人地址不存在，请核对后重试"},
	{regexp.MustCompile(`(?:mailbox|inbox).*(?:full|storage|quota)|quota exceeded`), "收件人邮箱空间已满"},
	{regexp.MustCompile(`(?:domain not found|dns.*(?:query error|cannot resolve|can not resolve|invalid ip))`), "对方域名无法正常解析"},
	{regexp.MustCompile(`(?:smtp connect error|connection (?:timed out|refused))`), "暂时无法连接对方邮件服务器"},
	{regexp.MustCompile(`(?:message size too large|message too big)`), "邮件大小超过对方服务器限制"},
	{regexp.MustCompile(`(?:user suspended|account suspended)`), "收件账号已暂停使用"},
	{regexp.MustCompile(`(?:user reject|recipient.*reject|not allowed to send)`), "收件人拒绝接收此邮件"},
	{regexp.MustCompile(`(?:blacklist|black list)`), "邮件被对方的黑名单策略拒收"},
	{regexp.MustCompile(`(?:virus found|virus detected)`), "邮件被检测出病毒，未能投递"},
	{regexp.MustCompile(`(?:queued? timeout|retry timeout)`), "邮件多次重试后仍未投递成功"},
	{regexp.MustCompile(`(?:system reject|policy reject|spam)`), "邮件被对方服务器的安全策略拒收"},
}

var (
	mtaFailedRe   = regexp.MustCompile(`(?:bounce|fail|error|reject|den(?:y|ied)|invalid|5\d\d|hard)`)
	mtaPendingRe  = regexp.MustCompile(`(?:defer|queue|pending|retry|temporary|4\d\d)`)
	mtaSOkRe      = regexp.MustCompile(`(?:^|\s)s_ok(?:\s|$)`)
	proxyRejectRe = regexp.MustCompile(`(?:\b5\d\d\b|reject|user (?:unknown|not found)|mailbox unavailable|access denied|policy reject|permanent(?:ly)? (?:fail|den))`)
	splitRcptRe   = regexp.MustCompile(`[;,]`)
)

func isDeliveryAgentMainAction(entry Entry) bool {
	return daMainCommands[strings.ToLower(strings.TrimSpace(entry.Cmd))]
}

func deliveryAgentStatus(entry Entry) (string, bool) {
	cmd := strings.ToLower(strings.TrimSpace(entry.Cmd))
	if !daMainCommands[cmd] {
		return "", false
	}
	result := strings.TrimSpace(entry.Result)
	if daSuccessResults[result] {
		return "delivered", true
	}
	if result == "1" {
		return "pending", true
	}
	if daFailedResults[result] {
		return "failed", true
	}
	if result == "9" {
		return "ignored", true
	}
	return "unknown", true
}

func friendlyBounceReason(entry Entry) string {
	raw := strings.TrimSpace(firstString(entry.BounceReason, entry.Errinfo, entry.Result, entry.Respond))
	lower := strings.ToLower(raw)
	for _, p := range bouncePatterns {
		if p.re.MatchString(lower) {
			return p.text
		}
	}
	if raw == "" {
		return "投递失败"
	}
	return raw
}

func mtaStatus(entry Entry) string {
	raw := strings.ToLower(strings.Join(nonEmpty(entry.Result, entry.Respond, entry.Errinfo, entry.Commresult), " "))
	if mtaFailedRe.MatchString(raw) {
		return "failed"
	}
	if mtaPendingRe.MatchString(raw) {
		return "pending"
	}
	if strings.ToUpper(strings.TrimSpace(entry.Cmd)) == "DATA" && mtaSOkRe.MatchString(raw) {
		return "pending"
	}
	return "unknown"
}

func statusText(entry Entry, status string) string {
	switch status {
	case "delivered":
		if entry.Channel == "local" && entry.FolderName != "" {
			if entry.FolderID == "6" {
				return "邮件被识别为病毒邮件"
			}
			return "已投递到" + entry.FolderName
		}
		return "已投递成功"
	case "pending":
		return "投递延迟，系统正在重试"
	case "failed":
		return friendlyBounceReason(entry)
	default:
		return "状态待确认"
	}
}

func normalizedRecipients(entry Entry) []string {
	var out []string
	for _, value := range entry.Recipients {
		for _, part := range splitRcptRe.Split(value, -1) {
			part = strings.ToLower(strings.TrimSpace(part))
			if part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

func reliableMessageIDs(entry Entry) []string {
	var ids []string
	if mid := strings.ToLower(strings.TrimSpace(entry.Mid)); mid != "" {
		ids = append(ids, "mid:"+mid)
	}
	if messageID := strings.ToLower(strings.TrimSpace(entry.MessageID)); messageID != "" {
		ids = append(ids, "message-id:"+messageID)
	}
	if tid := strings.ToLower(strings.TrimSpace(entry.Tid)); tid != "" {
		ids = append(ids, "tid:"+tid)
	}
	return ids
}

func messageKey(entry Entry, fallback string) string {
	ids := reliableMessageIDs(entry)
	if len(ids) > 0 {
		return ids[0]
	}
	return fallback
}

func addressDomain(value string) string {
	address := ExtractAddr(value)
	at := strings.LastIndex(address, "@")
	if at > 0 {
		return strings.ToLower(address[at+1:])
	}
	return ""
}

func entryHasAddressDomain(entry Entry, field, domain string) bool {
	expected := strings.ToLower(strings.TrimSpace(domain))
	if expected == "" {
		return true
	}
	if field == "sender" {
		return addressDomain(entry.Sender) == expected
	}
	for _, recipient := range normalizedRecipients(entry) {
		if addressDomain(recipient) == expected {
			return true
		}
	}
	return false
}

type derivedQuery struct {
	Direction       string
	Sender          string
	Recipient       string
	SenderDomain    string
	RecipientDomain string
	Domain          string
	Subject         string
	MsgID           string
}

func deriveQuery(in DeliveryQuery) derivedQuery {
	q := derivedQuery{
		Direction:       in.Direction,
		Sender:          ExtractAddr(in.Sender),
		Recipient:       ExtractAddr(in.Recipient),
		SenderDomain:    strings.ToLower(strings.TrimSpace(in.SenderDomain)),
		RecipientDomain: strings.ToLower(strings.TrimSpace(in.RecipientDomain)),
		Domain:          strings.ToLower(strings.TrimSpace(in.Domain)),
		Subject:         in.Subject,
		MsgID:           strings.TrimSpace(in.MsgID),
	}
	return q
}

func matchesProxyConditions(entry Entry, derived derivedQuery) bool {
	withoutSubject := derived
	withoutSubject.Subject = ""
	if !matchesDeliveryConditions(entry, withoutSubject) {
		return false
	}
	return derived.Subject == "" || strings.TrimSpace(entry.Subject) == "" || strings.Contains(strings.ToLower(entry.Subject), strings.ToLower(derived.Subject))
}

func matchesDeliveryConditions(entry Entry, derived derivedQuery) bool {
	if derived.MsgID != "" && !strings.EqualFold(strings.TrimSpace(entry.Tid), strings.TrimSpace(derived.MsgID)) && !strings.EqualFold(strings.TrimSpace(entry.MessageID), strings.TrimSpace(derived.MsgID)) {
		return false
	}
	if derived.Sender != "" && ExtractAddr(entry.Sender) != ExtractAddr(derived.Sender) {
		return false
	}
	if derived.Recipient != "" {
		found := false
		want := strings.ToLower(derived.Recipient)
		for _, r := range normalizedRecipients(entry) {
			if r == want {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if derived.SenderDomain != "" && !entryHasAddressDomain(entry, "sender", derived.SenderDomain) {
		return false
	}
	if derived.RecipientDomain != "" && !entryHasAddressDomain(entry, "recipient", derived.RecipientDomain) {
		return false
	}
	if derived.Subject != "" && !strings.Contains(strings.ToLower(entry.Subject), strings.ToLower(derived.Subject)) {
		return false
	}
	return true
}

func aggregatePeerRows(entries []Entry, direction, source string) []Entry {
	if direction != "inbound" {
		direction = "outbound"
	}
	type rowKey = string
	rows := map[rowKey]Entry{}
	order := []rowKey{}
	bounceByTransaction := map[string]Entry{}
	if source == "da" {
		for _, entry := range entries {
			if strings.ToLower(strings.TrimSpace(entry.Cmd)) != "bounce" {
				continue
			}
			tid := strings.ToLower(strings.TrimSpace(entry.Tid))
			for _, recipient := range normalizedRecipients(entry) {
				key := tid + "\x00" + recipient
				current, ok := bounceByTransaction[key]
				if !ok || entry.TimestampISO >= current.TimestampISO {
					bounceByTransaction[key] = entry
				}
			}
		}
	}
	for entryIndex, entry := range entries {
		var status string
		if source == "da" {
			mapped, ok := deliveryAgentStatus(entry)
			if !ok {
				status = "unknown"
			} else {
				status = mapped
			}
		} else {
			status = mtaStatus(entry)
		}
		if status == "ignored" {
			continue
		}
		var peers []string
		if direction == "inbound" {
			if entry.Sender != "" {
				peers = []string{entry.Sender}
			}
		} else {
			peers = normalizedRecipients(entry)
		}
		for _, peer := range peers {
			normalizedPeer := strings.ToLower(strings.TrimSpace(peer))
			tid := strings.ToLower(strings.TrimSpace(entry.Tid))
			recips := normalizedRecipients(entry)
			transactionRecipient := normalizedPeer
			if len(recips) > 0 {
				transactionRecipient = recips[0]
			}
			var aggregateID string
			if source == "da" {
				if tid == "" {
					aggregateID = "tid:record:" + itoa(entryIndex)
				} else {
					aggregateID = "tid:" + tid
				}
			} else {
				aggregateID = messageKey(entry, "record:"+itoa(entryIndex))
			}
			key := aggregateID + "\x00" + normalizedPeer
			current, hasCurrent := rows[key]
			isSdn := source == "da" && entry.Proxy != ""
			currentIsSdn := current.Proxy != ""
			sameStatus := current.DeliveryStatus == status
			currentHasStatus := current.DeliveryStatus != "unknown"
			entryHasStatus := status != "unknown"
			isNewer := entry.TimestampISO >= current.TimestampISO
			replaceWithinSameValidity := hasCurrent && currentHasStatus == entryHasStatus &&
				((sameStatus && isSdn && !currentIsSdn) ||
					(!(sameStatus && currentIsSdn && !isSdn) && isNewer))
			if !hasCurrent || (entryHasStatus && !currentHasStatus) || replaceWithinSameValidity {
				reasonEntry := entry
				if status == "failed" {
					if bounce, ok := bounceByTransaction[tid+"\x00"+transactionRecipient]; ok {
						reasonEntry.BounceReason = bounce.BounceReason
						if reasonEntry.BounceReason == "" {
							reasonEntry.BounceReason = bounce.Errinfo
						}
						if bounce.Errinfo != "" {
							reasonEntry.Errinfo = bounce.Errinfo
						}
					}
				}
				var failureReason string
				if status == "failed" {
					failureReason = friendlyBounceReason(reasonEntry)
				} else if status == "pending" {
					failureReason = strings.TrimSpace(firstString(entry.Errinfo, entry.Result, entry.Respond))
				}
				out := entry
				if direction == "outbound" {
					out.Recipients = []string{peer}
				}
				out.Peer = peer
				out.StatusSource = source
				out.DeliveryStatus = status
				out.StatusText = statusText(reasonEntry, status)
				out.FailureReason = failureReason
				if !hasCurrent {
					order = append(order, key)
				}
				rows[key] = out
			}
		}
	}
	out := make([]Entry, 0, len(order))
	for _, key := range order {
		out = append(out, rows[key])
	}
	sortEntriesDesc(out)
	return out
}

func aggregateProxyRows(entries []Entry) []Entry {
	rows := map[string]Entry{}
	order := []string{}
	for i, entry := range entries {
		peer := strings.TrimSpace(entry.Sender)
		if peer == "" {
			continue
		}
		tid := strings.ToLower(strings.TrimSpace(entry.Tid))
		aggregateID := "tid:" + tid
		if tid == "" {
			aggregateID = "record:" + itoa(i)
		}
		key := aggregateID + "\x00" + strings.ToLower(peer)
		raw := strings.ToLower(strings.Join(nonEmpty(entry.Result, entry.Errinfo), " "))
		rejected := proxyRejectRe.MatchString(raw)
		current, exists := rows[key]
		currentRejected := current.DeliveryStatus == "failed"
		if exists && ((!rejected && currentRejected) || (rejected == currentRejected && entry.TimestampISO < current.TimestampISO)) {
			continue
		}
		out := entry
		out.Peer = peer
		out.StatusSource = "proxy"
		out.FailureReason = ""
		if rejected {
			out.DeliveryStatus = "failed"
			out.StatusText = "邮件已被我方入口拒绝"
			out.FailureReason = friendlyBounceReason(entry)
		} else {
			out.DeliveryStatus = "unknown"
			out.StatusText = "邮件后续处理状态异常，请联系客服进一步核实"
		}
		if !exists {
			order = append(order, key)
		}
		rows[key] = out
	}
	out := make([]Entry, 0, len(order))
	for _, key := range order {
		out = append(out, rows[key])
	}
	sortEntriesDesc(out)
	return out
}

func sortEntriesDesc(entries []Entry) {
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && entries[j-1].TimestampISO < entries[j].TimestampISO; j-- {
			entries[j-1], entries[j] = entries[j], entries[j-1]
		}
	}
}

func firstString(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func nonEmpty(values ...string) []string {
	var out []string
	for _, v := range values {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
