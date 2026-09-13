// manager.go 实现调度器的「进程内运行主体」Manager：持有已注册任务与数据库连接池，
// 周期性 tick——对每个任务算出到期计划、用 tryClaim 抢租约、以心跳维持租约跑 Handler、
// 写回终态。Manager 自身不启动任何 goroutine，直到调用方以可取消的 context 调 Run。
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Options 配置一个 Manager。默认值在 NewManager 里补齐，所以对调用方而言所有字段
// 都可省略。
type Options struct {
	// RunnerID 在审计行里标识本进程。留空默认生成一个新 UUID——排查时足够可读，
	// 在副本间依然唯一。
	RunnerID string

	// TickInterval 是 manager 醒来评估「所有已注册任务中到期的计划」的频率。应当
	// 小于最短的任务 cadence；默认 30 * time.Second。
	TickInterval time.Duration

	// Logger 用于结构化日志。nil 默认用 slog.Default()。
	Logger *slog.Logger
}

// Manager 是每进程一个的调度器。注册一个或多个任务后，用可取消的 context 调 Run。
type Manager struct {
	pool   *pgxpool.Pool
	opts   Options
	jobs   map[string]*JobSpec
	mu     sync.RWMutex
	logger *slog.Logger
}

// NewManager 构造一个 Manager。pool 必须指向含 sys_cron_executions 表的数据库。
// 在 Run 被调用前，manager 不会启动任何 goroutine。
func NewManager(pool *pgxpool.Pool, opts Options) *Manager {
	if opts.RunnerID == "" {
		opts.RunnerID = uuid.NewString()
	}
	if opts.TickInterval <= 0 {
		opts.TickInterval = 30 * time.Second
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Manager{
		pool:   pool,
		opts:   opts,
		jobs:   make(map[string]*JobSpec),
		logger: opts.Logger.With("component", "scheduler", "runner_id", opts.RunnerID),
	}
}

// Register 把一个任务加进 manager。必须在 Run 之前调用；之后的注册也会被接受，
// 只是新任务要到下一轮循环迭代才会被 tick。
func (m *Manager) Register(job JobSpec) error {
	if err := job.validate(); err != nil {
		return err
	}
	spec := job
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.jobs[spec.Name]; exists {
		return fmt.Errorf("scheduler: duplicate job name %q", spec.Name)
	}
	m.jobs[spec.Name] = &spec
	return nil
}

// snapshot 返回一份已注册 spec 的副本，让循环迭代时不用一直持锁。
func (m *Manager) snapshot() []*JobSpec {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*JobSpec, 0, len(m.jobs))
	for _, j := range m.jobs {
		out = append(out, j)
	}
	return out
}

// Run 阻塞直到 ctx 被取消，期间每 Options.TickInterval 就 tick 一次。返回 ctx 终止的
// 原因（通常是 context.Canceled）。
func (m *Manager) Run(ctx context.Context) error {
	m.logger.Info("scheduler starting",
		"tick_interval", m.opts.TickInterval.String(),
		"jobs", len(m.snapshot()))

	// 刚启动就立刻先 tick 一次，避免全新进程干等一个完整间隔；之后退到 TickInterval
	// 的频率。
	if err := m.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
		m.logger.Warn("scheduler tick error", "error", err)
	}

	t := time.NewTicker(m.opts.TickInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			m.logger.Info("scheduler stopped", "reason", ctx.Err())
			return ctx.Err()
		case <-t.C:
			if err := m.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				m.logger.Warn("scheduler tick error", "error", err)
			}
		}
	}
}

// RunOnce 对所有已注册任务执行一次 tick。对测试、一次性 CLI、以及任何想在不独占
// goroutine 的情况下驱动调度器的调用方开放。
func (m *Manager) RunOnce(ctx context.Context) error {
	now, err := dbNow(ctx, m.pool)
	if err != nil {
		return err
	}
	for _, job := range m.snapshot() {
		if err := m.runJob(ctx, job, now); err != nil {
			m.logger.Warn("job tick error",
				"job", job.Name, "error", err)
		}
	}
	return nil
}

// runJob 遍历单个任务的作用域，并按追赶模式逐个处理到期的计划。
func (m *Manager) runJob(ctx context.Context, job *JobSpec, now time.Time) error {
	scopes, err := job.Scopes(ctx, now)
	if err != nil {
		return fmt.Errorf("scheduler: scope provider for %q: %w", job.Name, err)
	}

	// 在做计划前，先把被遗弃的 RUNNING 租约收口。这一步对每个任务都执行，无论
	// AllowStaleReentry 是什么，原因在于：
	//
	//   * 不可重入任务（AllowStaleReentry=false）需要 FAILED 审计行 + 告警——这是
	//     本步的原始动机。
	//
	//   * 可重入任务（AllowStaleReentry=true）在 `latest_only` 模式下从不去重新认领
	//     历史 plan_time，所以崩溃 pod 遗留的卡死 RUNNING 行会一直留在表里，把
	//     `scheduler_running_stale_total > 0` 钉死。把它标成 FAILED 既让仪表保持真实，
	//     又（因为同 plan_time 上 tryClaim 的 retry-from-FAILED 分支在还有尝试次数时
	//     仍可触发）保留了重试路径。
	//
	//   * 可重入 `every_plan` 任务本可以靠 tryClaim 的 stale-steal 分支——但那个分支
	//     只有在「同一 plan_time 再次被尝试」时才触发，而这与本清扫互相竞争但无妨：
	//     无论谁赢，该行都会离开 RUNNING。
	//
	// MUL-2957 评审：见张大彪的 blocker #1。
	if affected, err := markStaleAsFailed(ctx, m.pool, job.Name, now); err != nil {
		m.logger.Warn("scheduler: mark stale failed",
			"job", job.Name, "error", err)
	} else if affected > 0 {
		m.logger.Warn("scheduler: closed out abandoned RUNNING leases",
			"job", job.Name,
			"rows", affected,
			"reentrant", job.AllowStaleReentry)
	}

	for _, scope := range scopes {
		plans, err := m.plansForTick(ctx, job, scope, now)
		if err != nil {
			m.logger.Warn("scheduler: plan computation",
				"job", job.Name, "scope", scope.String(), "error", err)
			continue
		}
		for _, planTime := range plans {
			m.processPlan(ctx, job, scope, planTime, now)
		}
	}
	return nil
}

// plansForTick 计算本次 tick 要尝试的计划时间列表，遵守追赶模式与边界。
func (m *Manager) plansForTick(
	ctx context.Context,
	job *JobSpec,
	scope Scope,
	now time.Time,
) ([]time.Time, error) {
	// 钩子驱动型任务（例如 Autopilot 的调度触发器——它们的 plan_time 来自每个触发器
	// 自己的 cron 表达式，而不是均匀的 Cadence 网格）完全绕过 Cadence 计划器。这里
	// 仍然读一下最近存储的计划，好让钩子决定是否从那里续跑；也仍然把 MaxPlansPerTick
	// 当作钩子返回列表的安全上限。
	if job.PlansForScope != nil {
		info, err := latestPlan(ctx, m.pool, job.Name, scope)
		if err != nil {
			return nil, err
		}
		plans, err := job.PlansForScope(ctx, scope, now, info)
		if err != nil {
			return nil, fmt.Errorf("scheduler: plans hook for %q: %w", job.Name, err)
		}
		if job.MaxPlansPerTick > 0 && len(plans) > job.MaxPlansPerTick {
			plans = plans[:job.MaxPlansPerTick]
		}
		return plans, nil
	}

	eligible := now.Add(-job.ScheduleDelay)
	latest := FloorPlan(eligible, job.Cadence)
	if latest.After(eligible) {
		// Truncate 落到了未来——只在 cadence 极小、出现取整误差时发生；此刻还不到期。
		return nil, nil
	}

	switch job.CatchUpMode {
	case CatchUpLatestOnly:
		return []time.Time{latest}, nil

	case CatchUpEveryPlan:
		info, err := latestPlan(ctx, m.pool, job.Name, scope)
		if err != nil {
			return nil, err
		}
		// 确定仍算「在窗口内」的最早计划。
		oldestAllowed := now.Add(-job.CatchUpWindow)
		if job.CatchUpWindow <= 0 {
			oldestAllowed = latest
		}
		var start time.Time
		switch {
		case info.Found && info.RetryEligible(now):
			// info.PlanTime 处是 FAILED、还有剩余尝试次数、next_retry_at <= now。
			// 把游标留在同一个 plan_time 上，好让 tryClaim 的 retry-from-FAILED 分支
			// 接手；之后再向前推进经过那些可能也已到期的新计划。没有这个分支，游标会
			// 无条件跳到 PlanTime+cadence，把 FAILED 行晾在那里直到 max_attempts 被别处
			// 耗尽——而稳态下那永远不会发生。
			//
			// (MUL-2957 评审：见张大彪的 retry blocker。)
			start = info.PlanTime
		case info.Found:
			// 最近存储的计划是 SUCCESS、RUNNING，或已无剩余重试预算的 FAILED——越过它，
			// 免得对同一 plan_time 做无谓的重复插入尝试。
			start = info.PlanTime.Add(job.Cadence)
		default:
			// 该 (job, scope) 还没有历史。落到最近计划做引导。
			start = latest
		}
		if start.Before(oldestAllowed) {
			start = FloorPlan(oldestAllowed, job.Cadence)
			if start.Before(oldestAllowed) {
				start = start.Add(job.Cadence)
			}
		}
		var plans []time.Time
		for t := start; !t.After(latest) && len(plans) < job.MaxPlansPerTick; t = t.Add(job.Cadence) {
			plans = append(plans, t)
		}
		return plans, nil

	default:
		return nil, fmt.Errorf("scheduler: job %q: unknown catch_up_mode %v", job.Name, job.CatchUpMode)
	}
}

// (旧版包装已移除；plansForTick 把它的桶变量改名为 `latest` 后，之前的 latestPlan_
// shim 已不再需要。)

// processPlan 负责一次 (job, scope, plan_time) 尝试的完整流程：认领 → 带心跳跑
// Handler → 写终态。
func (m *Manager) processPlan(
	ctx context.Context,
	job *JobSpec,
	scope Scope,
	planTime time.Time,
	now time.Time,
) {
	c, err := tryClaim(ctx, m.pool, job, scope, planTime, now, m.opts.RunnerID)
	if err != nil {
		m.logger.Warn("scheduler claim error",
			"job", job.Name, "scope", scope.String(),
			"plan_time", planTime.Format(time.RFC3339), "error", err)
		return
	}
	if c.Conflicted && !c.Won && !c.Stole {
		// 别的 runner 已持有该计划，或它已是终态。静默无事可做是预期情形。
		return
	}
	if !c.Won && !c.Stole {
		// 防御性判断——理论上不可达，但能覆盖将来 SQL 改动新增第四条路径的情况。
		return
	}

	m.runClaimed(ctx, job, scope, planTime, c)
}

// runClaimed 对一条「已认领到的租约」运行 Handler，并把终态写回数据库。
func (m *Manager) runClaimed(
	ctx context.Context,
	job *JobSpec,
	scope Scope,
	planTime time.Time,
	c claim,
) {
	log := m.logger.With(
		"job", job.Name,
		"scope", scope.String(),
		"plan_time", planTime.Format(time.RFC3339),
		"attempt", c.Attempt,
		"execution_id", c.ID.String())

	if c.Stole {
		log.Info("scheduler stole stale lease")
	} else {
		log.Info("scheduler claimed plan")
	}

	// 处理器级 context 以 RunTimeout 为界。心跳使用分离的 background context，这样
	// 一次缓慢的 ctx 取消不会把续期丢掉。
	runCtx, cancel := context.WithTimeout(ctx, job.RunTimeout)
	defer cancel()

	hbCtx, hbCancel := context.WithCancel(context.Background())
	defer hbCancel()
	hbDone := make(chan struct{})
	go m.runHeartbeats(hbCtx, hbDone, job, c, log)

	start := time.Now()
	res, handlerErr := func() (out HandlerResult, retErr error) {
		// defer 闭包里调用 recover() 并赋给具名返回值 retErr，这样「handler 直接
		// panic」会被当成「返回了错误」处理：classifyError 把它记为 "handler_panic"，
		// finishFailure 写 FAILED 审计行。没有具名返回值的话，panic 会被静默吞掉，
		// 外层代码反而写出一条 rows_affected=0 的 SUCCESS 行。
		defer func() {
			if r := recover(); r != nil {
				log.Error("scheduler handler panic", "panic", r)
				retErr = fmt.Errorf("%w: %v", ErrHandlerPanic, r)
			}
		}()
		return job.Handler(runCtx, HandlerInput{
			Job:      job,
			Scope:    scope,
			PlanTime: planTime,
			Attempt:  c.Attempt,
			RunnerID: m.opts.RunnerID,
			Heartbeat: func(ctx context.Context) error {
				return heartbeat(ctx, m.pool, c.ID, c.LeaseToken, job.StaleTimeout)
			},
		})
	}()
	duration := time.Since(start)

	hbCancel()
	<-hbDone

	dur := duration.Milliseconds()
	dbTime, dberr := dbNow(context.Background(), m.pool)
	if dberr != nil {
		// 退回本机时间也足够让审计行在排查时保持诚实；稳态查询的水位索引用的是
		// plan_time 而不是 finished_at。
		dbTime = time.Now().UTC()
	}

	if handlerErr != nil {
		nextRetry := time.Time{}
		if c.Attempt < job.MaxAttempts {
			delay := job.retryDelay(c.Attempt)
			nextRetry = dbTime.Add(delay)
		}
		errCode := classifyError(handlerErr)
		if err := finishFailure(context.Background(), m.pool, c.ID, c.LeaseToken,
			dbTime, dur, errCode, handlerErr.Error(), nextRetry); err != nil {
			if errors.Is(err, ErrLeaseLost) {
				log.Warn("scheduler: terminal FAILED ignored, lease was stolen",
					"duration_ms", dur, "error", handlerErr.Error())
				return
			}
			log.Error("scheduler: write terminal FAILED",
				"duration_ms", dur, "handler_error", handlerErr.Error(), "error", err)
			return
		}
		log.Warn("scheduler: handler failed",
			"duration_ms", dur,
			"error_code", errCode,
			"error", handlerErr.Error(),
			"will_retry", c.Attempt < job.MaxAttempts)
		return
	}

	if err := finishSuccess(context.Background(), m.pool, c.ID, c.LeaseToken,
		dbTime, dur, res); err != nil {
		if errors.Is(err, ErrLeaseLost) {
			log.Warn("scheduler: terminal SUCCESS ignored, lease was stolen",
				"duration_ms", dur)
			return
		}
		log.Error("scheduler: write terminal SUCCESS",
			"duration_ms", dur, "error", err)
		return
	}
	log.Info("scheduler: handler succeeded",
		"duration_ms", dur,
		"rows_affected", res.RowsAffected)
}

// runHeartbeats 在 Handler 运行期间维持租约存活。ctx 被取消时退出。结束时关闭 done，
// 让调用方确认「运行完成后不会再有心跳更新发出」。
func (m *Manager) runHeartbeats(
	ctx context.Context,
	done chan<- struct{},
	job *JobSpec,
	c claim,
	log *slog.Logger,
) {
	defer close(done)
	t := time.NewTicker(job.HeartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			hbCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := heartbeat(hbCtx, m.pool, c.ID, c.LeaseToken, job.StaleTimeout)
			cancel()
			if errors.Is(err, ErrLeaseLost) {
				log.Warn("scheduler: lease lost during heartbeat, runner should stop")
				return
			}
			if err != nil {
				log.Warn("scheduler: heartbeat error", "error", err)
			}
		}
	}
}

// classifyError 把 handler 错误映射成审计行上存储的短 error_code。未知错误给通用码；
// 具体码只留给认识的那些哨兵（context 超时、写终态前租约已丢、被调度器 recover 的
// panic 等）。
func classifyError(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "run_timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, ErrLeaseLost):
		return "lease_lost"
	case errors.Is(err, ErrHandlerPanic):
		return "handler_panic"
	default:
		return "handler_error"
	}
}

// ErrHandlerPanic 包装从任务 handler 里 recover 出来的 panic 值，这样调度器能把它记到
// 审计行上，并（若 max_attempts 允许）重试。生产 handler 应当优先返回 error；但我们
// 把 panic 当作失败处理，而不是让它要么搞崩进程、要么——更糟——被静默吞掉写进一条
// SUCCESS 审计行。
var ErrHandlerPanic = errors.New("scheduler: handler panic")
