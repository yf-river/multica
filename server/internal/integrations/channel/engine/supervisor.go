package engine

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	mathrand "math/rand/v2"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/util"
)

// 角色定位：每个活跃安装一条常驻连接的**监督器**——它是 lark.Hub 的渠道无关泛化版。
// Hub 驱动飞书专属的 EventConnector；Supervisor 驱动 channel.Registry 构建的任意
// channel.Channel，并枚举**所有**渠道类型的安装而不只是 feishu。
//
// 多副本安全（本文件的核心命题）：通过 WS 租约 CAS 保证"全局至多一个 Supervisor 持有
// 某安装的租约"，因此跨副本重复消费事件不可能发生。租约值 = 进程级 nodeID + 每 super
// 的单调代号；每个变更都是一次 CAS（acquire/renew/release 只有 token 匹配才生效）。
// 断线重连用指数退避 + 抖动；凭据轮换（安装行指纹漂移）会让下一次 sweep 拆旧建新。
//
// 文件内部按"数据声明 → 配置与默认值 → 生命周期（Run/Wait）→ sweep → 单安装监督
// 循环（supervise）→ 租约细节 → 退避工具"组织。

// Installation 是 Supervisor 驱动一条连接所需的"某行 channel_installation"的渠道无关
// 视图。刻意保持最小：引擎从不直接读平台凭据——它把 Config 交给 registry 的 factory，
// 由 factory 解码所需内容；除"拿 ChannelType 挑 factory"外，它也从不针对渠道类型分支。
type Installation struct {
	// ID 是 channel_installation 主键。它是租约键、也是 supervisors 表的键
	//（每个 ID 一条 supervisor goroutine）。
	ID pgtype.UUID

	// ChannelType 挑出能构建这行 Channel 的 registry Factory（"feishu"、"slack"…）。
	ChannelType channel.Type

	// Fingerprint 把携带凭据的 config 浓缩成一条不透明字符串。两条指纹相等的行对
	// supervisor 来说可互换；两次 sweep 之间任何变化都会拆掉正在运行的连接并按新凭据
	// 重建——这样重装的渠道（换了 app_id/secret/region）会被拾起，而不是拿过期凭据
	// 无限跑下去。指纹由 store 计算；引擎只当它是不透明串。
	Fingerprint string

	// Config 是平台凭据/配置 JSON，原样传给 registry Factory 作为 channel.Config.Raw。
	// 引擎从不窥探它内部。
	Config json.RawMessage
}

// AcquireLeaseParams 给某安装的 WS supervisor 租约做围栏。store 执行 CAS：
// 无租约、已过期、或已被 Token 持有（续期）时放行；否则经 ErrLeaseNotAcquired 报告
// "租约在别处"。
type AcquireLeaseParams struct {
	ID        pgtype.UUID
	Token     string
	ExpiresAt time.Time
	TTL       time.Duration
}

// ReleaseLeaseParams 释放调用方仍持有的 WS supervisor 租约。store 必须以 Token 做围栏，
// 以免轮换前任的一次迟到释放把继任者刚拿到的租约打掉。
type ReleaseLeaseParams struct {
	ID    pgtype.UUID
	Token string
}

// ErrLeaseNotAcquired 是 store 在 TryAcquireWSLease 的 CAS 谓词不匹配时返回的哨兵——
// 即另一个副本（或本进程内轮换中尚未退出的前任）持有一条活租约。Supervisor 把它当作
// "还不是我们的，稍后再试"，区别于传输层错误。store 把后端"无行"信号翻译成它。
var ErrLeaseNotAcquired = errors.New("engine: ws lease held elsewhere")

// InstallationStore 枚举所有渠道类型的活跃安装。LeaseStore 刻意与之分离，这样生产环境
// 可以把安装元数据放 PostgreSQL、把低频变更的租约放 Redis。
type InstallationStore interface {
	// ListActiveInstallations 返回**全部**渠道类型的每个活跃安装。这里没有平台过滤——
	// 那个硬编码的 "feishu" 正是 MUL-3620 要消除的局限。
	ListActiveInstallations(ctx context.Context) ([]Installation, error)
}

// LeaseStore 拥有 token 围栏的、按安装区分的 WebSocket 租约。
type LeaseStore interface {
	// ListHeldWSLeases 返回当前有任何活跃属主的那些 ID。它只是 sweep 的优化；权威判断
	// 始终是 TryAcquireWSLease。
	ListHeldWSLeases(ctx context.Context, ids []pgtype.UUID) (map[string]struct{}, error)

	// TryAcquireWSLease 在租约不存在或已带相同 token 时放行（不确定响应的安全重试）；
	// 否则返回 ErrLeaseNotAcquired。
	TryAcquireWSLease(ctx context.Context, arg AcquireLeaseParams) error

	// RenewWSLease 只延长"当前值等于 Token"的租约。ErrLeaseNotAcquired 表示已失去
	// 所有权。
	RenewWSLease(ctx context.Context, arg AcquireLeaseParams) error

	// ReleaseWSLease 只删除"当前值等于 Token"的租约。
	ReleaseWSLease(ctx context.Context, arg ReleaseLeaseParams) error
}

// LeaseMetrics 是可选的、后端无关的可观测性缝。
type LeaseMetrics interface {
	RecordLeaseOperation(operation, outcome string)
	SetActiveLeaseOwners(count float64)
	SetOwnersWithRenewalErrors(count float64)
	SetLastSuccessfulRenewal(at time.Time)
	ObserveTakeoverLatency(delay time.Duration)
}

// Config 调 Supervisor 生命周期循环的各项参数。所有字段经 withDefaults 有合理的生产
// 默认值；测试通常只设 Now 和 Logger 注入确定性。
type Config struct {
	// LeaseTTL 是一次成功 AcquireWSLease 在"可被另一副本抢走"之前有效的时长。
	// 续期以更紧的 LeaseRenewInterval 进行；两者之间的空隙吸收瞬时的 DB 抖动。
	LeaseTTL time.Duration

	// LeaseRenewInterval 是 Supervisor 重新获取"已拥有的租约"的节拍。必须**显著小于**
	// LeaseTTL——漏掉一次续期也不至于立刻让出租约。
	LeaseRenewInterval time.Duration

	// PollInterval 是 Supervisor 扫描"该接管的安装"的频率（新安装、或在别的副本上租约
	// 已过期的安装）。
	PollInterval time.Duration

	// LeaseErrorRetryInterval 是续期遇传输错误后的快速重试节拍。LeaseExpirySafetyMargin
	// 从上次确认的 TTL 里扣掉——这样被分区切走的属主会在 Redis 能把租约授给继任者之前
	// 先断开。
	LeaseErrorRetryInterval time.Duration
	LeaseExpirySafetyMargin time.Duration

	// MinBackoff / MaxBackoff 界定每次安装的重连退避区间：从 MinBackoff 起步，连续失败
	// 一次翻倍（封顶 MaxBackoff），任何存活至少 ResetBackoffAfter 的连接都会把退避归零。
	MinBackoff        time.Duration
	MaxBackoff        time.Duration
	ResetBackoffAfter time.Duration

	// LeaseReleaseTimeout 限制单次 ReleaseWSLease 调用。释放跑在新 context 上（停机时
	// 父 ctx 早已被取消），若没有这个截止，冻结的连接池可能让停机无限挂起。超时后租约
	// 就靠下个副本上的自然 TTL 过期来收尾。
	LeaseReleaseTimeout time.Duration

	// DisconnectTimeout 限制一次连接结束后调用的 Channel.Disconnect，理由同
	// LeaseReleaseTimeout。
	DisconnectTimeout time.Duration

	// RotationWaitTimeout 限制一轮 sweep 等所有被取消的"凭据轮换前任"断开并释放租约的
	// 时长。整批共用一个共享截止，所以大量同时发生的轮换不会把一轮 sweep 拖慢 N 倍。
	RotationWaitTimeout time.Duration

	// ShutdownTimeout 限制父 ctx 取消后 Wait 等待 supervisor goroutine 的时间。Wait 自身
	// 不执行它；由调用方把该值传给 WaitWithTimeout。暴露出来让启动代码和测试共享同一默认。
	ShutdownTimeout time.Duration

	// Now 返回当前时间。测试注入用；生产用 time.Now。
	Now func() time.Time

	// Logger 可选；缺省 slog.Default。
	Logger *slog.Logger

	// LeaseMetrics 可选；nil 关闭 Prometheus 记录。
	LeaseMetrics LeaseMetrics
}

// 默认值具名且导出，不是出于私有偏好：其它包是**按它们定尺寸**的。企微跨副本分发器
// 会一直重新让出帧，直到租约搬完——这需要一整个 DefaultPollInterval；并在受
// DefaultShutdownTimeout 限制的停机内排空。把这两个数在那边复制一份，就会变成随时可能
// 与本文件漂移的第二份定义。
const (
	// DefaultLeaseTTL 是租约不续期时的有效时长。
	DefaultLeaseTTL = 180 * time.Second
	// DefaultPollInterval 是 Supervisor 扫描"它该持有的安装"的频率，也就是租约搬到
	// 另一副本需要的时间。
	DefaultPollInterval = 30 * time.Second
	// DefaultShutdownTimeout 限制 Wait 汇合各 per-installation goroutine 的时长。
	DefaultShutdownTimeout = 15 * time.Second
)

func (c Config) withDefaults() Config {
	if c.LeaseTTL == 0 {
		c.LeaseTTL = DefaultLeaseTTL
	}
	if c.LeaseRenewInterval == 0 {
		c.LeaseRenewInterval = min(60*time.Second, c.LeaseTTL/3)
	}
	if c.PollInterval == 0 {
		c.PollInterval = min(DefaultPollInterval, c.LeaseRenewInterval/2)
	}
	if c.LeaseErrorRetryInterval == 0 {
		c.LeaseErrorRetryInterval = min(5*time.Second, c.LeaseRenewInterval/4)
	}
	if c.LeaseExpirySafetyMargin == 0 {
		c.LeaseExpirySafetyMargin = min(5*time.Second, c.LeaseTTL/10)
	}
	if c.MinBackoff == 0 {
		c.MinBackoff = 2 * time.Second
	}
	if c.MaxBackoff == 0 {
		c.MaxBackoff = 60 * time.Second
	}
	if c.ResetBackoffAfter == 0 {
		c.ResetBackoffAfter = 60 * time.Second
	}
	if c.LeaseReleaseTimeout == 0 {
		c.LeaseReleaseTimeout = 5 * time.Second
	}
	if c.DisconnectTimeout == 0 {
		c.DisconnectTimeout = 5 * time.Second
	}
	if c.RotationWaitTimeout == 0 {
		c.RotationWaitTimeout = c.DisconnectTimeout + c.LeaseReleaseTimeout
	}
	if c.ShutdownTimeout == 0 {
		c.ShutdownTimeout = DefaultShutdownTimeout
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if err := c.Validate(); err != nil {
		panic(err)
	}
	return c
}

// Validate 强制"安全失败式续期"成立所需的时序不变量。解析部署配置的调用方可以在构造
// Supervisor 前先校验；NewSupervisor 也会拒绝无效的程序化配置。
func (c Config) Validate() error {
	if c.PollInterval <= 0 || c.LeaseRenewInterval <= 0 || c.LeaseTTL <= 0 {
		return errors.New("channel engine: lease intervals must be positive")
	}
	// 为什么必须 poll <= renew < ttl：poll 若是继任者观察的间隔而 renew 是属主的保持
	// 间隔，一旦 poll > renew，属主总能在继任者下手前续期 → 租约永远搬不走；
	// renew >= ttl 则一次漏续就可能让租约在自己续期前过期。
	if c.PollInterval > c.LeaseRenewInterval || c.LeaseRenewInterval >= c.LeaseTTL {
		return fmt.Errorf("channel engine: require poll <= renew < ttl (poll=%s renew=%s ttl=%s)", c.PollInterval, c.LeaseRenewInterval, c.LeaseTTL)
	}
	if c.LeaseErrorRetryInterval <= 0 {
		return errors.New("channel engine: lease error retry interval must be positive")
	}
	// 安全余量必须小于 ttl-renew：它是"属主把租约当到期"用的提前量，若大过整段续期
	// 窗口，属主会在还能续期时误判失约。
	if c.LeaseExpirySafetyMargin <= 0 || c.LeaseExpirySafetyMargin >= c.LeaseTTL-c.LeaseRenewInterval {
		return fmt.Errorf("channel engine: lease expiry safety margin must be positive and less than ttl-renew (margin=%s)", c.LeaseExpirySafetyMargin)
	}
	if c.RotationWaitTimeout < 0 {
		return errors.New("channel engine: rotation wait timeout cannot be negative")
	}
	return nil
}

// Supervisor 拥有"每个活跃安装一条常驻连接"的 per-installation supervisor goroutine，
// 覆盖所有渠道类型。它借 WS 租约 CAS 实施多副本安全规则——全局至多一个 Supervisor 持有
// 任一安装的租约，跨副本重复消费事件因此不可能。
//
// 它是 lark.Hub 的渠道无关泛化：Hub 驱动的是 ConnectorFactory 构建的飞书专属
// EventConnector；Supervisor 驱动 channel.Registry 构建的任意 channel.Channel，并枚举
// 所有渠道类型的安装而不是只枚举 feishu。
//
// 生命周期：
//
//	sup := NewSupervisor(installations, leases, registry, handler, engine.Config{})
//	go sup.Run(ctx)             // ctx 被取消时返回
//	... 触发 ctx 取消 ...
//	sup.Wait()                  // 汇合每个 per-installation goroutine
type Supervisor struct {
	store      InstallationStore
	leaseStore LeaseStore
	registry   *channel.Registry
	handler    channel.InboundHandler
	cfg        Config

	// nodeID 是每个进程的租约所有权 token。TryAcquireWSLease 把匹配的 token 当作
	// "这是我们自己，续期"，所以稳定的 nodeID 让续期不会在副本间乒乓。
	nodeID string

	mu sync.Mutex
	// supervisors 按 installation_id 索引每个在途 supervisor goroutine，连同构建该
	// Channel 时用的凭据指纹。当行的指纹漂移（重装）时，下一轮 sweep 拆掉连接并按新凭据
	// 重建。
	supervisors map[string]supervisorEntry
	// contendedSince 记录本节点第一次观察到外来属主的时间，以便无 per-ID 标签地度量
	// "成功过期接管"的延迟。
	contendedSince map[string]time.Time
	activeOwners   int
	// renewalErrors 是一个有界集合（只含活跃本地属主），用来暴露"部分续期失败"——
	// 全进程只有一张"最近成功"时间戳时，只要另有安装健康，这种失败会被掩盖掉。
	renewalErrors map[string]struct{}
	// supervisorGen 是 supervisorEntry 上单调代号计数器的源头。minting 新条目（初次
	// 启动或轮换重启）时在 mu 下自增。
	supervisorGen uint64
	wg            sync.WaitGroup
	stopped       bool
	stopChan      chan struct{}
}

// supervisorEntry 是 Supervisor 为每条运行中 goroutine 持有的 per-installation 状态。
// cancel 终止该 goroutine（级联到渠道拆除 + 租约释放）；fingerprint 是构建 Channel 时的
// 凭据快照，sweep 靠它检测生命周期中途的轮换；gen 是单调代号，让 goroutine 的收尾清理
// 能把自己的条目与"轮换路径已换入的继任条目"区分开。
type supervisorEntry struct {
	cancel      context.CancelFunc
	done        <-chan struct{}
	fingerprint string
	gen         uint64
}

// leaseCandidate 是 sweep 里等待尝试获取租约的候选。
type leaseCandidate struct {
	installation Installation
	// localRotation 标记该候选仍被本进程自己的前任卡着（在有界等待之后）——
	// 此时不应把等待计入"接管延迟"。
	localRotation bool
}

// rotationWait 是一次"等轮换前任退出"的等待。
type rotationWait struct {
	installationID string
	done           <-chan struct{}
}

// NewSupervisor 构造绑定到给定 store、channel registry 与共享入站 handler 的 Supervisor。
// handler 被注入 Supervisor 构建的每条 Channel（经 channel.Config.Handler）——入站流水线
// 只写一次、各平台共享。Supervisor 在 Run 之前不启动任何 goroutine。nil registry 或 store
// 是编程错误，会在 Run 时 panic。
func NewSupervisor(store InstallationStore, leaseStore LeaseStore, registry *channel.Registry, handler channel.InboundHandler, cfg Config) *Supervisor {
	cfg = cfg.withDefaults()
	return &Supervisor{
		store:          store,
		leaseStore:     leaseStore,
		registry:       registry,
		handler:        handler,
		cfg:            cfg,
		nodeID:         newNodeID(),
		supervisors:    make(map[string]supervisorEntry),
		contendedSince: make(map[string]time.Time),
		renewalErrors:  make(map[string]struct{}),
		stopChan:       make(chan struct{}),
	}
}

// NodeID 暴露进程级租约 token，供测试与可观测性用（运维可以把 DB 租约行关联回正在运行
// 的副本）。
func (s *Supervisor) NodeID() string { return s.nodeID }

// Run 是 Supervisor 的主循环。每 PollInterval 扫一遍安装：试图租用任何"当前不被本进程
// 监督"的安装，并收割"已被吊销或租约已丢"的安装的 supervisor。ctx 被取消时返回；调用方
// 随后**必须**调 Wait 汇合所有 supervisor goroutine 再退出。
func (s *Supervisor) Run(ctx context.Context) {
	defer close(s.stopChan)

	// 立即扫第一轮：刚重启的服务器不必等满一个 PollInterval 才接管自己的安装。
	s.sweep(ctx)

	t := time.NewTicker(s.cfg.PollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			s.cancelAll()
			return
		case <-t.C:
			s.sweep(ctx)
		}
	}
}

// Wait 阻塞直到 Supervisor 启动的每条 supervisor goroutine 都退出。请在取消 Run 的
// context **之后**调用。
//
// 它先等 Run 返回（stopChan 关闭），然后才汇合 supervisor WaitGroup。这个先后次序是
// 承重的：Run 是 startSupervisor 的唯一调用方（它执行 s.wg.Add(1)），而并发执行
// WaitGroup.Add 与 WaitGroup.Wait 是数据竞争（WaitGroup 契约下未定义）。Run 一返回就不
// 可能再有 Add，所以下面的 wg.Wait 无竞争。（Run 即使 panic 也会经 defer 关闭 stopChan；
// 调用方总是"先启动 Run + 取消 ctx"再配 Wait。）
//
// 停机路径上优先用 WaitWithTimeout——卡住的 supervisor（典型是冻结 DB 连接池上的租约
// 释放）不能无限阻塞进程退出。
func (s *Supervisor) Wait() {
	<-s.stopChan
	s.wg.Wait()
}

// WaitWithTimeout 是 Wait 的有界版。所有 supervisor goroutine 在截止前退出返回 true，
// 超时返回 false。超时后，被抛弃的 goroutine 由 OS 回收，未释放的租约在下一副本上按
// LeaseTTL 自然过期。timeout <= 0 回退成无界 Wait。
func (s *Supervisor) WaitWithTimeout(timeout time.Duration) bool {
	if timeout <= 0 {
		s.Wait()
		return true
	}
	done := make(chan struct{})
	go func() {
		s.Wait()
		close(done)
	}()
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-done:
		return true
	case <-t.C:
		return false
	}
}

// ShutdownTimeout 暴露配置的优雅停机截止，启动代码可以把它原样传给 WaitWithTimeout，
// 无需再推导一遍。
func (s *Supervisor) ShutdownTimeout() time.Duration { return s.cfg.ShutdownTimeout }

// sweep 枚举当前活跃安装，为本进程尚未监督的启动 supervisor；取消已吊销安装的
// supervisor；对"安装行轮换过凭据"的 supervisor 取消并就地替换，让新渠道拾起新行。
func (s *Supervisor) sweep(ctx context.Context) {
	rows, err := s.store.ListActiveInstallations(ctx)
	if err != nil {
		s.cfg.Logger.Warn("channel engine: list active installations failed", "error", err)
		return
	}
	active := make(map[string]struct{}, len(rows))
	candidates := make([]leaseCandidate, 0, len(rows))
	candidateIDs := make([]pgtype.UUID, 0, len(rows))
	rotationWaits := make([]rotationWait, 0)
	for _, row := range rows {
		// 跳过没有注册"每安装 Factory"的渠道类型。这类行由 Supervisor 之外驱动
		//（例如 Slack 的 app 级 Socket Mode 连接器为它**所有**安装持有一条部署级连接，
		// 所以每条 Slack 行只带出站凭据 + 路由、没有自己的连接）。没有这道守卫的话，
		// 监督循环会对这种行租约→Registry.Build 撞 ErrUnknownType→释放→永久退避，
		// 每行都在空转租约与日志。
		if _, ok := s.registry.Lookup(row.ChannelType); !ok {
			continue
		}
		id := uuidString(row.ID)
		active[id] = struct{}{}
		done, rotated := s.cancelOnRotation(id, row)
		if rotated {
			rotationWaits = append(rotationWaits, rotationWait{installationID: id, done: done})
		}
		if !s.isSupervised(id) {
			candidates = append(candidates, leaseCandidate{installation: row, localRotation: rotated})
			candidateIDs = append(candidateIDs, row.ID)
		}
	}
	// 收割"安装已不再活跃"（自上次 sweep 后吊销）的 supervisor：它在下一个边界退出、
	// 释放租约，goroutine 返回。
	s.mu.Lock()
	for id, entry := range s.supervisors {
		if _, stillActive := active[id]; !stillActive {
			entry.cancel()
			delete(s.supervisors, id)
		}
	}
	for id := range s.contendedSince {
		if _, stillActive := active[id]; !stillActive {
			delete(s.contendedSince, id)
		}
	}
	s.mu.Unlock()

	if len(candidates) == 0 {
		return
	}
	s.waitForRotations(ctx, rotationWaits)
	held, err := s.leaseStore.ListHeldWSLeases(ctx, candidateIDs)
	if err != nil {
		s.recordLeaseOperation("list", "error")
		s.cfg.Logger.Warn("channel engine: list held leases failed; acquisition sweep skipped", "error", err)
		return
	}
	s.recordLeaseOperation("list", "success")
	for _, candidate := range candidates {
		row := candidate.installation
		id := uuidString(row.ID)
		if _, occupied := held[id]; occupied {
			if !candidate.localRotation {
				s.markContended(id)
			}
			continue
		}
		s.startSupervisor(ctx, row)
	}
}

func (s *Supervisor) isSupervised(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.supervisors[id]
	return ok
}

// cancelOnRotation 在某条现有 supervisor 的指纹与当前行不一致时取消它，并返回它的完成
// 信号。sweep 先取消每个被轮换的前任，再在一个有界截止下等整批结束，然后才查 held 键——
// 及时取消的前任会释放它的键，让继任者能在同一轮 sweep 里接上。
func (s *Supervisor) cancelOnRotation(id string, row Installation) (<-chan struct{}, bool) {
	want := row.Fingerprint
	s.mu.Lock()
	entry, ok := s.supervisors[id]
	if !ok || entry.fingerprint == want {
		s.mu.Unlock()
		return nil, false
	}
	s.cfg.Logger.Info("channel engine: credentials rotated, restarting supervisor",
		"installation_id", id,
		"channel_type", string(row.ChannelType),
	)
	entry.cancel()
	delete(s.supervisors, id)
	s.mu.Unlock()
	return entry.done, true
}

// waitForRotations 在一个共享截止（RotationWaitTimeout）下等待所有被取消的轮换前任
// 退出。
func (s *Supervisor) waitForRotations(ctx context.Context, waits []rotationWait) {
	if len(waits) == 0 {
		return
	}
	deadline := time.Now().Add(s.cfg.RotationWaitTimeout)
	for _, wait := range waits {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			s.cfg.Logger.Warn("channel engine: timed out waiting for credential rotations to stop",
				"timeout", s.cfg.RotationWaitTimeout.String(),
			)
			return
		}
		t := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-wait.done:
			t.Stop()
		case <-t.C:
			s.cfg.Logger.Warn("channel engine: timed out waiting for rotated supervisor to stop",
				"installation_id", wait.installationID,
				"timeout", s.cfg.RotationWaitTimeout.String(),
			)
			return
		}
	}
}

// startSupervisor 在父 context 下启动一条新的 supervisor goroutine 并登记条目。
func (s *Supervisor) startSupervisor(parent context.Context, inst Installation) {
	id := uuidString(inst.ID)
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	if _, exists := s.supervisors[id]; exists {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	s.supervisorGen++
	gen := s.supervisorGen
	s.supervisors[id] = supervisorEntry{
		cancel:      cancel,
		done:        done,
		fingerprint: inst.Fingerprint,
		gen:         gen,
	}
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		// supervisor 可能在没被显式取消时退出（租约被占或获取失败）。goroutine 返回时
		// 总是把它的子 context 从 Run 的长命父 context 上解绑。
		defer cancel()
		s.supervise(ctx, inst, id, gen, done)
	}()
}

// leaseToken 组合每个 supervisor 的租约 token：进程级 nodeID（供跨副本观测）配 supervisor
// 的 gen——这样**同一进程内**为同一安装前后接力跑的两条 supervisor（轮换路径）持不同的
// token。这一区别阻止旧 supervisor 在取消后的一次迟到 release 用 CAS 匹配并删掉继任者
// 刚拿到的租约。
//
// 结果是内部 CAS 标记，**不是**凭据：从不会发给任何平台，单独持有它也不授任何权（只有
// 能直写 Redis/DB 的人才能拿它做动作）。它仍不进日志字段——GH #7132 曾把明文
// `lease_token=` 字段报成渠道凭据泄漏，而每次有人在日志里读到它都要花一整轮排查才能
// 证伪。supervise() 改记 node_id + lease_gen，两者携带同样的诊断信息、但名字读起来不
// 像机密。
func leaseToken(nodeID string, gen uint64) string {
	return nodeID + "-g" + strconv.FormatUint(gen, 10)
}

// supervise 负责一条安装的连接生命周期。它循环：获取租约 → 构建 Channel → 跑它
// （Connect 阻塞）→ 运行期间续租 → 退出时释放 + 退避 → 重复。ctx 被取消时返回。
func (s *Supervisor) supervise(ctx context.Context, inst Installation, id string, gen uint64, done chan<- struct{}) {
	defer close(done)
	defer func() {
		// 只在条目仍属于自己时才清理——gen 区分"这个条目是我的"与"轮换路径已经用新
		// supervisor 换掉了我"。
		s.mu.Lock()
		if entry, ok := s.supervisors[id]; ok && entry.gen == gen {
			delete(s.supervisors, id)
		}
		s.mu.Unlock()
	}()

	leaseTok := leaseToken(s.nodeID, gen)
	log := s.cfg.Logger.With(
		"installation_id", id,
		"channel_type", string(inst.ChannelType),
		"node_id", s.nodeID,
		"lease_gen", gen,
	)
	backoff := s.cfg.MinBackoff

	for {
		if ctx.Err() != nil {
			return
		}

		// 落败的候选直接退出。下一轮成批 sweep 观察到 Redis、只在键消失后重新尝试——
		// 避免每副本为单条安装做盲目重试循环。
		leased, confirmedUntil, err := s.acquireLease(ctx, inst.ID, leaseTok)
		if err != nil {
			s.recordLeaseOperation("acquire", "error")
			log.Warn("channel engine: acquire lease error", "error", err)
			return
		}
		if !leased {
			s.recordLeaseOperation("acquire", "contended")
			s.markContended(id)
			return
		}
		s.recordLeaseOperation("acquire", "success")
		s.observeTakeover(id)
		s.setRenewalError(id, false)
		s.adjustActiveOwners(1)

		// 拿到租约：经 registry 构建平台 Channel，在子 context 下跑它，并并行续租。
		ch, err := s.registry.Build(channel.Config{
			Type:    inst.ChannelType,
			ID:      inst.ID,
			Raw:     inst.Config,
			Handler: s.handler,
		})
		if err != nil {
			log.Error("channel engine: build channel failed", "error", err)
			s.releaseLease(inst.ID, leaseTok)
			s.adjustActiveOwners(-1)
			if sleep(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff, s.cfg.MaxBackoff)
			continue
		}

		runCtx, runCancel := context.WithCancel(ctx)
		renewDone := make(chan struct{})
		go func() {
			defer close(renewDone)
			// renewLeaseUntil 在租约丢失时自己取消 runCtx，让 Channel 即使线缆 I/O 卡住
			// 也能退出——这正是"跨副本每安装至多一条活跃连接"在租约被偷时仍然成立的原因。
			s.renewLeaseUntil(runCtx, runCancel, inst.ID, leaseTok, confirmedUntil)
		}()

		startedAt := s.cfg.Now()
		runErr := ch.Connect(runCtx)
		runCancel()
		<-renewDone
		s.disconnect(ch, id, log)
		s.releaseLease(inst.ID, leaseTok)
		s.adjustActiveOwners(-1)

		if ctx.Err() != nil {
			return
		}

		// 连接存活得够久（"稳定"）就把退避归零，让单次迟到失败不至于从退避上限起步。
		uptime := s.cfg.Now().Sub(startedAt)
		if uptime >= s.cfg.ResetBackoffAfter {
			backoff = s.cfg.MinBackoff
		}
		if runErr != nil {
			log.Warn("channel engine: connection exited with error", "error", runErr, "uptime", uptime.String())
		} else {
			log.Info("channel engine: connection exited cleanly", "uptime", uptime.String())
		}
		if sleep(ctx, jitter(backoff)) {
			return
		}
		backoff = nextBackoff(backoff, s.cfg.MaxBackoff)
	}
}

// acquireLease 尝试认领或续期 WS 租约。调用后已拥有则返回 (true, nil)；租约在他处则
// (false, nil)；传输/DB 故障则 (false, err)。token 是每 supervisor 的 token
// （见 leaseToken），**不是**进程级 nodeID。
func (s *Supervisor) acquireLease(ctx context.Context, instID pgtype.UUID, token string) (bool, time.Time, error) {
	started := s.cfg.Now()
	expires := started.Add(s.cfg.LeaseTTL)
	err := s.leaseStore.TryAcquireWSLease(ctx, AcquireLeaseParams{
		ID:        instID,
		Token:     token,
		ExpiresAt: expires,
		TTL:       s.cfg.LeaseTTL,
	})
	if err == nil {
		return true, started.Add(s.cfg.LeaseTTL - s.cfg.LeaseExpirySafetyMargin), nil
	}
	if errors.Is(err, ErrLeaseNotAcquired) {
		return false, time.Time{}, nil
	}
	return false, time.Time{}, err
}

// renewLeaseUntil 以紧节拍重新获取租约，让"漏一次续期"不至于让出租约。ctx 被取消时退出。
// 租约丢失**必须**取消 Channel 的运行 context——否则 supervise 循环会先释放租约、而
// Channel 的收消息循环仍持续消费事件、直到线缆 I/O 终于报错——这正是"两个副本处理同一
// 安装"的故障模式。cancelRun 立刻顶住 Channel 的 ctx done，所以即使 socket 沉默，
// Connect 也会在有界时间内返回。token **必须**与获取时用的每 supervisor token 相同。
func (s *Supervisor) renewLeaseUntil(ctx context.Context, cancelRun context.CancelFunc, instID pgtype.UUID, token string, confirmedUntil time.Time) {
	nextDelay := renewalJitter(s.cfg.LeaseRenewInterval)
	for {
		remaining := confirmedUntil.Sub(s.cfg.Now())
		if remaining <= 0 {
			s.leaseLost(instID, cancelRun, "last confirmed lease expired")
			return
		}
		wait := min(nextDelay, remaining)
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		if !s.cfg.Now().Before(confirmedUntil) {
			s.leaseLost(instID, cancelRun, "last confirmed lease expired")
			return
		}

		started := s.cfg.Now()
		attemptCtx, attemptCancel := context.WithTimeout(ctx, confirmedUntil.Sub(started))
		err := s.leaseStore.RenewWSLease(attemptCtx, AcquireLeaseParams{
			ID: instID, Token: token, ExpiresAt: started.Add(s.cfg.LeaseTTL), TTL: s.cfg.LeaseTTL,
		})
		attemptCancel()
		if err != nil {
			if errors.Is(err, ErrLeaseNotAcquired) {
				s.leaseLost(instID, cancelRun, "lease token no longer matches")
				return
			}
			s.setRenewalError(uuidString(instID), true)
			s.recordLeaseOperation("renew", "error")
			s.cfg.Logger.Warn("channel engine: lease renewal error",
				"installation_id", uuidString(instID),
				"error", err,
				"confirmed_until", confirmedUntil,
			)
			nextDelay = s.cfg.LeaseErrorRetryInterval
			continue
		}
		confirmedUntil = started.Add(s.cfg.LeaseTTL - s.cfg.LeaseExpirySafetyMargin)
		s.setRenewalError(uuidString(instID), false)
		s.recordLeaseOperation("renew", "success")
		if s.cfg.LeaseMetrics != nil {
			s.cfg.LeaseMetrics.SetLastSuccessfulRenewal(s.cfg.Now())
		}
		nextDelay = renewalJitter(s.cfg.LeaseRenewInterval)
	}
}

func (s *Supervisor) leaseLost(instID pgtype.UUID, cancelRun context.CancelFunc, reason string) {
	s.setRenewalError(uuidString(instID), false)
	s.recordLeaseOperation("renew", "lost")
	s.cfg.Logger.Warn("channel engine: lease lost; tearing down connection",
		"installation_id", uuidString(instID), "reason", reason)
	cancelRun()
}

// releaseLease 写一次 token 围栏的释放，让下一个 supervisor（本进程或另一副本）不必等
// LeaseTTL 就能接手安装。它跑在新且有界的 context 上（停机时父 ctx 已取消）。token
// **必须**与获取时用的每 supervisor token 相同——轮换继任者的租约带着不同 token，迟到
// 的释放会空操作而非打掉它。
func (s *Supervisor) releaseLease(instID pgtype.UUID, token string) {
	s.setRenewalError(uuidString(instID), false)
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.LeaseReleaseTimeout)
	defer cancel()
	if err := s.leaseStore.ReleaseWSLease(ctx, ReleaseLeaseParams{
		ID:    instID,
		Token: token,
	}); err != nil {
		s.recordLeaseOperation("release", "error")
		s.cfg.Logger.Warn("channel engine: release lease failed",
			"installation_id", uuidString(instID),
			"error", err,
		)
		return
	}
	s.recordLeaseOperation("release", "success")
}

// recordLeaseOperation 透传一次租约操作指标（无指标器时为空操作）。
func (s *Supervisor) recordLeaseOperation(operation, outcome string) {
	if s.cfg.LeaseMetrics != nil {
		s.cfg.LeaseMetrics.RecordLeaseOperation(operation, outcome)
	}
}

// adjustActiveOwners 按 delta 调整本进程持有的活跃租约计数并上报指标。
func (s *Supervisor) adjustActiveOwners(delta int) {
	s.mu.Lock()
	s.activeOwners += delta
	count := s.activeOwners
	s.mu.Unlock()
	if s.cfg.LeaseMetrics != nil {
		s.cfg.LeaseMetrics.SetActiveLeaseOwners(float64(count))
	}
}

// setRenewalError 在续期错误集合里登记/撤销某安装，并只在状态真的变化时上报指标
// （避免每轮续期都刷一次 gauge）。
func (s *Supervisor) setRenewalError(id string, hasError bool) {
	s.mu.Lock()
	_, alreadySet := s.renewalErrors[id]
	if hasError {
		s.renewalErrors[id] = struct{}{}
	} else {
		delete(s.renewalErrors, id)
	}
	count := len(s.renewalErrors)
	s.mu.Unlock()
	if alreadySet == hasError {
		return
	}
	if s.cfg.LeaseMetrics != nil {
		s.cfg.LeaseMetrics.SetOwnersWithRenewalErrors(float64(count))
	}
}

// markContended 记下本节点第一次看到某安装被外来属主占用的时间（只记第一次，后续重复
// 忽略），供接管延迟度量。
func (s *Supervisor) markContended(id string) {
	s.mu.Lock()
	if _, ok := s.contendedSince[id]; !ok {
		s.contendedSince[id] = s.cfg.Now()
	}
	s.mu.Unlock()
}

// observeTakeover 在终于拿到曾被占用的租约时，清掉 contended 标记并上报接管延迟。
func (s *Supervisor) observeTakeover(id string) {
	s.mu.Lock()
	started, ok := s.contendedSince[id]
	if ok {
		delete(s.contendedSince, id)
	}
	s.mu.Unlock()
	if ok && s.cfg.LeaseMetrics != nil {
		s.cfg.LeaseMetrics.ObserveTakeoverLatency(s.cfg.Now().Sub(started))
	}
}

// disconnect 在 Connect 返回后、于一个新建有界 context 上拆除 Channel——卡死的 Disconnect
// 不能挂住 supervise 循环。到这里链路本身已断（Connect 已返回），所以这只是尽力而为的
// 资源清理。
func (s *Supervisor) disconnect(ch channel.Channel, id string, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.DisconnectTimeout)
	defer cancel()
	if err := ch.Disconnect(ctx); err != nil {
		log.Warn("channel engine: disconnect failed", "installation_id", id, "error", err)
	}
}

// cancelAll 取消每条在途 supervisor 并清空登记表（Run 退出路径）。
func (s *Supervisor) cancelAll() {
	s.mu.Lock()
	s.stopped = true
	for id, entry := range s.supervisors {
		entry.cancel()
		delete(s.supervisors, id)
	}
	s.mu.Unlock()
}

// newNodeID 返回本进程唯一的 16 字节十六进制随机串。会写进
// channel_installation.ws_lease_token；后续 acquire 时匹配的 token 当作续期（同一属主）。
func newNodeID() string {
	buf := make([]byte, 16)
	if _, err := cryptorand.Read(buf); err != nil {
		// crypto/rand 失败罕见且致命；与其在启动时 panic，不如退回时间戳派生的 token。
		return fmt.Sprintf("nodeid-fallback-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

// nextBackoff 把当前退避翻倍、封顶到 max。
func nextBackoff(cur, max time.Duration) time.Duration {
	next := cur * 2
	if next > max {
		return max
	}
	return next
}

// jitter 把重连分散到 [0.5d, 1.5d) 区间，避免大量安装在同一计时器边界同时重试
// （重连风暴）。
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	delta := d / 2
	return d - delta + time.Duration(mathrand.Int64N(int64(2*delta)+1))
}

// renewalJitter 用窄的 [0.9d, 1.1d] 区间，让各副本分散 Redis 调用而不侵蚀配置好的
// TTL 安全预算。
func renewalJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	delta := d / 10
	return d - delta + time.Duration(mathrand.Int64N(int64(2*delta)+1))
}

// sleep 是感知 ctx 的 time.Sleep。返回 true 当且仅当 ctx 在睡眠完成前被取消——调用方
// 可据此短路停机。
func sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() != nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return true
	case <-t.C:
		return false
	}
}

func uuidString(u pgtype.UUID) string { return util.UUIDToString(u) }
