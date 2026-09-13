package daemon

import (
	"sync"
	"time"
)

// reconcileBroadcaster 用"关闭并替换 channel"模式把"立即对账"信号扇出给任意数量
// 的监听者。订阅者调用 notify() 获得一个快照 channel，它在下一次广播时被关闭；
// 醒来后订阅者经 notify() 再取一个新快照 channel，以接收下一个信号。
//
// 广播器存在的理由：某些 daemon 循环跑在粗粒度 ticker 上（任务取消轮询 5s、
// workspace 同步 30s）。当 daemon 的 WS 连接断开又重连时，服务器在间隙里改变的
// 任何东西对这些循环都不可见，直到它们下一个 tick 触发。broadcast() 让 WS 连接
// 路径能立即推一下每个等待者去重查，而不打乱 ticker 节奏。
//
// 广播器是边沿触发、带单槽回放：若 broadcast() 在没有订阅者时触发，下一次
// notify() 调用会返回一个已经关闭的 channel，让迟到的订阅者恰好观察到一次被错过
// 的事件。这封闭了 daemon 启动竞态：workspaceSyncLoop（或其它迟到订阅者）恰在
// WS 连接期间广播落地后停在 ticker 上，否则要等整整一个 ticker 周期才知道。
//
// broadcast() 会对 minBroadcastInterval 内紧邻的调用做去抖，让反复抖动的 WS
// 连接不会把一次网络抖动变成 GetTaskStatus / ListWorkspaces 请求的踩踏。间隔刻意
// 设得很短：一次真实重连仍会快速传导，但一秒内十次重连会折叠成一次立即扇出，
// 外加间隔过后至多一次的跟进。
type reconcileBroadcaster struct {
	mu                   sync.Mutex
	ch                   chan struct{}
	pending              bool
	lastBroadcast        time.Time
	minBroadcastInterval time.Duration
	// now 由测试注入，使去抖窗口可确定；生产用 time.Now。
	now func() time.Time
}

func newReconcileBroadcaster() *reconcileBroadcaster {
	return &reconcileBroadcaster{
		ch:                   make(chan struct{}),
		minBroadcastInterval: time.Second,
		now:                  time.Now,
	}
}

// notify 返回一个在下一次广播时关闭的 channel。订阅者醒来后应再次调用 notify()
// 以接收下一信号——返回的 channel 是一次性的。
//
// 若在没有订阅者时来了广播，下一次 notify() 返回的 channel 已经关闭。该回放标志
// 会被那次调用清除：第二个并发的迟到订阅者不会看到同一次被重放的事件。需要广播
// 投递给每个 goroutine 的调用方必须在广播发生前订阅。
func (b *reconcileBroadcaster) notify() <-chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pending {
		// 把错过的广播恰好重放一次：交还一个全新且已关闭的 channel，不打扰真实
		// 订阅者正用的 b.ch；并清除 pending，让下一次 notify() 恢复边沿触发行为。
		b.pending = false
		replay := make(chan struct{})
		close(replay)
		return replay
	}
	return b.ch
}

// broadcast 唤醒每个当前订阅者，然后为将来的订阅者安装新 channel。若没有当前
// 订阅者，就设置单槽回放标志，使下一次 notify() 观察到一次错过的广播。
//
// 与上一次广播相距 minBroadcastInterval 之内的调用被丢弃；函数报告信号是否真的
// 触发，让调用方可以为被抑制的广播打 debug 级跟踪。
func (b *reconcileBroadcaster) broadcast() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	if !b.lastBroadcast.IsZero() && now.Sub(b.lastBroadcast) < b.minBroadcastInterval {
		return false
	}
	b.lastBroadcast = now
	b.pending = true
	close(b.ch)
	b.ch = make(chan struct{})
	return true
}

// workspaceChangeSignal 是工作区集合变化的单消费者脏标志。单槽缓冲会保留"同步
// 循环启动前或 API 读在途时到达"的事件，同时把更大的突发合并成对最新服务器状态
// 的一次收尾对账。
type workspaceChangeSignal struct {
	ch chan struct{}
}

func newWorkspaceChangeSignal() *workspaceChangeSignal {
	return &workspaceChangeSignal{ch: make(chan struct{}, 1)}
}

// notify 返回用于等待下一次工作区变化广播的 channel（nil 接收者为空操作）。
func (s *workspaceChangeSignal) notify() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.ch
}

// broadcast 非阻塞地置位一次变化信号；channel 已满（信号未被消费）则返回 false。
func (s *workspaceChangeSignal) broadcast() bool {
	if s == nil {
		return false
	}
	select {
	case s.ch <- struct{}{}:
		return true
	default:
		return false
	}
}
