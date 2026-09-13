// inbound.go：把 Slack Events API 负载翻译成引擎统一的入站结构
// channel.InboundMessage 的平台中立层。
//
// 这些是参数化的自由函数（bot 身份作为参数传入，而不是挂在 channel 上的方法）：
// 每个安装各自维护一条 Socket Mode 连接（slack_channel.go），在翻译每条事件时
// 把本安装已装的 bot 用户 id 传进来，供 @ 提及识别等逻辑使用。
package slack

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
)

// slackRawEvent 携带跨平台信封装不下的 Slack 特有字段——它们只会在 Slack 的
// resolver 内部读回（team_id 用来路由安装；核心层从不读 Raw）。
type slackRawEvent struct {
	TeamID      string         `json:"team_id"`
	APIAppID    string         `json:"api_app_id,omitempty"`
	EventType   string         `json:"event_type"`
	SubType     string         `json:"subtype,omitempty"`
	ChannelType string         `json:"channel_type,omitempty"`
	Files       []slackRawFile `json:"files,omitempty"`
}

// slackRawFile 是 Slack 文件对象中媒体 resolver 稍后取文件所需的子集（在连接器
// ACK 路径之外处理）。下载地址是 Slack 托管的 url_private(_download)，要用该
// 安装的 bot 令牌才能取到——它从不交给核心层，也不会越过 Raw 被持久化。
type slackRawFile struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Mimetype    string `json:"mimetype,omitempty"`
	Size        int64  `json:"size,omitempty"`
	DownloadURL string `json:"download_url"`
}

// rawFilesFrom 把事件的文件对象映射进原始信封，只保留媒体 resolver 真能取到的
// 文件：即下载地址指向 Slack 域名的那批。这样会滤掉还在处理中（还没有 URL）的
// 上传，以及 url_private 指向 Google Drive / Dropbox 的远端 "external" 文件——
// resolver 拒绝把 bot 令牌发往站外域名，带着它们只会让 HasMedia 承诺一批流水线
// 永远绑不回来的媒体，让每条注定失败的下载白白烧掉一趟延迟执行和一条意图台账行。
func rawFilesFrom(files []slack.File) []slackRawFile {
	var out []slackRawFile
	for _, f := range files {
		downloadURL := f.URLPrivateDownload
		if downloadURL == "" {
			downloadURL = f.URLPrivate
		}
		if !isFetchableSlackFileURL(downloadURL) {
			continue
		}
		out = append(out, slackRawFile{
			ID:          f.ID,
			Name:        f.Name,
			Mimetype:    f.Mimetype,
			Size:        int64(f.Size),
			DownloadURL: downloadURL,
		})
	}
	return out
}

// compileMentionRe 构造能匹配「@ 提及 botUserID」的正则。Slack 把提及渲染成
// <@U123> 或 <@U123|name> 两种形态，所以两边都要接住。botUserID 为空（安装没
// 找到 / 还没识别出来）时返回 nil——此时提及识别退化为空操作是安全的：私聊与
// app_mention 事件根本不依赖它，而无法路由的团队早在安装解析阶段就被丢掉了。
func compileMentionRe(botUserID string) *regexp.Regexp {
	if botUserID == "" {
		return nil
	}
	return regexp.MustCompile(`<@` + regexp.QuoteMeta(botUserID) + `(\|[^>]*)?>`)
}

// inboundFromMessage 把一条 Slack message 事件归一化。对绝不该进核心层的事件
// 返回 ok=false：bot 自己的消息与其它 bot 的消息（防环），以及编辑/删除/入群
// 等带 subtype 的系统消息（只有全新的用户消息才被摄取）。
//
// 群组寻址策略（v1，有意为之）：群消息只有带了显式 <@bot> 提及才算「发给
// bot」。bot 已介入的线程里不带提及的续聊，在这里【不】自动判定为发给 bot：
// 「回复的是 bot 的消息」属于会话状态，应当由带会话感知的共享服务/resolver 层
// 判断，而不是记在每条连接各自的适配器内存里。在那套机制落地前，频道/线程里的
// 续聊仍需再次 @ 提及 bot。单聊（P2P DM）则每条消息都摄取，不做筛选。
func inboundFromMessage(e slackevents.EventsAPIEvent, m *slackevents.MessageEvent, botUserID string, mentionRe *regexp.Regexp) (channel.InboundMessage, bool) {
	if m.BotID != "" || m.SubType == "bot_message" {
		return channel.InboundMessage{}, false
	}
	if m.User == "" || (botUserID != "" && m.User == botUserID) {
		return channel.InboundMessage{}, false
	}
	if !isIngestableSubtype(m.SubType) {
		return channel.InboundMessage{}, false
	}

	chatType := slackChatType(m.Channel, m.ChannelType)
	addressed := chatType == channel.ChatTypeP2P || mentionsBot(m.Text, mentionRe)
	// 文件附件挂在归一化的 Msg 上（对全新消息，slackevents 会把顶层消息负载
	// 拷一份进去）。
	var files []slackRawFile
	if m.Message != nil {
		files = rawFilesFrom(m.Message.Files)
	}
	return buildInbound(e, buildInboundParams{
		eventType: "message",
		subType:   m.SubType,
		channelID: m.Channel,
		userID:    m.User,
		text:      m.Text,
		ts:        m.TimeStamp,
		threadTS:  m.ThreadTimeStamp,
		chatType:  chatType,
		addressed: addressed,
		files:     files,
	}, mentionRe), true
}

// inboundFromAppMention 把一条 app_mention 事件归一化。app_mention 按定义就是
// 发给 bot 的，且发生在频道（群）里。同一个频道里的 @ 提及也会以相同 ts 的
// message 事件再送来一份，引擎的 (installation, message_id=ts) 去重会把这对
// 双胞胎压成一条——这里无需任何特判。
func inboundFromAppMention(e slackevents.EventsAPIEvent, m *slackevents.AppMentionEvent, botUserID string, mentionRe *regexp.Regexp) (channel.InboundMessage, bool) {
	if m.BotID != "" || m.User == "" || (botUserID != "" && m.User == botUserID) {
		return channel.InboundMessage{}, false
	}
	return buildInbound(e, buildInboundParams{
		eventType: "app_mention",
		channelID: m.Channel,
		userID:    m.User,
		text:      m.Text,
		ts:        m.TimeStamp,
		threadTS:  m.ThreadTimeStamp,
		chatType:  channel.ChatTypeGroup,
		addressed: true,
		files:     rawFilesFrom(m.Files),
	}, mentionRe), true
}

// buildInboundParams 是 buildInbound 的中间入参集合：各入口把事件里的 Slack
// 形态字段拆进来，由 buildInbound 统一拼成跨平台结构。
type buildInboundParams struct {
	eventType string
	subType   string
	channelID string
	userID    string
	text      string
	ts        string
	threadTS  string
	chatType  channel.ChatType
	addressed bool
	files     []slackRawFile
}

// buildInbound 把 Slack 事件字段组装成引擎的 channel.InboundMessage：Slack 特有
// 字段打进 Raw（供 resolver 读），ts 同时充当事件/消息 id，线程归属转成
// ReplyCtx，正文剥掉 @ 提及。
func buildInbound(e slackevents.EventsAPIEvent, p buildInboundParams, mentionRe *regexp.Regexp) channel.InboundMessage {
	raw, _ := json.Marshal(slackRawEvent{
		TeamID:      e.TeamID,
		APIAppID:    e.APIAppID,
		EventType:   p.eventType,
		SubType:     p.subType,
		ChannelType: string(p.chatType),
		Files:       p.files,
	})
	var reply *channel.ReplyCtx
	// 只有这条消息确实在某个线程里（thread_ts 存在、且不是它自己——Slack 会把
	// 线程首条消息的 thread_ts 设成它自己的 ts）才标记成线程内回复。
	if p.threadTS != "" && p.threadTS != p.ts {
		reply = &channel.ReplyCtx{MessageID: p.threadTS, RootID: p.threadTS}
	}
	text := cleanText(p.text, mentionRe)
	return channel.InboundMessage{
		EventID:        p.ts,
		MessageID:      p.ts,
		Type:           channel.MsgTypeText,
		Text:           text,
		CommandText:    text,
		ReplyTo:        reply,
		AddressedToBot: p.addressed,
		Source: channel.Source{
			ChannelType: TypeSlack,
			ChatID:      p.channelID,
			ChatType:    p.chatType,
			SenderID:    p.userID,
			ThreadID:    p.threadTS,
		},
		Raw: raw,
	}
}

// cleanText 把开头/中间夹着的 bot 提及串剥掉并收掉两侧空白，让核心层看到的是
// 用户的真实指令，而不是 "<@U123> hi" 这种原始形态。
func cleanText(text string, mentionRe *regexp.Regexp) string {
	if mentionRe != nil {
		text = mentionRe.ReplaceAllString(text, "")
	}
	return strings.TrimSpace(text)
}

// mentionsBot 判断正文里有没有对当前 bot 的 @ 提及。
func mentionsBot(text string, mentionRe *regexp.Regexp) bool {
	return mentionRe != nil && mentionRe.MatchString(text)
}

// slackChatType 把 Slack 的频道 id / channel_type 映射成归一化的 ChatType。只有
// 1:1 私聊（"im"，或 "D…" 开头的频道 id）算 p2p；其余一切——公开/私有频道、
// 以及多人群聊 mpim（多人会话）——都算群。群消息要走引擎「必须 @ 到 bot」的
// 过滤器，多人群聊里的闲谈才不会被误当成发给 bot 的指令。
func slackChatType(channelID, channelType string) channel.ChatType {
	switch channelType {
	case "im":
		return channel.ChatTypeP2P
	case "mpim", "channel", "group", "private_channel":
		return channel.ChatTypeGroup
	}
	if strings.HasPrefix(channelID, "D") {
		return channel.ChatTypeP2P
	}
	return channel.ChatTypeGroup
}

// isIngestableSubtype 判断某消息 subtype 是不是该让核心层摄取的「全新用户消息」。
// 空 subtype 是常态；thread_broadcast 和 file_share 是真实的用户消息；其余
// （message_changed、message_deleted、channel_join……）都是系统/编辑类事件。
func isIngestableSubtype(subType string) bool {
	switch subType {
	case "", "thread_broadcast", "file_share":
		return true
	default:
		return false
	}
}
