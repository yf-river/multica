package telegram

import (
	"container/heap"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// 角色定位：把 agent 的聊天回复送回 Telegram——一次往返的"出站半边"，对齐 lark.Patcher /
// slack.Outbound，都是挂在同一张共享事件总线上的订阅者。
//
// 流式输出：Telegram 没有流式更新协议，所以用该平台的标准做法模拟 "stream 帧" 体验——
// 第一批 token 到达时先发一条占位消息，之后 agent 的文本越长越多就调用限速的
// editMessageText 原地刷新（对应 EventTaskMessage 文本帧），到 EventChatDone 做最后一次
// edit 或补发。edit 按聊天粒度限速，确保不超过 Telegram 对 editMessageText 的速率预算；
// 真被 429 限流时流式部分退避停下，但最终完整内容一定通过 EventChatDone 路径送达。
//
// 并发模型（两个关键决策）：
//  1. 事件总线是同步的，所以 EventChatDone 的 handler 只做"入有界队列"就返回，绝不能就地
//     做 Telegram I/O——那会把实时 fanout 卡在 retry_after 等待和慢网络请求后面；
//  2. 真正的发送工作交给一组固定 worker + 一个按 (bot,chat) 键控的调度器：同一聊天的多条
//     任务共用一份速率状态（lastEdit / backoffTill），不同会话不会互相拖慢。
type Outbound struct {
	q       outboundQueries
	decrypt Decrypter
	logger  *slog.Logger
	apiBase string
	client  *http.Client

	mu                 sync.Mutex
	streams            map[string]*streamState // 键 = task_id：流式占位消息的中间态
	chats              map[chatScheduleKey]*chatSchedule
	botFallbackBackoff map[string]time.Time
	now                func() time.Time
	wait               func(context.Context, time.Duration) error

	// 终态投递（EventChatDone 之后真正发完整回复）的三段式调度状态：
	// dispatchTerminalReplies 从 ready 队列挑会话 → 固定 worker 发（每轮最多一次 API
	// 调用）→ 结果回 terminalResults 更新队列；429/限速的会话进 terminalRetries 小顶堆
	// 按时醒来。这块状态全都挂在 terminalMu 这把独立锁下。
	terminalMu               sync.Mutex
	terminalSessions         map[string]*terminalSession
	terminalReady            []string
	terminalRetries          terminalRetryHeap
	terminalWake             chan struct{}
	terminalWork             chan terminalWork
	terminalResults          chan terminalResult
	terminalStopped          bool
	terminalInFlight         int
	queuedTerminalReplyCount int
	queuedTerminalReplyBytes int
	workerOnce               sync.Once
	workerWG                 sync.WaitGroup
	terminalWorkerWG         sync.WaitGroup
}

// outboundQueries 是订阅者需要的两张生成查询的切片接口；*db.Queries 天然满足它。
type outboundQueries interface {
	GetChannelTaskDelivery(ctx context.Context, taskID pgtype.UUID) (db.ChannelTaskDelivery, error)
	GetChannelInstallation(ctx context.Context, arg db.GetChannelInstallationParams) (db.ChannelInstallation, error)
}

// streamState 追踪一条在途的流式回复。一个 task 一条；task 结束（EventChatDone /
// TaskFailed / Cancelled）时清除。
type streamState struct {
	chatID      int64
	threadID    int64
	replyTo     int64
	messageID   int64  // 正在被 edit 的占位消息 id；0 表示还没发第一条
	accumulated string // agent 到目前为止吐出的全部文本（增量 edit 的基底）
	schedule    *chatSchedule
}

// chatSchedule 串行化"同一条聊天"的 Telegram 投递，并持有所有发往该聊天的任务共享的
// 速率限制状态。refs / idleSince 受 Outbound.mu 保护；lastEdit / backoffTill 受本结构
// 自己的 mu 保护。一份调度实例 = 一张 o.chats 里的条目。
type chatSchedule struct {
	mu          sync.Mutex
	key         chatScheduleKey
	refs        int // 正在使用该调度的流/任务数；降到 0 后开始记 idle 计时
	lastEdit    time.Time
	backoffTill time.Time
	backoffUnix atomic.Int64 // backoffTill 的无锁快照（见 setBackoffTill）
	idleSince   time.Time
}

// chatScheduleKey 唯一标识一份调度状态：同一个 bot（安装）的同一个聊天。
type chatScheduleKey struct {
	botKey string
	chatID int64
}

// terminalSession 是一次"终态投递"会话在队列层的状态机。一个 ChatSessionID 一个。
type terminalSession struct {
	queue        []*terminalReply // 按到达顺序排队的待发回复；上限见 maxQueuedTerminalReplies（全局 64）/ maxQueuedTerminalRepliesPerSession（每会话 8）
	running      bool             // 有一个回复正被 worker 处理
	ready        bool             // 在 terminalReady 名单里等着被派发
	retryWaiting bool             // 因 429/限速在 terminalRetries 堆里等重试
}

// terminalReply 是一条待终态投递的 EventChatDone 回复的全部状态：从切好的分片、进度下标
// 到降级开关。跨多次 worker 轮次存活，直到全部发完（done）才清理。
type terminalReply struct {
	event             events.Event
	byteSize          int
	initialized       bool // 是否已完成目标解析与分片（懒初始化，见 sendNextTerminalRequest）
	target            *replyTarget
	schedule          *chatSchedule
	chunks            []string
	chunkIndex        int
	streamedMessageID int64 // 流式占位消息 id：能 edit 就先 edit 它，省一条新消息
	placeholderEdited bool  // 占位消息是否已经 edit 成第一片
	fallbackFreshSend bool  // 占位 edit 失败后是否已改为"直接发一条新消息"
	plainTextFallback bool  // 是否已降级成纯文本（HTML 被拒时的退路）
	cleanupOnce       sync.Once
}

type terminalWork struct {
	sessionID string
	reply     *terminalReply
}

type terminalResult struct {
	terminalWork
	done    bool
	retryAt time.Time
	err     error
}

type terminalRetry struct {
	sessionID string
	retryAt   time.Time
}

// terminalRetryHeap 是按 retryAt 升序的小顶堆：调度器只关心"下一个最早该醒来的会话"。
type terminalRetryHeap []terminalRetry

func (h terminalRetryHeap) Len() int           { return len(h) }
func (h terminalRetryHeap) Less(i, j int) bool { return h[i].retryAt.Before(h[j].retryAt) }
func (h terminalRetryHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *terminalRetryHeap) Push(x any)        { *h = append(*h, x.(terminalRetry)) }
func (h *terminalRetryHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

// editInterval 是同一条聊天里两次 editMessageText 的最小间隔。Telegram 大体容忍每聊天
// 每秒约一次 edit，而对群组有更严的预算（约 20 条/分钟）；2.5 秒让一次长生成全程都能
// 舒服地卡在两套预算之内，又不会显得"卡死不动"。
const editInterval = 2500 * time.Millisecond

// 各类容量/时长上限。设计意图：idle 的调度记录保持短暂可复用（让紧挨着的两条任务、
// 以及任务被取消的场景不至于丢掉该聊天的 edit 冷却或 Telegram retry_after 窗口），
// 但调度表与"压缩降级"表都设了硬上限，防止长期运行内存无限增长。
const (
	chatScheduleIdleTTL                = 10 * time.Minute
	maxChatSchedules                   = 1024
	terminalWorkerCount                = 4
	maxBotFallbacks                    = 1024
	chatCapacityRetry                  = time.Second
	maxQueuedTerminalReplies           = 64
	maxQueuedTerminalRepliesPerSession = 8
	maxQueuedTerminalReplyBytes        = 16 << 20
)

// streamPlaceholder 是首批 token 还没到齐时第一帧的占位文案。
const streamPlaceholder = "…"

// taskFailedText 是 agent 运行彻底失败时发给用户的提示。
const taskFailedText = "❌ The agent run failed. Please try again."

// NewOutbound 组装 Telegram 出站订阅者。
func NewOutbound(q outboundQueries, decrypt Decrypter, apiBase string, client *http.Client, logger *slog.Logger) *Outbound {
	if logger == nil {
		logger = slog.Default()
	}
	o := &Outbound{
		q:                  q,
		decrypt:            decrypt,
		logger:             logger,
		apiBase:            apiBase,
		client:             client,
		streams:            make(map[string]*streamState),
		chats:              make(map[chatScheduleKey]*chatSchedule),
		botFallbackBackoff: make(map[string]time.Time),
		now:                time.Now,
		wait:               waitForOutbound,
		terminalSessions:   make(map[string]*terminalSession),
		terminalWake:       make(chan struct{}, 1),
		terminalWork:       make(chan terminalWork, terminalWorkerCount),
		terminalResults:    make(chan terminalResult, terminalWorkerCount),
	}
	return o
}

// Register 订阅转写/完成/失败四类事件。入站是 agent 运行过程，完成与失败会走到终态投递。
func (o *Outbound) Register(bus *events.Bus) {
	bus.Subscribe(protocol.EventTaskMessage, o.handleTaskMessage)
	bus.Subscribe(protocol.EventChatDone, o.enqueueTerminalReply)
	bus.Subscribe(protocol.EventTaskFailed, o.handleTaskFailed)
	bus.Subscribe(protocol.EventTaskCancelled, o.handleTaskCancelled)
}

// Start 启动异步的终态投递 worker。为什么必须异步：EventChatDone 发布在同步的处理总线
// 上，其 handler 只能入队；Telegram 的速率限制和网络延迟绝不能拖住实时 fanout。
func (o *Outbound) Start(ctx context.Context) {
	o.workerOnce.Do(func() {
		o.workerWG.Add(1)
		o.terminalWorkerWG.Add(terminalWorkerCount)
		for range terminalWorkerCount {
			go o.sendTerminalReplies(ctx)
		}
		go o.dispatchTerminalReplies(ctx)
		o.wakeTerminalDispatcher()
	})
}

// WaitWithTimeout 给优雅停机加上限：事件总线不该被 Telegram 投递延迟无限牵制。
func (o *Outbound) WaitWithTimeout(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		o.workerWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// handleTaskMessage 处理一条流式分片：agent 每来一帧文本，就更新占位消息（限速）。
// 总线投递是同步的，所以这里的一切都必须在紧凑超时内完成，并且绝不向上抛错。
func (o *Outbound) handleTaskMessage(e events.Event) {
	payload, ok := e.Payload.(protocol.TaskMessagePayload)
	if !ok || payload.Type != "text" || payload.Content == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	target, err := o.resolveTarget(ctx, e, true)
	if err != nil || target == nil {
		return
	}

	o.mu.Lock()
	st, exists := o.streams[target.streamKey]
	if !exists {
		// 第一条文本帧：建流状态，同时占住（retain）该聊天的速率调度。
		schedule := o.retainChatLocked(target.botKey, target.chatID)
		if schedule == nil {
			o.mu.Unlock()
			return
		}
		st = &streamState{
			chatID: target.chatID, threadID: target.threadID, replyTo: target.replyTo,
			schedule: schedule,
		}
		o.streams[target.streamKey] = st
	}
	st.accumulated += payload.Content
	snapshot := st.accumulated
	msgID := st.messageID
	o.mu.Unlock()

	o.pushPartial(ctx, target, st, msgID, snapshot)
}

// pushPartial 第一次 flush 时发占位消息，之后都改成 edit。
func (o *Outbound) pushPartial(ctx context.Context, target *replyTarget, st *streamState, msgID int64, snapshot string) {
	if !st.schedule.mu.TryLock() {
		return
	}
	defer st.schedule.mu.Unlock()
	now := o.now()
	// 这条聊天正处在限速退避期：这次分片先跳过（不排队），下一帧来了再说。
	if o.terminalAvailableAt(st.schedule, target.botKey, now).After(now) {
		return
	}

	api := newBotAPI(o.apiBase, target.botToken, o.client)
	text := snapshot
	if utf16Units(text) > maxMessageUnits {
		// 流式中途就超长：把流式占位消息冻结在上限处；完整回复由最终的 EventChatDone
		// 发送按分片补齐（这里不追着 edit，避免在长文本上反复烧 API 预算）。
		text = chunkMessage(text, maxMessageUnits)[0]
	}
	if msgID == 0 {
		var reply *replyParameters
		if st.replyTo != 0 {
			reply = &replyParameters{MessageID: st.replyTo, AllowSendingWithoutReply: true}
		}
		m, err := api.SendMessage(ctx, sendMessageParams{
			ChatID:          st.chatID,
			Text:            firstNonEmpty(formatHTML(text), streamPlaceholder),
			ParseMode:       "HTML",
			MessageThreadID: st.threadID,
			ReplyParameters: reply,
		})
		if err != nil {
			o.noteEditFailure(st.schedule, err)
			return
		}
		st.schedule.lastEdit = o.now()
		o.mu.Lock()
		st.messageID = m.MessageID
		o.mu.Unlock()
		return
	}
	err := api.EditMessageText(ctx, editMessageTextParams{
		ChatID:    st.chatID,
		MessageID: msgID,
		Text:      formatHTML(text),
		ParseMode: "HTML",
	})
	if err != nil && !isNotModified(err) {
		o.noteEditFailure(st.schedule, err)
		return
	}
	st.schedule.lastEdit = o.now()
}

// noteEditFailure 把 429 强制的退避应用到该流；其它失败只记日志、流不再 edit
// （最终完整内容仍会经 EventChatDone 送达，这里放弃的是中间态刷新而非最终结果）。
func (o *Outbound) noteEditFailure(schedule *chatSchedule, err error) {
	if wait, ok := retryAfter(err); ok {
		schedule.setBackoffTill(o.now().Add(wait))
		return
	}
	o.logger.Warn("telegram outbound: stream edit failed", "error", err)
}

// enqueueTerminalReply 只做一件事：往"按会话分组的有界 FIFO"尾部追加一条。events.Bus
// 是同步的；在这里做 Telegram I/O 会把 SubscribeAll 的实时 fanout 卡在 retry_after
// 等待和慢网络请求后面。
func (o *Outbound) enqueueTerminalReply(e events.Event) {
	_, hasTaskID := eventTaskID(e)
	sessionID, sessionErr := util.ParseUUID(e.ChatSessionID)
	if !hasTaskID || sessionErr != nil || !sessionID.Valid {
		o.logger.Error("telegram outbound: terminal reply has invalid identity",
			"task_id", e.TaskID, "chat_session_id", e.ChatSessionID)
		o.clearStream(e)
		return
	}
	content := chatDoneContent(e.Payload)
	if content == "" {
		o.clearStream(e)
		return
	}
	sessionKey := e.ChatSessionID
	replyBytes := len(content)
	o.terminalMu.Lock()
	if o.terminalStopped {
		o.terminalMu.Unlock()
		o.clearStream(e)
		return
	}
	session := o.terminalSessions[sessionKey]
	if session == nil {
		session = &terminalSession{}
		o.terminalSessions[sessionKey] = session
	}
	// 硬上限逐层校验：队列总量 / 单会话数量 / 总字节。任何一个超了都不再收，就地丢弃
	// 并打错误日志（宁可丢一条展示型回复，也不能让内存与下游发送无限积压）。
	if o.queuedTerminalReplyCount >= maxQueuedTerminalReplies ||
		len(session.queue) >= maxQueuedTerminalRepliesPerSession ||
		replyBytes > maxQueuedTerminalReplyBytes-o.queuedTerminalReplyBytes {
		count := o.queuedTerminalReplyCount
		bytes := o.queuedTerminalReplyBytes
		perSession := len(session.queue)
		if len(session.queue) == 0 {
			delete(o.terminalSessions, sessionKey)
		}
		o.terminalMu.Unlock()
		o.clearStream(e)
		o.logger.Error("telegram outbound: terminal reply queue capacity exceeded",
			"task_id", e.TaskID, "chat_session_id", e.ChatSessionID,
			"queued_count", count, "queued_count_limit", maxQueuedTerminalReplies,
			"session_queued_count", perSession, "session_queued_count_limit", maxQueuedTerminalRepliesPerSession,
			"queued_bytes", bytes, "reply_bytes", replyBytes, "queued_bytes_limit", maxQueuedTerminalReplyBytes)
		return
	}
	session.queue = append(session.queue, &terminalReply{event: e, byteSize: replyBytes})
	o.queuedTerminalReplyCount++
	o.queuedTerminalReplyBytes += replyBytes
	// 会话空闲（没在跑、没排队等派发、也没在等重试）就把它放进 ready 名单。
	if !session.running && !session.ready && !session.retryWaiting {
		o.queueTerminalReadyLocked(sessionKey, session)
	}
	o.terminalMu.Unlock()
	o.wakeTerminalDispatcher()
}

// queueTerminalReadyLocked 把会话放进 ready 名单（调用方须持有 terminalMu）。幂等：
// 已在名单里/正在跑/正在等重试都不重复入列。
func (o *Outbound) queueTerminalReadyLocked(sessionID string, session *terminalSession) {
	if session == nil || len(session.queue) == 0 || session.ready || session.running || session.retryWaiting {
		return
	}
	session.ready = true
	o.terminalReady = append(o.terminalReady, sessionID)
}

// wakeTerminalDispatcher 非阻塞地叫醒调度器（缓冲区容量 1，醒着时无需再叫）。
func (o *Outbound) wakeTerminalDispatcher() {
	select {
	case o.terminalWake <- struct{}{}:
	default:
	}
}

// dispatchTerminalReplies 是终态投递的"大脑"：把到点的重试唤醒并重新入 ready 名单，
// 再从 ready 名单里按 worker 空闲额度挑会话派给 worker。挑出来的会话在整个回复发完前
// 不再派第二个（保持同会话 FIFO 且每会话只有一个在途请求）。
func (o *Outbound) dispatchTerminalReplies(ctx context.Context) {
	defer o.workerWG.Done()
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()

	for {
		o.terminalMu.Lock()
		now := o.now()
		// 第 1 步：把到点的重试会话重新入 ready 名单。
		for o.terminalRetries.Len() > 0 && !o.terminalRetries[0].retryAt.After(now) {
			retry := heap.Pop(&o.terminalRetries).(terminalRetry)
			session := o.terminalSessions[retry.sessionID]
			if session == nil || !session.retryWaiting {
				continue
			}
			session.retryWaiting = false
			o.queueTerminalReadyLocked(retry.sessionID, session)
		}
		// 第 2 步：有空闲 worker 且有 ready 会话就派活。派的是 queue[0]（队头）；
		// 会话从 running 到结果回来前不会再被派。
		for o.terminalInFlight < terminalWorkerCount && len(o.terminalReady) > 0 {
			sessionID := o.terminalReady[0]
			o.terminalReady[0] = ""
			o.terminalReady = o.terminalReady[1:]
			session := o.terminalSessions[sessionID]
			if session == nil || len(session.queue) == 0 || !session.ready {
				continue
			}
			session.ready = false
			session.running = true
			o.terminalInFlight++
			o.terminalWork <- terminalWork{sessionID: sessionID, reply: session.queue[0]}
		}
		// 第 3 步：若还有未到点的重试，把 timer 拨到最早那次。
		var timerC <-chan time.Time
		if o.terminalRetries.Len() > 0 {
			delay := o.terminalRetries[0].retryAt.Sub(o.now())
			if delay < 0 {
				delay = 0
			}
			timer.Reset(delay)
			timerC = timer.C
		}
		o.terminalMu.Unlock()

		select {
		case <-ctx.Done():
			o.stopTerminalScheduler()
			return
		case result := <-o.terminalResults:
			o.updateQueueAfterTerminalRequest(result)
		case <-o.terminalWake:
		case <-timerC:
		}
		if timerC != nil && !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
}

// updateQueueAfterTerminalRequest 处理一个 worker 结果：从 running 恢复会话，按结果决定
// 是推进（done）到下一队头、转重试等待，还是立刻重新入 ready。结果带 sessionID + 原
// reply 指针，用于核对"确实是当前队头"（防止陈旧结果错配新队头）。
func (o *Outbound) updateQueueAfterTerminalRequest(result terminalResult) {
	o.terminalMu.Lock()
	defer o.terminalMu.Unlock()
	session := o.terminalSessions[result.sessionID]
	if session == nil || len(session.queue) == 0 || session.queue[0] != result.reply {
		return
	}
	session.running = false
	o.terminalInFlight--
	if result.done {
		// 整条回复发完（或确定放弃）：出队并归还全局配额。
		if result.err != nil {
			o.logger.Warn("telegram outbound: reply delivery failed",
				"error", result.err, "chat_session_id", result.reply.event.ChatSessionID)
		}
		o.queuedTerminalReplyCount--
		o.queuedTerminalReplyBytes -= result.reply.byteSize
		session.queue[0] = nil
		session.queue = session.queue[1:]
		if len(session.queue) == 0 {
			delete(o.terminalSessions, result.sessionID)
			return
		}
		o.queueTerminalReadyLocked(result.sessionID, session)
		return
	}
	// 未发完：若这次失败携带明确的未来重试时刻，转 retryWaiting 并按时刻进堆。
	if result.retryAt.After(o.now()) {
		session.retryWaiting = true
		heap.Push(&o.terminalRetries, terminalRetry{sessionID: result.sessionID, retryAt: result.retryAt})
		return
	}
	o.queueTerminalReadyLocked(result.sessionID, session)
}

// stopTerminalScheduler 停机收尾：关掉派活通道、等 worker 排空，然后清空队列状态并对
// 每条未发出的回复做资源清理（释放聊天调度引用）。
func (o *Outbound) stopTerminalScheduler() {
	o.terminalMu.Lock()
	o.terminalStopped = true
	close(o.terminalWork)
	o.terminalMu.Unlock()
	o.terminalWorkerWG.Wait()

	o.terminalMu.Lock()
	replies := make([]*terminalReply, 0)
	for _, session := range o.terminalSessions {
		replies = append(replies, session.queue...)
	}
	o.terminalSessions = make(map[string]*terminalSession)
	o.terminalReady = nil
	o.terminalRetries = nil
	o.terminalInFlight = 0
	o.queuedTerminalReplyCount = 0
	o.queuedTerminalReplyBytes = 0
	o.terminalMu.Unlock()
	for _, reply := range replies {
		o.cleanupTerminalReply(reply)
	}
}

// sendTerminalReplies 是固定数量的投递 worker：从 terminalWork 取活、做一次 Telegram
// 请求、把结果投回 terminalResults。请求 done 时顺手做本回复的资源清理。
func (o *Outbound) sendTerminalReplies(ctx context.Context) {
	defer o.terminalWorkerWG.Done()
	for work := range o.terminalWork {
		request := o.sendNextTerminalRequest(ctx, work.reply)
		if request.done {
			o.cleanupTerminalReply(work.reply)
		}
		result := terminalResult{terminalWork: work, done: request.done, retryAt: request.retryAt, err: request.err}
		select {
		case o.terminalResults <- result:
		case <-ctx.Done():
			return
		}
	}
}

type terminalRequestResult struct {
	done    bool
	retryAt time.Time
	err     error
}

// sendNextTerminalRequest 每次最多做一次 Telegram API 请求。节流/retry_after 以"未来时刻"
// 的形式交回给键控调度器（由它安排何时再试），这样固定数量的 worker 不会被某一条慢会话
// 占死，能腾出来服务其它会话。
func (o *Outbound) sendNextTerminalRequest(ctx context.Context, reply *terminalReply) terminalRequestResult {
	if !reply.initialized {
		// 首次轮到这条回复：做目标解析 + 分片等一次性初始化。
		target, err := o.resolveTarget(ctx, reply.event, false)
		if err != nil {
			return terminalRequestResult{done: true, err: err}
		}
		if target == nil {
			return terminalRequestResult{done: true}
		}

		o.mu.Lock()
		st := o.streams[target.streamKey]
		var schedule *chatSchedule
		if st != nil {
			schedule = st.schedule
		} else {
			schedule = o.retainChatLocked(target.botKey, target.chatID)
			if schedule == nil {
				// 调度表满了拿不到槽位：稍后重试，而不是绕过限速状态硬发。
				o.mu.Unlock()
				return terminalRequestResult{retryAt: o.now().Add(chatCapacityRetry)}
			}
		}
		// 流式状态已经结束它的使命（EventChatDone 到了）；交给终态投递后删掉流条目。
		delete(o.streams, target.streamKey)
		o.mu.Unlock()

		reply.initialized = true
		reply.target = target
		reply.schedule = schedule
		reply.chunks = chunkMessage(chatDoneContent(reply.event.Payload), maxMessageUnits)
		if st != nil {
			reply.streamedMessageID = st.messageID
		}
		if len(reply.chunks) == 0 {
			return terminalRequestResult{done: true}
		}
		return terminalRequestResult{retryAt: o.now()}
	}

	// 已初始化：在调度锁内做真正的一次 API 调用（每秒额度/退避此刻才生效）。
	schedule := reply.schedule
	schedule.mu.Lock()
	defer schedule.mu.Unlock()
	now := o.now()
	available := o.terminalAvailableAt(schedule, reply.target.botKey, now)
	if available.After(now) {
		return terminalRequestResult{retryAt: available}
	}
	api := newBotAPI(o.apiBase, reply.target.botToken, o.client)

	// 路径 A：有流式占位消息且还没 edit 过 → 先 edit 占位消息成第一片（用户看到的
	// 还是同一条消息，体验连续）。edit 失败（非"未变化"）就放弃占位消息、改走路径 B
	// 直接发新的。
	if reply.streamedMessageID != 0 && !reply.placeholderEdited && !reply.fallbackFreshSend {
		err := api.EditMessageText(ctx, editMessageTextParams{
			ChatID: reply.target.chatID, MessageID: reply.streamedMessageID,
			Text: formatHTML(reply.chunks[0]), ParseMode: "HTML",
		})
		if retry, ok := retryAfter(err); ok {
			retryAt := o.now().Add(retry)
			schedule.setBackoffTill(retryAt)
			return terminalRequestResult{retryAt: retryAt}
		}
		if err != nil && !isNotModified(err) {
			reply.fallbackFreshSend = true
			reply.chunkIndex = 0
			return terminalRequestResult{retryAt: o.now()}
		}
		reply.placeholderEdited = true
		reply.chunkIndex = 1
		schedule.lastEdit = o.now()
		schedule.setBackoffTill(time.Time{})
		if reply.chunkIndex == len(reply.chunks) {
			return terminalRequestResult{done: true}
		}
		return terminalRequestResult{retryAt: schedule.lastEdit.Add(editInterval)}
	}

	// 路径 B：直接发一条新消息（每片一条）。第一片才引用原始消息。
	chunk := reply.chunks[reply.chunkIndex]
	params := sendMessageParams{
		ChatID: reply.target.chatID, Text: formatHTML(chunk), ParseMode: "HTML",
		MessageThreadID: reply.target.threadID,
	}
	if reply.chunkIndex == 0 {
		params.ReplyParameters = optionalReplyParameters(reply.target.replyTo)
	}
	if reply.plainTextFallback {
		params.Text = chunk
		params.ParseMode = ""
	}
	_, err := api.SendMessage(ctx, params)
	if retry, ok := retryAfter(err); ok {
		retryAt := o.now().Add(retry)
		schedule.setBackoffTill(retryAt)
		return terminalRequestResult{retryAt: retryAt}
	}
	// HTML 解析类错误：降级成纯文本在同一下标重发一次。
	if err != nil && !reply.plainTextFallback && isHTMLParseError(err) {
		reply.plainTextFallback = true
		return terminalRequestResult{retryAt: o.now()}
	}
	if err != nil {
		return terminalRequestResult{done: true, err: fmt.Errorf("send final chunk: %w", err)}
	}
	reply.chunkIndex++
	reply.plainTextFallback = false
	schedule.lastEdit = o.now()
	schedule.setBackoffTill(time.Time{})
	if reply.chunkIndex == len(reply.chunks) {
		return terminalRequestResult{done: true}
	}
	// 多片之间同样遵守 editInterval 节奏（Telegram 对高频消息的全局预算按 20/分钟计）。
	return terminalRequestResult{retryAt: schedule.lastEdit.Add(editInterval)}
}

// cleanupTerminalReply 释放一条终态回复占用的资源：发完的释放聊天调度引用；没来得及初始
// 化就被丢弃的（队列超限/停机）走 clearStream 清掉可能的流条目。cleanupOnce 保证只清一次。
func (o *Outbound) cleanupTerminalReply(reply *terminalReply) {
	reply.cleanupOnce.Do(func() {
		if reply.initialized && reply.schedule != nil {
			o.releaseChat(reply.schedule, reply.target.chatID)
			return
		}
		o.clearStream(reply.event)
	})
}

// handleTaskFailed 清掉流状态并发一条失败提示。注意任务失败后可能还要自动重试
// （retry_pending 标志），那种情况只清流、不发失败消息——消息还在重试中。
func (o *Outbound) handleTaskFailed(e events.Event) {
	if taskFailureRetryPending(e.Payload) {
		o.clearStream(e)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	target, err := o.resolveTarget(ctx, e, false)
	if err != nil || target == nil {
		return
	}
	o.mu.Lock()
	st := o.streams[target.streamKey]
	delete(o.streams, target.streamKey)
	var schedule *chatSchedule
	if st != nil {
		schedule = st.schedule
	} else {
		schedule = o.retainChatLocked(target.botKey, target.chatID)
	}
	o.mu.Unlock()
	if schedule == nil {
		o.logger.WarnContext(ctx, "telegram outbound: failure notice skipped; chat scheduler is saturated")
		return
	}
	defer o.releaseChat(schedule, target.chatID)
	schedule.mu.Lock()
	defer schedule.mu.Unlock()

	api := newBotAPI(o.apiBase, target.botToken, o.client)
	// 有流式占位消息就优先把它改写成失败提示（保持同一条消息），失败再退回发一条新的。
	if st != nil && st.messageID != 0 {
		if err := o.runScheduled(ctx, schedule, func() error {
			return api.EditMessageText(ctx, editMessageTextParams{
				ChatID: st.chatID, MessageID: st.messageID, Text: taskFailedText,
			})
		}); err == nil || isNotModified(err) {
			return
		}
	}
	if err := o.runScheduled(ctx, schedule, func() error {
		_, err := api.SendMessage(ctx, sendMessageParams{
			ChatID: target.chatID, Text: taskFailedText, MessageThreadID: target.threadID,
			ReplyParameters: optionalReplyParameters(target.replyTo),
		})
		return err
	}); err != nil {
		o.logger.WarnContext(ctx, "telegram outbound: failure notice failed", "error", err)
	}
}

// handleTaskCancelled 只丢本地流状态，不改动用户已经看到的半截 Telegram 消息。取消没有
// 最终 agent 答复；保留那半截原文总比拿一条机器合成的提示覆盖用户可见内容强，同时确保
// 本地不留任何任务状态。
func (o *Outbound) handleTaskCancelled(e events.Event) {
	o.clearStream(e)
}

// taskFailureRetryPending 判断 TaskFailed payload 是否带 retry_pending 标志（为 true 表示
// agent 运行还会自动重试，此时不打扰用户）。
func taskFailureRetryPending(payload any) bool {
	fields, ok := payload.(map[string]any)
	if !ok {
		return false
	}
	retryPending, _ := fields["retry_pending"].(bool)
	return retryPending
}

// clearStream 按事件的 task id 清掉对应流条目并释放其聊天调度引用（幂等，无流则空操作）。
func (o *Outbound) clearStream(e events.Event) {
	taskID, ok := eventTaskID(e)
	if !ok {
		return
	}
	key := util.UUIDToString(taskID)
	o.mu.Lock()
	st := o.streams[key]
	delete(o.streams, key)
	if st != nil {
		o.releaseChatLocked(st.schedule, st.chatID)
	}
	o.mu.Unlock()
}

// retainChatLocked 取得（或新建）一条聊天的速率调度并占用（refs++）。必须先清理
// 空闲过期条目，否则长期运行的进程会让表无限膨胀。调用方须持有 Outbound.mu。
func (o *Outbound) retainChatLocked(botKey string, chatID int64) *chatSchedule {
	now := o.now()
	o.pruneIdleChatsLocked(now)
	key := chatScheduleKey{botKey: botKey, chatID: chatID}
	schedule := o.chats[key]
	// 已存在但空闲超过 TTL、又没在退避的记录要推倒重建（它的限速状态已失效）。
	if schedule != nil && schedule.refs == 0 && !schedule.idleSince.IsZero() &&
		now.Sub(schedule.idleSince) >= chatScheduleIdleTTL && !schedule.hasActiveBackoff(now) {
		delete(o.chats, key)
		schedule = nil
	}
	if schedule == nil {
		if !o.makeChatScheduleRoomLocked(now) {
			return nil
		}
		schedule = &chatSchedule{key: key}
		o.chats[key] = schedule
	}
	schedule.refs++
	schedule.idleSince = time.Time{}
	return schedule
}

func (o *Outbound) releaseChat(schedule *chatSchedule, chatID int64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.releaseChatLocked(schedule, chatID)
}

// releaseChatLocked 释放一次占用（refs--）。归零时记下 idle 起始时刻并顺手清理
// （可能有其他条目刚到期）。调用方须持有 Outbound.mu。
func (o *Outbound) releaseChatLocked(schedule *chatSchedule, chatID int64) {
	if schedule == nil {
		return
	}
	schedule.refs--
	if schedule.refs == 0 && o.chats[schedule.key] == schedule {
		schedule.idleSince = o.now()
		o.pruneIdleChatsLocked(schedule.idleSince)
	}
}

// pruneIdleChatsLocked 把"空闲超过 TTL 且没有活跃退避"的调度条目从表里清掉。
// 调用方须持有 Outbound.mu。
func (o *Outbound) pruneIdleChatsLocked(now time.Time) {
	o.pruneBotFallbacksLocked(now)
	for key, schedule := range o.chats {
		if schedule.refs != 0 || schedule.idleSince.IsZero() {
			continue
		}
		if now.Sub(schedule.idleSince) >= chatScheduleIdleTTL && !schedule.hasActiveBackoff(now) {
			delete(o.chats, key)
		}
	}
}

// makeChatScheduleRoomLocked 对整个调度表强加硬上限（maxChatSchedules）。满员时先逐出
// 最久空闲的"干净"条目；若空闲候选全带着活跃 retry_after，则把其中最早的截止时间保守
// 合并进该安装（bot）的降级表后再逐出。整张表都在使用中则拒绝新条目——终态任务稍后
// 重试，也不允许绕过限速状态硬插。调用方须持有 Outbound.mu。
func (o *Outbound) makeChatScheduleRoomLocked(now time.Time) bool {
	if len(o.chats) < maxChatSchedules {
		return true
	}
	// 第一轮：找最久空闲且此刻无退避的条目直接逐出。
	var inactiveKey chatScheduleKey
	var inactive *chatSchedule
	for key, schedule := range o.chats {
		if schedule.refs != 0 || schedule.idleSince.IsZero() ||
			now.Sub(schedule.idleSince) < editInterval || schedule.hasActiveBackoff(now) {
			continue
		}
		if inactive == nil || schedule.idleSince.Before(inactive.idleSince) {
			inactiveKey, inactive = key, schedule
		}
	}
	if inactive != nil {
		delete(o.chats, inactiveKey)
		return true
	}

	// 第二轮：没有干净可逐的，找"带退避但允许合并进 bot 降级表"的最老空闲条目。
	var activeKey chatScheduleKey
	var active *chatSchedule
	for key, schedule := range o.chats {
		if schedule.refs != 0 || schedule.idleSince.IsZero() ||
			now.Sub(schedule.idleSince) < editInterval || !schedule.hasActiveBackoff(now) {
			continue
		}
		if !o.canMergeBotFallbackLocked(schedule.key.botKey, now) {
			continue
		}
		if active == nil || schedule.idleSince.Before(active.idleSince) {
			activeKey, active = key, schedule
		}
	}
	if active == nil {
		return false
	}
	o.mergeBotFallbackLocked(active.key.botKey, active.backoffSnapshot(), now)
	delete(o.chats, activeKey)
	return true
}

// canMergeBotFallbackLocked 判断某个 bot 还能不能接收一条合并进来的退避记录
// （bot 降级表同样有硬上限）。调用方须持有 Outbound.mu。
func (o *Outbound) canMergeBotFallbackLocked(botKey string, now time.Time) bool {
	o.pruneBotFallbacksLocked(now)
	_, exists := o.botFallbackBackoff[botKey]
	return exists || len(o.botFallbackBackoff) < maxBotFallbacks
}

// mergeBotFallbackLocked 把一条调度条目的退避截止时刻并入 bot 级降级表（取更晚者），
// 之后该 bot 的新聊天也会避开这个窗口。调用方须持有 Outbound.mu。
func (o *Outbound) mergeBotFallbackLocked(botKey string, until, now time.Time) {
	if !until.After(now) {
		return
	}
	if current := o.botFallbackBackoff[botKey]; until.After(current) {
		o.botFallbackBackoff[botKey] = until
	}
}

// pruneBotFallbacksLocked 清掉已过期的 bot 级退避记录。调用方须持有 Outbound.mu。
func (o *Outbound) pruneBotFallbacksLocked(now time.Time) {
	for botKey, until := range o.botFallbackBackoff {
		if !until.After(now) {
			delete(o.botFallbackBackoff, botKey)
		}
	}
}

// botFallbackTill 返回该 bot 目前被压到的退避截止时刻（已过期为零值）。加锁读。
func (o *Outbound) botFallbackTill(botKey string, now time.Time) time.Time {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.pruneBotFallbacksLocked(now)
	return o.botFallbackBackoff[botKey]
}

// terminalAvailableAt 算出这条聊天"下一次允许发请求"的时刻：取三者最晚——聊天自己的
// retry_after 退避、editInterval 冷却、以及 bot 级合并退避。
func (o *Outbound) terminalAvailableAt(schedule *chatSchedule, botKey string, now time.Time) time.Time {
	available := schedule.backoffTill
	if editAvailable := schedule.lastEdit.Add(editInterval); editAvailable.After(available) {
		available = editAvailable
	}
	if fallback := o.botFallbackTill(botKey, now); fallback.After(available) {
		available = fallback
	}
	return available
}

// setBackoffTill 更新退避时刻，调用方须已持有 schedule.mu。backoffUnix 这个原子字段给
// 缓存清理提供"无锁快照"——保留全局 Outbound.mu → schedule.mu 的加锁顺序，不制造
// 投递路径（schedule.mu → Outbound.mu）的反向锁序，避免死锁。
func (s *chatSchedule) setBackoffTill(t time.Time) {
	s.backoffTill = t
	if t.IsZero() {
		s.backoffUnix.Store(0)
		return
	}
	s.backoffUnix.Store(t.UnixNano())
}

// hasActiveBackoff 无锁判断此刻是否仍在退避窗口内。
func (s *chatSchedule) hasActiveBackoff(now time.Time) bool {
	return s.backoffUnix.Load() > now.UnixNano()
}

// backoffSnapshot 无锁读回退避时刻（供逐出前合并到 bot 降级表）。
func (s *chatSchedule) backoffSnapshot() time.Time {
	nanos := s.backoffUnix.Load()
	if nanos == 0 {
		return time.Time{}
	}
	return time.Unix(0, nanos)
}

// runScheduled 等待该聊天的 edit 冷却与任何 Telegram 429 retry_after 窗口过去后执行
// operation。429 会一直重试到事件上下文过期；其它错误一律不重试（可能已产生副作用，
// 重试会重复）。
func (o *Outbound) runScheduled(ctx context.Context, schedule *chatSchedule, operation func() error) error {
	for {
		now := o.now()
		available := o.terminalAvailableAt(schedule, schedule.key.botKey, now)
		if delay := available.Sub(now); delay > 0 {
			if err := o.wait(ctx, delay); err != nil {
				return err
			}
		}
		err := operation()
		if retry, ok := retryAfter(err); ok {
			schedule.setBackoffTill(o.now().Add(retry))
			continue
		}
		if err == nil {
			schedule.lastEdit = o.now()
			schedule.setBackoffTill(time.Time{})
		}
		return err
	}
}

// waitForOutbound 把 sleepCtx 包装成可注入的 wait 函数（测试时可换假时间）。
func waitForOutbound(ctx context.Context, delay time.Duration) error {
	if sleepCtx(ctx, delay) {
		return nil
	}
	return ctx.Err()
}

// replyTarget 是一条事件解析后的投递目的地（聊天的数字化坐标 + bot token）。
type replyTarget struct {
	streamKey string // task_id 字符串形式，用作 streams 表键
	botKey    string // installation id 字符串形式，用作聊天调度/降级表键
	chatID    int64
	threadID  int64
	replyTo   int64
	botToken  string
}

// resolveTarget 把事件携带的"任务投递快照"映射成 Telegram 凭据与目标坐标。找不到投递
// 快照意味着该任务来自 Web/Desktop/Mobile——即使它的 Chat 曾有过 Telegram 路由，也绝
// 不能发进外部会话（那是别的渠道的记录）。
func (o *Outbound) resolveTarget(ctx context.Context, e events.Event, _ bool) (*replyTarget, error) {
	taskID, hasTaskID := eventTaskID(e)
	if !hasTaskID {
		return nil, nil
	}
	delivery, err := o.q.GetChannelTaskDelivery(ctx, taskID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("lookup telegram task delivery: %w", err)
	}
	if delivery.ChannelType != string(TypeTelegram) {
		return nil, nil
	}
	binding := telegramBindingFromTaskDelivery(delivery)
	inst, err := o.q.GetChannelInstallation(ctx, db.GetChannelInstallationParams{
		ID:          binding.InstallationID,
		ChannelType: string(TypeTelegram),
	})
	if err != nil {
		return nil, fmt.Errorf("load telegram installation: %w", err)
	}
	if inst.Status != "active" {
		return nil, nil // 触发到回复之间安装已被吊销
	}
	creds, err := decodeCredentials(inst.Config, o.decrypt)
	if err != nil {
		return nil, fmt.Errorf("decode telegram credentials: %w", err)
	}
	chatID, threadID, replyTo := outboundTarget(binding)
	return &replyTarget{
		streamKey: util.UUIDToString(taskID),
		botKey:    util.UUIDToString(inst.ID),
		chatID:    chatID,
		threadID:  threadID,
		replyTo:   replyTo,
		botToken:  creds.BotToken,
	}, nil
}

// telegramBindingFromTaskDelivery 把投递快照行重构成绑定结构（快照是绑定在触发时刻的
// 副本，投递用副本而不是现场查绑定，保证回复送回用户当时所在的位置）。
func telegramBindingFromTaskDelivery(delivery db.ChannelTaskDelivery) db.ChannelChatSessionBinding {
	return db.ChannelChatSessionBinding{
		ID: delivery.BindingID, InstallationID: delivery.InstallationID,
		ChannelType: delivery.ChannelType, ChannelChatID: delivery.ChannelChatID,
		ChatType: delivery.ChatType, LastMessageID: delivery.ChannelMessageID,
		LastThreadID: delivery.ChannelThreadID, RouteRevision: delivery.RouteRevision,
		Config: delivery.Config,
	}
}

// outboundTarget 还原数值聊天 id（当绑定键是复合键 "chat:thread" 时真正的 chat id 藏在
// 绑定 config 里）以及回复要用的线程/引用消息 id。
func outboundTarget(b db.ChannelChatSessionBinding) (chatID, threadID, replyTo int64) {
	raw := b.ChannelChatID
	if len(b.Config) > 0 {
		var cfg telegramBindingConfig
		if err := json.Unmarshal(b.Config, &cfg); err == nil && cfg.ChatID != "" {
			raw = cfg.ChatID
		}
	}
	chatID, _ = strconv.ParseInt(raw, 10, 64)
	if b.LastThreadID.Valid {
		threadID, _ = strconv.ParseInt(b.LastThreadID.String, 10, 64)
	}
	if b.LastMessageID.Valid {
		replyTo = parseMessageRef(b.LastMessageID.String)
	}
	return chatID, threadID, replyTo
}

// optionalReplyParameters 把"可能是 0（无引用）"的消息 id 变成可选的 replyParameters。
func optionalReplyParameters(messageID int64) *replyParameters {
	if messageID == 0 {
		return nil
	}
	return &replyParameters{MessageID: messageID, AllowSendingWithoutReply: true}
}

// eventTaskID 从事件信封或 payload 里抽出 task id（不同事件类型放的位置不一样）。
func eventTaskID(e events.Event) (pgtype.UUID, bool) {
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

// chatDoneContent 从 EventChatDone 的 payload 里抽出回复文本。
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

// isNotModified 识别 Telegram 的 "message is not modified" edit 错误——无害（两次内容
// 完全相同），不应按失败处理。
func isNotModified(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.Code == http.StatusBadRequest &&
		containsFold(ae.Description, "message is not modified")
}

// firstNonEmpty 取第一个非空串；都空返回空。
func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
