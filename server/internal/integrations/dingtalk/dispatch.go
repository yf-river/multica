// dispatch.go：入站消息的分发器——把「收帧」和「处理」解耦开。
//
// 角色：每条钉钉 Stream 连接（dingtalk_channel.go）都在 socket 读循环里收到
// 机器人消息回调后立刻入队本分发器，由它异步处理，让读循环可以马上回 ack、永不被
// 慢操作（比如下载图片可能耗时几十秒）卡住。
//
// 并发模型：按会话（conversation）串行——同一个会话的消息严格按到达顺序处理，
// 保证对话不乱序；不同会话之间并行，用每安装一把的信号量限住最大并发。重连/断连
// 由上层 supervisor 管，分发器只负责把已 ack 的任务跑完。
package dingtalk

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// 入站分发器的调参。远端媒体解析已被共享的 Router 拆出去另跑，所以这里超时只管
// 同步摄取这一段路径。
const (
	dispatchJobTimeout = 120 * time.Second
	maxDispatchWorkers = 8
	// maxDispatchQueueDepth 只是给单个会话的积压设的「内存安全」兜底。取值远高于
	// 任何真实的人类连发节奏，所以溢出丢弃在实际中几乎不可达——单个会话要积压数百
	// 轮没被处理才会撞上。万一真的溢出，丢弃的是最新消息并记 warn 日志；socket 读
	// 循环绝不能阻塞，所以这里不能用阻塞式背压。
	maxDispatchQueueDepth = 256
	// 光限单会话队列长度，仍允许每个会话各挂一个等待中的 drain goroutine。这里再给
	// 整个安装「已接收但未处理完」的消息总数设上限，让排队中的负载与等信号量的等待
	// 者都有确定的顶。
	maxDispatchPending = 2048
)

// dispatcher 把入站处理从 Stream 读循环里拆出来：帧立刻 ack，任务按会话在各自的
// 串行队列上跑，慢媒体下载既不会饿死 ping/system 帧，也不会打乱某段对话的时间
// 顺序。跨会话任务并行，受每安装一把全局信号量限制。单会话队列只是内存兜底
// （maxDispatchQueueDepth），设得足够高，真实人类连发永远够不到；引擎的去重让任何
// 重复投递都无害。
//
// 任务跑在 context.Background() 派生的独立上下文里、自带截止时间，刻意与 socket
// 的运行上下文脱钩：网关断线重连绝不能取消正在追加中的任务。
type dispatcher struct {
	handle func(ctx context.Context, job inboundJob)
	logger *slog.Logger
	sem    chan struct{}
	ctx    context.Context
	cancel context.CancelFunc

	mu         sync.Mutex
	queues     map[string][]inboundJob
	active     map[string]bool
	closed     bool
	pending    int
	maxPending int
	workers    sync.WaitGroup
	done       chan struct{}
}

// newDispatcher 构造分发器：handle 是每条任务的处理函数（钉钉侧为
// dingtalkChannel.runInbound），共享一把跨会话的信号量。
func newDispatcher(handle func(ctx context.Context, job inboundJob), logger *slog.Logger) *dispatcher {
	if logger == nil {
		logger = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &dispatcher{
		handle:     handle,
		logger:     logger,
		sem:        make(chan struct{}, maxDispatchWorkers),
		ctx:        ctx,
		cancel:     cancel,
		queues:     make(map[string][]inboundJob),
		active:     make(map[string]bool),
		maxPending: maxDispatchPending,
		done:       make(chan struct{}),
	}
}

// enqueue 把一条原始入站任务挂到它所属会话的队列尾，并在该会话还没有 drain
// goroutine 时启动一个。绝不阻塞调用方。
func (d *dispatcher) enqueue(convID string, job inboundJob) {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		d.logger.Debug("dingtalk dispatch: dispatcher closed, dropping message",
			"conversation_id", convID, "msg_id", job.callback.MsgId)
		return
	}
	if d.pending >= d.maxPending {
		d.mu.Unlock()
		d.logger.Warn("dingtalk dispatch: installation queue full, dropping message",
			"conversation_id", convID, "msg_id", job.callback.MsgId)
		return
	}
	if len(d.queues[convID]) >= maxDispatchQueueDepth {
		d.mu.Unlock()
		d.logger.Warn("dingtalk dispatch: conversation queue full, dropping message",
			"conversation_id", convID, "msg_id", job.callback.MsgId)
		return
	}
	d.queues[convID] = append(d.queues[convID], job)
	d.pending++
	start := !d.active[convID]
	if start {
		d.active[convID] = true
		d.workers.Add(1)
	}
	d.mu.Unlock()
	if start {
		go d.drain(convID)
	}
}

// drain 严格按顺序跑完一个会话的任务，队列清空就退出（之后再来消息会新起一个
// drain）。信号量给所有会话的在跑任务设了并行上限；drain 在信号量上等待，正好
// 保住本会话「不插队」的顺序性。
func (d *dispatcher) drain(convID string) {
	defer d.workers.Done()
	for {
		d.mu.Lock()
		if d.ctx.Err() != nil {
			d.pending -= len(d.queues[convID])
			delete(d.queues, convID)
			delete(d.active, convID)
			d.mu.Unlock()
			return
		}
		q := d.queues[convID]
		if len(q) == 0 {
			delete(d.queues, convID)
			delete(d.active, convID)
			d.mu.Unlock()
			return
		}
		job := q[0]
		d.queues[convID] = q[1:]
		d.mu.Unlock()

		select {
		case d.sem <- struct{}{}:
		case <-d.ctx.Done():
			d.mu.Lock()
			d.pending -= 1 + len(d.queues[convID])
			delete(d.queues, convID)
			delete(d.active, convID)
			d.mu.Unlock()
			return
		}
		ctx, cancel := context.WithTimeout(d.ctx, dispatchJobTimeout)
		d.handle(ctx, job)
		cancel()
		<-d.sem
		d.mu.Lock()
		d.pending--
		d.mu.Unlock()
	}
}

// drainAndClose 停止接收新任务，并等待已 ack 的消息跑完。ctx 过期则取消所有
// 在跑任务，goroutine 汇合留在后台异步完成——绝不把调用方拖过它的关停预算。
// 幂等。
func (d *dispatcher) drainAndClose(ctx context.Context) bool {
	d.startClose()
	return d.waitClosed(ctx)
}

// startClose 原子地停掉新入队，并启动后台异步的 worker 汇合。把这一步从
// waitClosed 拆出来，是为了让持有方（dispatchSlot）能在重连决定队列可否复用之前，
// 先把「正在关闭」的状态发布出去。
func (d *dispatcher) startClose() {
	d.mu.Lock()
	if !d.closed {
		d.closed = true
		go func() {
			d.workers.Wait()
			d.cancel()
			close(d.done)
		}()
	}
	d.mu.Unlock()
}

// waitClosed 等待 worker 全部汇合：正常等到 done 关闭返回 true；ctx 先到期就
// 强取消所有在跑任务，再给一次短等，汇合上了返回 true、否则 false（意味着队列
// 仍有人在跑，只能靠后台 goroutine 收尾）。
func (d *dispatcher) waitClosed(ctx context.Context) bool {
	select {
	case <-d.done:
		return true
	case <-ctx.Done():
		d.cancel()
		select {
		case <-d.done:
			return true
		default:
			return false
		}
	}
}

// isClosed 报告分发器是否已进入关闭态（不再接受新入队）。
func (d *dispatcher) isClosed() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.closed
}
