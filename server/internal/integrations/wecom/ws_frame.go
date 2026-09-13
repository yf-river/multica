// ws_frame.go —— aibot WebSocket 的线上格式。每帧都是带 {cmd, headers.req_id,
// body} 信封的 JSON。我们只解析会去处理的帧：
//
//	入站   — aibot_msg_callback（用户消息）、aibot_event_callback（事件）
//	出站   — aibot_subscribe（鉴权）、ping（心跳）、aibot_send_msg（推送）、
//	         aibot_respond_msg（窗口内回复）
//	响应   — 服务端对 aibot_subscribe / ping / send_msg 写的 ack
//
// 线上协议文档见 https://developer.work.weixin.qq.com/document/path/101463 。
package wecom

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
)

// 客户端发出的帧命令。
const (
	cmdSubscribe  = "aibot_subscribe"
	cmdPing       = "ping"
	cmdSendMsg    = "aibot_send_msg"
	cmdRespondMsg = "aibot_respond_msg"
)

// 服务端发出的帧命令。读取循环按它们 switch。
const (
	cmdMsgCallback   = "aibot_msg_callback"
	cmdEventCallback = "aibot_event_callback"
	cmdServerPing    = "ping"
	cmdPong          = "pong"
)

// aibot_event_callback.body.event.eventtype 里的事件类型。
const (
	eventDisconnected = "disconnected_event"
	eventEnterChat    = "enter_chat"
	eventTemplateCard = "template_card_event"
	eventFeedback     = "feedback_event"
)

// aibot_send_msg 的接收方种类。企微用整数而非字符串。
const (
	chatTypeSingleInt = 1
	chatTypeGroupInt  = 2
)

// frameHeaders 携带逐帧的关联 id。服务端 ack 会回显 req_id，客户端靠它把请求与
// 响应配对。
type frameHeaders struct {
	ReqID string `json:"req_id"`
}

// frameEnvelope 是服务端推送的每一帧的外层形态。Body 保持原始 JSON，让下游代码
// 能直接解具体结构、而不必重解外层信封。
type frameEnvelope struct {
	Cmd     string          `json:"cmd"`
	Headers frameHeaders    `json:"headers"`
	Body    json.RawMessage `json:"body"`

	// 响应字段（服务端 ack 我们某次写出时出现）。
	ErrCode int    `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
}

// aibotMsgCallback 是 aibot_msg_callback 帧的 body——一条从会话推到机器人面前
// 的用户消息。
type aibotMsgCallback struct {
	MsgID    string `json:"msgid"`
	AIBotID  string `json:"aibotid"`
	ChatID   string `json:"chatid"`
	ChatType string `json:"chattype"` // "single" | "group"
	From     struct {
		UserID string `json:"userid"`
	} `json:"from"`
	MsgType string `json:"msgtype"` // "text" | "image" | "voice" | "file" | "video" | "mixed"
	Text    struct {
		Content string `json:"content"`
	} `json:"text"`
	// Voice 携带的是转写文本，不是音频。企微在它那边跑语音识别、只把结果送来，
	// 所以一条语音不需要下载、不需要媒体密钥、不需要存储——它就是一句恰好被说
	// 出来的话。不按会话类型设门禁：语音无论从哪种会话来，转写都按同一方式读。
	Voice struct {
		Content string `json:"content"`
	} `json:"voice"`
	// Image / File / Video 是可下载的那几种。各自只携带一个预签名 COS url 加
	// 一串加密它字节的密钥——没有名字、没有大小、没有 MIME 类型（这些从哪来
	// 见 media_download.go）。
	Image mediaBody `json:"image"`
	File  mediaBody `json:"file"`
	Video mediaBody `json:"video"`
	// Mixed 承载图文混排——一条用户把文本段与附件交错排出来的消息。其中每一项
	// 自带类型、并携带与同类型单发消息相同的 body。
	Mixed struct {
		MsgItem []mixedItem `json:"msg_item"`
	} `json:"mixed"`
}

// mediaBody 是每种可下载类型携带的 {url, aeskey} 对。长连接模式下密钥按 url 现
// 造，所以它随消息走、而不在配置里。
type mediaBody struct {
	URL    string `json:"url"`
	AESKey string `json:"aeskey"`
}

// mixedItem 是图文混排消息的一段：一句话、一句说出的话、或一个附件，按用户排
// 版顺序排列。
type mixedItem struct {
	MsgType string `json:"msgtype"`
	Text    struct {
		Content string `json:"content"`
	} `json:"text"`
	Voice struct {
		Content string `json:"content"`
	} `json:"voice"`
	Image mediaBody `json:"image"`
	File  mediaBody `json:"file"`
	Video mediaBody `json:"video"`
}

// words 是图文混排某一段里 SENDER 敲出来或说出来的话。附件不贡献任何词——这正是
// 它与下面 render 的区别。
func (item mixedItem) words() string {
	switch strings.ToLower(item.MsgType) {
	case "text":
		return strings.TrimSpace(item.Text.Content)
	case "voice":
		// 企微在它那边跑语音识别、只把结果送来，所以一段语音就是一句恰好被说
		// 出来的话——无需下载、无需密钥。它是发送者本人的话，所以嘴里说出来的
		// "/issue 登录坏了" 和敲出来的一样是命令。
		return strings.TrimSpace(item.Voice.Content)
	default:
		return ""
	}
}

// render 把图文混排的一段变成它对消息正文贡献的那一行。本适配器不认识的那类段
// 落不贡献任何东西，而不是塞一个多余的占位符。
func (item mixedItem) render() string {
	if s := item.words(); s != "" {
		return s
	}
	body, kind, ok := mediaFor(item.MsgType, item.Image, item.File, item.Video)
	if !ok || strings.TrimSpace(body.URL) == "" {
		return ""
	}
	return mediaPlaceholder(kind)
}

// mediaPlaceholder 是在消息正文里替附件站位的标记，让 agent 在「字节经分离的媒
// 体路径到达之前（或替代其到达）」也能看到有东西被附上了。
//
// 这些字符串与飞书、钉钉逐字节一致：lark/content_flatten.go 的 flattenContent
// 返回 "[Image]" / "[File]" / "[Video]"，dingtalk/inbound.go:95 钉死
// dingtalkImagePlaceholder = "[Image]"、inbound.go:205 是 "[File]"。agent 用同
// 一套 prompt 读所有通道；给 wecom 单独造一种拼法，只会平白让 agent 多学一样。
func mediaPlaceholder(kind channel.MsgType) string {
	switch kind {
	case channel.MsgTypeImage:
		return "[Image]"
	case channel.MsgTypeVideo:
		return "[Video]"
	default:
		return "[File]"
	}
}

// mediaFor 为原始 wecom msgtype 返回其 body 与规范化后的类型，并回答该类型是
// 不是我们会去下载的那种。
func mediaFor(msgType string, image, file, video mediaBody) (mediaBody, channel.MsgType, bool) {
	switch strings.ToLower(msgType) {
	case "image":
		return image, channel.MsgTypeImage, true
	case "file":
		return file, channel.MsgTypeFile, true
	case "video":
		return video, channel.MsgTypeVideo, true
	default:
		return mediaBody{}, channel.MsgTypeUnknown, false
	}
}

// attachments 列出这条回调上可下载的媒体，按用户发出的顺序。没有 url 的 body 跳
// 过：没有可抓的，带着它走下去只会为一个永不可能存在的对象开一条 intent-ledger
// 行。
func (mc aibotMsgCallback) attachments() []InboundMedia {
	var out []InboundMedia
	add := func(body mediaBody, kind channel.MsgType) {
		if strings.TrimSpace(body.URL) == "" {
			return
		}
		out = append(out, InboundMedia{Kind: kind, URL: body.URL, AESKey: body.AESKey})
	}
	if body, kind, ok := mediaFor(mc.MsgType, mc.Image, mc.File, mc.Video); ok {
		add(body, kind)
		return out
	}
	if !strings.EqualFold(mc.MsgType, "mixed") {
		return nil
	}
	for _, item := range mc.Mixed.MsgItem {
		if body, kind, ok := mediaFor(item.MsgType, item.Image, item.File, item.Video); ok {
			add(body, kind)
		}
	}
	return out
}

// ownText 是这条回调「对 agent 可读」的正文，以及它到底有没有正文。
//
// 纯文本回它自己的内容；语音回企微识别出的转写——那是发送者本人的话、无需下载；
// 照片 / 文件 / 视频回一个括号占位符，因为字节会在之后的分离路径上到，而消息
// 此刻必须说点什么（下载若永不成功，幸存下来的也正是这个占位符）；图文混排回按
// 编排顺序渲染好的各段——这样写在图片上面的 "look at this" 依旧读得通。
//
// 识别在环境噪音或半秒短按时回空，而空正文到达 agent 就是一轮什么都没说的话——
// 所以空转写回答 false、走回执路径，和一张位置卡片或企微明年新加的类型一模一样。
func (mc aibotMsgCallback) ownText() (string, bool) {
	switch strings.ToLower(mc.MsgType) {
	case "text":
		return mc.Text.Content, true
	case "voice":
		transcript := strings.TrimSpace(mc.Voice.Content)
		return transcript, transcript != ""
	case "image", "file", "video":
		body, kind, _ := mediaFor(mc.MsgType, mc.Image, mc.File, mc.Video)
		if strings.TrimSpace(body.URL) == "" {
			return "", false
		}
		return mediaPlaceholder(kind), true
	case "mixed":
		var runs []string
		for _, item := range mc.Mixed.MsgItem {
			if s := item.render(); s != "" {
				runs = append(runs, s)
			}
		}
		if len(runs) == 0 {
			return "", false
		}
		return strings.Join(runs, "\n"), true
	default:
		return "", false
	}
}

// ownCommandSource 是斜杠命令解析器读的东西：发送者本人的话，不含任何本适配器
// 写出来的东西。
//
// 它不是 ownText。ownText 在附件所在处插入 "[Image]" / "[File]" / "[Video]"，
// 因为落库正文必须显示「有东西被附上了、附在哪」。engine.ParseIssueCommand 只
// 看第一行非空行，所以一个人先附截图、再敲 "/issue 登录坏了"——很自然、也是企
// 微编辑器鼓励的顺序——正文就以 "[Image]" 开头，解析器看到占位符而不是命令，
// 任务不建，也没有任何地方告诉他为什么。同一件事换个顺序敲就成。这不该是用户
// 需要知道的差别。
//
// 所以命令源丢掉占位符、按顺序保留发送者写的段。落库正文仍然带着它们：把占位符
// 从那里裁掉，会丢失分离式媒体绑定器要落地进去的位置
// （engine/issue_command.go 的 issueDescriptionFromCommandBody 从另一头说了同
// 一句话）。
//
// 一条说出来的消息回它的转写。那些话和敲出来的行一样属于发送者本人，所以嘴上
// 说 "/issue 登录坏了" 会建出敲出来会建的同一张任务——而若从打出来的字段取命令
// 源，它就是空的，此时引擎会退回 Text、照样建任务，并且在 SkipAgentRun 为 false
// 时还会让 agent 把任务正文跑一遍。
//
// 单独的照片 / 文件 / 视频回空。它整段正文就是占位符，没有可解析的词；把一条
// 发送者从未敲过的字符串递给解析器，正是这个函数存在的理由要消灭的缺陷。
func (mc aibotMsgCallback) ownCommandSource() string {
	switch strings.ToLower(mc.MsgType) {
	case "text":
		return mc.Text.Content
	case "voice":
		return strings.TrimSpace(mc.Voice.Content)
	case "mixed":
		var runs []string
		for _, item := range mc.Mixed.MsgItem {
			if s := item.words(); s != "" {
				runs = append(runs, s)
			}
		}
		return strings.Join(runs, "\n")
	default:
		return ""
	}
}

// aibotEventCallback 是 aibot_event_callback 帧的 body。我们只读事件类型；具体
// 事件字段（模板卡片选择、反馈投票）尚未对外呈现。
type aibotEventCallback struct {
	Event struct {
		EventType string `json:"eventtype"`
	} `json:"event"`
}

// ---- normalization ----

// InboundMessage 是 wecom 侧拍平后的信封：WS 读取循环从一条解码后的
// aibot_msg_callback 构造出它。它以 JSON 形态塞进 channel.InboundMessage.Raw，
// 让 wecom_resolvers.go 够得到跨平台信封不带的那几个平台特有字段（BotID、
// ReqID）。
type InboundMessage struct {
	// BotID 是这条事件被投递给的那台智能机器人的标识。它是安装解析器用的路由
	// 键。
	BotID string `json:"bot_id"`

	// MsgID 是企业微信按消息分配的 id，用于两阶段去重。
	MsgID string `json:"msg_id,omitempty"`

	// MsgType 是原始 wecom 类型（"text"、"image"、"event"、……）。媒体 / 未知类
	// 型经跨平台的 channel.MsgType 枚举往返（见 channelMsgType）；原始字符串留
	// 在这里供审计。
	MsgType string `json:"msg_type,omitempty"`

	// ChatType 是腾讯内部的会话判别值（1:1 为 "single"，群聊为 "group"）。
	ChatType string `json:"chat_type,omitempty"`

	// ChatID 是消息起源处的 userid（单聊）或 chatid（群聊）——出站与会话绑定的
	// 路由身份。
	ChatID string `json:"chat_id,omitempty"`

	// SenderUserID 是敲出这条消息的人的 userid。
	SenderUserID string `json:"sender_user_id,omitempty"`

	// Content 是人类可读的正文：用户的话——敲的，或语音经企微识别后还回来的
	// ——加上替附件站位的占位符。跨平台信封的 Text 字段由它填充。
	Content string `json:"content,omitempty"`

	// ReqID 是服务端随这条消息送来的帧 req_id。留着它，未来 aibot_respond_msg
	// （5 秒窗口）可以回显；第一版迭代一律用 aibot_send_msg，用不到它。
	ReqID string `json:"req_id,omitempty"`

	// Media 列出要抓取的附件，按用户发出的顺序。它是 MediaResolver 的输入，只
	// 随 channel.InboundMessage.Raw 走、由引擎在内存中传递、绝不落库——url 五分
	// 钟就过期、密钥一次一用，两者都不该进表或进日志行。
	Media []InboundMedia `json:"media,omitempty"`
}

// InboundMedia 是回调上的一个可下载附件。
type InboundMedia struct {
	// Kind 是附件行打标的规范化媒体类型。
	Kind channel.MsgType `json:"kind"`
	// URL 是预签名 COS 地址，五分钟有效、无需 access token。
	URL string `json:"url"`
	// AESKey 解开 URL 抓回来的东西。长连接模式按 url 现造一把；见 media_crypt.go。
	AESKey string `json:"aeskey"`
}

// channelMessageFromCallback 把 wecom 侧的 aibot_msg_callback 转成 engine.Router
// 消费的跨平台 channel.InboundMessage。wecom 侧 InboundMessage 塞在 Raw 里，让
// wecom_resolvers.go 够得到平台特有字段。
//
// 路由身份：
//   - single → ChatType=p2p、ChatID=userid、SenderID=userid
//   - group  → ChatType=group、ChatID=chatid、SenderID=from.userid
//
// 用户在群里 @ 机器人，在线上和一条原始群消息无法区分——企微只在「确实被 @」时
// 才会转发给机器人，所以任何收到的群消息都算作被 @。
//
// text 是调用方已经用 ownText 求出的「对 agent 可读」正文。它作为参数传入而非在
// 这里重算，因为调用方在到达这里之前就必须知道消息能不能路由。命令源是另一条字
// 符串，在这里从 mc 推导——两者为何不能是同一字符串见 ownCommandSource。
//
// botDisplayName 是机器人在会话里的名字，来自安装配置。它只用于一件事：识别发送
// 者的 @ 在哪里结束。为空完全没问题，会退回到空白启发式；见 stripLeadingMentions。
func channelMessageFromCallback(botID, botDisplayName string, mc aibotMsgCallback, text, reqID string) channel.InboundMessage {
	chatType := channel.ChatTypeP2P
	if strings.EqualFold(mc.ChatType, "group") {
		chatType = channel.ChatTypeGroup
	}
	senderID := mc.From.UserID
	chatID := mc.ChatID
	if chatType == channel.ChatTypeP2P && chatID == "" {
		// 某些版本只在群聊里填 ChatID；退回发送者。
		chatID = senderID
	}

	// 命令源是发送者本人的话——ownCommandSource，不是解析后的正文——这样图文混
	// 排即使第一段是截图，它的 "/issue …" 仍落在解析器读到的第一行。
	//
	// 群里 @ 就是触达机器人的方式，所以它到达时和后面敲的字粘在一起——"@Andrew
	// /clear" 是一个人想要开新会话，不是碰巧含某个词的散文——所以要把 @ 从前面剥
	// 掉。
	//
	// 只对群做。1:1 里没人需要 @ 机器人，所以开头的 "@" 是发送者在提一个他正在谈
	// 论的同事："@李雷 /issue 帮我问问他" 是一个问题，把名字剥掉会变成一条没人要
	// 求的任务，再叠加下面的 SkipAgentRun，连回答都没有。
	//
	// 对纯文本消息它就是 mc.Text.Content——正是设置 CommandText 之前 p2p 一直透传
	// 的那个；对语音就是转写，所以说出口的命令也是命令。
	command := mc.ownCommandSource()
	if chatType == channel.ChatTypeGroup {
		command = stripLeadingMentions(command, botDisplayName)
	}
	media := mc.attachments()
	normalizedText, control, controlNormalized := normalizeWeComControlLayout(
		mc, text, command, chatType, botDisplayName, len(media) > 0,
	)
	if controlNormalized {
		text = normalizedText
		// 带媒体、裸 /clear 是一轮真会话，不是共享的 pending 哨兵。下面的
		// ForceFresh 会带上已经消费掉的那条指令。
		if control.Kind == engine.ControlCommandFreshSession && control.Body == "" {
			command = text
		}
	}

	wm := InboundMessage{
		BotID:        botID,
		MsgID:        mc.MsgID,
		MsgType:      mc.MsgType,
		ChatType:     mc.ChatType,
		ChatID:       chatID,
		SenderUserID: senderID,
		Content:      text,
		ReqID:        reqID,
		Media:        media,
	}
	raw, _ := json.Marshal(wm)

	return channel.InboundMessage{
		EventID:        mc.MsgID,
		MessageID:      mc.MsgID,
		Type:           channelMsgType(mc.MsgType),
		Text:           text,
		AddressedToBot: true,
		// 发送者本人的话，去掉群里的 @、不含媒体占位符。命令归类是共享的
		// （channel/message.go），本字段为空时退回 Text——而群里 Text 以 @ 开头、
		// 截图在前时以 "[Image]" 开头，所以一退到那个兜底，此类斜杠命令全被读成
		// 普通散文。飞书从它的命令 body 设这个字段（feishu_channel.go:139）、
		// Slack 从清洗后的文本设（slack/inbound.go:131）；WeCom 曾是唯一把它留空
		// 的适配器。
		CommandText: command,
		ForceFresh:  controlNormalized && control.Kind == engine.ControlCommandFreshSession,
		// 纯粹的 /issue 命令在 WeCom 不应触发 agent——引擎已经建好任务、
		// OutboundReplier 已经发了 "✅ 已创建 #N"。再让 agent 看到 "/issue foo"
		// 就会吐一句 "I don't recognize this slash command" 把会话弄脏。wecom 在
		// 这一点上是独一份——Slack/Lark 保留着历史行为「让 agent 也看到 /issue 并
		// 回答」。
		//
		// 从「引擎将解析的同一个源」读取，群里的 /issue 才和 p2p 的行为一致，而不
		// 是「建完任务还去问 agent」。必须同一个源：若从原始文本读，一条 p2p
		// "@李雷 /issue …" 会建任务然后沉默——这正是上面剥 @ 要设门禁的全部原因；
		// 若从解析后正文读，一条「截图然后 /issue」的消息会跳过 agent、而解析器又
		// 拒了占位符那行，发送者到头来既没任务也没回答。
		SkipAgentRun: isIssueCommand(command),
		Source: channel.Source{
			ChannelType: TypeWecom,
			ChatID:      chatID,
			ChatType:    chatType,
			SenderID:    senderID,
		},
		Raw: raw,
	}
}

// normalizeWeComControlLayout 把 /clear 或 /new 从「对 agent 可读」的正文里移除，
// 同时保留媒体占位符在原文混排中的位置。CommandText 仍是发送者写的、无占位符的
// 那个源，让 Router 单独运用两条指令之间的语义差别。
func normalizeWeComControlLayout(
	mc aibotMsgCallback,
	visible string,
	command string,
	chatType channel.ChatType,
	botDisplayName string,
	hasMedia bool,
) (string, engine.ControlCommand, bool) {
	control, ok := engine.ParseControlCommand(command)
	if !ok || (control.Body == "" && !hasMedia) {
		return visible, engine.ControlCommand{}, false
	}

	normalizeWords := func(words string) string {
		if chatType == channel.ChatTypeGroup {
			return stripLeadingMentions(words, botDisplayName)
		}
		return strings.TrimSpace(words)
	}

	switch strings.ToLower(mc.MsgType) {
	case "text":
		itemControl, itemOK := engine.ParseControlCommand(normalizeWords(mc.Text.Content))
		if !itemOK || itemControl.Kind != control.Kind {
			return visible, engine.ControlCommand{}, false
		}
		return itemControl.Body, control, true
	case "voice":
		itemControl, itemOK := engine.ParseControlCommand(normalizeWords(mc.Voice.Content))
		if !itemOK || itemControl.Kind != control.Kind {
			return visible, engine.ControlCommand{}, false
		}
		return itemControl.Body, control, true
	case "mixed":
		var runs []string
		consumed := false
		for _, item := range mc.Mixed.MsgItem {
			rendered := item.render()
			if !consumed {
				if words := item.words(); words != "" {
					itemControl, itemOK := engine.ParseControlCommand(normalizeWords(words))
					if itemOK && itemControl.Kind == control.Kind {
						rendered = itemControl.Body
						consumed = true
					}
				}
			}
			if rendered != "" {
				runs = append(runs, rendered)
			}
		}
		if consumed {
			return strings.Join(runs, "\n"), control, true
		}
	}
	return visible, engine.ControlCommand{}, false
}

// stripLeadingMentions 剥掉消息开头的 @提及——在群里这就是发送者 @ 机器人的方式。
// 企微把 @ 原样放进文本、旁边不送任何提及列表，所以没有可对照的东西，只能看形
// 状：最开头的 "@"，直到下一个空格。
//
// 只对群消息做——调用方按 chatType 给它设门禁。1:1 里没人会 @ 机器人，所以同样
// 的开头 "@" 是发送者自己句子里某个同事的名字，剥掉等于改写他说的话。
//
// 只剥最前面。句子更深处出现名字是发送者在谈论某人——"@Andrew ask @李雷 about
// yesterday" 是一条指令、指名一位同事——把那个也剥掉就是悄悄改写他说的话。
//
// 它主要喂命令归类。对已识别的 /clear 或 /new，normalizeWeComControlLayout 在重
// 建对 agent 可见的混排正文时也应用同一套地址清洗，让机器人的 @ 与被消费的指令
// 都不会作为提示词文本被落库。
//
// Slack 用一条正则对着它的提及 token 做同样的事（slack/inbound.go 的 cleanText）；
// 飞书拿到的是平台已经给干净的命令 body。WeCom 曾是唯一把原始文本直接透传的适配
// 器。
func stripLeadingMentions(s, botName string) string {
	for {
		trimmed := strings.TrimLeftFunc(s, unicode.IsSpace)
		if !strings.HasPrefix(trimmed, "@") {
			return trimmed
		}
		// 先整词匹配我们自己的名字。显示名可以含空格——"Multica Bot" 是最明显的
		// 例子——若在第一个空格处截断会留下 "Bot /clear 重新分析"，那不是命令，
		// 于是那个群里每条斜杠命令都会仍被丢掉。
		//
		// 名字不是猜的。它来自安装配置、在机器人接入时设置，因为回调不带结构化
		// 提及列表可读。缺席时跑下面的启发式——对单词型名字正确，也是所有安装
		// 在有人填这个字段前的默认行为。
		if botName != "" && strings.HasPrefix(trimmed[1:], botName) {
			s = trimmed[1+len(botName):]
			continue
		}
		i := strings.IndexFunc(trimmed, unicode.IsSpace)
		if i < 0 {
			// 整条消息就是一个孤零零的提及、别无他物。没有命令也没有话——原样放
			// 行，让调用方来裁定空正文，而不是在这里现造一个。
			return trimmed
		}
		s = trimmed[i:]
	}
}

// isIssueCommand 问引擎自己的解析器，而不是自己镜像一份。镜像曾经漂移过：它用
// strings.TrimSpace 裁剪——会把包括 U+3000（中文输入法全角模式打出的表意空格）
// 在内的一切 Unicode 空格剥掉——而 engine.ParseIssueCommand 只裁 " \t"。
//
// 于是 p2p 里一条以 U+3000 开头的行：这里读成命令、那里读成散文——SkipAgentRun
// 被设上所以 agent 不跑，解析器拒绝了所以任务不建。发送者什么都得不到，任何地
// 方的错误日志都不解释为什么。群消息在到达本助手前已经过「开头提及规范化」，也
// 必须走同一个解析器。
//
// 镜像一份解析器，它就是解析器。委托只花掉一条已做 I/O 的路径上的一次分配，并
// 消灭整一类错误。
func isIssueCommand(body string) bool {
	_, ok := engine.ParseIssueCommand(body)
	return ok
}

// channelMsgType 把原始 aibot msg_type 映射到规范化枚举。
func channelMsgType(wecomType string) channel.MsgType {
	switch strings.ToLower(wecomType) {
	case "text":
		return channel.MsgTypeText
	case "image":
		return channel.MsgTypeImage
	case "file":
		return channel.MsgTypeFile
	case "voice", "audio":
		return channel.MsgTypeAudio
	case "video":
		return channel.MsgTypeVideo
	case "mixed":
		// 图文混排：文本段与附件交错。它映射成 Text，因为经 ownText 渲染后这条
		// 消息确实就是文本——各段按编排顺序、每个附件以占位符站台——而附件作为
		// MediaRefs 单独走，和 Lark `post` 的处理一模一样：同一种形态换了个名
		// 字——lark/feishu_channel.go:167 把 post 映射成 MsgTypeText，而
		// lark/media_ingest.go:272 从同一个 post 里抽 img/media 段。
		//
		// 这一行以前读作 Unknown，当时的注释对当时存在的代码是对的：dispatchFrame
		// 只路由「以词抵达」的那些类型，混排消息到不了规范化，把它叫 Text 等于声
		// 称一种并不存在的路由。把路由加上之后，这个声称才成真。两者必须一起落
		// 地——不路由就把类型映射成 Text，正是旧注释警告过的那种「死了且误导」
		// 的映射。
		return channel.MsgTypeText
	default:
		// 一种适配器完全读不了的类型。dispatchFrame 用「不支持类型」回执回答它
		// 然后停住，所以这种类型到不了这里的规范化。
		return channel.MsgTypeUnknown
	}
}

// ---- outbound helpers ----

// subscribeBody 构造 aibot_subscribe body。成功时服务端回显 req_id、errcode 为
// 0。
func subscribeBody(botID, secret string) map[string]any {
	return map[string]any{"bot_id": botID, "secret": secret}
}

// sendMsgTextBody 构造一条携带纯文本内容的 aibot_send_msg body。aibot_send_msg
// 支持的 msgtype 只有 markdown 和 template_card——text 在该 cmd 上不被接受（对比
// aibot_respond_msg，它接受 text）。所以我们以 markdown 形态发送；企微客户端经
// markdown 路径渲染纯文本、不需要任何特殊转义。chatType 单聊为 1、群聊为 2。
func sendMsgTextBody(chatID string, chatType int, content string) (map[string]any, error) {
	if chatID == "" {
		return nil, errors.New("wecom: send_msg requires chat_id")
	}
	if chatType != chatTypeSingleInt && chatType != chatTypeGroupInt {
		return nil, errors.New("wecom: send_msg chat_type must be 1 (single) or 2 (group)")
	}
	return map[string]any{
		"chatid":    chatID,
		"chat_type": chatType,
		"msgtype":   "markdown",
		"markdown":  map[string]string{"content": content},
	}, nil
}

// aibotChatTypeFromChannel 把引擎的 ChatType 枚举映射成 aibot_send_msg body 要
// 的那个整数。
func aibotChatTypeFromChannel(t channel.ChatType) int {
	if t == channel.ChatTypeGroup {
		return chatTypeGroupInt
	}
	return chatTypeSingleInt
}
