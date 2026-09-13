// ack.go：钉钉的「处理中」ACK 通知——代替打字指示灯的角色。
//
// 背景：经典机器人 API（oToMessages/batchSend）不提供按消息的 reaction，agent
// 跑一次长任务时，手机端用户会盯着沉默直到回复落下。（钉钉的 AI-assistant 栈
// 通过 ackReactionScope 提供 reaction 能力，但 v0.9.1 的 stream SDK 还没把它
// 暴露出来。）ack 通知器当打字指示灯的替代：摄取后先发一条轻量的「正在处理」，
// 用户立刻知道消息已被收到。
//
// 实现 engine.TypingNotifier：引擎在接受一轮 turn、持久化并排入 agent 运行后调
// OnIngested。/issue 这类会同步返回的终止命令不补这条「不可撤回的处理确认」。
package dingtalk

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/util"
)

// ackProcessingText 是这条顶替「正在打字」的确认文案。刻意保持简短：它是真实、
// 不可撤回的聊天消息，不是临时指示灯。
const ackProcessingText = "👀 On it — I'll reply here when it's ready."

// ackCoalesceWindow 抑制同一会话的重复 ack。取值略高于 run 的防抖窗口：一批涌进
// 同一轮 run 的消息只出一条 ack，而真正晚到的下一轮会再确认一次。
const ackCoalesceWindow = 5 * time.Second

// ackNotifier 负责发出处理 ack，并按会话做突发合并。
type ackNotifier struct {
	client  *Client
	decrypt Decrypter
	logger  *slog.Logger
	window  time.Duration
	now     func() time.Time

	mu      sync.Mutex
	lastAck map[string]time.Time

	// sendText 把文本投进该安装的会话。nil 时走真实 Open-API 发送；测试注入
	// recorder。
	sendText func(ctx context.Context, inst engine.ResolvedInstallation, msg channel.InboundMessage, text string) error
}

var _ engine.TypingNotifier = (*ackNotifier)(nil)

// NewAckNotifier 基于共享出站 client 与凭据解密器构造 ack 通知器。
func NewAckNotifier(client *Client, decrypt Decrypter, logger *slog.Logger) *ackNotifier {
	if logger == nil {
		logger = slog.Default()
	}
	return &ackNotifier{
		client:  client,
		decrypt: decrypt,
		logger:  logger,
		window:  ackCoalesceWindow,
		lastAck: make(map[string]time.Time),
	}
}

// OnIngested 发出处理 ack——除非同一会话最近一条 ack 仍在合并窗口内。
func (n *ackNotifier) OnIngested(ctx context.Context, inst engine.ResolvedInstallation, msg channel.InboundMessage, sessionID pgtype.UUID) {
	if n.suppress(sessionID) {
		return
	}
	send := n.sendText
	if send == nil {
		send = n.realSend
	}
	if err := send(ctx, inst, msg, ackProcessingText); err != nil {
		n.logger.WarnContext(ctx, "dingtalk ack: send failed",
			"installation_id", util.UUIDToString(inst.ID), "error", err)
	}
}

// OnSettled 清掉会话的去重记录，让它的下一轮立刻重新 ack。
func (n *ackNotifier) OnSettled(_ context.Context, sessionID pgtype.UUID) {
	key := util.UUIDToString(sessionID)
	if key == "" {
		return
	}
	n.mu.Lock()
	delete(n.lastAck, key)
	n.mu.Unlock()
}

// suppress 判断对 sessionID 的 ack 是否该跳过；不该跳就顺手记下这次 ack。检查加
// 记录是原子的，同一波并发摄取只会出一条 ack。
func (n *ackNotifier) suppress(sessionID pgtype.UUID) bool {
	key := util.UUIDToString(sessionID)
	if key == "" {
		return false
	}
	now := n.clock()
	n.mu.Lock()
	defer n.mu.Unlock()
	if last, ok := n.lastAck[key]; ok && now.Sub(last) < n.window {
		return true
	}
	// 插入前先把超出窗口的条目清掉。OnSettled 只对「没投出任务」的运行触发，
	// 会派任务的会话不清理就会永久泄漏条目。过期条目本就是死数据（后面的轮次
	// 会重新 ack），而这段只跑在缓存未命中时，map 大小因此被窗口内见过的会话数
	// 框住。
	for k, last := range n.lastAck {
		if now.Sub(last) >= n.window {
			delete(n.lastAck, k)
		}
	}
	n.lastAck[key] = now
	return false
}

func (n *ackNotifier) clock() time.Time {
	if n.now != nil {
		return n.now()
	}
	return time.Now()
}

// realSend 是生产路径：把文案发进该安装的会话。
func (n *ackNotifier) realSend(ctx context.Context, inst engine.ResolvedInstallation, msg channel.InboundMessage, text string) error {
	_, err := sendInstallationText(ctx, n.client, n.decrypt, inst, targetFromMessage(msg), text)
	return err
}
