// jobs_plugin_hook.go 把「插件 manifest 声明的定时 hook」接进共享 DB 调度器：每个启用
// 的 plugin hook schedule 是一个带 generation 的 scope，plan_time 由该 schedule 的
// cron+时区逐次算出。scope 里带 generation UUID，因此「被修改/重新启用的 schedule 会
// 开启一条全新串行时间线」，同时允许旧的在途投递跑完。文件里的缓存负责在多次 tick
// 间记住 schedule 配置与「本次触发合并了几次错过的 occurrence」。
package scheduler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/featureflags"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/plugincontract"
)

const (
	// JobNamePluginHookScheduleDispatch 会持久化到 sys_cron_executions 里，跨版本
	// 必须保持稳定。
	JobNamePluginHookScheduleDispatch = "plugin_hook_schedule_dispatch"
	ScopeKindPluginHookSchedule       = "plugin_hook_schedule"
	pluginHookScheduleAttempts        = 3
	pluginHookSchedulePageSize        = 1024
)

// pluginHookScheduleConfig 是一次插件 hook 调度在进程内的快照配置。
type pluginHookScheduleConfig struct {
	ID             pgtype.UUID
	InstallationID pgtype.UUID
	HookKey        string
	CronExpression string
	Timezone       string
	Generation     pgtype.UUID
	ActivatedAt    time.Time
}

// pluginHookScheduleCache 缓存 schedule 配置（schedules）与「已合并的错失次数」
// （coalesced）。用锁保护，因为 scope provider 与 handler 可能在并发的 tick goroutine
// 里读到同一份缓存。
type pluginHookScheduleCache struct {
	mu        sync.RWMutex
	schedules map[string]pluginHookScheduleConfig
	coalesced map[string]int
}

func newPluginHookScheduleCache() *pluginHookScheduleCache {
	return &pluginHookScheduleCache{
		schedules: make(map[string]pluginHookScheduleConfig),
		coalesced: make(map[string]int),
	}
}

// replace 整份替换 schedule 快照，并清掉那些已不在库里的 scope 的合并计数——重试 tick
// 之间要保留计数，但库已不再返回的 schedule 该丢弃。
func (c *pluginHookScheduleCache) replace(schedules map[string]pluginHookScheduleConfig) {
	c.mu.Lock()
	c.schedules = schedules
	// 跨重试 tick 保留计数，但丢弃数据库不再返回的那些 schedule 的条目。setCoalesced
	// 会把合并计数限制成「每个活跃 scope 至多一条待处理条目」，包括在输掉竞速的
	// 副本上。
	for key := range c.coalesced {
		scopeID, _, _ := strings.Cut(key, "/")
		if _, active := schedules[scopeID]; !active {
			delete(c.coalesced, key)
		}
	}
	c.mu.Unlock()
}

func (c *pluginHookScheduleCache) get(scopeID string) (pluginHookScheduleConfig, bool) {
	c.mu.RLock()
	config, ok := c.schedules[scopeID]
	c.mu.RUnlock()
	return config, ok
}

// setCoalesced 记录某 scope 在给定 plan 时刻「合并了几次错失的 occurrence」，同 scope
// 的旧条目先清掉。
func (c *pluginHookScheduleCache) setCoalesced(scopeID string, plan time.Time, count int) {
	c.mu.Lock()
	for key := range c.coalesced {
		if strings.HasPrefix(key, scopeID+"/") {
			delete(c.coalesced, key)
		}
	}
	c.coalesced[scopeID+"/"+plan.UTC().Format(time.RFC3339Nano)] = count
	c.mu.Unlock()
}

// takeCoalesced 取出并删除某 scope+plan 的合并计数（消费一次）。
func (c *pluginHookScheduleCache) takeCoalesced(scopeID string, plan time.Time) int {
	key := scopeID + "/" + plan.UTC().Format(time.RFC3339Nano)
	c.mu.Lock()
	count := c.coalesced[key]
	delete(c.coalesced, key)
	c.mu.Unlock()
	return count
}

// PluginHookScheduleDispatchJob 驱动「manifest 声明的 Plugin schedule」走共享 DB
// 调度器。scope 里包含 generation UUID，因此被改动/重新启用的 schedule 会开启一条全新
// 串行时间线，同时允许旧的在途投递继续跑完。
func PluginHookScheduleDispatchJob(queries *db.Queries, plugins *service.PluginService) JobSpec {
	cache := newPluginHookScheduleCache()
	return JobSpec{
		Name:              JobNamePluginHookScheduleDispatch,
		CatchUpMode:       CatchUpLatestOnly,
		RunTimeout:        45 * time.Second,
		StaleTimeout:      90 * time.Second,
		HeartbeatInterval: 15 * time.Second,
		AllowStaleReentry: true,
		MaxAttempts:       pluginHookScheduleAttempts,
		RetryBackoff:      []time.Duration{30 * time.Second, 2 * time.Minute},
		MaxPlansPerTick:   1,
		Scopes:            pluginHookScheduleScopes(queries, plugins, cache),
		PlansForScope:     pluginHookSchedulePlans(cache),
		Handler:           pluginHookScheduleHandler(queries, plugins, cache),
	}
}

// pluginHookScheduleScopes 返回一个 ScopeProvider：从库里列出所有启用的 hook schedule，
// 为每个构造成 scope（id:generation），并刷新进程内缓存。插件功能关闭时清空缓存、
// 返回空列表（等于整个任务静默停摆）。
func pluginHookScheduleScopes(
	queries *db.Queries,
	plugins *service.PluginService,
	cache *pluginHookScheduleCache,
) ScopeProvider {
	return func(ctx context.Context, _ time.Time) ([]Scope, error) {
		if plugins == nil || !featureflags.PluginsV1Enabled(ctx, plugins.FeatureFlags) {
			cache.replace(map[string]pluginHookScheduleConfig{})
			return nil, nil
		}
		rows, err := queries.ListEnabledPluginHookSchedules(ctx)
		if err != nil {
			return nil, fmt.Errorf("plugin schedule scope: list enabled schedules: %w", err)
		}
		configs := make(map[string]pluginHookScheduleConfig, len(rows))
		scopes := make([]Scope, 0, len(rows))
		for _, row := range rows {
			scopeID := pluginHookScheduleScopeID(row.ID, row.Generation)
			if scopeID == "" || !row.ActivatedAt.Valid {
				continue
			}
			configs[scopeID] = pluginHookScheduleConfig{
				ID:             row.ID,
				InstallationID: row.InstallationID,
				HookKey:        row.HookKey,
				CronExpression: row.CronExpression,
				Timezone:       row.Timezone,
				Generation:     row.Generation,
				ActivatedAt:    row.ActivatedAt.Time.UTC(),
			}
			scopes = append(scopes, Scope{Kind: ScopeKindPluginHookSchedule, ID: scopeID})
		}
		cache.replace(configs)
		return scopes, nil
	}
}

// pluginHookSchedulePlans 是此任务的 PlansForScope 钩子：同一 generation 严格串行——
// 最近一行还在 RUNNING（或还占着重试预算）就不计划新 occurrence；否则按 cron 求出
// after 之后最近一次到期时刻，并缓存本次合并的错失次数。
func pluginHookSchedulePlans(cache *pluginHookScheduleCache) func(
	context.Context, Scope, time.Time, LatestPlanInfo,
) ([]time.Time, error) {
	return func(_ context.Context, scope Scope, now time.Time, latest LatestPlanInfo) ([]time.Time, error) {
		config, ok := cache.get(scope.ID)
		if !ok {
			return nil, nil
		}

		// 一个 generation 严格串行。最近一行仍在运行、或还占着重试预算时，不要计划
		// 更新的 occurrence。
		if latest.Found {
			switch latest.Status {
			case "RUNNING":
				return nil, nil
			case "FAILED":
				if latest.Attempt < latest.MaxAttempts {
					if latest.RetryEligible(now) {
						return []time.Time{latest.PlanTime}, nil
					}
					return nil, nil
				}
			}
		}

		after := config.ActivatedAt
		if latest.Found {
			after = latest.PlanTime
		}
		plan, count, err := latestPluginHookOccurrence(
			config.CronExpression, config.Timezone, after, now,
		)
		if err != nil {
			return nil, fmt.Errorf("plugin schedule plans for %s: %w", scope.ID, err)
		}
		if count == 0 {
			return nil, nil
		}
		cache.setCoalesced(scope.ID, plan, count-1)
		return []time.Time{plan}, nil
	}
}

// latestPluginHookOccurrence 用有界分页去枚举 occurrence，这样一次长时间停机也会收敛
// 到「真正最近一次到期」，而不是停在 service.NextOccurrencesUTC 返回的前 1024 条就
// 结束。
func latestPluginHookOccurrence(cronExpression, timezone string, after, until time.Time) (time.Time, int, error) {
	var latest time.Time
	count := 0
	cursor := after
	for {
		occurrences, err := service.NextOccurrencesUTC(cronExpression, timezone, cursor, until)
		if err != nil {
			return time.Time{}, 0, err
		}
		if len(occurrences) == 0 {
			return latest, count, nil
		}
		count += len(occurrences)
		latest = occurrences[len(occurrences)-1]
		if len(occurrences) < pluginHookSchedulePageSize || !latest.Before(until) {
			return latest, count, nil
		}
		cursor = latest
	}
}

// pluginHookScheduleHandler 是此任务的执行体：解析 scope → 确认插件特性与 schedule 仍
// 有效 → 重放一致性校验 → 经 InvokeHook 投递（用确定性 delivery_id 保证幂等）；各种
// 「不该投」的情形以 skipped_reason 记进审计行，作为 SUCCESS（rows_affected=0）。
func pluginHookScheduleHandler(
	queries *db.Queries,
	plugins *service.PluginService,
	cache *pluginHookScheduleCache,
) Handler {
	return func(ctx context.Context, in HandlerInput) (HandlerResult, error) {
		scheduleID, generation, err := parsePluginHookScheduleScopeID(in.Scope.ID)
		if err != nil {
			return HandlerResult{}, fmt.Errorf("parse plugin hook schedule scope: %w", err)
		}
		if plugins == nil || !featureflags.PluginsV1Enabled(ctx, plugins.FeatureFlags) {
			return pluginHookScheduleSkipped("feature_disabled"), nil
		}

		row, err := queries.GetPluginHookSchedule(ctx, scheduleID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return pluginHookScheduleSkipped("schedule_not_found"), nil
			}
			return HandlerResult{}, fmt.Errorf("load plugin hook schedule: %w", err)
		}
		if !row.Enabled || row.Generation != generation {
			return pluginHookScheduleSkipped("schedule_generation_changed"), nil
		}

		installation, err := queries.GetPluginInstallation(ctx, row.InstallationID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return pluginHookScheduleSkipped("installation_not_found"), nil
			}
			return HandlerResult{}, fmt.Errorf("load plugin installation: %w", err)
		}
		if !installation.Enabled {
			return pluginHookScheduleSkipped("installation_disabled"), nil
		}
		// manifest 一致性：当前 hook 定义必须仍声明这个 cron 调度且允许 schedule
		// 触发、走 HTTP 传输——任一条件不符都说明 manifest 改过，本次跳过。
		hook, err := service.FindHook(installation, row.HookKey)
		if err != nil || hook.Schedule == nil ||
			!service.HookAllowsTrigger(hook, plugincontract.TriggerSchedule) ||
			hook.Transport.Type != plugincontract.TransportHTTP ||
			hook.Schedule.Cron != row.CronExpression || hook.Schedule.Timezone != row.Timezone {
			return pluginHookScheduleSkipped("manifest_changed"), nil
		}

		// 熔断打开时不投递，只把 next_run 推进（并记下合并次数与投递滞后）。
		if plugins.HookBreakerOpen(ctx, installation.ID, hook.Key) {
			advancePluginHookNextRun(ctx, queries, row, in.PlanTime)
			result := pluginHookScheduleSkipped("circuit_open")
			result.Result["coalesced_occurrences"] = cache.takeCoalesced(in.Scope.ID, in.PlanTime)
			result.Result["dispatch_lag_ms"] = max(time.Since(in.PlanTime).Milliseconds(), 0)
			return result, nil
		}

		deliveryID := pluginHookScheduleDeliveryID(installation.ID, hook.Key, row.Generation, in.PlanTime)
		_, err = plugins.InvokeHook(ctx, service.HookInvocation{
			Installation: installation,
			Hook:         hook,
			Trigger:      plugincontract.TriggerSchedule,
			Actor:        service.HookActor{Type: "plugin", ID: installation.ID},
			DeliveryID:   deliveryID,
			PlannedAt:    pgtype.Timestamptz{Time: in.PlanTime.UTC(), Valid: true},
		}, in.Attempt)
		if err != nil {
			// 最后一次尝试也失败：推进 next_run，丢弃合并计数，把错误交给调度器写成
			// FAILED 审计行。
			if in.Job != nil && in.Attempt >= in.Job.MaxAttempts {
				advancePluginHookNextRun(ctx, queries, row, in.PlanTime)
				cache.takeCoalesced(in.Scope.ID, in.PlanTime)
			}
			return HandlerResult{}, fmt.Errorf("deliver scheduled plugin hook: %w", err)
		}

		advancePluginHookNextRun(ctx, queries, row, in.PlanTime)
		return HandlerResult{RowsAffected: 1, Result: map[string]any{
			"delivery_id":           deliveryID,
			"coalesced_occurrences": cache.takeCoalesced(in.Scope.ID, in.PlanTime),
			"dispatch_lag_ms":       max(time.Since(in.PlanTime).Milliseconds(), 0),
		}}, nil
	}
}

// pluginHookScheduleSkipped 构造「本次跳过」的审计结果，reason 说明为什么没投。
func pluginHookScheduleSkipped(reason string) HandlerResult {
	return HandlerResult{RowsAffected: 0, Result: map[string]any{"skipped_reason": reason}}
}

// advancePluginHookNextRun 把该 schedule 行的 next_run_at 推进到 plan 之后的下一次
// occurrence。推进失败就静默放弃——这只是提示性字段，不影响投递本身。
func advancePluginHookNextRun(ctx context.Context, queries *db.Queries, row db.PluginHookSchedule, plan time.Time) {
	next, err := service.NextOccurrenceAfterUTC(row.CronExpression, row.Timezone, plan)
	if err != nil {
		return
	}
	_, _ = queries.UpdatePluginHookScheduleNextRun(ctx, db.UpdatePluginHookScheduleNextRunParams{
		ID:         row.ID,
		Generation: row.Generation,
		NextRunAt:  pgtype.Timestamptz{Time: next.UTC(), Valid: true},
	})
}

// pluginHookScheduleScopeID 把 schedule id 与 generation 拼成 scope id（"id:gen"）。
// 任一为空返回 ""。
func pluginHookScheduleScopeID(id, generation pgtype.UUID) string {
	idString := util.UUIDToString(id)
	generationString := util.UUIDToString(generation)
	if idString == "" || generationString == "" {
		return ""
	}
	return idString + ":" + generationString
}

// parsePluginHookScheduleScopeID 把 scope id 拆回 schedule id 与 generation。
func parsePluginHookScheduleScopeID(scopeID string) (pgtype.UUID, pgtype.UUID, error) {
	parts := strings.Split(scopeID, ":")
	if len(parts) != 2 {
		return pgtype.UUID{}, pgtype.UUID{}, errors.New("expected schedule:generation scope")
	}
	id, err := util.ParseUUID(parts[0])
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, err
	}
	generation, err := util.ParseUUID(parts[1])
	if err != nil {
		return pgtype.UUID{}, pgtype.UUID{}, err
	}
	return id, generation, nil
}

// pluginHookScheduleDeliveryID 基于 (installation, hook, generation, plan) 派生确定性
// 投递 id：同一 plan 重试时 delivery_id 不变，让接收方能幂等去重。
func pluginHookScheduleDeliveryID(
	installationID pgtype.UUID,
	hookKey string,
	generation pgtype.UUID,
	plan time.Time,
) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{
		util.UUIDToString(installationID),
		hookKey,
		util.UUIDToString(generation),
		plan.UTC().Format(time.RFC3339Nano),
	}, "\x00")))
	return "psd_" + hex.EncodeToString(digest[:])
}
