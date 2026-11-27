package cklogs

const HardSize = 500

type TimeRange struct {
	FromMs int64
	ToMs   int64
}

type Filters struct {
	Sender    string
	Recipient string
	Domain    string
	// Direction 仅被 LogBot 后端用于决定域名条件落在 from_addr 还是 to_addr；
	// Kibana 后端忽略它（域名用 sender/rcpt 双侧 match_phrase）。
	Direction string
	Subject   string
	MsgID     string
	Tid       string
	Account   string
	Op        string
	ClientIP  string
	TimeRange TimeRange
}

type QueryOptions struct {
	Dataset   string
	Stage     string
	CountOnly bool
	PageSize  int
	Offset    int
	// FullSize lets delivery_agent detail requests bypass the HardSize clamp
	// for one single full-fetch page bounded by maxDeliveryAgentEntries.
	FullSize bool
}

type QueryResult struct {
	TotalExact    bool
	TotalRelation string
	HitIDs        []string
	OK            bool
	Entries       []Entry
	Total         int
	ErrorKind     string
	Message       string
	// UpstreamTotal/FetchedRows/RowsIncomplete 仅 LogBot detail 链路填写：
	// 前两者是上游 total_count 与实际取回原始行数的跨窗口合计（含重复行口径，
	// 合计值只做展示不做完整性判断）；RowsIncomplete 按单个 job 逐段判定后 OR 累积，
	// 避免跨窗正负差额相抵。Kibana 后端从不设置，行为不变。
	UpstreamTotal  int
	FetchedRows    int
	RowsIncomplete bool
	// 以下字段由 LogBot 后端按来源状态（limit/partial/failed/expired/skipped_future）
	// 填写，用于诚实上报不完整结果；Kibana 后端从不设置，行为不变。
	Truncated   bool     // 结果不完整：来源达到展示上限、快照截断或部分命中行未取回
	Partial     bool     // 来源仍有时段未扫描完成
	Limitations []string // 需要透传给调用方的不完整说明
}

type TraceResult struct {
	OK              bool
	Timeline        []TraceEvent
	PartialFailures []PartialFailure
	ErrorKind       string
	Message         string
}

type PartialFailure struct {
	Source  string `json:"source,omitempty"`
	Index   string `json:"index,omitempty"`
	Message string `json:"message,omitempty"`
}

type Entry struct {
	Source         string            `json:"source,omitempty"`
	Tid            string            `json:"tid,omitempty"`
	Mid            string            `json:"mid,omitempty"`
	MessageID      string            `json:"messageId,omitempty"`
	TimestampISO   string            `json:"timestampIso,omitempty"`
	Sender         string            `json:"sender,omitempty"`
	Recipients     []string          `json:"recipients,omitempty"`
	Subject        string            `json:"subject,omitempty"`
	Cmd            string            `json:"cmd,omitempty"`
	Result         string            `json:"result,omitempty"`
	Respond        string            `json:"respond,omitempty"`
	Errinfo        string            `json:"errinfo,omitempty"`
	ClientIP       string            `json:"clientip,omitempty"`
	Score          string            `json:"score,omitempty"`
	Commresult     string            `json:"commresult,omitempty"`
	Spamfng        string            `json:"spamfng,omitempty"`
	Cactag         string            `json:"cactag,omitempty"`
	CacVerdict     string            `json:"cacVerdict,omitempty"`
	CacHighRisk    bool              `json:"cacHighRisk,omitempty"`
	Datarulename   string            `json:"datarulename,omitempty"`
	Cntrulename    string            `json:"cntrulename,omitempty"`
	Blackip        string            `json:"blackip,omitempty"`
	Debuginfo      string            `json:"debuginfo,omitempty"`
	Signals        map[string]string `json:"signals,omitempty"`
	BounceReason   string            `json:"bounceReason,omitempty"`
	FolderID       string            `json:"folderId,omitempty"`
	FolderName     string            `json:"folderName,omitempty"`
	Channel        string            `json:"channel,omitempty"`
	Proxy          string            `json:"proxy,omitempty"`
	Peer           string            `json:"peer,omitempty"`
	StatusSource   string            `json:"statusSource,omitempty"`
	DeliveryStatus string            `json:"deliveryStatus,omitempty"`
	StatusText     string            `json:"statusText,omitempty"`
	FailureReason  string            `json:"failureReason,omitempty"`
	State          string            `json:"state,omitempty"`
}

type AuthEntry struct {
	Protocol     string `json:"protocol,omitempty"`
	TimestampISO string `json:"timestampIso,omitempty"`
	Account      string `json:"account,omitempty"`
	LoginOK      bool   `json:"loginOk"`
	FailReason   string `json:"failReason,omitempty"`
	IP           string `json:"ip,omitempty"`
	AuthType     string `json:"authType,omitempty"`
	RiskIP       string `json:"riskIp,omitempty"`
	AuthFailCnt  string `json:"authFailCnt,omitempty"`
	DeviceID     string `json:"deviceId,omitempty"`
	Domain       string `json:"domain,omitempty"`
}

type TraceEvent struct {
	Source         string   `json:"source,omitempty"`
	TimestampISO   string   `json:"timestampIso,omitempty"`
	DelayMs        string   `json:"delayMs,omitempty"`
	Stage          string   `json:"stage,omitempty"`
	Result         string   `json:"result,omitempty"`
	RemoteResponse string   `json:"remoteResponse,omitempty"`
	Errinfo        string   `json:"errinfo,omitempty"`
	Sender         string   `json:"sender,omitempty"`
	Recipients     []string `json:"recipients,omitempty"`
	SpamVerdict    string   `json:"spamVerdict,omitempty"`
	State          string   `json:"state,omitempty"`
	FolderID       string   `json:"folderId,omitempty"`
	FolderName     string   `json:"folderName,omitempty"`
	DeliverReason  string   `json:"deliverReason,omitempty"`
	BounceReason   string   `json:"bounceReason,omitempty"`
	Desc           string   `json:"desc,omitempty"`
	RuleName       string   `json:"ruleName,omitempty"`
	WhiteType      string   `json:"whiteType,omitempty"`
	Mid            string   `json:"mid,omitempty"`
}

type DeliveryQuery struct {
	Direction       string
	Sender          string
	Recipient       string
	SenderDomain    string
	RecipientDomain string
	Domain          string
	Subject         string
	MsgID           string
	TimeRange       TimeRange
	Page            int
	PageSize        int
	CountOnly       bool
}

type DeliveryResult struct {
	Status      string   `json:"status"`
	Provider    string   `json:"provider"`
	Code        string   `json:"code,omitempty"`
	Total       *int     `json:"total,omitempty"`
	Entries     []Entry  `json:"entries"`
	Limitations []string `json:"limitations"`
	Truncated   *bool    `json:"truncated,omitempty"`
	CountOnly   *bool    `json:"countOnly,omitempty"`
	Page        int      `json:"page,omitempty"`
	PageSize    int      `json:"pageSize,omitempty"`
	HasMore     *bool    `json:"hasMore,omitempty"`
}

type AccountQuery struct {
	Dataset   string
	Account   string
	ClientIP  string
	Op        string
	TimeRange TimeRange
	CountOnly bool
}

type AccountResult struct {
	Status      string      `json:"status"`
	Provider    string      `json:"provider"`
	Dataset     string      `json:"dataset,omitempty"`
	Protocol    string      `json:"protocol,omitempty"`
	Code        string      `json:"code,omitempty"`
	Total       *int        `json:"total,omitempty"`
	Entries     []AuthEntry `json:"entries"`
	Limitations []string    `json:"limitations"`
	CountOnly   *bool       `json:"countOnly,omitempty"`
}

type LoginResult struct {
	Account string          `json:"account"`
	Results []AccountResult `json:"results"`
}

type MessageQuery struct {
	Tid       string
	TimeRange TimeRange
}

type MessageResult struct {
	Status              string       `json:"status"`
	Provider            string       `json:"provider"`
	Code                string       `json:"code,omitempty"`
	Tid                 string       `json:"tid,omitempty"`
	Timeline            []TraceEvent `json:"timeline"`
	DeliveredFolderID   string       `json:"deliveredFolderId,omitempty"`
	DeliveredFolderName string       `json:"deliveredFolderName,omitempty"`
	DeliverReason       string       `json:"deliverReason,omitempty"`
	BounceReason        string       `json:"bounceReason,omitempty"`
	PartialFailures     []string     `json:"partialFailures"`
	Limitations         []string     `json:"limitations"`
}

func ptrInt(v int) *int    { return &v }
func ptrBool(v bool) *bool { return &v }
