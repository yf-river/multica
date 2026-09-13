// inbound.go：把钉钉 Stream 回调（botCallbackData）翻译成引擎归一化的
// channel.InboundMessage。
//
// 角色：这条 Stream 连接属于某一份安装，翻译时把【本安装】的 AppKey 一并盖进
// 信封（dingtalkRawEvent），让 Router 能把事件反查回它的安装——钉钉回调本身不
// 带 robotCode，全靠这一下盖章。
//
// 覆盖的消息类型：文本直接进正文；图片 picture 与富文本 richText 的下载码会转成
// dingtalkRawEvent.Media 资源表留给媒体解析器；音频/视频/文件/未知类型降级成带
// [占位] 的文本。@ 机器人的提及（群里按 isInAtList + 机器人全名判定）会在正文里
// 剥掉，只留用户真正要说的话。
package dingtalk

import (
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
)

// botCallbackData 是钉钉机器人消息回调的载荷——CALLBACK 帧 data 字段里装的 JSON。
// 这里只保留翻译层要读的字段；钉钉实际还发更多，直接忽略。替代厂商 SDK 的
// chatbot.BotCallbackDataModel。
type botCallbackData struct {
	ConversationId    string              `json:"conversationId"`
	ConversationTitle string              `json:"conversationTitle"`
	ConversationType  string              `json:"conversationType"`
	AtUsers           []botCallbackAtUser `json:"atUsers"`
	ChatbotUserId     string              `json:"chatbotUserId"`
	SenderStaffId     string              `json:"senderStaffId"`
	MsgId             string              `json:"msgId"`
	Msgtype           string              `json:"msgtype"`
	IsInAtList        bool                `json:"isInAtList"`
	Text              botCallbackText     `json:"text"`
	// Content 是非文本消息（picture / richText）按 msgtype 区分的负载，惰性按
	// msgtype 解码；超配额回调（errorCode 20001 会把 text/content 整个剥掉）里
	// 它缺省为空。
	Content json.RawMessage `json:"content"`
}

type botCallbackAtUser struct {
	DingtalkId string `json:"dingtalkId"`
	StaffId    string `json:"staffId"`
}

type botCallbackText struct {
	Content string `json:"content"`
}

// pictureContent 是 msgtype=picture 的 content 结构。真实载荷两个 code 都会给
// （developerpedia 文档），任一都能经 messageFiles/download 下载。
type pictureContent struct {
	DownloadCode        string `json:"downloadCode"`
	PictureDownloadCode string `json:"pictureDownloadCode"`
}

// richTextContent 是 msgtype=richText 的 content 结构：一个【保序】的异构元素
// 数组——text 段 {"text":…} 与 picture 段 {"type":"picture","downloadCode":…}
// 按发送顺序交错。今天文档未定义的其它元素种类一律跳过。
type richTextContent struct {
	RichText []richTextItem `json:"richText"`
}

type richTextItem struct {
	Text                string `json:"text"`
	Type                string `json:"type"`
	DownloadCode        string `json:"downloadCode"`
	PictureDownloadCode string `json:"pictureDownloadCode"`
}

// refAlt 把一个图片元素的两种下载码排成 (主引用, 备用)：主码缺省时把次码提拔成
// 主引用、备用留空。
func refAlt(downloadCode, pictureDownloadCode string) (ref, alt string) {
	if downloadCode != "" {
		return downloadCode, pictureDownloadCode
	}
	return pictureDownloadCode, ""
}

// dingtalkRawEvent 存放跨平台信封（channel.InboundMessage.Raw）装不下的钉钉特有
// 字段。AppID 由接收连接盖章（它就是安装的路由键），只在各 resolver 里读回。
type dingtalkRawEvent struct {
	AppID             string                  `json:"app_id"`
	ConversationTitle string                  `json:"conversation_title,omitempty"`
	Media             []dingtalkMediaResource `json:"media,omitempty"`
}

type dingtalkMediaResource struct {
	Ref string `json:"ref"`
	Alt string `json:"alt,omitempty"`
	// InlineIndex 是适配器生成的占位符在可见正文里的第几次出现（含用户自己写的
	// 相同文本），媒体解析器据此把图插回原位置。
	InlineIndex int `json:"inline_index,omitempty"`
}

func dingtalkMediaResourceAt(ref, alt string, inlineIndex int) dingtalkMediaResource {
	return dingtalkMediaResource{Ref: ref, Alt: alt, InlineIndex: inlineIndex}
}

// 钉钉在 conversationType 里发的会话类型判别值。
const (
	convTypeP2P              = "1"
	convTypeGroup            = "2"
	dingtalkImagePlaceholder = "[Image]"
)

// inboundFromCallback 把一条钉钉 bot 回调归一化。ok=false 只用于【绝不能进核心】
// 的事件：没有发送者 staff id 的消息（系统/机器人自发的）。text/picture/richText
// 变可摄取消息；畸形或超配额的媒体负载（errorCode 20001 会剥掉 content）仍以显式
// 的「图片不可用」占位符进核心，而不是被适配器悄悄丢弃；audio/video/file/未知类型
// 同样以文本占位符透传。私聊（1:1）消息永远算「@ 到了机器人」；群消息只有在真的
// @ 到它时（钉钉经 isInAtList 上报）才算数。
func inboundFromCallback(data *botCallbackData, appID string) (channel.InboundMessage, bool) {
	return inboundFromCallbackWithBotName(data, appID, "")
}

// inboundFromCallbackWithBotName 用【仅对这份安装经钉钉群机器人列表接口验证过】的
// 机器人名字翻译一条回调。空名字刻意「失败即保守」：适配器保留所有可见的 @ 提及，
// 绝不靠空白猜它的起止位置。
func inboundFromCallbackWithBotName(data *botCallbackData, appID, botName string) (channel.InboundMessage, bool) {
	if data == nil {
		return channel.InboundMessage{}, false
	}
	if data.SenderStaffId == "" {
		return channel.InboundMessage{}, false
	}

	chatType := dingtalkChatType(data.ConversationType)
	rawEvent := dingtalkRawEvent{
		AppID:             appID,
		ConversationTitle: strings.TrimSpace(data.ConversationTitle),
	}
	msg := channel.InboundMessage{
		EventID:        data.MsgId,
		MessageID:      data.MsgId,
		AddressedToBot: chatType == channel.ChatTypeP2P || data.IsInAtList,
		Source: channel.Source{
			ChannelType: TypeDingTalk,
			ChatID:      data.ConversationId,
			ChatType:    chatType,
			SenderID:    data.SenderStaffId,
		},
	}

	switch data.Msgtype {
	case "text":
		msg.Type = channel.MsgTypeText
		msg.Text = strings.TrimSpace(normalizeDingTalkBotMention(data, data.Text.Content, botName))
		msg.CommandText = msg.Text
		return withDingTalkRaw(msg, rawEvent), true

	case "picture":
		var pc pictureContent
		if len(data.Content) == 0 || json.Unmarshal(data.Content, &pc) != nil {
			// 超配额（errorCode 20001 剥掉 content）或畸形负载：发送者是真用户、发了
			// 一张机器人读不懂的图。把它送进引擎，好走身份门槛拿到一句反馈。
			return mediaUnreadableMsg(msg, rawEvent), true
		}
		ref, alt := refAlt(pc.DownloadCode, pc.PictureDownloadCode)
		if ref == "" {
			return mediaUnreadableMsg(msg, rawEvent), true
		}
		msg.Type = channel.MsgTypeImage
		msg.Text = dingtalkImagePlaceholder
		msg.CommandText = msg.Text
		rawEvent.Media = []dingtalkMediaResource{dingtalkMediaResourceAt(ref, alt, 0)}
		return withDingTalkRaw(msg, rawEvent), true

	case "richText":
		var rc richTextContent
		if len(data.Content) == 0 || json.Unmarshal(data.Content, &rc) != nil {
			// 超配额 / 畸形 richText：同样交给引擎做身份门槛反馈，而不是适配器静默丢弃。
			return mediaUnreadableMsg(msg, rawEvent), true
		}
		normalizeDingTalkRichTextBotMention(data, rc.RichText, botName)
		var (
			text                   strings.Builder
			commandText            strings.Builder
			inlinePlaceholderCount int
		)
		for _, item := range rc.RichText {
			// 单个元素原则上可能同时带 text 段和图片 code；两者各自独立处理（不用
			// switch），避免任一被悄悄丢掉。先文本后图片，与发送顺序一致。两样都没有
			// 的（未文档化种类）不产出任何东西。
			if item.Text != "" {
				text.WriteString(item.Text)
				commandText.WriteString(item.Text)
				inlinePlaceholderCount += strings.Count(item.Text, dingtalkImagePlaceholder)
			}
			if item.Type == "picture" || item.DownloadCode != "" || item.PictureDownloadCode != "" {
				ref, alt := refAlt(item.DownloadCode, item.PictureDownloadCode)
				if ref == "" {
					continue // 图片元素却没有可用下载码
				}
				appendImagePlaceholder(&text)
				rawEvent.Media = append(rawEvent.Media, dingtalkMediaResourceAt(ref, alt, inlinePlaceholderCount))
				inlinePlaceholderCount++
			}
		}
		if len(rawEvent.Media) == 0 {
			msg.Type = channel.MsgTypeText
		} else {
			msg.Type = channel.MsgTypeImage
		}
		msg.Text = strings.TrimSpace(text.String())
		msg.CommandText = strings.TrimSpace(commandText.String())
		normalizeDingTalkRichTextControlLayout(&msg, rc.RichText, len(rawEvent.Media) > 0)
		return withDingTalkRaw(msg, rawEvent), true

	case "audio":
		msg.Type = channel.MsgTypeAudio
		msg.Text = "[Audio message]"
	case "video":
		msg.Type = channel.MsgTypeVideo
		msg.Text = "[Video message]"
	case "file":
		msg.Type = channel.MsgTypeFile
		msg.Text = "[File]"
	default:
		msg.Type = channel.MsgTypeUnknown
		msg.Text = "[Unsupported DingTalk message]"
	}
	msg.CommandText = msg.Text
	return withDingTalkRaw(msg, rawEvent), true
}

// normalizeDingTalkRichTextControlLayout 在共享 Router 处理前，从可见富文本正文里
// 剥掉两条会话控制指令（/new、/clear）之一，同时保留交错的图片占位符——Router 靠
// CommandText 无法重建这些占位符。原命令来源对 Router 仍然可见；适配器自己绝不改
// /new 的会话轮换，也不对剩余内容重新分类。
func normalizeDingTalkRichTextControlLayout(msg *channel.InboundMessage, items []richTextItem, hasMedia bool) {
	control, ok := engine.ParseControlCommand(msg.CommandText)
	if !ok || (control.Body == "" && !hasMedia) {
		return
	}

	// 只在【首个非空 text 段】能解析出同款指令时才动手：若指令实际落在更后面的段，
	// firstControl 与 control 对不上，函数原样返回，绝不去别处乱剥。
	firstText := -1
	for i := range items {
		if strings.TrimSpace(items[i].Text) != "" {
			firstText = i
			break
		}
	}
	if firstText < 0 {
		return
	}
	firstControl, ok := engine.ParseControlCommand(items[firstText].Text)
	if !ok || firstControl.Kind != control.Kind {
		return
	}

	if control.Kind == engine.ControlCommandFreshSession {
		msg.ForceFresh = true
	}
	items[firstText].Text = firstControl.Body
	var visible strings.Builder
	for _, item := range items {
		visible.WriteString(item.Text)
		if item.Type == "picture" || item.DownloadCode != "" || item.PictureDownloadCode != "" {
			ref, _ := refAlt(item.DownloadCode, item.PictureDownloadCode)
			if ref != "" {
				appendImagePlaceholder(&visible)
			}
		}
	}
	msg.Text = strings.TrimSpace(visible.String())
	if control.Kind == engine.ControlCommandFreshSession && control.Body == "" {
		// 带媒体的裸 /clear 是「真实的一轮」，不是共享引擎里那个裸命令哨兵。指令
		// 已被消费，用 ForceFresh 把信号带下去。
		msg.CommandText = msg.Text
	}
}

// normalizeDingTalkRichTextBotMention 从富文本里含 @ 提及的那个 text 段剥掉寻址
// 外壳。钉钉可能把那段放在媒体前或媒体后，且与控制指令所在的段相互独立。
func normalizeDingTalkRichTextBotMention(data *botCallbackData, items []richTextItem, botName string) {
	runs := make([]string, len(items))
	for i := range items {
		runs[i] = items[i].Text
	}
	removeDingTalkBotMention(data, runs, botName)
	for i := range items {
		items[i].Text = runs[i]
	}
}

// normalizeDingTalkBotMention 从纯文本消息里剥掉 @ 机器人的寻址 token（无论出现
// 在哪个位置）。
func normalizeDingTalkBotMention(data *botCallbackData, text, botName string) string {
	runs := []string{text}
	removeDingTalkBotMention(data, runs, botName)
	return runs[0]
}

type dingTalkMentionSpan struct {
	run        int
	start, end int
}

// removeDingTalkBotMention 只在「群聊 + 确被 @ + 持有验证过的 bot 名」时动手：
// 把 runs 里每个「@名字」剥掉。名前或名后整段空白时，连边界空白一起吞掉，避免正
// 文留下悬挂空格；两侧都有实词时去掉名后紧跟的水平空白（保留换行等垂直分隔），
// 免得 @ 与下一词之间残留空格把词断成两截。
func removeDingTalkBotMention(data *botCallbackData, runs []string, botName string) {
	botName = strings.TrimSpace(botName)
	if data == nil || data.ConversationType != convTypeGroup || !data.IsInAtList || botName == "" {
		return
	}
	mentions := exactDingTalkBotMentionSpans(runs, botName)
	for i := len(mentions) - 1; i >= 0; i-- {
		span := mentions[i]
		prefix := runs[span.run][:span.start]
		suffix := runs[span.run][span.end:]
		switch {
		case strings.TrimSpace(prefix) == "":
			prefix = ""
			suffix = trimLeftHorizontalSpace(suffix)
		case strings.TrimSpace(suffix) == "":
			prefix = trimRightHorizontalSpace(prefix)
			suffix = ""
		default:
			suffix = trimLeftHorizontalSpace(suffix)
		}
		runs[span.run] = prefix + suffix
	}
}

// exactDingTalkBotMentionSpans 逐段找字面量 "@botName"，只接受前后边界都是空白或
// 字符串边界的位置（防止把 "mybot" 这类嵌在词里的误伤）。返回的 span 按段内偏移
// 记录。
func exactDingTalkBotMentionSpans(runs []string, botName string) []dingTalkMentionSpan {
	literal := "@" + botName
	var spans []dingTalkMentionSpan
	for run, text := range runs {
		for offset := 0; offset < len(text); {
			relative := strings.Index(text[offset:], literal)
			if relative < 0 {
				break
			}
			start := offset + relative
			end := start + len(literal)
			if dingTalkMentionLeftBoundary(text[:start]) && dingTalkMentionRightBoundary(text[end:]) {
				spans = append(spans, dingTalkMentionSpan{run: run, start: start, end: end})
			}
			offset = end
		}
	}
	return spans
}

func dingTalkMentionLeftBoundary(prefix string) bool {
	if prefix == "" {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(prefix)
	return unicode.IsSpace(r)
}

func dingTalkMentionRightBoundary(suffix string) bool {
	if suffix == "" {
		return true
	}
	r, _ := utf8.DecodeRuneInString(suffix)
	// 钉钉不提供提及的起止区间，只有空白/结尾能证明「验证过的全名」真的到此为止；
	// 标点也可能是另一个显示名的延续（例如 "Bot-DEV"），所以这里保守地拒绝。
	return unicode.IsSpace(r)
}

func trimLeftHorizontalSpace(value string) string {
	return strings.TrimLeft(value, " \t\u3000")
}

func trimRightHorizontalSpace(value string) string {
	return strings.TrimRight(value, " \t\u3000")
}

// withDingTalkRaw 把钉钉特有信封序列化进消息的 Raw 字段。
func withDingTalkRaw(msg channel.InboundMessage, rawEvent dingtalkRawEvent) channel.InboundMessage {
	msg.Raw, _ = json.Marshal(rawEvent)
	return msg
}

// mediaUnreadableMsg 把适配器解析不了的媒体转成显式占位符。没有可下载的引用时，
// 共享媒体解析器就不进路径了，由普通聊天轮次携带这层「图读不了」的信号。
func mediaUnreadableMsg(msg channel.InboundMessage, rawEvent dingtalkRawEvent) channel.InboundMessage {
	msg.Type = channel.MsgTypeImage
	msg.Text = "[Image unavailable]"
	msg.CommandText = msg.Text
	return withDingTalkRaw(msg, rawEvent)
}

// appendImagePlaceholder 往 builder 里补一段换行分隔的图片占位行。
func appendImagePlaceholder(b *strings.Builder) {
	if b.Len() > 0 {
		b.WriteByte('\n')
	}
	b.WriteString(dingtalkImagePlaceholder + "\n")
}

// dingtalkChatType 把钉钉的 conversationType 映射成归一化 ChatType。"1" 是 1:1
// 私聊；其它一切（群聊 "2"）都算群，走引擎的「必须 @ 到机器人」过滤。
func dingtalkChatType(conversationType string) channel.ChatType {
	if conversationType == convTypeP2P {
		return channel.ChatTypeP2P
	}
	return channel.ChatTypeGroup
}
