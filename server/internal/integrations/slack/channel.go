// channel.go：Slack 出站发送器——通过 chat.postMessage 把 agent 回复发回
// Slack，以及超长正文的分片逻辑。
//
// 本文件是 Slack 集成的「出站半边」：持有一份安装的 bot 令牌（xoxb-）后，调
// Slack Web API chat.postMessage 把回复投到对应会话（必要时落进来源线程）。
// 入站事件走的是另一条、彼此独立的 Socket Mode 连接（见 slack_channel.go），
// 用的令牌不同。发送器本身不保存安装所属身份（工作区 / agent / 安装者），
// 那条信息由 Router 按消息现查后，连同凭据一起在构造时给进来。
package slack

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/slack-go/slack"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/util"
)

// TypeSlack 是 Slack 适配器在 channel 引擎里的类型判别值。刻意定义在这里而
// 不是 channel 核心包里：注册新平台不应要求改动核心代码，所以 Type 值跟随
// 各自的适配器。
const TypeSlack channel.Type = "slack"

// maxMessageRunes 是单条出站 chat.postMessage 正文的长度上限。Slack 硬性把单
// 条消息卡在约 4 万字符，这里留出余量按 38000 分片。
const maxMessageRunes = 38000

// slackSender 通过 chat.postMessage 把 agent 回复发回 Slack，是出站半边：它
// 持有该安装的 bot 令牌（xoxb-），回复必须用它发（入站事件跑在 slack_channel.go
// 里那条独立的 Socket Mode 连接上）。安装所属身份（工作区 / agent / 安装者）
// 由 Router 按消息现查后传入，因此这里没有存。
type slackSender struct {
	creds  credentials
	api    *slack.Client
	logger *slog.Logger
}

// Send 用 chat.postMessage 发一条纯文本回复；若 out.ThreadID 有值就落进该
// 线程，让解耦后的回复能回到发起它的那个会话串里。超长正文在 Slack 单条上限
// 之下自动分片；返回的 SendResult 带着每一片的 ts，而 MessageID 保持为最后一
// 片的 ts，兼容老调用方「MessageID 即最新消息」的取法。
func (c *slackSender) Send(ctx context.Context, out channel.OutboundMessage) (channel.SendResult, error) {
	return c.send(ctx, out, slack.SlackMetadata{})
}

// SendWithMetadata 与 Send 相同，只是额外把结构化 metadata 附到消息上
// （metadata 承载 binding_id / route_revision 等，供事件侧识别本条消息）。
func (c *slackSender) SendWithMetadata(ctx context.Context, out channel.OutboundMessage, metadata slack.SlackMetadata) (channel.SendResult, error) {
	return c.send(ctx, out, metadata)
}

func (c *slackSender) send(ctx context.Context, out channel.OutboundMessage, metadata slack.SlackMetadata) (channel.SendResult, error) {
	if c.api == nil {
		return channel.SendResult{}, errors.New("slack: api client not configured")
	}
	threadTS := outboundThreadTS(out)
	var lastTS string
	var messageIDs []string
	// 先把 agent 产出的标准 Markdown 转成 Slack 的 mrkdwn 再发送，否则加粗/
	// 标题/链接会以字面标记文本展示，而不是渲染成效果。
	for _, chunk := range chunkMessage(formatMrkdwn(out.Text), maxMessageRunes) {
		opts := []slack.MsgOption{
			slack.MsgOptionText(chunk, false),
			slack.MsgOptionDisableLinkUnfurl(),
		}
		if metadata.EventType != "" {
			opts = append(opts, slack.MsgOptionMetadata(metadata))
		}
		if threadTS != "" {
			opts = append(opts, slack.MsgOptionTS(threadTS))
		}
		_, ts, err := c.api.PostMessageContext(ctx, out.ChatID, opts...)
		if err != nil {
			return channel.SendResult{}, fmt.Errorf("slack: chat.postMessage: %w", err)
		}
		lastTS = ts
		messageIDs = append(messageIDs, ts)
	}
	return channel.SendResult{MessageID: lastTS, MessageIDs: messageIDs}, nil
}

// slackOutboundMetadataEvent 是给出站消息打的 metadata 事件类型名：带有它的
// 消息说明是 Multica 自己发的出站消息（区别于人工在 Slack 里直接发的消息），
// 上层事件订阅据此做回写/去重。
const slackOutboundMetadataEvent = "multica_channel_outbound"

// outboundMetadata 把出站消息的关键上下文（来源绑定、路由修订号、消息种类）
// 打包成 Slack 的消息 metadata，随消息一起发给 Slack，事件侧读回后即可把
// 事件与本次出站一一对上。
func outboundMetadata(bindingID pgtype.UUID, routeRevision int64, kind string) slack.SlackMetadata {
	return slack.SlackMetadata{EventType: slackOutboundMetadataEvent, EventPayload: map[string]any{
		"binding_id": util.UUIDToString(bindingID), "route_revision": routeRevision, "kind": kind,
	}}
}

// newSlackSender 从已解密的凭据和已配置的 API client 构造一个只发消息的
// 发送器。与出站订阅器分开构造，是为了测试时能注入指向 httptest server 的
// client。
func newSlackSender(creds credentials, api *slack.Client, logger *slog.Logger) *slackSender {
	if logger == nil {
		logger = slog.Default()
	}
	return &slackSender{creds: creds, api: api, logger: logger}
}

// outboundThreadTS 挑出出站回复要挂的 Slack thread_ts：显式的引用目标（ReplyTo）
// 优先；没有就沿用入站消息原本所属的线程。
func outboundThreadTS(out channel.OutboundMessage) string {
	if out.ReplyTo != "" {
		return out.ReplyTo
	}
	return out.ThreadID
}

// chunkMessage 按 rune（字符）边界把文本切成 <= maxRunes 的若干段，避免超长
// agent 回复顶破 Slack 的单条上限。按 rune 切是为了不把多字节字符（中文、
// emoji）劈成两半。空正文会得到一个空块（Slack 拒收真空文本，但上游调用方
// 已预先拦截了那种情况）。
func chunkMessage(text string, maxRunes int) []string {
	if maxRunes <= 0 || len([]rune(text)) <= maxRunes {
		return []string{text}
	}
	runes := []rune(text)
	var chunks []string
	for len(runes) > 0 {
		n := maxRunes
		if n > len(runes) {
			n = len(runes)
		}
		chunks = append(chunks, string(runes[:n]))
		runes = runes[n:]
	}
	return chunks
}
