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

func timeoutMessage() string {
	return "日志查询超过60秒，请缩小查询范围或增加查询条件后重试"
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
