// outbound.go：出站 Patcher——订阅任务生命周期事件，把 chat 回复以纯文本 IM
// 发给飞书，任务失败时发一张简短错误卡片。
package lark

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// CardStatus 镜像 lark_outbound_card_message.status。保持命名类型，调用方就
// 不能往状态列里传任意字符串。
type CardStatus string

const (
	CardStatusPending   CardStatus = "pending"
	CardStatusStreaming CardStatus = "streaming"
	CardStatusFinal     CardStatus = "final"
	CardStatusError     CardStatus = "error"
)

// CardKind 枚举 patcher 渲染的少量卡片变体。Renderer 可插拔替换，线上卡片模板
// 的演进无需碰到 patcher 的传输 / DB 逻辑。
type CardKind string

const (
	CardKindThinking CardKind = "thinking"
	CardKindRunning  CardKind = "running"
	CardKindFinal    CardKind = "final"
	CardKindError    CardKind = "error"
)

// CardRender 是 Renderer 产出的已渲染卡片体。patcher 在交给 APIClient 前会先
// 序列化成 JSON。
type CardRender struct {
	JSON string
}

// RenderInput 是 Renderer 构建/更新卡片时看到的（类型化）快照。字段随任务
// 生命周期逐步填充——/issue 流程会设 IssueNumber，完成的 chat 任务设 Content，
// 失败的设 ErrorMessage。
type RenderInput struct {
	Kind         CardKind
	AgentName    string
	IssueNumber  int32
	IssueID      pgtype.UUID
	TaskID       pgtype.UUID
	Content      string
	ErrorMessage string
}

// Renderer 把类型化的 RenderInput 变成真正的 Lark 卡片 JSON。集中在这里，换
// 卡片模板（或做 A/B）时就不用碰事件订阅或持久化代码。
type Renderer interface {
	Render(in RenderInput) (CardRender, error)
}

// defaultRenderer 产出能跑在 Lark 通用交互卡片 schema 上的最小纯文本卡片。
// 具体 JSON 布局等真实产品卡片设计落地后再细化；这个默认值让接线是真的（JSON
// 能按 Lark schema 反序列化），又没把产品绑死在某个模板上。
type defaultRenderer struct{}

// NewDefaultRenderer 返回生产默认 Renderer。需要自定义模板时经
// PatcherConfig.Renderer 覆盖。
func NewDefaultRenderer() Renderer { return &defaultRenderer{} }

// Render 实现 Renderer：按卡片类型拼出一张最小文本卡片。
func (defaultRenderer) Render(in RenderInput) (CardRender, error) {
	header := "Multica"
	if in.AgentName != "" {
		header = in.AgentName
	}
	var body string
	switch in.Kind {
	case CardKindThinking:
		body = "Thinking…"
	case CardKindRunning:
		body = "Working on it…"
	case CardKindFinal:
		body = in.Content
		if body == "" {
			body = "Done."
		}
	case CardKindError:
		body = "Run failed."
		if in.ErrorMessage != "" {
			body = "Run failed: " + in.ErrorMessage
		}
	default:
		return CardRender{}, fmt.Errorf("unknown card kind %q", in.Kind)
	}
	// 每张卡片的 config 里 update_multi 都【必须】为 true：Lark 拒绝把
	// PatchInteractiveCard 用在一张没有声明自己是“可共享、可更新”的卡片上。
	// 由于本 renderer 驱动 thinking → streaming → final/error 生命周期（卡片
	// 只发一次、随后被 patch 多次），缺了 update_multi 会让首次发送后的每次
	// patch 在 Lark 侧静默无效，而本地出站状态行仍会翻到 streaming/final。
	// 每种 kind——包括 thinking 和 error——都要带上它，因为这份初始 JSON 就是
	// Lark 保存并用于后续 patch 的体。
	doc := map[string]any{
		"config": map[string]any{
			"wide_screen_mode": true,
			"update_multi":     true,
		},
		"header": map[string]any{
			"template": "blue",
			"title":    map[string]any{"tag": "plain_text", "content": header},
		},
		"elements": []any{
			map[string]any{
				"tag": "div",
				"text": map[string]any{
					"tag":     "plain_text",
					"content": body,
				},
			},
		},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return CardRender{}, err
	}
	return CardRender{JSON: string(raw)}, nil
}

// PatcherQueries 是 Patcher 需要的 *db.Queries 的窄子集。声明成接口，patcher
// 无需真实 Postgres 连接就能单测。
type PatcherQueries interface {
	GetAgentTask(ctx context.Context, id pgtype.UUID) (db.AgentTaskQueue, error)
	TaskHasChannelIngestedMessages(ctx context.Context, taskID pgtype.UUID) (bool, error)
	GetChannelTaskDelivery(ctx context.Context, taskID pgtype.UUID) (db.ChannelTaskDelivery, error)
	GetChatSession(ctx context.Context, id pgtype.UUID) (db.ChatSession, error)
	GetAgent(ctx context.Context, id pgtype.UUID) (db.Agent, error)
	GetLarkInstallation(ctx context.Context, id pgtype.UUID) (Installation, error)
	GetLarkChatSessionBindingBySession(ctx context.Context, chatSessionID pgtype.UUID) (ChatSessionBinding, error)
	GetLarkOutboundCardByTask(ctx context.Context, taskID pgtype.UUID) (OutboundCardMessage, error)
	CreateLarkOutboundCardMessage(ctx context.Context, arg CreateOutboundCardMessageParams) (OutboundCardMessage, error)
	UpdateLarkOutboundCardStatus(ctx context.Context, arg UpdateOutboundCardStatusParams) error
}

// CredentialsResolver 为传输层解密安装的 app_secret。*InstallationService 直接
// 满足它；测试用 fake 替换。
type CredentialsResolver interface {
	DecryptAppSecret(inst Installation) (string, error)
}

// PatcherConfig 调优出站 Patcher。经 withDefaults 填默认值；测试通常覆盖
// Renderer / Now / Logger。
type PatcherConfig struct {
	// Renderer 驱动 EventTaskFailed 路径上的错误卡片模板。成功路径
	// （EventChatDone）完全绕过 renderer——它把原始 assistant 回复当纯文本 IM
	// 消息发送——所以这里只对失败分支有意义。
	Renderer Renderer
	Now      func() time.Time
	Logger   *slog.Logger
}

// withDefaults 填充配置默认值。
func (c PatcherConfig) withDefaults() PatcherConfig {
	if c.Renderer == nil {
		c.Renderer = NewDefaultRenderer()
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	return c
}

// Patcher 响应事件总线上的任务生命周期事件，把 chat 回复以纯文本 IM 消息转发
// 给飞书。它是 §4.5 的出站侧——但 Bohan 反馈卡片装饰让回复像系统通知之后，
// 原先的 “thinking → streaming → final 卡片” 生命周期被削减成 EventChatDone
// 上的一条纯文本回复。卡片渲染的唯一幸存者是错误路径：失败运行在
// EventTaskFailed 上以一张短错误卡片浮现，因为与普通回复的视觉区分确实有用。
//
// 范围：
//
//   - 只有 chat_session 带有 lark_chat_session_binding 的任务才产生出站。
//     从 Web UI 或 autopilot 出生的任务原样放过。
//
//   - 每个 EventChatDone 产出一条 Lark 文本消息；没有流式、没有节流、没有追踪
//     卡片状态的 DB 行。
//
//   - 多副本安全继承自入站 WS 租约：同一时刻至多一个副本持有安装租约，事件
//     总线是进程内的，所以每次运行恰好一个 Patcher 作出反应。
type Patcher struct {
	queries         PatcherQueries
	credentials     CredentialsResolver
	client          APIClient
	typingIndicator *TypingIndicatorManager
	cfg             PatcherConfig
}

// NewPatcher 构造绑定好依赖的 Patcher。patcher 直到 Register 被调用才会订阅
// 总线。
func NewPatcher(queries PatcherQueries, credentials CredentialsResolver, client APIClient, cfg PatcherConfig) *Patcher {
	cfg = cfg.withDefaults()
	return &Patcher{
		queries:     queries,
		credentials: credentials,
		client:      client,
		cfg:         cfg,
	}
}

// SetTypingIndicatorManager 把 typing-indicator manager 接进 patcher，让回复在
// 发出前先清掉“处理中”反应。启动时在 patcher 与 manager 都构造好后调用一次。
// nil 会禁用清理步骤。
func (p *Patcher) SetTypingIndicatorManager(m *TypingIndicatorManager) {
	p.typingIndicator = m
}

// Register 让 patcher 在给定总线上订阅它关心的任务生命周期事件。只有对全新
// 总线调用才幂等；调用方应在服务器启动时（总线与 patcher 构造好后、HTTP 流量
// 开始前）恰好调用一次。
//
// 订阅刻意保持最小：
//
//   - EventChatDone —— agent 回复完成。Patcher 把回复以纯文本 IM 消息发送
//     （Lark 的 `msg_type=text`），而不是交互卡片。早先的卡片设计（thinking →
//     running → final 一系列 patch）让每条回复都像嵌在卡片装饰里的系统通知；
//     换成纯文本让自由聊天显得原生。
//
//   - EventTaskFailed —— 运行失败；浮出一张短错误卡片，让失败与成功回复在视觉
//     上有区分。
//
//   - EventTaskCancelled —— 运行结束但没有答案。不为它发送任何东西；订阅存在的
//     意义是让 Typing 反应被摘掉。取消不会发布 chat-done 也不会发布 task-failed，
//     所以没有这个订阅徽标会永久挂在用户消息上。task:cancelled 由 CancelTask 对
//     每个被取消的行广播一次，还有其后的排队跟进取消、agent 与 issue 级批量
//     取消、runtime 与成员撤销、以及删除聊天会话。删除是过去唯一什么都不发的：
//     BroadcastCancelledTasks 原本通过各任务的 chat_session 解析 workspace——而
//     chat_session 正是它事务里刚删掉的那行——没有 workspace 的事件到不了总线。
//     现在它从调用方拿 workspace。还剩两个洞，都不是订阅缺失：归档 agent 保持
//     静默是刻意选择——agent:archived 会失效每个客户端的任务列表——且列表刷新
//     不会移除 Lark 反应；另外在反应仍在添加时到达的结束事件清不掉任何东西，
//     因为 Add 只在 Lark 调用返回后才记录状态，徽标落在清理之后、已无物可摘。
//     第二个洞在 task:cancelled 之前就存在——chat-done 与 task-failed 同样会与
//     add 竞争——要收掉它需要一个 add 在调用返回时可检查的按会话代数。
//
// 我们刻意不订阅 EventTaskQueued / EventTaskRunning（不再有 thinking 卡片生命
// 周期——只有噪音没有价值）也不订阅 EventTaskCompleted（chat 任务总是先发
// EventChatDone——那才是我们关心的；非 chat 任务本来就没有 Lark 绑定，会提前
// 返回）。不订阅 EventTaskCompleted 也避免了之前的 “Done.” 覆盖回归：那次空
// 内容的 EventTaskCompleted 负载会抹掉真实回复。
func (p *Patcher) Register(bus *events.Bus) {
	bus.Subscribe(protocol.EventTaskFailed, p.handleEvent)
	bus.Subscribe(protocol.EventChatDone, p.handleEvent)
	bus.Subscribe(protocol.EventTaskCancelled, p.handleEvent)
}

// handleEvent 给事件处理包一个紧的超时后台 ctx，然后处理事件。
func (p *Patcher) handleEvent(e events.Event) {
	// 用带紧超时的新后台 ctx：总线投递是同步的，卡住的 Lark HTTP 调用否则会
	// 楔住整个发布调用点。
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := p.processEvent(ctx, e); err != nil {
		p.cfg.Logger.Warn("lark patcher: event handling failed",
			"event_type", e.Type,
			"task_id", e.TaskID,
			"chat_session_id", e.ChatSessionID,
			"error", err,
		)
	}
}

// processEvent 处理一条任务生命周期事件：查绑定与任务来源，决定是否要发飞书
// 侧回复，需要时发送。
func (p *Patcher) processEvent(ctx context.Context, e events.Event) error {
	taskID, chatSessionID, ok := taskAndSessionFromEvent(e)
	if !ok {
		return nil
	}
	if !chatSessionID.Valid {
		// issue / autopilot 任务没有 chat_session。
		return nil
	}
	// 被取消的运行没有回复要放，唯一亏欠用户的是把 Typing 徽标摘掉。这在下面
	// 每个查询之前执行，因为其中每一个都可能对一个“屏幕上还有徽标”的运行回答
	// “不”：
	//
	//   - 会话删除的 cancel 广播时绑定已经没了（它们在删除绑定的事务提交后才
	//     触发）；
	//
	//   - 来源分类回答“这条回复该不该上飞书”，而一个因持有空输入批次被取消的
	//     任务——#6611 修的正是其成因——会报告没有 channel 摄入消息，于是紧随
	//     其后的清理恰恰在最需要它的那次运行上被跳过。取消没有答案可错投，所以
	//     不存在这个问题。
	//
	// 这里不发布任何东西，两道闸门没在保护什么：徽标本来就是 Lark 自己的，
	// Clear 只碰本进程加过徽标的会话。清理按会话而非按轮次键控，所以取消一个
	// 会话里两轮中的一轮会把两轮的徽标都摘掉；最坏代价只是仍在运行的那一轮
	// 少了徽标。
	if e.Type == protocol.EventTaskCancelled {
		if p.typingIndicator != nil {
			p.typingIndicator.Clear(ctx, chatSessionID)
		}
		return nil
	}

	delivery, err := p.queries.GetChannelTaskDelivery(ctx, taskID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// 直接 Multica 任务或违反快照不变量——fail closed。
			return nil
		}
		return fmt.Errorf("lookup lark task delivery: %w", err)
	}
	if delivery.ChannelType != channelTypeFeishu {
		return nil
	}
	binding := ChatSessionBinding{
		ID: delivery.BindingID, InstallationID: delivery.InstallationID,
		ChannelChatID: delivery.ChannelChatID, ChatType: delivery.ChatType, Config: delivery.Config,
		LastMessageID: delivery.ChannelMessageID, LastThreadID: delivery.ChannelThreadID,
	}

	// 只有绑定的会话才走到这里，先给任务来源分类再花任何发送功夫。Web/移动端
	// 直接聊天任务可能复用一条起源于 Lark 的会话，但它们的回复只属于 Multica。
	// 密封渠道任务与直接任务一样持有一个输入批次，所以判别依据是该批次不可变
	// 的 channel_ingested 来源，而不是 chat_input_task_id 是否存在（#5645 最初
	// 用的判别）。
	task, err := p.queries.GetAgentTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("load agent task: %w", err)
	}
	deliver, err := engine.TaskInputIsChannelIngested(ctx, p.queries, task)
	if err != nil {
		return fmt.Errorf("classify task input origin: %w", err)
	}
	if !deliver {
		return nil
	}

	inst, err := p.queries.GetLarkInstallation(ctx, binding.InstallationID)
	if err != nil {
		return fmt.Errorf("load installation: %w", err)
	}
	if InstallationStatus(inst.Status) != InstallationActive {
		// 触发与事件之间被撤销了；没什么可 patch 的。
		return nil
	}
	creds, err := p.installationCredentials(inst)
	if err != nil {
		return err
	}

	agent, agentErr := p.queries.GetAgent(ctx, inst.AgentID)
	agentName := ""
	if agentErr == nil {
		agentName = agent.Name
	}

	// 在回复可见之前先清掉“处理中”反应，用户看到干净的画面切换。Best-effort：
	// 这里的失败只记日志，不阻塞真正的回复。
	if p.typingIndicator != nil {
		p.typingIndicator.Clear(ctx, chatSessionID)
	}

	switch e.Type {
	case protocol.EventChatDone:
		return p.sendChatReply(ctx, creds, binding, e.Payload)
	case protocol.EventTaskFailed:
		return p.fail(ctx, creds, binding, taskID, agentName, e.Payload)
	}
	return nil
}

// sendChatReply 把 ChatDonePayload.Content 变成一条 Lark 消息。线上形态按正文
// 是否含 markdown 语法逐条选择：
//
//   - 纯白话（无 markdown）→ `msg_type=text`。一句 "Hi!" 回复应该像普通 IM
//     消息，而不是带一圈装饰的通知卡片。
//
//   - 含 markdown（标题、列表、代码块、表格、粗体/斜体、链接）→ schema-2.0
//     交互卡片，正文是 `tag: "markdown"` 元素，让 Lark 客户端渲染格式而不是把
//     裸的 `**bold**` 字符留在记录里。这张卡片比旧的绑定引导模板更克制——只有
//     一个 markdown 块，没有 header / 图标 / CTA 按钮。
//
// 空内容静默丢弃：宁可什么都不显示，也别发 “Done.”（先前的卡片兜底曾在真实
// dev 环境让 Bohan 困惑）。实际上空 Content 意味着 daemon 完成了任务却没产出
// 可见输出——只发生在诸如 chat 任务只确认了一条系统事件的边角情况；那种情形
// 不发声是正确的产品决策。
func (p *Patcher) sendChatReply(ctx context.Context, creds InstallationCredentials, binding ChatSessionBinding, payload any) error {
	content := chatDoneContent(payload)
	if content == "" {
		return nil
	}
	target := threadReplyTarget(binding)
	if containsMarkdown(content) {
		return sendWithThreadFallback(p.cfg.Logger, "send markdown card", target, func(t ReplyTarget) error {
			_, err := p.client.SendMarkdownCard(ctx, SendMarkdownCardParams{
				InstallationID: creds,
				ChatID:         outboundChatID(binding),
				Markdown:       content,
				ReplyTarget:    t,
			})
			return err
		})
	}
	return sendWithThreadFallback(p.cfg.Logger, "send text message", target, func(t ReplyTarget) error {
		_, err := p.client.SendTextMessage(ctx, SendTextParams{
			InstallationID: creds,
			ChatID:         outboundChatID(binding),
			Text:           content,
			ReplyTarget:    t,
		})
		return err
	})
}

// outboundChatID 从聊天绑定恢复真实的 Lark chat id。channel_chat_id 可能是复合
// 的 "chat:thread" 话题隔离键，所以真实 chat id 从绑定 config
// （larkBindingConfig）读；话题出现前的行（config "{}"）直接按键路由——对它们
// 而言键就是真实 chat id。
func outboundChatID(b ChatSessionBinding) ChatID {
	if len(b.Config) > 0 {
		var cfg larkBindingConfig
		if err := json.Unmarshal(b.Config, &cfg); err == nil && cfg.ChatID != "" {
			return ChatID(cfg.ChatID)
		}
	}
	return ChatID(b.ChannelChatID)
}

// threadReplyTarget 从聊天绑定最近一次入站触发推导出站回复目标。只有当那次
// 触发本身在飞书话题里（有 last_lark_thread_id）时才贴话题回复：普通群聊/私聊
// 保持不变的聊天级发送路径，只有发生在话题内的 @ 才得到贴话题的回复（对
// last_lark_message_id 回 reply_in_thread）。零值 ReplyTarget 表示“在聊天级
// 发送”。
func threadReplyTarget(binding ChatSessionBinding) ReplyTarget {
	if binding.LastThreadID.Valid && binding.LastThreadID.String != "" &&
		binding.LastMessageID.Valid && binding.LastMessageID.String != "" {
		return ReplyTarget{MessageID: binding.LastMessageID.String, InThread: true}
	}
	return ReplyTarget{}
}

// sendWithThreadFallback 用贴话题目标执行 send；只有当贴话题尝试以“话题回复
// 确实落不了地”的 Lark 错误失败（触发消息被撤回、话题没了、话题被禁用、消息
// 被聚合——见 threadReplyUnsupportedCodes）时，才在聊天级重试一次，让回复不被
// 静默丢失。其它任何失败——transport 错误、5xx、超时、限流、或模棱两可的
// “服务器可能已收到”错误——都记日志并按失败返回而不重试：盲目的聊天级重试
// 可能重复回复，或把只该进话题的回复漏进主群聊。目标本就是聊天级时没有可回退
// 的，错误原样返回。
//
// 它是包级函数（而非 Patcher 方法），让事件驱动的 Patcher 与即时的
// OutcomeReplier 共享同一条分类后退路径。
func sendWithThreadFallback(log *slog.Logger, op string, target ReplyTarget, send func(ReplyTarget) error) error {
	err := send(target)
	if err == nil {
		return nil
	}
	if target.IsSet() && isThreadReplyUnsupported(err) {
		log.Warn("lark: thread reply unsupported for target, retrying at chat level",
			"op", op, "reply_message_id", target.MessageID, "error", err)
		if fallbackErr := send(ReplyTarget{}); fallbackErr != nil {
			return fmt.Errorf("%s (chat-level fallback after thread-unsupported reply: %v): %w", op, err, fallbackErr)
		}
		return nil
	}
	if target.IsSet() {
		log.Warn("lark: thread reply failed; not falling back (non-classified error)",
			"op", op, "reply_message_id", target.MessageID, "error", err)
	}
	return fmt.Errorf("%s: %w", op, err)
}

// installationCredentials 解密安装行的 app_secret 组装飞书 API 凭据。
func (p *Patcher) installationCredentials(inst Installation) (InstallationCredentials, error) {
	if p.credentials == nil {
		return InstallationCredentials{}, errors.New("lark patcher: credentials resolver missing")
	}
	secret, err := p.credentials.DecryptAppSecret(inst)
	if err != nil {
		return InstallationCredentials{}, fmt.Errorf("decrypt app_secret: %w", err)
	}
	creds := InstallationCredentials{
		AppID:     inst.AppID,
		AppSecret: secret,
		Region:    RegionOrDefault(inst.Region),
	}
	if inst.TenantKey.Valid {
		creds.TenantKey = inst.TenantKey.String
	}
	return creds, nil
}

// fail 在任务失败时浮出一张短错误卡片。与成功路径（sendChatReply 的纯文本）
// 不同，失败保持卡片形态，因为用户受益于视觉区分——带 header 的卡片比普通
// 气泡难忽略得多——而且这类事件足够稀少，卡片装饰不算吵。
//
// 一次性发送（无 patch、无 DB 行）：如果任务第二次失败，就再发一张卡片，也没
// 问题——失败通常是单次终态事件。
func (p *Patcher) fail(ctx context.Context, creds InstallationCredentials, binding ChatSessionBinding, taskID pgtype.UUID, agentName string, payload any) error {
	render, err := p.cfg.Renderer.Render(RenderInput{
		Kind:         CardKindError,
		AgentName:    agentName,
		TaskID:       taskID,
		ErrorMessage: errorMessageFromPayload(payload),
	})
	if err != nil {
		return fmt.Errorf("render error card: %w", err)
	}
	return sendWithThreadFallback(p.cfg.Logger, "send error card", threadReplyTarget(binding), func(t ReplyTarget) error {
		_, err := p.client.SendInteractiveCard(ctx, SendCardParams{
			InstallationID: creds,
			ChatID:         outboundChatID(binding),
			CardJSON:       render.JSON,
			ReplyTarget:    t,
		})
		return err
	})
}

// taskAndSessionFromEvent 解析任务发布者发出的“类类型化”负载——一个带
// `task_id`（恒有）与 `chat_session_id`（仅 chat 任务）的 map[string]any。
// EventChatDone 改带 ChatDonePayload 结构体。
func taskAndSessionFromEvent(e events.Event) (taskID, chatSessionID pgtype.UUID, ok bool) {
	if e.TaskID != "" {
		if err := taskID.Scan(e.TaskID); err != nil {
			taskID = pgtype.UUID{}
		}
	}
	if e.ChatSessionID != "" {
		if err := chatSessionID.Scan(e.ChatSessionID); err != nil {
			chatSessionID = pgtype.UUID{}
		}
	}
	switch p := e.Payload.(type) {
	case map[string]any:
		if !taskID.Valid {
			if s, _ := p["task_id"].(string); s != "" {
				_ = taskID.Scan(s)
			}
		}
		if !chatSessionID.Valid {
			if s, _ := p["chat_session_id"].(string); s != "" {
				_ = chatSessionID.Scan(s)
			}
		}
	case protocol.ChatDonePayload:
		if !taskID.Valid {
			_ = taskID.Scan(p.TaskID)
		}
		if !chatSessionID.Valid {
			_ = chatSessionID.Scan(p.ChatSessionID)
		}
	}
	return taskID, chatSessionID, taskID.Valid
}

// chatDoneContent 从事件负载里取出 ChatDone 的内容文本。
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

// errorMessageFromPayload 从失败事件负载里取出错误消息文本。
func errorMessageFromPayload(payload any) string {
	if m, ok := payload.(map[string]any); ok {
		if s, ok := m["error"].(string); ok {
			return s
		}
		if s, ok := m["error_message"].(string); ok {
			return s
		}
	}
	return ""
}
