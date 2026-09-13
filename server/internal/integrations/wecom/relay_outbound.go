// relay_outbound.go —— 一条回复怎样到达「能发送它的那个副本」。
//
// 企微是唯一没有出站 REST 路径的通道：每个写都走 aibot WebSocket，而 WS 租约保
// 证只有恰好一个副本握着它。与此同时，chat:done 由「服务 daemon 的 POST
// /tasks/{id}/complete 请求的那个副本」——一次负载均衡的决策——在进程内
// events.Bus 上发布。离开租约时，outbound.go 过去除了丢弃回复无事可做（GH #7215、
// #6890；SELF_HOSTING.md 写明由此产生的约束）。
//
// 本文件经仓库已经为「恰好这种形状的问题」在跑的 Redis Stream 中继路由它：
// daemonws.RelayNotifier 以 ScopeDaemonRuntime 发布，唤醒连在另一副本上的
// daemon；每个节点的 XREAD 循环把帧交给握着那条连接的人。
//
// 那套机制对它的消费者有三条要求，而照抄 daemon 唤醒形状的朴素版本满足不了：
//
//  1. 它会重放。分片读者从 (now - ReplayGrace) 而不是 "$" 开始读，所以 pod 宕机
//     期间发布的事件会被重读——它自己的文档写着「下游消费者必须幂等」。进程内的
//     seen-set 跨重启不幂等，也不跨两个副本。所以占位必须住在 Redis 里、以那一轮
//     作键、寿命要舒舒服服长过重放窗口。Redis 在这里不是新依赖：它就是传输本身。
//     没有 Redis 就没有中继，就没有跨副本投递、也就没有要去重的东西。
//
//  2. 它在分片读取循环上【同步】调用消费者。daemon 唤醒在那里的活是一次 map 查
//     询加一次带缓冲写；我们的是网络往返、要等至多 ackTimeout 拿平台的裁定。内联
//     干会让一个不健康的机器人拖垮浏览器实时流量、daemon 唤醒、以及那个分片上的
//     每个其他机器人。所以 DeliverWecomOutbound 只把帧交给一条有界队列就返回。
//
//  3. 它的读者启动得很早。因此注册必须在 senders 注册表和订阅者存在之前就能发
//     生：这个对象最先被构造和注册，Attach 稍后补上 handler。中间到达的帧在队列
//     里等着，而不是撞上一个 nil 字段被丢弃。
//
// 顺序：同一条安装的每条回复由一条队列、一个 worker 承载，按到达顺序处理——包括
// 跨一次「重新递出」。一帧必须再试一次时不会排到队列尾；它停在自己安装那条队的
// 队首，后面一切都等它——因为「同一个人收到的两条回答顺序颠倒」正是本顺序机制存
// 在要防的缺陷。
//
// 分片是并发上限、不是隔离保证：安装被哈希到 RelayConfig.Shards 条队列上，所以撞
// 上同一分片的两台机器人确实会互相排队。分片数买来的是「一台慢机器人占不满所有
// worker」。
package wecom

import (
	"context"
	"encoding/json"
	"errors"
	"hash/fnv"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/realtime"
	"github.com/multica-ai/multica/server/internal/util"
)

// relayPublisher 是本包要用的 realtime relay 切片。*realtime.ShardedStreamRelay
// 和 *realtime.RedisRelay 都满足它。
type relayPublisher interface {
	PublishWithID(scopeType, scopeID, exclude string, frame []byte, id string) error
}

// DedupeStore 是跨重启、跨两个副本的「至多一次」占位。生产用 Redis 撑底（见
// NewRedisDedupe）；nil 只留下进程内的闸门——对单副本部署这是对的，因为那种部署
// 根本不用中继。
type DedupeStore interface {
	// Claim 报告调用方是不是第一个拿下 key 的人。它必须在跨进程间原子。
	Claim(ctx context.Context, key string, ttl time.Duration) (bool, error)
	// Release 把占位还回去，用于一次「可证没有发生」的投递。
	Release(ctx context.Context, key string)
	// Held 报告 key 此刻是否被占着。发布方靠它事后得知「它路由出去的那次投递是否
	// 有哪个副本接」——见 RelayOutbound.watchOutcomes。
	Held(ctx context.Context, key string) (bool, error)
	// ClaimBudget 是上面任意一次单程往返最长能花多久。存储声明它，因为也是存储执
	// 行它：outcomeGrace 每次递出付一次这个预算，而由分发器另存一个副本的数字可以
	// 自由漂移、偏离实际生效的超时。
	ClaimBudget() time.Duration
}

// minDedupeTTL 给占位寿命定了底。占位维持的不变量是「它比中继的重放窗口活得
// 久」——一帧还在可重放期就过期的占位不算占位——而那个窗口是 OPERATOR 旋钮
// （REALTIME_RELAY_REPLAY_GRACE），不是常量，所以 TTL 也不能是：一个 2 小时的宽
// 限期对着固定 1 小时 TTL，会在两者之间的任何一次重启上把用户已读过的回答重发一
// 遍。dedupeTTLFor 从它推导。
const minDedupeTTL = time.Hour

// dedupeTTLFor 是 max(minDedupeTTL, 2×replayGrace)：宽限期的两倍，好让占位稳稳
// 活过它守护的窗口；取底是为了极小宽限期不会造出比「默认一直以来的那 1 小时」还
// 短的占位。
func dedupeTTLFor(replayGrace time.Duration) time.Duration {
	if ttl := 2 * replayGrace; ttl > minDedupeTTL {
		return ttl
	}
	return minDedupeTTL
}

// RelayConfig 给分发器定尺寸。没有一项是口味问题：每一项背后都有一个运维边界，
// 已写进各自注释；两个边界住在本包外的（中继的重放窗口、supervisor 的租约轮询间
// 隔）以参数传入而不是靠猜。
//
// 零值字段取文档里的默认值。
type RelayConfig struct {
	// Shards 是多少条独立队列承载帧。它是并发上限而非顺序上限：顺序来自哈希把
	// 一条安装放上一条队列，而撞在同一分片上的安装确实会互相排队。所以定尺寸时
	// 该问的是「在健康机器人排到它们后面之前，允许多少台机器人同时变慢」。八条
	// 覆盖一次部署量级的机器人、对着唯一会让投递变慢的东西——ackTimeout，5 秒
	// ——同时只保留 8 个空闲 goroutine，而不是每安装一个。
	Shards int

	// QueueDepth 是每分片的深度，而它必须吸收的突发是一次重启的重放：分片读者从
	// (now - ReplayGrace) 打开、把窗口内发布的一切一次交出。每分片 256 在默认八
	// 分片上是在飞 2048 帧——超过一次企微回答的重放窗口对单套部署能容纳的量，又
	// 小到「被楔住的机器人只会被拒发、不会无界撑大堆」。
	QueueDepth int

	// DrainBudget 约束关停后整个排空过程，不是单次投递。它的边界在外部：排空跑在
	// 进程自己的关停里，所以必须能塞进通道 supervisor 的 ShutdownTimeout
	// （engine.Config，默认 15s），还要给 supervisor 自己 join 留空间。十秒就是那
	// 个合适尺寸；够不着的就留给占位 TTL 和下一副本的重放窗口。
	DrainBudget time.Duration

	// ReplayGrace 是中继配置的启动回看窗口。它决定占位 TTL 的尺寸（dedupeTTLFor），
	// 而且是运维旋钮（REALTIME_RELAY_REPLAY_GRACE）——这正是它作为参数的原因。
	ReplayGrace time.Duration

	// LeaseSettle 是 WebSocket 租约完成移动到另一副本要花的时间，它决定重试链的
	// 尺寸。其边界是通道 supervisor 的 PollInterval（CHANNEL_WS_LEASE_POLL_INTERVAL，
	// 默认 30s）：副本在它下一次清扫时才知道自己该持有某条安装，所以一帧在移动
	// 中途到达时，至少要重递那么久，否则会在持有者还在路上的时候就放弃它。以前
	// 固定 5 次、每次 200ms 只覆盖 3 秒——一次轮询的十分之一——早在它所服务的那
	// 次移动结束前就过期了。
	LeaseSettle time.Duration

	// RetryBackoff 是两次重递之间的第一次等待；其后逐次翻倍、以 LeaseSettle 的四
	// 分之一为顶——租约若提前落地，不必干坐。刻意取小：重递成功的常见原因就是移
	// 动已经完成。
	RetryBackoff time.Duration
}

const (
	defaultRelayShards      = 8
	defaultRelayQueueDepth  = 256
	defaultRelayDrainBudget = 10 * time.Second
	// defaultRelayLeaseSettle 是 supervisor 自己的默认轮询间隔，是引用而非重抄：
	// 同一个量，在这里再存一份就可以自由漂移、偏离它本要「活得比它久」的那个东西。
	defaultRelayLeaseSettle  = engine.DefaultPollInterval
	defaultRelayRetryBackoff = 200 * time.Millisecond
)

// withDefaults 填掉零字段并返回补全后的配置。
func (c RelayConfig) withDefaults() RelayConfig {
	if c.Shards <= 0 {
		c.Shards = defaultRelayShards
	}
	if c.QueueDepth <= 0 {
		c.QueueDepth = defaultRelayQueueDepth
	}
	if c.DrainBudget <= 0 {
		c.DrainBudget = defaultRelayDrainBudget
	}
	if c.LeaseSettle <= 0 {
		c.LeaseSettle = defaultRelayLeaseSettle
	}
	if c.RetryBackoff <= 0 {
		c.RetryBackoff = defaultRelayRetryBackoff
	}
	return c
}

// retryPlan 是重递链，按 settle 窗口一次性算出来而不是手写：延迟从 RetryBackoff
// 逐次翻倍、各自以 LeaseSettle 的四分之一为顶，链足够长到 TOTAL 覆盖一个半 settle
// 窗口——移动本身，外加半个给随后而来的重连与订阅。
//
// 双保险设界，病态配置也造不出无界链：至多 relayRetryChainCap 项。
const relayRetryChainCap = 24

func (c RelayConfig) retryPlan() []time.Duration {
	target := c.LeaseSettle + c.LeaseSettle/2
	cap := c.LeaseSettle / 4
	if cap < c.RetryBackoff {
		cap = c.RetryBackoff
	}
	var (
		plan  []time.Duration
		total time.Duration
		delay = c.RetryBackoff
	)
	for total < target && len(plan) < relayRetryChainCap {
		if delay > cap {
			delay = cap
		}
		plan = append(plan, delay)
		total += delay
		delay *= 2
	}
	return plan
}

// relayFrame 是一次在副本之间运输途中的投递。凡租约持有方能自己读的东西，它带
// id 而不是渲染好的 payload——所以附件由将要发送它的副本去取，而不是经 Redis 运
// 送。
type relayFrame struct {
	Kind           string `json:"kind"` // relayKindReply | relayKindInbox
	InstallationID string `json:"installation_id"`
	ChatID         string `json:"chat_id"`
	ChatType       int    `json:"chat_type"`
	Content        string `json:"content"`
	TaskID         string `json:"task_id,omitempty"`
	MessageID      string `json:"message_id,omitempty"`
	WorkspaceID    string `json:"workspace_id,omitempty"`
	SessionID      string `json:"session_id,omitempty"`
	CarriesFiles   bool   `json:"carries_files,omitempty"`
}

const (
	relayKindReply = "reply"
	relayKindInbox = "inbox"
)

// relayHandler 在握着 socket 的那个副本上执行一次投递。*Outbound 实现它；这层间
// 接让本对象能在订阅者存在之前就注册给中继。
type relayHandler interface {
	deliverRelayed(ctx context.Context, f relayFrame) deliveryOutcome
	// ownsSocket 回答「本进程此刻是否握着那条安装的活连接」。它给全局占位把门
	// ——见 perform。
	ownsSocket(installationID string) bool
}

// deliveryOutcome 告诉分发器这次占位能否释放。
type deliveryOutcome int

const (
	// outcomeNotOurs —— 本副本没有那条安装的 socket。别个副本会接这帧；占位必须
	// 还回去。
	outcomeNotOurs deliveryOutcome = iota
	// outcomeDone —— 已送达，或以「不可重试的方式」失败。
	outcomeDone
	// outcomeProvablyNotSent —— 什么都没上线路，所以占位还回去、重放或另一副本可
	// 以再试。刻意不用于 ack 超时：errAckTimeout 意味着帧很可能已到，而本适配器
	// 的既定规则是「调用方自担风险地重试这类发送」。
	outcomeProvablyNotSent
)

// seenEvents 是进程内、有界的一批「已处理过」的中继事件 id 集合。它是挡在 Redis
// 占位前的一道廉价首闸——发布方拿到自己刚发的帧是常见情形、不需要一次往返。它不
// 是幂等机制；DedupeStore 才是。
type seenEvents struct {
	mu    sync.Mutex
	ids   map[string]struct{}
	order []string
	limit int
}

func newSeenEvents(limit int) *seenEvents {
	return &seenEvents{ids: make(map[string]struct{}, limit), limit: limit}
}

func (s *seenEvents) claim(id string) bool {
	if id == "" {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.ids[id]; dup {
		return false
	}
	s.ids[id] = struct{}{}
	s.order = append(s.order, id)
	if len(s.order) > s.limit {
		delete(s.ids, s.order[0])
		s.order = s.order[1:]
	}
	return true
}

func (s *seenEvents) forget(id string) {
	if id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.ids, id)
}

// queued 是一帧在等 worker，带着它各次占位所用的 id、以及 worker 已捡起过几次。
type queued struct {
	frame    relayFrame
	eventID  string
	attempts int
}

// 一次释放的占位除了本进程没人会去重试它：每个副本对给定流帧恰好读一次、只在重
// 启时重放，所以「释放好让持有者来接」在持有者已处理过自己的那份拷贝时会把回复晾
// 干——它抢占位输了、返回了，而永远不会再有人唤醒它。所以「释放的一方」就是「按
// RelayConfig.retryPlan 重递、直到租约落定」的一方。

// hold 是某条安装的一列队，当它的队首正在等一次重递超时。items[0] 是要重试的
// 帧；它后面的一切都到得更晚、绝不能超车。恰好由一个 worker goroutine 拥有，所以
// 不用加锁。
type hold struct {
	items   []queued
	readyAt time.Time
}

// RelayOutbound 把一次投递发布给握着机器人 socket 的副本，并执行其他副本发布的
// 那些。每进程一个。
type RelayOutbound struct {
	publisher relayPublisher
	dedupe    DedupeStore
	dedupeTTL time.Duration
	cfg       RelayConfig
	retryPlan []time.Duration
	logger    *slog.Logger
	seen      *seenEvents

	// verify 把一条被路由回复的 id 带给结局观察者——「到底有没有人最终送达」的唯
	// 一负责人。带缓冲、宁可拒发也不阻塞：发布方跑在总线订阅者的 goroutine 上。
	verify chan pendingOutcome

	// metrics 在构造之后才到：本对象在 metrics 注册表存在之前就建好了，因为中继的
	// 分片读者比两者都先启动。用 atomic 是因为 boot 还在接线时 DeliverWecomOutbound
	// 已经在分片读者上跑了。
	metrics atomic.Value // Metrics

	ready   chan struct{}
	handler relayHandler

	queues []chan queued
	wg     sync.WaitGroup
}

// NewRelayOutbound 构造跨副本路由器。replayGrace 是中继配置的启动重放窗口，它决
// 定占位 TTL 的尺寸（dedupeTTLFor）。拿订阅者调 Attach、拿进程 context 调 Start；
// 在此之前把它注册给中继都是安全的。
func NewRelayOutbound(publisher relayPublisher, dedupe DedupeStore, cfg RelayConfig, logger *slog.Logger) *RelayOutbound {
	if logger == nil {
		logger = slog.Default()
	}
	cfg = cfg.withDefaults()
	r := &RelayOutbound{
		publisher: publisher,
		dedupe:    dedupe,
		dedupeTTL: dedupeTTLFor(cfg.ReplayGrace),
		cfg:       cfg,
		retryPlan: cfg.retryPlan(),
		logger:    logger,
		seen:      newSeenEvents(4096),
		verify:    make(chan pendingOutcome, cfg.QueueDepth),
		ready:     make(chan struct{}),
		queues:    make([]chan queued, cfg.Shards),
	}
	for i := range r.queues {
		r.queues[i] = make(chan queued, cfg.QueueDepth)
	}
	return r
}

// SetMetrics 装健康出口。启动期间调一次、在任何投递能跑之前（worker 等 Attach），
// 所以唯一的并发读者是分片读者上的拒发计数器——这就是它要用 atomic 的原因。
func (r *RelayOutbound) SetMetrics(m Metrics) {
	if r != nil && m != nil {
		r.metrics.Store(m)
	}
}

func (r *RelayOutbound) mx() Metrics {
	if v := r.metrics.Load(); v != nil {
		return v.(Metrics)
	}
	return nopMetrics{}
}

// Attach 提供执行投递的 handler。在订阅者存在之后调一次。此前收到的帧在各自队列
// 里等待。
func (r *RelayOutbound) Attach(h relayHandler) {
	if r == nil {
		return
	}
	r.handler = h
	close(r.ready)
}

// Start 把 worker 跑到 ctx 结束，然后把已入队的东西排空，好让优雅关停不把某人正
// 在等的回复晾干。
func (r *RelayOutbound) Start(ctx context.Context) {
	if r == nil {
		return
	}
	for i := range r.queues {
		r.wg.Add(1)
		go r.work(ctx, r.queues[i])
	}
	r.wg.Add(1)
	go r.watchOutcomes(ctx)
}

// handlerReady 阻塞到 Attach 跑过，并报告是否确有可用来投递的 handler。
func (r *RelayOutbound) handlerReady(ctx context.Context) bool {
	select {
	case <-r.ready:
		return true
	case <-ctx.Done():
		// handler 到达前（或到达途中）被取消。若它确实已挂上——select 会在两条
		// 已就绪 channel 间任意选——已入队的仍可排空；没有它，排空也无物可递。
		select {
		case <-r.ready:
			return true
		default:
			return false
		}
	}
}

// work 是一个分片的整个生命周期。
//
// 它碰的每样东西都只属于这个 goroutine：按安装的列队和唤醒它们的那个 timer。两
// 者都是刻意的。列队让一帧被重递时待在它安装队列的队首而不是队尾，所以同一机器
// 人后来的回复无法超车。timer 属于 worker 自己，让「挂起的重递」成为 worker 的一
// 部分——在外部安排的 timer 可能等 worker 排空退出后才触发，把一帧放进一条没人读
// 的队列。
func (r *RelayOutbound) work(ctx context.Context, q chan queued) {
	defer r.wg.Done()
	if !r.handlerReady(ctx) {
		return
	}
	lines := map[string]*hold{}
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		wait := time.Hour
		if next, ok := earliestDue(lines); ok {
			if wait = time.Until(next); wait < 0 {
				wait = 0
			}
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(wait)

		select {
		case item := <-q:
			// select 是公平的，所以一条 item 可能赢过已触发的 ctx.Done。在已取消
			// 的 context 上执行它会把该帧错记成去重故障；它属于排空。
			if ctx.Err() != nil {
				r.drainRemaining(ctx, q, lines, &item)
				return
			}
			r.offer(ctx, lines, item)
		case <-timer.C:
			if ctx.Err() != nil {
				r.drainRemaining(ctx, q, lines, nil)
				return
			}
			r.fireDue(ctx, lines)
		case <-ctx.Done():
			r.drainRemaining(ctx, q, lines, nil)
			return
		}
	}
}

// earliestDue 是任何一条队列的队首可被再次递出的最近时刻。
func earliestDue(lines map[string]*hold) (time.Time, bool) {
	var soonest time.Time
	found := false
	for _, h := range lines {
		if len(h.items) == 0 {
			continue
		}
		if !found || h.readyAt.Before(soonest) {
			soonest, found = h.readyAt, true
		}
	}
	return soonest, found
}

// offer 从分片队列取走一帧。若它的安装已有一条在等重递的队列，它排到那条队的队
// 尾：超车它前面那帧，正是本机制存在要防的重排。
func (r *RelayOutbound) offer(ctx context.Context, lines map[string]*hold, item queued) {
	if h := lines[item.frame.InstallationID]; h != nil {
		if len(h.items) >= r.cfg.QueueDepth {
			r.shed(item, "the installation's line is full behind a retry")
			return
		}
		h.items = append(h.items, item)
		return
	}
	r.step(ctx, lines, item)
}

// step 跑一次投递、把它的含义归档到队列上。需要再递一次的帧待在其安装队列队首、
// 带着下一次延迟；结束的则让排它后面的东西上前。
func (r *RelayOutbound) step(ctx context.Context, lines map[string]*hold, item queued) {
	inst := item.frame.InstallationID
	if r.perform(ctx, item) {
		r.advance(lines, inst)
		return
	}
	delay, more := r.delayFor(item.attempts)
	if !more {
		// 耗尽链条的最常见原因，是占位正被「另一副本已送达」持有，所以这不计为丢
		// 弃。发布方的结局观察才是点名「没人投递」的东西，而且它只点名一次。
		r.logger.Warn("wecom relay: giving up on a routed delivery after retries",
			"installation_id", inst, "task_id", item.frame.TaskID,
			"attempts", item.attempts+1)
		r.advance(lines, inst)
		return
	}
	item.attempts++
	h := lines[inst]
	if h == nil {
		h = &hold{items: []queued{item}}
		lines[inst] = h
	} else {
		h.items[0] = item // 它是队首；step 只会被以队首调用
	}
	h.readyAt = time.Now().Add(delay)
}

// advance 让一个已结束的队首退役、并让那条安装的下一帧立刻到期。
func (r *RelayOutbound) advance(lines map[string]*hold, inst string) {
	h := lines[inst]
	if h == nil {
		return // 它直接来自队列、从未加入队列
	}
	h.items = h.items[1:]
	if len(h.items) == 0 {
		delete(lines, inst)
		return
	}
	h.readyAt = time.Time{} // 现在到期；下一个循环轮次捡起它
}

// fireDue 递出每一条队首已熬完退避的队列。
func (r *RelayOutbound) fireDue(ctx context.Context, lines map[string]*hold) {
	now := time.Now()
	for inst, h := range lines {
		if len(h.items) == 0 {
			delete(lines, inst)
			continue
		}
		if h.readyAt.After(now) {
			continue
		}
		r.step(ctx, lines, h.items[0])
	}
}

// delayFor 是下一次递出要等多久，以及是否还有下一次。
func (r *RelayOutbound) delayFor(attempts int) (time.Duration, bool) {
	if attempts < 0 || attempts >= len(r.retryPlan) {
		return 0, false
	}
	return r.retryPlan[attempts], true
}

// shed 记录一帧因队列空间不足被拒。它是 ADMISSION（准入）决策、仅此而已：
// relay_shed，按类型打标，记在拒它的那个副本上。
//
// 它刻意不动回复计数器。每个副本都读每一帧，所以没有副本能从这里判断「一次拒发
// 是否让用户付出了代价」——拒发的那个未必是本来会发它的那个；而且租约交接期间两
// 个副本可能同时握着 sender，所以连「我握着 socket」也不证明「只有我能发」。每个
// 副本各答各的这个问题，正是让同一条回复被同时计为「已送达」和「已丢弃」的根源。
//
// 所以回复结局留在唯一能在事后结清它的那个主人手里：发布方的 watchOutcomes，它问
// 「是否有任何副本认领过这次投递」，都没有才记一次损失。一次真正让用户丢了回复的
// 拒发，会以「无人认领的投递」到达那个主人、并在那里被计。
func (r *RelayOutbound) shed(item queued, why string) {
	r.mx().RecordRelayShed(item.frame.Kind)
	r.logger.Warn("wecom relay: shedding a routed delivery, "+why,
		"kind", item.frame.Kind,
		"installation_id", item.frame.InstallationID, "task_id", item.frame.TaskID)
}

// drainRemaining 把已入队的东西执行完，好让优雅关停不把回复晾干——整个排空共用一
// 个有界预算，所以一条满分片不会叠出一串串行的 ack 等待。临近边缘才启动的投递继
// 承 deadline、随它中止。first 是取消抢赢竞态时 worker 已从队列取走的一条 item。
// 待在队列里等重递的帧先走、且不等它们的退避：那次等待的存在是给租约时间移动，
// 而关停已超过了那个诉求。
func (r *RelayOutbound) drainRemaining(ctx context.Context, q chan queued, lines map[string]*hold, first *queued) {
	drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.cfg.DrainBudget)
	defer cancel()
	if first != nil {
		// 排到它安装队列的队尾而不是插队。这帧是在队列「队首已停着的任何东西」之
		// 后被取出的，先发它会把用户会话里的两条回答颠过来——正是 offer() 存在要
		// 防的重排，在出口处又做回去。那条安装没有队列时就没有可超车的。
		if h := lines[first.frame.InstallationID]; h != nil {
			h.items = append(h.items, *first)
		} else {
			r.perform(drainCtx, *first)
		}
	}
	for inst, h := range lines {
		for _, item := range h.items {
			if drainCtx.Err() != nil {
				return
			}
			r.perform(drainCtx, item)
		}
		delete(lines, inst)
	}
	for {
		if drainCtx.Err() != nil {
			return
		}
		select {
		case item := <-q:
			r.perform(drainCtx, item)
		default:
			return
		}
	}
}

// Wait 阻塞到每个 worker 都停下。供测试与关停用。
func (r *RelayOutbound) Wait() {
	if r != nil {
		r.wg.Wait()
	}
}

// publish 把一次投递交给其他副本。报告它是否出去了。
func (r *RelayOutbound) publish(f relayFrame, eventID string) bool {
	if r == nil || r.publisher == nil {
		return false
	}
	body, err := json.Marshal(f)
	if err != nil {
		r.logger.Warn("wecom relay: marshal outbound frame", "error", err)
		return false
	}
	if err := r.publisher.PublishWithID(realtime.ScopeWecomOutbound, f.InstallationID, "", body, eventID); err != nil {
		r.logger.Warn("wecom relay: publish failed",
			"error", err, "installation_id", f.InstallationID, "task_id", f.TaskID)
		return false
	}
	// 一条已发布的回复自此欠某人一个结局。watchOutcomes 就是那个某人——为什么不
	// 能是别人见它的注释。
	if f.Kind == relayKindReply {
		r.awaitOutcome(f, eventID)
	}
	return true
}

// DeliverWecomOutbound 是 realtime.WecomOutboundDeliverer。它绝不能阻塞：调用方是
// 共享的分片读取循环，那分片上的一切都在它后面等。
func (r *RelayOutbound) DeliverWecomOutbound(scopeID string, frame []byte, eventID string) {
	if r == nil {
		return
	}
	var f relayFrame
	if err := json.Unmarshal(frame, &f); err != nil {
		r.logger.Warn("wecom relay: undecodable frame", "error", err, "installation_id", scopeID)
		return
	}
	select {
	case r.queues[r.shardFor(f.InstallationID)] <- queued{frame: f, eventID: eventID}:
	default:
		// 宁可拒发也不卡住分片。要计数，因为「没人收到的回复」正是整条路径存在的
		// 目的要终结的那种静默。
		r.shed(queued{frame: f, eventID: eventID}, "dispatch queue full")
	}
}

func (r *RelayOutbound) shardFor(installationID string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(installationID))
	return int(h.Sum32() % uint32(len(r.queues)))
}

// perform 在一次「至多一次」占位下执行一次投递，并报告这帧是否 FINISHED——已送
// 达、被拒、或不归本副本。False 意味着它还欠一次递出，由调用方排在它安装队列的
// 队首。
func (r *RelayOutbound) perform(ctx context.Context, item queued) bool {
	// 所有权先、全局占位第二。占位是跨副本的 SET NX：每个副本都读每一帧，若让一
	// 个发不了的副本赢下它，能发的那副本就会输掉竞态、断定别人拿了、然后返回——
	// 而赢家随后发现自己没 socket、释放 key，从此再没人唤醒输家。每方行为都正确、
	// 回复还是丢了。所以副本只在「先确证自己能兑现」之后才去抢占位。socket 仍可能
	// 在这检查与发送之间消失；那个窗口正是 deliverRelayed 要复查、以及 not-ours 结
	// 局要释放占位的原因。
	if !r.handler.ownsSocket(item.frame.InstallationID) {
		return true
	}
	if !r.seen.claim(item.eventID) {
		return true
	}
	key := dedupeKey(item.eventID)
	if r.dedupe != nil {
		won, err := r.dedupe.Claim(ctx, key, r.dedupeTTL)
		if err != nil {
			// 答不上来的去重存储不能悄悄变成「至少一次」路径：会话里出现重复回答
			// 比晚到的回答更糟。但也没有外部机制去重试一个正在跑的进程——重放只在
			// 重启时发生——所以重试是我们自己的。
			r.seen.forget(item.eventID)
			r.logger.WarnContext(ctx, "wecom relay: dedupe unavailable, retrying locally",
				"error", err, "installation_id", item.frame.InstallationID)
			return false
		}
		if !won {
			// 输掉占位并不等于「别人会去送」。在租约移动的半路上，这里的输家可能
			// 正是【此刻】握着 socket 的副本，而赢家即将发现自己不再握着、释放
			// ——而且每个副本对每条流帧恰好读一次，外部永远不会把它送回来。所以输
			// 家要再查一次，有界、带退避；常见情形（占位被持有是因为回复已送达）
			// 会把链条烧完、停住。
			r.seen.forget(item.eventID)
			return false
		}
	}
	outcome := r.handler.deliverRelayed(ctx, item.frame)
	if outcome == outcomeDone {
		return true
	}
	// 不是我们的、或可证从未写出的：把占位还回去——并在本地也再递一次，因为光释
	// 放唤醒不了任何人（见上面的 !won）。
	r.seen.forget(item.eventID)
	if r.dedupe != nil {
		r.dedupe.Release(ctx, key)
	}
	return false
}

func dedupeKey(eventID string) string { return "wecom:outbound:claim:" + eventID }

// pendingOutcome 是一条端到端命运尚未可知的已路由回复。
type pendingOutcome struct {
	key       string
	sessionID string
	instID    string
	taskID    string
	dueAt     time.Time
}

// outcomeGrace 是一条被路由的回复在「发布方断定没人握着它」之前被给予的时间。它
// 必须活过整条重递链——仍在重试的回复是在途、不是丢了——再加上链条上每一次递出
// 要付的占位往返。
func (r *RelayOutbound) outcomeGrace() time.Duration {
	// 每条「递出」一次占位往返，不是整条链一次。每次重递都要调一次自己的 Claim、
	// 每次都能烧掉存储的整份 claim 预算，所以只用退避总和加一次往返算出的宽限期，
	// 会在链条面对慢存储仍运转时过期——观察者随后记一次损失，而下一次尝试会以送
	// 达反驳它。按最坏情况定尺寸，代价只是健康存储上「晚一点结清」，那是无害方向。
	budget := defaultClaimBudget
	if r.dedupe != nil {
		budget = r.dedupe.ClaimBudget()
	}
	total := budget * time.Duration(len(r.retryPlan)+1)
	for _, d := range r.retryPlan {
		total += d
	}
	return total
}

// watchOutcomes 是一条已路由回复最终结局的唯一主人。
//
// 别人当不了。发布方把帧交给 Redis 就返回；每个读到它却没 socket 的副本静默、正
// 确地返回、什么都不计；真送达的那副本计送达。所以当「没有任何副本握着 socket」
// ——全都在重连中——时，回复在每一方都行为正确、没有计数器移动的情况下丢了。那
// 正是 SELF_HOSTING.md 描述、而过去无法度量尺寸的窗口。
//
// 占位 key 让它事后可观察：由拿走投递的人设、只由「证明自己什么也没发出」的副本
// 释放。所以一旦重递链不可能还在跑，一个缺失的 key 就表示「这条回复谁也没送到」。
// 每条已路由回复一次 Redis EXISTS，而已路由回复是「离租约」的少数。
func (r *RelayOutbound) watchOutcomes(ctx context.Context) {
	defer r.wg.Done()
	if r.dedupe == nil {
		// 没有占位存储也就没有中继（见 DedupeStore），所以没有结局会成疑的已路由
		// 回复。
		for {
			select {
			case <-r.verify:
			case <-ctx.Done():
				return
			}
		}
	}
	var pending []pendingOutcome
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		wait := time.Hour
		if len(pending) > 0 {
			if wait = time.Until(pending[0].dueAt); wait < 0 {
				wait = 0
			}
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(wait)

		select {
		case p := <-r.verify:
			pending = append(pending, p) // 宽限期是常量，所以它保持有序
		case <-timer.C:
			now := time.Now()
			for len(pending) > 0 && !pending[0].dueAt.After(now) {
				r.settle(ctx, pending[0])
				pending = pending[1:]
			}
		case <-ctx.Done():
			// 关停不是证据。仍在宽限期内的回复也许会被握着它的副本送达，而本进程正
			// 在离开；在这里计它等于报一条没有发生的损失。
			if len(pending) > 0 {
				r.logger.Info("wecom relay: shutting down with routed replies still unresolved",
					"count", len(pending))
			}
			return
		}
	}
}

// settle 问「是否有任何人认领过这一条已路由回复」，没人认领就记下损失。
func (r *RelayOutbound) settle(ctx context.Context, p pendingOutcome) {
	held, err := r.dedupe.Held(ctx, p.key)
	if err != nil {
		// 读不了的占位存储两向都证明不了什么。在这里说「丢了」会把一次 Redis 抖动
		// 变成一整批幻影丢弃。
		r.logger.WarnContext(ctx, "wecom relay: could not settle a routed reply's outcome",
			"error", err, "installation_id", p.instID, "task_id", p.taskID)
		return
	}
	if held {
		return // 有人拿走了；计它的是拿走的那副本
	}
	r.mx().RecordOutboundDropped(string(dropNoConnection))
	r.logger.WarnContext(ctx, "wecom outbound: reply not delivered",
		"reason", string(dropNoConnection),
		"chat_session_id", p.sessionID,
		"installation_id", p.instID,
		"task_id", p.taskID,
		"detail", "routed to the replicas and none took the delivery: either no replica "+
			"held a live connection, or the one that did could not admit the frame "+
			"(see outbound_relay_shed_total)")
}

// awaitOutcome 为一条已路由回复报名参加上面的核查。绝不阻塞：调用方是总线订阅者
// 的 goroutine。
func (r *RelayOutbound) awaitOutcome(f relayFrame, eventID string) {
	if r.dedupe == nil {
		return
	}
	select {
	case r.verify <- pendingOutcome{
		key:       dedupeKey(eventID),
		sessionID: f.SessionID,
		instID:    f.InstallationID,
		taskID:    f.TaskID,
		dueAt:     time.Now().Add(r.outcomeGrace()),
	}:
	default:
		r.logger.Warn("wecom relay: outcome watch full, a routed reply's fate will go unrecorded",
			"installation_id", f.InstallationID, "task_id", f.TaskID)
	}
}

// WithRelay 把跨副本路由器挂到订阅者上。没有它，订阅者保持它从前的行为：离租约产
// 生的回复就地丢弃。
func WithRelay(r *RelayOutbound) OutboundOption {
	return func(o *Outbound) { o.relay = r }
}

// relayEventID 是每条占位的键所系的那个 id。从那一轮派生而非现造，所以同一完成事
// 件被重发——发布的重试、重放的流条目、第二个订阅者——是同一个占位，不可能变成会
// 话里的第二条消息。
func relayEventID(e events.Event, taskID pgtype.UUID) string {
	return "wecom:" + e.Type + ":" + util.UUIDToString(taskID)
}

// relayInboxEventID 对没有任务的收件箱推送是同样的规则。
func relayInboxEventID(itemID, recipientID string) string {
	return "wecom:inbox:" + itemID + ":" + recipientID
}

// deliverRelayed 执行一条由另一副本发布的投递。它是 relayHandler 那一半，跑在分发
// worker 上——绝不在分片读取循环上。
//
// 凡这里可读的东西，帧只带 id、不带 payload：附件行由本副本去取——它就是能发它们
// 的那个——且从不经 Redis 运送。
func (o *Outbound) deliverRelayed(ctx context.Context, f relayFrame) deliveryOutcome {
	instID, err := util.ParseUUID(f.InstallationID)
	if err != nil || !instID.Valid {
		return outcomeDone // 无法寻址；重试也无法让它可寻址
	}
	if o.senders == nil {
		return outcomeNotOurs
	}
	sender := o.senders.get(instID)
	if sender == nil {
		return outcomeNotOurs // 握着租约的副本会接它
	}
	if f.Content != "" {
		if err := sender.sendTextCtx(ctx, f.ChatID, f.ChatType, f.Content); err != nil {
			// 回复计数器是给 AGENT REPLIES 的——它们文档化的单位。路由到这里的一条
			// 收件箱推送不能动它们：同一条推送会随「哪个副本恰好握着 socket」而被计
			// 成已送达回复、被丢弃回复、或什么都不计，delivered/dropped 比例就会追
			// 踪 socket 的摆放而不是结局。
			if f.Kind == relayKindReply {
				if reason := unconfirmedReason(err); reason != "" {
					o.unconfirmedFor(ctx, f.SessionID, f.Kind, reason, err)
				} else {
					o.droppedFor(ctx, f.SessionID, f.Kind, classifyDrop(err), err)
				}
			} else {
				o.logger.WarnContext(ctx, "wecom relay: inbox push failed on the lease holder",
					"error", err, "installation_id", f.InstallationID)
			}
			// 只有「可证发生在写之前」的失败才释放占位。过了那一点的一切——一次已
			// 尝试的写、一直没来的裁定、等待中过期的 context——都可能已到达对端，
			// 在那里释放占位会把一次重试变成用户会话里的一条重复回答。
			if provablyNotSent(err) {
				return outcomeProvablyNotSent
			}
			return outcomeDone
		}
		if f.Kind == relayKindReply {
			o.delivered()
		}
	}
	if f.Kind == relayKindInbox {
		return outcomeDone
	}
	if f.CarriesFiles {
		o.deliverAttachmentsByID(f.MessageID, f.WorkspaceID, attachmentTarget{
			InstallationID: instID,
			ChatID:         f.ChatID,
			ChatType:       f.ChatType,
			SessionID:      f.SessionID,
		}, f.Content == "")
	}
	return outcomeDone
}

// ownsSocket 是占位前的所有权闸门。刻意做便宜：一次 map 读。
func (o *Outbound) ownsSocket(installationID string) bool {
	if o.senders == nil {
		return false
	}
	id, err := util.ParseUUID(installationID)
	if err != nil || !id.Valid {
		return false
	}
	return o.senders.get(id) != nil
}

// provablyNotSent 报告某个发送错误是否确凿发生在「任何字节能出去」之前。ws_sender
// 自己标出这条边界：写本身报的失败包着 errWriteAttempted、缺失的裁定是
// errAckTimeout、说出口的拒绝是 *wecomAPIError——三者都意味着对端可能已（拒绝则是
// 确已）看见这帧。裸 context 错误是有歧义的——request() 在写前检查和写后等待都会
// 返回一个——所以按「可能已送出」处理，代价是一次不被重试的投递而非一条重复。
func provablyNotSent(err error) bool {
	var apiErr *wecomAPIError
	switch {
	case err == nil:
		return false
	case errors.As(err, &apiErr):
		return false
	case errors.Is(err, errAckTimeout):
		return false
	case errors.Is(err, errWriteAttempted):
		return false
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return false
	default:
		return true
	}
}

var _ relayHandler = (*Outbound)(nil)

// itemIDOf 是收件箱条目自己的 id，这正是让一条被路由的推送幂等的东西：两个副本读
// 到同一帧重放时，把占位系在同一通知上、而不是系在它们各自读到它的那一刻。
func itemIDOf(item map[string]any) string {
	if s, _ := item["id"].(string); s != "" {
		return s
	}
	// 老 payload 不带 id。退回「类型 + 它所属任务」比 id 弱，但对一条通知仍然稳
	// 定——这正是占位需要的。
	t, _ := item["type"].(string)
	ref, _ := item["issue_id"].(string)
	return t + ":" + ref
}
