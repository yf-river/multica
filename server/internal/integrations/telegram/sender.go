package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
)

// 角色定位：Telegram 出站发送的"唯一入口"。agent 的回复（engine 流式产出、sender 落
// 地）与各类判断回复（replier.post）最终都经由这里：把标准 Markdown 转成 Telegram HTML、
// 按 3500 UTF-16 码元上限分片、逐片调用 sendMessage。用户可见的静态文案 msg* 常量也
// 集中在本文件顶部（含语言选型决策记录）。
//
// 数据流向：engine → channel.OutboundMessage → 本文件 → botAPI.sendMessage → Telegram。

// bot 在 Telegram 里说的用户可见文案。用英文，凡是表达同一意思的地方与 Slack、DingTalk
// 适配器逐字对齐。
//
// 每种渠道的界面语言按"对话那头是谁"来选。下面是决策记录，免得后来的人出于好心
// "改回"中文（MUL-6509、#7349）。企业微信和飞书是中国市场产品——企微只在中国大陆，
// 它们的适配器刻意保留中文文案。Telegram 在中国大陆被屏蔽、触达的是其他所有人：
// 一个面向中文的 bot 在那里对几乎每一个用户都说错语言，尤其是用户必须先操作的
// 绑定提示。
//
// 用英文而不是语言包查询，是因为这个 bot 在**开口前**还不知道对面是谁：绑定提示是发给
// 尚未绑定用户的，而 user.language 只由设置里的语言切换器写入，从未打开过设置的
// 用户该字段恒为 NULL。英文是产品默认值（packages/core/i18n/types.ts 的
// DEFAULT_LOCALE）。
//
// 绑定提示里带着兑换链接。
const (
	msgAgentOffline     = "⚠️ The agent is offline right now. Your message was received and will be handled once it's back online."
	msgAgentArchived    = "⚠️ This agent has been archived and can't respond. Please contact your workspace admin."
	msgUnsupportedType  = "Sorry, I can't handle this kind of message yet. Please send text."
	msgBindingGroupHint = "Please message me in a direct chat first, then link your Multica account."
)

// maxMessageUnits 限制单次 sendMessage 的正文长度。Telegram 硬上限是"entity 解析后
// 4096 个 UTF-16 码元"；留 3500 是给 Markdown→HTML 转换留余量，同时按 UTF-16 计数能
// 正确处理星号字符（如 emoji 占 2 码元）。
const maxMessageUnits = 3500

// sender 通过 sendMessage 把 agent 回复发回 Telegram。只管出站半边；安装归属身份由
// Router 按消息逐条解析（s.logger/api 在 newSender 时绑定）。
type sender struct {
	api    *botAPI
	logger *slog.Logger
}

func newSender(api *botAPI, logger *slog.Logger) *sender {
	if logger == nil {
		logger = slog.Default()
	}
	return &sender{api: api, logger: logger}
}

// Send 发一条文本回复：Markdown 转 Telegram HTML + 按单条上限分片。返回的 SendResult
// 携带**最后一片**的消息 id（流式编辑要拿它继续 edit）。HTML 被 Telegram 拒收时降级成
// 纯文本重发——转换的边缘情况绝不能吃掉整条回复。
func (s *sender) Send(ctx context.Context, out channel.OutboundMessage) (channel.SendResult, error) {
	if s.api == nil {
		return channel.SendResult{}, errors.New("telegram: api client not configured")
	}
	chatID, err := strconv.ParseInt(out.ChatID, 10, 64)
	if err != nil {
		return channel.SendResult{}, fmt.Errorf("telegram: bad chat id %q: %w", out.ChatID, err)
	}
	var threadID int64
	if out.ThreadID != "" {
		threadID, _ = strconv.ParseInt(out.ThreadID, 10, 64)
	}
	var replyTo int64
	if out.ReplyTo != "" {
		replyTo = parseMessageRef(out.ReplyTo)
	}

	var lastID string
	for _, chunk := range chunkMessage(out.Text, maxMessageUnits) {
		var reply *replyParameters
		if replyTo != 0 {
			reply = &replyParameters{MessageID: replyTo, AllowSendingWithoutReply: true}
		}
		m, err := sendMessageWithRetryAfter(ctx, s.api, sendMessageParams{
			ChatID:          chatID,
			Text:            formatHTML(chunk),
			ParseMode:       "HTML",
			MessageThreadID: threadID,
			ReplyParameters: reply,
		})
		if err != nil {
			// HTML 被拒时的降级：把原始 markdown 当纯文本重发。此时**不要**重试传输层或
			// 其它 API 错误——第一次请求可能已经到达 Telegram 并被转发，盲目重发会重复。
			if !isHTMLParseError(err) {
				return channel.SendResult{}, fmt.Errorf("telegram: sendMessage: %w", err)
			}
			m, err = sendMessageWithRetryAfter(ctx, s.api, sendMessageParams{
				ChatID:          chatID,
				Text:            chunk,
				MessageThreadID: threadID,
				ReplyParameters: reply,
			})
			if err != nil {
				return channel.SendResult{}, fmt.Errorf("telegram: sendMessage: %w", err)
			}
		}
		lastID = messageKey(chatID, m.MessageID)
		replyTo = 0 // 只有第一片引用原始消息，后续分片不重复引用
	}
	return channel.SendResult{MessageID: lastID}, nil
}

// parseMessageRef 从"裸 id"或本适配器存的复合键 "chat:message" 里抽出数字消息 id。
func parseMessageRef(ref string) int64 {
	if _, after, ok := strings.Cut(ref, ":"); ok {
		ref = after
	}
	id, _ := strconv.ParseInt(ref, 10, 64)
	return id
}

// chunkMessage 把文本按 <=maxUnits 个 UTF-16 码元切成若干片，且只在 rune 边界切断。
// 优先在换行处切，这样代码块、段落能被干净地拆开（不会从某行中间腰斩）。
func chunkMessage(text string, maxUnits int) []string {
	runes := []rune(text)
	if maxUnits <= 0 || utf16Units(text) <= maxUnits {
		return []string{text}
	}
	var chunks []string
	for len(runes) > 0 {
		// 贪心扫出"再放一个字符就超限"的最长窗口。星号字符按 2 码元计。
		n := 0
		end := 0
		for i, r := range runes {
			units := 1
			if r > 0xFFFF {
				units = 2
			}
			if n+units > maxUnits {
				break
			}
			n += units
			end = i + 1
		}
		// 极端情况：单个字符就超限时至少切走一个，避免死循环。
		if end == 0 {
			end = 1
		}
		// 若窗口内最后一个换行处能留下"足够大"的第一片（> 上限一半）就退回去切在换行处，
		// 否则会产生一堆碎小分片，宁可保持容量优先。
		if i := lastIndexRune(runes[:end], '\n'); i >= 0 && utf16Units(string(runes[:i])) > maxUnits/2 {
			end = i + 1
		}
		chunks = append(chunks, strings.TrimRight(string(runes[:end]), "\n"))
		runes = runes[end:]
	}
	return chunks
}

// utf16Units 统计字符串占多少 UTF-16 码元：>0xFFFF 的星号字符（emoji、生僻字）占 2，
// 其余占 1。Telegram 的消息/消息 id 长度限制都以 UTF-16 码元计，不能用 len()（字节）或
// len(runes)（码点）。
func utf16Units(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// isHTMLParseError 判断这个 400 是不是"HTML 解析失败"（未知标签、缺收尾标签等）。
// 只有这类错误才值得降级纯文本重发——若是参数错等其它 400，降级也救不回来。
func isHTMLParseError(err error) bool {
	var ae *apiError
	if !errors.As(err, &ae) || ae.Code != http.StatusBadRequest {
		return false
	}
	description := strings.ToLower(ae.Description)
	return strings.Contains(description, "parse entities") ||
		strings.Contains(description, "unsupported start tag") ||
		strings.Contains(description, "can't find end tag")
}

// lastIndexRune 返回 rs 里等于 r 的最后一个下标，没有返回 -1。
func lastIndexRune(rs []rune, r rune) int {
	for i := len(rs) - 1; i >= 0; i-- {
		if rs[i] == r {
			return i
		}
	}
	return -1
}
