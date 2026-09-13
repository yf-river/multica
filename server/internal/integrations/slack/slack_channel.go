// slack_channel.go：单条 Socket Mode 连接的「频道实现」——每个 Slack 安装一条
// 长连接，负责收事件、翻译、交给引擎，并实现 channel.Channel 接口。
//
// BYO（自带应用）模型下，每个 Slack 安装各自持有一个 Slack 应用（自己的 xapp-
// 应用级令牌，密文存在安装 config 里），因此各自要一条独立连接——与 stage-3 的
// 按安装模型、以及现在的飞书完全一致。engine.Supervisor 通过注册的 Factory 为
// 每个 active 的 Slack 安装建一个 slackChannel，并负责租约与重连生命周期；
// Connect 会阻塞在接收循环上直到 ctx 取消。
//
// 入站事件由共享的 inbound.go 助手翻译，用【本安装】的 bot 用户 id 参数化，然后
// 交给引擎 router；router 按事件的 api_app_id（即本应用的 id，也就是按应用的路由
// 键）解析出安装。出站回复主通道是 EventChatDone 订阅器（NewOutbound）；Send
// 满足 Channel 契约，用本安装的 bot 令牌发消息。
package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"time"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
)

// slackChannel 是一个安装独占一条的 Socket Mode 连接。BYO 模型下每个 Slack
// 安装各自带一个 Slack 应用——自己的应用级令牌（xapp-，密文存在安装 config
// 里）——所以各连各的，与 stage-3 的按安装模型及现在的飞书完全一致。引擎
// engine.Supervisor 通过注册的 Factory 为每个 active 的 Slack 安装建一个
// slackChannel，并负责租约与重连生命周期；Connect 阻塞在接收循环上直到 ctx
// 取消。
//
// 入站事件由共享的 inbound.go 助手翻译，用【本安装】的 bot 用户 id 参数化，
// 然后交给引擎 router；router 按事件的 api_app_id（即本应用的 id，也就是按
// 应用的路由键）解析出安装。出站回复主通道是 EventChatDone 订阅器
// （NewOutbound）；Send 满足 Channel 契约，用本安装的 bot 令牌发消息。
type slackChannel struct {
	appID     string
	botUserID string
	appToken  string        // 已解密的 xapp- —— 给 Socket Mode 连接做鉴权
	botAPI    *slack.Client // bot 令牌客户端，出站 Send 用
	handler   channel.InboundHandler
	slash     *SlashCommandProcessor // nil 表示关闭 /issue、/new 斜杠命令处理
	logger    *slog.Logger
}

// slashCommandTimeout 限制脱离接收循环处理的 `/issue`、`/new`、`/clear` 命令
// （安装 + 身份解析、数据变更、response_url 回复）的总时长。它们在自己独立的
// context 里跑，慢吞吞的 DB 或 Slack HTTP 调用不会卡死事件投递。
const slashCommandTimeout = 10 * time.Second

func (c *slackChannel) Type() channel.Type { return TypeSlack }

func (c *slackChannel) Capabilities() channel.Capability {
	return channel.CapText | channel.CapThreadReply
}

// Disconnect 是空操作：Socket Mode 连接的整个生命周期都限定在 Connect 内（运行
// context 一取消它就返回），这里没有长期存活、需要释放的资源。与
// feishuChannel.Disconnect 一致。
func (c *slackChannel) Disconnect(ctx context.Context) error { return nil }

// Send 用本安装的 bot 令牌发一条出站回复，复用共享的 slackSender
// （Markdown→mrkdwn、分片、线程处理）。
func (c *slackChannel) Send(ctx context.Context, out channel.OutboundMessage) (channel.SendResult, error) {
	return newSlackSender(credentials{BotUserID: c.botUserID}, c.botAPI, c.logger).Send(ctx, out)
}

// Connect 打开本安装的 Socket Mode 连接（用它【自己】的应用级令牌鉴权）并跑接收
// 循环，直到 ctx 取消或链路断开。它镜像已移除的 AppConnector.connectOnce，但按
// 安装实例化：bot 身份是固定的（本安装的 bot 用户 id），不用再按事件里的 team_id
// 现查。
func (c *slackChannel) Connect(ctx context.Context) error {
	if c.handler == nil {
		return errors.New("slack: inbound handler not configured")
	}
	if c.appToken == "" {
		return errors.New("slack: app-level token not configured")
	}
	// Socket Mode 连接只靠应用级令牌鉴权；bot 令牌仅供出站 Web API 调用。
	api := slack.New("", slack.OptionAppLevelToken(c.appToken))
	sm := socketmode.New(api)

	// 每条连接都跑在【自己】可取消的 context 下。每个出口路径（handler 出错、
	// 事件流关闭、ctx 取消）都会 cancel runCtx 并等 run goroutine 观察到后退出，
	// 这样一次瞬时故障就能在 supervisor 重连前先把这条活连接拆干净——不会泄漏
	// 一个还在往无人读的 channel 里灌事件的 socket goroutine。
	runCtx, runCancel := context.WithCancel(ctx)
	runErr := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		runErr <- sm.RunContext(runCtx)
		close(done)
	}()
	defer func() {
		runCancel()
		<-done
	}()

	mentionRe := compileMentionRe(c.botUserID)
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-runErr:
			if ctx.Err() != nil {
				return nil
			}
			if err != nil {
				return err
			}
			return errors.New("slack: socket mode connection closed")
		case evt, ok := <-sm.Events:
			if !ok {
				if ctx.Err() != nil {
					return nil
				}
				return errors.New("slack: socket mode event stream closed")
			}
			if err := c.handleSocketEvent(ctx, sm, evt, mentionRe); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
		}
	}
}

// handleSocketEvent 按事件类型分流处理 Socket Mode 事件。
func (c *slackChannel) handleSocketEvent(ctx context.Context, sm *socketmode.Client, evt socketmode.Event, mentionRe *regexp.Regexp) error {
	switch evt.Type {
	case socketmode.EventTypeEventsAPI:
		eventsAPI, ok := evt.Data.(slackevents.EventsAPIEvent)
		if !ok {
			return nil
		}
		// 先 ACK：Slack 大约 3 秒就把未 ACK 的信封判过期，远低于 handler 里那些
		// DB 活的时间。ACK 与 handler 的处理结果无关，先回掉再说。
		if evt.Request != nil {
			if err := sm.Ack(*evt.Request); err != nil {
				c.logger.WarnContext(ctx, "slack: ack failed", "error", err)
			}
		}
		return c.dispatchEventsAPI(ctx, eventsAPI, mentionRe)
	case socketmode.EventTypeSlashCommand:
		// 同样先 ACK：与 Events API 信封一样，Slack 约 3 秒就会把未 ACK 的斜杠
		// 命令判过期，远低于下面 DB + Slack HTTP 的耗时。回复走命令自带的
		// response_url 带外送达，所以这里回一个空 ACK 就是对的。命令处理永远
		// 不搞垮连接（产品级结果只是临时回复，不是基建错误）。
		if evt.Request != nil {
			if err := sm.Ack(*evt.Request); err != nil {
				c.logger.WarnContext(ctx, "slack: ack slash command failed", "error", err)
			}
		}
		cmd, ok := evt.Data.(slack.SlashCommand)
		if ok {
			envelopeID := ""
			if evt.Request != nil {
				envelopeID = evt.Request.EnvelopeID
			}
			c.dispatchSlashCommand(cmd, envelopeID)
		}
		return nil
	case socketmode.EventTypeConnecting, socketmode.EventTypeConnected, socketmode.EventTypeHello:
		c.logger.DebugContext(ctx, "slack: socket mode", "event", evt.Type, "app_id", c.appID)
	case socketmode.EventTypeIncomingError, socketmode.EventTypeErrorBadMessage:
		c.logger.WarnContext(ctx, "slack: socket mode error", "event", evt.Type, "app_id", c.appID)
	default:
		if evt.Request != nil {
			_ = sm.Ack(*evt.Request)
		}
	}
	return nil
}

// dispatchEventsAPI 把一个 Events API 信封翻译成归一化入站消息交给引擎。
// handler 返回非 nil 错误属于基建故障，向上抛出让 supervisor 重连；正常的
// 「产品性丢弃」返回 nil。
func (c *slackChannel) dispatchEventsAPI(ctx context.Context, e slackevents.EventsAPIEvent, mentionRe *regexp.Regexp) error {
	var (
		msg channel.InboundMessage
		ok  bool
	)
	switch inner := e.InnerEvent.Data.(type) {
	case *slackevents.AppMentionEvent:
		msg, ok = inboundFromAppMention(e, inner, c.botUserID, mentionRe)
	case *slackevents.MessageEvent:
		msg, ok = inboundFromMessage(e, inner, c.botUserID, mentionRe)
	default:
		return nil
	}
	if !ok {
		return nil
	}
	return c.handler(ctx, msg)
}

// dispatchSlashCommand 在一个独立的、带自身超时 context 的 goroutine 里处理已
// ACK 的 `/issue`、`/new`、`/clear` 命令——数据变更和 response_url 回复绝不阻塞
// socket 接收循环（与 router 的脱离式出站路径同理）。processor 为 nil（没接线
// 斜杠处理）就直接丢弃。
func (c *slackChannel) dispatchSlashCommand(cmd slack.SlashCommand, envelopeID string) {
	if c.slash == nil {
		c.logger.Warn("slack: slash command received but no processor configured",
			"command", cmd.Command, "app_id", c.appID)
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), slashCommandTimeout)
		defer cancel()
		c.slash.HandleEnvelope(ctx, cmd, envelopeID)
	}()
}

// ChannelDeps 是 Slack Factory 闭包捕获的共享依赖。引擎的入站 handler 通过
// channel.Config.Handler 在每次构建时注入；Decrypter 把安装 config 里存着的
// 密文令牌还原成明文。
type ChannelDeps struct {
	Decrypt Decrypter
	Logger  *slog.Logger
	// Slash 处理经 Socket Mode 送来的 `/issue`、`/new` 命令。nil 表示关闭斜杠
	// 命令处理（连接仍照常服务普通消息和 @ 提及）；只测入站消息的测试传 nil。
	Slash *SlashCommandProcessor
}

// RegisterSlack 注册按安装实例化的 Slack Factory，让 engine.Supervisor 为每个
// active Slack 安装建一个并监管一条 slackChannel。「接入 Slack 入站」只需这一
// 次调用加适配器本身——不用改引擎（与 lark.RegisterFeishu 同一契约）。
func RegisterSlack(reg *channel.Registry, deps ChannelDeps) {
	reg.Register(TypeSlack, newSlackFactory(deps))
}

// newSlackFactory 构造每次安装构建 Channel 的工厂闭包：解析 config、解密出
// app/bot 令牌，组装 slackChannel。
func newSlackFactory(deps ChannelDeps) channel.Factory {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return func(cfg channel.Config) (channel.Channel, error) {
		var ic installConfig
		if err := json.Unmarshal(cfg.Raw, &ic); err != nil {
			return nil, fmt.Errorf("slack: decode installation config: %w", err)
		}
		appToken, err := decryptToken(ic.AppTokenEncrypted, deps.Decrypt)
		if err != nil {
			return nil, fmt.Errorf("slack: decrypt app token: %w", err)
		}
		if appToken == "" {
			return nil, errors.New("slack: installation has no app-level token")
		}
		botToken, err := decryptToken(ic.BotTokenEncrypted, deps.Decrypt)
		if err != nil {
			return nil, fmt.Errorf("slack: decrypt bot token: %w", err)
		}
		return &slackChannel{
			appID:     ic.AppID,
			botUserID: ic.BotUserID,
			appToken:  appToken,
			botAPI:    slack.New(botToken),
			handler:   cfg.Handler,
			slash:     deps.Slash,
			logger:    logger,
		}, nil
	}
}
