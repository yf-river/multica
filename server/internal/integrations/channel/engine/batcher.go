package engine

import (
	"sync"
	"time"
)

// 角色定位：入站"去抖"器——把"同一条 chat_session 的同一上下文代"内的多次运行触发合并
// 成一次 agent 运行。Router 每收入一条消息就调 Schedule 重置该代的静默窗口；等消息安静
// 下来，最新那次 flush 恰好执行一次。这样把代内爆发性连发压成一次运行，同时 /clear 作为
// 硬性批次边界始终生效。只对"运行触发"去抖；chat_message 落库、去重、帧 ACK 都已在上游
// 同步完成，与本文件无关。
//
// 状态是本进程内存的、以 chat_session_id + context 代数为键。WS 租约保证每个安装同时
// 只有一个活跃属主，所以一条会话只会被一个进程去抖。窗口内硬崩溃会丢掉待触发的运行
//（消息已持久化，只是要到下一条消息才再触发运行）。调用方把持久化的 context 代数放进
// 键里，保证 /clear 的边界永远不会顶掉**上一代**上下文的待触发 flush。优雅停机调
// FlushAll，正常重启就不会撞上"丢弃待触发"那个边界。goroutine 安全；全 supervisor 共享
// 一个实例。

// DefaultChatRunBatchWindow 是入站去抖器在触发一次 agent 运行前等待的静默窗口。
// 3 秒（MUL-2968）权衡：足够把"先转发一份记录、再补一句说明"这样的连发吸收成一次运行，
// 又短到 bot 首条回复不会让人感到明显延迟。
const DefaultChatRunBatchWindow = 3 * time.Second

// stoppableTimer 是 batcher 依赖的 *time.Timer 切片，钉成接口好让测试注入手工触发的
// 假定时器。*time.Timer 天然满足它。
type stoppableTimer interface {
	Stop() bool
}

// pendingBatcher 对"按 (chat_session, context generation) 分键"的运行触发做去抖。
// 每个入站消息调用一次 Schedule，它（重）置一个该代的定时器；安静到点后，最新一次
// flush 恰好执行一次。
//
// 内部用 seq 每 (re)schedule 一次发一个单调递增的代号。onFire 回调带着它被武装时的代号，
// 若已有更新的 schedule 顶替就退出——这圈住了 time.AfterFunc 的竞态：定时器触发可能与
// 想取消它的 Stop() 并发发生。
type pendingBatcher struct {
	window time.Duration

	// afterFunc 构造一个 d 后执行 fn 的定时器。生产用 time.AfterFunc；测试注入假实现
	// 以获得确定性的 flush 时序。
	afterFunc func(d time.Duration, fn func()) stoppableTimer

	mu      sync.Mutex
	pending map[string]*pendingEntry
	// seq 每次（重新）schedule 发一个单调代号，见上面类型注释。
	seq      uint64
	stopped  bool
	inflight sync.WaitGroup
}

// pendingEntry 是某个键当前"已武装"的去抖条目。
type pendingEntry struct {
	timer stoppableTimer
	flush func()
	gen   uint64
}

// newPendingBatcher 返回给定静默窗口的去抖器。窗口 <= 0 时回落成
// DefaultChatRunBatchWindow。
func newPendingBatcher(window time.Duration) *pendingBatcher {
	if window <= 0 {
		window = DefaultChatRunBatchWindow
	}
	return &pendingBatcher{
		window:    window,
		afterFunc: realAfterFunc,
		pending:   make(map[string]*pendingEntry),
	}
}

func realAfterFunc(d time.Duration, fn func()) stoppableTimer {
	return time.AfterFunc(d, fn)
}

// Schedule （重）置 key 的静默窗口。最新一次的 flush 获胜：触发运行只需要会话级信息，
// 因此只保留最新的闭包（它捕获了最新的安装/消息上下文）就够了。FlushAll 之后再调
// Schedule 会改为**内联执行** flush 而不是丢弃（这是停机竞态：已经开始排空后仍可能有
// 消息到达）。
func (b *pendingBatcher) Schedule(key string, flush func()) {
	b.schedule(key, flush, true)
}

// ScheduleIfAbsent 只在"本进程还没有这个 key 的活窗口"时才武装它。崩溃恢复用它处理较老
// 的持久化上下文代：丢失的定时器必须补回来，但较新一代上的消息绝不能重置较老一代已在
// 运行的静默窗口、或顶替它的发送者元数据。
func (b *pendingBatcher) ScheduleIfAbsent(key string, flush func()) {
	b.schedule(key, flush, false)
}

// schedule 是两者的共同实现。replace=true 时重复 schedule 会替换闭包并重置窗口；
// false 时已存在的窗口原样保留。
func (b *pendingBatcher) schedule(key string, flush func(), replace bool) {
	b.mu.Lock()
	if b.stopped {
		b.mu.Unlock()
		flush()
		return
	}
	b.seq++
	gen := b.seq
	fire := func() { b.onFire(key, gen) }
	if e, ok := b.pending[key]; ok {
		if !replace {
			b.mu.Unlock()
			return
		}
		e.timer.Stop()
		e.flush = flush
		e.gen = gen
		e.timer = b.afterFunc(b.window, fire)
		b.mu.Unlock()
		return
	}
	b.pending[key] = &pendingEntry{
		flush: flush,
		gen:   gen,
		timer: b.afterFunc(b.window, fire),
	}
	b.mu.Unlock()
}

// onFire 在 key 仍是"活的、已武装"的那一代时执行 flush。它是定时器回调：生产环境跑在
// time.AfterFunc 的 goroutine 上，所以 flush 天然与入站路径解耦、不阻塞收消息。
func (b *pendingBatcher) onFire(key string, gen uint64) {
	b.mu.Lock()
	e, ok := b.pending[key]
	if !ok || b.stopped || e.gen != gen {
		b.mu.Unlock()
		return
	}
	delete(b.pending, key)
	flush := e.flush
	b.inflight.Add(1)
	b.mu.Unlock()

	defer b.inflight.Done()
	flush()
}

// FlushAll 停掉去抖器，把每个仍待触发的 flush 恰好执行一次，然后等并发触发的 flush 全部
// 结束。应在优雅停机的**入站投递已停止之后**调用一次。FlushAll 之后去抖器进入终态：
// 之后再有 Schedule 直接内联执行。
func (b *pendingBatcher) FlushAll() {
	b.mu.Lock()
	b.stopped = true
	entries := make([]*pendingEntry, 0, len(b.pending))
	for _, e := range b.pending {
		e.timer.Stop()
		entries = append(entries, e)
	}
	b.pending = make(map[string]*pendingEntry)
	b.mu.Unlock()

	for _, e := range entries {
		e.flush()
	}
	b.inflight.Wait()
}

// pendingCount 报告当前有多少会话持有已武装的窗口（测试断言用）。
func (b *pendingBatcher) pendingCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.pending)
}
