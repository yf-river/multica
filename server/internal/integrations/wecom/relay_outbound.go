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
	// Claim takes key for token, or re-takes it when key already holds
	// token — the same owner coming back after a Release whose result was
	// unknown. Atomic across processes. The token is what makes every other
	// operation here safe to retry: a re-sent command can never act on a
	// claim that a later owner has since taken.
	Claim(ctx context.Context, key, token string, ttl time.Duration) (bool, error)
	// Release gives back the claim held under token, for a delivery that
	// provably did not happen: a compare-and-delete. false with a nil error
	// means the key no longer holds token — the delete already landed, the
	// claim expired, or somebody else holds it now — and is not a failure.
	// A non-nil error means the OUTCOME IS UNKNOWN: the delete may or may
	// not have landed. The caller must not read it as either.
	Release(ctx context.Context, key, token string) (bool, error)
	// Settle marks the claim held under token as settled: its holder is about
	// to record the delivery's outcome. false means the key no longer holds
	// token — the publisher already resolved it as lost, or it expired — and
	// the holder must then record nothing, so the reply ends with one record.
	// Settling an already-settled claim reports true (a retry is safe).
	Settle(ctx context.Context, key, token string) (bool, error)
	// Resolve is the publisher's end-of-grace read, and its fence: a claim
	// still held by a token at that point is turned into claimLost in the
	// same operation, so a holder that comes back later finds its Settle
	// refused and records nothing. See RelayOutbound.settle.
	Resolve(ctx context.Context, key string) (claimState, error)
	// ClaimState is Resolve WITHOUT the fence: it reports what the key holds
	// right now and changes nothing. It is what an inbox publish polls while it
	// waits to see whether a replica took the delivery; Resolve would fence a
	// holder that is merely mid-delivery. See RelayOutbound.confirmInbox.
	ClaimState(ctx context.Context, key string) (claimState, error)
	// ClaimBudget is the longest ONE round trip above may take. The store
	// states it because the store enforces it: outcomeGrace pays this budget
	// once per offer, and a copy of the number kept by the dispatcher would be
	// free to drift from the timeout actually applied.
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
	// DeliveryBudget is the longest one delivery attempt may take once it
	// holds the claim, and it bounds the WHOLE logical delivery: the wait for
	// the target chat's turn, and every piece a long answer is split into with
	// its own ack wait. Zero means ackTimeout.
	//
	// One budget for all of it, because that is what the publisher's outcome
	// grace reserves for one offer (outcomeGrace) — a grace sized for one ack
	// while the delivery waits for several is a Resolve that fences a reply
	// its holder is still writing.
	//
	// The grace charges it PER OFFER, not once for the chain: an offer that
	// fails provably-unsent hands the claim back, so the next offer starts
	// with a budget of its own. Lowering it therefore shortens the grace
	// twelve-fold on the defaults, which is why tests that wait a grace out
	// shrink it along with the claim budget and the lease settle.
	DeliveryBudget time.Duration

	// Shards is how many independent queues carry frames, and it is a
	// concurrency bound rather than an ordering one: ordering comes from the
	// hash putting one installation on one queue, and installations that
	// collide on a shard DO wait behind each other. The number to size against
	// is therefore how many bots may be slow at once before a healthy one
	// queues behind them. Eight covers a deployment's worth of bots against
	// the one thing that makes a delivery slow — ackTimeout, 5s — while
	// keeping eight idle goroutines rather than one per installation.
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

	// AcceptanceWindow is how long an inbox publish waits to see a replica take
	// the delivery claim before it concludes that nobody holds that bot's
	// socket. Its bound is the frame's trip to every replica's dispatcher plus
	// one claim round trip (DedupeStore.ClaimBudget), NOT the re-offer chain
	// the reply path's outcomeGrace outlasts: an inbox frame nobody claimed has
	// no chain behind it, so waiting the chain out would only delay the
	// fallback. Zero takes defaultRelayAcceptanceWindow.
	AcceptanceWindow time.Duration

	// RetryBackoff is the FIRST wait between re-offers; the chain doubles from
	// there, capped at a quarter of LeaseSettle so a lease that lands early is
	// not sat out. Small on purpose: the common reason a re-offer succeeds is
	// that the move has already finished.
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
	// defaultRelayAcceptanceWindow matches defaultClaimBudget: one claim round
	// trip plus room for the bus fan-out and a dispatcher's turn. The two live
	// in different files on purpose — this is the relay's bound on how long it
	// will wait, that is the store's bound on how long one call may take.
	defaultRelayAcceptanceWindow = 2 * time.Second
)

// withDefaults fills the zero fields and returns the completed config.
// deliveryBudget is DeliveryBudget with its default applied.
func (c RelayConfig) deliveryBudget() time.Duration {
	if c.DeliveryBudget > 0 {
		return c.DeliveryBudget
	}
	return ackTimeout
}

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
	if c.AcceptanceWindow <= 0 {
		c.AcceptanceWindow = defaultRelayAcceptanceWindow
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
	Kind           string `json:"kind"` // relayKindReply | relayKindInbox | relayKindSeal
	InstallationID string `json:"installation_id"`
	ChatID         string `json:"chat_id"`
	ChatType       int    `json:"chat_type"`
	Content        string `json:"content"`
	// SealReason names the ending a relayKindSeal frame carries, instead of the
	// words for it. The words are the ROUND's, and the round is on the holder:
	// its locale was captured when its bubble was opened, so the holder is the
	// only replica that can say the sentence in the language the asker reads.
	SealReason   string `json:"seal_reason,omitempty"`
	TaskID       string `json:"task_id,omitempty"`
	MessageID    string `json:"message_id,omitempty"`
	WorkspaceID  string `json:"workspace_id,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
	CarriesFiles bool   `json:"carries_files,omitempty"`
}

const (
	relayKindReply = "reply"
	relayKindInbox = "inbox"
	// relayKindSeal ENDS A ROUND WITHOUT CARRYING WORDS, which is the one thing
	// a reply frame cannot express: every wordless ending — a cancellation, a
	// completion with nothing to say, an answer that is only files — had no way
	// to reach the replica holding the bubble, so off-lease it left a spinner
	// claiming work was still in progress for the rest of the protocol's
	// window. Nothing else ends it: the sweep writes no frame, OnSettled needs
	// an unbound round, and the next question opens its own bubble.
	//
	// It is NOT a reply with an empty body, and the difference is the point. A
	// reply whose round is gone falls through to an ordinary push; one
	// cancel-all click would then put 这次处理已取消 into every chat in the
	// deployment. A seal frame with no round does NOTHING AT ALL.
	relayKindSeal = "seal"
)

// relayHandler 在握着 socket 的那个副本上执行一次投递。*Outbound 实现它；这层间
// 接让本对象能在订阅者存在之前就注册给中继。
type relayHandler interface {
	deliverRelayed(ctx context.Context, f relayFrame) relayResult
	// ownsSocket answers "does this process hold that installation's live
	// connection right now". It gates the global claim — see perform.
	ownsSocket(installationID string) bool
}

// claimState is what Resolve reads off one claim key.
type claimState int

const (
	// claimAbsent — nobody holds it: never claimed, released, or expired.
	claimAbsent claimState = iota
	// claimHeld — a replica took it and has not recorded an outcome. Resolve
	// turns this into claimLost as it reports it.
	claimHeld
	// claimSettled — its holder recorded the outcome.
	claimSettled
	// claimLost — the publisher recorded it as lost.
	claimLost
)

// claimSettledValue and claimLostValue are what a key holds once it is no
// longer a token. A token never collides with them: it is hex and a slash.
const (
	claimSettledValue = "settled"
	claimLostValue    = "lost"
)

// relayResult is what one delivery attempt reports: whether the frame is
// finished, and — for a finished one — the record its holder owes. The record
// is separated from the delivery so it can be made AFTER the claim is settled:
// a holder whose Settle is refused (the publisher already counted the reply
// as lost) must record nothing, or the reply ends with two records.
type relayResult struct {
	outcome deliveryOutcome
	record  func()
}

// deliveryOutcome tells the dispatcher whether the claim may be released.
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

// relayPublish is what one trip through the cross-replica router produced.
//
// The bool it replaces could not tell "the bus took it" from "a replica took
// the delivery" from "the publish call itself failed", and an inbox push needs
// all three: only an accepted-and-claimed frame is a delivery it may stop
// making, and only a failed publish forbids it from trying another bot.
type relayPublish int

const (
	// relayNotWired — nothing reached the bus: no relay is attached, or the
	// frame could not even be built. A caller may treat it as a delivery that
	// certainly did not happen.
	relayNotWired relayPublish = iota
	// relayAccepted — the bus took the frame. For a reply or a seal that is the
	// whole answer. For an inbox push it means a replica that owns the target
	// bot's socket took the delivery claim, so somebody is carrying it.
	relayAccepted
	// relayNoHolder — the bus took the frame, but across the acceptance window
	// no replica took the delivery claim: nobody holds that installation's
	// socket, so every dispatcher discards the frame. Only an inbox push can
	// report this (see confirmInbox). The delivery provably did not happen.
	relayNoHolder
	// relayUncertain — the publish call failed, or the claim store could not
	// answer. The frame may or may not have been accepted, so nothing may be
	// concluded and nothing may be re-routed on this evidence.
	relayUncertain
)

// routed reports whether the bus took the frame, which is the whole question
// the reply and seal callers ask. They have no second route to fall back to,
// and a frame that may have been accepted is treated as not carried, so they
// keep their old local-send behaviour.
func (p relayPublish) routed() bool { return p == relayAccepted }

// seenEvents is a bounded, per-process set of relay event ids already acted on.
// It is a cheap first gate in front of the Redis claim — the publisher's own
// copy of its own frame is the common case and never needs a round trip. It is
// NOT the idempotency mechanism; DedupeStore is.
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

	// owner is this process's half of every claim token (tokenFor). Minted
	// once per RelayOutbound so a claim can tell its own re-offers from
	// another replica's, and nothing else.
	owner  string
	logger *slog.Logger
	seen   *seenEvents

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
		owner:     newReqID(),
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

// publish hands a delivery to the other replicas and reports what the bus did
// with it. See relayPublish for why the answer is not a bool.
//
// It NEVER waits: every caller of this one either has no second route to take
// when nobody holds the target bot, or is not in a position to take one. The
// single caller that is (an inbox push with the recipient-wide bot behind it)
// asks through publishAwaitingHolder instead, and pays the wait only there.
func (r *RelayOutbound) publish(f relayFrame, eventID string) relayPublish {
	return r.publishFrame(context.Background(), f, eventID, false)
}

// publishAwaitingHolder is publish plus the acceptance wait: it answers whether
// a replica owning the target bot's socket actually took the delivery, which is
// what an inbox push needs before it may put a second card in the member's chat
// through another bot. The wait is bounded by ctx, and by the acceptance window
// it configures -- see confirmInbox.
//
// WHY IT IS A SEPARATE ENTRY POINT. Bus delivery is synchronous on the caller's
// goroutine, and the publisher of an inbox:new is the HTTP request that created
// the comment; only one replica holds a given bot's socket, so this wait is the
// common path in a multi-replica deployment. Spending it where the answer is
// then discarded -- a member-authored push, which has no second route to try,
// or the fallback attempt itself, which has nothing behind it -- holds the
// request open for a verdict nobody can act on. Those callers keep publish.
func (r *RelayOutbound) publishAwaitingHolder(ctx context.Context, f relayFrame, eventID string) relayPublish {
	return r.publishFrame(ctx, f, eventID, true)
}

// publishFrame is the one implementation behind the two entry points.
func (r *RelayOutbound) publishFrame(ctx context.Context, f relayFrame, eventID string, awaitHolder bool) relayPublish {
	if r == nil || r.publisher == nil {
		return relayNotWired
	}
	body, err := json.Marshal(f)
	if err != nil {
		r.logger.Warn("wecom relay: marshal outbound frame", "error", err)
		return relayNotWired
	}
	if err := r.publisher.PublishWithID(realtime.ScopeWecomOutbound, f.InstallationID, "", body, eventID); err != nil {
		r.logger.Warn("wecom relay: publish failed",
			"error", err, "installation_id", f.InstallationID, "task_id", f.TaskID)
		// The bus call failed, but a publish whose RESPONSE was lost may still
		// have been accepted. Nothing may be concluded from it.
		return relayUncertain
	}
	// 一条已发布的回复自此欠某人一个结局。watchOutcomes 就是那个某人——为什么不
	// 能是别人见它的注释。
	if f.Kind == relayKindReply {
		r.awaitOutcome(f, eventID)
	}
	// Reply and seal frames are fire-and-forget: their callers read only "did
	// the bus take it", and counting a routed reply twice is what the outcome
	// watch exists to prevent. Only the inbox push asks whether a replica
	// actually took the delivery, because only it has a second route to fall
	// back to -- and only when the caller asked to wait.
	if !awaitHolder || f.Kind != relayKindInbox {
		return relayAccepted
	}
	if r.dedupe == nil {
		// No claim store means no relay in production (see DedupeStore); the
		// bus call is the only signal available, so take it.
		return relayAccepted
	}
	return r.confirmInbox(ctx, eventID)
}

// confirmInbox answers whether a replica took the delivery claim for an inbox
// frame the bus accepted, so the publisher can tell "somebody is carrying this"
// from "nobody holds that bot's socket" without waiting out a re-offer chain
// that will never exist.
//
// It reads the claim key for up to RelayConfig.AcceptanceWindow. A claim that
// appears means a replica owning the installation's socket engaged the
// delivery, and the relay's own guarantees (re-offers, the outcome record) take
// over from there. A key still absent at the end of the window means no replica
// even took it — the frame is handed to each replica exactly once and only a
// socket holder claims — so none ever will. The publisher then FENCES the key
// under a token no delivery worker presents (fenceTokenFor) and reports
// relayNoHolder, which is what lets the caller
// route the notification somewhere else. The fence is what keeps that safe: a
// dispatcher that is late, or a replica replaying the frame across a restart,
// finds the claim taken and delivers nothing -- on the publishing replica too,
// which is the one a fence under its own delivery token would NOT have stopped.
// See fenceTokenFor.
//
// A claim store that cannot answer is relayUncertain: the delivery might be
// happening, and the caller must not route a second copy on that. So is a
// caller whose own budget ran out mid-wait -- the wait is bounded by ctx as
// well as by the window, because this runs on a request's goroutine.
func (r *RelayOutbound) confirmInbox(ctx context.Context, eventID string) relayPublish {
	window := r.cfg.AcceptanceWindow
	// THE CALLER'S BUDGET IS THE OUTER BOUND. This wait runs on the goroutine
	// that is serving an HTTP request (see publishAwaitingHolder), so a caller
	// that runs out of time must take the wait with it rather than be held for
	// a window it configured elsewhere. A tighter caller deadline shortens the
	// window; with no deadline the window stands.
	deadline := time.Now().Add(window)
	callerDeadline, hasCallerDeadline := ctx.Deadline()
	if hasCallerDeadline && callerDeadline.Before(deadline) {
		deadline = callerDeadline
	}
	// The poll's OWN deadline ends the wait; the context is the backstop that
	// keeps a claim store which ignores its deadline from outlasting it, so it
	// gets a little more room than the loop. Were the two equal, the context
	// could fire in the same instant the last poll lands and the verdict would
	// rest on which select case won: reporting "uncertain" for a bot nobody
	// holds, and silently giving up the fallback.
	pollCtx, cancel := context.WithTimeout(ctx, time.Until(deadline)+window/8+time.Millisecond)
	defer cancel()
	key := dedupeKey(eventID)
	// Eight polls over whatever window actually applies, so a caller that
	// shortened the wait still samples it evenly rather than once at the start
	// and once past its own deadline.
	step := window / 8
	if effective := time.Until(deadline); effective < window {
		step = effective / 8
	}
	if step < time.Millisecond {
		step = time.Millisecond
	}
	for {
		state, err := r.dedupe.ClaimState(pollCtx, key)
		if err != nil {
			r.logger.WarnContext(pollCtx, "wecom relay: inbox claim state unreadable; the push is not re-routed",
				"error", err, "event_id", eventID)
			return relayUncertain
		}
		if state != claimAbsent {
			return relayAccepted
		}
		if !time.Now().Before(deadline) {
			break
		}
		timer := time.NewTimer(step)
		select {
		case <-timer.C:
		case <-pollCtx.Done():
			timer.Stop()
			return relayUncertain
		}
	}
	// A deadline can pass before its cancellation callback updates Err. An
	// expired caller must not fence the unconfirmed delivery or start a fallback.
	callerErr := ctx.Err()
	if callerErr == nil && hasCallerDeadline && !time.Now().Before(callerDeadline) {
		callerErr = context.DeadlineExceeded
	}
	if callerErr != nil {
		r.logger.WarnContext(ctx, "wecom relay: the caller's budget ran out before an inbox push's bot could be confirmed; not re-routing",
			"error", callerErr, "event_id", eventID)
		return relayUncertain
	}
	won, err := r.dedupe.Claim(pollCtx, key, r.fenceTokenFor(eventID), r.dedupeTTL)
	if err != nil {
		r.logger.WarnContext(pollCtx, "wecom relay: could not fence an unclaimed inbox delivery; the push is not re-routed",
			"error", err, "event_id", eventID)
		return relayUncertain
	}
	if !won {
		// A replica took the claim between the last read and the fence.
		return relayAccepted
	}
	r.logger.InfoContext(pollCtx, "wecom relay: no replica holds the inbox push's bot; its sender may route it elsewhere",
		"event_id", eventID)
	return relayNoHolder
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
	token := r.tokenFor(item.eventID)
	if r.dedupe != nil {
		won, err := r.dedupe.Claim(ctx, key, token, r.dedupeTTL)
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
	// DeliveryBudget is what the publisher's outcomeGrace charges per offer
	// (outcomeGrace, below), so it has to be what this delivery actually
	// gets. It was documented as the bound and never applied: the send's only
	// limit was ackTimeout, the constant, whatever the config said. An
	// operator who lowered the budget shrank the grace without shortening the
	// delivery, and a Resolve landing inside an ack wait fences a reply that
	// is on its way.
	//
	// A budget PER OFFER and not one for the chain, because this is where the
	// claim is taken and given back: an offer that ends provably-unsent
	// releases the claim a few lines down, and the next offer arrives here
	// and opens a fresh one.
	//
	// The default is ackTimeout, so a deployment that sets nothing sees no
	// change. A delivery cut here ends in a context error, which
	// unconfirmedReason reads as unknown rather than failed — correct when
	// the cut lands after the write, and marked as certain when it lands
	// before one (errNotAttempted, ws_sender.go).
	dctx, cancelDelivery := context.WithTimeout(ctx, r.cfg.deliveryBudget())
	res := r.handler.deliverRelayed(dctx, item.frame)
	cancelDelivery()
	if res.outcome == outcomeDone {
		// FINISHED. The holder's record is made only once the claim says
		// so: Settle is a compare-and-set on this replica's token, and a
		// refusal means the publisher already resolved the reply as lost
		// at the end of its grace — it has a record, so this one must not
		// be made. That is the whole reason record is a closure rather than
		// a counter moved inside deliverRelayed.
		if r.dedupe != nil && !r.settleClaim(ctx, key, token, item.frame) {
			return true
		}
		if res.record != nil {
			res.record()
		}
		return true
	}
	// Not ours, or provably never written: give the claim back — and offer it
	// again locally too, because release alone wakes nobody (see !won above).
	//
	// Release is a compare-and-delete on this replica's token, so it is safe
	// to retry and can never take a claim a later owner holds. Its three
	// answers are all "offer it again":
	//   - released: the next offer's Claim starts from an empty key;
	//   - not ours any more: the delete already landed, or another replica
	//     holds it now — the next Claim decides which;
	//   - error: UNKNOWN whether the delete landed. Nothing is recorded on
	//     that word. If the key still holds this token the next Claim
	//     re-takes it (same owner) and the delivery runs again; if the delete
	//     did land the next Claim takes it fresh, or loses it to a replica
	//     that got there first — which then delivers and records, once.
	// A claim that stays stranded under this token — every release erroring,
	// every re-offer failing — is what the publisher's Resolve finds at the
	// end of the grace: held, by nobody who recorded anything, and it
	// records the loss there, once.
	if r.dedupe != nil {
		if released, err := r.dedupe.Release(ctx, key, token); err != nil {
			r.logger.WarnContext(ctx, "wecom relay: claim release outcome unknown; the next offer re-claims",
				"error", err, "kind", item.frame.Kind,
				"installation_id", item.frame.InstallationID, "task_id", item.frame.TaskID)
		} else if !released {
			r.logger.DebugContext(ctx, "wecom relay: claim no longer ours at release",
				"kind", item.frame.Kind, "installation_id", item.frame.InstallationID, "task_id", item.frame.TaskID)
		}
	}
	r.seen.forget(item.eventID)
	return false
}

// claimSettleAttempts is how many times the holder asks the store to settle
// its claim before giving up. An error from Settle means UNKNOWN, exactly as
// it does for Release — and unlike Release there is no later offer to resolve
// it, because a settled delivery is finished. So the retry is what resolves
// it, and the operation is built to be retried: redisSettleSource is
// idempotent and token-fenced, so a retry after a settle that DID land
// matches the settled value and reports success, while one after a settle
// that never landed finds either the token (settles it now) or the
// publisher's fence (refused, and the holder correctly records nothing).
const claimSettleAttempts = 3

// settleRetryBackoff paces those attempts. The claim layer's own knob rather
// than a new one: it is the same "how long before asking the store again"
// question the re-offer chain asks.
func (r *RelayOutbound) settleRetryBackoff() time.Duration {
	if r.cfg.RetryBackoff > 0 {
		return r.cfg.RetryBackoff
	}
	return defaultRelayRetryBackoff
}

// settleClaim marks a delivered frame's claim as settled and reports whether
// its holder may record the outcome.
//
// False has two meanings, and only one of them is covered elsewhere.
//
// Covered: the publisher already resolved the reply as lost. Its record
// stands, and a second one here would double it.
//
// NOT always covered: every attempt came back unknown. The holder cannot tell
// which state the store is in. If the settles never landed the claim is still
// held by this token, and the publisher's Resolve ends the reply once — the
// common case, and the reason not to count here. But if a settle DID land and
// only its response was lost, the key already reads settled, so that Resolve
// stays quiet too and the reply ends with NO record at all. Recording here
// instead would turn the common case into the double count this whole path
// exists to prevent, so the miss is the side deliberately taken: a monitoring
// gap under a store that keeps failing, not a reply the user did not get. The
// warning below is what makes it visible.
func (r *RelayOutbound) settleClaim(ctx context.Context, key, token string, f relayFrame) bool {
	var (
		lastErr error
		made    int
	)
	for attempt := 0; attempt < claimSettleAttempts; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(r.settleRetryBackoff())
			select {
			case <-timer.C:
			case <-ctx.Done():
				// Shutdown does not excuse the settle: the frame is already
				// in the user's chat, and the store call itself runs on a
				// context of its own (dedupe_redis.go). Skip the pause, not
				// the attempt.
				timer.Stop()
			}
			if settleBudgetSpent(ctx) {
				break
			}
		}
		made++
		settled, err := r.dedupe.Settle(ctx, key, token)
		switch {
		case err != nil:
			lastErr = err
		case settled:
			return true
		default:
			r.logger.WarnContext(ctx, "wecom relay: delivered after the publisher resolved the reply as lost; not counted again",
				"kind", f.Kind, "installation_id", f.InstallationID, "task_id", f.TaskID)
			return false
		}
	}
	// Still unknown after every attempt this was allowed to make. See the
	// doc comment: the publisher ends the reply when the settles never
	// landed, and nothing ends it when one landed and only its answer was
	// lost. Which of the two happened is exactly what is not knowable here,
	// so the outcome is left unrecorded rather than double-recorded.
	r.logger.WarnContext(ctx, "wecom relay: delivered, but the claim could not be settled; this reply's outcome may go unrecorded",
		"error", lastErr, "attempts", made, "kind", f.Kind,
		"installation_id", f.InstallationID, "task_id", f.TaskID,
		"detail", "the delivery itself reached the chat; if the settle landed and only its "+
			"response was lost, the publisher's Resolve reads the claim as settled and stays "+
			"quiet as well, so no delivered/dropped/unconfirmed is counted for this reply")
	return false
}

// settleBudgetSpent reports whether a bounding DEADLINE on ctx has passed.
//
// A bare cancellation is not one. Shutdown interrupts the work, but the frame
// is already in the user's chat and its claim still has to be settled — which
// is why the store call drops cancellation (dedupe_redis.go). A deadline is
// the opposite case: drainRemaining bounds the WHOLE drain with one, and a
// retry chain that keeps opening attempts past it spends time the shutdown
// already promised away. Attempts already made stand; no new one begins.
func settleBudgetSpent(ctx context.Context) bool {
	deadline, ok := ctx.Deadline()
	return ok && !time.Now().Before(deadline)
}

// tokenFor is the owner token one claim is held under: this process, and the
// delivery. Stable across re-offers on this process, unique across replicas.
func (r *RelayOutbound) tokenFor(eventID string) string { return r.owner + "/" + eventID }

// fenceTokenFor is the token a no-holder fence is planted under, and it is
// deliberately NOT tokenFor. Claim answers "won" when the stored value equals
// the caller's token, so a fence planted under the publisher's own delivery
// token would be reclaimable by that replica's dispatcher the moment its
// socket came back -- the ordinary reconnect, not an exotic one -- and the
// frame it then delivered would be the second card the fallback had already
// replaced. The suffix makes the fenced value one no delivery worker ever
// presents, so the fence holds against every worker on every replica, the
// publishing one included. It is never released; the claim TTL retires it.
func (r *RelayOutbound) fenceTokenFor(eventID string) string {
	return r.tokenFor(eventID) + "/fence"
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
	// TWO round trips per offer, not one. Every offer makes its own Claim,
	// and every offer ends in a second call on the same budget: a Release
	// when it is owed another offer, a Settle when it is finished. Counting
	// only the Claim leaves half the store time out of the arithmetic, and
	// the absent state is deliberately NOT fenced by Resolve — a premature
	// expiry there would be recorded as a loss while a later offer could
	// still claim, deliver and settle, which is the contradiction this whole
	// path exists to remove. (Fencing absent would trade that miscount for a
	// claim no offer can take, i.e. a reply the user never gets: worse.)
	offers := len(r.retryPlan) + 1
	total := budget * time.Duration(2*offers)
	for _, d := range r.retryPlan {
		total += d
	}
	// The settle retry on the finished offer: its extra attempts and the
	// pauses between them (settleClaim).
	total += time.Duration(claimSettleAttempts-1) * (budget + r.settleRetryBackoff())
	// Plus a DELIVERY PER OFFER, not one for the chain. perform gives every
	// claimed delivery a budget of its own, and the failure that spends the
	// whole of one is also the failure that hands the claim back: an offer
	// whose chat is busy waits for the chat's turn until its budget runs out
	// and comes back errChatBusy, which is provablyNotSent, so the claim is
	// released and the frame is offered again with a fresh budget. Charging
	// one delivery to the chain therefore sized the grace for a chain that
	// cannot happen — every offer's backoff plus one offer's delivery — and
	// on the defaults that is 5s of grace against 60s the chain can spend.
	//
	// The Resolve that lands inside a live offer is the whole cost: it fences
	// the key as lost in the same operation, so the holder that comes back
	// records nothing while the counter already says the reply was dropped.
	// One reply, counted lost and delivered at once.
	//
	// The other direction — a delivery that outlives the budget perform gave
	// it — cannot happen: it IS the budget, applied to the context the
	// delivery runs on, so an answer split into several frames spends it
	// across all of them rather than taking an ack wait per piece.
	total += r.cfg.deliveryBudget() * time.Duration(offers)
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
	state, err := r.dedupe.Resolve(ctx, p.key)
	if err != nil {
		// 读不了的占位存储两向都证明不了什么。在这里说「丢了」会把一次 Redis 抖动
		// 变成一整批幻影丢弃。
		r.logger.WarnContext(ctx, "wecom relay: could not settle a routed reply's outcome",
			"error", err, "installation_id", p.instID, "task_id", p.taskID)
		return
	}
	switch state {
	case claimSettled:
		return // its holder recorded the outcome
	case claimLost:
		return // already resolved — a republished completion meets the same key
	case claimHeld:
		// A replica took it and never settled it: its release or its settle
		// was lost on the wire, or the process went with it. Resolve has
		// just fenced the key as lost, so a holder that comes back finds its
		// Settle refused and records nothing; this is the reply's one record.
		r.mx().RecordOutboundDropped(string(dropTransport))
		r.logger.WarnContext(ctx, "wecom outbound: reply not delivered",
			"reason", string(dropTransport),
			"chat_session_id", p.sessionID,
			"installation_id", p.instID,
			"task_id", p.taskID,
			"detail", "a replica claimed the delivery and never recorded an outcome: "+
				"its claim could not be released or settled, and nothing reached the chat")
		return
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

// WithRelay attaches the cross-replica router to the subscriber. Without it the
// subscriber keeps the behaviour it had: a reply produced off-lease is dropped
// where it stands.
func WithRelay(r noticeRouter) OutboundOption {
	return func(o *Outbound) { o.relay = r }
}

// relayEventID 是每条占位的键所系的那个 id。从那一轮派生而非现造，所以同一完成事
// 件被重发——发布的重试、重放的流条目、第二个订阅者——是同一个占位，不可能变成会
// 话里的第二条消息。
func relayEventID(e events.Event, taskID pgtype.UUID) string {
	return "wecom:" + e.Type + ":" + util.UUIDToString(taskID)
}

// relayInboxEventID is the same rule for an inbox push, which has no task. The
// installation is part of the id because one push is offered to two bots: the
// acting agent's, then the recipient-wide one. The publisher FENCES the claim
// of a bot nobody holds before it tries the other, and distinct ids are what
// keep that fence from also swallowing the route around it.
func relayInboxEventID(itemID, recipientID, installationID string) string {
	return "wecom:inbox:" + itemID + ":" + recipientID + ":" + installationID
}

// deliverRelayed 执行一条由另一副本发布的投递。它是 relayHandler 那一半，跑在分发
// worker 上——绝不在分片读取循环上。
//
// The frame carries identifiers, not payloads, for anything readable here: the
// attachment rows are fetched by this replica, which is the one that can send
// them, and are never shipped through Redis.
func (o *Outbound) deliverRelayed(ctx context.Context, f relayFrame) relayResult {
	// A SEAL FRAME IS ANSWERED BEFORE ANY OF THIS, and it is filtered by who
	// holds the ROUND rather than by who holds a socket. That is why it needs
	// no installation id: a cancellation deliberately does not chase an address
	// (typing_indicator.go), and a bubble is writable only on the replica that
	// painted it — so "do I have this round" asks the same question without a
	// database read.
	//
	// It writes no message, so none of the reply machinery below applies: no
	// counters move, no fallback push, and a round that is not here means the
	// frame did its whole job by doing nothing.
	if f.Kind == relayKindSeal {
		o.sealRelayedRound(ctx, f)
		return relayResult{outcome: outcomeDone}
	}
	instID, err := util.ParseUUID(f.InstallationID)
	if err != nil || !instID.Valid {
		return relayResult{outcome: outcomeDone} // unaddressable; a retry cannot make it addressable
	}
	if o.senders == nil {
		return relayResult{outcome: outcomeNotOurs}
	}
	sender := o.senders.get(instID)
	if sender == nil {
		return relayResult{outcome: outcomeNotOurs} // the replica holding the lease will take it
	}
	// record is the holder's one record for a finished reply, made by the
	// dispatcher after the claim is settled (perform). It moves the reply
	// counters only for AGENT REPLIES — their documented unit. An inbox push
	// routed here must not move them: the same push would otherwise count as
	// a delivered reply, a dropped reply, or nothing at all depending on
	// which replica happened to hold the socket.
	var record func()
	// hasVisibleChar, not `!= ""`, and the same predicate the local path uses
	// (outbound.go). A completion of "\n" carrying a file is words to neither
	// of them: the local path sends nothing and lets the file carry the
	// reply's outcome, and a frame routed here has to reach the same two
	// conclusions or which replica held the socket decides whether the user
	// sees a blank message and whether the text or the file is what the reply
	// counter is counting.
	// THE BUBBLE THIS REPLY BELONGS TO IS ON THIS REPLICA, so seal it rather
	// than pushing a second message underneath it. The replica that takes a
	// relayed reply is by definition the one holding the socket, and a bubble
	// is writable only where it was painted — which is that same replica. The
	// frame carries the task id for exactly this lookup.
	//
	// Replies only: an inbox push is not an answer to a round and must never
	// close one.
	// NOT GATED ON HAVING WORDS. An answer that is only files still ends the
	// round, and gating the take on hasVisibleChar left that round open: the
	// file arrived and the spinner above it kept turning. What the words should
	// be is decided AFTER the round is in hand, because the copy has to be in
	// the round's own locale and that was captured when its bubble was opened.
	spoke := false
	text := f.Content
	if f.Kind == relayKindReply && f.TaskID != "" {
		if sessionID, err := util.ParseUUID(f.SessionID); err == nil && sessionID.Valid {
			if t, _ := o.rounds().take(ctx, sessionID, byTask(f.TaskID)); t.HasBubble {
				if !hasVisibleChar(text) {
					text = wordlessSealCopy(t.Handle.Locale, f.CarriesFiles)
				}
				sealErr := o.finishStream(ctx, t.Handle, text)
				switch classifySeal(sealErr) {
				case sealOnScreen:
					record, spoke = o.delivered, true
				case sealUnknown:
					se := sealErr
					record = func() {
						o.unconfirmedFor(ctx, f.SessionID, f.Kind, unconfirmedSealReason(se), se)
					}
					spoke = true
				default:
					// Proof the words are not in the bubble. They still have to
					// reach the room, on a budget the seal cannot have spent.
					var cancel context.CancelFunc
					ctx, cancel = fallbackBudget(ctx)
					defer cancel()
				}
			}
		}
	}
	// text, not f.Content: when a wordless ending's seal was refused, the copy
	// that was going to close the bubble is what the room gets instead — the
	// same substitution the local path makes (outbound.go).
	if hasVisibleChar(text) && !spoke {
		if err := sender.sendTextCtx(ctx, f.ChatID, f.ChatType, text); err != nil {
			// WHETHER THIS FRAME IS FINISHED IS SETTLED BEFORE ANY COUNTER
			// MOVES. A frame that is owed another offer is still in flight,
			// and counting it here counts it once per attempt: the dispatcher
			// re-offers a provably-unsent frame across the whole retry chain
			// (RelayConfig.retryPlan is eleven entries on the production
			// defaults) and the publisher's watchOutcomes settles it once
			// more afterwards, so one reply lands on outbound_dropped twelve
			// or thirteen times — and if a later offer succeeds, on
			// outbound_delivered as well. That is precisely the "one reply
			// counted as delivered and dropped at the same time" defect shed's
			// comment above says this package no longer has.
			//
			// So the counters below record an outcome only for a frame that
			// will not be offered again; a frame still in flight is owed its
			// outcome by the single owner that can settle it after the fact —
			// the publisher's watchOutcomes, which counts a delivery nobody
			// took exactly once.
			//
			// Only a failure PROVEN to precede the write releases the claim.
			// Everything past that point — an attempted write, a verdict that
			// never came, a context that expired while waiting — may have
			// reached the peer, and releasing the claim there turns a retry
			// into a duplicate answer in the user's chat.
			if provablyNotSent(err) {
				o.logger.DebugContext(ctx, "wecom relay: nothing reached the wire, the frame is owed another offer",
					"error", err, "kind", f.Kind,
					"installation_id", f.InstallationID, "task_id", f.TaskID)
				return relayResult{outcome: outcomeProvablyNotSent}
			}
			sendErr := err
			if f.Kind == relayKindReply {
				// The same mapping the direct path uses. A partial send in
				// particular has to agree across the two, or one reply counts
				// as delivered or dropped depending on which replica held the
				// socket — see recordSend.
				record = func() { o.recordSend(ctx, f.SessionID, f.Kind, sendErr) }
			} else {
				record = func() {
					o.logger.WarnContext(ctx, "wecom relay: inbox push failed on the lease holder",
						"error", sendErr, "installation_id", f.InstallationID)
				}
			}
			return relayResult{outcome: outcomeDone, record: record}
		}
		if f.Kind == relayKindReply {
			record = o.delivered
		}
	}
	if f.Kind == relayKindInbox {
		return relayResult{outcome: outcomeDone}
	}
	if f.CarriesFiles {
		o.deliverAttachmentsByID(f.MessageID, f.WorkspaceID, attachmentTarget{
			InstallationID: instID,
			ChatID:         f.ChatID,
			ChatType:       f.ChatType,
			SessionID:      f.SessionID,
			// Same reason as the direct path in outbound.go: the failure runs
			// on a detached goroutine with no context left to read a profile
			// with, so the reader's language is resolved here. A relayed reply
			// is the one a multi-replica deployment takes most of the time —
			// without this, an English reader's file-failure notice arrives in
			// the deployment's language and nothing else differs.
			Locale: deploymentLocale(),
		}, !hasVisibleChar(f.Content))
	}
	return relayResult{outcome: outcomeDone, record: record}
}

// wordlessSealCopy is what closes a bubble for an ending that has no words of
// its own. One place, so a relayed round and a local one cannot say different
// sentences about the same outcome.
func wordlessSealCopy(locale Locale, carriesFiles bool) string {
	c := copyFor(locale)
	if carriesFiles {
		return c.StreamNoReplyWithFiles
	}
	return c.StreamNoReply
}

// sealReason names the ending a seal frame carries. The words are not shipped:
// they are the round's, and the round's locale is on the holder.
const (
	sealReasonCancelled = "cancelled"
	sealReasonNoReply   = "no_reply"
)

// sealRelayedRound closes a round on behalf of a replica that could not reach
// it, and does NOTHING when the round is not here — which is the reason this is
// its own frame kind rather than a reply with an empty body.
func (o *Outbound) sealRelayedRound(ctx context.Context, f relayFrame) {
	if f.TaskID == "" {
		return
	}
	sessionID, err := util.ParseUUID(f.SessionID)
	if err != nil || !sessionID.Valid {
		return
	}
	t, _ := o.rounds().take(ctx, sessionID, byTask(f.TaskID))
	if !t.HasBubble {
		// No bubble here. Nothing to close and nothing to say: this ending was
		// silent before the frame existed and stays silent.
		return
	}
	text := wordlessSealCopy(t.Handle.Locale, f.CarriesFiles)
	if f.SealReason == sealReasonCancelled {
		text = copyFor(t.Handle.Locale).StreamCancelled
	}
	if err := o.finishStream(ctx, t.Handle, text); err != nil {
		// A seal that cannot land leaves the bubble where it was. Saying the
		// words as a message instead is the local path's move for an ANSWER,
		// which the asker is waiting for; nobody is waiting on this one, and a
		// push here is the plain message this frame kind exists to avoid.
		o.logger.WarnContext(ctx, "wecom relay: could not seal a routed round's ending",
			"task_id", f.TaskID, "reason", f.SealReason, "error", err)
	}
}

// ownsSocket is the pre-claim ownership gate. Cheap by design: one map read.
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
	case errors.Is(err, errPartiallySent):
		// An answer past the cap goes out as several frames, and a failure on
		// the second says nothing about the first, which the user is already
		// reading. Retrying such a send would repeat what landed.
		return false
	case errors.As(err, &apiErr):
		return false
	case errors.Is(err, errAckTimeout):
		return false
	case errors.Is(err, errStreamBusy):
		// Nothing was written: the gate refused to put a frame out while the
		// server still owed a verdict on this req_id. Provably not sent is
		// what lets the answer go out once, by the plain route.
		return true
	case errors.Is(err, errStreamAckTimeout):
		// A stream frame whose verdict never came back is the same evidence as
		// errAckTimeout and has to be read the same way: the frame went to the
		// socket and the server said nothing. Missing here it fell to the
		// default and was reported as provably unsent, which is what let a
		// sealed bubble's answer go out a second time as a plain message.
		return false
	case errors.Is(err, errWriteAttempted):
		return false
	case errors.Is(err, errNotAttempted):
		// AHEAD of the context branch below, which this error also matches:
		// every not-attempted failure wraps the ctx.Err() that ended it. The
		// chat lock is taken and the context is checked before a frame is
		// built, so a delivery that ended at either point wrote nothing — and
		// these are the most retryable failures the path has. Reading them as
		// the ambiguous context error underneath settles the claim on a
		// message that was never offered to the socket.
		return true
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
