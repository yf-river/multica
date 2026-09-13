// Package scheduler 是 docs/db-backed-execution-scheduler-rfc.md (MUL-2957) 里描述的
// 基于数据库的执行记录调度器。
//
// 它把 `sys_cron_executions` 表变成所有「内部周期性任务」的分布式锁 + 审计日志。
// 每台 app 实例都会去 tick 同一批注册任务，但表上 (job_name, scope_kind, scope_id,
// plan_time) 的唯一键保证同一计划只有一台实例能拿到租约；没抢到的实例静默不做事。
//
// 失败处理、陈旧租约窃取、重试策略、追赶(catch-up)行为全都由注册的 JobSpec 驱动——
// 调度器本身刻意只是包在 db_ops.go 那些 SQL 原语外面的一层薄壳。
package scheduler

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// CatchUpMode 控制当某次 tick 跑晚了、或停机很久后恢复时，调度器要认领哪些
// plan_time。
type CatchUpMode int

const (
	// CatchUpLatestOnly 只认领最近到期的那一个计划。适合「处理器自己有水位线、
	// 不需要逐 tick 重放也能补回漏掉数据」的任务（例如 task_usage 小时汇总：
	// rollup_task_usage_hourly_window 靠 task_usage_hourly_rollup_state.watermark_at
	// 追赶）。
	CatchUpLatestOnly CatchUpMode = iota

	// CatchUpEveryPlan 会认领每一个被跳过的 plan_time，最老的优先，每 tick 最多
	// MaxPlansPerTick 个，且受 CatchUpWindow 限制。适合「每个计划桶都有独立业务
	// 含义」的任务。
	CatchUpEveryPlan
)

// String 返回该模式的稳定 snake_case 标识。
func (m CatchUpMode) String() string {
	switch m {
	case CatchUpLatestOnly:
		return "latest_only"
	case CatchUpEveryPlan:
		return "every_plan"
	default:
		return fmt.Sprintf("unknown(%d)", int(m))
	}
}

// Scope 标识一次计划执行的加锁维度。全局任务用 ScopeGlobal——kind 和 id 都用字面串
// "global"，保证唯一键里没有 NULL 列。
type Scope struct {
	Kind string
	ID   string
}

// ScopeGlobal 是「锁整库」类任务（例如 rollup_task_usage_hourly）用的单例作用域。
var ScopeGlobal = Scope{Kind: "global", ID: "global"}

// String 拼出 "kind/id" 形式的作用域标识。
func (s Scope) String() string { return s.Kind + "/" + s.ID }

// ScopeProvider 产出一个任务在给定时刻应当被 tick 的作用域列表。全局任务返回
// {ScopeGlobal}；按分片拆分/分发的任务可以每分片返回一项。
type ScopeProvider func(ctx context.Context, now time.Time) ([]Scope, error)

// HandlerInput 是调度器传给任务处理器的入参。
type HandlerInput struct {
	Job       *JobSpec
	Scope     Scope
	PlanTime  time.Time
	Attempt   int
	RunnerID  string
	Heartbeat func(ctx context.Context) error
}

// HandlerResult 是处理器返回的结果。RowsAffected 与 Result 会写进审计行；Result 必须
// 小（表里 JSONB 列 + finishSuccess 里的运行时守卫隐式限制其大小）。
type HandlerResult struct {
	RowsAffected int64
	Result       map[string]any
}

// Handler 是一个任务的业务逻辑。调度器负责租约，对每条被认领的 (job, scope,
// plan_time) 行「恰好调用一次」Handler，并在 lease_token 保护下写回终态。
//
// 长时间运行的处理器必须周期性调用 HandlerInput.Heartbeat（例如每 30s），好让调度器
// 能延长 stale_after；若返回的错误是 ErrLeaseLost，处理器应当停止并返回。
type Handler func(ctx context.Context, in HandlerInput) (HandlerResult, error)

// JobSpec 描述一个已注册的任务。调度器把 spec 存在按 Name 键控的注册表里；Name 必须
// 跨版本保持稳定，因为它是审计/索引的键。
type JobSpec struct {
	// Name 是任务的规范标识。用 ASCII 的 snake_case。
	Name string

	// Cadence 是计划桶的大小（例如 5 * time.Minute）。调度器把 `db_now -
	// ScheduleDelay` 向下取整成 Cadence 的整数倍，得到规范的 UTC plan_time。
	Cadence time.Duration

	// ScheduleDelay 把「eligible（可执行）」的时界从现在往回推。延迟 5 分钟意味着
	// 12:00 的计划要到 12:05（db_now）才变得可执行。这能防止刚到的数据被那些拿
	// `now() - 5 min` 当上界比较的处理器漏掉（例如 rollup_task_usage_hourly_window）。
	ScheduleDelay time.Duration

	// CatchUpMode 在「只跑最近一个」与「每个计划都重放」之间二选一。
	CatchUpMode CatchUpMode

	// CatchUpWindow 限制调度器回溯重放漏跑计划（CatchUpEveryPlan）或计数被跳过
	// 计划（CatchUpLatestOnly）时的最远范围。早于 now - CatchUpWindow 的计划忽略。
	CatchUpWindow time.Duration

	// MaxPlansPerTick 限制 CatchUpEveryPlan 模式下单次 tick 认领的计划数。
	// latest-only 任务忽略该值。
	MaxPlansPerTick int

	// RunTimeout 是单次处理器的 context 上限。必须小于 StaleTimeout。
	RunTimeout time.Duration

	// StaleTimeout 是 RUNNING 租约距离最后一次心跳多久后被视为陈旧。租约陈旧且
	// AllowStaleReentry 为 true 时，别的 runner 可以窃取它。
	StaleTimeout time.Duration

	// HeartbeatInterval 是处理器运行期间调度器续期 stale_after 的频率。必须小于
	// StaleTimeout。
	HeartbeatInterval time.Duration

	// AllowStaleReentry 允许别的 runner 窃取陈旧的 RUNNING 租约。非幂等任务应设
	// false；此时陈旧租约以 error_code='stale_timeout' 转成 FAILED，需要人工修复。
	AllowStaleReentry bool

	// MaxAttempts 限制同一个 plan_time 在被判 FAILED 前最多尝试的次数（含首次）。
	MaxAttempts int

	// RetryBackoff[i] 是第 i+2 次尝试（即第二次尝试）之前等待的时长。下标超出
	// len-1 时复用最后一个条目。空切片表示不重试。
	RetryBackoff []time.Duration

	// Scopes 返回每轮循环要 tick 的作用域。全局任务请用 StaticScopes(ScopeGlobal)
	// 这个辅助函数。
	Scopes ScopeProvider

	// PlansForScope 若非 nil，就「取代」本任务基于 Cadence 的计划生成器。该钩子收到
	// (job, scope) 对最近一次已存储的计划与当前（来自 DB 的）`now`，返回本次 tick
	// 要尝试的计划时间列表（规范 UTC）。返回空切片表示「本轮没有到期计划」。
	//
	// 设了 PlansForScope 后，Cadence / CatchUpMode / CatchUpWindow 对计划生成不再
	// 起作用——具体有哪些 plan_time 完全由钩子说了算。剩下的计时字段（RunTimeout、
	// StaleTimeout、HeartbeatInterval、MaxAttempts、RetryBackoff、
	// AllowStaleReentry）仍约束租约与重试行为；MaxPlansPerTick 仍作为钩子返回列表的
	// 安全上限（manager 会截断超出部分）。
	//
	// 为那些 plan_time 构不成均匀 Cadence 网格的任务设计——例如 Autopilot 里由任意
	// cron 表达式驱动的调度触发器：每个触发器是自己的 scope，触发时刻按「cron +
	// 时区」逐触发器计算。
	//
	// 钩子每 tick 对每个 (job, scope) 调用一次。钩子返回的 plan_time 原样传给
	// tryClaim，所以钩子必须返回规范 UTC 时间戳。返回一个「已终态」的 plan_time 是
	// 安全的：tryClaim 会把它当成冲突，该行不会被重跑。
	PlansForScope func(ctx context.Context, scope Scope, now time.Time,
		latest LatestPlanInfo) ([]time.Time, error)

	// Handler 是每次执行的业务逻辑。
	Handler Handler
}

// StaticScopes 返回一个永远产出给定作用域的 ScopeProvider。用于作用域集合永不改变
// 的任务（例如 global/global，或固定的分片数量）。
func StaticScopes(scopes ...Scope) ScopeProvider {
	frozen := append([]Scope(nil), scopes...)
	return func(_ context.Context, _ time.Time) ([]Scope, error) {
		return frozen, nil
	}
}

// validate 校验那些 SQL 原语依赖的不变式。
func (j *JobSpec) validate() error {
	if strings.TrimSpace(j.Name) == "" {
		return fmt.Errorf("scheduler: job name is required")
	}
	if j.PlansForScope == nil && j.Cadence <= 0 {
		return fmt.Errorf("scheduler: job %q: cadence must be > 0 (or set PlansForScope)", j.Name)
	}
	if j.RunTimeout <= 0 {
		return fmt.Errorf("scheduler: job %q: run_timeout must be > 0", j.Name)
	}
	if j.StaleTimeout <= j.RunTimeout {
		return fmt.Errorf("scheduler: job %q: stale_timeout (%s) must be greater than run_timeout (%s)",
			j.Name, j.StaleTimeout, j.RunTimeout)
	}
	if j.HeartbeatInterval <= 0 || j.HeartbeatInterval >= j.StaleTimeout {
		return fmt.Errorf("scheduler: job %q: heartbeat_interval must be > 0 and < stale_timeout", j.Name)
	}
	if j.MaxAttempts < 1 {
		return fmt.Errorf("scheduler: job %q: max_attempts must be >= 1", j.Name)
	}
	if j.Scopes == nil {
		return fmt.Errorf("scheduler: job %q: scopes provider is required", j.Name)
	}
	if j.Handler == nil {
		return fmt.Errorf("scheduler: job %q: handler is required", j.Name)
	}
	if j.PlansForScope == nil && j.CatchUpMode == CatchUpEveryPlan && j.MaxPlansPerTick <= 0 {
		return fmt.Errorf("scheduler: job %q: max_plans_per_tick must be > 0 for every_plan catch-up", j.Name)
	}
	return nil
}

// retryDelay 返回「第 N 次尝试（从 1 开始）失败之后、下一次尝试变得可执行之前」
// 的等待时长。
func (j *JobSpec) retryDelay(attempt int) time.Duration {
	if len(j.RetryBackoff) == 0 {
		return 0
	}
	idx := attempt - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(j.RetryBackoff) {
		idx = len(j.RetryBackoff) - 1
	}
	return j.RetryBackoff[idx]
}

// FloorPlan 返回 cadence c 下包含 `eligible` 的规范 UTC plan_time 桶。对测试开放。
func FloorPlan(eligible time.Time, c time.Duration) time.Time {
	if c <= 0 {
		return eligible.UTC()
	}
	t := eligible.UTC()
	return t.Truncate(c)
}
