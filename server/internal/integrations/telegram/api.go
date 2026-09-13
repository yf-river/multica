package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// 角色定位：对 Telegram Bot API 的极简手写客户端——只实现本适配器需要的五个方法：
// getMe（安装时校验 token）、getUpdates（收消息长轮询）、sendMessage / editMessageText
// （出站回复 + 流式刷新）、sendChatAction（"typing…" 输入指示）。
//
// 为什么不用 SDK：Bot API 就是纯 JSON over HTTPS，一个薄薄的 net/http 封装足够，
// 引入 SDK 只会白白增加依赖面。错误的语义化也集中在这里：把 429 限流（带 retry_after）、
// 409 冲突（另一实例也在轮询同一 bot）解析成可区分处理的错误类型。
// 文件内部组织：先常量与错误类型，再是唯一请求入口 call，然后是各 API 方法。

// defaultAPIBase 是生产环境的 Bot API 域名。测试通过把 base 指向 httptest server 绕过。
const defaultAPIBase = "https://api.telegram.org"

// longPollTimeoutSecs 是 getUpdates 的服务端挂起秒数。选 50 秒：既低于常见 60 秒的
// 代理/负载均衡空闲超时（避免被中间层掐断连接），又能把请求频率压到很低。
const longPollTimeoutSecs = 50

// ErrConflict 表示 getUpdates 收到 409：同一个 bot token 正被另一个消费者轮询
// （另一个 Multica 实例、另一个 worktree、或某个外部进程）。轮询循环把 409 当作
// 本次连接的致命错误上抛，并附带精确说明，让运维一眼看到"bot 在他处被轮询"，
// 而不是陷入无声的"抢消息大战"。
var ErrConflict = errors.New("telegram: bot is already being polled by another instance (409 conflict)")

// requestError 故意不让 Error() 带上请求 URL。为什么：Bot API 的 URL 里就嵌着 bot token，
// 而 net/http 的传输层错误可能把 URL 一并包含进去。通过 Unwrap 保留对取消/传输错误的
// 分类判断，同时保证外层错误字符串里永远不可能打印出凭据（token）——防止日志泄密。
type requestError struct {
	method string
	cause  error
}

func (e *requestError) Error() string { return fmt.Sprintf("telegram: %s request failed", e.method) }

func (e *requestError) Unwrap() error { return e.cause }

// apiError 是 Bot API 返回非 ok 时的响应错误。
type apiError struct {
	Code        int
	Description string
	// RetryAfter 是 429 时 Telegram 强制要求的退避秒数。
	RetryAfter int
}

func (e *apiError) Error() string {
	return fmt.Sprintf("telegram api: %d %s", e.Code, e.Description)
}

// retryAfter 从错误里读出 429 的退避时长；若该错误不携带退避信息则返回 false。
// 注意 429 但没给 retry_after 的兜底是 1 秒——Telegram 限流时宁可多等不能立刻重试。
func retryAfter(err error) (time.Duration, bool) {
	var ae *apiError
	if errors.As(err, &ae) && ae.Code == http.StatusTooManyRequests {
		secs := ae.RetryAfter
		if secs <= 0 {
			secs = 1
		}
		return time.Duration(secs) * time.Second, true
	}
	return 0, false
}

// botAPI 是某一个 bot 的 API 客户端（一个安装持有一个，含各自的 token）。
type botAPI struct {
	base   string // API 域名，结尾不带斜杠
	token  string
	client *http.Client
}

// newBotAPI 组装客户端。base 为空用生产域名；client 为空用带超时的默认客户端——
// 65 秒比 getUpdates 的 50 秒服务端挂起略长，保证长轮询挂起期间不会被自己的客户端
// 超时掐断。
func newBotAPI(base, token string, client *http.Client) *botAPI {
	if base == "" {
		base = defaultAPIBase
	}
	if client == nil {
		client = &http.Client{Timeout: 65 * time.Second}
	}
	return &botAPI{base: base, token: token, client: client}
}

// envelope 是 Bot API 的响应外层包装（所有方法共用同一个形状）。
type envelope struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Parameters  *struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// call POST 一次 Bot API 方法并把 result 解码进 out（out 为 nil 时跳过解码）。
// API 层失败返回 *apiError；getUpdates 的 409 由调用方（GetUpdates）再映射为 ErrConflict。
func (a *botAPI) call(ctx context.Context, method string, params any, out any) error {
	var body io.Reader
	if params != nil {
		buf, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("telegram: encode %s params: %w", method, err)
		}
		body = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.base+"/bot"+a.token+"/"+method, body)
	if err != nil {
		return fmt.Errorf("telegram: build %s request: %w", method, err)
	}
	if params != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.client.Do(req)
	if err != nil {
		// 网络/传输层错误一律包成 requestError：外层不再重复拼接 URL，见其类型注释。
		return &requestError{method: method, cause: err}
	}
	defer resp.Body.Close()
	var env envelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return fmt.Errorf("telegram: decode %s response (http %d): %w", method, resp.StatusCode, err)
	}
	if !env.OK {
		ae := &apiError{Code: env.ErrorCode, Description: env.Description}
		if env.Parameters != nil {
			ae.RetryAfter = env.Parameters.RetryAfter
		}
		return ae
	}
	if out != nil {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return fmt.Errorf("telegram: decode %s result: %w", method, err)
		}
	}
	return nil
}

// User 是 Bot API 的 User 对象（getMe 的返回值 / 消息发送者）。
type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name,omitempty"`
	Username  string `json:"username,omitempty"`
}

// Chat 是 Bot API 的 Chat 对象。
type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"` // 取值："private"、"group"、"supergroup"、"channel"
}

// Message 是本适配器实际会读取的 Bot API Message 对象子集。
// 只有判类型需要那几个媒体字段，所以用空 struct 占位只表达"存在"。
type Message struct {
	MessageID       int64           `json:"message_id"`
	From            *User           `json:"from"`
	Chat            Chat            `json:"chat"`
	Date            int64           `json:"date"`
	Text            string          `json:"text"`
	Entities        []MessageEntity `json:"entities,omitempty"`
	Caption         string          `json:"caption,omitempty"` // 媒体消息的文字说明
	CaptionEntities []MessageEntity `json:"caption_entities,omitempty"`
	ReplyToMessage  *Message        `json:"reply_to_message,omitempty"`
	MessageThreadID int64           `json:"message_thread_id,omitempty"` // 论坛话题 id
	IsTopicMessage  bool            `json:"is_topic_message,omitempty"`
	Photo           []any           `json:"photo,omitempty"`
	Document        *struct {
		FileName string `json:"file_name"`
	} `json:"document,omitempty"`
	Voice   *struct{} `json:"voice,omitempty"`
	Video   *struct{} `json:"video,omitempty"`
	Sticker *struct{} `json:"sticker,omitempty"`
}

// MessageEntity 是 Bot API 对 @提及、斜杠命令、链接和格式化文本的结构化标注。
// 注意：Offset 和 Length 以 UTF-16 码元为单位，不是 UTF-8 字节偏移，也不是 Go 的 rune 下标。
type MessageEntity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
}

// Update 是 getUpdates 返回列表里的一项。v1 只消费纯新消息（message 字段）；
// 消息编辑、频道动态、按钮回调查询一律忽略。
type Update struct {
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message"`
}

// GetMe 校验 token 是否有效，并返回机器人自身的身份信息。
func (a *botAPI) GetMe(ctx context.Context) (User, error) {
	var u User
	err := a.call(ctx, "getMe", nil, &u)
	return u, err
}

// WebhookInfo 是启动长轮询安装前需要检查的 Bot API webhook 状态子集。
type WebhookInfo struct {
	URL                string `json:"url"`
	PendingUpdateCount int    `json:"pending_update_count"`
}

// GetWebhookInfo 探测是否存在会让 getUpdates 失效的 webhook
// （bot 一旦设置了 webhook，getUpdates 就不再推送消息）。
func (a *botAPI) GetWebhookInfo(ctx context.Context) (WebhookInfo, error) {
	var info WebhookInfo
	err := a.call(ctx, "getWebhookInfo", nil, &info)
	return info, err
}

type getUpdatesParams struct {
	Offset         int64    `json:"offset,omitempty"`
	Timeout        int      `json:"timeout"`
	AllowedUpdates []string `json:"allowed_updates"`
}

// GetUpdates 长轮询拉取新消息。409 在这里统一翻译成 ErrConflict。
func (a *botAPI) GetUpdates(ctx context.Context, offset int64) ([]Update, error) {
	var updates []Update
	err := a.call(ctx, "getUpdates", getUpdatesParams{
		Offset:  offset,
		Timeout: longPollTimeoutSecs,
		// 把推送流限制成纯 message：唤醒次数更少，且编辑/回应表情/频道动态永远不会
		// 进入处理管线。
		AllowedUpdates: []string{"message"},
	}, &updates)
	if err != nil {
		var ae *apiError
		if errors.As(err, &ae) && ae.Code == http.StatusConflict {
			return nil, ErrConflict
		}
		return nil, err
	}
	return updates, nil
}

type sendMessageParams struct {
	ChatID          int64            `json:"chat_id"`
	Text            string           `json:"text"`
	ParseMode       string           `json:"parse_mode,omitempty"` // "HTML"；纯文本调用留空
	MessageThreadID int64            `json:"message_thread_id,omitempty"`
	ReplyParameters *replyParameters `json:"reply_parameters,omitempty"`
}

// replyParameters 是 Telegram 现在的"引用回复"表达形态。旧字段 reply_to_message_id
// 已不在 sendMessage 参数里。
type replyParameters struct {
	MessageID                int64 `json:"message_id"`
	AllowSendingWithoutReply bool  `json:"allow_sending_without_reply,omitempty"`
}

// SendMessage 发一条消息。需要发"转换后的 Markdown"的调用方自己带 HTML 解析模式；
// 纯文本调用让 parseMode 留空。
func (a *botAPI) SendMessage(ctx context.Context, p sendMessageParams) (Message, error) {
	var m Message
	err := a.call(ctx, "sendMessage", p, &m)
	return m, err
}

// sendMessageWithRetryAfter 在遇到 Telegram 明确的 429 退避时只重试一次。
// 刻意不去重试传输层或其他 API 错误——因为"响应丢失"可能意味着 Telegram 其实已经
// 收下并转发了这条消息，盲目重发会产生重复消息。
func sendMessageWithRetryAfter(ctx context.Context, a *botAPI, p sendMessageParams) (Message, error) {
	m, err := a.SendMessage(ctx, p)
	if wait, ok := retryAfter(err); ok {
		if !sleepCtx(ctx, wait) {
			return Message{}, ctx.Err()
		}
		return a.SendMessage(ctx, p)
	}
	return m, err
}

type editMessageTextParams struct {
	ChatID    int64  `json:"chat_id"`
	MessageID int64  `json:"message_id"`
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode,omitempty"`
}

// EditMessageText 覆盖替换一条已发送消息的文本——这正是"流式输出"的原语：agent 生成一点
// 就 edit 一次。Telegram 对"文本没变化"返回的错误由调用方吞掉。
func (a *botAPI) EditMessageText(ctx context.Context, p editMessageTextParams) error {
	return a.call(ctx, "editMessageText", p, nil)
}

// SendChatAction 在触发消息所在的论坛话题里展示约 5 秒的原生 "typing…" 指示（适用时）。
func (a *botAPI) SendChatAction(ctx context.Context, chatID, messageThreadID int64) error {
	return a.call(ctx, "sendChatAction", struct {
		ChatID          int64  `json:"chat_id"`
		Action          string `json:"action"`
		MessageThreadID int64  `json:"message_thread_id,omitempty"`
	}{
		ChatID: chatID, Action: "typing", MessageThreadID: messageThreadID,
	}, nil)
}
