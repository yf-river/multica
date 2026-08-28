package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	lifeCognitionPollInterval = 15 * time.Second
	lifeCognitionClaimLimit   = 10
)

type lifeCognitionTaskContext struct {
	Type        string          `json:"type"`
	JobID       string          `json:"job_id"`
	JobType     string          `json:"job_type"`
	WorkspaceID string          `json:"workspace_id"`
	UserID      string          `json:"user_id"`
	Input       json.RawMessage `json:"input"`
}

type lifeCognitionJobInput struct {
	MaterialID       string   `json:"material_id"`
	MaterialIDs      []string `json:"material_ids"`
	SourceType       string   `json:"source_type"`
	SourceKey        string   `json:"source_key"`
	SourceRevision   string   `json:"source_revision"`
	ChatSessionID    string   `json:"chat_session_id"`
	ThroughMessageID string   `json:"through_message_id"`
	PeriodKind       string   `json:"period_kind"`
	PeriodStart      string   `json:"period_start"`
	PeriodEnd        string   `json:"period_end"`
}

func runLifeCognitionWorker(ctx context.Context, queries *db.Queries) {
	tickLifeCognitionJobs(ctx, queries)
	ticker := time.NewTicker(lifeCognitionPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickLifeCognitionJobs(ctx, queries)
		}
	}
}

func tickLifeCognitionJobs(ctx context.Context, queries *db.Queries) {
	if _, err := queries.ExpireLifeActionProposals(ctx); err != nil {
		slog.Warn("life cognition: expire proposals", "error", err)
	}
	scheduleLifeCognitionJobs(ctx, queries, time.Now())
	reconcileLifeCognitionJobs(ctx, queries)
	jobs, err := queries.ClaimDueLifeCognitionJobs(ctx, lifeCognitionClaimLimit)
	if err != nil {
		slog.Warn("life cognition: claim failed", "error", err)
		return
	}
	for _, job := range jobs {
		agent, err := queries.GetAgent(ctx, job.CompanionAgentID)
		if err != nil || agent.ArchivedAt.Valid {
			failLifeCognitionJob(ctx, queries, job.ID, fmt.Errorf("companion agent unavailable: %w", err))
			continue
		}
		input := append(json.RawMessage(nil), job.Input...)
		var inputObject map[string]any
		if json.Unmarshal(input, &inputObject) != nil || inputObject == nil {
			inputObject = map[string]any{}
		}
		if err := attachLifeJobSources(ctx, queries, job, inputObject); err != nil {
			failLifeCognitionJob(ctx, queries, job.ID, err)
			continue
		}
		input, err = json.Marshal(inputObject)
		if err != nil {
			failLifeCognitionJob(ctx, queries, job.ID, err)
			continue
		}
		payload, err := json.Marshal(lifeCognitionTaskContext{
			Type: "life_cognition", JobID: util.UUIDToString(job.ID), JobType: job.JobType,
			WorkspaceID: util.UUIDToString(job.WorkspaceID), UserID: util.UUIDToString(job.UserID), Input: input,
		})
		if err != nil {
			failLifeCognitionJob(ctx, queries, job.ID, err)
			continue
		}
		task, err := queries.CreateLifeCognitionAgentTask(ctx, db.CreateLifeCognitionAgentTaskParams{
			AgentID: job.CompanionAgentID, RuntimeID: agent.RuntimeID, Context: payload,
			InitiatorUserID: job.UserID,
			TriggerSummary:  pgtype.Text{String: "人生后台任务：" + job.JobType, Valid: true},
		})
		if err != nil {
			failLifeCognitionJob(ctx, queries, job.ID, err)
			continue
		}
		if err := queries.AttachLifeCognitionJobTask(ctx, db.AttachLifeCognitionJobTaskParams{ID: job.ID, TaskID: task.ID}); err != nil {
			failLifeCognitionJob(ctx, queries, job.ID, err)
			continue
		}
		slog.Info("life cognition: queued companion task", "job_id", util.UUIDToString(job.ID), "job_type", job.JobType, "task_id", util.UUIDToString(task.ID))
	}
}

func attachLifeJobSources(ctx context.Context, queries *db.Queries, job db.LifeCognitionJob, inputObject map[string]any) error {
	var spec lifeCognitionJobInput
	if err := json.Unmarshal(job.Input, &spec); err != nil {
		return fmt.Errorf("decode life cognition input: %w", err)
	}
	var materials []db.LifeMaterial
	var err error
	switch job.JobType {
	case "understand_materials":
		materials, err = resolveLifeJobMaterials(ctx, queries, job, spec)
	case "observer_run":
		if spec.PeriodStart != "" && spec.PeriodEnd != "" {
			materials, err = listLifeMaterialsForPeriod(ctx, queries, job, spec.PeriodStart, spec.PeriodEnd)
		}
	case "chronicle_generate":
		return attachChronicleJobSources(ctx, queries, job, spec, inputObject)
	}
	if err != nil {
		return err
	}
	if len(materials) > 0 {
		inputObject["new_materials"] = lifeMaterialItems(materials, true)
	}
	return nil
}

func resolveLifeJobMaterials(ctx context.Context, queries *db.Queries, job db.LifeCognitionJob, spec lifeCognitionJobInput) ([]db.LifeMaterial, error) {
	if len(spec.MaterialIDs) > 0 || spec.MaterialID != "" {
		rawIDs := append([]string(nil), spec.MaterialIDs...)
		if spec.MaterialID != "" {
			rawIDs = append(rawIDs, spec.MaterialID)
		}
		ids := make([]pgtype.UUID, 0, len(rawIDs))
		for _, rawID := range rawIDs {
			id, err := util.ParseUUID(rawID)
			if err != nil {
				return nil, fmt.Errorf("invalid life material id: %w", err)
			}
			ids = append(ids, id)
		}
		return queries.ListLifeMaterialsByIDs(ctx, db.ListLifeMaterialsByIDsParams{
			WorkspaceID: job.WorkspaceID, UserID: job.UserID, MaterialIds: ids,
		})
	}
	if spec.ChatSessionID != "" && spec.ThroughMessageID != "" {
		return queries.ListLifeChatTurnMaterials(ctx, db.ListLifeChatTurnMaterialsParams{
			WorkspaceID: job.WorkspaceID, UserID: job.UserID,
			ChatSessionID: spec.ChatSessionID, ThroughMessageID: spec.ThroughMessageID,
		})
	}
	if spec.SourceType != "" && spec.SourceKey != "" && spec.SourceRevision != "" {
		material, err := queries.GetLifeMaterialBySourceRevision(ctx, db.GetLifeMaterialBySourceRevisionParams{
			WorkspaceID: job.WorkspaceID, UserID: job.UserID, SourceType: spec.SourceType,
			SourceKey: spec.SourceKey, SourceRevision: spec.SourceRevision,
		})
		if err != nil {
			return nil, err
		}
		return []db.LifeMaterial{material}, nil
	}
	return nil, fmt.Errorf("understand_materials job has no exact material source")
}

func listLifeMaterialsForPeriod(ctx context.Context, queries *db.Queries, job db.LifeCognitionJob, startRaw, endRaw string) ([]db.LifeMaterial, error) {
	start, err := time.Parse(time.RFC3339, startRaw)
	if err != nil {
		return nil, fmt.Errorf("invalid period_start: %w", err)
	}
	end, err := time.Parse(time.RFC3339, endRaw)
	if err != nil {
		return nil, fmt.Errorf("invalid period_end: %w", err)
	}
	return queries.ListLifeMaterialsInPeriod(ctx, db.ListLifeMaterialsInPeriodParams{
		WorkspaceID: job.WorkspaceID, UserID: job.UserID,
		PeriodStart: pgtype.Timestamptz{Time: start, Valid: true},
		PeriodEnd:   pgtype.Timestamptz{Time: end, Valid: true},
	})
}

func attachChronicleJobSources(ctx context.Context, queries *db.Queries, job db.LifeCognitionJob, spec lifeCognitionJobInput, inputObject map[string]any) error {
	materials, err := listLifeMaterialsForPeriod(ctx, queries, job, spec.PeriodStart, spec.PeriodEnd)
	if err != nil {
		return err
	}
	if spec.PeriodKind == "day" {
		inputObject["new_materials"] = lifeMaterialItems(materials, true)
		return nil
	}
	inputObject["material_index"] = lifeMaterialItems(materials, false)
	childKind := "day"
	if spec.PeriodKind == "year" {
		childKind = "month"
	}
	start, _ := time.Parse(time.RFC3339, spec.PeriodStart)
	end, _ := time.Parse(time.RFC3339, spec.PeriodEnd)
	entries, err := queries.ListLifeChroniclesInPeriod(ctx, db.ListLifeChroniclesInPeriodParams{
		WorkspaceID: job.WorkspaceID, UserID: job.UserID, PeriodKind: childKind,
		PeriodStart: pgtype.Timestamptz{Time: start, Valid: true}, PeriodEnd: pgtype.Timestamptz{Time: end, Valid: true},
	})
	if err != nil {
		return err
	}
	items := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		evidence, err := queries.ListLifeChronicleEvidence(ctx, entry.ID)
		if err != nil {
			return err
		}
		evidenceItems := make([]map[string]any, 0, len(evidence))
		for _, source := range evidence {
			evidenceItems = append(evidenceItems, map[string]any{"source_type": source.SourceType, "source_id": util.UUIDToString(source.SourceID)})
		}
		items = append(items, map[string]any{
			"id": util.UUIDToString(entry.ID), "period_kind": entry.PeriodKind,
			"period_start": entry.PeriodStart.Time.Format(time.RFC3339), "period_end": entry.PeriodEnd.Time.Format(time.RFC3339),
			"facts": entry.Facts, "feelings": entry.Feelings, "understanding_then": entry.UnderstandingThen,
			"understanding_later": entry.UnderstandingLater, "actions": entry.Actions, "evidence": evidenceItems,
		})
	}
	inputObject["period_chronicles"] = items
	return nil
}

func lifeMaterialItems(materials []db.LifeMaterial, includeContent bool) []map[string]any {
	items := make([]map[string]any, 0, len(materials))
	for _, material := range materials {
		item := map[string]any{
			"id": util.UUIDToString(material.ID), "source_type": material.SourceType,
			"source_key": material.SourceKey, "source_revision": material.SourceRevision,
			"occurred_at": material.OccurredAt.Time.Format(time.RFC3339Nano),
		}
		if includeContent {
			item["content"] = material.Content
			item["metadata"] = json.RawMessage(material.Metadata)
		} else {
			item["excerpt"] = lifeTextExcerpt(material.Content, 240)
		}
		items = append(items, item)
	}
	return items
}

func lifeTextExcerpt(value string, maxRunes int) string {
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes]) + "…"
}

func scheduleLifeCognitionJobs(ctx context.Context, queries *db.Queries, now time.Time) {
	scheduleLifeProactiveChecks(ctx, queries, now)
	scheduleLifeProactiveReviews(ctx, queries, now)
	scheduleLifeMemoryReviews(ctx, queries, now)
	scheduleLifeThoughtDevelopment(ctx, queries, now)
	scheduleLifeCommitmentReviews(ctx, queries, now)
	scheduleLifeRelationshipReviews(ctx, queries, now)
	scheduleLifeExperimentChecks(ctx, queries, now)
	scheduleLifeObserverRuns(ctx, queries, now)
	scheduleLifeChronicles(ctx, queries, now)
}

func scheduleLifeProactiveReviews(ctx context.Context, queries *db.Queries, now time.Time) {
	rows, err := queries.ListPendingLifeProactiveReviews(ctx, 100)
	if err != nil {
		slog.Warn("life cognition: list proactive reviews", "error", err)
		return
	}
	for _, row := range rows {
		err = createScheduledLifeJob(ctx, queries, row.WorkspaceID, row.UserID, row.AgentID,
			"proactive_review", "proactive-review:"+util.UUIDToString(row.ID), map[string]any{
				"check_id": util.UUIDToString(row.ID), "reason": row.Reason, "message": row.Message,
				"checked_at": timestampValue(row.CheckedAt), "user_responded_at": timestampValue(row.UserRespondedAt),
			}, now)
		if err != nil {
			slog.Warn("life cognition: schedule proactive review", "error", err)
		}
	}
}

func scheduleLifeThoughtDevelopment(ctx context.Context, queries *db.Queries, now time.Time) {
	rows, err := queries.ListDueLifeInternalThoughts(ctx, 100)
	if err != nil {
		slog.Warn("life cognition: list internal thoughts", "error", err)
		return
	}
	for _, row := range rows {
		err = createScheduledLifeJob(ctx, queries, row.WorkspaceID, row.UserID, row.CompanionAgentID,
			"develop_thought", "thought:"+util.UUIDToString(row.ID)+":"+row.LastDevelopedAt.Time.UTC().Format(time.RFC3339),
			map[string]any{"thought_id": util.UUIDToString(row.ID), "type": row.ThoughtType, "title": row.Title, "content": row.Content, "metadata": json.RawMessage(row.Metadata)}, now)
		if err == nil {
			_ = queries.MarkLifeInternalThoughtScheduled(ctx, row.ID)
		}
	}
}

func createScheduledLifeJob(ctx context.Context, queries *db.Queries, workspaceID, userID, agentID pgtype.UUID, jobType, dedupeKey string, input any, scheduledAt time.Time) error {
	raw, err := json.Marshal(input)
	if err != nil {
		return err
	}
	_, err = queries.CreateLifeCognitionJob(ctx, db.CreateLifeCognitionJobParams{
		WorkspaceID: workspaceID, UserID: userID, CompanionAgentID: agentID,
		JobType: jobType, DedupeKey: dedupeKey, Input: raw,
		ScheduledAt: pgtype.Timestamptz{Time: scheduledAt, Valid: true},
	})
	return err
}

func scheduleLifeProactiveChecks(ctx context.Context, queries *db.Queries, now time.Time) {
	rows, err := queries.ListDueLifeProactivePolicies(ctx, 100)
	if err != nil {
		slog.Warn("life cognition: list proactive policies", "error", err)
		return
	}
	for _, row := range rows {
		due := row.NextCheckAt.Time
		err = createScheduledLifeJob(ctx, queries, row.WorkspaceID, row.UserID, row.AgentID,
			"proactive_check", "scheduled:"+due.UTC().Format(time.RFC3339), map[string]any{
				"reason": "定期检查现在是否有值得主动开口的事", "quiet_hours": json.RawMessage(row.QuietHours),
				"timezone": row.Timezone, "unanswered_count": row.UnansweredCount,
			}, now)
		if err != nil {
			slog.Warn("life cognition: schedule proactive check", "error", err)
			continue
		}
		next := now.Add(pgIntervalDuration(row.MinimumInterval, 12*time.Hour))
		if err = queries.AdvanceLifeProactivePolicy(ctx, db.AdvanceLifeProactivePolicyParams{
			WorkspaceID: row.WorkspaceID, UserID: row.UserID,
			NextCheckAt: pgtype.Timestamptz{Time: next, Valid: true},
		}); err != nil {
			slog.Warn("life cognition: advance proactive policy", "error", err)
		}
	}
}

func scheduleLifeMemoryReviews(ctx context.Context, queries *db.Queries, now time.Time) {
	rows, err := queries.ListDueLifeMemoryReviews(ctx, 100)
	if err != nil {
		slog.Warn("life cognition: list memory reviews", "error", err)
		return
	}
	for _, row := range rows {
		err = createScheduledLifeJob(ctx, queries, row.WorkspaceID, row.UserID, row.AgentID,
			"review_memories", "memory:"+util.UUIDToString(row.ID)+":"+row.ReviewAfter.Time.UTC().Format(time.RFC3339),
			map[string]any{"memory_id": util.UUIDToString(row.ID), "content": row.Content, "status": row.Status}, now)
		if err != nil {
			slog.Warn("life cognition: schedule memory review", "error", err)
			continue
		}
		_ = queries.MarkLifeMemoryReviewScheduled(ctx, db.MarkLifeMemoryReviewScheduledParams{
			ID: row.ID, ReviewAfter: pgtype.Timestamptz{Time: now.Add(30 * 24 * time.Hour), Valid: true},
		})
	}
}

func scheduleLifeCommitmentReviews(ctx context.Context, queries *db.Queries, now time.Time) {
	rows, err := queries.ListDueLifeCommitments(ctx, 100)
	if err != nil {
		slog.Warn("life cognition: list commitment reviews", "error", err)
		return
	}
	for _, row := range rows {
		profile, profileErr := queries.GetCompanionProfile(ctx, db.GetCompanionProfileParams{WorkspaceID: row.WorkspaceID, UserID: row.UserID})
		if profileErr != nil {
			continue
		}
		due := row.DueAt
		if row.RevisitAfter.Valid {
			due = row.RevisitAfter
		}
		err = createScheduledLifeJob(ctx, queries, row.WorkspaceID, row.UserID, profile.AgentID,
			"proactive_check", "commitment:"+util.UUIDToString(row.ID)+":"+due.Time.UTC().Format(time.RFC3339),
			map[string]any{"reason": "已经确认的承诺到了回看时间", "commitment_id": util.UUIDToString(row.ID), "content": row.Content}, now)
		if err == nil {
			_ = queries.AdvanceLifeCommitmentRevisit(ctx, db.AdvanceLifeCommitmentRevisitParams{
				ID: row.ID, RevisitAfter: pgtype.Timestamptz{Time: now.Add(24 * time.Hour), Valid: true},
			})
		}
	}
}

func scheduleLifeRelationshipReviews(ctx context.Context, queries *db.Queries, now time.Time) {
	rows, err := queries.ListDueLifeRelationshipEvents(ctx, 100)
	if err != nil {
		slog.Warn("life cognition: list relationship reviews", "error", err)
		return
	}
	for _, row := range rows {
		err = createScheduledLifeJob(ctx, queries, row.WorkspaceID, row.UserID, row.AgentID,
			"relationship_reunion", "event:"+util.UUIDToString(row.ID)+":"+row.RevisitAfter.Time.UTC().Format(time.RFC3339),
			map[string]any{"relationship_event_id": util.UUIDToString(row.ID), "event_type": row.EventType, "context": row.Context}, now)
		if err == nil {
			_ = queries.AdvanceLifeRelationshipEventRevisit(ctx, db.AdvanceLifeRelationshipEventRevisitParams{
				ID: row.ID, RevisitAfter: pgtype.Timestamptz{Time: now.Add(7 * 24 * time.Hour), Valid: true},
			})
		}
	}
}

func scheduleLifeExperimentChecks(ctx context.Context, queries *db.Queries, now time.Time) {
	rows, err := queries.ListRunningLifeExperimentRoundsForChecks(ctx, 100)
	if err != nil {
		slog.Warn("life cognition: list experiment rounds", "error", err)
		return
	}
	day := now.UTC().Format("2006-01-02")
	for _, row := range rows {
		_ = createScheduledLifeJob(ctx, queries, row.WorkspaceID, row.UserID, row.AgentID,
			"experiment_check", "round:"+util.UUIDToString(row.ID)+":"+day,
			map[string]any{"round_id": util.UUIDToString(row.ID), "plan": json.RawMessage(row.Plan), "ends_at": timestampValue(row.EndsAt)}, now)
	}
}

func scheduleLifeObserverRuns(ctx context.Context, queries *db.Queries, now time.Time) {
	rows, err := queries.ListDueLifeObservers(ctx, 100)
	if err != nil {
		slog.Warn("life cognition: list observers", "error", err)
		return
	}
	for _, row := range rows {
		due := row.NextRunAt.Time
		periodStart := now.Add(-30 * 24 * time.Hour)
		if row.LastRunAt.Valid {
			periodStart = row.LastRunAt.Time
		}
		err = createScheduledLifeJob(ctx, queries, row.WorkspaceID, row.UserID, row.AgentID,
			"observer_run", "observer:"+util.UUIDToString(row.ID)+":"+due.UTC().Format(time.RFC3339),
			map[string]any{"observer_id": util.UUIDToString(row.ID), "period_start": periodStart.Format(time.RFC3339), "period_end": now.Format(time.RFC3339)}, now)
		if err == nil {
			_ = queries.AdvanceLifeObserverSchedule(ctx, db.AdvanceLifeObserverScheduleParams{
				ID: row.ID, NextRunAt: pgtype.Timestamptz{Time: now.Add(pgIntervalDuration(row.MinimumInterval, 24*time.Hour)), Valid: true},
			})
		}
	}
}

func scheduleLifeChronicles(ctx context.Context, queries *db.Queries, now time.Time) {
	profiles, err := queries.ListLifeProfilesForScheduling(ctx)
	if err != nil {
		slog.Warn("life cognition: list profiles for chronicles", "error", err)
		return
	}
	for _, profile := range profiles {
		periods, err := queries.ListMissingLifeChroniclePeriods(ctx, db.ListMissingLifeChroniclePeriodsParams{
			WorkspaceID: profile.WorkspaceID, UserID: profile.UserID, MaxPeriods: 32,
			BeforeTime: pgtype.Timestamptz{Time: now, Valid: true},
		})
		if err != nil {
			slog.Warn("life cognition: list missing chronicle periods", "error", err)
			continue
		}
		for _, period := range periods {
			_ = createScheduledLifeJob(ctx, queries, profile.WorkspaceID, profile.UserID, profile.AgentID,
				"chronicle_generate", period.PeriodKind+":"+period.PeriodStart.Time.Format("2006-01-02"), map[string]any{
					"period_kind": period.PeriodKind, "period_start": period.PeriodStart.Time.Format(time.RFC3339), "period_end": period.PeriodEnd.Time.Format(time.RFC3339),
				}, now)
		}
	}
}

func pgIntervalDuration(interval pgtype.Interval, fallback time.Duration) time.Duration {
	if !interval.Valid {
		return fallback
	}
	d := time.Duration(interval.Microseconds)*time.Microsecond + time.Duration(interval.Days)*24*time.Hour + time.Duration(interval.Months)*30*24*time.Hour
	if d <= 0 {
		return fallback
	}
	return d
}

func timestampValue(value pgtype.Timestamptz) any {
	if !value.Valid {
		return nil
	}
	return value.Time.Format(time.RFC3339Nano)
}

func reconcileLifeCognitionJobs(ctx context.Context, queries *db.Queries) {
	rows, err := queries.ListRunningLifeCognitionJobsWithTask(ctx, 100)
	if err != nil {
		slog.Warn("life cognition: reconcile failed", "error", err)
		return
	}
	for _, row := range rows {
		switch row.TaskStatus {
		case "completed":
			failLifeCognitionJob(ctx, queries, row.ID, fmt.Errorf("agent task completed without submitting governed structured output"))
		case "failed", "cancelled":
			errText := row.TaskStatus
			if row.TaskError.Valid && row.TaskError.String != "" {
				errText += ": " + row.TaskError.String
			}
			failLifeCognitionJob(ctx, queries, row.ID, fmt.Errorf("%s", errText))
		}
	}
}

func failLifeCognitionJob(ctx context.Context, queries *db.Queries, id pgtype.UUID, err error) {
	if updateErr := queries.FailLifeCognitionJob(ctx, db.FailLifeCognitionJobParams{ID: id, Error: err.Error()}); updateErr != nil {
		slog.Warn("life cognition: mark failed", "job_id", util.UUIDToString(id), "error", updateErr)
	}
}
