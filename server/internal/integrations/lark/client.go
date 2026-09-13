// client.go：本包对 Lark 开放平台 HTTP API 的窄接口定义，以及默认的 stub
// 实现。
package lark

import (
	"context"
	"errors"
	"io"
	"log/slog"
)

// APIClient 是本包从 Lark 开放平台 HTTP API 需要的窄接口。刻意在这里定义
// （而不是取自已 vendored SDK），让包其余部分可以在不把 Lark 传输层拖进每个
// 测试的前提下编译与单测，也让我们能随意替换实现（真实 SDK、stub、fake）而
// 不动调用点。
//
// 所有方法都限定在单条安装内——调用方已经鉴权过安装行并解密了它的
// app_secret。client 从不自己读 `lark_installation`。
type APIClient interface {
	// IsConfigured 报告该 APIClient 能否通过网络访问 Lark。这是“HTTP 出站已
	// 接线”的信号：stub 返回 false；真实 Lark HTTP client 一旦实例化就返回
	// true。handler 在决定是否暴露需要与 Lark 通信的安装/管理 UI 时会询问它。
	IsConfigured() bool

	// SendInteractiveCard 向飞书聊天里发一张交互卡片，返回 Lark 给的
	// message_id。patcher 会把它持久化到 lark_outbound_card_message，后续 patch
	// 用同一 id 定位卡片。
	SendInteractiveCard(ctx context.Context, p SendCardParams) (string, error)

	// PatchInteractiveCard 替换先前发出的卡片内容。节流判断归调用方；本方法
	// 只做网络调用。
	PatchInteractiveCard(ctx context.Context, p PatchCardParams) error

	// SendTextMessage 向飞书聊天发一条纯文本消息。agent 的聊天回复在正文不含
	// markdown 语法（短句/确认/招呼）时走这里——纯文本气泡更像普通 IM 消息；
	// 即便加了 markdown 卡片变体也刻意保留此路径，因为把一句 "Hello!" 包进
	// 卡片只是加上用户不想要的视觉装饰。
	SendTextMessage(ctx context.Context, p SendTextParams) (string, error)

	// SendMarkdownCard 把 agent 回复以飞书交互卡片（schema 2.0）发出，卡片里是
	// 一个 `tag: "markdown"` 的正文元素。聊天回复路由器在正文含 markdown 语法
	// （围栏代码块、标题、列表、表格等）时走这条路——Lark 会把 markdown 渲染成
	// 格式化文本，而不是把裸的 `**bold**` / `# heading` 留给用户。返回卡片的
	// message_id。
	SendMarkdownCard(ctx context.Context, p SendMarkdownCardParams) (string, error)

	// SendBindingPromptCard 是专门的“你需要绑定”出站。与 SendInteractiveCard
	// 分开，让生产卡片模板变化时抽象保持稳定——身份检查处的调用点无需知道
	// Lark 的卡片 schema。
	SendBindingPromptCard(ctx context.Context, p BindingPromptParams) error

	// GetBotInfo 返回 Bot 在该安装下的 `open_id`（即我们持久化在
	// lark_installation 上的 `bot_open_id`）。唯一调用方是 RegistrationService——
	// 设备流注册返回新的 `client_id` / `client_secret` 后，服务用这些凭据铸
	// tenant_access_token 再调 /open-apis/bot/v3/info 拿 Bot 身份，结果与
	// app_id / app_secret 在同一事务里随安装者绑定一起冻结进 lark_installation。
	GetBotInfo(ctx context.Context, creds InstallationCredentials) (BotInfo, error)

	// GetMessage 按 id 经 GET /open-apis/im/v1/messages/{message_id} 拉一条消息。
	// Lark 恒返回数组（data.items[]）：普通消息正好一个元素；`merge_forward`
	// 消息的第一个元素是转发哨兵，其余是被打包的子消息（每个都是普通类型化
	// 消息，用 upper_message_id 回指）。入站 enricher 两种形态都用：items[0]
	// 取引用回复的父消息，items[1:] 取转发记录。返回原始切片让本方法保持轻薄
	// 的传输适配器——压平与块组装是 enricher 的活。
	GetMessage(ctx context.Context, creds InstallationCredentials, messageID string) ([]LarkMessage, error)

	// ListChatMessages 经 GET /open-apis/im/v1/messages 拉取单个聊天里最近的
	// 消息。它为群聊上下文预取供能：用户在繁忙群里 @ Bot 时，enricher 拉一个
	// 有界的周边消息窗口，让 agent 看到整段对话而不只是被 @ 的那一行。
	//
	// p.ThreadID 非空时请求经 container_id_type=thread 限定到单个飞书话题
	// （话题），所以话题内的 @ 只会看到该话题的消息——同群里的兄弟话题共享一个
	// chat_id 但必须互相隔离（#5835）。ThreadID 为空则保持聊天级
	// container_id_type=chat 窗口。
	//
	// 结果按最新在前返回（sort_type=ByCreateTimeDesc），最多 p.PageSize 条
	// （Lark 硬顶每页 50）；顺序与截断由调用方处理。只拉一页——刻意不暴露分页，
	// 让入站 ACK 路径的 HTTP 扇出保持单次往返。与 GetMessage 一样，本方法是
	// 轻薄传输适配器：压平与块组装归 enricher。
	ListChatMessages(ctx context.Context, creds InstallationCredentials, p ListMessagesParams) ([]LarkMessage, error)

	// DownloadMessageResource 经 GET /open-apis/im/v1/messages/{message_id}/
	// resources/{file_key} 下载消息附带的一个二进制资源。Type 是开放平台资源
	// 类别（image_key 用 "image"；file_key 支撑的视频/文件/音频用 "file"）。
	DownloadMessageResource(ctx context.Context, creds InstallationCredentials, p DownloadResourceParams) (DownloadedResource, error)

	// BatchGetUsers 经 GET /open-apis/contact/v3/users/batch 把一组用户 open_id
	// 解析成显示名。enricher 用它给近期上下文/引用/转发的说话者（以及 @ Bot 的
	// 发送者）贴真实名字，而不是 "User 1 / User 2" 的位置标签。返回 open_id ->
	// name 映射；API 没返回的 id（联系人范围受限、已停用用户等）干脆不在映射里，
	// 调用方退回位置标签。超过 Lark 单次 50 上限的 openIDs 由 client 丢弃。
	BatchGetUsers(ctx context.Context, creds InstallationCredentials, openIDs []string) (map[string]string, error)

	// AddMessageReaction 给既有消息加一个 emoji 反应。标准用途是“Typing”指示，
	// 表示 Bot 正在处理用户消息。返回 Lark 分配的 reaction_id 以便稍后移除。
	AddMessageReaction(ctx context.Context, p AddReactionParams) (string, error)

	// DeleteMessageReaction 移除先前加在消息上的反应。这是 typing indicator
	// 生命周期的清理半边。
	DeleteMessageReaction(ctx context.Context, p DeleteReactionParams) error
}

// TokenCacheInvalidator 由“进程内缓存 tenant_access_token”的 APIClient 实现，
// 让凭据轮换能叫它忘掉持有的缓存。否则轮换对它不可见：重注册一个 Bot 会在
// 相同 app_id 下发新 app_secret，Lark 会撤销所有旧 secret 铸造的 token，而缓存
// 键不变。
//
// 刻意与 APIClient 分开而不是做成它的方法：只有真实 HTTP client 才有缓存，
// stub / fake 没什么可忘的。调用方做类型断言，缺失时跳过。
type TokenCacheInvalidator interface {
	InvalidateTokenCache(appID string)
}

// ListMessagesParams 为群聊上下文预选取一个有界、最近的单聊消息窗口。只暴露
// enricher 今天需要的字段（ChatID、ThreadID、PageSize、EndTime）；start_time
// 与 page_token 在出现需要它们的调用方之前刻意省略。
type ListMessagesParams struct {
	ChatID ChatID
	// ThreadID 非空时把列表限定到单个飞书话题（话题）：client 发
	// container_id_type=thread，以 thread id 作 container_id 而非聊天容器。这样
	// 话题内的 @ 永远不会看到兄弟话题的消息（#5835）。飞书线程容器不接收
	// end_time，所以此路径下 EndTime 被忽略——调用方在客户端侧锚定窗口。空值
	// 保持聊天级容器。
	ThreadID string
	// PageSize 是要拉多少条最近消息。client 会把它夹进 Lark 合法的 1..50 区间。
	PageSize int
	// EndTime 大于 0 时，把窗口上限卡在“创建时间不晚于此 Unix 秒数”的消息上
	// （Lark 的 end_time 是秒级而非毫秒级）。enricher 把它设为触发消息的时间，
	// 让预取锚定在被 @ 的那一刻，而不是拉取执行时的最新。ThreadID 已设置时忽略
	// （线程容器拒绝 end_time）。
	EndTime int64
}

// DownloadResourceParams 是一次资源下载所需的参数。
type DownloadResourceParams struct {
	MessageID string
	FileKey   string
	Type      string
}

// DownloadedResource 是一次已完成的资源下载结果。
type DownloadedResource struct {
	Data        []byte
	ContentType string
	Filename    string
	SizeBytes   int64
}

// DownloadedResourceStream 是一次流式资源下载结果（Body 需由调用方关闭）。
type DownloadedResourceStream struct {
	Body        io.ReadCloser
	ContentType string
	Filename    string
	SizeBytes   int64
}

// LarkMessage 是 enricher 需要的 IM v1 消息条目的规范化切片。Body.content 原样
// 透传（仍是飞书二次 JSON 编码、msg_type 特异的字符串），由压平器而非传输
// client 负责内容解释。
type LarkMessage struct {
	MessageID      string
	MessageType    string // Lark `msg_type`：text / post / image / merge_forward / …
	Content        string // 原始 body.content（一条 JSON 编码字符串）
	SenderID       string // sender.id（用户为 open_id，应用为 app_id）
	SenderType     string // sender.sender_type：user / app / anonymous / …
	CreateTime     string // epoch 毫秒，按 Lark 返回的原样字符串
	ParentID       string
	RootID         string
	ThreadID       string // Lark 话题（话题）id；话题外的消息为空
	UpperMessageID string // 子消息挂在哪个 merge_forward 父转发之下
	Deleted        bool
	Mentions       []LarkMessageMention
}

// LarkMessageMention 镜像 IM REST item 形态里 mentions[] 的一项。注意这与 WS
// receive 事件的 mention 形态不同：这里的 `id` 是裸 open_id 字符串，而不是
// 嵌套的 {open_id, union_id, user_id} 对象。
type LarkMessageMention struct {
	Key  string // 例如 "@_user_1"
	ID   string // open_id
	Name string // 显示名（可能为空）
}

// BotInfo 是我们关心的 /open-apis/bot/v3/info（外加一次取 union_id 的
// /open-apis/contact/v3/users 查询）切片：Bot 的安装级 `open_id` 与其稳定的
// `union_id`。
//
// 两个标识都持久化在 lark_installation 上：
//
//   - `open_id` 是 per-app 的 Lark 标识；/bot/v3/info 返回它，出站发送路径用它
//     给用户寻址。
//
//   - `union_id` 是限定在 Lark tenant 内的跨 app 稳定标识。它是多 Bot 群聊里
//     两个 WS 视角唯一一致的字段——见 MUL-2671 群聊 @ 分流。decoder 拿入站
//     `mentions[].id` 与 `union_id` 比对，好让群里有多个 Bot 绑定同一个群时，
//     正确那个 Bot 的 supervisor 处理事件。
//
// /bot/v3/info 返回的其它一切（显示名、头像、activate_status、ip_white_list）
// 都被刻意丢弃——下游可在需要时凭 bot_open_id 重取，把它们冻结进我们的
// schema 会在操作者每次改 Bot 时制造一处漂移面。
type BotInfo struct {
	OpenID  OpenID
	UnionID string
}

// SendCardParams 是发一张全新卡片的输入形态。
type SendCardParams struct {
	InstallationID InstallationCredentials
	ChatID         ChatID
	// CardJSON 是原始飞书交互卡片 JSON 体。原样透传，让卡片模板包可以不拖着
	// 这个传输接口演进。
	CardJSON string
	// ReplyTarget 设置后，发送走 Lark 的回复接口（POST /im/v1/messages/{id}/
	// reply）而非聊天级发送接口，卡片因此落在原始 话题（线程）里。空
	// ReplyTarget 保持旧的聊天级发送。
	ReplyTarget ReplyTarget
}

// ReplyTarget 描述出站消息应如何贴回一条入站消息。MessageID 非空时传输层走
// Lark 指向该消息的回复接口；InThread 映射到 reply_in_thread 标志，让回复留在
// 该消息的话题内。零值（空 MessageID）表示“在聊天级发送”——历史行为——所以
// 不在乎话题的调用方直接不设即可。
type ReplyTarget struct {
	MessageID string
	InThread  bool
}

// IsSet 报告该目标是否应走回复接口。回复需要父 message_id；没有它就没有可回
// 复的对象，调用方退回聊天级发送。
func (r ReplyTarget) IsSet() bool { return r.MessageID != "" }

// PatchCardParams 是更新既有卡片的输入形态。
type PatchCardParams struct {
	InstallationID    InstallationCredentials
	LarkCardMessageID string
	CardJSON          string
}

// SendTextParams 是发一条纯文本消息的输入形态。Text 原样发给 Lark；client 负责
// 拼出 Lark 要求的 `{"text": "..."}` 内容信封并做 JSON 编码。
type SendTextParams struct {
	InstallationID InstallationCredentials
	ChatID         ChatID
	Text           string
	// ReplyTarget 把文本回复贴回飞书话题；见 ReplyTarget。空值保持聊天级发送。
	ReplyTarget ReplyTarget
}

// SendMarkdownCardParams 是“把 agent 回复发成带 markdown 正文元素的飞书交互
// 卡片”的输入形态。Markdown 原样交给 Lark；client 在它外面套 schema-2.0 卡片
// 信封。
type SendMarkdownCardParams struct {
	InstallationID InstallationCredentials
	ChatID         ChatID
	// Markdown 是正文。Lark schema-2.0 markdown 支持类 GFM：**加粗**、*斜体*、
	// `行内代码`、围栏代码块、标题、有序+无序列表、链接、表格、引用块、分隔线。
	Markdown string
	// Summary 非空时作为 Lark 在聊天列表/桌面通知里显示的单行预览。空值退回
	// Lark 从正文自行推导的内容。
	Summary string
	// ReplyTarget 把卡片回复贴回飞书话题；见 ReplyTarget。空值保持聊天级发送。
	ReplyTarget ReplyTarget
}

// BindingPromptParams 携带渲染并发送成员绑定引导卡片（单个 CTA：打开绑定
// URL）所需的数据。
type BindingPromptParams struct {
	InstallationID InstallationCredentials
	OpenID         OpenID
	// BindURL 是用户点击的绝对 URL。令牌由调用方嵌进 URL；client 从不看到它。
	BindURL string
}

// AddReactionParams 是给消息加 emoji 反应的输入形态。
type AddReactionParams struct {
	InstallationID InstallationCredentials
	MessageID      string
	EmojiType      string
}

// DeleteReactionParams 是移除先前加在消息上的反应的输入形态。
type DeleteReactionParams struct {
	InstallationID InstallationCredentials
	MessageID      string
	ReactionID     string
}

// InstallationCredentials 是 client 代表某 workspace 的 bot 向 Lark 鉴权所需
// 的安装级传输上下文。把这些显式传给每次调用（而不是为每个安装构造 client）
// 让生命周期简单：hub 解密一次 app_secret，之后所有出站调用复用该结构体。
//
// 明文 app_secret 只在一次调用在途时存在于该结构体内；调用方绝不能记录或
// 持久化它。
type InstallationCredentials struct {
	AppID     string
	AppSecret string
	TenantKey string
	// Region 为用这些凭据的每次调用选择 Lark 开放平台主机（飞书大陆版 vs
	// Lark 国际版）。空默认飞书。凭据构建点从 lark_installation.region 复制它；
	// 设备流安装者从自动探测的 tenant 设置它。这正是让一个部署同时服务两朵云
	// 的东西——见 http_client.go resolveBaseURL 与 ws_endpoint.go Endpoint。
	Region Region
}

// ErrAPIClientNotConfigured 由 stub client 返回，表示还没接上真实 Lark client。
// 调用点应把这种情况当作自托管部署（无 Lark app）的预期条件——记警告、退回
// “Lark 集成未配置”，并继续服务其它 workspace 功能。
var ErrAPIClientNotConfigured = errors.New("lark: API client not configured")

// stubAPIClient 是未注册生产 client 时的默认 APIClient。它用
// ErrAPIClientNotConfigured 拒绝一切传输调用，让配错的环境大声失败，而不是
// 悄悄丢掉卡片或设备流注册响应。
//
// 刻意不静默成功：返回 "" message id 的 stub 会让入站 dispatcher 记下一堆指向
// 虚无的 lark_outbound_card_message 行。
type stubAPIClient struct {
	log *slog.Logger
}

// NewStubAPIClient 返回默认的 no-op APIClient。未提供真实实现时 hub 会构造
// 一个，使依赖 APIClient 的子系统（出站 patcher、设备流注册）仍可接线；它们的
// 第一次调用会冒出清晰错误。
func NewStubAPIClient(log *slog.Logger) APIClient {
	if log == nil {
		log = slog.Default()
	}
	return &stubAPIClient{log: log}
}

// IsConfigured 实现 APIClient：stub 恒为 false。
func (s *stubAPIClient) IsConfigured() bool { return false }

// SendInteractiveCard 实现 APIClient（stub 恒报未配置）。
func (s *stubAPIClient) SendInteractiveCard(ctx context.Context, p SendCardParams) (string, error) {
	s.log.Warn("lark stub client: SendInteractiveCard called", "chat_id", string(p.ChatID))
	return "", ErrAPIClientNotConfigured
}

// PatchInteractiveCard 实现 APIClient（stub 恒报未配置）。
func (s *stubAPIClient) PatchInteractiveCard(ctx context.Context, p PatchCardParams) error {
	s.log.Warn("lark stub client: PatchInteractiveCard called", "card_message_id", p.LarkCardMessageID)
	return ErrAPIClientNotConfigured
}

// SendTextMessage 实现 APIClient（stub 恒报未配置）。
func (s *stubAPIClient) SendTextMessage(ctx context.Context, p SendTextParams) (string, error) {
	s.log.Warn("lark stub client: SendTextMessage called", "chat_id", string(p.ChatID))
	return "", ErrAPIClientNotConfigured
}

// SendMarkdownCard 实现 APIClient（stub 恒报未配置）。
func (s *stubAPIClient) SendMarkdownCard(ctx context.Context, p SendMarkdownCardParams) (string, error) {
	s.log.Warn("lark stub client: SendMarkdownCard called", "chat_id", string(p.ChatID))
	return "", ErrAPIClientNotConfigured
}

// SendBindingPromptCard 实现 APIClient（stub 恒报未配置）。
func (s *stubAPIClient) SendBindingPromptCard(ctx context.Context, p BindingPromptParams) error {
	s.log.Warn("lark stub client: SendBindingPromptCard called", "open_id", string(p.OpenID))
	return ErrAPIClientNotConfigured
}

// GetBotInfo 实现 APIClient（stub 恒报未配置）。
func (s *stubAPIClient) GetBotInfo(ctx context.Context, creds InstallationCredentials) (BotInfo, error) {
	s.log.Warn("lark stub client: GetBotInfo called", "app_id", creds.AppID)
	return BotInfo{}, ErrAPIClientNotConfigured
}

// GetMessage 实现 APIClient（stub 恒报未配置）。
func (s *stubAPIClient) GetMessage(ctx context.Context, creds InstallationCredentials, messageID string) ([]LarkMessage, error) {
	s.log.Warn("lark stub client: GetMessage called", "message_id", messageID)
	return nil, ErrAPIClientNotConfigured
}

// ListChatMessages 实现 APIClient（stub 恒报未配置）。
func (s *stubAPIClient) ListChatMessages(ctx context.Context, creds InstallationCredentials, p ListMessagesParams) ([]LarkMessage, error) {
	s.log.Warn("lark stub client: ListChatMessages called", "chat_id", string(p.ChatID))
	return nil, ErrAPIClientNotConfigured
}

// DownloadMessageResource 实现 APIClient（stub 恒报未配置）。
func (s *stubAPIClient) DownloadMessageResource(ctx context.Context, creds InstallationCredentials, p DownloadResourceParams) (DownloadedResource, error) {
	s.log.Warn("lark stub client: DownloadMessageResource called", "message_id", p.MessageID)
	return DownloadedResource{}, ErrAPIClientNotConfigured
}

// BatchGetUsers 实现 APIClient（stub 恒报未配置）。
func (s *stubAPIClient) BatchGetUsers(ctx context.Context, creds InstallationCredentials, openIDs []string) (map[string]string, error) {
	s.log.Warn("lark stub client: BatchGetUsers called", "count", len(openIDs))
	return nil, ErrAPIClientNotConfigured
}

// AddMessageReaction 实现 APIClient（stub 恒报未配置）。
func (s *stubAPIClient) AddMessageReaction(ctx context.Context, p AddReactionParams) (string, error) {
	s.log.Warn("lark stub client: AddMessageReaction called", "message_id", p.MessageID, "emoji_type", p.EmojiType)
	return "", ErrAPIClientNotConfigured
}

// DeleteMessageReaction 实现 APIClient（stub 恒报未配置）。
func (s *stubAPIClient) DeleteMessageReaction(ctx context.Context, p DeleteReactionParams) error {
	s.log.Warn("lark stub client: DeleteMessageReaction called", "message_id", p.MessageID, "reaction_id", p.ReactionID)
	return ErrAPIClientNotConfigured
}
