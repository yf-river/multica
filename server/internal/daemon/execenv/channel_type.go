package execenv

// 文件导读：本文件集中定义聊天会话的「频道判别」常量与判定逻辑——数据来自任务
// 载荷（claim）上服务端已盖好的 chat_channel_type 与 chat_type。
//
// 用途有三块：SurfacePersistsTranscript 判定「这个频道的对话是否落在 Multica 自己的
// chat_message 表里、能否用 `multica chat history` 读回」；AudienceOf 把「谁能看到
// 回复」归类成私聊/群聊/未知三态；ChannelCarriesFiles 判定 agent 产出的文件是否真能
// 送达读者。这些只在每轮聊天 prompt / 连续性提示里消费，服务端各自 adapter 侧另有
// 权威定义（见下方说明）。

// 到达任务载荷的聊天频道判别值。服务端从 channel_chat_session_binding 行盖出
// `chat_channel_type`（handler/daemon.go）；空值表示没有 IM 频道在后的 web/mobile
// 聊天会话。
//
// 刻意用普通字符串常量：daemon 比较的是服务端已序列化成 JSON 的值，不该为了让一个
// 判别值而把服务端集成包（integrations/slack、integrations/lark）拖进自己的构建。
// 权威定义在各自 adapter 侧——slack.TypeSlack 与 channel.TypeFeishu——两侧就下面
// 的线上字符串达成一致。WeCom 在其 adapter 落地前先把保留的线上判别值放这里。
const (
	ChannelTypeSlack    = "slack"
	ChannelTypeFeishu   = "feishu"
	ChannelTypeWecom    = "wecom"
	ChannelTypeDingtalk = "dingtalk"
)

// SurfacePersistsTranscript 报告某个聊天表面是否把对话存在 Multica 的 chat_message
// 表里、可经 `multica chat history` 读回（handler/chat_history.go 的非 Slack 回退）。
// Web 聊天（空判别值）、Feishu、WeCom 与 DingTalk 都经共享的 AppendUserMessage 路径
// 持久化；Slack 则读实时频道。它是「哪些表面可读」的唯一真源，被连续性提示路由器、
// 聊天 prompt 的历史副本与表面清单共享——保证未来某个不持久化的频道永远不会被告知
// 「读回它」。
func SurfacePersistsTranscript(channelType string) bool {
	switch channelType {
	case "", ChannelTypeFeishu, ChannelTypeWecom, ChannelTypeDingtalk:
		return true
	default:
		return false
	}
}

// 房间形态判别值，镜像 channel_chat_session_binding.chat_type
// （channel.ChatTypeP2P / channel.ChatTypeGroup）。每个 adapter 都会持久化这一列，
// 因此无论平台，只需一次读取就能知道对话形态。空表示服务端没有上报——web 聊天没有
// 绑定行，或服务端早于该字段。
const (
	ChatTypeP2P   = "p2p"
	ChatTypeGroup = "group"
)

// ChatAudience 是一次运行可以宣称的「谁能读到它的回复」。三态，因为「未知」不是
// 「私密」：web 聊天完全没有绑定行、按构造是 1:1，但一个服务端没上报形态的 IM 频道
// 可能是任何大小的房间——此时文案绝不能承诺一个对话未必拥有的隐私。
//
// per-turn 聊天 prompt 只声明一次受众。把分类收拢在一个函数里，可防止群聊、私聊与
// 兼容路径彼此漂移。
type ChatAudience int

const (
	// ChatAudienceUnknown 刻意做成零值：未初始化的房间上下文绝不能变成隐私声明。
	ChatAudienceUnknown ChatAudience = iota
	// ChatAudienceDirect —— 显式 p2p 绑定，或 web 聊天使用的无频道形态。声明协议在
	// 这里无法区分「被删的绑定」。
	ChatAudienceDirect
	// ChatAudienceGroup —— 由一群运行还没见过的用户共享的房间。
	ChatAudienceGroup
)

// AudienceOf 对一次 claim 的 (chat_channel_type, chat_type) 组合归类。
func AudienceOf(channelType, chatType string) ChatAudience {
	switch {
	case chatType == ChatTypeGroup:
		return ChatAudienceGroup
	case chatType == ChatTypeP2P:
		return ChatAudienceDirect
	case channelType == "":
		return ChatAudienceDirect
	default:
		return ChatAudienceUnknown
	}
}

// ChannelCarriesFiles 报告 agent 产出的文件是否真能到达这段对话。它是两层频道策略
// 的「交付」半边（MUL-4899）。
//
// 唯一调用方是 per-turn 聊天 prompt（daemon.buildChatPrompt）。运行时 brief 绝不能
// 调用它：答案会随一条续跑会话的轮次而变，而 brief 是 prompt-cache 前缀（MUL-5377）。
//
// serverSaysDelivers 是 claim 的 chat_channel_delivers_files，也是频道型聊天唯一被
// 咨询的东西。频道类型不被咨询——想从频道类型作答的冲动正是这个签名要防的缺陷：
// 最后一跳能否发生，需要「回去取绑定附件的 adapter」加「它回去取所需的对象存储」，
// 后半是任何 daemon 都观察不到的部署事实。`case ChannelTypeWecom: return true`
// 读起来像在陈述 adapter，作用却像在陈述每个跑着它的部署——包括没配存储、从而向
// agent 承诺了服务端无法兑现的交付的那些。混合版本从另一方向落进同一处：一个「知道
// WeCom 能交付文件」的新 daemon 配上「太老、无法做这步」的旧服务端，会推断出一个
// 并不存在的能力。两种情况在这里都落到 false——也就是「用文字描述文件」这条 per-turn
// 指令。
//
// web / mobile 聊天不在这里回答。它没有任何频道类型，由自己的分支处理——指向浏览器
// 渲染的附件卡而非 IM 消息。
func ChannelCarriesFiles(channelType string, serverSaysDelivers bool) bool {
	if channelType == "" {
		return false
	}
	return serverSaysDelivers
}

// ChannelDisplayName 把 chat_channel_type 渲染成 prompt / brief 文案用的展示名。
// 未知类型直接落到原始判别值而非通用占位符，因此一个服务端新增、这里没映射的频道
// 仍会在 prompt 里报出自己的名字，而不是静默显示成 "unknown"。
func ChannelDisplayName(channelType string) string {
	switch channelType {
	case ChannelTypeSlack:
		return "Slack"
	case ChannelTypeFeishu:
		return "Feishu/Lark"
	case ChannelTypeWecom:
		return "WeCom"
	case ChannelTypeDingtalk:
		return "DingTalk"
	default:
		return channelType
	}
}
