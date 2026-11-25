package cklogs

import "time"

const DefaultOpTimeout = 90 * time.Second

type Service struct {
	Backend Backend
	// Client 保留给既有测试与调用方直接调整 Kibana 客户端参数（如 Timeout）；
	// 与 Backend 指向同一实例，由 NewService 同步设置。logbot provider 下为 nil。
	Client *Client
}

// NewService 装配 Kibana provider（默认，行为与既有版本一致）。
func NewService(c *Client) *Service {
	return &Service{Backend: KibanaBackend{Client: c}, Client: c}
}

// NewLogBotService 装配 LogBot provider（cklogs.provider=logbot）。
func NewLogBotService(c *LogBotClient) *Service {
	return &Service{Backend: LogBotBackend{Client: c}}
}

func (s *Service) backend() Backend {
	if s == nil {
		return nil
	}
	if s.Backend != nil {
		return s.Backend
	}
	if s.Client != nil {
		// 兼容直接构造 Service{Client: ...} 的旧调用方。
		return KibanaBackend{Client: s.Client}
	}
	return nil
}

func (s *Service) backendName() string {
	if b := s.backend(); b != nil {
		return b.Name()
	}
	return "kibana"
}

func (s *Service) configured() bool {
	b := s.backend()
	return b != nil && b.Configured()
}

func (s *Service) OpTimeout() time.Duration {
	op := DefaultOpTimeout
	if b := s.backend(); b != nil {
		if t := b.Timeout(); t > op {
			return t
		}
	}
	return op
}

const narrowQueryAdvice = "建议先将起止时间缩小到邮件发生时间前后 1 小时；仍未完成时分段查询，并补充对端完整邮箱或邮件主题。"

func timeoutMessage() string {
	return "日志查询超时，查询尚未完成，不能据此判断没有记录。" + narrowQueryAdvice
}

func notConfiguredMessage(provider string) string {
	if provider == "logbot" {
		return "LogBot 账号未配置（cklogs.logbot.auth），未执行任何查询。"
	}
	return "日志平台凭据未配置（CK_LOGS_BASIC_USER/CK_LOGS_BASIC_PASS），未执行任何查询。"
}

func notConfiguredDeliveryFor(provider string) DeliveryResult {
	return DeliveryResult{
		Status:      "not_available",
		Provider:    "none",
		Entries:     []Entry{},
		Limitations: []string{notConfiguredMessage(provider)},
	}
}

// backendUnavailableDelivery 把后端返回的 not_available（LogBot 不支持的能力）
// 转成对外 not_available，绝不用空结果冒充无记录。
func backendUnavailableDelivery(message string) DeliveryResult {
	return DeliveryResult{
		Status:      "not_available",
		Provider:    "log_platform",
		Entries:     []Entry{},
		Limitations: []string{message},
		CountOnly:   ptrBool(false),
		HasMore:     ptrBool(false),
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
