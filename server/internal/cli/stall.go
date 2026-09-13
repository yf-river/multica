// stall.go 实现「传输停滞(stall)检测」：http.Client.Timeout 是对整条请求（含响应体）
// 计墙钟时间，于是它惩罚的恰好是「正在正常工作的传输」——一条在慢链路上稳定到达的
// 599KB skill 包，会在 30 秒读到一半时被杀掉；而一条真死的连接却同样被挂满 30 秒
// （GH #7498）。真正该问的不是「是否太久了」而是「是否还在前进」。
//
// 因此下面的客户端把墙钟降级为「兜底」，把「有进展」当成主判据：连接与响应头阶段有
// 上界、读 body 必须持续产出字节、整体再套一个松到健康传输碰不到的总上限。慢但活着
// 的传输能跑完，死了的比以往更早失败。
//
// 这套机制是通用的，放这里供任何调用方使用。先用上的场景是 skill 命令
// （见 newSkillAPIClient），因为故障正是在那里被报告的。转正刻意只需一步：把
// NewAPIClient 指向 NewStallAwareHTTPClient、把 StallAwareContext 并入 APIContext、
// 删掉 skill 私有的构造器即可。常驻第二套 HTTP 客户端会让 CLI 出现两套超时人格，
// 比只留其中任何一套都糟。
package cli

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// defaultStallTimeout 是一条传输「完全没有任何字节产出」超过多久就判死。它比被
	// 取代的 30 秒总超时更紧：既然判据变成「是否有进展」，就没必要等完整个慢下载
	// 理论上可能需要的时长。
	defaultStallTimeout = 15 * time.Second

	// defaultTransferCeiling 给「一直在前进、永远跑不完」的命令（服务器无限地一点
	// 一点吐字节）设个上限。它只是兜底、不是主判据，所以松到健康传输碰不到——但
	// 也绝不会缺席。
	defaultTransferCeiling = 10 * time.Minute
)

// StallTimeout 返回单次读取的「无进展预算」（多久没新数据就判死）。
//
// MULTICA_HTTP_STALL_TIMEOUT 直接设置它。若没设，则遵循显式设置的 MULTICA_HTTP_TIMEOUT：
// 在该路径上它保留字面含义——「用户愿意干等多久没有任何东西到达」——而不是又悄悄
// 变回总时长上限。两个都没设时用 defaultStallTimeout。
func StallTimeout() time.Duration {
	if d, ok := durationFromEnv("MULTICA_HTTP_STALL_TIMEOUT"); ok {
		return d
	}
	if _, ok := durationFromEnv("MULTICA_HTTP_TIMEOUT"); ok {
		return httpTimeout()
	}
	return defaultStallTimeout
}

// TransferCeiling 返回一条「感知停滞」命令的硬上限。它永远不会低于停滞预算的某个
// 倍数，因此把停滞阈值调高不会让上限反过来先触发、把停滞检测掩盖掉。
func TransferCeiling() time.Duration {
	ceiling := defaultTransferCeiling
	if floor := 4 * StallTimeout(); floor > ceiling {
		ceiling = floor
	}
	return ceiling
}

// durationFromEnv 解析时长环境变量，兼容 Go duration 字符串（"45s"、"2m"）或纯
// 数字秒数（"45"），与 httpTimeout 的解析口径一致。未设置/为空/非法/非正值时返回
// false，让调用方区分「用户有意这么设」与「该回退默认值」。
func durationFromEnv(name string) (time.Duration, bool) {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return 0, false
	}
	if d, err := time.ParseDuration(v); err == nil && d > 0 {
		return d, true
	}
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second, true
	}
	return 0, false
}

// StallError 表示一条「停止产出字节」的传输。它刻意与「超时(deadline)」错误不同：
// 「收到 200KB 之后 15 秒没新数据」和「整条命令撞上总上限」指向不同的原因与修法，
// 把它们折叠成同一个 "request timed out" 正是当初 #7498 在客户端难排障的原因。
type StallError struct {
	// Idle 是被突破的「无进展预算」时长。
	Idle time.Duration
	// BytesRead 是停滞前已到达的 body 字节数。0 表示 body 根本没开始，通常意味着
	// 连接或服务器死了，而不是链路慢。
	BytesRead int64
	Op        string // 例如 "GET /api/skills/abc"——只在 --debug 里显示
	Err       error  // 底层取消原因，供 --debug 用
}

// Error 拼出停滞错误的描述文本。
func (e *StallError) Error() string {
	msg := fmt.Sprintf("transfer stalled: no data received for %s after %d bytes", e.Idle, e.BytesRead)
	if e.Op != "" {
		return e.Op + ": " + msg
	}
	return msg
}

// Unwrap 暴露底层取消错误。
func (e *StallError) Unwrap() error { return e.Err }

// NewStallAwareHTTPClient 构造本文件开头描述的 HTTP 客户端：连接/TLS/响应头阶段有
// 界、响应体「停止前进就失败」、两者之后还有一道松的总墙钟上限。
func NewStallAwareHTTPClient() *http.Client {
	idle := StallTimeout()
	base := &http.Transport{
		// ProxyFromEnvironment 与 http.DefaultTransport 保持一致。去掉它会静默坑掉
		// 所有在公司代理后面的用户——而这个人群恰好和本改动要解决的「慢链路」用户
		// 高度重叠。
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: idle, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   idle,
		ExpectContinueTimeout: 1 * time.Second,
		// 覆盖 body 守卫看不到的阶段：连接已建立、请求已发出、服务器迟迟不回。
		ResponseHeaderTimeout: idle,
	}
	return &http.Client{
		Transport: &stallGuardTransport{base: base, idle: idle},
		// 墙钟仍然保留，但从「主失败判据」降为「兜底」，且松到健康传输碰不到。若设成
		// 0，会让「停滞的请求*上传*」——响应体守卫看不到的那个方向——失去任何时限，
		// 等于拆掉一道墙钟又悄悄弄丢了另一道。
		Timeout: TransferCeiling(),
	}
}

// StallAwareContext 为 NewStallAwareHTTPClient 构造的客户端派生一个命令 context。
// 和 APIContext 一样，它让传输层自己的 deadline 先触发，这样失败表现为可分类的网络
// 错误，而不是一句裸的 context 取消。
func StallAwareContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, TransferCeiling()+apiContextGrace)
}

// stallGuardTransport 把每条响应体都包上一层「进度检查」。
type stallGuardTransport struct {
	base http.RoundTripper
	idle time.Duration
}

func (t *stallGuardTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// 真正能打断「阻塞在 body 读取上」的是取消这个 context；从别的 goroutine 去
	// close body 会跟读取方竞争。req.WithContext 会复制请求，所以调用方自己的请求
	// 不受影响，符合 RoundTripper 契约。
	ctx, cancel := context.WithCancel(req.Context())
	resp, err := t.base.RoundTrip(req.WithContext(ctx))
	if err != nil {
		cancel()
		return nil, err
	}
	op := ""
	if req.URL != nil {
		op = req.Method + " " + req.URL.Path
	}
	resp.Body = newStallGuardBody(resp.Body, t.idle, cancel, op)
	return resp, nil
}

// stallGuardBody 让「持续 idle 时长没有任何字节产出」的 Read 失败。
//
// 计时器是「到点复查」而不是每次 Read 都 Reset：热路径上的 Reset 会和计时器触发
// 竞争，可能误杀一条刚送完数据的传输。这里计时器醒来后，在与 Read 写入进度的同一把
// 锁下重读进度：要么按剩余时间重新武装，要么判定停滞。代价至多每个停滞窗口多一次
// 唤醒。
type stallGuardBody struct {
	rc     io.ReadCloser
	idle   time.Duration
	cancel context.CancelFunc
	op     string
	timer  *time.Timer

	mu      sync.Mutex
	last    time.Time // 最近一次读到数据的时间
	read    int64     // 已累计读到的字节数
	tripped bool      // 是否已判定停滞
	closed  bool      // 是否已关闭
}

func newStallGuardBody(rc io.ReadCloser, idle time.Duration, cancel context.CancelFunc, op string) *stallGuardBody {
	b := &stallGuardBody{rc: rc, idle: idle, cancel: cancel, op: op, last: time.Now()}
	b.timer = time.AfterFunc(idle, b.check)
	return b
}

// check 是计时器回调：若距上次有数据已超过 idle，则置 tripped 并取消派生 context
// 中断读取；否则按剩余时间重新武装。
func (b *stallGuardBody) check() {
	b.mu.Lock()
	if b.closed || b.tripped {
		b.mu.Unlock()
		return
	}
	if remaining := b.idle - time.Since(b.last); remaining > 0 {
		b.timer.Reset(remaining)
		b.mu.Unlock()
		return
	}
	b.tripped = true
	b.mu.Unlock()
	b.cancel()
}

func (b *stallGuardBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	if n > 0 {
		b.mu.Lock()
		b.read += int64(n)
		b.last = time.Now()
		b.mu.Unlock()
	}
	if err != nil && err != io.EOF {
		if stalled := b.stallError(err); stalled != nil {
			return n, stalled
		}
	}
	return n, err
}

func (b *stallGuardBody) Close() error {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	b.timer.Stop()
	err := b.rc.Close()
	// 等 body 用完再释放派生 context；提前取消会打断这条正被守卫保护的读取。
	b.cancel()
	return err
}

// stallError 把守卫自己触发的取消翻译成停滞的原因。其它原因造成的错误原样放行。
func (b *stallGuardBody) stallError(cause error) *StallError {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.tripped {
		return nil
	}
	return &StallError{Idle: b.idle, BytesRead: b.read, Op: b.op, Err: cause}
}
