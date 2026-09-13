// refresh.go：ghsnapshot 的「写 / 调度」半边——出站工作队列与持久化。
// 负责：跨事件的去重与每 PR 单飞行、有界并发 worker 池、GitHub 限流退避、head-SHA
// 守卫的原子快照写入，以及三条触发面（webhook、页面访问、TTL 巡检）的统一入口。
// 未被决定（还在跑 CI / 可合并性未知）的开放 PR 会进入有界的 chase 重抓。
package ghsnapshot

import (
	"context"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// TxBeginner 是 manager 打开快照写入事务所需要的 pgx 池子集。*pgxpool.Pool 满足它。
type TxBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// address 是刷新单元，也是去重 / 单飞行键（验收标准 3）：一个
// (installation, owner, repo, number) 元组，可能扇出到跨多个 workspace 的若干
// github_pull_request 行。
type address struct {
	InstallationID int64
	Owner          string
	Repo           string
	Number         int32
}

// 默认调参。chase 窗口的退避按 30s → 1m → 2m → 5m 爬升并封顶在 5m；chase 在快照
// 决定、PR 关闭或到达 maxChaseAttempts 时停止——绝无界（验收标准 7）。TTL 巡检与页面
// 访问刷新负责回收任何被停掉的 chase 漏掉的东西。
var (
	defaultChaseBackoff  = []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute}
	defaultConcurrency   = 12
	defaultViewTTL       = 60 * time.Second
	defaultSweepTTL      = 10 * time.Minute
	defaultSweepInterval = 10 * time.Minute
	defaultSweepMaxRows  = int32(200)
	maxChaseAttempts     = 12
	queueBuffer          = 2048
)

// Manager 拥有出站 GitHub API 刷新管线。client 为 nil/禁用的 Manager 是惰性的：每个
// 触发方法都是空操作，因此没有 GitHub App 私钥的部署能把功能整体降级关闭，且不影响
// PR 关联、merge→Done 等任何既有行为（验收标准 4）。
type Manager struct {
	client    *Client
	queries   *db.Queries
	pool      TxBeginner
	onApplied func(ctx context.Context, prID pgtype.UUID)

	concurrency   int
	viewTTL       time.Duration
	sweepTTL      time.Duration
	sweepInterval time.Duration
	sweepMaxRows  int32
	chaseBackoff  []time.Duration
	now           func() time.Time
	jitter        func() time.Duration
	// fetch 是快照抓取器——一条接缝，让测试在无真实 GitHub 的情况下驱动队列/退避。
	// 默认是 FetchPRSnapshot。
	fetch func(ctx context.Context, c *Client, installationID int64, owner, repo string, number int32) (*PRSnapshot, error)

	queue chan address

	mu       sync.Mutex
	active   map[address]bool // 已排队或飞行中 → 合并（每 PR 单飞行）
	inFlight map[address]bool
	trailing map[address]bool // active 期间到达的一个事件；当前抓取结束后再补刷一次
	attempts map[address]int  // 当前未决窗口内的 chase 次数
	// 有界 TTL 巡检最后一次返回的 address。查询从这个游标之后开始并回绕，防止早期
	// address 反复失败时固定的第一页一直饿死后面的安装。
	sweepAfter address
	// 次级限流以「惹出它的那个安装」为作用域。一个客户绝不能把别的安装全部暂停。
	rateUntil map[int64]time.Time

	ctx     context.Context
	started bool
}

// NewManager 接好管线。onApplied 在每个 PR 行实际写入快照（守卫通过）时调用一次，
// 让 handler 能广播实时的 PR 更新。传 nil client 得到禁用的（空操作）manager。
func NewManager(client *Client, queries *db.Queries, pool TxBeginner, onApplied func(ctx context.Context, prID pgtype.UUID)) *Manager {
	return &Manager{
		client:        client,
		queries:       queries,
		pool:          pool,
		onApplied:     onApplied,
		concurrency:   defaultConcurrency,
		viewTTL:       defaultViewTTL,
		sweepTTL:      defaultSweepTTL,
		sweepInterval: defaultSweepInterval,
		sweepMaxRows:  defaultSweepMaxRows,
		chaseBackoff:  defaultChaseBackoff,
		now:           time.Now,
		jitter:        func() time.Duration { return time.Duration(rand.Int63n(int64(250 * time.Millisecond))) },
		fetch:         FetchPRSnapshot,
		queue:         make(chan address, queueBuffer),
		active:        map[address]bool{},
		inFlight:      map[address]bool{},
		trailing:      map[address]bool{},
		attempts:      map[address]int{},
		rateUntil:     map[int64]time.Time{},
	}
}

// Enabled 报告管线是否真的会干活。
func (m *Manager) Enabled() bool { return m != nil && m.client.Enabled() }

// Start 在 ctx 下启动 worker 池与 TTL 巡检。manager 被禁用时为空操作（且安全）。
func (m *Manager) Start(ctx context.Context) {
	if !m.Enabled() {
		return
	}
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return
	}
	m.started = true
	m.ctx = ctx
	m.mu.Unlock()

	for i := 0; i < m.concurrency; i++ {
		go m.worker(ctx)
	}
	go m.sweepLoop(ctx)
}

// Enqueue 为一个 PR address 安排一次刷新。重复事件会合并，但在 address 已排队或飞行
// 中到达的事件会留下一次「尾随刷新」。这个尾边很重要：synchronize 事件在旧 head 的请求
// 还在跑时推进了新 head，受守卫的旧响应被丢弃后，紧接着就抓新 head。每个 address 至多
// 一个请求在飞行、至多保留一次尾随请求。绝不阻塞调用方。
func (m *Manager) Enqueue(installationID int64, owner, repo string, number int32) {
	if !m.Enabled() {
		return
	}
	addr := address{InstallationID: installationID, Owner: owner, Repo: repo, Number: number}
	m.mu.Lock()
	if m.active[addr] {
		if m.inFlight[addr] {
			m.trailing[addr] = true
		}
		m.mu.Unlock()
		return
	}
	m.active[addr] = true
	m.mu.Unlock()

	select {
	case m.queue <- addr:
	default:
		// 队列饱和：丢弃并让 TTL 巡检 / 下一次事件恢复，而不是阻塞 webhook handler。
		// 取消标记，使其可以被重新入队。
		m.mu.Lock()
		delete(m.active, addr)
		delete(m.inFlight, addr)
		delete(m.trailing, addr)
		m.mu.Unlock()
		slog.Warn("ghsnapshot: refresh queue full, dropping enqueue")
	}
}

// MaybeEnqueueOnView 是页面访问触发：只有快照缺失或比 view TTL 更旧时才刷新——打开一张
// 已有新鲜数据的卡片不会产生任何开销。
func (m *Manager) MaybeEnqueueOnView(installationID int64, owner, repo string, number int32, fetchedAt time.Time, hasFetched bool) {
	if !m.Enabled() {
		return
	}
	if hasFetched && m.now().Sub(fetchedAt) < m.viewTTL {
		return
	}
	m.Enqueue(installationID, owner, repo, number)
}

// worker 是 worker 池的常驻循环：从队列拿 address，处理限流暂停，然后执行一次抓取。
func (m *Manager) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case addr := <-m.queue:
			// 被限流的安装在 worker 池外等待。让定时器持有期间 address 保持 active，
			// 这样事件继续合并，又不会让一个租户占满所有全局 worker。
			if pause := m.rateLimitPause(addr.InstallationID); pause > 0 {
				m.deferActive(ctx, addr, pause)
				continue
			}
			m.mu.Lock()
			m.inFlight[addr] = true
			m.mu.Unlock()
			m.process(ctx, addr)
			m.finish(addr)
		}
	}
}

// process 为一个 address 执行一次完整刷新：抓快照 → 找到受影响的 PR 行 → 逐行守卫写入
// → 按需安排 chase。所有失败路径都保证不丢「最后已知快照」、不写错误数据。
func (m *Manager) process(ctx context.Context, addr address) {
	// 每次请求的随机抖动摊平突发。安装级限流等待在此之前已处理，因此不会占用 worker 槽。
	if j := m.jitter(); j > 0 {
		if !sleepCtx(ctx, j) {
			return
		}
	}

	snap, err := m.fetch(ctx, m.client, addr.InstallationID, addr.Owner, addr.Repo, addr.Number)
	if err != nil {
		var rl *RateLimitError
		if asRateLimit(err, &rl) {
			m.extendRateLimit(addr.InstallationID, rl.RetryAfter)
			// 不为一个持续被限流的安装创造无界重试循环。有界 TTL 巡检（只扫开放/draft
			// PR）或下一个 webhook/view 事件会在 Retry-After 之后把 address 交回来。
			return
		}
		// 瞬时/GitHub 失败：保留最后已知快照（行不被触碰，卡片显示的是过期数据，
		// 绝不是错误数据）。绝不记录任何秘密。下一次触发或 TTL 巡检会重试。
		slog.Warn("ghsnapshot: fetch failed", "owner", addr.Owner, "repo", addr.Repo, "number", addr.Number, "err", err.Error())
		return
	}

	rows, err := m.queries.ListGitHubPRRowsByAddress(ctx, db.ListGitHubPRRowsByAddressParams{
		InstallationID: addr.InstallationID,
		RepoOwner:      addr.Owner,
		RepoName:       addr.Repo,
		PrNumber:       addr.Number,
	})
	if err != nil {
		slog.Warn("ghsnapshot: list rows failed", "err", err.Error())
		return
	}

	anyApplied := false
	anyOpenApplied := false
	for _, row := range rows {
		applied, err := m.applySnapshot(ctx, row.ID, snap)
		if err != nil {
			slog.Warn("ghsnapshot: apply snapshot failed", "err", err.Error())
			continue
		}
		if !applied {
			continue
		}
		anyApplied = true
		if row.State == "open" || row.State == "draft" {
			anyOpenApplied = true
		}
		if m.onApplied != nil {
			m.onApplied(ctx, row.ID)
		}
	}

	// chase 决策。只有在「快照未决断 且 这个 head 上仍有开放 PR 行」时才继续追。若
	// 什么都没写进去（head 已经超过本响应，或 PR 没了），推进 head 的那个 webhook 早已
	// 把新 head 入队，所以在这里停下。
	if anyApplied && anyOpenApplied && !snap.Decided() {
		m.scheduleChase(addr)
	} else {
		m.mu.Lock()
		delete(m.attempts, addr)
		m.mu.Unlock()
	}
}

// rateLimitPause 返回某安装剩余的限流等待时长；已过期则清掉记录返回 0。
func (m *Manager) rateLimitPause(installationID int64) time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	until, ok := m.rateUntil[installationID]
	if !ok {
		return 0
	}
	pause := until.Sub(m.now())
	if pause <= 0 {
		delete(m.rateUntil, installationID)
		return 0
	}
	return pause
}

// extendRateLimit 记录某安装的限流截至时刻（取更晚者），使并发失败的请求不会互相缩短
// 退避。
func (m *Manager) extendRateLimit(installationID int64, retryAfter time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	until := m.now().Add(retryAfter)
	if until.After(m.rateUntil[installationID]) {
		m.rateUntil[installationID] = until
	}
}

// deferActive 在 delay 之后把被限流的 address 放回队列，且不占用 worker。定时器持有该
// address 期间 active 标记保持置位，重复触发仍会合并进这次已安排的抓取。
func (m *Manager) deferActive(ctx context.Context, addr address, delay time.Duration) {
	time.AfterFunc(delay, func() {
		if ctx.Err() != nil {
			m.release(addr)
			return
		}
		select {
		case m.queue <- addr:
		default:
			m.release(addr)
			slog.Warn("ghsnapshot: refresh queue full, dropping rate-limited enqueue")
		}
	})
}

// release 清除 address 的全部三个状态标记，令其可被重新入队。
func (m *Manager) release(addr address) {
	m.mu.Lock()
	delete(m.active, addr)
	delete(m.inFlight, addr)
	delete(m.trailing, addr)
	m.mu.Unlock()
}

// finish 在一个 worker 完成后释放 address，或者把合并出来的那一次尾随边转成下一次排队
// 刷新——绝不让两个 worker 同时持有同一个 address。
func (m *Manager) finish(addr address) {
	m.mu.Lock()
	if !m.trailing[addr] {
		delete(m.active, addr)
		delete(m.inFlight, addr)
		m.mu.Unlock()
		return
	}
	delete(m.trailing, addr)
	delete(m.inFlight, addr)
	m.mu.Unlock()

	select {
	case m.queue <- addr:
	default:
		// worker 刚消费了一个槽，饱和不太可能，但仍保持 webhook 路径非阻塞，让 TTL
		// 巡检来兜底。
		m.mu.Lock()
		delete(m.active, addr)
		delete(m.inFlight, addr)
		delete(m.trailing, addr)
		m.mu.Unlock()
		slog.Warn("ghsnapshot: refresh queue full, dropping trailing enqueue")
	}
}

// applySnapshot 为单个 PR 行执行「带 head-SHA 守卫的原子批量替换」：先守卫式 UPDATE
// 快照列，再对逐检查行做全量 DELETE + INSERT——全部在同一个事务里。当行的 head 已越过
// 快照的 head 时返回 applied=false（且什么都不写）（验收标准 1）。
func (m *Manager) applySnapshot(ctx context.Context, prID pgtype.UUID, snap *PRSnapshot) (bool, error) {
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	q := m.queries.WithTx(tx)

	rollup := pgtype.Text{}
	if snap.HasChecks {
		rollup = textOrNull(snap.RollupState)
	}
	n, err := q.UpdateGitHubPRSnapshot(ctx, db.UpdateGitHubPRSnapshotParams{
		ApiMergeable:        textOrNull(snap.Mergeable),
		ApiMergeStateStatus: textOrNull(snap.MergeStateStatus),
		ChecksRollupState:   rollup,
		HeadSha:             snap.HeadSHA,
		FetchedAt:           tsFromTime(m.now()),
		PrID:                prID,
	})
	if err != nil {
		return false, err
	}
	if n == 0 {
		// head 已推进——丢弃整个响应，包括逐检查行。什么都不写。
		return false, nil
	}
	if err := q.DeleteGitHubPRCheckRuns(ctx, prID); err != nil {
		return false, err
	}
	for i, c := range snap.Contexts {
		if err := q.InsertGitHubPRCheckRun(ctx, db.InsertGitHubPRCheckRunParams{
			PrID:            prID,
			HeadSha:         snap.HeadSHA,
			Ordinal:         int32(i),
			Name:            c.Name,
			Status:          c.Status,
			Conclusion:      textOrNull(c.Conclusion),
			DetailsUrl:      textOrNull(c.DetailsURL),
			IsStatusContext: c.IsStatusContext,
		}); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// scheduleChase 在当前退避步长后重新入队该 address。受 maxChaseAttempts 约束，让一个
// 永远 PENDING 的 CI 或卡住的可合并性结论不可能无限转下去（验收标准 7）。
func (m *Manager) scheduleChase(addr address) {
	m.mu.Lock()
	attempt := m.attempts[addr]
	if attempt >= maxChaseAttempts {
		delete(m.attempts, addr)
		m.mu.Unlock()
		return
	}
	m.attempts[addr] = attempt + 1
	idx := attempt
	if idx >= len(m.chaseBackoff) {
		idx = len(m.chaseBackoff) - 1
	}
	delay := m.chaseBackoff[idx]
	m.mu.Unlock()
	m.scheduleRetry(addr, delay)
}

// scheduleRetry 在 delay 后重新入队该 address，除非 manager 正在关闭。
func (m *Manager) scheduleRetry(addr address, delay time.Duration) {
	ctx := m.ctx
	time.AfterFunc(delay, func() {
		if ctx != nil && ctx.Err() != nil {
			return
		}
		m.Enqueue(addr.InstallationID, addr.Owner, addr.Repo, addr.Number)
	})
}

// sweepLoop 周期驱动一次 TTL 巡检。
func (m *Manager) sweepLoop(ctx context.Context) {
	ticker := time.NewTicker(m.sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.sweepOnce(ctx)
		}
	}
}

// sweepOnce 为每个「快照既过期又未决断」的开放 PR 入队一次刷新。受 sweepMaxRows 约束。
// 这是安全网：兜住基础分支在没有 pull_request webhook 的情况下变化导致的未决 PR，也兜
// 住部署期间任何被丢弃的 webhook。
func (m *Manager) sweepOnce(ctx context.Context) {
	m.mu.Lock()
	after := m.sweepAfter
	m.mu.Unlock()

	rows, err := m.queries.ListStaleUndecidedGitHubPRs(ctx, db.ListStaleUndecidedGitHubPRsParams{
		OlderThan:           tsFromTime(m.now().Add(-m.sweepTTL)),
		AfterInstallationID: after.InstallationID,
		AfterRepoOwner:      after.Owner,
		AfterRepoName:       after.Repo,
		AfterPrNumber:       after.Number,
		MaxRows:             m.sweepMaxRows,
	})
	if err != nil {
		slog.Warn("ghsnapshot: sweep query failed", "err", err.Error())
		return
	}
	if len(rows) > 0 {
		last := rows[len(rows)-1]
		m.mu.Lock()
		m.sweepAfter = address{
			InstallationID: last.InstallationID,
			Owner:          last.RepoOwner,
			Repo:           last.RepoName,
			Number:         last.PrNumber,
		}
		m.mu.Unlock()
	}
	for _, r := range rows {
		m.Enqueue(r.InstallationID, r.RepoOwner, r.RepoName, r.PrNumber)
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

// asRateLimit 探测 err 是否为 *RateLimitError，并把目标指针指过去。
func asRateLimit(err error, target **RateLimitError) bool {
	if rl, ok := err.(*RateLimitError); ok {
		*target = rl
		return true
	}
	return false
}

// textOrNull 把空串映射成 NULL 的 pgtype.Text，避免在列里存字面空串。
func textOrNull(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

// tsFromTime 把 time.Time 包装成 valid 的 pgtype.Timestamptz。
func tsFromTime(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

// sleepCtx 睡 d 时长或直到 ctx 被取消；被取消时返回 false。
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
