package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
)

// 角色定位：本文件是 Telegram 渠道在服务端侧的"一个安装 = 一条长轮询循环"的门面实现。
// Telegram 没有 WebSocket，官方支持的实时收消息方式是 getUpdates 长轮询——客户端阻塞地
// 挂着一个 HTTP 请求，Telegram 有消息就立刻返回。本文件把"每轮拉取 → 翻译成统一入站消息
// → 交给引擎"的整条收消息链路串起来，是全渠道收消息循环的"Telegram 方言"版本。
//
// 职责边界：只管"收"（Connect 轮询、dispatch 分发、出错时给用户发礼貌提示）。"发"
// （出站回复的 Markdown→HTML 转换、分片、引用回复）在 sender.go；与 Bot API 的底层请求
// 细节（超时、conflict/限流错误解析）在 api.go；安装包配置的解密与装拆在 install.go。
// 通用渠道协议（Channel/InboundHandler/Config 等）定义在 ../channel。
//
// 上游与数据流：被 engine.Supervisor 以"每个已安装的 Telegram bot 一条循环"的方式监督，
// 启动/停止时机与其他渠道（飞书 WS 长连接、Slack Socket Mode）完全一致。数据流：
// getUpdates 拉回 Update → dispatch 翻译为 channel.InboundMessage → 交给引擎统一入站
// 处理器；处理器报错则原样上抛给 Supervisor 触发带退避的重连。
//
// 核心约定（为什么不能同时开两条循环）：Telegram 对同一个 bot token 只允许一个消费者
// 调 getUpdates，第二个会收到 409 Conflict。这天然对应 Supervisor 的 WS 租约语义——
// 每个安装跨副本最多只有一条活跃循环，两边是一一映射，不需要额外的分布式锁。
type telegramChannel struct {
	// botID 是机器人数字 ID（即安装配置里的 app_id），打日志和判断消息是否点名本 bot 用。
	botID       int64
	botUsername string
	api         *botAPI
	handler     channel.InboundHandler
	logger      *slog.Logger
}

// pollRetryDelay 是一次 Connect 尝试内部、getUpdates 瞬时失败后重试前等待的时长。
//
// 为什么单独设这个值：一次轮询失败可能是几秒的网络抖动，立刻上抛会让 Supervisor
// 按指数退避重连，白白震荡；先在单次 Connect 内等 2 秒重试一轮，把轻微抖动就地消化，
// 只有持续失败才把错误交给 Supervisor 的退避逻辑。
const pollRetryDelay = 2 * time.Second

func (c *telegramChannel) Type() channel.Type { return TypeTelegram }

func (c *telegramChannel) Capabilities() channel.Capability {
	return channel.CapText | channel.CapThreadReply | channel.CapQuoteReply |
		channel.CapTypingIndicator | channel.CapMessageEdit
}

// Disconnect 是个空操作：轮询循环的整个生命周期都嵌套在 Connect 里（运行 ctx 被取消时
// Connect 自然返回），没有独立的连接句柄需要断开。与 slackChannel.Disconnect 的设计对齐。
func (c *telegramChannel) Disconnect(ctx context.Context) error { return nil }

// Send 用本安装的 bot token 发一条出站回复，复用共享的 sender（负责 Markdown→Telegram
// HTML 转换、超长文本分片、线程/引用定位）。
func (c *telegramChannel) Send(ctx context.Context, out channel.OutboundMessage) (channel.SendResult, error) {
	return newSender(c.api, c.logger).Send(ctx, out)
}

// Connect 运行 getUpdates 长轮询收消息循环，直到以下二者之一：
//   - ctx 被取消：正常停机，返回 nil；
//   - 链路以循环自身无法吸收的方式失败：返回错误，Supervisor 拿到错误后按退避策略重连。
//
// 409 Conflict 会以 ErrConflict 返回并附上精确说明：它意味着**另一个消费者**正在轮询这个
// bot token（可能是别的副本实例）。这种状态靠退避无法恢复，只能靠运维介入（停掉另一个
// 消费者，或给不同环境用不同 bot）。
func (c *telegramChannel) Connect(ctx context.Context) error {
	if c.handler == nil {
		return errors.New("telegram: inbound handler not configured")
	}
	// offset 从 0 开始：Telegram 会把所有积压的 update 一次性下发（含上次运行已处理过的），
	// 引擎靠 (installation, message_id) 两级去重吸收这些重复投递——这与飞书 WS 断线重连
	// 依赖的"重放容忍"是同一种机制。
	var offset int64
	for {
		updates, err := c.api.GetUpdates(ctx, offset)
		if err != nil {
			// 循环退出的唯一合法原因是 ctx 被取消（Supervisor 在停机），此时把错误吞掉、
			// 返回 nil 表示"正常停止"，避免把"我们主动叫停"误报成"链路故障"。
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, ErrConflict) {
				c.logger.WarnContext(ctx, "telegram: getUpdates conflict — this bot token is polled by another instance; stop the other consumer or use a distinct bot per environment",
					"bot_id", c.botID)
				return err
			}
			// 被 Telegram 限流（429）：服务端在响应头里给了 retry_after，按它睡够再继续，
			// 而不是立刻重试把限流打得更狠。
			if wait, ok := retryAfter(err); ok {
				c.logger.WarnContext(ctx, "telegram: getUpdates rate limited", "bot_id", c.botID, "retry_after", wait)
				if !sleepCtx(ctx, wait) {
					return nil
				}
				continue
			}
			// 瞬时网络/API 故障：在本次 Connect 内先等 pollRetryDelay 重试一次（见上面常量
			// 注释），避免一次轻微抖动就去震荡 Supervisor 的退避；若持续失败则靠连续返回
			// 错误升级为"链路故障"。
			c.logger.WarnContext(ctx, "telegram: getUpdates failed", "bot_id", c.botID, "error", err)
			if !sleepCtx(ctx, pollRetryDelay) {
				return nil
			}
			return fmt.Errorf("telegram: getUpdates: %w", err)
		}
		for _, u := range updates {
			// 推进 offset：本次收到的 update 下次不再重复拉取。用 max 而非直接赋值，是
			// 防止 Telegram 偶发返回乱序/重复批次时把 offset 往回退（往回退等于重复消费）。
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			if err := c.dispatch(ctx, u); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
		}
	}
}

// dispatch 把一条 Telegram update 翻译成入站消息并交给引擎。
//
// 错误分级很关键：
//   - handler 返回非 nil 属于基础设施故障，必须上抛——Supervisor 会重连；本轮没推进 offset
//     的 update 会重新投递，重复部分由两级去重吸收，不丢消息；
//   - 产品层面的"丢弃"（比如不认识的消息类型）一律返回 nil，不触发重连；
//   - 私聊里不支持的媒体类型、或群里明确 @bot 的不支持消息，给发送者回一条礼貌提示。
func (c *telegramChannel) dispatch(ctx context.Context, u Update) error {
	msg, ok := inboundFromUpdate(u, c.botID, c.botUsername)
	if !ok {
		return nil
	}
	if msg.Type != channel.MsgTypeText {
		// 只有"消息本来就是发给 bot 的"（私聊天然是，群聊需 @bot）才提示不支持；
		// 群聊里没 @bot 的闲聊/他人对话不该被打扰，静默忽略即可。
		if msg.Source.ChatType == channel.ChatTypeP2P || msg.AddressedToBot {
			c.notifyUnsupported(ctx, u)
		}
		return nil
	}
	if msg.Text == "" {
		return nil
	}
	if err := c.handler(ctx, msg); err != nil {
		c.notifyIssueDispatchError(msg)
		return err
	}
	return nil
}

const (
	// 引擎侧"补发失败提示"的总体超时：超过 5 秒仍未发出就放弃，不能让这个兜底动作拖住进程。
	issueErrorReplyTimeout  = 5 * time.Second
	issueDispatchFailedText = "⚠️ I couldn't create that issue because an internal error occurred. Please try again."
)

// notifyIssueDispatchError 防止"用户 @bot 发 /issue、但引擎在能产出正常结果之前就报基础设施
// 错误"时静默失败——用户那边看起来就是 bot 已读不回。
//
// 兜底动作在独立 goroutine 里执行、与轮询循环解耦，这样 Supervisor 不需要等这个"尽力而为"
// 的提示发完就能立刻重连。只有确认是"点名发给 bot 的 /issue 命令"才触发（普通消息失败
// 不需要专门打扰用户）。
func (c *telegramChannel) notifyIssueDispatchError(msg channel.InboundMessage) {
	if !isAddressedIssueCommand(msg) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), issueErrorReplyTimeout)
		defer cancel()
		chatID, err := strconv.ParseInt(msg.Source.ChatID, 10, 64)
		if err != nil {
			c.logger.Warn("telegram: issue dispatch-error reply has invalid chat id", "error", err)
			return
		}
		var threadID int64
		if msg.Source.ThreadID != "" {
			threadID, _ = strconv.ParseInt(msg.Source.ThreadID, 10, 64)
		}
		if _, err := c.api.SendMessage(ctx, sendMessageParams{
			ChatID:          chatID,
			Text:            issueDispatchFailedText,
			MessageThreadID: threadID,
			ReplyParameters: optionalReplyParameters(parseMessageRef(msg.MessageID)),
		}); err != nil {
			c.logger.WarnContext(ctx, "telegram: issue dispatch-error reply failed", "error", err)
		}
	}()
}

// notifyUnsupported 告诉"正在跟 bot 对话"的发送者：他发的这种非文本消息类型暂不支持。
// 在嘈杂的群里提示必须保留话题路由并引用触发消息，用户才能一眼看出提示对应哪条消息。
// 尽力而为：失败只记日志，不再重试。
func (c *telegramChannel) notifyUnsupported(ctx context.Context, u Update) {
	if u.Message == nil {
		return
	}
	if _, err := c.api.SendMessage(ctx, sendMessageParams{
		ChatID:          u.Message.Chat.ID,
		Text:            msgUnsupportedType,
		MessageThreadID: u.Message.MessageThreadID,
		ReplyParameters: &replyParameters{
			MessageID:                u.Message.MessageID,
			AllowSendingWithoutReply: true,
		},
	}); err != nil {
		c.logger.WarnContext(ctx, "telegram: unsupported-type notice failed", "error", err)
	}
}

// sleepCtx 睡 d 时长，或直到 ctx 结束（谁先到算谁）；返回 false 表示 ctx 先结束、
// 调用方应当停止而不是继续重试。
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// ChannelDeps 是 Telegram Factory 闭包捕获的共享依赖（进程级单例，所有安装共用）。
// 引擎的入站 handler 比较特殊：它不在这里，而是由 Superviser 在每次构建渠道实例时
// 通过 channel.Config.Handler 注入（不同安装会拿到同一个全局 handler）。
type ChannelDeps struct {
	Decrypt Decrypter
	Logger  *slog.Logger
	// APIBase 覆盖 Bot API 域名（仅测试用）。空值用生产地址。
	APIBase string
	// HTTPClient 覆盖轮询用的 HTTP 客户端（仅测试用）。空值用默认客户端，
	// 其超时按"长轮询挂起时间"专门调过。
	HTTPClient *http.Client
}

// RegisterTelegram 把"按安装构建 Telegram 渠道"的 Factory 注册进注册表，engine.Supervisor
// 就能为每个活跃的 Telegram 安装构建并监督一条轮询循环。与 lark.RegisterFeishu /
// slack.RegisterSlack 同一套契约——新增渠道不需要改动引擎代码。
func RegisterTelegram(reg *channel.Registry, deps ChannelDeps) {
	reg.Register(TypeTelegram, newTelegramFactory(deps))
}

// newTelegramFactory 把进程级共享依赖 + 每安装的安装配置（config.go 里 json.Unmarshal 的
// installConfig）合成为一条 telegramChannel。返回的函数每次被 Superviser 调用都重新解密
// token——这样 bot token 在安装记录里轮换后，重启渠道即可生效，无需重启进程。
func newTelegramFactory(deps ChannelDeps) channel.Factory {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return func(cfg channel.Config) (channel.Channel, error) {
		var ic installConfig
		if err := json.Unmarshal(cfg.Raw, &ic); err != nil {
			return nil, fmt.Errorf("telegram: decode installation config: %w", err)
		}
		token, err := decryptToken(ic.BotTokenEncrypted, deps.Decrypt)
		if err != nil {
			return nil, fmt.Errorf("telegram: decrypt bot token: %w", err)
		}
		if token == "" {
			return nil, errors.New("telegram: installation has no bot token")
		}
		botID, err := strconv.ParseInt(ic.AppID, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("telegram: installation app_id is not a bot id: %w", err)
		}
		return &telegramChannel{
			botID:       botID,
			botUsername: ic.BotUsername,
			api:         newBotAPI(deps.APIBase, token, deps.HTTPClient),
			handler:     cfg.Handler,
			logger:      logger,
		}, nil
	}
}
