package cklogs

// recipientCoverageUnresolved deliberately returns no total: an exact zero
// cannot establish absence when the upstream may store compound addresses.
// Never recover by dropping the recipient (the self-service inbound account).
func recipientCoverageUnresolved(source, code string) DeliveryResult {
	return DeliveryResult{
		Status: "error", Provider: "log_platform", Code: code,
		Entries:     []Entry{},
		Limitations: []string{source + " 收件人精确查询未命中，当前无法完整核对可能存在的多收件人记录；不能据此断言无记录。请缩小时间范围、增加对端完整邮箱，或联系管理员核查。"},
		CountOnly:   ptrBool(false), HasMore: ptrBool(false),
	}
}
