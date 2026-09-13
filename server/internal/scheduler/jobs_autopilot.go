// jobs_autopilot.go 把「Autopilot 定时触发器」接进共享 DB 调度器：每个启用的触发器是
// 自己的 scope（scope_kind="autopilot_trigger"，scope_id=trigger id），plan_time 由
// 该触发器的 cron+时区基于 DB 时间算出。这份代码取代了旧版 cmd/server 里用 goroutine
// 实现的 autopilot_scheduler.go（MUL-3551）。
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// JobNameAutopilotScheduleDispatch 是写进 sys_cron_executions 审计行的规范任务名。
// 跨版本必须稳定——改名会让历史行失去归属。
const JobNameAutopilotScheduleDispatch = "autopilot_schedule_dispatch"

// ScopeKindAutopilotTrigger 标记作用域维度。每个启用的调度触发器是一个 scope；
// scope_id 是触发器 UUID。
const ScopeKindAutopilotTrigger = "autopilot_trigger"

// DefaultAutopilotScheduleTimezone 是触发器 timezone 列为 NULL/空时的回退值。与
// service.DefaultAutopilotTriggerTimezone 一致。
const DefaultAutopilotScheduleTimezone = "UTC"

// maxAutopilotScheduleLateness 是「在配置的 plan_time 之后投递一次 cron occurrence」
// 的容差窗口。正常调度抖动是几十秒级；超过这个就是陈旧的补投，应当等下一个配置槽位，
// 而不是在某个随意时刻补触发。
const maxAutopilotScheduleLateness = 5 * time.Minute

// AutopilotScheduleDispatcher 是本任务从 service.AutopilotService 里需要的窄接口。
// 定义在这里，让本包的单测（以及 cmd/server 集成测试）不用拖进整个 service 层就能
// 打桩。
type AutopilotScheduleDispatcher interface {
	DispatchAutopilotForPlan(
		ctx context.Context,
		autopilot db.Autopilot,
		triggerID pgtype.UUID,
		source string,
		payload []byte,
		plannedAt time.Time,
	) (*db.AutopilotRun, error)
}

// AutopilotScheduleDispatchJob 返回驱动「定时 Autopilot 派发」的 JobSpec，走既有
// scheduler + sys_cron_executions 租约设施。取代旧版 cmd/server/autopilot_scheduler.go
// 的 goroutine（MUL-3551）。
//
// 设计要点：
//
//   - 每个启用的调度触发器是自己的 scope（scope_kind = "autopilot_trigger"、
//     scope_id = trigger.id）。
//   - plan_time 是从触发器 cron 表达式 + 时区基于 DB 时间算出的规范 UTC cron
//     occurrence。PlansForScope 钩子枚举 (lastPlan, dbNow] 范围内的 occurrence，只取
//     最近一个（折叠掉漏触发的——与旧 goroutine 同策略）。
//   - handler 调 AutopilotService.DispatchAutopilotForPlan，它本身靠迁移 124 的
//     部分唯一索引对 (trigger_id, planned_at) 幂等。再加上 sys_cron_executions 对
//     (job, scope, plan_time) 的唯一键，同一计划 occurrence 即使在 stale-steal 重试下
//     也不会产生重复 run。
//
// 重启语义：
//
//   - 触发器在下一个 tick 从 autopilot_trigger 表恢复——不维护任何内存定时器表。
//     scope provider 每 tick 跑一次，从 `kind='schedule' AND enabled AND
//     a.status='active'` 重新推导可执行集合。
//   - 若上次进程在认领某 plan_time 之后、写终态之前崩溃，manager.runJob 里的陈旧租约
//     清扫会把该行升为 error_code='stale_timeout' 的 FAILED。AllowStaleReentry=true +
//     attempts < MaxAttempts 让下一 tick 能窃取租约重入派发——而
//     DispatchAutopilotForPlan 的幂等查找会返回已创建的 run，而不是再造一个。
func AutopilotScheduleDispatchJob(
	pool *pgxpool.Pool,
	queries *db.Queries,
	dispatcher AutopilotScheduleDispatcher,
) JobSpec {
	cache := newAutopilotScheduleCache()

	return JobSpec{
		Name:              JobNameAutopilotScheduleDispatch,
		Cadence:           0, // 钩子驱动；cron 表达式是任意的
		ScheduleDelay:     0,
		CatchUpMode:       CatchUpLatestOnly, // 设了钩子后此字段被忽略，但记录意图
		CatchUpWindow:     24 * time.Hour,
		RunTimeout:        2 * time.Minute,
		StaleTimeout:      5 * time.Minute,
		HeartbeatInterval: 30 * time.Second,
		AllowStaleReentry: true,
		MaxAttempts:       3,
		RetryBackoff: []time.Duration{
			1 * time.Minute,
			5 * time.Minute,
			15 * time.Minute,
		},
		// 双保险：CatchUpLatestOnly 永远用不到 5 个 plan_time；这个上限只在将来有人把
		// 钩子改成 every_plan 语义时才有意义。
		MaxPlansPerTick: 5,

		Scopes:        autopilotScopes(pool, queries, cache),
		PlansForScope: autopilotPlansForScope(cache),
		Handler:       autopilotHandler(queries, dispatcher),
	}
}

// autopilotTriggerConfig 是计划钩子需要的「单触发器每 tick 视图」（cron + 时区 +
// 引导起点）。
type autopilotTriggerConfig struct {
	TriggerID      string
	CronExpression string
	Timezone       string
	CreatedAt      time.Time
	// LastFiredAt 是 autopilot_trigger.last_fired_at；触发器从没触发过（或到目前为止
	// 没被任何调度器管过）时为零值。计划钩子拿它锚定冷启动枚举，保证那些已被处理过的
	// occurrence（例如迁移前被旧 goroutine 处理过、或重启前被本进程上一次生命周期处理
	// 过）不会被重放。
	LastFiredAt time.Time
}

// autopilotScheduleCache 持有每 tick 的触发器配置映射。scope provider 在每 tick 开头
// 重写它，计划钩子按 scope 各读一次。读写锁让将来可以安全地并行做按 scope 计划。
type autopilotScheduleCache struct {
	mu       sync.RWMutex
	triggers map[string]autopilotTriggerConfig
}

func newAutopilotScheduleCache() *autopilotScheduleCache {
	return &autopilotScheduleCache{triggers: make(map[string]autopilotTriggerConfig)}
}

func (c *autopilotScheduleCache) replace(next map[string]autopilotTriggerConfig) {
	c.mu.Lock()
	c.triggers = next
	c.mu.Unlock()
}

func (c *autopilotScheduleCache) get(id string) (autopilotTriggerConfig, bool) {
	c.mu.RLock()
	v, ok := c.triggers[id]
	c.mu.RUnlock()
	return v, ok
}

// autopilotScopes 列出本次 tick 可触发的所有调度触发器，并填充计划钩子要用的缓存。
// 被禁用的触发器与被暂停/归档的 autopilot 会自然滤掉，因为 SQL 过滤条件已排除它们。
func autopilotScopes(
	pool *pgxpool.Pool,
	queries *db.Queries,
	cache *autopilotScheduleCache,
) ScopeProvider {
	_ = pool // reserved for future tx-bounded reads
	return func(ctx context.Context, now time.Time) ([]Scope, error) {
		rows, err := queries.ListSchedulableAutopilotTriggers(ctx)
		if err != nil {
			return nil, fmt.Errorf("autopilot scope: list schedulable triggers: %w", err)
		}
		next := make(map[string]autopilotTriggerConfig, len(rows))
		scopes := make([]Scope, 0, len(rows))
		for _, r := range rows {
			id := util.UUIDToString(r.ID)
			if id == "" {
				continue
			}
			tz := DefaultAutopilotScheduleTimezone
			if r.Timezone.Valid && r.Timezone.String != "" {
				tz = r.Timezone.String
			}
			cron := ""
			if r.CronExpression.Valid {
				cron = r.CronExpression.String
			}
			if cron == "" {
				continue
			}
			createdAt := time.Time{}
			if r.CreatedAt.Valid {
				createdAt = r.CreatedAt.Time.UTC()
			}
			lastFiredAt := time.Time{}
			if r.LastFiredAt.Valid {
				lastFiredAt = r.LastFiredAt.Time.UTC()
			}
			next[id] = autopilotTriggerConfig{
				TriggerID:      id,
				CronExpression: cron,
				Timezone:       tz,
				CreatedAt:      createdAt,
				LastFiredAt:    lastFiredAt,
			}
			scopes = append(scopes, Scope{Kind: ScopeKindAutopilotTrigger, ID: id})
		}
		cache.replace(next)
		return scopes, nil
	}
}

// autopilotPlansForScope 返回 PlansForScope 钩子：枚举 (lastPlan, dbNow] 里的每个
// cron occurrence，只保留最近一个，并且若它落在「派发容差窗口」之外就拒绝。
// latest-only 的折叠防止长时间停机把每个漏掉的时间槽都重放一遍；容差守卫防止一个
// 刚被暂停/刚变为可触发的触发器在配置时间过了几小时后才触发。
//
// 可重试状态要特殊处理：最近存储的 plan_time 若是「还有剩余尝试次数、next_retry_at
// <= now」的 FAILED 行，钩子就原样返回那个 plan_time，让 tryClaim 的 FAILED-带重试
// 分支能触发。没有这个分支，下面「半开区间 (latest.PlanTime, now]」的枚举会跳过那个
// 失败桶，这次 occurrence 就丢了——这正是 #4444 评审里指出的经典 bug：认领后崩溃的
// 序列可能漏掉一次定时 occurrence（MUL-3551 验收 ③）。
func autopilotPlansForScope(cache *autopilotScheduleCache) func(
	ctx context.Context, scope Scope, now time.Time, latest LatestPlanInfo,
) ([]time.Time, error) {
	const replayWindow = 24 * time.Hour
	return func(ctx context.Context, scope Scope, now time.Time, latest LatestPlanInfo) ([]time.Time, error) {
		cfg, ok := cache.get(scope.ID)
		if !ok {
			// 触发器在「scope 列表」与「计划计算」之间消失了，或被过滤掉。没什么可
			// 计划的——静默无事可做是正确的。
			return nil, nil
		}

		// 重试路径：manager 的陈旧租约清扫已把任何被遗弃的 RUNNING 行升成 FAILED。
		// 若那行 FAILED 还有剩余尝试次数、且 next_retry_at 已过，同一个 plan_time 就
		// 还能再次认领。tryClaim 的 retry-from-FAILED 分支只有在计划器返回那个精确的
		// plan_time 时才会触发，所以这里必须把它抛出来——向前移到更新的 occurrence
		// 会让 FAILED 行永远停在 attempt < max_attempts，漏掉这次派发。
		if latest.RetryEligible(now) {
			return []time.Time{latest.PlanTime}, nil
		}

		// 锚点选择——三种情形，按序：
		//
		//   1. `latest.Found`：该触发器至少已有一条新调度器写的 sys_cron_executions
		//      行。严格从最近一次 plan_time 之后续跑。
		//
		//   2. `cfg.LastFiredAt` 已设置：触发器以前触发过，无论是迁移前的旧 goroutine
		//      还是本进程上一次生命周期。严格从最近一次成功触发之后续跑，以免重放
		//      已经处理过的 occurrence。这正是 MUL-3551 dev 上报的「部署后误触发」的
		//      成因：没有它，一个在旧代码下 Mon 17:10 触发过的触发器，新调度器一接管
		//      就会再枚举一次——因为 (created_at, now] 半开区间仍包含 Mon 17:10。
		//
		//   3. 从没触发过的全新触发器：锚在 created_at，只枚举触发器创建之后出现的
		//      occurrence。
		//
		// 每种情形最终都要过一道安全上限，防止一次枚举超过 `replayWindow` 的历史。
		var after time.Time
		switch {
		case latest.Found:
			after = latest.PlanTime
		case !cfg.LastFiredAt.IsZero():
			after = cfg.LastFiredAt
		default:
			after = cfg.CreatedAt
		}
		// 用 CatchUpWindow 界住重放，防止长时间暂停/休眠的触发器枚举出数百万个历史桶。
		// `after` 通常已经很新（要么是 latest.PlanTime，要么是 last_fired_at）；这个
		// 上限只在「触发器几周前就建了但从没触发过」这种不常见路径上有意义。
		if oldest := now.Add(-replayWindow); after.Before(oldest) {
			after = oldest
		}

		occs, err := service.NextOccurrencesUTC(cfg.CronExpression, cfg.Timezone, after, now)
		if err != nil {
			return nil, fmt.Errorf("autopilot plans: cron eval for trigger %s: %w", scope.ID, err)
		}
		if len(occs) == 0 {
			return nil, nil
		}
		// CatchUpLatestOnly：把漏掉的触发折叠到最近一次。
		latestDue := occs[len(occs)-1]
		if isAutopilotSchedulePlanStale(now, latestDue) {
			return nil, nil
		}
		return []time.Time{latestDue}, nil
	}
}

// isAutopilotSchedulePlanStale 判断计划是否已陈旧到不该再补触发。
func isAutopilotSchedulePlanStale(now, planTime time.Time) bool {
	return now.Sub(planTime) > maxAutopilotScheduleLateness
}

// autopilotHandler 派发一次 (trigger, planTime) 尝试，并返回会影响租约的结果。它会在
// handler 内部重新读取 trigger + autopilot，这样 tick 之间的状态变化（例如触发器被
// 禁用、autopilot 被暂停）能立刻生效，不必等下一次 scope 列表。
func autopilotHandler(
	queries *db.Queries,
	dispatcher AutopilotScheduleDispatcher,
) Handler {
	return func(ctx context.Context, in HandlerInput) (HandlerResult, error) {
		triggerID, err := parseScopeUUID(in.Scope.ID)
		if err != nil {
			return HandlerResult{}, fmt.Errorf("autopilot handler: scope id is not a valid uuid: %w", err)
		}

		trigger, err := queries.GetAutopilotTrigger(ctx, triggerID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// 触发器在 scope 列表与运行之间被删了。sys_cron_executions 里为一个已
				// 消失的触发器留一条 SUCCESS 行没毛病——没什么可派发的，将来也没有 tick
				// 会再返回这个 scope。
				return HandlerResult{RowsAffected: 0, Result: map[string]any{
					"skipped_reason": "trigger_not_found",
				}}, nil
			}
			return HandlerResult{}, fmt.Errorf("load trigger: %w", err)
		}
		if !trigger.Enabled || trigger.Kind != "schedule" {
			return HandlerResult{RowsAffected: 0, Result: map[string]any{
				"skipped_reason": "trigger_disabled",
			}}, nil
		}

		autopilot, err := queries.GetAutopilot(ctx, trigger.AutopilotID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return HandlerResult{RowsAffected: 0, Result: map[string]any{
					"skipped_reason": "autopilot_not_found",
				}}, nil
			}
			return HandlerResult{}, fmt.Errorf("load autopilot: %w", err)
		}
		if autopilot.Status != "active" {
			return HandlerResult{RowsAffected: 0, Result: map[string]any{
				"skipped_reason": "autopilot_inactive",
				"status":         autopilot.Status,
			}}, nil
		}

		run, err := dispatcher.DispatchAutopilotForPlan(
			ctx, autopilot, trigger.ID, "schedule", nil, in.PlanTime,
		)
		if err != nil {
			return HandlerResult{}, fmt.Errorf("dispatch for plan: %w", err)
		}

		// 同一次写入里，把「仅作展示」的 next_run_at 推进到下一个时间槽，并更新
		// last_fired_at，让触发器 UI 不再显示一个「已经触发过」的 next run（MUL-3749）。
		// 该值停留在应用本机时钟——与触发器创建/更新的展示路径一致——但被地板到「刚
		// 触发过的那个 plan_time」（见 advancedNextRun），所以滞后的应用时钟绝不会把
		// 刚派发过的时间槽又算一遍。错误不致命：规范记录是 autopilot_run.created_at，
		// 下次派发无论如何都会刷新该值；cron/时区解析失败时就退化为只更新
		// last_fired_at，让「触发过」这件事仍被记录。
		tz := DefaultAutopilotScheduleTimezone
		if trigger.Timezone.Valid && trigger.Timezone.String != "" {
			tz = trigger.Timezone.String
		}
		if next, ok := advancedNextRun(trigger.CronExpression.String, tz, in.PlanTime, time.Now()); ok {
			_ = queries.AdvanceTriggerNextRun(ctx, db.AdvanceTriggerNextRunParams{
				ID:        trigger.ID,
				NextRunAt: pgtype.Timestamptz{Time: next, Valid: true},
			})
		} else {
			_ = queries.TouchAutopilotTriggerFiredAt(ctx, trigger.ID)
		}

		return HandlerResult{
			RowsAffected: 1,
			Result: map[string]any{
				"run_id":     util.UUIDToString(run.ID),
				"run_status": run.Status,
			},
		}, nil
	}
}

// advancedNextRun 计算调度触发器在 planTime 触发后要写入的「仅作展示」next_run_at。
// 它在应用本机时钟（`now`，通常是 time.Now()）上求 cron，但锚定在 max(now, planTime)，
// 保证结果永远严格晚于刚触发过的那个槽位——即使本实例时钟比「判计划到期的 DB 时钟」
// 慢也不行。没有这个下限，落后亚秒级时钟会重新算出同一个槽位，MUL-3749 的
// next_run_at 陈旧 bug 就会在周期边界复发。cron/时区解析失败返回 ok=false，提示调用
// 方退化为只更新 last_fired_at。
func advancedNextRun(cronExpr, timezone string, planTime, now time.Time) (time.Time, bool) {
	anchor := now
	if planTime.After(anchor) {
		anchor = planTime
	}
	next, err := service.NextOccurrenceAfterUTC(cronExpr, timezone, anchor)
	if err != nil {
		return time.Time{}, false
	}
	return next, true
}

// parseScopeUUID 把 scope.ID 字符串还原成 pgtype.UUID。标准带连字符形式与不带连字符
// 的 32-hex 裸形式都接受，让契约宽容些。
func parseScopeUUID(s string) (pgtype.UUID, error) {
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		return pgtype.UUID{}, err
	}
	if !u.Valid {
		return pgtype.UUID{}, errors.New("invalid uuid")
	}
	return u, nil
}
