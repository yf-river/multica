package telegram

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
)

// 角色定位：把一条 Telegram Update 翻译成引擎的规范化消息 channel.InboundMessage。
// 引擎只认这套跨平台信封（文本、消息类型、回复定位、来源 Source、事件/消息 id），
// Telegram 专属字段（哪个 bot、发件人昵称等）塞进 Raw 供 Telegram 自己的 resolver 读回。
// 全部用参数化自由函数实现（调用方把自己的 botID/botUsername 穿进来），对齐 slack/inbound.go，
// 这样每条安装的轮询循环都拿"自己那条 bot"的身份来做归属判断。

// telegramRawEvent 携带跨平台信封放不下的 Telegram 专属字段——只在 Telegram 自己的
// resolver（resolvers.go）里读回，引擎与其它渠道从不解析它。
type telegramRawEvent struct {
	// BotID 把消息路由回它的安装（config->>'app_id'）。
	BotID string `json:"bot_id"`
	// EventType 是给"丢弃审计"用的粗略标签（目前恒为 "message"）。
	EventType string `json:"event_type"`
	// SenderName 是发送者在 Telegram 上的显示名，群聊语境归因（"谁说的"）时携带。
	SenderName string `json:"sender_name,omitempty"`
}

// inboundFromUpdate 把一条 Telegram update 规范化。返回 ok=false 表示这条消息**绝不能**
// 进入核心：bot 自己发的消息、channel 频道动态、编辑消息（编辑类已被 allowed_updates
// 挡掉）、或不支持的媒体类型（是否给私聊发"不支持"提示由调用方决定）。
//
// 群聊点名策略与 Slack v1 对齐：只有消息里显式 @bot 提及、或直接回复了 bot 的消息，
// 才算"发给 bot"。Telegram 的隐私模式默认开启，本来就不会把没点名 bot 的群聊闲谈推给 bot，
// 这个检查是对"在 BotFather 里关掉了隐私模式"的 bot 做的纵深防御。
func inboundFromUpdate(u Update, botID int64, botUsername string) (channel.InboundMessage, bool) {
	m := u.Message
	// From==nil 或 IsBot 或 ID==botID 都是 bot 自身/旁路消息，一律不收。
	if m == nil || m.From == nil || m.From.IsBot || m.From.ID == botID {
		return channel.InboundMessage{}, false
	}
	chatType, ok := telegramChatType(m.Chat.Type)
	if !ok {
		return channel.InboundMessage{}, false
	}

	text := m.Text
	if text == "" {
		text = m.Caption
	}
	msgType := classifyMessage(m)

	mentioned := mentionsBot(m, botUsername)
	repliedToBot := m.ReplyToMessage != nil && m.ReplyToMessage.From != nil && m.ReplyToMessage.From.ID == botID
	// 私聊天然"发给 bot"；群聊要求 @提及或回复 bot 才算。
	addressed := chatType == channel.ChatTypeP2P || mentioned || repliedToBot

	cleaned := normalizeText(text, botUsername)
	commandText := cleaned
	forceFresh := false
	// 控制命令（如 /clear 或 /issue 前的斜杠指令）由引擎统一解析：把指令壳剥掉、正文留下，
	// 并标记是否要求开全新会话。
	if control, ok := engine.ParseControlCommand(cleaned); ok {
		cleaned = control.Body
		forceFresh = control.Kind == engine.ControlCommandFreshSession
	}
	agentText := cleaned
	quotedHuman := m.ReplyToMessage != nil && m.ReplyToMessage.From != nil && !m.ReplyToMessage.From.IsBot
	// 群聊里"@bot 且引用了一条真人消息"：把被引用的那条人话附上，作为 agent 的理解上下文
	// （例如用户引用一段长文说"总结一下这个"）。
	if chatType == channel.ChatTypeGroup && mentioned && quotedHuman {
		agentText = enrichWithQuotedHumanMessage(cleaned, m.Chat.ID, m.ReplyToMessage)
	}

	senderID := strconv.FormatInt(m.From.ID, 10)
	chatID := strconv.FormatInt(m.Chat.ID, 10)
	threadID := ""
	if m.IsTopicMessage && m.MessageThreadID != 0 {
		threadID = strconv.FormatInt(m.MessageThreadID, 10)
	}

	raw, _ := json.Marshal(telegramRawEvent{
		BotID:      strconv.FormatInt(botID, 10),
		EventType:  "message",
		SenderName: senderDisplayName(m.From),
	})

	var reply *channel.ReplyCtx
	if m.ReplyToMessage != nil {
		reply = &channel.ReplyCtx{
			MessageID: messageKey(m.Chat.ID, m.ReplyToMessage.MessageID),
			RootID:    threadID,
		}
	}

	return channel.InboundMessage{
		EventID: strconv.FormatInt(u.UpdateID, 10),
		// Telegram 的 message id 只在"同一个聊天"内唯一，所以去重键
		// (installation, message_id) 用 "chat:message" 复合形式（见 messageKey）。
		MessageID:      messageKey(m.Chat.ID, m.MessageID),
		Type:           msgType,
		Text:           agentText,
		CommandText:    commandText,
		ReplyTo:        reply,
		AddressedToBot: addressed,
		ForceFresh:     forceFresh,
		Source: channel.Source{
			ChannelType: TypeTelegram,
			ChatID:      chatID,
			ChatType:    chatType,
			SenderID:    senderID,
			// Telegram 的用户 id 全局唯一，所以"本安装内的发送者 id"直接兼作
			// "跨安装的稳定 id"，无需像某些渠道那样再做一层映射。
			SenderStableID: senderID,
			ThreadID:       threadID,
		},
		Raw: raw,
	}, true
}

// messageKey 拼出"本安装内唯一"的消息 id："chatID:messageID"（Telegram 的 message_id
// 只在单个 chat 内唯一，不拼 chat 会跨聊天冲突）。
func messageKey(chatID, messageID int64) string {
	return strconv.FormatInt(chatID, 10) + ":" + strconv.FormatInt(messageID, 10)
}

// telegramChatType 把 Telegram 的 chat.type 映射成规范枚举。频道动态（channel post，
// 广播性质、没有可回话的发送者语境）不入库。
func telegramChatType(t string) (channel.ChatType, bool) {
	switch t {
	case "private":
		return channel.ChatTypeP2P, true
	case "group", "supergroup":
		return channel.ChatTypeGroup, true
	default:
		return "", false
	}
}

// classifyMessage 把消息载体归类成规范 MsgType。v1 只有文本可被处理（与飞书/Slack
// 对齐）；媒体种类还是要如实报出来，好让调用方回一句"暂不支持这类消息"，而不是装死。
func classifyMessage(m *Message) channel.MsgType {
	switch {
	case m.Text != "":
		return channel.MsgTypeText
	case len(m.Photo) > 0:
		return channel.MsgTypeImage
	case m.Voice != nil:
		return channel.MsgTypeAudio
	case m.Video != nil:
		return channel.MsgTypeVideo
	case m.Document != nil:
		return channel.MsgTypeFile
	default:
		return channel.MsgTypeUnknown
	}
}

// mentionsBot 判断消息文本里是否 @了本 bot。Telegram 会给提及打 entity 标注，但直接匹配
// 字面 token 也行——bot 用户名全局唯一、在文本里永远带 "@" 前缀，而且这种算法不依赖
// entity 的出现顺序，对残缺网关更宽容。
func mentionsBot(m *Message, botUsername string) bool {
	if botUsername == "" {
		return false
	}
	wantMention := "@" + botUsername
	for _, source := range []struct {
		text     string
		entities []MessageEntity
	}{
		{text: m.Text, entities: m.Entities},
		{text: m.Caption, entities: m.CaptionEntities},
	} {
		for _, entity := range source.entities {
			// 只认 @提及与 bot 命令两类 entity；其余（URL、加粗等）不参与判断。
			if entity.Type != "mention" && entity.Type != "bot_command" {
				continue
			}
			value, ok := messageEntityText(source.text, entity)
			if !ok {
				continue
			}
			if strings.EqualFold(value, wantMention) || commandTargetsBot(value, botUsername) {
				return true
			}
		}
	}
	// Telegram 正常情况下会给 entity，但旧夹具和某些不完整的网关可能缺——
	// 保留一个"按边界找 @bot"的兜底扫描，保证这些情况也能识别。
	return containsBotMention(m.Text, botUsername) || containsBotMention(m.Caption, botUsername)
}

// normalizeText 去掉 @bot 提及记号，但保留 /clear、/issue 这类共享斜杠命令原文，
// 好让引擎的命令解析器识别它们。
func normalizeText(text, botUsername string) string {
	cleaned := text
	if botUsername != "" {
		cleaned = removeBotMentions(cleaned, botUsername)
	}
	return strings.TrimSpace(cleaned)
}

// enrichWithQuotedHumanMessage 只把"群成员显式回复并 @bot 选中的那条消息"前置给 agent。
// 群聊的周边历史永远不会混进 agent 上下文（避免噪声与隐私泄漏）。CommandText 保持发送者
// 自己清理后的指令原文——被引用消息里若有命令，只属于历史语境，不该被执行。
func enrichWithQuotedHumanMessage(instruction string, chatID int64, quoted *Message) string {
	quotedText := quoted.Text
	if quotedText == "" {
		quotedText = quoted.Caption
	}
	if strings.TrimSpace(quotedText) == "" {
		quotedText = "[empty or non-text message]"
	}
	sender := "Unknown user"
	if quoted.From != nil {
		if name := senderDisplayName(quoted.From); name != "" {
			sender = name
		}
	}
	msgType := classifyMessage(quoted)
	block := fmt.Sprintf("<quoted_message message_id=%q sender=%q type=%q>\n%s\n</quoted_message>",
		messageKey(chatID, quoted.MessageID), sender, msgType, quotedText)
	if instruction == "" {
		return block
	}
	return block + "\n\n" + instruction
}

// commandTargetsBot 判断一条斜杠命令（如 "/start@MyBot"）末尾的 @ 后缀是不是指向本 bot。
// Telegram 允许命令带 @bot 后缀，@ 后缀命中才算点名了这个 bot。
func commandTargetsBot(command, botUsername string) bool {
	at := strings.LastIndexByte(command, '@')
	return at >= 0 && strings.EqualFold(command[at+1:], botUsername)
}

// messageEntityText 从 entity 的 (offset, length) 里切出对应文本。Telegram 的偏移以
// UTF-16 码元计，所以先把字符串转成 UTF-16 数组再切，避免用错单位切出半个字符。
func messageEntityText(text string, entity MessageEntity) (string, bool) {
	if entity.Offset < 0 || entity.Length <= 0 {
		return "", false
	}
	units := utf16.Encode([]rune(text))
	end := entity.Offset + entity.Length
	// end < entity.Offset 是防整数溢出（恶意/损坏的 entity 可能构造出超大偏移）。
	if entity.Offset > len(units) || end < entity.Offset || end > len(units) {
		return "", false
	}
	return string(utf16.Decode(units[entity.Offset:end])), true
}

// containsBotMention 在纯文本里做"边界感知"的 @bot 匹配：命中 @bot 前缀后，若紧跟的字符
// 仍是用户名合法字符（_/字母/数字），说明是 @botxxx 这样的更长用户名，不算点名。
func containsBotMention(text, botUsername string) bool {
	token := "@" + strings.ToLower(botUsername)
	lower := strings.ToLower(text)
	for start := 0; ; {
		i := strings.Index(lower[start:], token)
		if i < 0 {
			return false
		}
		i += start
		end := i + len(token)
		if end == len(lower) || !isTelegramUsernameByte(lower[end]) {
			return true
		}
		start = end
	}
}

// removeBotMentions 从文本里去掉 @bot 提及。逐段拼接而不是 ReplaceAll：仅当 @bot 后面
// 不是用户名合法字符时才判定为完整提及并删除，避免误删 "@botxxxx" 的其它用户名。
func removeBotMentions(text, botUsername string) string {
	token := "@" + strings.ToLower(botUsername)
	lower := strings.ToLower(text)
	var out strings.Builder
	for start := 0; start < len(text); {
		i := strings.Index(lower[start:], token)
		if i < 0 {
			out.WriteString(text[start:])
			break
		}
		i += start
		end := i + len(token)
		if end < len(lower) && isTelegramUsernameByte(lower[end]) {
			out.WriteString(text[start:end])
			start = end
			continue
		}
		out.WriteString(text[start:i])
		start = end
	}
	return out.String()
}

// isTelegramUsernameByte 判断字节是否为 Telegram 用户名的合法中缀字符
// （下划线、小写字母、数字）。注意返回 true 只代表"是"，用户名其它限制不在这里检查。
func isTelegramUsernameByte(b byte) bool {
	return b == '_' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9'
}

// containsFold 是大小写不敏感的 strings.Contains——Telegram 的 bot 用户名不区分大小写，
// 匹配时不能做精确比较。
func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}

// senderDisplayName 拼出 "名 姓"，都没有就用用户名兜底。
func senderDisplayName(u *User) string {
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if name != "" {
		return name
	}
	return u.Username
}
