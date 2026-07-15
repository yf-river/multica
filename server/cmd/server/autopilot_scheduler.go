package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const schedulerInterval = 30 * time.Second

// runAutopilotScheduler polls for due schedule triggers and dispatches them.
func runAutopilotScheduler(ctx context.Context, queries *db.Queries, svc *service.AutopilotService) {
	// Recover triggers that were claimed but never advanced (e.g. after a crash).
	recoverLostTriggers(ctx, queries)

	ticker := time.NewTicker(schedulerInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickScheduledAutopilots(ctx, queries, svc)
		}
	}
}

// recoverLostTriggers finds schedule triggers whose next_run_at is NULL
// (claimed but never advanced, typically after a crash) and recomputes it.
func recoverLostTriggers(ctx context.Context, queries *db.Queries) {
	triggers, err := queries.RecoverLostTriggers(ctx)
	if err != nil {
		slog.Warn("autopilot scheduler: failed to recover lost triggers", "error", err)
		return
	}
	if len(triggers) == 0 {
		return
	}

	slog.Info("autopilot scheduler: recovering lost triggers", "count", len(triggers))
	for _, t := range triggers {
		if !t.CronExpression.Valid || t.CronExpression.String == "" {
			continue
		}
		if err := advanceTriggerNextRun(ctx, queries, t.ID, t.CronExpression.String, t.Timezone); err != nil {
			slog.Warn("autopilot scheduler: failed to recover trigger",
				"trigger_id", util.UUIDToString(t.ID), "error", err)
		}
	}
}

// tickScheduledAutopilots claims all due triggers and dispatches each one.
func tickScheduledAutopilots(ctx context.Context, queries *db.Queries, svc *service.AutopilotService) {
	triggers, err := queries.ClaimDueScheduleTriggers(ctx)
	if err != nil {
		slog.Warn("autopilot scheduler: failed to claim due triggers", "error", err)
		return
	}
	if len(triggers) == 0 {
		return
	}

	slog.Info("autopilot scheduler: claimed due triggers", "count", len(triggers))

	for _, t := range triggers {
		autopilot, err := queries.GetAutopilot(ctx, t.AutopilotID)
		if err != nil {
			slog.Warn("autopilot scheduler: failed to load autopilot",
				"trigger_id", util.UUIDToString(t.ID),
				"autopilot_id", util.UUIDToString(t.AutopilotID),
				"error", err,
			)
			continue
		}

		// Dispatch the autopilot run.
		if _, err := svc.DispatchAutopilot(ctx, autopilot, t.ID, "schedule", nil); err != nil {
			slog.Warn("autopilot scheduler: dispatch failed",
				"autopilot_id", util.UUIDToString(autopilot.ID),
				"trigger_id", util.UUIDToString(t.ID),
				"error", err,
			)
		}

		// Advance next_run_at for this trigger.
		if t.CronExpression.Valid && t.CronExpression.String != "" {
			if err := advanceTriggerNextRun(ctx, queries, t.ID, t.CronExpression.String, t.Timezone); err != nil {
				slog.Warn("autopilot scheduler: failed to advance next_run_at",
					"trigger_id", util.UUIDToString(t.ID),
					"cron", t.CronExpression.String,
					"error", err,
				)
			}
		}
	}
}

func advanceTriggerNextRun(ctx context.Context, queries *db.Queries, id pgtype.UUID, cron string, timezone pgtype.Text) error {
	tz := service.DefaultAutopilotTriggerTimezone
	if timezone.Valid && timezone.String != "" {
		tz = timezone.String
	}
	next, err := service.ComputeNextRun(cron, tz)
	if err != nil {
		return err
	}
	return queries.AdvanceTriggerNextRun(ctx, db.AdvanceTriggerNextRunParams{
		ID:        id,
		NextRunAt: pgtype.Timestamptz{Time: next, Valid: true},
	})
}
