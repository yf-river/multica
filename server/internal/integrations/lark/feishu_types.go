// feishu_types.go：飞书适配器侧较“原生”的入站/出站值类型定义。
package lark

import "github.com/jackc/pgx/v5/pgtype"

// 本文件存放飞书适配器较原生的入站/出站值类型。WS connector 把原始 Lark 事件
// 解码成 InboundMessage；feishuChannel 再把它转成 channel.InboundMessage 交给
// 与渠道无关的 engine.Router；出站时由 feishu resolvers / OutcomeReplier 在
// 适配器边界转回去。（原先定义在 dispatcher.go 上，MUL-3620 Router 切换时移除。）

// InboundMessage 是飞书 connector 解码并富化后的事件，也是适配器内部形态：
// feishuChannel 把它映射成 channel.InboundMessage（并把本结构塞进 Raw），这样
// resolvers 能读到规范化信封没有携带的平台特有字段。
type InboundMessage struct {
	EventType    string
	EventID      string
	AppID        string
	ChatID       ChatID
	ChatType     ChatType
	MessageID    string
	SenderOpenID OpenID
	Body         string
	// Content 是飞书在 event.message.content 里下发的、msg_type 特异的原始
	// JSON 字符串。text/post 解码会立刻消费它；媒体摄入会留着它，以便适配器
	// 在转成 channel.InboundMessage 之前提取 image_key/file_key。
	Content string
	// ForceFreshSession 把本次分发标记为一次性“全新开始”：daemon 认领由此产生
	// 的 chat task 时，应跳过对之前会话的恢复。
	ForceFreshSession bool
	AddressedToBot    bool

	// MessageType 是原始 Lark msg_type（"text"、"post"、"merge_forward"、
	// "image"、"interactive"……）。decoder 填充它，这样入站 enricher 能判断某条
	// 消息是否需要一次 HTTP 往返来展开，而 core 保持对 msg_type 无感、只读 Body。
	MessageType string

	// CreateTime 是触发消息的创建时间（epoch 毫秒）。enricher 以它作为群聊
	// “近期上下文”窗口的锚点；typing indicator 用它在加反应时跳过过期消息。
	CreateTime string

	// ParentID 是本消息引用回复的那条消息的 message_id（原样 parent_id）；
	// RootID 是话题/回复树的根锚点。enricher 以 ParentID 展开引用回复。
	ParentID string
	RootID   string

	// ThreadID 是飞书话题（话题）id，只有话题里的消息才有值，所以非空即代表
	// “话题内发生了 @”。会持久化到聊天绑定上，出站 patcher 据此把回复贴回
	// 对应话题。
	ThreadID string

	// CommandBody 是用户【自己】输入的文本（enricher 前置引用/转发上下文之前的
	// 解码 Body）。`/issue` 从它解析，而不是从富化后的 Body。
	CommandBody string
}

// Outcome 归类入站管线做出的决定。OutcomeReplier 检查它来选择飞书侧的回复。
// 取值镜像 engine.Outcome。
type Outcome string

const (
	// OutcomeDropped —— 未摄入（身份、去重、群组过滤……）。
	OutcomeDropped Outcome = "dropped"
	// OutcomeNeedsBinding —— open_id 未绑定；发送绑定卡片。
	OutcomeNeedsBinding Outcome = "needs_binding"
	// OutcomeIngested —— 消息落地且已（或将要）入队一次运行。
	OutcomeIngested Outcome = "ingested"
	// OutcomeFreshPending —— 一次裸 /clear 被持久化，留给下一个聊天轮次。
	OutcomeFreshPending Outcome = "fresh_pending"
	OutcomeChatStarted  Outcome = "chat_started"
	// OutcomeIssueUsage —— /issue 没带必需的标题就发了。
	OutcomeIssueUsage Outcome = "issue_usage"
	// OutcomeAgentOffline —— 消息落地了，但 agent 没有绑定 runtime。
	OutcomeAgentOffline Outcome = "agent_offline"
	// OutcomeAgentArchived —— 消息落地了，但 agent 已归档。
	OutcomeAgentArchived Outcome = "agent_archived"
)

// DispatchResult 是供 OutcomeReplier 消费、驱动出站回复的飞书侧判定结果。引擎
// 产出 engine.Result；飞书 OutboundReplier 适配器把它转成这个形态。
type DispatchResult struct {
	Outcome        Outcome
	DropReason     DropReason
	InstallationID pgtype.UUID
	ChatSessionID  pgtype.UUID
	SenderOpenID   OpenID
	TaskID         pgtype.UUID
	IssueID        pgtype.UUID
	IssueNumber    int32
	// IssueIdentifier 是带 workspace 限定的键（"MUL-42"），确认消息里原样使用。
	IssueIdentifier string
	// IssueTitle 是 /issue 时填的标题，会在确认消息里回显。
	IssueTitle string
	// IssueDuplicate 用于区分“命中活跃 issue 冲突”与“创建成功”，冲突时上面的
	// 现有 issue 字段仍然有效。
	IssueDuplicate bool
	// IssueUsageHadMedia 让用法提示回复告诉发送者：要用纠正后的命令重发，就得
	// 把当前这条消息里的媒体再带一次。
	IssueUsageHadMedia bool
}
