// autopilot_failure_monitor.go
//
// 职责：周期性巡检"自动驾驶（autopilot）"的失败率，自动暂停那些近期运行记录
// 被失败主导的 autopilot，并给创建者/负责人发收件箱通知。
// 背景见 MUL-1336：曾经有个每 5 分钟跑一次的 autopilot，7 天里 1475/1476 次
// 全部失败还在继续烧任务/token。本文件就是那道"熔断闸"：只有跑得足够多、
// 失败率高得在统计上有意义时才动手，避免误伤样本不足的新 autopilot。
// 文件中存在一"普通版"（tickAutopilotFailureMonitor，逐条操作）与一"事务版"
// （*Durable，每个 autopilot 的暂停+审计+收件箱+实时事件在一个数据库事务里
// 原子提交）两套实现，生产环境走 Durable 版。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// failureMonitorConfig 是失败监控器的一整套可调参数。默认值对应 MUL-1336 §6
// action item #2 的提议：暂停那些"近期运行历史被失败主导、且运行次数足够多
// （失败率在统计上有意义）"的 autopilot。
//
// 全部参数可用环境变量覆盖（见 envFailureMonitorConfig）。把 Interval 设为
// <=0 则整个监控器不启动。
type failureMonitorConfig struct {
	Interval     time.Duration // 巡检周期
	Lookback     time.Duration // 看多长一段历史
	MinRuns      int64         // 判定有效的最小运行次数（样本量门槛）
	FailRatio    float64       // 失败率阈值（0~1）
	StartupDelay time.Duration // 启动后先等多久再开跑
}

func defaultFailureMonitorConfig() failureMonitorConfig {
	return failureMonitorConfig{
		Interval:     24 * time.Hour,
		Lookback:     7 * 24 * time.Hour,
		MinRuns:      50,
		FailRatio:    0.9,
		StartupDelay: 1 * time.Minute,
	}
}

// envFailureMonitorConfig 从环境变量读配置，缺省/非法值逐项回退默认。
func envFailureMonitorConfig() failureMonitorConfig {
	cfg := defaultFailureMonitorConfig()
	cfg.Interval = envDurationOrZero("AUTOPILOT_FAIL_MONITOR_INTERVAL", cfg.Interval)
	cfg.Lookback = envDurationPositive("AUTOPILOT_FAIL_MONITOR_LOOKBACK", cfg.Lookback)
	cfg.StartupDelay = envDurationNonNegative("AUTOPILOT_FAIL_MONITOR_STARTUP_DELAY", cfg.StartupDelay)
	if v, ok := envInt64Positive("AUTOPILOT_FAIL_MONITOR_MIN_RUNS"); ok {
		cfg.MinRuns = v
	}
	if v, ok := envFloatInUnitInterval("AUTOPILOT_FAIL_MONITOR_FAIL_RATIO"); ok {
		cfg.FailRatio = v
	}
	return cfg
}

// runAutopilotFailureMonitor 周期暂停那些近期运行失败率超过阈值的 autopilot。
// 这是为了拦住失控的定时 autopilot 在热循环里持续烧任务/token（例如 MUL-1336
// 里的 `Registro de ls cada 5 min`：7 天 1476 次里失败 1475 次，还每 5 分钟
// 触发一次）。监控器会给 autopilot 的创建者（若创建者是 agent，则给该 agent
// 的负责人）留一条 severity=attention 的收件箱通知，让人知道发生了自动暂停。
//
// 用 `AUTOPILOT_FAIL_MONITOR_INTERVAL=0` 可整体禁用。
func runAutopilotFailureMonitor(ctx context.Context, queries *db.Queries, bus *events.Bus, cfg failureMonitorConfig) {
	if cfg.Interval <= 0 {
		slog.Info("autopilot failure monitor: disabled (interval <= 0)")
		return
	}

	slog.Info(
		"autopilot failure monitor: starting",
		"interval", cfg.Interval.String(),
		"lookback", cfg.Lookback.String(),
		"min_runs", cfg.MinRuns,
		"fail_ratio", cfg.FailRatio,
	)

	// 错峰启动，避免进程一上来就整批压向数据库——大批量滚动重启时这点很重要。
	if cfg.StartupDelay > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(cfg.StartupDelay):
		}
	}

	// 启动延迟结束后立刻先跑一轮，让新部署的节点不用等满一个周期就能抓到
	// 已有的"惯犯"。
	tickAutopilotFailureMonitor(ctx, queries, bus, cfg)

	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickAutopilotFailureMonitor(ctx, queries, bus, cfg)
		}
	}
}

// runAutopilotFailureMonitorDurable 是生产用的监控器。每次暂停、审计快照、
// 收件箱条目和实时事件都在一个数据库事务里提交，崩溃不会留下"已暂停但没有
// 解释说明"的规则。
func runAutopilotFailureMonitorDurable(ctx context.Context, queries *db.Queries, txStarter service.TxStarter, bus *events.Bus, cfg failureMonitorConfig) {
	if cfg.Interval <= 0 {
		slog.Info("autopilot failure monitor: disabled (interval <= 0)")
		return
	}
	if txStarter == nil {
		slog.Error("autopilot failure monitor: transaction starter is required")
		return
	}

	slog.Info(
		"autopilot failure monitor: starting",
		"interval", cfg.Interval.String(),
		"lookback", cfg.Lookback.String(),
		"min_runs", cfg.MinRuns,
		"fail_ratio", cfg.FailRatio,
	)
	if cfg.StartupDelay > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(cfg.StartupDelay):
		}
	}

	tickAutopilotFailureMonitorDurable(ctx, queries, txStarter, bus, cfg)
	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickAutopilotFailureMonitorDurable(ctx, queries, txStarter, bus, cfg)
		}
	}
}

// tickAutopilotFailureMonitorDurable 执行一轮"事务版"巡检：查出超阈值的候选
// autopilot，逐个在独立事务里完成"暂停 + 记规则版本 + 建收件箱通知 + 落库
// 派生事件"，事务提交后再把事件发布到总线。单个候选失败只跳过它，不影响整轮。
func tickAutopilotFailureMonitorDurable(ctx context.Context, queries *db.Queries, txStarter service.TxStarter, bus *events.Bus, cfg failureMonitorConfig) {
	since := time.Now().Add(-cfg.Lookback)
	candidates, err := queries.SelectAutopilotsExceedingFailureThreshold(ctx, db.SelectAutopilotsExceedingFailureThresholdParams{
		MinRuns:            cfg.MinRuns,
		FailRatioThreshold: cfg.FailRatio,
		Since:              pgtype.Timestamptz{Time: since, Valid: true},
	})
	if err != nil {
		slog.Warn("autopilot failure monitor: failed to query candidates", "error", err)
		return
	}
	for _, candidate := range candidates {
		tx, err := txStarter.Begin(ctx)
		if err != nil {
			slog.Warn("autopilot failure monitor: begin pause transaction failed", "autopilot_id", util.UUIDToString(candidate.ID), "error", err)
			continue
		}
		qtx := queries.WithTx(tx)
		paused, err := qtx.SystemPauseAutopilot(ctx, candidate.ID)
		if err != nil {
			_ = tx.Rollback(ctx)
			if !isNoRows(err) {
				slog.Warn("autopilot failure monitor: pause failed", "autopilot_id", util.UUIDToString(candidate.ID), "error", err)
			}
			continue
		}

		failPct := 100.0
		if candidate.TotalRuns > 0 {
			failPct = math.Round(float64(candidate.FailedRuns)/float64(candidate.TotalRuns)*1000) / 10
		}
		if err := service.RecordAutopilotRuleVersion(ctx, qtx, paused, "system", pgtype.UUID{}); err != nil {
			_ = tx.Rollback(ctx)
			slog.Warn("autopilot failure monitor: record rule version failed", "autopilot_id", util.UUIDToString(paused.ID), "error", err)
			continue
		}

		outboxEvents, err := emitAutopilotPausedNotificationsTx(ctx, qtx, paused, candidate, cfg, failPct)
		if err != nil {
			_ = tx.Rollback(ctx)
			slog.Warn("autopilot failure monitor: create pause notifications failed", "autopilot_id", util.UUIDToString(paused.ID), "error", err)
			continue
		}
		updated := events.Event{
			Type:           protocol.EventAutopilotUpdated,
			IdempotencyKey: "autopilot:updated:" + util.UUIDToString(paused.ID) + ":" + util.TimestampToString(paused.UpdatedAt),
			StreamKey:      "autopilot:" + util.UUIDToString(paused.ID),
			WorkspaceID:    util.UUIDToString(paused.WorkspaceID),
			ActorType:      "system",
			Payload: map[string]any{
				"autopilot": autopilotEventPayload(paused),
				"reason":    "auto_paused_high_failure_rate",
			},
		}
		// 把"暂停通知相关事件 + 状态更新事件"全部登记为可靠事件后一并提交，
		// 保证要么全落库、要么一个都不落。
		persisted := make([]events.Event, 0, len(outboxEvents)+1)
		for _, event := range append(outboxEvents, updated) {
			persistedEvent, recordErr := service.RecordDurableEventTx(ctx, qtx, event)
			if recordErr != nil {
				_ = tx.Rollback(ctx)
				slog.Warn("autopilot failure monitor: record durable event failed", "autopilot_id", util.UUIDToString(paused.ID), "event_type", event.Type, "error", recordErr)
				persisted = nil
				break
			}
			persisted = append(persisted, persistedEvent)
		}
		if persisted == nil {
			continue
		}
		if err := tx.Commit(ctx); err != nil {
			slog.Warn("autopilot failure monitor: commit pause transaction failed", "autopilot_id", util.UUIDToString(paused.ID), "error", err)
			continue
		}
		for _, event := range persisted {
			bus.PublishRecovered(event)
		}
		slog.Info("autopilot failure monitor: paused autopilot", "autopilot_id", util.UUIDToString(paused.ID), "workspace_id", util.UUIDToString(paused.WorkspaceID), "failed_runs", candidate.FailedRuns, "total_runs", candidate.TotalRuns, "fail_pct", failPct)
	}
}

// tickAutopilotFailureMonitor 执行一轮"普通版"巡检：查出候选、逐个尝试暂停，
// 成功后发通知 + WS 事件。它是事务版的非原子对照实现（保留用于测试/降级路径）。
func tickAutopilotFailureMonitor(ctx context.Context, queries *db.Queries, bus *events.Bus, cfg failureMonitorConfig) {
	since := time.Now().Add(-cfg.Lookback)
	candidates, err := queries.SelectAutopilotsExceedingFailureThreshold(
		ctx,
		db.SelectAutopilotsExceedingFailureThresholdParams{
			MinRuns:            cfg.MinRuns,
			FailRatioThreshold: cfg.FailRatio,
			Since:              pgtype.Timestamptz{Time: since, Valid: true},
		},
	)
	if err != nil {
		slog.Warn("autopilot failure monitor: failed to query candidates", "error", err)
		return
	}
	if len(candidates) == 0 {
		return
	}

	slog.Info("autopilot failure monitor: candidates", "count", len(candidates))

	for _, c := range candidates {
		paused, err := queries.SystemPauseAutopilot(ctx, c.ID)
		if err != nil {
			// pgx returns ErrNoRows when the WHERE status='active' clause
			// matched zero rows — i.e. another caller (manual UI action,
			// concurrent monitor) paused it first. Treat as a benign no-op.
			//
			// 中文说明：SystemPauseAutopilot 的 WHERE status='active' 若没匹配
			// 到任何行，pgx 会返回 ErrNoRows——也就是别的调用方（手动 UI 操作、
			// 并发的另一个监控实例）已经先把它暂停了。视为无害的空操作即可。
			if isNoRows(err) {
				continue
			}
			slog.Warn("autopilot failure monitor: pause failed",
				"autopilot_id", util.UUIDToString(c.ID),
				"error", err,
			)
			continue
		}

		// A system auto-pause is a substantive status change (MUL-4302 §3.4).
		// Record it as a rule-version publish with a 'system' publisher (no member
		// actor). Best-effort: the monitor is a background sweep, a paused autopilot
		// does not dispatch (so this version is never the active version at a real
		// run — a later member resume would supersede it), and a failed write must
		// not abort the sweep.
		//
		// 中文说明：系统自动暂停是实质性的状态变更（MUL-4302 §3.4），按"规则
		// 版本发布"记录，发布者为 system（没有成员 actor）。这里是尽力而为：
		// 监控器是后台巡检；被暂停的 autopilot 不会派发（所以这个版本永远不会
		// 成为某次真实运行时的活动版本——之后成员手动恢复会取代它）；写失败也
		// 绝不能中断整轮巡检。
		if verr := service.RecordAutopilotRuleVersion(ctx, queries, paused, "system", pgtype.UUID{}); verr != nil {
			slog.Warn("autopilot failure monitor: record rule version failed",
				"autopilot_id", util.UUIDToString(paused.ID), "error", verr)
		}

		failPct := 100.0
		if c.TotalRuns > 0 {
			failPct = math.Round(float64(c.FailedRuns)/float64(c.TotalRuns)*1000) / 10 // 保留一位小数
		}

		slog.Info(
			"autopilot failure monitor: paused autopilot",
			"autopilot_id", util.UUIDToString(c.ID),
			"workspace_id", util.UUIDToString(c.WorkspaceID),
			"title", c.Title,
			"failed_runs", c.FailedRuns,
			"total_runs", c.TotalRuns,
			"fail_pct", failPct,
		)

		emitAutopilotPausedNotifications(ctx, queries, bus, paused, c, cfg, failPct)

		// 把状态变更广播出去，让任何开着的 UI 即时刷新该 autopilot 行。
		workspaceID := util.UUIDToString(paused.WorkspaceID)
		bus.Publish(events.Event{
			Type:        protocol.EventAutopilotUpdated,
			WorkspaceID: workspaceID,
			ActorType:   "system",
			Payload: map[string]any{
				"autopilot": autopilotEventPayload(paused),
				"reason":    "auto_paused_high_failure_rate",
			},
		})
	}
}

// emitAutopilotPausedNotifications 为每个相关接收者创建一条收件箱消息并发布
// inbox:new 事件，让通知实时到达。接收者：
//
//  1. autopilot 创建者本人（若他是成员）；
//  2. 若创建者是 agent，则解析到该 agent 的 owner_id（映射成工作区成员）。
//
// 通过 owner_id 解析，是为了不把消息发给"收件箱不可操作"的 agent，同时又能
// 把告警归属到真正搭这套 autopilot 的人身上。两条路径都落不到人（例如 agent
// 没有 owner）就静默跳过——WS 上的 autopilot:updated 事件仍会把变更推到任何
// 登录工作区成员的 UI 上。
func emitAutopilotPausedNotifications(
	ctx context.Context,
	queries *db.Queries,
	bus *events.Bus,
	autopilot db.Autopilot,
	candidate db.SelectAutopilotsExceedingFailureThresholdRow,
	cfg failureMonitorConfig,
	failPct float64,
) {
	eventsToPublish, err := emitAutopilotPausedNotificationsTx(ctx, queries, autopilot, candidate, cfg, failPct)
	if err != nil {
		slog.Warn("autopilot failure monitor: create pause notifications failed", "autopilot_id", util.UUIDToString(autopilot.ID), "error", err)
		return
	}
	for _, event := range eventsToPublish {
		bus.Publish(event)
	}
}

// emitAutopilotPausedNotificationsTx 生成"暂停通知"相关的事件列表（可能为空）。
// 每个接收者一条收件箱条目 + 一条 inbox:new 事件；接收者去重（同人既是创建者
// 又出现在别的路径时不重复发）。事务版与非事务版共用本函数，因此通知内容是
// 一致的。body 里带失败数、失败率与回溯窗口，并引导去 autopilot 页面排查后
// 重新启用。
func emitAutopilotPausedNotificationsTx(
	ctx context.Context,
	queries *db.Queries,
	autopilot db.Autopilot,
	candidate db.SelectAutopilotsExceedingFailureThresholdRow,
	cfg failureMonitorConfig,
	failPct float64,
) ([]events.Event, error) {
	recipients := resolveAutopilotPausedRecipients(ctx, queries, autopilot)
	if len(recipients) == 0 {
		return nil, nil
	}

	title := fmt.Sprintf("Autopilot paused: %s", autopilot.Title)
	body := fmt.Sprintf(
		"Auto-paused after %d of %d runs failed (%.1f%%) in the last %s. Investigate the failures, fix the root cause, then re-enable from the autopilot page.",
		candidate.FailedRuns, candidate.TotalRuns, failPct, formatLookback(cfg.Lookback),
	)
	details, _ := json.Marshal(map[string]any{
		"autopilot_id":         util.UUIDToString(autopilot.ID),
		"autopilot_title":      autopilot.Title,
		"failed_runs":          candidate.FailedRuns,
		"total_runs":           candidate.TotalRuns,
		"fail_pct":             failPct,
		"lookback_seconds":     int64(cfg.Lookback.Seconds()),
		"threshold_min_runs":   cfg.MinRuns,
		"threshold_fail_ratio": cfg.FailRatio,
		"reason":               "auto_paused_high_failure_rate",
	})

	workspaceID := util.UUIDToString(autopilot.WorkspaceID)

	emitted := make(map[string]bool, len(recipients))
	eventsToPublish := make([]events.Event, 0, len(recipients))
	for _, r := range recipients {
		key := r.Type + ":" + util.UUIDToString(r.ID)
		if emitted[key] {
			continue
		}
		emitted[key] = true

		item, err := queries.CreateInboxItem(ctx, db.CreateInboxItemParams{
			ID:            dbid.NewV7(),
			WorkspaceID:   autopilot.WorkspaceID,
			RecipientType: r.Type,
			RecipientID:   r.ID,
			Type:          "autopilot_paused",
			Severity:      "attention",
			IssueID:       pgtype.UUID{},
			Title:         title,
			Body:          util.StrToText(body),
			ActorType:     util.StrToText("system"),
			ActorID:       pgtype.UUID{},
			Details:       details,
		})
		if err != nil {
			return nil, fmt.Errorf("create pause inbox for %s:%s: %w", r.Type, util.UUIDToString(r.ID), err)
		}

		eventsToPublish = append(eventsToPublish, events.Event{
			Type:           protocol.EventInboxNew,
			IdempotencyKey: "inbox:new:" + util.UUIDToString(item.ID),
			StreamKey:      "inbox:" + util.UUIDToString(r.ID),
			WorkspaceID:    workspaceID,
			ActorType:      "system",
			ActorID:        "",
			Payload:        map[string]any{"item": service.InboxItemFields(item)},
		})
	}
	return eventsToPublish, nil
}

// pausedRecipient 标识一个收件箱消息的接收者。
type pausedRecipient struct {
	Type string // 取值 "member" 或 "agent"
	ID   pgtype.UUID
}

// resolveAutopilotPausedRecipients 解析"该通知谁"：创建者是成员就直接给他；
// 创建者是 agent 则顺藤摸瓜找到它的负责人（必须是工作区成员），落不到人返回空。
func resolveAutopilotPausedRecipients(
	ctx context.Context,
	queries *db.Queries,
	autopilot db.Autopilot,
) []pausedRecipient {
	if autopilot.CreatedByType == "member" {
		return []pausedRecipient{{Type: "member", ID: autopilot.CreatedByID}}
	}

	// Creator is an agent — find the agent's human owner so the alert lands
	// somewhere actionable. If we can't resolve a member, skip notification
	// rather than spam an agent that can't act on it.
	//
	// 中文说明：创建者是 agent——找到它的负责人（人类成员），让告警落到能干活
	// 的人手里。若解析不到成员就跳过通知，绝不向无法行动的 agent 灌消息。
	agent, err := queries.GetAgent(ctx, autopilot.CreatedByID)
	if err != nil {
		slog.Debug("autopilot failure monitor: failed to load creator agent",
			"agent_id", util.UUIDToString(autopilot.CreatedByID),
			"error", err,
		)
		return nil
	}
	if !agent.OwnerID.Valid {
		return nil
	}

	member, err := queries.GetMemberByUserAndWorkspace(ctx, db.GetMemberByUserAndWorkspaceParams{
		UserID:      agent.OwnerID,
		WorkspaceID: autopilot.WorkspaceID,
	})
	if err != nil {
		return nil
	}
	return []pausedRecipient{{Type: "member", ID: member.UserID}}
}

// autopilotEventPayload 组装前端监听器消费的最简负载形状（对齐
// handler.AutopilotResponse）。放在这里而不是 import handler 包，是为了避免
// 循环依赖（handler 依赖 service，而本文件与 service 同处 cmd/server 包内）。
func autopilotEventPayload(a db.Autopilot) map[string]any {
	return map[string]any{
		"id":                   util.UUIDToString(a.ID),
		"workspace_id":         util.UUIDToString(a.WorkspaceID),
		"title":                a.Title,
		"description":          util.TextToPtr(a.Description),
		"assignee_id":          util.UUIDToString(a.AssigneeID),
		"status":               a.Status,
		"execution_mode":       a.ExecutionMode,
		"issue_title_template": util.TextToPtr(a.IssueTitleTemplate),
		"created_by_type":      a.CreatedByType,
		"created_by_id":        util.UUIDToString(a.CreatedByID),
		"last_run_at":          util.TimestampToPtr(a.LastRunAt),
		"created_at":           util.TimestampToString(a.CreatedAt),
		"updated_at":           util.TimestampToString(a.UpdatedAt),
	}
}

// isNoRows 包装 pgx 的" :one 查询没匹配到行"哨兵错误。SystemPauseAutopilot
// 这条 UPDATE 在 autopilot 已被暂停/归档时不会返回行，我们希望把它当作无害
// 的空操作，而不是需要记日志的错误。
func isNoRows(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}

// formatLookback 把回溯窗口时长格式化成人类可读文案（"7 days"、"3 hours"），
// 整单位优先；不能整除时退回 time.Duration 的原生格式。
func formatLookback(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	hours := d / time.Hour
	if hours >= 24 && d%(24*time.Hour) == 0 {
		days := hours / 24
		if days == 1 {
			return "1 day"
		}
		return fmt.Sprintf("%d days", days)
	}
	if d%time.Hour == 0 {
		if hours == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", hours)
	}
	return d.String()
}

// envDurationOrZero 解析时长类环境变量：显式的 0/负值会被尊重（用于禁用监控
// 器）；空值返回默认；解析失败打 warn 并回退默认。
func envDurationOrZero(name string, def time.Duration) time.Duration {
	raw := os.Getenv(name)
	if raw == "" {
		return def
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		slog.Warn("invalid env var, using default", "name", name, "value", raw, "default", def.String(), "error", err)
		return def
	}
	return v
}

// envDurationPositive 解析必须为正的时长环境变量：空值回默认；非法或非正数
// 打 warn 并回退默认。
func envDurationPositive(name string, def time.Duration) time.Duration {
	raw := os.Getenv(name)
	if raw == "" {
		return def
	}
	v, err := time.ParseDuration(raw)
	if err != nil || v <= 0 {
		slog.Warn("invalid env var, using default", "name", name, "value", raw, "default", def.String(), "error", err)
		return def
	}
	return v
}

// envDurationNonNegative 解析允许为 0 的时长环境变量（0 可用来关掉启动延迟）：
// 空值回默认；非法或负数打 warn 并回退默认。
func envDurationNonNegative(name string, def time.Duration) time.Duration {
	raw := os.Getenv(name)
	if raw == "" {
		return def
	}
	v, err := time.ParseDuration(raw)
	if err != nil || v < 0 {
		slog.Warn("invalid env var, using default", "name", name, "value", raw, "default", def.String(), "error", err)
		return def
	}
	return v
}

// envInt64Positive 解析正 int64 环境变量。为空返回 (0,false)；非法或非正数打
// warn 并同样返回 (0,false)，由调用方决定回退。
func envInt64Positive(name string) (int64, bool) {
	raw := os.Getenv(name)
	if raw == "" {
		return 0, false
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v <= 0 {
		slog.Warn("invalid env var, ignored", "name", name, "value", raw, "error", err)
		return 0, false
	}
	return v, true
}

// envFloatInUnitInterval 解析 (0,1] 区间内的浮点环境变量（用于失败率阈值）。
// 为空返回 (0,false)；非法或越界打 warn 并返回 (0,false)。
func envFloatInUnitInterval(name string) (float64, bool) {
	raw := os.Getenv(name)
	if raw == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v <= 0 || v > 1 {
		slog.Warn("invalid env var (must be in (0,1]), ignored", "name", name, "value", raw, "error", err)
		return 0, false
	}
	return v, true
}
