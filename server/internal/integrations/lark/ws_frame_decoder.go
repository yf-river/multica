// ws_frame_decoder.go：把飞书塞在长连接 data Frame 里的 JSON 事件负载解码成
// 统一入站结构。
package lark

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// LarkJSONFrameDecoder 解码飞书嵌套在长连接 data Frame 内的 JSON 事件负载。
// 外层二进制 Frame 信封（ws_frame.go）由 connector 剥掉；decoder 只看到
// Frame.Payload 里的字节——飞书按标准“事件订阅”信封格式给出：
// {schema, header, event}。
//
// 三种结果：
//
//   - (msg, true,  nil) —— `im.message.receive_v1` 事件。Hub 走 Dispatcher 转发。
//   - (zero, false, nil) —— 心跳形态的 JSON，或我们尚不处理的事件类型
//     （im.chat.access_event_v1 等）。connector 静默丢弃并仍向飞书回 200 ACK，
//     让服务器停止重发。
//   - (zero, false, err) —— JSON 畸形或 schema 解析不了。connector 记日志并丢弃
//     这一帧；WS 连接保持不断——单个坏负载不该放大成重连风暴。
//
// decoder 无状态且 goroutine 安全——单个实例服务所有 supervisor goroutine。
type LarkJSONFrameDecoder struct{}

// NewLarkJSONFrameDecoder 构造一个新的 JSON 帧解码器。
func NewLarkJSONFrameDecoder() *LarkJSONFrameDecoder { return &LarkJSONFrameDecoder{} }

// Decode 实现 FrameDecoder 接口，把一帧 JSON 负载解码成入站消息。
func (d *LarkJSONFrameDecoder) Decode(payload []byte, inst Installation) (InboundMessage, bool, error) {
	if len(payload) == 0 {
		return InboundMessage{}, false, nil
	}
	var env larkEventEnvelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return InboundMessage{}, false, fmt.Errorf("envelope: %w", err)
	}

	// 飞书长连接 data frame 恒为 v2 事件信封（schema "2.0"）。旧 webhook v1
	// 那种 "type":"event_callback" 形态不会出现在长连接上——这里防御性地兼容
	// 一下，以防飞书开放向后兼容模式；但规范路径以 schema 为准。
	if env.Type != "" && env.Type != "event_callback" {
		return InboundMessage{}, false, nil
	}

	if env.Header.EventType != "im.message.receive_v1" {
		return InboundMessage{}, false, nil
	}

	if env.Event == nil {
		return InboundMessage{}, false, errors.New("event_callback with empty event payload")
	}
	var evt larkMessageReceiveEvent
	if err := json.Unmarshal(env.Event, &evt); err != nil {
		return InboundMessage{}, false, fmt.Errorf("event: %w", err)
	}

	msg := InboundMessage{
		EventType:    env.Header.EventType,
		EventID:      env.Header.EventID,
		AppID:        env.Header.AppID,
		ChatID:       ChatID(evt.Message.ChatID),
		ChatType:     normalizeChatType(evt.Message.ChatType),
		MessageID:    evt.Message.MessageID,
		SenderOpenID: OpenID(evt.Sender.SenderID.OpenID),
		MessageType:  evt.Message.MessageType,
		Content:      evt.Message.Content,
		CreateTime:   evt.Message.CreateTime,
		// parent_id / root_id 只在回复/引用场景下由飞书填充。enricher 以
		// ParentID（被直接引用的那条消息）作为引用回复展开的键；RootID 携带
		// 是为了完整性与将来的话题处理。
		ParentID: evt.Message.ParentID,
		RootID:   evt.Message.RootID,
		// thread_id 仅当消息在飞书话题（话题）里时才存在。出站 patcher 靠它
		// 决定是否回进该话题；空串表示普通聊天消息。
		ThreadID: evt.Message.ThreadID,
	}

	botUnionID := ""
	if inst.BotUnionID.Valid {
		botUnionID = inst.BotUnionID.String
	}

	// text + post 在这里同步压平（无外部调用——decoder 必须保持快速且零依赖）。
	// merge_forward 让 Body 留空：展开它需要一次 HTTP 往返，由下游 enricher
	// 处理，enricher 以 MessageType 为键。独立媒体给个简短可见标记，真正的
	// 二进制下载与绑定交给渠道适配器单独做。
	switch evt.Message.MessageType {
	case "text", "post":
		msg.Body = resolveMentions(flattenContent(evt.Message.MessageType, evt.Message.Content),
			evt.Message.Mentions, inst.BotOpenID, botUnionID)
	case "image", "file", "audio", "media", "video":
		msg.Body = flattenContent(evt.Message.MessageType, evt.Message.Content)
	}

	// 在任何 enricher 跑之前，先把用户自己的文本快照成命令源。enricher 会改写
	// Body（前置引用/转发上下文）但绝不碰 CommandBody，所以 `/issue …` 仍然
	// 按用户实际输入来解析。
	msg.CommandBody = msg.Body

	if msg.ChatType == ChatTypeGroup {
		msg.AddressedToBot = containsMention(evt.Message.Mentions, inst.BotOpenID, botUnionID)
	}

	return msg, true, nil
}

// larkEventEnvelope 镜像飞书对每条推送包的最外层 JSON。
type larkEventEnvelope struct {
	Schema string          `json:"schema"`
	Type   string          `json:"type"`
	Header larkEventHeader `json:"header"`
	Event  json.RawMessage `json:"event"`
}

// larkEventHeader 是事件信封的头信息（event_id、event_type 等）。
type larkEventHeader struct {
	EventID    string `json:"event_id"`
	EventType  string `json:"event_type"`
	CreateTime string `json:"create_time"`
	AppID      string `json:"app_id"`
	TenantKey  string `json:"tenant_key"`
}

// larkMessageReceiveEvent 是 im.message.receive_v1 的有据可查的负载结构。
type larkMessageReceiveEvent struct {
	Sender struct {
		SenderID struct {
			OpenID  string `json:"open_id"`
			UnionID string `json:"union_id"`
			UserID  string `json:"user_id"`
		} `json:"sender_id"`
		SenderType string `json:"sender_type"`
		TenantKey  string `json:"tenant_key"`
	} `json:"sender"`
	Message struct {
		MessageID   string        `json:"message_id"`
		ChatID      string        `json:"chat_id"`
		ChatType    string        `json:"chat_type"`
		MessageType string        `json:"message_type"`
		Content     string        `json:"content"`
		Mentions    []larkMention `json:"mentions"`
		CreateTime  string        `json:"create_time"`
		// ParentID / RootID 只在消息是回复/引用时才出现。ParentID 是被直接引用的
		// 消息；RootID 是回复树的根。
		ParentID string `json:"parent_id"`
		RootID   string `json:"root_id"`
		// ThreadID 仅对飞书话题（话题）里的消息存在。普通聊天消息飞书会省略它，
		// 所以它的存在本身就是“在话题里发生了 @”的信号。
		ThreadID string `json:"thread_id"`
	} `json:"message"`
}

// larkMention 是消息 mentions 数组里的一个 @ 对象。
type larkMention struct {
	Key string `json:"key"`
	ID  struct {
		OpenID  string `json:"open_id"`
		UnionID string `json:"union_id"`
		UserID  string `json:"user_id"`
	} `json:"id"`
	Name string `json:"name"`
}

// resolveMentions 替换飞书的 `@_user_N` 占位符，让 agent 拿到读起来自然、
// 无需自己解析 mentions 数组的正文。Bot 自己的 @ 会被剥掉（dispatcher 已经靠
// AddressedToBot 路由了事件——每条消息前再重复挂个 `@<bot>` 只会让聊天记录和
// 下游 LLM 上下文都更吵，没有信息增益）。其他参与者渲染成 `@<显示名>`；name
// 为空时防御性地保留占位符（实际飞书总会填充）。
//
// 替换是单趟 token 扫描，不是无脑 ReplaceAll。两个原因：
//
//   - 前缀冲突：一次 @ 了 11 人的聊天里会出现 `@_user_1` 和 `@_user_10`；
//     对 `@_user_1` 做 ReplaceAll 会把 `@_user_10` 的子串弄坏。这里按 key
//     长度降序排，在每个扫描位置优先尝试最长匹配，长的占位符总能赢。
//
//   - 空白保真：剥 bot @ 时只动它紧邻的一个空格——要么是占位符后面的那个，
//     要么（没有的话）退回已经输出的一个尾随空格。用户消息里的 tab、缩进、
//     代码块、表格竖线以及其他有意的空白一律原样保留。
func resolveMentions(text string, mentions []larkMention, botOpenID, botUnionID string) string {
	if text == "" || len(mentions) == 0 {
		return text
	}
	// 过滤空 key 并按最长优先排序，保证任意扫描位置 `@_user_10` 先于
	// `@_user_1` 被匹配。
	sorted := make([]larkMention, 0, len(mentions))
	for _, m := range mentions {
		if m.Key != "" {
			sorted = append(sorted, m)
		}
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		return len(sorted[i].Key) > len(sorted[j].Key)
	})

	out := make([]byte, 0, len(text))
	i := 0
	for i < len(text) {
		var matched *larkMention
		for idx := range sorted {
			if strings.HasPrefix(text[i:], sorted[idx].Key) {
				matched = &sorted[idx]
				break
			}
		}
		if matched == nil {
			out = append(out, text[i])
			i++
			continue
		}
		end := i + len(matched.Key)
		switch {
		case isBotMention(*matched, botOpenID, botUnionID):
			// 剥掉：吃掉一个相邻空格（优先占位符后的；否则回退删掉一个已输出
			// 的空格），免得接缝处留下双空格或悬空行首空格。tab/换行/其它字符
			// 不碰。
			if end < len(text) && text[end] == ' ' {
				end++
			} else if n := len(out); n > 0 && out[n-1] == ' ' {
				out = out[:n-1]
			}
		case matched.Name != "":
			out = append(out, '@')
			out = append(out, matched.Name...)
		default:
			// 未知的 @ ——保留占位符原样，至少让 agent 看到稳定的 token。
			out = append(out, matched.Key...)
		}
		i = end
	}
	return string(out)
}

// isBotMention 判断负载里的某个 @ 是否指向【本】Bot。与 containsMention 保持
// 一致：已知 union_id 时只信任它（open_id 在多 Bot 群里是结构性的反转——
// 拿它匹配会重新引入 MUL-2671 的路由 bug）。只有 union_id 缺失时才退回
// open_id——单 Bot 安装下这是对的，也是回填前旧行的最好选择。
func isBotMention(m larkMention, botOpenID, botUnionID string) bool {
	if botUnionID != "" {
		return m.ID.UnionID == botUnionID
	}
	if botOpenID == "" {
		return false
	}
	return m.ID.OpenID == botOpenID
}

// extractTextBody 从 text 类型消息的 JSON content 里取出纯文本。
func extractTextBody(content string) string {
	if content == "" {
		return ""
	}
	var doc struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(content), &doc); err != nil {
		return ""
	}
	return doc.Text
}

// normalizeChatType 把飞书字符串形态的聊天类型归一化成 ChatType；大小写不敏感，
// 未知值原样保留（防御：不会因新取值崩溃）。
func normalizeChatType(t string) ChatType {
	switch strings.ToLower(t) {
	case "p2p":
		return ChatTypeP2P
	case "group":
		return ChatTypeGroup
	default:
		return ChatType(t)
	}
}

// containsMention 回答“本群事件里是否 @ 了【本】Bot”。
//
// Bot 在 WS 各视角下稳定的标识是 `union_id`——见 MUL-2671 群聊 @ 分流。在
// 有多个 Multica Bot 的飞书群里，每个 Bot 的 WS 都会收到事件，而飞书填的
// `mentions[].id.open_id` 是按“它在跟谁说话”来填各 app 形态的：Bot Y 被 @ 时，
// Bot X 的 WS 看到的是 X 的负载形态 open_id；X 自己被 @ 时看到的是另一个
// 负载形态 open_id。只有 `union_id` 在两条 WS 流上是一致的。
//
// 匹配顺序：
//
//  1. 当知道 Bot 的 `union_id`（安装时由 GetBotInfo 抓取，落
//     lark_installation.bot_union_id）时，与 `mentions[].id.union_id` 比对。
//     这是正确路径，多 Bot 部署下无歧义。
//
//  2. `union_id` 未知时——migration 112 之前创建的单 Bot 安装，或受联系人
//     范围限制、/contact/v3/users 查不到的操作者——退回按各 app 的 `open_id`
//     比对。这在多 Bot 群聊里是结构性反转，但对 WS 大多数时候遇到的
//     p2p/单 Bot 场景是够用的，也避免回填前的安装直接硬失败。
//
// 空输入短路返回 false 而不是“匹配所有 @”，用来防一条安装行两个标识都被
// 留空的极端情况。
func containsMention(mentions []larkMention, botOpenID, botUnionID string) bool {
	if botUnionID != "" {
		for _, m := range mentions {
			if m.ID.UnionID == botUnionID {
				return true
			}
		}
		return false
	}
	if botOpenID == "" {
		return false
	}
	for _, m := range mentions {
		if m.ID.OpenID == botOpenID {
			return true
		}
	}
	return false
}
