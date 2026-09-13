// outbound.go —— WeCom 的 EventChatDone 订阅者。agent 在总线上跑完一次会话回复
// 后，这个订阅者查出 wecom 的 chat_session 绑定、经共享注册表解析出活的
// wsSender、把回复以 aibot_send_msg 推回去。与 slack.Outbound 对齐；没有 wecom
// 绑定的会话被忽略，所以它能和共享总线上的 Slack / Lark 订阅者共存。
//
// 保持精简：aibot 没有 thread、没有按机器人的出站 REST、没有 mrkdwn 转换——回复
// 文本和 OutboundReplier 的消息走同一条 sendMsgTextBody（markdown msgtype，渲染
// 纯文本无需转义）。
//
// 副本拓扑：企微唯一的出站路径是 sendersRegistry 持有的进程内 WebSocket，而
// EventChatDone / EventInboxNew 在进程内 events.Bus 上分发——所以发布事件的副本
// 未必是握着该机器人 WS 租约的那个（Slack/Lark 免疫——它们的出站是任何副本都能
// 做的无状态 HTTP）。
//
// 在带分片 / 双副本 realtime relay 的部署里，非租约方产生的回复或收件箱推送会经
// relay 转发给租约持有方（relay_outbound.go），单副本约束不再适用于路由。没有
// relay——遗留模式、或没有 REDIS_URL——约束照旧：WeCom 启用后端必须单副本跑。
// 无论哪种模式，当没有任何副本握着活连接（全都处于重连中）时产生的投递仍会丢；
// 这个残余窗口是 relay 刻意不解决的持久化问题。启动日志会说明当前处于哪种体制。
// 见 router.go。
package wecom

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// outboundQueries 是 WeCom 出站订阅者需要的生成查询切片。*db.Queries 满足它。
type outboundQueries interface {
	GetChannelTaskDelivery(ctx context.Context, taskID pgtype.UUID) (db.ChannelTaskDelivery, error)
	GetAgentTask(ctx context.Context, id pgtype.UUID) (db.AgentTaskQueue, error)
	TaskHasChannelIngestedMessages(ctx context.Context, taskID pgtype.UUID) (bool, error)
	GetChannelInstallation(ctx context.Context, arg db.GetChannelInstallationParams) (db.ChannelInstallation, error)
	FindChannelBindingForMember(ctx context.Context, arg db.FindChannelBindingForMemberParams) (db.ChannelUserBinding, error)
	GetWorkspace(ctx context.Context, id pgtype.UUID) (db.Workspace, error)
	ListAttachmentsByChatMessage(ctx context.Context, arg db.ListAttachmentsByChatMessageParams) ([]db.Attachment, error)
}

// Outbound 把 agent 的会话回复经入站循环拥有的同一条 aibot WebSocket 送回企微。
// 注册在共享事件总线上；没有 wecom 绑定的会话被静默忽略。
type Outbound struct {
	q       outboundQueries
	senders *sendersRegistry
	logger  *slog.Logger

	// objects 是部署的对象存储，没有则为 nil。非 nil 正是「打开文件投递」的开
	// 关（outbound_media.go）。
	objects mediaObjectStore

	// spawn 跑一次附件投递。做成字段而不是裸 `go`，让测试能内联运行它、确定性
	// 地观察结果。
	spawn func(func())

	// metrics 统计每条回复最后怎么了。nil 即丢弃；为什么要有丢弃细分见
	// outbound_outcome.go。
	metrics Metrics

	// relay 在「本副本没握着机器人 socket」时把回复路由给握着的那个。无 Redis 的
	// 部署为 nil，此时它也不需要：一个副本既发布又握 socket。
	relay *RelayOutbound

	// 两个计数器约束附件投递，是两个而不是一个，因为一个数没法同时待在两个地
	// 方。
	//
	// admittedAttachments 统计本订阅者已启动、还没看到返回的 goroutine。它在
	// spawn 之前占位，所以它既约束每个 goroutine 要跑的附件查询、也约束 goroutine
	// 本身。此刻对那一轮还一无所知，超出它只能记日志。
	//
	// pendingAttachments 统计「已查过那一轮、发现有文件」的投递。它在查询之后占
	// 位，正因如此，一个因容量不足被拒的投递可以在从未对「一个根本不存在的文件」
	// 报警的前提下上报给用户。
	//
	// admitted 上限刻意取两者中较大那个，这样一批「确实带文件」的积压会先填满
	// pending 上限、并在能说明丢了什么的那条路径上被拒。顶到 admitted 上限并不
	// 意味着 pending 满了：admission 贯穿 goroutine 的整个生命周期——包括它的查
	// 询、包括那些查完发现根本没文件的轮次——而那些从不占 pending 槽。
	pendingMu           sync.Mutex
	pendingAttachments  int
	admittedAttachments int
}

// NewOutbound 构造 WeCom 出站订阅者。senders 是和 wecom.ChannelDeps、
// OutboundReplier 用同一张进程级注册表——回复投递走「绑定所属安装的活 wsSender」，
// 所以 Supervisor 中途丢了租约的会话会静默丢弃，而不是另开第二条连接。
//
// WithAttachments 是唯一的选项：传进部署的对象存储后，agent 产出的文件就会跟在
// 回答后面送进会话。
func NewOutbound(q outboundQueries, senders *sendersRegistry, logger *slog.Logger, opts ...OutboundOption) *Outbound {
	if logger == nil {
		logger = slog.Default()
	}
	o := &Outbound{
		q:       q,
		senders: senders,
		logger:  logger,
		spawn:   func(f func()) { go f() },
	}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

// Register 在总线上订阅会话完成事件。
func (o *Outbound) Register(bus *events.Bus) {
	bus.Subscribe(protocol.EventChatDone, o.handleEvent)
	// 经智能机器人投递的收件箱通知：当接收成员有带活连接的 WeCom 绑定、其
	// inbox:new 条目会被以 markdown 卡片推到 aibot。
	bus.Subscribe(protocol.EventInboxNew, o.handleInboxNew)
}

func (o *Outbound) handleEvent(e events.Event) {
	// 总线投递是同步的——一次卡住的 WS 写绝不能楔死发布调用点。用带紧凑超时的
	// 新 ctx，和 Slack 一样。
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// 「未投递的回复」只在一个人手里记录，所以一次丢弃被恰好计一次、且总带原
	// 因。processEvent 里那些「以自己的错误结束一轮」的分支各自记录并返回 nil；
	// 浮到这里的都由错误分类而来。
	if err := o.processEvent(ctx, e); err != nil {
		if reason := unconfirmedReason(err); reason != "" {
			o.unconfirmed(ctx, e, reason, err)
		} else {
			o.dropped(ctx, e, classifyDrop(err), err)
		}
	}
}

func (o *Outbound) processEvent(ctx context.Context, e events.Event) error {
	sessionID, err := util.ParseUUID(e.ChatSessionID)
	if err != nil || !sessionID.Valid {
		// Issue / autopilot 类任务不带 chat_session。
		return nil
	}
	// 空完成通常在此结束这一轮。agent 产出了文件却只字未提时不在此结束：平台会
	// 为这种情况专写一条 assistant 消息，现在就返回等于把这件活扔掉。
	content := chatDoneContent(e.Payload)
	if content == "" && !o.mayCarryAttachments(e) {
		o.skipped(ctx, e, skipNothingToSay)
		return nil
	}
	// 能走到这里的都是「已绑定、非空」的完成，所以在加载凭证或发送之前先分清任
	// 务来源。一条在 Multica Web 界面问的问题可以复用一条发源于 WeCom 的会话——
	// 而它的答案只属于 Multica。没有这个闸门，答案会被推进企微会话；在群里就意味
	// 着推到房间里每个人面前。slack/outbound.go:118 以及飞书、钉钉的对应实现都在
	// 这里设闸；WeCom 曾是唯一不设的。
	//
	// 失败即关闭：无法确证来源的不投递。
	//
	// 上面这一切都是读操作。请保持这样：闸门必须始终赶在「任何消费或改写本轮的
	// WeCom 侧状态」之前，因为一条不能进房间的答案，也不能接管房间的消息位置。
	taskID, ok := chatDoneTaskID(e)
	if !ok {
		o.dropped(ctx, e, dropTaskMissing, nil)
		return nil
	}
	delivery, err := o.q.GetChannelTaskDelivery(ctx, taskID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("wecom: lookup task delivery: %w", err)
	}
	if delivery.ChannelType != channelTypeWecom {
		return nil
	}
	binding := wecomBindingFromTaskDelivery(delivery)
	task, err := o.q.GetAgentTask(ctx, taskID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// 它的完成还在路上时被取消并删除。
			o.dropped(ctx, e, dropTaskMissing, nil)
			return nil
		}
		return fmt.Errorf("wecom: load agent task: %w", err)
	}
	deliver, err := engine.TaskInputIsChannelIngested(ctx, o.q, task)
	if err != nil {
		return fmt.Errorf("wecom: classify task input origin: %w", err)
	}
	if !deliver {
		o.skipped(ctx, e, skipOriginNotChannel)
		return nil
	}
	inst, err := o.q.GetChannelInstallation(ctx, db.GetChannelInstallationParams{
		ID:          binding.InstallationID,
		ChannelType: channelTypeWecom,
	})
	if err != nil {
		return fmt.Errorf("wecom: load installation: %w", err)
	}
	if inst.Status != string(InstallationActive) {
		o.skipped(ctx, e, skipInstallationInactive) // 触发与回复之间被撤销
		return nil
	}
	if o.senders == nil {
		return errors.New("wecom: sender registry not configured")
	}
	chatType := aibotChatTypeFromChannel(channel.ChatType(binding.ChatType))
	sender := o.senders.get(inst.ID)
	if sender == nil {
		// 放弃之前先试一次：这条回复可能只是「在错的副本上产生」。把它交给握着
		// socket 的那一个。
		//
		// 由实际送达它的副本计数，而不是在这里——所以一条被路由然后送达的回复
		// 只出现一次，在发送者的那一侧。一条在「所有副本都在重连」时被路由的回
		// 复没人读、也没人计；那个窗口正是这份代码刻意不解决的持久化问题
		// （relay_outbound.go）。
		if o.relay.publish(relayFrame{
			Kind:           relayKindReply,
			InstallationID: util.UUIDToString(inst.ID),
			ChatID:         binding.ChannelChatID,
			ChatType:       chatType,
			Content:        content,
			TaskID:         util.UUIDToString(taskID),
			MessageID:      chatDoneMessageID(e.Payload),
			WorkspaceID:    e.WorkspaceID,
			SessionID:      e.ChatSessionID,
			CarriesFiles:   o.mayCarryAttachments(e),
		}, relayEventID(e, taskID)) {
			o.logger.DebugContext(ctx, "wecom outbound: routed to the replica holding the socket",
				"installation_id", util.UUIDToString(inst.ID), "chat_session_id", e.ChatSessionID)
			return nil
		}
		// 本副本上没有这条安装的活 WS。两种原因：(1) Supervisor 丢了租约或正在重
		// 连——是瞬时的，用户的下一条入站消息会到达重连后的循环；(2) 多副本部署
		// 上租约被「发布这个事件的副本」之外的另一个副本握着，所以这里永远无法送
		// 达（见本文件头部注释里的单副本约束）。无论哪种，缓冲都不对——socket 回
		// 来时回复早已过期——所以把它浮给调用方的 WARN，而不是静默丢弃。
		return errNoLiveConnection
	}
	// 话先走。空完成能走到这里只因为绑了一个文件，而文件前多一只空 markdown 气
	// 泡，是用户得滚过去的噪音。
	if content != "" {
		if err := sender.sendTextCtx(ctx, binding.ChannelChatID, chatType, content); err != nil {
			return err
		}
		o.delivered()
	}
	// 然后，agent 在话之外产出的任何东西，作为独立消息发出去——企微回复无法内联
	// 携带文件。
	// carriesTheReply 为 true 时文件就是答案：空完成只因绑了文件才走到这里，所以
	// 这条回复还没被计过任何数，附件路径欠着它的结局。
	o.deliverAttachments(e, attachmentTarget{
		InstallationID: binding.InstallationID,
		ChatID:         binding.ChannelChatID,
		ChatType:       chatType,
		SessionID:      e.ChatSessionID,
	}, content == "")
	return nil
}

func wecomBindingFromTaskDelivery(delivery db.ChannelTaskDelivery) db.ChannelChatSessionBinding {
	return db.ChannelChatSessionBinding{
		ID: delivery.BindingID, InstallationID: delivery.InstallationID,
		ChannelType: delivery.ChannelType, ChannelChatID: delivery.ChannelChatID,
		ChatType:      delivery.ChatType,
		LastMessageID: delivery.ChannelMessageID, LastThreadID: delivery.ChannelThreadID,
		RouteRevision: delivery.RouteRevision, Config: delivery.Config,
	}
}

// chatDoneTaskID 找回一条 EventChatDone 属于哪个任务。优先用信封的 TaskID，payload
// 作兜底——service.broadcastChatDone 设置 ChatDonePayload.TaskID、把信封的留空，
// 所以实际上兜底路径才是活的。
func chatDoneTaskID(e events.Event) (pgtype.UUID, bool) {
	raw := e.TaskID
	if raw == "" {
		switch p := e.Payload.(type) {
		case protocol.ChatDonePayload:
			raw = p.TaskID
		case map[string]any:
			raw, _ = p["task_id"].(string)
		}
	}
	id, err := util.ParseUUID(raw)
	return id, err == nil && id.Valid
}

// chatDoneContent 从 EventChatDone payload（类型化 payload，或经序列化往返后的
// map 形态）里取出回复文本。
func chatDoneContent(payload any) string {
	switch p := payload.(type) {
	case protocol.ChatDonePayload:
		return p.Content
	case map[string]any:
		if s, ok := p["content"].(string); ok {
			return s
		}
	}
	return ""
}

// handleInboxNew 是经智能机器人投递成员通知的 inbox:new 订阅者。接收成员若有带
// 活连接的 WeCom 绑定，通知就被推到 aibot。任何不中——接收者非成员、没有 wecom
// 绑定、没有活 sender、发送失败——handler 都是空操作，成员照常通过应用内收件箱
// 收到通知。
func (o *Outbound) handleInboxNew(e events.Event) {
	payload, ok := e.Payload.(map[string]any)
	if !ok {
		return
	}
	item, ok := payload["item"].(map[string]any)
	if !ok {
		return
	}
	// 只投成员——agent 不通过会话通道收任何东西。
	if rt, _ := item["recipient_type"].(string); rt != "member" {
		return
	}
	recipientIDStr, _ := item["recipient_id"].(string)
	workspaceIDStr, _ := item["workspace_id"].(string)
	if recipientIDStr == "" || workspaceIDStr == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	o.tryDeliverInbox(ctx, item, recipientIDStr, workspaceIDStr)
}

// tryDeliverInbox 是投递核心。机器人确实推了通知才返回 true。
func (o *Outbound) tryDeliverInbox(ctx context.Context, item map[string]any, recipientIDStr, workspaceIDStr string) bool {
	recipientID, err := util.ParseUUID(recipientIDStr)
	if err != nil || !recipientID.Valid {
		return false
	}
	workspaceID, err := util.ParseUUID(workspaceIDStr)
	if err != nil || !workspaceID.Valid {
		return false
	}
	binding, err := o.q.FindChannelBindingForMember(ctx, db.FindChannelBindingForMemberParams{
		WorkspaceID:   workspaceID,
		MulticaUserID: recipientID,
		ChannelType:   channelTypeWecom,
	})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			o.logger.WarnContext(ctx, "wecom outbound: lookup member binding failed",
				"error", err, "workspace_id", workspaceIDStr, "recipient_id", recipientIDStr)
		}
		return false // 无绑定 → 无经机器人可投的东西
	}
	if o.senders == nil {
		return false
	}
	sender := o.senders.get(binding.InstallationID)

	// 解析链接用的 slug。尽力而为——slug 缺失只是让 URL 退回 workspace UUID。
	slug := ""
	if ws, err := o.q.GetWorkspace(ctx, workspaceID); err == nil {
		slug = ws.Slug
	}
	content := buildInboxMarkdown(item, workspaceIDStr, slug)
	if content == "" {
		return false
	}
	// 智能机器人收件箱通知是发给被绑定用户的 1:1 推送。绑定行的 channel_user_id
	// 是 bot 作用域的 T-* userid——企微把它当作单聊（chat_type=1）发送的 chatid。
	if sender == nil {
		// 这里没有 socket。和回复路径同形：交给握着 socket 的副本。一条收件箱推
		// 送和一条回答一样用户可见，让它只留在本地，正是「即使回复已能路由、单副
		// 本约束仍必须保留」的原因。
		if o.relay.publish(relayFrame{
			Kind:           relayKindInbox,
			InstallationID: util.UUIDToString(binding.InstallationID),
			ChatID:         binding.ChannelUserID,
			ChatType:       chatTypeSingleInt,
			Content:        content,
		}, relayInboxEventID(itemIDOf(item), recipientIDStr)) {
			o.logger.DebugContext(ctx, "wecom outbound: routed an inbox push to the replica holding the socket",
				"installation_id", uuidStringPub(binding.InstallationID))
			return true
		}
		// 记日志，不计入回复计数器。那些计数器的文档单位是 AGENT REPLIES，把一条
		// 收件箱通知记在那里会显示成「本适配器欠某人一条回复没送达」——与
		// deliverRelayed 里中继收件箱路径已避免的同一个单位错误，也正是 delivered/
		// dropped 比例能当作结局来读的原因。成员仍会在应用内收件箱收到它，这让一
		// 次未送达的机器人推送成为降级而非损失。
		o.logger.WarnContext(ctx, "wecom outbound: inbox push not delivered and not routable",
			"installation_id", uuidStringPub(binding.InstallationID),
			"recipient_id", recipientIDStr)
		return false // supervisor 挂了或在重连——没有活连接
	}
	if err := sender.sendTextCtx(ctx, binding.ChannelUserID, chatTypeSingleInt, content); err != nil {
		o.logger.WarnContext(ctx, "wecom outbound: inbox push failed",
			"error", err, "installation_id", uuidStringPub(binding.InstallationID),
			"recipient_id", recipientIDStr)
		return false // 发送失败 → 无机器人投递
	}
	o.logger.DebugContext(ctx, "wecom outbound: inbox delivered via bot",
		"installation_id", uuidStringPub(binding.InstallationID),
		"recipient_id", recipientIDStr,
		"inbox_type", item["type"])
	return true
}

// uuidStringPub 给日志行渲染 pgtype.UUID，而不依赖 engine.uuidString（那是另一个
// 包）。
func uuidStringPub(u pgtype.UUID) string {
	return util.UUIDToString(u)
}
