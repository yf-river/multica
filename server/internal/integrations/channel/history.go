package channel

// 角色定位：定义"按需读历史"的渠道无关词汇表。历史是 agent 通过两条统一 CLI 命令
// 主动**拉取**的——`multica chat history`（渠道**总览**：顶层消息 + 线程元数据，不含
// 线程内容）与 `multica chat thread [id]`（读某一条线程的全部消息）。agent 永远看不到
// 平台专属 API：服务端把会话的绑定解析成渠道类型，分发给该平台的 reader，reader 返回
// 本文件的规范化结构。新增一个平台 = "实现一个 reader"，agent 面对的契约永不改变
//（MUL-3871）。

// HistoryRole 是拉取到的消息作者的规范化角色，对应 agent 已经在用的 chat_message.role
// 领域枚举。
type HistoryRole string

const (
	// HistoryRoleUser 是人类（或第三方 bot，例如告警 bot）发的消息——agent 应该读的上下文。
	HistoryRoleUser HistoryRole = "user"
	// HistoryRoleAssistant 是**这个 bot 自己**在这段对话里先前发的消息。
	HistoryRoleAssistant HistoryRole = "assistant"
)

// HistoryMessage 是规范化后的单条消息。无论哪个平台都是同样的形状，agent 读到的永远是
// 一份统一列表，就像 `multica issue comment list --output json` 一样。
type HistoryMessage struct {
	// ID 是平台侧的消息标识（Slack ts、飞书 message_id）。
	ID string `json:"id"`
	// Author 是发送者的人类可读显示标签（"Alice"、"Bot"，名字解析不出来时兜底用
	// 位置式的 "User 2"）。
	Author string `json:"author"`
	// AuthorID 是平台原生发送者 id（有的话）。某些平台/bot 消息为空。
	AuthorID string `json:"author_id,omitempty"`
	// Role 区分 bot 自己的发言与别人的发言。
	Role HistoryRole `json:"role"`
	// Text 是消息正文，已由适配器拍平为纯文本。
	Text string `json:"text"`
	// TS 是平台时间戳字符串，在同一平台内可按字典序排序（Slack "1700000000.000100"）。
	// 它兼作分页游标。
	TS string `json:"ts"`

	// 下面几个字段只在"顶着一个线程的 CHANNEL-OVERVIEW 行"上才有值——agent 需要据此
	// `multica chat thread <thread_id>` 去读线程内容。普通消息和线程读取的行上都没有。

	// ThreadID 是传给 `multica chat thread <id>` 以读取该线程消息的标识。
	// 仅当这条总览行带线程时才有。
	ThreadID string `json:"thread_id,omitempty"`
	// ReplyCount 是线程里的回复条数（没有线程时为 0/省略）。
	ReplyCount int `json:"reply_count,omitempty"`
	// LatestReply 是最新一条回复的平台时间戳（已知时）。
	LatestReply string `json:"latest_reply,omitempty"`
}

// HistoryPage 是规范化后的一个分页。消息按"最旧在前"排序，转写文本才能像聊天界面那样
// 自上而下阅读。
type HistoryPage struct {
	// ChannelType 是历史来源的平台（"slack"）。会话没绑任何渠道（纯 Web 聊天会话）时为空。
	ChannelType string `json:"channel_type,omitempty"`
	// ThreadID 在**线程读取**时有值：这批消息属于哪个线程。渠道总览时为空。
	ThreadID string `json:"thread_id,omitempty"`
	// Messages 是拉取到的消息，最旧在前。
	Messages []HistoryMessage `json:"messages"`
	// NextCursor 非空时是一个不透明游标：把它作为下一次的 Before 可以翻到**更旧的**
	// 消息。空表示没有更旧消息可翻。
	NextCursor string `json:"next_cursor,omitempty"`
}

// HistoryOptions 调节一次读取。它们平台中立；每个 reader 自己把它们映射到自家 API 的
// 分页原语上。
type HistoryOptions struct {
	// Limit 限制最多返回多少条。reader 会把它夹到自家平台单页上限内；<= 0 时套用
	// 一个合理默认值。
	Limit int
	// Before 是不透明游标（上一页的 NextCursor）；reader 只返回严格早于它的消息。
	// 空表示从最新的消息开始读。
	Before string
	// After 与 Until 是"服务端权威"的生成边界。reader 必须把窗口当作 [After, Until)；
	// 调用方无法通过 URL 设置它们。
	After           string
	Until           string
	BoundaryPending bool
	// ContextRevision 是任务那次不可变"渠道上下文"的代数。0 表示这是传统/私聊读取，
	// 没有生成代边界。
	ContextRevision int64
}
