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
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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
	// Quote is the message the sender was replying to (引用), present only
	// when they replied to one.
	Quote quotedMessage `json:"quote"`
}

// quotedMessage is the message a sender replied to. WeCom mirrors only its
// CONTENT — a msgtype and that type's body, the same shape a 图文混排 run
// has — so the fields come off mixedItem. What it does NOT carry is any
// identity: no msgid, no userid of whoever wrote it. That is the whole reason
// the quote is rendered into the body rather than resolved: there is nothing
// to resolve it against, on our side or WeCom's.
//
// A quoted 图文混排 nests one more level than a run does, hence the extra
// Mixed field and the render override below.
type quotedMessage struct {
	mixedItem
	Mixed struct {
		MsgItem []mixedItem `json:"msg_item"`
	} `json:"mixed"`
}

// quoteRun is one line the quoted message contributes: words somebody typed or
// said, or the marker standing in for an attachment together with the
// attachment it stands for.
//
// The marker and its attachment are one value on purpose. They used to be two
// lists, rendered by one function and collected by another, and anything that
// changed the rendered text in between (a sentence that spells the marker out,
// a cut that removes it) left the second list describing a body the agent never
// saw. A run that has an attachment cannot be laid out without its marker, and
// a run that is laid out cannot lose its attachment, so fitQuote below decides
// what survives once and both follow.
type quoteRun struct {
	text  string
	media *InboundMedia
}

// runs lays the quoted message out as the lines it contributes, in order. A
// kind this adapter does not know contributes nothing, the same way a mixed
// run does.
func (q quotedMessage) runs() []quoteRun {
	if !strings.EqualFold(q.MsgType, "mixed") {
		if run, ok := q.mixedItem.quoteRun(); ok {
			return []quoteRun{run}
		}
		return nil
	}
	var out []quoteRun
	for _, item := range q.Mixed.MsgItem {
		if run, ok := item.quoteRun(); ok {
			out = append(out, run)
		}
	}
	return out
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

// quoteRun is render for a run inside a QUOTED message: an attachment comes
// back as a marker with room in it to be named (quotedMediaPlaceholder) along
// with the attachment that marker stands for.
func (item mixedItem) quoteRun() (quoteRun, bool) {
	if s := item.words(); s != "" {
		return quoteRun{text: s}, true
	}
	body, kind, ok := mediaFor(item.MsgType, item.Image, item.File, item.Video)
	if !ok || strings.TrimSpace(body.URL) == "" {
		return quoteRun{}, false
	}
	marker := quotedMediaPlaceholder(kind)
	return quoteRun{text: marker, media: &InboundMedia{
		Kind:              kind,
		URL:               body.URL,
		AESKey:            body.AESKey,
		InlinePlaceholder: marker,
	}}, true
}

// mediaPlaceholder is the marker that stands in for an attachment in the
// stored message body, so the agent can see that something was attached
// before (or instead of) the bytes arriving on the detached media path.
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

// mediaUnavailable is what a quoted attachment's marker says before anything
// has been fetched for it, and what it keeps saying if the fetch never
// succeeds. A marker that is never rewritten still reads as a sentence.
const mediaUnavailable = "unavailable"

// quotedMediaPlaceholder is the marker a QUOTED attachment renders as. It
// carries a word inside the brackets where the sender's own "[Image]" carries
// nothing, because the quote's marker is the one that has to be joined to an
// attachment.
//
// Fetching the quoted picture puts it in the attachment list and stops there.
// The list is a separate block of text from the body, so with one attachment
// the reader infers the join and with two it cannot — and a quote is exactly
// the case where a message carries somebody else's picture beside its own.
// The marker is what the engine rewrites into the attachment's id.
//
// The sender's OWN attachments keep the bare "[Image]" they have always had.
// It is already unambiguous, being the attachment on the message the reader is
// looking at, and changing it would change what every existing wecom message
// body says.
func quotedMediaPlaceholder(kind channel.MsgType) string {
	return namedMediaPlaceholder(kind, mediaUnavailable)
}

// namedMediaPlaceholder writes a name inside the bracketed marker: "[Image]"
// becomes "[Image: <name>]". engine.inlineAttachmentIDMarker rewrites the same
// shape and keeps the label ahead of the colon, so the two spellings cannot
// drift apart.
func namedMediaPlaceholder(kind channel.MsgType, name string) string {
	return strings.TrimSuffix(mediaPlaceholder(kind), "]") + ": " + name + "]"
}

// mediaFor returns the body and normalized kind for a raw wecom msgtype, and
// whether that type is one we download at all.
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
	_, quotedMedia := mc.quotedBlock()
	return append(quotedMedia, mc.ownAttachments()...)
}

func (mc aibotMsgCallback) ownAttachments() []InboundMedia {
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

// quotePrefix labels the quoted block so an agent reading the body as plain
// text can tell it apart from the sender's own words. It sits inside a
// markdown blockquote rather than replacing it: the quote can be several
// lines, and only the blockquote keeps the later ones attached to it.
//
// Spelled like the media placeholders (mediaPlaceholder above) so an agent
// reading every channel through one prompt meets one vocabulary.
const quotePrefix = "[Quote]"

// maxQuotedRunes bounds the quoted block. Runes, not bytes: the quoted text is
// usually Chinese, where a byte bound would cut roughly a third as many
// characters and could split one in half.
const maxQuotedRunes = 500

// quotedContext renders the message the sender was replying to, to be shown
// AHEAD of their own words.
//
// Without it a reply is unanswerable: "这个怎么处理" quoting an alert is a
// complete question in the chat and an empty one to the agent, which sees the
// three words and none of what they point at. WeCom sends the quoted content
// on every such message and this adapter was dropping it.
//
// It is deliberately kept out of ownCommandSource: the command parsers read
// the first non-empty line, and a quoted line is not one the sender typed
// here. Prefixing it would let a quote of somebody else's "/issue …" file an
// issue nobody asked for.
func (mc aibotMsgCallback) quotedContext() string {
	block, _ := mc.quotedBlock()
	return block
}

// quotedBlock is the quote block and the attachments whose markers are in it.
//
// FETCHING THE ATTACHMENTS IS THE WHOLE OF THE FIX. "[Image]" is all a quote
// of a picture could be rendered as — the payload carries no sender, no
// message id and no timestamp to say WHICH picture — so an agent reading that
// line cannot tell a screenshot it read a minute ago from one it has never
// seen, and answers "左下角那块看不清" about a picture it does not have. The
// url and aeskey on the quote are the only thing that resolves it, and the
// callback hands them over exactly the way it hands over an attachment the
// sender just made.
//
// The marker closes the other half. Fetching the bytes puts the picture in the
// attachment list; the marker's occurrence number is what says WHICH entry of
// that list the quote's marker is, and without it a message carrying two
// attachments — one quoted, one just sent — hands the agent two markers and
// two ids with nothing joining them.
//
// The rule the pair obeys: an attachment is returned only if its marker is in
// the block, and its InlineIndex counts occurrences in the block as returned.
// The engine binds by searching the stored body for the Nth occurrence of the
// marker string, so the number has to be taken from the text the agent will
// read — not from how many attachments came before it — and the text is
// whatever is left after the cut. Both come out of fitQuote in one pass, so
// neither can describe a block the other did not produce.
func (mc aibotMsgCallback) quotedBlock() (string, []InboundMedia) {
	rendered, media := fitQuote(mc.Quote.runs(), maxQuotedRunes)
	if rendered == "" {
		return "", nil
	}
	var b strings.Builder
	for i, line := range strings.Split(rendered, "\n") {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString("> ")
		if i == 0 {
			b.WriteString(quotePrefix)
			b.WriteString(" ")
		}
		b.WriteString(line)
	}
	return b.String(), media
}

// fitQuote lays the quote's runs out within maxRunes and returns the text with
// the attachments that survived in it, each stamped with which occurrence of
// its marker it is.
//
// A quoted document would otherwise become the body. The sender quoted it to
// point at it, not to resend it, and the words that carry their question are
// their own — which follow the block and must not be pushed out of the agent's
// reach by it. So TEXT is cut at maxRunes, markers counting toward that budget
// while they fall inside it, and the cut is marked with "…".
//
// A marker is never cut, and never dropped. Past the budget, runs of words are
// dropped and runs that carry an attachment are still laid out whole, one line
// each; a marker that starts inside the budget and ends past it is kept to the
// end. So the block can run past maxRunes by at most the markers it carries
// (about twenty runes each), and every attachment the caller will fetch is
// one the agent can find in the text. #7980 left the same constraint in its
// discussion: a truncated block must not drop a placeholder it still counts.
//
// InlineIndex is counted on the text as laid out, markers and words alike. A
// quoted sentence that spells "[Image: unavailable]" out is an occurrence of
// that marker as far as the engine's search is concerned, so it moves the real
// marker's number up by one.
func fitQuote(runs []quoteRun, maxRunes int) (string, []InboundMedia) {
	var (
		lines  []string
		owners []*InboundMedia // owners[i] is the attachment lines[i] is the marker of, or nil
		used   int
		cut    bool
	)
	for _, run := range runs {
		width := utf8.RuneCountInString(run.text)
		if len(lines) > 0 {
			width++ // the newline ahead of it
		}
		switch {
		case run.media != nil:
			lines = append(lines, run.text)
			owners = append(owners, run.media)
			used += width
		case cut:
			// The budget ran out earlier; only markers still get through.
		case used+width <= maxRunes:
			lines = append(lines, run.text)
			owners = append(owners, nil)
			used += width
		default:
			cut = true
			room := maxRunes - used
			if len(lines) > 0 {
				room-- // the newline ahead of it
			}
			if room > 0 {
				kept := strings.TrimRight(string([]rune(run.text)[:room]), " \t\n")
				if kept != "" {
					lines = append(lines, kept)
					owners = append(owners, nil)
				}
			}
			if len(lines) == 0 {
				lines = append(lines, "")
				owners = append(owners, nil)
			}
			lines[len(lines)-1] = strings.TrimRight(lines[len(lines)-1], " \t\n") + "…"
		}
	}

	var media []InboundMedia
	for i, owner := range owners {
		if owner == nil {
			continue
		}
		m := *owner
		m.InlineIndex = strings.Count(strings.Join(lines[:i], "\n"), m.InlinePlaceholder)
		media = append(media, m)
	}
	return strings.Join(lines, "\n"), media
}

// ownCommandSource is what the slash-command parsers read: the sender's own
// words, and nothing this adapter wrote.
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
	// InlinePlaceholder is the exact marker in the stored body this attachment
	// stands for, and InlineIndex is which occurrence of that marker it is.
	// They travel onto channel.MediaRef, and the binder rewrites that one
	// occurrence to name the attachment once a row exists (engine/session.go).
	//
	// Set for a QUOTED attachment only — see quotedMediaPlaceholder for why the
	// sender's own marker stays bare.
	InlinePlaceholder string `json:"inline_placeholder,omitempty"`
	InlineIndex       int    `json:"inline_index,omitempty"`
}

// inline copies the marker this attachment stands for onto the ref the binder
// will rewrite. A ref with no marker is left exactly as it was, which is what
// keeps the sender's own attachments on the path they were already on.
func (m InboundMedia) inline(ref channel.MediaRef) channel.MediaRef {
	if m.InlinePlaceholder == "" {
		return ref
	}
	ref.InlinePlaceholder = m.InlinePlaceholder
	ref.InlineIndex = m.InlineIndex
	// The marker REFERS to the attachment, it does not carry it: the picture
	// belongs to a message somebody else sent, and replacing the marker with an
	// inline image would state this sender attached it again.
	ref.InlineIDOnly = true
	return ref
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
	// A quote counts as content here for the same reason media does: it is why
	// the directive-only layouts below are not the empty pending sentinel.
	// Rendering it happens further down — this only needs to know it exists.
	quoted := mc.quotedContext()
	normalizedText, control, controlNormalized := normalizeWeComControlLayout(
		mc, text, command, chatType, botDisplayName, len(media) > 0 || quoted != "",
	)
	if controlNormalized {
		text = normalizedText
	}

	// The quoted message goes on last, so everything above — the control-layout
	// rewrite and the command source it may hand back — still reads the body
	// the sender actually composed. Only the stored, agent-visible text grows.
	ownBody := text
	if quoted != "" {
		if text == "" {
			text = quoted
		} else {
			text = quoted + "\n\n" + text
		}
	}

	// A bare /clear that still carries content — media, a quote, or both — is a
	// real turn, not the shared pending sentinel, so it must not reach Router
	// with the directive still in the command source. ForceFresh below carries
	// the already-consumed directive; the command source becomes whatever body
	// is left, which is never a command (a quote opens with "> ", a placeholder
	// with "["), so nothing downstream re-parses it.
	//
	// This runs after the quote is prepended, not before: leaving it above would
	// hand Router an empty CommandText, which it fills from Text — reaching the
	// same place by a route that only works while the quote happens not to parse
	// as a command.
	if controlNormalized && control.Kind == engine.ControlCommandFreshSession && control.Body == "" {
		command = text
	}

	// An enriching adapter owes Router a command source (router.go:200-208).
	// ownCommandSource answers "" for a standalone photo, file or video on
	// purpose — a placeholder is not words the sender typed — but once a quote
	// is prepended, Router's empty-CommandText fallback assigns the ALREADY
	// enriched Text, and the quote becomes the Chat title (#8058's shape).
	//
	// So hand over the body as it stood before enrichment: still no words the
	// sender did not type, and the placeholder is dropped again downstream by
	// deriveFirstMessageTitle, which lands the title back on the media path it
	// takes when the same screenshot arrives without a quote. lark snapshots
	// its own body for this reason (ws_frame_decoder.go:113).
	if command == "" && quoted != "" {
		command = ownBody
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
		// The quote is context the sender picked by replying to it, which is
		// what channel.InboundMessage.HasSelectedContext names: it is input
		// even when a control command has no body of its own. Without it a
		// bare directive behind a quote reads as an empty message to Router,
		// which persists nothing and answers nobody.
		HasSelectedContext: quoted != "",
		ForceFresh:         controlNormalized && control.Kind == engine.ControlCommandFreshSession,
		// A pure /issue command in WeCom should NOT trigger the
		// agent — the engine already creates the issue and the
		// OutboundReplier already sends "✅ 已创建 #N". Letting the agent
		// see "/issue foo" then produces a "I don't recognize this slash
		// command" reply that just clutters the conversation. wecom is
		// alone on this — Slack/Lark keep the historical "let the agent
		// see /issue and respond too" behaviour.
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
	hasOtherContent bool,
) (string, engine.ControlCommand, bool) {
	control, ok := engine.ParseControlCommand(command)
	if !ok || (control.Body == "" && !hasOtherContent) {
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

// ---- streaming replies ----

// streamContentLimit is aibot's cap on stream.content: 20480 bytes of utf8
// (https://developer.work.weixin.qq.com/document/path/101031). Content is a
// FULL replacement of the bubble's body on every frame, never a delta, so this
// bounds the whole answer rather than one chunk of it.
const streamContentLimit = 20480

// streamThinkingPlaceholder is what the opening frame says. Per 101031 a
// content carrying <think></think> renders as the client's own thinking
// affordance — the animated dots — which is exactly the "working on it" bubble
// we want and costs no copy in any language. Tencent's own OpenClaw plugin
// opens its streams with the same literal.
const streamThinkingPlaceholder = "<think></think>"

// aibot errcodes worth branching on.
//
// A wrong guess about either degrades rather than breaks — an errcode this file
// does not recognise falls through to the plain-message fallback, which is
// where a refused frame ends up anyway.
const (
	// errcodeStreamExpired — the stream ran past its window and the server
	// will not take another frame for it.
	//
	// Measured on 2026-08-09 against the live tenant, not inferred: a stream
	// framed every thirty seconds was refused with exactly this code, and the
	// server's own errmsg named the reason — "stream message update expired
	// (>10 minutes), cannot update". The published global errcode table
	// defines it the same way. The window it implies is streamMaxAge; see
	// stream_store.go for the numbers and what else the probe settled.
	errcodeStreamExpired = 846608

	// errcodeStreamBadReqID — this req_id may not carry a stream.
	//
	// ASSUMPTION, unconfirmed by the vendor and unmeasured: this number is
	// read off Tencent's OpenClaw plugin source, with no version pinned, and
	// it does not appear in WeCom's published error tables the way 846608
	// does. What would settle it: the code appearing in the published tables,
	// a support answer naming it, or a probe that opens a stream on an event
	// callback's req_id and reads back what the server says.
	//
	// An event callback's req_id looks usable and is not; only a message
	// callback's works, so the event path has to use aibot_send_msg.
	errcodeStreamBadReqID = 846605
)

// streamError is a server rejection of a stream frame, carrying the errcode so
// callers can tell "this bubble is beyond saving" from "that frame did not
// land".
type streamError struct {
	Code int
	Msg  string
}

func (e *streamError) Error() string {
	return fmt.Sprintf("wecom: stream frame rejected errcode=%d errmsg=%s", e.Code, e.Msg)
}

// Unusable reports whether the rejection means this stream can never be
// written to again — the caller must fall back to a plain message rather than
// retry.
func (e *streamError) Unusable() bool {
	return e.Code == errcodeStreamExpired || e.Code == errcodeStreamBadReqID
}

// streamUnusable is the package-level predicate over any error, so callers do
// not each re-implement the type assertion.
//
// Only a verdict from the server counts. A write that failed, an ack that
// never came, errNoLiveConnection from a registry holding no socket for the
// installation — none of those is in here, because none of them says anything
// about the stream. A req_id belongs to the turn and not to the connection it
// arrived on (measured 2026-08-09; sendersRegistry.stream), so a missing socket
// is a fact about this moment rather than about the bubble.
func streamUnusable(err error) bool {
	var se *streamError
	if errors.As(err, &se) {
		return se.Unusable()
	}
	return false
}

// respondStreamBody builds an aibot_respond_msg body carrying one frame of a
// streaming reply. finish=false paints or updates the bubble; finish=true
// seals it, after which the message is immutable.
//
// The blank-closing-frame check is the one rule that is not obvious from the
// wire format: WeCom ignores content with nothing visible in it, so a closing
// frame of spaces closes nothing and leaves the user with a bubble that spins
// forever. Refusing here means every caller inherits the check.
func respondStreamBody(streamID, content string, finish bool) (map[string]any, error) {
	if streamID == "" {
		return nil, errors.New("wecom: stream frame requires a stream id")
	}
	if finish {
		content = defuseThinkTags(content)
	}
	content = truncateStreamContent(content)
	if finish && !hasVisibleChar(content) {
		return nil, errors.New("wecom: closing stream frame needs visible content")
	}
	return map[string]any{
		"msgtype": "stream",
		"stream": map[string]any{
			"id":      streamID,
			"finish":  finish,
			"content": content,
		},
	}, nil
}

// defuseThinkTags stops an answer that talks about <think> from being read as
// one.
//
// The tag is the client's, not ours: per 101031 a stream body wrapped in
// <think></think> renders as WeCom's own collapsed thinking affordance, which
// is what the opening frame is built from. An answer that happens to contain
// the literal — quoting a prompt, explaining this very feature, pasting XML —
// gets the same treatment, and half the reply disappears into a fold with no
// edit and no unsend to undo it with.
//
// A zero-width space after the angle bracket is enough: the scanner no longer
// matches, and the reader sees the same characters they would have seen. Only
// the tag's own opening is touched, so comparisons, generics and HTML samples
// in the rest of the answer come through as written. Callers apply this to
// closing frames only — the opening frame IS the affordance.
func defuseThinkTags(s string) string {
	if !strings.Contains(s, "<") {
		return s
	}
	const zwsp = "​"
	var b strings.Builder
	last := 0
	// Indexing s directly, never a case-folded copy of it. Walking s while
	// reading strings.ToLower(s) at the same offsets looks equivalent and is
	// not: case folding does not preserve length. U+212A KELVIN SIGN is three
	// bytes and lowercases to a one-byte "k", so the folded copy can be
	// SHORTER than the original and an offset taken from s can be past its
	// end. "KK<x" — two Kelvin signs and an angle bracket — is enough to slice
	// out of range, and the string being scanned is the agent's own answer, so
	// any text a user can talk the agent into echoing would take the backend
	// down with it.
	for i := 0; i < len(s); i++ {
		if s[i] != '<' {
			continue
		}
		j := i + 1
		if j < len(s) && s[j] == '/' {
			j++
		}
		if j+len("think") > len(s) || !strings.EqualFold(s[j:j+len("think")], "think") {
			continue
		}
		b.WriteString(s[last : i+1])
		b.WriteString(zwsp)
		last = i + 1
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// truncateStreamContent cuts content to the protocol's byte limit on a
// character boundary. An answer that arrives clipped still answers; one the
// server rejects for length does not.
func truncateStreamContent(s string) string {
	if len(s) <= streamContentLimit {
		return s
	}
	const ellipsis = "…"
	cut := streamContentLimit - len(ellipsis)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + ellipsis
}

// hasVisibleChar reports whether s contains a rune that is neither whitespace
// nor a control character. That is the test a completion has to pass before it
// becomes a message: a body the client renders as nothing still occupies a
// bubble in the chat, and a completion of newlines is one.
//
// Not the same as "the client will render something", and deliberately not.
// Format runes — U+200B zero width space, U+FEFF, a soft hyphen — are neither
// space nor control, so a body made only of those passes here and still shows
// as nothing. Nothing upstream rejects such a body either: it reaches the chat
// as an empty bubble, and this predicate is not what stops it. The line is
// drawn here to keep a Unicode category table out of the adapter — moving it
// is a separate decision, and that table is its cost.
func hasVisibleChar(s string) bool {
	for _, r := range s {
		if !unicode.IsSpace(r) && !unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// newStreamID mints the developer-chosen id that names one streaming message.
// Reusing an id replaces that message's body; a fresh one opens another
// bubble, which is why this must never collide across concurrent turns.
func newStreamID() string {
	var buf [12]byte
	if _, err := cryptorand.Read(buf[:]); err != nil {
		return fmt.Sprintf("wecom-stream-%d", time.Now().UnixNano())
	}
	return "s" + hex.EncodeToString(buf[:])
}

// sendMsgContentLimit is the cap on one aibot_send_msg markdown body: the same
// 20480 utf8 bytes the stream frame gets
// (https://developer.work.weixin.qq.com/document/path/101138). A body past it
// is refused WHOLE — the server does not clip it — and the refusal arrives as
// errcode 45002 on the ack, so before splitForWire a long answer simply never
// appeared in the chat.
const sendMsgContentLimit = 20480

// splitForWire cuts a reply into pieces the platform will accept, and returns
// the input untouched when it already fits — which is nearly always, so the
// common path allocates nothing.
//
// Splitting rather than truncating is the point. A long answer is a code
// review, a pasted log, a document draft: the tail is not filler, and neither
// a reply the server refuses whole nor one that stops at an ellipsis with no
// way to read the rest is an answer. The cut prefers a line boundary, then a
// rune boundary, so a piece never ends mid-character and rarely ends mid-line.
//
// Each piece carries a marker so the reader knows the answer continues. This
// is the one place the adapter adds words to an agent's own text, which is why
// the marker is a bare counter rather than a sentence: it belongs to no
// language, so it needs no translation and cannot contradict an answer written
// in one.
func splitForWire(content string) []string {
	if len(content) <= sendMsgContentLimit {
		return []string{content}
	}

	var pieces []string
	remaining := content
	for len(remaining) > 0 {
		// Reserve room for the widest marker this piece could end up with.
		// The total is not known until the split is done, so the placeholder
		// stands in for it: "…" is three bytes, which covers a total up to
		// three digits — far past any answer that reaches this function.
		marker := fmt.Sprintf("\n\n(%d/…)", len(pieces)+1)
		budget := sendMsgContentLimit - len(marker)
		if len(remaining) <= sendMsgContentLimit {
			pieces = append(pieces, remaining)
			break
		}
		cut := wireCutPoint(remaining, budget)
		// Nothing is dropped at the seam. The cut is an index into remaining
		// and both sides of it are kept: a line break the cut point chose ends
		// the piece it belongs to, so concatenating the pieces with their
		// markers stripped gives the answer back byte for byte. An earlier
		// version trimmed leading newlines here, which silently ate a
		// paragraph break out of every log and code block long enough to
		// split.
		pieces = append(pieces, remaining[:cut])
		remaining = remaining[cut:]
	}

	// A piece with nothing visible in it is not sent. A long answer that ends
	// in a run of blank lines puts that run in a piece of its own — the last
	// piece carries no marker, so nothing else makes it visible — and that
	// piece reaches the chat as an empty bubble, which is the thing
	// hasVisibleChar exists at the call sites to prevent. Dropping it costs
	// the reader nothing: what is dropped is whitespace that would have
	// occupied a whole message on its own.
	//
	// Filtered before the markers go on, so the numbering counts the pieces
	// the person actually receives.
	kept := pieces[:0]
	for _, p := range pieces {
		if hasVisibleChar(p) {
			kept = append(kept, p)
		}
	}
	pieces = kept

	// The count is only knowable once the split is done, so the markers go on
	// afterwards. The last piece gets none: there is nothing after it to
	// promise, and the reader can see that for themselves.
	total := len(pieces)
	for i := range pieces {
		if i == total-1 {
			continue
		}
		pieces[i] += fmt.Sprintf("\n\n(%d/%d)", i+1, total)
	}
	return pieces
}

// wireCutPoint picks where to end a piece: the last line break inside the
// budget when there is one worth using, otherwise the last rune boundary.
func wireCutPoint(s string, budget int) int {
	if budget >= len(s) {
		return len(s)
	}
	// A line break in the last quarter of the budget is worth taking; one
	// near the start would waste most of a frame. The cut goes AFTER it, so
	// the break stays at the end of the piece it terminated rather than
	// falling into the gap between two frames.
	if nl := strings.LastIndexByte(s[:budget], '\n'); nl > budget*3/4 {
		return nl + 1
	}
	cut := budget
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	if cut == 0 {
		// A single rune wider than the budget cannot happen at this size, but
		// returning 0 would loop forever, so fall back to the raw cut.
		return budget
	}
	return cut
}
