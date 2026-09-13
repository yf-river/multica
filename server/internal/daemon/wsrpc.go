package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// errWSRPCUnavailable 在 wsRPCClient.Call 发现没有可用的 WS 连接来承载请求时返回。
// 调用方把它当作“回退到 HTTP”的信号。
var errWSRPCUnavailable = errors.New("ws rpc: no active connection")

// errWSRPCUncertain 在“请求帧确实已发出，但连接在拿到确定答复前断开”时返回。此时
// 结果未知（服务器可能已经落库），调用方绝不能为同一份工作再回退到别的传输通道——
// 那会造成重复认领（MUL-4257）。
var errWSRPCUncertain = errors.New("ws rpc: sent but outcome unknown (connection lost)")

// wsRPCResponseGrace 是 daemon 在服务端自己申请的执行预算之外再多等的响应时间：
// 这样“刚好在服务器截止前完成提交”的 claim 也能在 daemon 放弃前报回来（MUL-4257）。
const wsRPCResponseGrace = 2 * time.Second

var wsClaimUncertainFallbackDelay = batchClaimRequestTimeout + wsRPCResponseGrace

// errWSRPCWriteBufferFull 在连接写缓冲区写满时返回；调用方此时回退 HTTP 而不是把
// socket 卡死。
var errWSRPCWriteBufferFull = errors.New("ws rpc: write buffer full")

// wsRPCClient 是通用 WS 请求/响应通道（MUL-4257）的 daemon 侧实现。它在共享、复用的
// WS 控制连接上按 request_id 把响应与请求关联起来，因此允许多个 RPC 同时在途。发送
// 委托给注入的 sendFrame 函数（把帧推进活动连接的写通道）；当没有连接挂上来时 Call
// 会以 errWSRPCUnavailable 快速失败，调用方改走 HTTP。
// wsOutbound 是排队等着 WS writer 发送的一帧。它可被取消：当某个 RPC 调用方在帧真正
// 落到 socket 之前放弃（超时/连接脱离）时，可以阻止它稍后被送出——否则一个被背压的
// writer 可能在 daemon 已经 HTTP 回退之后才把过期的 tasks.claim 发出去，造成重复认领
// （MUL-4257，Sol-Boy review）。sent/cancel 在 mu 之下竞争，保证决策原子：谁先到就决定
// 了这帧到底送不送。
type wsOutbound struct {
	data     []byte
	mu       sync.Mutex
	sent     bool
	canceled bool
}

// beginWrite 由 writer 在 WriteMessage 之前调用。若这帧已被取消则返回 false（跳过
// 不送）；否则把帧标记为 sent，使并发的 cancel() 无法再“收回”它。
func (o *wsOutbound) beginWrite() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.canceled {
		return false
	}
	o.sent = true
	return true
}

// cancel 由正在放弃的 RPC 调用方调用。若帧仍在排队则返回 true（现在已取消——writer
// 会跳过它，保证绝不送达）；若 writer 已经开始发它，返回 false。
func (o *wsOutbound) cancel() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.sent {
		return false
	}
	o.canceled = true
	return true
}

type wsRPCClient struct {
	mu        sync.Mutex
	pending   map[string]chan protocol.RPCResponsePayload
	sendFrame func([]byte) (*wsOutbound, error)
	// rpcV1Supported 属于“当前挂着的这条连接”。attach 在换上新 sender 之前会先清掉
	// 它，因此“claim 与重连竞态”时协商状态绝不会被带到下一条连接上。
	rpcV1Supported bool
	generation     uint64
	// grace 叠加到一次调用的服务端超时预算上，得到 daemon 实际等响应的时长：这样
	// 刚好在服务器截止前完成提交的 claim 也能在 daemon 放弃前报回来（MUL-4257）。
	grace time.Duration
}

func newWSRPCClient(grace time.Duration) *wsRPCClient {
	return &wsRPCClient{
		pending: make(map[string]chan protocol.RPCResponsePayload),
		grace:   grace,
	}
}

// attach 把一条存活连接的帧 writer 绑定进来，并清掉上一条连接协商出的能力位。
// 传 nil 表示脱离（断开时），此后 Call 会快速失败直到下一次 attach 且 rpc-v1
// heartbeat ack 确认。所有仍挂起的请求都会被失败处理，让调用方立刻回退 HTTP。
func (c *wsRPCClient) attach(sendFrame func([]byte) (*wsOutbound, error)) uint64 {
	c.mu.Lock()
	c.generation++
	c.rpcV1Supported = false
	c.sendFrame = sendFrame
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	generation := c.generation
	c.mu.Unlock()
	return generation
}

// markRPCV1Supported 记录当前挂着的连接明确支持 rpc-v1。没有活 sender 时收到的
// heartbeat ack 不能启用未来某条连接。
func (c *wsRPCClient) markRPCV1Supported(generation uint64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.sendFrame != nil && c.generation == generation {
		c.rpcV1Supported = true
	}
	c.mu.Unlock()
}

func (c *wsRPCClient) currentGeneration() uint64 {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.generation
}

// supportsRPCV1 报告当前连接是否明确协商过 rpc-v1。Call 会在同一把锁下重查这个
// 条件并捕获 sender，因此本方法只是快速路径提示，本身不能单独授权一次发送。
func (c *wsRPCClient) supportsRPCV1() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sendFrame != nil && c.rpcV1Supported
}

// Call 在任意已附着的连接上发起一次 RPC。传输层测试与自带协商契约的调用方直接用
// 这个。
func (c *wsRPCClient) Call(ctx context.Context, method string, serverTimeout time.Duration, reqBody, respBody any) (int, error) {
	return c.call(ctx, method, serverTimeout, reqBody, respBody, false)
}

// CallIfRPCV1Supported 只在当前附着连接明确协商过 rpc-v1 时才发起 RPC。能力位检查
// 与 sender 捕获在同一把锁下完成，因此重连不会把“上一条连接授权过的调用”改道到
// 替换它的新连接上。
func (c *wsRPCClient) CallIfRPCV1Supported(ctx context.Context, method string, serverTimeout time.Duration, reqBody, respBody any) (int, error) {
	return c.call(ctx, method, serverTimeout, reqBody, respBody, true)
}

// call 一直阻塞到收到响应、单请求超时或 ctx 被取消。reqBody 被序列化进请求信封；
// 收到 2xx 时若 respBody 非空，会从响应体反序列化进去。返回响应状态码（请求从未到
// 达服务器时为 0），让调用方区分传输失败（→ HTTP 回退）与服务端错误。
func (c *wsRPCClient) call(ctx context.Context, method string, serverTimeout time.Duration, reqBody, respBody any, requireRPCV1 bool) (int, error) {
	if c == nil {
		return 0, errWSRPCUnavailable
	}
	var rawReq json.RawMessage
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return 0, fmt.Errorf("ws rpc: marshal request: %w", err)
		}
		rawReq = b
	}
	id := uuid.NewString()
	frame, err := json.Marshal(protocol.Message{
		Type: protocol.EventDaemonRPCRequest,
		Payload: marshalRaw(protocol.RPCRequestPayload{
			RequestID: id,
			Method:    method,
			Body:      rawReq,
			TimeoutMs: serverTimeout.Milliseconds(),
		}),
	})
	if err != nil {
		return 0, fmt.Errorf("ws rpc: marshal frame: %w", err)
	}

	ch := make(chan protocol.RPCResponsePayload, 1)
	c.mu.Lock()
	if c.sendFrame == nil || (requireRPCV1 && !c.rpcV1Supported) {
		c.mu.Unlock()
		return 0, errWSRPCUnavailable
	}
	send := c.sendFrame
	c.pending[id] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	item, err := send(frame)
	if err != nil {
		return 0, fmt.Errorf("ws rpc: send: %w", err)
	}

	// giveUp 处理一次被放弃的请求。若帧还在排队就取消它，让 writer 永不送达——这是
	// “确定没发出去”的结果，可安全地 HTTP 回退；若 writer 已开始发送，则它可能到达
	// 服务器，结果不确定，调用方绝不能回退（会重复认领，MUL-4257）。
	giveUp := func() error {
		if item.cancel() {
			return errWSRPCUnavailable
		}
		return errWSRPCUncertain
	}

	// 等服务端预算外加一段宽限：刚好在服务器截止前完成提交的 claim 必须在 daemon
	// 放弃并回退 HTTP 之前报回来，否则就会重复认领（MUL-4257）。
	timeout := serverTimeout + c.grace
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case resp, ok := <-ch:
		if !ok {
			// 连接脱离了。服务器是否看到这个请求取决于帧是否已离开 writer，因此交给
			// giveUp() 判断（没发出去 → 可安全回退；发出去了 → 结果不确定）。
			return 0, giveUp()
		}
		if resp.Status >= 200 && resp.Status < 300 {
			if respBody != nil && len(resp.Body) > 0 {
				if err := json.Unmarshal(resp.Body, respBody); err != nil {
					return resp.Status, fmt.Errorf("ws rpc: decode response: %w", err)
				}
			}
			return resp.Status, nil
		}
		msg := resp.Error
		if msg == "" {
			msg = fmt.Sprintf("ws rpc status %d", resp.Status)
		}
		return resp.Status, errors.New(msg)
	case <-timer.C:
		// 预算耗尽。若帧还堵在背压的 writer 后面，取消它，确保我们回退后它不会被送
		// 出（giveUp → 没发出）；若它已离开 writer，结果不确定，不能回退。
		if err := giveUp(); errors.Is(err, errWSRPCUncertain) {
			return 0, err
		}
		return 0, fmt.Errorf("ws rpc: timeout after %s: %w", timeout, errWSRPCUnavailable)
	case <-ctx.Done():
		item.cancel()
		return 0, ctx.Err()
	}
}

// deliver 把一条入站的 rpc_response 帧转给正在等待的 Call。发送在锁内完成，因此与
// attach(nil) 的 close+delete 串行：仍在 pending 里的 channel 保证还没被关闭，这里
// 绝不会向已关闭的 channel 发送。未知的 request id（已超时/已脱离）直接丢弃。
func (c *wsRPCClient) deliver(resp protocol.RPCResponsePayload) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	ch, ok := c.pending[resp.RequestID]
	if !ok {
		return
	}
	select {
	case ch <- resp:
	default:
	}
}

// ClaimTasksWSFirst 是“WS 优先”的 claim 策略（MUL-4257）：当 WS 控制连接已附着时
// 通过它发起 tasks.claim RPC，遇到“确定没到达服务器”的传输失败（无连接、写缓冲满、
// 未发送就超时）或服务端错误时回退到 HTTP claim 端点。已发送但断连/超时属于结果
// 不确定，只在短暂的安全窗口过后才经 HTTP 重试。请求/响应体与 HTTP 端点完全一致，
// 两种传输可互换。作为轮询切换的一部分接入 claim 轮询器。
func (d *Daemon) ClaimTasksWSFirst(ctx context.Context, daemonID string, runtimeIDs []string, maxTasks int) ([]*Task, error) {
	// 没升级、没有 batch 路由的服务器：之前的某次轮询已经学到这一点（通过 404），
	// 因此直接走旧版按 runtime 的 claim，每轮都跳过 WS + batch 尝试。
	if d.batchClaimUnsupported.Load() {
		return d.client.claimTasksLegacy(ctx, runtimeIDs, maxTasks)
	}
	bypassWSOnce := false
	if retryAfterNanos := d.wsClaimHTTPFallbackAfter.Load(); retryAfterNanos > 0 {
		retryAfter := time.Unix(0, retryAfterNanos)
		now := time.Now()
		if now.Before(retryAfter) {
			d.logger.Debug("ws claim outcome uncertain; delaying http fallback until safety window elapses",
				"retry_after", retryAfter.Sub(now).Round(time.Millisecond))
			return nil, nil
		}
		if d.wsClaimHTTPFallbackAfter.CompareAndSwap(retryAfterNanos, 0) {
			bypassWSOnce = true
			d.logger.Debug("previous ws claim outcome uncertain; using http fallback for this claim cycle")
		}
	}
	if !bypassWSOnce && d.wsRPC.supportsRPCV1() {
		var resp struct {
			Tasks []*Task `json:"tasks"`
		}
		// batchClaimRequestTimeout 是服务端执行预算；daemon 等这么久再加客户端的
		// 宽余量来收响应。
		_, err := d.wsRPC.CallIfRPCV1Supported(ctx, "tasks.claim", batchClaimRequestTimeout, map[string]any{
			"daemon_id":   daemonID,
			"runtime_ids": runtimeIDs,
			"max_tasks":   maxTasks,
		}, &resp)
		if err == nil {
			return resp.Tasks, nil
		}
		if errors.Is(err, errWSRPCUncertain) {
			// 这次 WS claim 可能已在服务端提交；立刻再用 HTTP 去认领同样的空闲槽会
			// 重复认领。跳过本轮，等“服务端执行预算 + 响应宽限”过去之后强制做一次
			// HTTP batch claim。若 WS claim 已提交，任务已被派发，交给 stale reclaim
			// 兜底恢复；若没提交，HTTP 让排队任务恢复活性。
			delay := wsClaimUncertainFallbackDelay
			if delay < 0 {
				delay = 0
			}
			d.wsClaimHTTPFallbackAfter.Store(time.Now().Add(delay).UnixNano())
			d.logger.Debug("ws claim outcome uncertain after disconnect; delaying http fallback", "retry_after", delay)
			return nil, nil
		}
		d.logger.Debug("ws claim failed; falling back to http", "error", err)
	}
	tasks, err := d.client.ClaimTasks(ctx, daemonID, runtimeIDs, maxTasks)
	if err == nil {
		return tasks, nil
	}
	// 服务器没有 batch 路由（404）：冻结旧 API 契约，回退到旧的逐 runtime claim 循环，
	// 并记住这个事实，免得每个周期都重新探测。
	if isBatchClaimUnsupported(err) {
		d.batchClaimUnsupported.Store(true)
		d.logger.Info("batch claim route unsupported by server; using legacy per-runtime claim")
		return d.client.claimTasksLegacy(ctx, runtimeIDs, maxTasks)
	}
	return nil, err
}
