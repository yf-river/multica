package channel

import "encoding/json"

// 文件角色：入站/出站消息的规范化信封定义——全系统跨渠道沟通的"公共词汇表"。所有
// 适配器把自家平台的原生 payload 翻译成本文件的结构体后交给核心；核心的 router、去重、
// 身份校验、持久化**只**读这些字段。边界规则（MUL-3515 §2）：信封只放每个平台都为真
// 的字段，平台特有的东西一律进 Raw，由产生它的适配器独占读取。

// ChatType 区分"与 bot 的一对一私聊"与"多方群聊"。产品行为不同：私聊里每条消息都收；
// 群聊只收"明确点名 bot"的消息（@提及或回复 bot 的消息）。线上取值与既有
// lark_chat_session_binding.lark_chat_type 约束一致，通用 channel_* 表才能一对一回填。
type ChatType string

const (
	// ChatTypeP2P 是与 bot 的直接（点对点）对话。
	ChatTypeP2P ChatType = "p2p"
	// ChatTypeGroup 是多方群聊。
	ChatTypeGroup ChatType = "group"
)

// MsgType 是规范化、跨平台的"消息种类"。适配器把平台原生类型映射到这个小而封闭的集合；
// 平台原始类型串（飞书 "post" / "merge_forward" / "interactive"…）**不**在这里表示——
// 它留在 InboundMessage.Raw 里、只由产生它的适配器读取。核心永远只需要知道
// "文本还是媒体，是哪种媒体"。
type MsgType string

const (
	// MsgTypeText 是纯文本或富文本消息。人类可读的内容由适配器拍平进 InboundMessage.Text。
	MsgTypeText MsgType = "text"
	// MsgTypeImage 是图片附件。
	MsgTypeImage MsgType = "image"
	// MsgTypeFile 是通用文件附件。
	MsgTypeFile MsgType = "file"
	// MsgTypeAudio 是语音/音频附件。
	MsgTypeAudio MsgType = "audio"
	// MsgTypeVideo 是视频附件。
	MsgTypeVideo MsgType = "video"
	// MsgTypeUnknown 是适配器无法映射的平台类型的兜底。核心把它当作"非文本、不可执行"
	// 的消息处理。
	MsgTypeUnknown MsgType = "unknown"
)

// Source 携带一条入站消息的跨平台路由身份——这里每个字段在所有平台都为真。平台特有的
// 路由键（飞书 app_id、Slack team id）由适配器解析成安装，**不**出现在 Source 上。
type Source struct {
	// ChannelType 是消息到达的平台；等于所属 Channel 的 Type。
	ChannelType Type

	// ChatID 是平台的会话标识。一个 ChatID 经 channel_chat_session_binding 映射到一条
	// Multica chat_session。
	ChatID string

	// ChatType 区分私聊与群聊。
	ChatType ChatType

	// SenderID 是平台原生、按安装区分的用户标识（飞书 open_id、Slack user id…）。
	// 在**同一条安装内**稳定，是身份绑定表存储用的键；**不可跨安装比较**。
	SenderID string

	// SenderStableID 是平台提供的、发送者的跨安装稳定身份（飞书 union_id…），没有则为空。
	// 顺带捕获，为将来的跨安装身份合并做准备；核心把空值当作"暂不可用"。
	SenderStableID string

	// ThreadID 是消息所属的平台线程/话题（当线程适用且消息在线程里时才有值）。空表示
	// 顶层会话消息。核心会持久化它，好让解耦的出站回复能回帖进同一个话题。
	ThreadID string
}

// MediaRef 引用一个媒体附件——适配器在消息到达核心**之前**就已把它存进对象存储。
// 核心从不持有原始字节，只持有这条引用，所以信封保持小而平台中立。
type MediaRef struct {
	// Type 是规范化媒体种类（image / file / audio / video）。
	Type MsgType
	// StorageKey 定位已持久化对象在 Multica 对象存储里的位置。
	StorageKey string
	// StorageURL 是存储后端返回的对象 URL，落库在附件行上，现有的附件下载端点
	// 之后可以凭它重新打开附件。
	StorageURL string
	// Filename 是原始显示名（平台提供时）。
	Filename string
	// MimeType 是内容类型（已知时）。
	MimeType string
	// SizeBytes 是对象大小（字节）；未知时为 0。
	SizeBytes int64
	// InlinePlaceholder 是持久化消息正文里可选的精确占位标记，该附件应当把它替换成
	// 一个稳定的 Markdown 链接。为空则附件独立展示、保持既有平台行为。InlineIndex 是
	// 该标记第几次出现（0 起），这样部分媒体失败也不会把后面的附件顶错位。
	InlinePlaceholder string
	InlineIndex       int
	// InlineIDOnly makes that replacement name the attachment instead of
	// embedding it: the marker becomes itself carrying the attachment's id,
	// "[Image: 019fe1d3-…]", rather than a Markdown link to the object.
	//
	// Set it when the marker REFERS to an attachment rather than carrying
	// one — a picture the message quotes belongs to a message somebody else
	// sent, and rendering it inline would state this sender attached it. The
	// id is the point either way: it is what joins the marker to the entry
	// the agent sees in the attachment list, and what it passes to
	// `multica attachment download`.
	InlineIDOnly bool
}

// ReplyCtx 描述一条入站消息引用/回复的那条消息。入站消息不是回复时为 nil。
type ReplyCtx struct {
	// MessageID 是直接父消息的平台 id（被引用的那条消息）。
	MessageID string
	// RootID 是平台报告的线程/根锚点（有的话）。
	RootID string
}

// InboundMessage 是核心消费的唯一规范化形态。每个适配器把平台原生 payload 翻译成这个
// 结构体；核心的 router、去重、身份校验、持久化**只**读这些字段。按边界规则（MUL-3515
// §2），结构体只放"跨平台都为真"的字段；平台特有的全部进 Raw。
type InboundMessage struct {
	// EventID 是平台的投递/事件标识，MessageID 是平台的消息标识。两者一起支撑幂等层：
	// 平台重连时可能重投同一事件，去重键是 (installation, MessageID)。
	EventID   string
	MessageID string

	// Source 是路由身份（会话、发送者、线程）。
	Source Source

	// Type 是规范化的消息种类。
	Type MsgType

	// Text 是 agent 可读的内容，已由适配器拍平。Router 或适配器可能剥掉命令前缀、
	// 或把引用上下文拼进来。非文本消息里它可能为空或是个简短占位；媒体本体在 MediaRefs。
	Text string

	// CommandText 是用户消息在被剥命令前缀/做语境增强**之前**的规范化文本。共享的命令
	// 识别器读这个字段，这样被改写过的 Text 永远不会被当成第二条命令重复解析。空则
	// 回落用 Text。
	CommandText string

	// HasSelectedContext 表示发送者主动选择的引用上下文，裸控制命令携带它时仍是一条真实输入。
	HasSelectedContext bool

	// MediaRefs 是 engine.MediaResolver.ResolveMedia 的**输出**通道：它为本消息下载并
	// 上传好的对象，每个都先写意图台账行、再 PUT，上传完成才把引用挂到这里。入站消息
	// 抵达核心时恒为空——适配器不能预先填充，因为绑定只接纳自己有能力认领意图的引用。
	MediaRefs []MediaRef

	// ReplyTo 是被引用/回复的上下文；没有则为 nil。
	ReplyTo *ReplyCtx

	// AddressedToBot 是适配器对"这条**群聊**消息是否在与 bot 交互"（@提及或回复 bot
	// 消息）给出的规范化判定。对私聊无意义，核心在私聊里忽略它。它是规范化布尔值而非
	// 平台数据——推导它的平台信号（提及数组、parent id）留在 Raw。
	AddressedToBot bool

	// ForceFresh 请求核心为这条消息开一条全新 agent 会话，而不是接续上一条。
	// Router 识别共享的 /clear 文本命令；适配器也可以为自家平台的原生操作设这个标记。
	ForceFresh bool

	// SkipAgentRun 请求核心"持久化这条消息 + 创建任何引擎侧产物（/issue 命令的 issue、
	// 会话绑定行），但**之后不要触发** agent 运行"。适配器在"纯控制命令、其唯一有意义
	// 的效果就是那个产物"时设置它（企微用它处理独立 /issue 调用——否则 agent 会回一句
	// 复读命令的"我不认识这个斜杠命令"）。Feishu、Slack 目前保持既有跨平台行为
	//（/issue 也当作普通聊天轮次触发 agent），故不设此位。
	SkipAgentRun bool

	// Raw 是未动过的平台原始 payload。适配器把平台特有字段（飞书原生 msg_type /
	// parent_id / root_id / 提及数组…）塞进来，且只在自己内部读回。核心从不读 Raw——
	// 这正是边界存在的全部意义。
	Raw json.RawMessage
}

// OutboundMessage 是核心能请任意 Channel 投递的最小出站回复：一段文本进某个会话，
// 可选地进线程、或引用某条具体消息。富卡片、媒体上传、出站 webhook 刻意**不**在这里
// 建模（MUL-3515 决策 §6）——支持更丰富输出的适配器用自家类型暴露，不放这个跨平台信封。
type OutboundMessage struct {
	// ChatID 是目标会话（平台聊天 id）。
	ChatID string
	// Text 是消息正文。
	Text string
	// ThreadID 有值时把回复送进给定的平台线程/话题；空则在会话层级发送。
	ThreadID string
	// ReplyTo 有值时引用回复给定的平台消息 id。
	ReplyTo string
}

// SendResult 是 Channel.Send 的结果。
type SendResult struct {
	// MessageID 是平台给已投递消息的标识。
	MessageID string
	// MessageIDs 在"一条逻辑回复被拆成多条消息"时包含全部平台消息标识。从不分片的
	// 适配器可以留空、只返回 MessageID 保持兼容。
	MessageIDs []string
}
