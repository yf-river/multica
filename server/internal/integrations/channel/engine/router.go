package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// 角色定位：渠道无关的入站流水线——飞书专属 lark.Dispatcher（+ Hub 的 handleEvent 出站
// 缝）的泛化版。它是 Supervisor 注入每条 Channel 的那个唯一共享的 channel.InboundHandler：
// 一条 Channel 把平台 payload 翻译成 channel.InboundMessage 后调用 Handle；Handle 按
// ChannelType 路由到该平台注册的 resolver set，并对每个平台跑同一条有序流水线——安装路由
// → 两阶段去重 → 群聊 @bot 过滤 → 身份 + 成员资格 → 保证会话 → 追加+标记 → /issue →
// 持久化去抖运行触发 + 解耦媒体绑定——随后驱动解耦的出站 replier 与打字指示器。
//
// 核心不含任何平台细节：平台形态的东西全在 resolver 接口后面（飞书 ResolverSet 是第一个
// 实现）。新增平台 = "注册一个 ResolverSet"，而不是"改 Router"。

// Router 是上述流水线的载体：持有注册表（渠道类型→ResolverSet）、共享服务（issue/task/
// session 读取）、去抖器与媒体队列等跨平台基础设施。
type Router struct {
	mu   sync.RWMutex
	sets map[channel.Type]ResolverSet

	issues    IssueCreator
	tasks     TaskEnqueuer
	reader    SessionReader
	lifecycle ChannelChatLifecycle

	batcher *pendingBatcher

	replyTimeout time.Duration
	mediaTimeout time.Duration
	mediaCtx     context.Context
	mediaCancel  context.CancelFunc
	mediaSem     chan struct{}
	replyWg      sync.WaitGroup
	mediaWg      sync.WaitGroup

	// mediaQueues 按会话维护"远程媒体解析"的有界链（同会话保序），tail 是链尾信号。
	mediaQueueMu sync.Mutex
	mediaQueues  map[string]*mediaQueueEntry
	stopping     bool

	logger *slog.Logger
}

// Config 调 Router。零值走默认。
type RouterConfig struct {
	// ReplyTimeout 限制一次解耦的 OutboundReplier.Reply / typing 调用。它跑在连接器
	// ACK 路径之外，所以必须严格低于平台 ACK 截止（飞书：3 秒）。默认 2.5s。
	ReplyTimeout time.Duration
	// MediaTimeout 限制一条消息的解耦"尽力而为媒体处理"（下载、上传、附件绑定）总预算。
	// 预算从追加时刻起算（必须匹配持久化的 fire_at 兜底），所以也涵盖排在同会话更早
	// 媒体后面、以及等全局并发槽的时间。默认 45s。
	MediaTimeout time.Duration
	// MediaConcurrency 限制跨全部会话的并发媒体解析数，压住突发内存（长度未知的上传
	// 每条最多缓冲到 100 MiB 资源上限）与平台下载压力。同会话的先后次序不受影响。
	// 默认 8。
	MediaConcurrency int
	Logger           *slog.Logger
	Lifecycle        ChannelChatLifecycle
}

// NewRouter 围绕共享的（平台无关）服务组装 Router：/issue 与聊天运行走 IssueCreator +
// TaskEnqueuer；去抖 flush 需要一个 SessionReader。在 Handle 被调用之前，用 Register 注册
// 平台的 ResolverSet。
func NewRouter(issues IssueCreator, tasks TaskEnqueuer, reader SessionReader, cfg RouterConfig) *Router {
	if cfg.ReplyTimeout == 0 {
		cfg.ReplyTimeout = 2500 * time.Millisecond
	}
	if cfg.MediaTimeout == 0 {
		cfg.MediaTimeout = DefaultMediaTimeout
	}
	if cfg.MediaConcurrency == 0 {
		cfg.MediaConcurrency = 8
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	mediaCtx, mediaCancel := context.WithCancel(context.Background())
	return &Router{
		sets:         make(map[channel.Type]ResolverSet),
		issues:       issues,
		tasks:        tasks,
		reader:       reader,
		lifecycle:    cfg.Lifecycle,
		replyTimeout: cfg.ReplyTimeout,
		mediaTimeout: cfg.MediaTimeout,
		mediaCtx:     mediaCtx,
		mediaCancel:  mediaCancel,
		mediaSem:     make(chan struct{}, cfg.MediaConcurrency),
		logger:       cfg.Logger,
		mediaQueues:  make(map[string]*mediaQueueEntry),
	}
}

// DefaultMediaTimeout 是默认的 RouterConfig.MediaTimeout。导出是为了让"渠道媒体落定
// 不变量"测试能断言对账器的落定延迟远大于每条流水线预算。
const DefaultMediaTimeout = 45 * time.Second

// maxRouteChangeRetries 限制"连接器持续报告过期路由"时进程内重试的次数。健康的冲突在
// 重新解析当前路由后收敛；持续的 schema 或数据库故障必须浮出水面，而不是让 webhook
// handler 永远活着空转。
const maxRouteChangeRetries = 8

// 去重收尾是"认领后的流水线已选出终态"之后的一段短持久化步骤。此时请求 ctx 可能已被
// 取消，所以保留它的值、同时给 Mark/Release 一次有界的落库机会——而不是把认领困到过期
// 回收。
const dedupFinalizeTimeout = time.Second

// mediaQueueEntry 是每会话媒体队列的一个节点：tail 表示"排在我后面的下一个任务要等
// 的信号"（前一个完成才关闭）。
type mediaQueueEntry struct {
	tail chan struct{}
}

// Register 把平台的 ResolverSet 绑定到 t 名下。在启动时（Run 之前）调用。空 Type 或
// 缺少必填 resolver 的 set 会被忽略。
func (r *Router) Register(t channel.Type, set ResolverSet) {
	if t == "" || set.Installation == nil || set.Identity == nil || set.Dedup == nil || set.Session == nil || set.Audit == nil {
		r.logger.Warn("channel router: ignoring incomplete resolver set", "channel_type", string(t))
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sets[t] = set
}

// EnableRunBatching 在每会话运行触发前装上去抖器。启动时调一次。窗口非正数时用
// DefaultChatRunBatchWindow。不装的话运行内联触发（测试用）。
func (r *Router) EnableRunBatching(window time.Duration) {
	r.batcher = newPendingBatcher(window)
}

// Drain 取消解耦的媒体处理、冲刷去抖的运行触发，并在 ctx 结束前汇合媒体/回复 goroutine。
// 返回是否全部完成。请在 Supervisor 停止投递事件之后的停机路径上调用；超时的媒体保留它
// 的持久化占位兜底。
func (r *Router) Drain(ctx context.Context) bool {
	r.mediaQueueMu.Lock()
	r.stopping = true
	r.mediaCancel()
	r.mediaQueueMu.Unlock()

	done := make(chan struct{})
	go func() {
		if r.batcher != nil {
			r.batcher.FlushAll()
		}
		r.mediaWg.Wait()
		r.replyWg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}

// ErrNoResolverSet 在消息到达一个"没注册 ResolverSet"的渠道类型时由 Handle 返回——这是
// 启动/注册 bug。它属于基础设施错误，适配器应上浮而不是静默丢弃。
var ErrNoResolverSet = errors.New("channel router: no resolver set for channel type")

// Handle 是那个共享的 channel.InboundHandler。它跑完整流水线，然后驱动解耦的出站侧；
// 只在基础设施故障时返回非 nil 错误（适配器据此重连）。产品结果（丢弃、需要绑定…）
// 不是错误。
func (r *Router) Handle(ctx context.Context, msg channel.InboundMessage) error {
	// 在任何共享命令改写之前保存用户的原始规范化文本。会话 binder 把这份原文交给命令
	// 分类器，而 Text 保持为 agent 可读的正文。
	if msg.CommandText == "" {
		msg.CommandText = msg.Text
	}

	// /new 与 /clear 共享一个全渠道解析器。适配器只规范化传输细节；语义分叉在这里——
	// 保证一条入站消息只有一个控制面含义。
	control, hasControl := ParseControlCommand(msg.CommandText)
	startChat := hasControl && control.Kind == ControlCommandNewChat
	bareFresh := false
	if startChat {
		if parsedText, textOK := ParseControlCommand(msg.Text); textOK && parsedText.Kind == ControlCommandNewChat {
			msg.Text = parsedText.Body
		} else if msg.Text == msg.CommandText {
			msg.Text = control.Body
		}
		// 被消费掉的 /new 原文不能被下游分类器重新解释成 /issue。
		msg.CommandText = control.Body
	} else if hasControl && control.Kind == ControlCommandFreshSession {
		// 即使适配器已设过 ForceFresh，也解析原始命令源。只有当适配器还没剥掉指令时才
		// 改写 Text——飞书等富媒体适配器可能在消息到达 Router 前已经增强/重建过那段
		// 可见正文。
		adapterAlreadyStripped := msg.ForceFresh
		msg.ForceFresh = true
		bareFresh = control.Body == ""
		if !adapterAlreadyStripped {
			msg.Text = control.Body
		}
	}

	r.mu.RLock()
	set, ok := r.sets[msg.Source.ChannelType]
	r.mu.RUnlock()
	if !ok {
		r.logger.Error("channel router: no resolver set", "channel_type", string(msg.Source.ChannelType))
		return ErrNoResolverSet
	}

	res, inst, err := r.dispatch(ctx, set, msg, bareFresh, startChat)
	if err != nil {
		outcome := "dispatch_failed"
		if startChat {
			outcome = "chat_start_failed"
		}
		r.logger.Error("channel router: dispatch error",
			"channel_type", string(msg.Source.ChannelType),
			"event_id", msg.EventID,
			"outcome", outcome,
			"error", err,
		)
		return err
	}
	r.logger.Debug("channel router: dispatch outcome",
		"channel_type", string(msg.Source.ChannelType),
		"event_id", msg.EventID,
		"outcome", string(res.Outcome),
		"drop_reason", string(res.DropReason),
	)

	// 入库时点亮打字指示器，解耦在 goroutine 里，让那次"反应型 HTTP 调用"绝不让连接器
	// 的 ACK 路径阻塞。
	if res.Outcome == OutcomeIngested && res.runScheduled && set.Typing != nil {
		go func() {
			tctx, cancel := context.WithTimeout(context.Background(), r.replyTimeout)
			defer cancel()
			set.Typing.OnIngested(tctx, inst, msg, res.ChatSessionID)
		}()
	}
	r.scheduleReply(set, inst, msg, res)
	return nil
}

// dispatch 跑完整流水线并返回类型化结果 + 解析出的安装（出站侧要用）。对应
// lark.Dispatcher.Handle。
func (r *Router) dispatch(ctx context.Context, set ResolverSet, msg channel.InboundMessage, bareFresh, startChat bool) (Result, ResolvedInstallation, error) {
	// 1. 路由到安装。适配器把平台路由键（消息上携带的）映射到它的安装行。这些丢弃分支
	//    在去重认领**之前**执行，因为它们没有合法安装可挂认领。
	inst, err := set.Installation.ResolveInstallation(ctx, msg)
	if err != nil {
		if errors.Is(err, ErrInstallationNotFound) {
			_ = set.Audit.RecordDrop(ctx, pgtype.UUID{}, msg, DropReasonInvalidEvent)
			return Result{Outcome: OutcomeDropped, DropReason: DropReasonInvalidEvent}, ResolvedInstallation{}, nil
		}
		return Result{}, ResolvedInstallation{}, fmt.Errorf("resolve installation: %w", err)
	}
	if !inst.Active {
		return r.drop(ctx, set, msg, inst.ID, DropReasonRevokedInstallation), inst, nil
	}

	// 2. 带属主围栏的两阶段去重认领——放在群聊过滤与身份检查**之前**，这样重连重放
	//    不会重新触发绑定提示、重写丢弃审计、或重碰会话。MessageID 为空表示没有可去重
	//    的键，跳过认领。
	var claimToken pgtype.UUID
	claimed := false
	if msg.MessageID != "" {
		token, err := set.Dedup.Claim(ctx, inst.ID, msg.MessageID)
		if err != nil {
			if errors.Is(err, ErrDuplicate) {
				return r.drop(ctx, set, msg, inst.ID, DropReasonDuplicate), inst, nil
			}
			return Result{}, inst, fmt.Errorf("dedup claim: %w", err)
		}
		claimToken = token
		claimed = true
	}

	res, finalize, err := r.processClaimed(ctx, set, msg, inst, claimToken, bareFresh, startChat)

	if claimed && finalize != finalizeNone {
		finalizeCtx, finalizeCancel := context.WithTimeout(context.WithoutCancel(ctx), dedupFinalizeTimeout)
		r.applyFinalize(finalizeCtx, set, inst.ID, msg.MessageID, claimToken, finalize)
		finalizeCancel()
	}

	// ErrClaimLost：认领已归别的 worker。按重复浮出。
	if errors.Is(err, ErrClaimLost) {
		return r.drop(ctx, set, msg, inst.ID, DropReasonDuplicate), inst, nil
	}
	return res, inst, err
}

// dedupFinalize 告诉 dispatch 在 processClaimed 之后怎么落地认领行。
type dedupFinalize int

const (
	finalizeNone dedupFinalize = iota
	finalizeMark
	finalizeRelease
)

// processClaimed 跑"去重之后"的流水线。对应 lark.Dispatcher.processClaimed；
// 每步的边界契约见其自身注释。
func (r *Router) processClaimed(ctx context.Context, set ResolverSet, msg channel.InboundMessage, inst ResolvedInstallation, claimToken pgtype.UUID, bareFresh, startChat bool) (Result, dedupFinalize, error) {
	// 3. 群聊 @提及过滤（仅群聊），放在身份检查之前——未绑定用户的群聊闲谈绝不触发
	//    一张绑定卡片轰炸。
	if msg.Source.ChatType == channel.ChatTypeGroup && !msg.AddressedToBot {
		return r.drop(ctx, set, msg, inst.ID, DropReasonNotAddressedInGroup), finalizeMark, nil
	}

	// 4. 身份检查：把平台发送者映射成 Multica 用户并复查工作区成员资格
	//    （绑定表与成员表之间没有外键；MUL-3515 §4）。
	identity, err := set.Identity.ResolveSender(ctx, inst, msg)
	if err != nil {
		switch {
		case errors.Is(err, ErrSenderUnbound):
			_ = set.Audit.RecordDrop(ctx, inst.ID, msg, DropReasonUnboundUser)
			return Result{
				Outcome:        OutcomeNeedsBinding,
				DropReason:     DropReasonUnboundUser,
				InstallationID: inst.ID,
				Sender:         msg.Source.SenderID,
			}, finalizeMark, nil
		case errors.Is(err, ErrSenderNotMember):
			return r.drop(ctx, set, msg, inst.ID, DropReasonNonWorkspaceMember), finalizeMark, nil
		default:
			return Result{}, finalizeRelease, fmt.Errorf("resolve sender: %w", err)
		}
	}

	// 4b. Invoke permission (MUL-3963): the same verdict the web chat applies
	//     before it opens a session. It judges the SENDER, never the installer
	//     who owns a group's route, and it runs before anything is stored — so
	//     a refused turn reaches no Chat, no /issue, and no later run's context.
	allowed, err := r.tasks.MemberMayInvokeAgent(ctx, inst.AgentID, identity.UserID)
	if err != nil {
		// Release rather than mark: a lookup that did not answer is not a
		// denial, and the redelivery is this message's remaining chance.
		return Result{}, finalizeRelease, fmt.Errorf("check invoke permission: %w", err)
	}
	if !allowed {
		_ = set.Audit.RecordDrop(ctx, inst.ID, msg, DropReasonInvokeDenied)
		return Result{
			Outcome:        OutcomeInvokeDenied,
			DropReason:     DropReasonInvokeDenied,
			InstallationID: inst.ID,
			Sender:         msg.Source.SenderID,
		}, finalizeMark, nil
	}

	// 5-6. Resolve the current Chat route, then either append normally or
	// atomically create the next Chat route with its optional first turn.
	sessionCreator := identity.UserID
	if msg.Source.ChatType == channel.ChatTypeGroup {
		sessionCreator = inst.InstallerUserID
	}

	mediaPendingSeconds := 0.0
	var parsedCommand *IssueCommand
	if !startChat {
		parsedCommand, _ = ParseIssueCommand(msg.CommandText)
	}
	issueNeedsUsage := parsedCommand != nil && parsedCommand.Title == ""
	hasSelectedContext := msg.HasSelectedContext && strings.TrimSpace(msg.Text) != ""
	hasMedia := set.Media != nil && set.Media.HasMedia(msg)
	bareFresh = bareFresh && !hasSelectedContext && !hasMedia
	resolveMedia := !issueNeedsUsage && hasMedia
	localMediaDeadline := time.Now().Add(r.mediaTimeout)
	if resolveMedia {
		mediaPendingSeconds = r.mediaTimeout.Seconds()
	}

	var sessionID pgtype.UUID
	var appendRes AppendResult
	var started StartSessionResult
	var startedTask db.AgentTaskQueue
	startTaskCommitted := false
	routeChangeRetries := 0
	for {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Result{}, finalizeRelease, ctxErr
		}

		if startChat {
			startedTask = db.AgentTaskQueue{}
			persistMessage := msg.CommandText != "" || hasSelectedContext || hasMedia
			var beforeCommit func(context.Context, pgx.Tx, db.ChatSession) error
			if persistMessage && !msg.SkipAgentRun {
				prepared, prepareErr := r.tasks.PrepareChatTaskEnqueue(ctx, inst.AgentID, identity.UserID)
				if prepareErr != nil {
					return Result{}, finalizeRelease, fmt.Errorf("prepare started chat task: %w", prepareErr)
				}
				beforeCommit = func(ctx context.Context, tx pgx.Tx, session db.ChatSession) error {
					var enqueueErr error
					startedTask, enqueueErr = r.tasks.EnqueuePreparedChannelChatTaskInTx(
						ctx, tx, session, identity.UserID, false, 1, prepared,
					)
					return enqueueErr
				}
			}
			started, err = set.Session.StartSession(ctx, StartSessionParams{
				Installation: inst, Creator: sessionCreator, Sender: identity.UserID, Message: msg,
				ClaimToken: claimToken, MediaPendingSeconds: mediaPendingSeconds,
				PersistMessage: persistMessage, BeforeCommit: beforeCommit,
			})
			sessionID, appendRes = started.SessionID, started.Append
		} else {
			sessionID, err = set.Session.EnsureSession(ctx, EnsureSessionParams{
				Installation: inst,
				Sender:       sessionCreator,
				Message:      msg,
			})
		}

		// ErrRouteChanged = 当前路由（会话归属）已被并发改动，本地快照过期：按新路由
		// 重试同一条消息（最多 maxRouteChangeRetries 次）。
		if errors.Is(err, ErrRouteChanged) {
			routeChangeRetries++
			r.logger.Info("channel route changed; retrying inbound",
				"outcome", "chat_route_retry", "channel_type", string(msg.Source.ChannelType),
				"event_id", msg.EventID, "attempt", routeChangeRetries,
			)
			if routeChangeRetries >= maxRouteChangeRetries {
				return Result{}, finalizeRelease, fmt.Errorf(
					"channel route did not stabilize after %d retries: %w",
					maxRouteChangeRetries, ErrRouteChanged,
				)
			}
			continue
		}
		if err != nil {
			if errors.Is(err, ErrClaimLost) {
				return Result{}, finalizeNone, err
			}
			return Result{}, finalizeRelease, fmt.Errorf("persist channel chat message: %w", err)
		}

		if startChat {
			if startedTask.ID.Valid {
				r.tasks.FinalizeChatTaskEnqueue(ctx, startedTask)
				startTaskCommitted = true
			}
			r.notifyChatStarted(inst, sessionCreator, msg.Source.ChannelType, started)
			if msg.CommandText == "" && !hasSelectedContext && !hasMedia {
				finalize := finalizeMark
				if appendRes.DedupMarked {
					finalize = finalizeNone
				}
				return Result{
					Outcome: OutcomeChatStarted, InstallationID: inst.ID,
					ChatSessionID: sessionID, ChannelBindingID: started.BindingID,
					ChannelRouteRevision: started.RouteRevision, Sender: msg.Source.SenderID,
				}, finalize, nil
			}
			break
		}

		if bareFresh {
			// 裸 /clear：只标记"待开新会话"，不把这条消息当成普通对话轮次入库。
			err = set.Session.MarkPendingFresh(ctx, sessionID, msg.MessageID)
			if errors.Is(err, ErrRouteChanged) {
				routeChangeRetries++
				if routeChangeRetries >= maxRouteChangeRetries {
					return Result{}, finalizeRelease, fmt.Errorf(
						"channel route did not stabilize after %d retries: %w",
						maxRouteChangeRetries, ErrRouteChanged,
					)
				}
				continue
			}
			if err != nil {
				return Result{}, finalizeRelease, fmt.Errorf("persist fresh command: %w", err)
			}
			return Result{
				Outcome:        OutcomeFreshPending,
				InstallationID: inst.ID,
				ChatSessionID:  sessionID,
				Sender:         msg.Source.SenderID,
			}, finalizeMark, nil
		}

		appendRes, err = set.Session.AppendMessage(ctx, AppendParams{
			SessionID:           sessionID,
			Sender:              identity.UserID,
			InstallationID:      inst.ID,
			Message:             msg,
			ClaimToken:          claimToken,
			MediaPendingSeconds: mediaPendingSeconds,
		})
		if errors.Is(err, ErrRouteChanged) {
			routeChangeRetries++
			r.logger.Info("channel route changed; retrying inbound",
				"outcome", "chat_route_retry", "channel_type", string(msg.Source.ChannelType),
				"event_id", msg.EventID, "attempt", routeChangeRetries,
			)
			if routeChangeRetries >= maxRouteChangeRetries {
				return Result{}, finalizeRelease, fmt.Errorf(
					"channel route did not stabilize after %d retries: %w",
					maxRouteChangeRetries, ErrRouteChanged,
				)
			}
			continue
		}
		if err != nil {
			if errors.Is(err, ErrClaimLost) {
				return Result{}, finalizeNone, err
			}
			return Result{}, finalizeRelease, fmt.Errorf("append user message: %w", err)
		}
		break
	}

	// 追加后的路径**不能** Release（chat_message + Mark 已提交）。再 Mark 一次是空操作，
	// 所以默认 finalizeNone——除非 binder 没在事务内 Mark（防御性），则回退成流水线后的
	// Mark。
	postAppendFinalize := finalizeNone
	if !appendRes.DedupMarked {
		postAppendFinalize = finalizeMark
	}

	res := Result{
		Outcome:              OutcomeIngested,
		InstallationID:       inst.ID,
		ChatSessionID:        sessionID,
		ChannelBindingID:     appendRes.BindingID,
		ChannelRouteRevision: appendRes.RouteRevision,
		Sender:               msg.Source.SenderID,
	}
	if startChat {
		res.ChannelBindingID = started.BindingID
		res.ChannelRouteRevision = started.RouteRevision
	}
	var mediaIssue db.Issue
	var deferredIssueTaskID pgtype.UUID

	// 7. /issue 命令（若有）。chat_message 已持久化；从这里开始的所有错误返回都表示
	//    finalizeNone（或防御性的 Mark）。
	if appendRes.IssueCommand != nil {
		if appendRes.IssueCommand.Title == "" {
			res.Outcome = OutcomeIssueUsage
			res.IssueUsageHadMedia = hasMedia
			return res, postAppendFinalize, nil
		}
		if resolveMedia {
			// CommandText 刻意不含适配器生成的媒体占位符（"图在前、命令在后"的版式仍要
			// 分类成 /issue）。分类完成后从完整规范化正文恢复描述，让建出的 issue 保留
			// 解耦媒体绑定将物化的行内位置。纯文本命令保持既有解析输出。
			appendRes.IssueCommand.Description = issueDescriptionFromCommandBody(
				msg.Text,
				msg.CommandText,
				appendRes.IssueCommand.Description,
			)
		}
		// 一次查询同时喂给广播 payload 的标识与聊天回复的标识。
		prefix := r.issuePrefix(ctx, inst.WorkspaceID)
		var assignedRunFireAt time.Time
		if resolveMedia {
			// 通用"延迟任务清扫器"是崩溃兜底。在媒体截止之后再留一段给有界附件终态器，
			// 让它不会与读取新建 issue 的 issue agent 赛跑。
			assignedRunFireAt = localMediaDeadline.Add(mediaFinalizeTimeout)
		}
		issueRes, err := r.createIssue(ctx, inst, set.OriginType, identity.UserID, sessionID, *appendRes.IssueCommand, prefix, assignedRunFireAt)
		if errors.Is(err, service.ErrActiveDuplicate) && issueRes.DuplicateIssue != nil {
			duplicate := *issueRes.DuplicateIssue
			res.IssueID = duplicate.ID
			res.IssueNumber = duplicate.Number
			res.IssueTitle = duplicate.Title
			res.IssueIdentifier = service.IssueIdentifier(prefix, duplicate.Number)
			res.IssueDuplicate = true
			// 重复是终端产品结果，不是基础设施故障、也不是聊天提示。完结持久化 chat
			// message 的媒体状态而不去解析它——没有新 issue 消费这些媒体，下载并持久化
			// 附件只会造出没人用的资源。
			if resolveMedia {
				r.enqueueMediaFinalization(set, inst, identity, appendRes.MessageID, msg, sessionID, localMediaDeadline)
			}
			return res, postAppendFinalize, nil
		}
		if err != nil {
			return Result{}, postAppendFinalize, fmt.Errorf("create issue from command: %w", err)
		}
		res.IssueID = issueRes.Issue.ID
		mediaIssue = issueRes.Issue
		deferredIssueTaskID = issueRes.AssignedTaskID
		res.IssueNumber = issueRes.Issue.Number
		res.IssueTitle = issueRes.Issue.Title
		// 与广播 payload 用同一个渲染器：降级的 prefix 不能让聊天里显示 "#42"、实时列表
		// 却显示 "-42"。
		res.IssueIdentifier = service.IssueIdentifier(prefix, issueRes.Issue.Number)
		// IssueService.Create 已把分配 agent 的 issue 任务入队。若再把这条命令排成普通
		// 聊天运行，agent 会把同一段 /issue 输入再执行一遍。同步 issue 命令到此为止。
		if resolveMedia {
			r.enqueueMedia(set, inst, identity, appendRes.MessageID, msg, sessionID, mediaIssue, pgtype.Text{
				String: appendRes.IssueCommand.Description,
				Valid:  true,
			}, msg.CommandText, deferredIssueTaskID, localMediaDeadline)
		}
		return res, postAppendFinalize, nil
	}

	// 8. 去抖运行触发。同步结果是"无 TaskID 的 OutcomeIngested"——任务行在 flush 时才
	//    创建。identity.UserID 是**本条消息**的发送者（任务发起者），刻意不是会话创建者
	//    （群会话创建者 = 安装者）。窗口内最新的发送者胜（MUL-2645）。
	//
	//    SkipAgentRun 让适配器把这条消息从 agent 轮次中摘出来——企微用于独立 /issue
	//    命令：引擎已做完有意义的工作（建了 issue、经 OutboundReplier 发了 "✅ Created
	//    #N" 回执），再跑 agent 只会让它复读一遍斜杠命令。chat_message 仍然持久化、
	//    OutboundReplier 仍会触发——只是被抑制了去抖运行触发（以及随之而来的打字指示器）。
	if !msg.SkipAgentRun && !startTaskCommitted {
		pendingContexts := appendRes.PendingContexts
		if len(pendingContexts) == 0 {
			pendingContexts = []PendingContext{{
				Revision: appendRes.ContextRevision, InitiatorUserID: identity.UserID,
			}}
		}
		for _, pending := range pendingContexts {
			revision := pending.Revision
			forceFresh := revision == appendRes.ContextRevision && msg.ForceFresh
			if revision == appendRes.ContextRevision {
				r.scheduleRunWithFresh(
					set, inst, msg, sessionID, identity.UserID,
					res.ChannelBindingID, res.ChannelRouteRevision, forceFresh, revision,
				)
			} else if pending.InitiatorUserID.Valid {
				r.scheduleRecoveredRun(
					set, inst, msg, sessionID, pending.InitiatorUserID,
					res.ChannelBindingID, res.ChannelRouteRevision, revision,
				)
			} else {
				slog.Warn("skipping recovered channel context without initiator snapshot",
					"chat_session_id", util.UUIDToString(sessionID),
					"context_revision", revision,
				)
			}
		}
	}
	res.runScheduled = !msg.SkipAgentRun && (startTaskCommitted || !startChat)
	if resolveMedia {
		r.enqueueMedia(set, inst, identity, appendRes.MessageID, msg, sessionID, mediaIssue, pgtype.Text{}, "", deferredIssueTaskID, localMediaDeadline)
	}
	if appendRes.BecameVisible && !startChat {
		r.notifyChatCreated(
			inst, sessionCreator, msg.Source.ChannelType, sessionID,
			appendRes.RouteRevision, appendRes.InitialTitle,
		)
	}
	if appendRes.InitialTitle != "" && r.lifecycle != nil {
		if !startChat && !appendRes.BecameVisible {
			r.lifecycle.ChannelChatTitleInitialized(inst.WorkspaceID, sessionCreator, sessionID, appendRes.InitialTitle)
		}
		r.lifecycle.GenerateChannelChatTitle(inst.WorkspaceID, sessionCreator, sessionID, appendRes.InitialTitle, chatTitleSource(msg.Text, msg.CommandText, !startChat && msg.ForceFresh))
	}
	return res, postAppendFinalize, nil
}

// notifyChatStarted 把"聊天已开始"的实时事件发布给 lifecycle（RouteRevision 路由轮换
// 后新建 Chat 用）。
func (r *Router) notifyChatStarted(inst ResolvedInstallation, creatorID pgtype.UUID, channelType channel.Type, started StartSessionResult) {
	r.notifyChatCreated(
		inst, creatorID, channelType, started.SessionID,
		started.RouteRevision, started.Append.InitialTitle,
	)
}

// notifyChatCreated 把 ChannelChatStarted 事件发布给 lifecycle（若无则空操作）。
func (r *Router) notifyChatCreated(inst ResolvedInstallation, creatorID pgtype.UUID, channelType channel.Type, sessionID pgtype.UUID, routeRevision int64, title string) {
	if r.lifecycle != nil {
		r.lifecycle.ChannelChatStarted(ChannelChatStartedEvent{
			WorkspaceID: inst.WorkspaceID, CreatorID: creatorID, AgentID: inst.AgentID,
			SessionID: sessionID, InstallationID: inst.ID,
			ChannelType: channelType, RouteRevision: routeRevision, Title: title,
		})
	}
}

// enqueueMedia 把远程媒体 I/O 从 Handle 上解耦，同时保留同一 chat 会话内的消息顺序。
// 运行调度独立且持久化：任务服务把任务推迟到持久化的媒体截止时刻，媒体完成后提前晋升
// 它。
func (r *Router) enqueueMedia(set ResolverSet, inst ResolvedInstallation, identity ResolvedIdentity, chatMessageID pgtype.UUID, msg channel.InboundMessage, sessionID pgtype.UUID, issue db.Issue, issueDescriptionBase pgtype.Text, issueCommandText string, issueTaskID pgtype.UUID, deadline time.Time) {
	r.enqueueMediaJob(set, inst, identity, chatMessageID, msg, sessionID, issue, issueDescriptionBase, issueCommandText, issueTaskID, true, deadline)
}

// enqueueMediaFinalization 不调用平台 resolver，只清掉持久的媒体待处理标记。重复 /issue
// 命令用这条路径：既有的 issue 与隐藏的命令消息都不该再获得一份"没有新 issue 会消费"
// 的媒体副本。
func (r *Router) enqueueMediaFinalization(set ResolverSet, inst ResolvedInstallation, identity ResolvedIdentity, chatMessageID pgtype.UUID, msg channel.InboundMessage, sessionID pgtype.UUID, deadline time.Time) {
	r.mediaQueueMu.Lock()
	if r.stopping {
		r.mediaQueueMu.Unlock()
		return
	}
	r.mediaWg.Add(1)
	r.mediaQueueMu.Unlock()

	go func() {
		defer r.mediaWg.Done()
		// 一条 channel_command 消息不会加入或门控 chat 任务的输入批次，所以清它自己的
		// 待处理标记不必等在会话有序的远程媒体队列后面。
		r.resolveAndBindMedia(set, inst, identity, chatMessageID, msg, sessionID, db.Issue{}, pgtype.Text{}, "", pgtype.UUID{}, false, deadline)
	}()
}

// enqueueMediaJob 把一条媒体的解析+绑定排进"该会话的链式队列"，保证同会话有序；
// 若 resolveRemote=false 则只做空终态（清标记 + 晋升）。
func (r *Router) enqueueMediaJob(set ResolverSet, inst ResolvedInstallation, identity ResolvedIdentity, chatMessageID pgtype.UUID, msg channel.InboundMessage, sessionID pgtype.UUID, issue db.Issue, issueDescriptionBase pgtype.Text, issueCommandText string, issueTaskID pgtype.UUID, resolveRemote bool, deadline time.Time) {
	key := keyForSession(sessionID)
	done := make(chan struct{})

	r.mediaQueueMu.Lock()
	if r.stopping {
		r.mediaQueueMu.Unlock()
		return
	}
	entry, ok := r.mediaQueues[key]
	var previous <-chan struct{}
	if !ok {
		entry = &mediaQueueEntry{}
		r.mediaQueues[key] = entry
	} else {
		previous = entry.tail
	}
	entry.tail = done
	r.mediaWg.Add(1)
	r.mediaQueueMu.Unlock()

	go func() {
		defer r.mediaWg.Done()
		defer close(done)
		defer r.finishMediaQueue(key, done)
		// 两段队列等待都以消息自身的截止为界、不只以全局停机为界：媒体突发时，一条已过期
		// 的任务不该一直占着自己的 goroutine 与 payload 排队到队头——它直接跳到"空终态"
		//（清标记 + 晋升），同时解开同会话后面的消息。
		expiry := time.NewTimer(time.Until(deadline))
		defer expiry.Stop()
		expired := false
		if previous != nil {
			select {
			case <-previous:
			case <-r.mediaCtx.Done():
			case <-expiry.C:
				expired = true
			}
		}
		if !expired && resolveRemote {
			select {
			case r.mediaSem <- struct{}{}:
				defer func() { <-r.mediaSem }()
			case <-r.mediaCtx.Done():
				// 排队等槽位时被取消：无槽继续。死 context 上会跳过 ResolveMedia，只跑
				// 有界 DB 终态——停机时占位标记仍能及时清除。
			case <-expiry.C:
				// 排队等槽位时已过期：不需要槽位——resolveAndBindMedia 看到死截止，只跑
				// 空终态。
			}
		}
		r.resolveAndBindMedia(set, inst, identity, chatMessageID, msg, sessionID, issue, issueDescriptionBase, issueCommandText, issueTaskID, resolveRemote, deadline)
	}()
}

// mediaFinalizeTimeout 是有界 DB 媒体终态（绑定/清标记/晋升）的预算。
const mediaFinalizeTimeout = 5 * time.Second

// resolveAndBindMedia 是解耦媒体任务的执行体：先解析远程媒体（resolveRemote 时），再
// 在有界 context 上绑定附件、清标记、按需晋升延迟任务、发布附件变更。
func (r *Router) resolveAndBindMedia(set ResolverSet, inst ResolvedInstallation, identity ResolvedIdentity, chatMessageID pgtype.UUID, msg channel.InboundMessage, sessionID pgtype.UUID, issue db.Issue, issueDescriptionBase pgtype.Text, issueCommandText string, issueTaskID pgtype.UUID, resolveRemote bool, deadline time.Time) {
	ctx, cancel := context.WithDeadline(r.mediaCtx, deadline)
	defer cancel()

	resolved := msg
	if !resolveRemote {
		resolved.MediaRefs = nil
	} else if ctx.Err() == nil {
		// 排队期间预算耗尽（或停机）时整体跳过：在死 context 上解析只会空转一堆立刻
		// 失败的意图写入。
		resolved = set.Media.ResolveMedia(ctx, inst, identity, sessionID, chatMessageID, msg)
	}
	finalizeCtx, finalizeCancel := context.WithTimeout(context.Background(), mediaFinalizeTimeout)
	defer finalizeCancel()
	if err := ctx.Err(); resolveRemote && err != nil {
		// 截止前解析出的引用已经躺在对象存储里，但不会获得附件行。这里**什么都不删**——
		// 它们的意图台账行在上传前已写入，对账器在落定延迟后回收未被引用的对象。
		resolved.MediaRefs = nil
		r.logger.Warn("channel router: media resolution incomplete; using placeholder",
			"channel_type", string(msg.Source.ChannelType),
			"event_id", msg.EventID,
			"message_id", msg.MessageID,
			"error", err)
	}
	bindRes, bindErr := set.Session.BindMedia(finalizeCtx, BindMediaParams{
		MessageID:            chatMessageID,
		SessionID:            sessionID,
		WorkspaceID:          inst.WorkspaceID,
		Sender:               identity.UserID,
		IssueID:              issue.ID,
		IssueDescriptionBase: issueDescriptionBase,
		IssueCommandText:     issueCommandText,
		Body:                 resolved.Text,
		MediaRefs:            resolved.MediaRefs,
	})
	if bindErr == nil && bindRes.InitialTitle != "" && r.lifecycle != nil {
		creatorID := identity.UserID
		if msg.Source.ChatType == channel.ChatTypeGroup {
			creatorID = inst.InstallerUserID
		}
		r.lifecycle.ChannelChatTitleInitialized(inst.WorkspaceID, creatorID, sessionID, bindRes.InitialTitle)
		r.lifecycle.GenerateChannelChatTitle(inst.WorkspaceID, creatorID, sessionID, bindRes.InitialTitle, bindRes.TitleSource)
	}
	if bindErr != nil {
		// 绝不内联删除：附件可能已落库也可能没有（歧义提交），但意图行与绑定在**同一**
		// 事务里，台账已反映任一种持久化结果，对象交给对账器收尾。
		r.logger.Warn("channel router: media attachment binding failed",
			"channel_type", string(msg.Source.ChannelType),
			"event_id", msg.EventID,
			"message_id", msg.MessageID,
			"err", bindErr)
	}
	if bindErr == nil && issue.ID.Valid && len(resolved.MediaRefs) > 0 {
		r.issues.PublishAttachmentsChanged(finalizeCtx, issue, identity.UserID)
	}
	if issueTaskID.Valid {
		if err := r.tasks.PromoteDeferredChannelIssueTask(finalizeCtx, issueTaskID); err != nil {
			r.logger.Warn("channel router: media-ready issue task promotion failed",
				"channel_type", string(msg.Source.ChannelType),
				"event_id", msg.EventID,
				"message_id", msg.MessageID,
				"task_id", util.UUIDToString(issueTaskID),
				"err", err)
		}
	}
	if err := r.tasks.PromoteChannelChatTasksIfMediaReady(finalizeCtx, sessionID); err != nil {
		r.logger.Warn("channel router: media-ready task promotion failed",
			"channel_type", string(msg.Source.ChannelType),
			"event_id", msg.EventID,
			"message_id", msg.MessageID,
			"err", err)
	}
}

// finishMediaQueue 移除会话队列条目（仅在链尾仍是自己时，防止误删后续排队者）。
func (r *Router) finishMediaQueue(key string, done chan struct{}) {
	r.mediaQueueMu.Lock()
	defer r.mediaQueueMu.Unlock()
	entry, ok := r.mediaQueues[key]
	if !ok || entry.tail != done {
		return
	}
	delete(r.mediaQueues, key)
}

// scheduleRunWithFresh 把每会话/上下文的运行触发交给去抖器（未启用批处理时内联触发）。
func (r *Router) scheduleRunWithFresh(
	set ResolverSet,
	inst ResolvedInstallation,
	msg channel.InboundMessage,
	sessionID, initiatorUserID, bindingID pgtype.UUID,
	routeRevision int64,
	fresh bool,
	contextRevision int64,
) {
	r.scheduleRunMode(
		set, inst, msg, sessionID, initiatorUserID, bindingID,
		routeRevision, fresh, contextRevision, true,
	)
}

// scheduleRecoveredRun 为崩溃恢复出的较老上下文代补排运行（不去重替换，也不借用
// 新发送者身份）。
func (r *Router) scheduleRecoveredRun(
	set ResolverSet,
	inst ResolvedInstallation,
	msg channel.InboundMessage,
	sessionID, initiatorUserID, bindingID pgtype.UUID,
	routeRevision, contextRevision int64,
) {
	r.scheduleRunMode(
		set, inst, msg, sessionID, initiatorUserID, bindingID,
		routeRevision, false, contextRevision, false,
	)
}

// scheduleRunMode 是两种排程的共同实现：replace=true 时同一键的新消息可顶替旧闭包
// （去抖窗口刷新）；false 时只补缺（见 batcher.ScheduleIfAbsent）。
func (r *Router) scheduleRunMode(
	set ResolverSet,
	inst ResolvedInstallation,
	msg channel.InboundMessage,
	sessionID, initiatorUserID, bindingID pgtype.UUID,
	routeRevision int64,
	fresh bool,
	contextRevision int64,
	replace bool,
) {
	if r.batcher == nil {
		r.flushChatRun(
			set, inst, msg, sessionID, initiatorUserID, bindingID,
			routeRevision, fresh, contextRevision,
		)
		return
	}
	key := keyForSessionContext(sessionID, contextRevision)
	flush := func() {
		// 同代内的更晚消息可能顶替这个闭包。/clear 会推进 context 代、因而用不同的批次
		// 键；边界前的 flush 仍独立武装着。
		r.flushChatRun(
			set, inst, msg, sessionID, initiatorUserID, bindingID,
			routeRevision, fresh, contextRevision,
		)
	}
	if replace {
		r.batcher.Schedule(key, flush)
	} else {
		r.batcher.ScheduleIfAbsent(key, flush)
	}
}

// chatRunFlushTimeout 限制解耦的 flush（会话重载 + 入队 + 通知），它在自己的新 context
// 上跑。
const chatRunFlushTimeout = 10 * time.Second

// flushChatRun 是去抖的运行触发：重载会话、为该窗口恰好入队一条聊天任务、经 replier 发
// 离线/归档通知（此刻才知道）。错误记日志、不返回。
func (r *Router) flushChatRun(
	set ResolverSet,
	inst ResolvedInstallation,
	msg channel.InboundMessage,
	sessionID, initiatorUserID, bindingID pgtype.UUID,
	routeRevision int64,
	forceFresh bool,
	contextRevision int64,
) {
	ctx, cancel := context.WithTimeout(context.Background(), chatRunFlushTimeout)
	defer cancel()

	session, err := r.reader.GetChatSession(ctx, sessionID)
	if err != nil {
		r.logger.Error("channel router: flush reload chat session failed",
			"chat_session_id", uuidString(sessionID), "err", err.Error())
		r.clearTyping(ctx, set, sessionID)
		return
	}
	if _, err := r.tasks.EnqueueChannelChatTask(
		ctx, session, initiatorUserID, forceFresh, contextRevision, bindingID, routeRevision,
	); err != nil {
		// 没有任务入队，任务生命周期事件永远不会发布，平台靠总线驱动的打字清除也就永远
		// 不会触发。在（任何通知之前的）这里清掉指示器，"处理中"反应才不会贴在用户的
		// 消息上。
		r.clearTyping(ctx, set, sessionID)
		switch {
		case errors.Is(err, service.ErrChatTaskAgentNoRuntime):
			r.emitFlushReply(ctx, set, inst, msg, sessionID, bindingID, routeRevision, OutcomeAgentOffline)
		case errors.Is(err, service.ErrChatTaskAgentArchived):
			r.emitFlushReply(ctx, set, inst, msg, sessionID, bindingID, routeRevision, OutcomeAgentArchived)
		default:
			r.logger.Error("channel router: flush enqueue chat task failed",
				"chat_session_id", uuidString(sessionID), "err", err.Error())
		}
	}
}

// clearTyping 请平台摘掉"处理中"指示器——用于 flush 没有产出任务运行的会话。
// TypingNotifier 为 nil（平台没有此特性）时空操作。
func (r *Router) clearTyping(ctx context.Context, set ResolverSet, sessionID pgtype.UUID) {
	if set.Typing != nil {
		set.Typing.OnSettled(ctx, sessionID)
	}
}

// emitFlushReply 为被 flush 的运行投递离线/归档通知。
func (r *Router) emitFlushReply(ctx context.Context, set ResolverSet, inst ResolvedInstallation, msg channel.InboundMessage, sessionID, bindingID pgtype.UUID, routeRevision int64, outcome Outcome) {
	if set.Replier == nil {
		return
	}
	set.Replier.Reply(ctx, inst, msg, Result{
		Outcome:              outcome,
		InstallationID:       inst.ID,
		ChatSessionID:        sessionID,
		ChannelBindingID:     bindingID,
		ChannelRouteRevision: routeRevision,
		Sender:               msg.Source.SenderID,
	})
}

// scheduleReply 把 OutboundReplier 从 ACK 关键路径上解耦。回复 goroutine 用带
// ReplyTimeout 截止的新 context，独立于入站 emit ctx（适配器在收消息循环退出时取消后者）。
// nil replier 短路——不起 goroutine。
func (r *Router) scheduleReply(set ResolverSet, inst ResolvedInstallation, msg channel.InboundMessage, res Result) {
	if set.Replier == nil {
		return
	}
	r.replyWg.Add(1)
	go func() {
		defer r.replyWg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), r.replyTimeout)
		defer cancel()
		set.Replier.Reply(ctx, inst, msg, res)
		if ctx.Err() == context.DeadlineExceeded {
			r.logger.Warn("channel router: outbound reply timed out",
				"event_id", msg.EventID, "outcome", string(res.Outcome),
				"timeout", r.replyTimeout.String())
		}
	}()
}

// keyForSession 是媒体队列的键。chat_session_id 全局唯一。
func keyForSession(sessionID pgtype.UUID) string {
	return string(sessionID.Bytes[:])
}

// keyForSessionContext 是去抖器的键：会话 id + 上下文代。
func keyForSessionContext(sessionID pgtype.UUID, contextRevision int64) string {
	return fmt.Sprintf("%s:%d", keyForSession(sessionID), contextRevision)
}

// applyFinalize 把在途认领行翻到它的终态，token 围栏保护。尽力而为：一次传输失败不能
// 撤销已经确定的结果。
func (r *Router) applyFinalize(ctx context.Context, set ResolverSet, instID pgtype.UUID, messageID string, claimToken pgtype.UUID, action dedupFinalize) {
	switch action {
	case finalizeMark:
		_ = set.Dedup.Mark(ctx, instID, messageID, claimToken)
	case finalizeRelease:
		_ = set.Dedup.Release(ctx, instID, messageID, claimToken)
	case finalizeNone:
	}
}

// drop 记录一次带原因的丢弃审计并返回 OutcomeDropped 结果。
func (r *Router) drop(ctx context.Context, set ResolverSet, msg channel.InboundMessage, instID pgtype.UUID, reason DropReason) Result {
	_ = set.Audit.RecordDrop(ctx, instID, msg, reason)
	return Result{Outcome: OutcomeDropped, DropReason: reason, InstallationID: instID}
}

// createIssue 把 /issue 命令翻译成 service.IssueCreate 的调用。assignedRunFireAt 供"等
// 媒体先绑定再让 agent 读 issue"的延迟任务用。
func (r *Router) createIssue(ctx context.Context, inst ResolvedInstallation, originType string, creatorUserID, sessionID pgtype.UUID, cmd IssueCommand, issuePrefix string, assignedRunFireAt time.Time) (service.IssueCreateResult, error) {
	if cmd.Title == "" {
		return service.IssueCreateResult{}, ErrEmptyIssueTitle
	}
	params := service.IssueCreateParams{
		WorkspaceID:  inst.WorkspaceID,
		Title:        cmd.Title,
		Description:  pgtype.Text{String: cmd.Description, Valid: cmd.Description != ""},
		Status:       "todo",
		Priority:     "none",
		AssigneeType: pgtype.Text{String: "agent", Valid: true},
		AssigneeID:   inst.AgentID,
		CreatorType:  "member",
		CreatorID:    creatorUserID,
		OriginType:   pgtype.Text{String: originType, Valid: originType != ""},
		OriginID:     sessionID,
	}
	// 不带 BroadcastPayload 时服务只发它的最小 {"issue_id": ...} 桩，每个读 "issue" 键
	// 的 issue:created 消费者都会丢弃该事件——最显眼的是订阅者监听器，它需要 id +
	// creator_id，因此从未订阅"敲 /issue 的那个人自己的 issue"。IssueToMap 是在 HTTP
	// handler 之外发布事件的共享渲染器；它与 handler 路径广播的 IssueResponse 保持键
	// 兼容，客户端无论从哪个入口建的 issue 都看到同一种形状。
	opts := service.IssueCreateOpts{
		AssignedAgentRunFireAt: assignedRunFireAt,
		BroadcastPayload: func(issue db.Issue, _ []db.Attachment, _ []db.IssueLabel) map[string]any {
			// 此处直接用 IssueToMap 是权威选择：该路径永远用上面的内置 "todo" 创建，
			// 内置状态本身就是它的类别，所以不可能也不需要读目录。（MUL-6243）
			return map[string]any{"issue": service.IssueToMap(issue, issuePrefix)}
		},
	}
	return r.issues.Create(ctx, params, opts)
}

// issuePrefix 读工作区的 issue 键（"MUL-42" 里的 "MUL"）。一次读失败不值得让建 issue
// 失败，所以降级为空——只是渲染出的标识缺了前缀。
func (r *Router) issuePrefix(ctx context.Context, workspaceID pgtype.UUID) string {
	ws, err := r.reader.GetWorkspace(ctx, workspaceID)
	if err != nil {
		r.logger.Warn("channel engine: workspace lookup for issue prefix failed",
			"workspace_id", util.UUIDToString(workspaceID), "error", err)
		return ""
	}
	return ws.IssuePrefix
}

// ErrEmptyIssueTitle 是防御性不变量错误。用户手写空标题时，Router 在调 createIssue 前就
// 按 OutcomeIssueUsage 处理了。
var ErrEmptyIssueTitle = errors.New("issue title is empty")

var _ channel.InboundHandler = (*Router)(nil).Handle
