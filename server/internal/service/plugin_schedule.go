// plugin_schedule.go —— 插件 hook 定时调度投影：安装/升级事务内做账本对齐，启停时激活/停用定时行。
package service

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/plugincontract"
)

// reconcilePluginHookSchedules 使持久化的定时投影与调用方安装/升级事务内的已同意 manifest 一致。
// cron/时区未变的行保持其代号（generation）：纯代码升级的插件不得重置调度时钟或让可重试执行变孤儿。
func (s *PluginService) reconcilePluginHookSchedules(
	ctx context.Context,
	queries *db.Queries,
	installation db.PluginInstallation,
	manifest plugincontract.Manifest,
) error {
	existingRows, err := queries.ListPluginHookSchedulesByInstallation(ctx, installation.ID)
	if err != nil {
		return &PluginError{Kind: PluginErrorUnavailable, Message: "list plugin hook schedules", Err: err}
	}
	existing := make(map[string]db.PluginHookSchedule, len(existingRows))
	for _, row := range existingRows {
		existing[row.HookKey] = row
	}

	now := time.Now().UTC()
	for _, hook := range manifest.Contributes.Hooks {
		if hook.Schedule == nil || !HookAllowsTrigger(hook, plugincontract.TriggerSchedule) {
			continue
		}
		nextRun, err := pluginScheduleNextRun(*hook.Schedule, now)
		if err != nil {
			// 已发布的 manifest 校验过。在此暴露偏差，比安装一条运行时无法求值的调度更安全。
			return &PluginError{Kind: PluginErrorInvalid, Message: fmt.Sprintf("schedule for hook %q is invalid", hook.Key), Err: err}
		}
		if !installation.Enabled {
			// 被禁用的安装没有有意义的 “下次” 运行。重新启用会轮换代号并从那一刻计算该投影。
			nextRun = pgtype.Timestamptz{}
		}

		row, found := existing[hook.Key]
		if !found {
			if _, err := queries.CreatePluginHookSchedule(ctx, db.CreatePluginHookScheduleParams{
				InstallationID: installation.ID,
				WorkspaceID:    installation.WorkspaceID,
				HookKey:        hook.Key,
				CronExpression: hook.Schedule.Cron,
				Timezone:       hook.Schedule.Timezone,
				Enabled:        installation.Enabled,
				NextRunAt:      nextRun,
			}); err != nil {
				return &PluginError{Kind: PluginErrorUnavailable, Message: "create plugin hook schedule", Err: err}
			}
			continue
		}
		delete(existing, hook.Key)
		if row.CronExpression == hook.Schedule.Cron && row.Timezone == hook.Schedule.Timezone {
			continue
		}
		if _, err := queries.UpdatePluginHookScheduleDefinition(ctx, db.UpdatePluginHookScheduleDefinitionParams{
			ID:             row.ID,
			CronExpression: hook.Schedule.Cron,
			Timezone:       hook.Schedule.Timezone,
			Enabled:        installation.Enabled,
			NextRunAt:      nextRun,
		}); err != nil {
			return &PluginError{Kind: PluginErrorUnavailable, Message: "update plugin hook schedule", Err: err}
		}
	}

	for _, orphan := range existing {
		if err := queries.DeletePluginHookSchedule(ctx, orphan.ID); err != nil {
			return &PluginError{Kind: PluginErrorUnavailable, Message: "delete removed plugin hook schedule", Err: err}
		}
	}
	return nil
}

// pluginScheduleNextRun 计算插件事务的下一次发生（UTC；无下一次则返回无效 Timestamptz）。
func pluginScheduleNextRun(schedule plugincontract.HookSchedule, after time.Time) (pgtype.Timestamptz, error) {
	next, err := NextOccurrenceAfterUTC(schedule.Cron, schedule.Timezone, after)
	if err != nil {
		return pgtype.Timestamptz{}, err
	}
	if next.IsZero() {
		return pgtype.Timestamptz{}, nil
	}
	return pgtype.Timestamptz{Time: next.UTC(), Valid: true}, nil
}

// setPluginHookSchedulesEnabled 批量启用/停用某安装的全部 hook 定时：停用直接清空 enabled；启用时重算每行
// next_run_at 并复活。
func (s *PluginService) setPluginHookSchedulesEnabled(
	ctx context.Context,
	queries *db.Queries,
	installationID pgtype.UUID,
	enabled bool,
) error {
	if !enabled {
		if err := queries.DisablePluginHookSchedules(ctx, installationID); err != nil {
			return &PluginError{Kind: PluginErrorUnavailable, Message: "disable plugin hook schedules", Err: err}
		}
		return nil
	}

	rows, err := queries.ListPluginHookSchedulesByInstallation(ctx, installationID)
	if err != nil {
		return &PluginError{Kind: PluginErrorUnavailable, Message: "list plugin hook schedules", Err: err}
	}
	now := time.Now().UTC()
	for _, row := range rows {
		nextRun, err := pluginScheduleNextRun(plugincontract.HookSchedule{
			Cron: row.CronExpression, Timezone: row.Timezone,
		}, now)
		if err != nil {
			return &PluginError{Kind: PluginErrorInvalid, Message: fmt.Sprintf("schedule for hook %q is invalid", row.HookKey), Err: err}
		}
		if _, err := queries.ReactivatePluginHookSchedule(ctx, db.ReactivatePluginHookScheduleParams{
			ID:        row.ID,
			NextRunAt: nextRun,
		}); err != nil {
			return &PluginError{Kind: PluginErrorUnavailable, Message: "reactivate plugin hook schedule", Err: err}
		}
	}
	return nil
}
