package cklogs

import "time"

const DefaultOpTimeout = 90 * time.Second

type Service struct {
	Client *Client
}

func NewService(c *Client) *Service {
	return &Service{Client: c}
}

func (s *Service) OpTimeout() time.Duration {
	op := DefaultOpTimeout
	if s != nil && s.Client != nil {
		if t := s.Client.timeout(); t > op {
			return t
		}
	}
	return op
}

const narrowQueryAdvice = "建议先将起止时间缩小到邮件发生时间前后 1 小时；仍未完成时分段查询，并补充对端完整邮箱或邮件主题。"

func timeoutMessage() string {
	return "日志查询超时，查询尚未完成，不能据此判断没有记录。" + narrowQueryAdvice
}

func notConfiguredDelivery() DeliveryResult {
	return DeliveryResult{
		Status:      "not_available",
		Provider:    "none",
		Entries:     []Entry{},
		Limitations: []string{"日志平台凭据未配置（CK_LOGS_BASIC_USER/CK_LOGS_BASIC_PASS），未执行任何查询。"},
	}
}

func deliveryError(code, message string, extra []string) DeliveryResult {
	lim := []string{message}
	lim = append(lim, extra...)
	return DeliveryResult{
		Status:      "error",
		Provider:    "log_platform",
		Code:        code,
		Entries:     []Entry{},
		Limitations: lim,
	}
}
