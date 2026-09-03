package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type lifeTxStarter interface {
	Begin(context.Context) (pgx.Tx, error)
}

const (
	lifeCognitionPollInterval   = 15 * time.Second
	lifeCognitionClaimLimit     = 10
	lifeCognitionContextVersion = "life-context-v6"
)

type lifeCognitionTaskContext struct {
	Type           string          `json:"type"`
	JobID          string          `json:"job_id"`
	JobType        string          `json:"job_type"`
	WorkspaceID    string          `json:"workspace_id"`
	UserID         string          `json:"user_id"`
	ClaimToken     string          `json:"claim_token"`
	ContextVersion int64           `json:"context_version_number"`
	Input          json.RawMessage `json:"input"`
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
	ContextVersion   int64    `json:"context_version_number"`
	ProcessingCursor string   `json:"processing_cursor"`
}

func runLifeCognitionWorker(ctx context.Context, queries *db.Queries, starters ...lifeTxStarter) {
	tickLifeCognitionJobs(ctx, queries, starters...)
	ticker := time.NewTicker(lifeCognitionPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tickLifeCognitionJobs(ctx, queries, starters...)
		}
	}
}

func tickLifeCognitionJobs(ctx context.Context, queries *db.Queries, starters ...lifeTxStarter) {
	var starter lifeTxStarter
	if len(starters) > 0 {
		starter = starters[0]
	}
	if _, err := queries.ExpireLifeActionProposals(ctx); err != nil {
		slog.Warn("life cognition: expire proposals", "error", err)
	}
	scheduleLifeCognitionJobs(ctx, queries, starter, time.Now())
	reconcileLifeCognitionJobs(ctx, queries)
	if recovered, recoverErr := recoverExpiredLifeCognitionJobs(ctx, queries, starter); recoverErr != nil {
		slog.Warn("life cognition: recover expired jobs", "error", recoverErr)
	} else if recovered > 0 {
		slog.Warn("life cognition: reclaimed expired jobs", "count", recovered)
	}
	jobs, err := queries.ClaimDueLifeCognitionJobsFenced(ctx, lifeCognitionClaimLimit)
	if err != nil {
		slog.Warn("life cognition: claim failed", "error", err)
		return
	}
	for _, job := range jobs {
		if !job.ClaimToken.Valid || job.ClaimToken.String == "" || job.ContextVersion <= 0 {
			failLifeCognitionJob(ctx, queries, job.ID, fmt.Errorf("life cognition claim did not receive fencing data"), job.ClaimToken, job.ContextVersion)
			continue
		}
		agent, err := queries.GetAgent(ctx, job.CompanionAgentID)
		if err != nil || agent.ArchivedAt.Valid {
			failLifeCognitionJob(ctx, queries, job.ID, fmt.Errorf("companion agent unavailable: %v", err), job.ClaimToken, job.ContextVersion)
			continue
		}
		input := append(json.RawMessage(nil), job.Input...)
		var inputObject map[string]any
		if json.Unmarshal(input, &inputObject) != nil || inputObject == nil {
			inputObject = map[string]any{}
		}
		if err := attachLifeJobSources(ctx, queries, job, inputObject); err != nil {
			failLifeCognitionJob(ctx, queries, job.ID, err, job.ClaimToken, job.ContextVersion)
			continue
		}
		inputObject["context_version"] = lifeCognitionContextVersion
		inputObject["context_version_number"] = job.ContextVersion
		inputObject["processing_cursor"] = job.ProcessingCursor
		input, err = json.Marshal(inputObject)
		if err != nil {
			failLifeCognitionJob(ctx, queries, job.ID, err, job.ClaimToken, job.ContextVersion)
			continue
		}
		payload, err := json.Marshal(lifeCognitionTaskContext{
			Type: "life_cognition", JobID: util.UUIDToString(job.ID), JobType: job.JobType,
			WorkspaceID: util.UUIDToString(job.WorkspaceID), UserID: util.UUIDToString(job.UserID),
			ClaimToken: job.ClaimToken.String, ContextVersion: job.ContextVersion, Input: input,
		})
		if err != nil {
			failLifeCognitionJob(ctx, queries, job.ID, err, job.ClaimToken, job.ContextVersion)
			continue
		}
		task, err := enqueueLifeCognitionTaskAtomically(ctx, queries, starter, job, agent, input, payload)
		if err != nil {
			failLifeCognitionJob(ctx, queries, job.ID, err, job.ClaimToken, job.ContextVersion)
			continue
		}
		slog.Info("life cognition: queued companion task", "job_id", util.UUIDToString(job.ID), "job_type", job.JobType, "task_id", util.UUIDToString(task.ID))
	}
}

// recoverExpiredLifeCognitionJobs settles the queue row and cognition job in
// one transaction.  Recording the cancellation event in that same transaction
// prevents a crash from leaving a terminal task that the durable stream never
// observes.
func recoverExpiredLifeCognitionJobs(ctx context.Context, queries *db.Queries, starter lifeTxStarter) (int, error) {
	if starter == nil {
		return 0, errors.New("life cognition transaction starter is required for recovery")
	}
	const maxRecoveryBatch = 100
	recovered := 0
	for recovered < maxRecoveryBatch {
		tx, err := starter.Begin(ctx)
		if err != nil {
			return recovered, fmt.Errorf("begin life cognition recovery transaction: %w", err)
		}
		qtx := queries.WithTx(tx)
		job, err := qtx.ClaimExpiredLifeCognitionJobForRecovery(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(ctx)
			return recovered, nil
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			return recovered, fmt.Errorf("claim expired life cognition job: %w", err)
		}

		var task db.AgentTaskQueue
		task, err = qtx.RecoverExpiredLifeCognitionTask(ctx, job.TaskID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(ctx)
			return recovered, fmt.Errorf("recover expired life cognition task: %w", err)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			task = db.AgentTaskQueue{}
		}
		if _, err := qtx.RecoverExpiredLifeCognitionJob(ctx, job.ID); err != nil {
			_ = tx.Rollback(ctx)
			return recovered, fmt.Errorf("recover expired life cognition job: %w", err)
		}
		if task.ID.Valid {
			if _, err := service.RecordTaskTerminalEventTx(ctx, qtx, protocol.EventTaskCancelled, task,
				map[string]any{"failure_reason": "runtime_recovery", "error": "life cognition lease expired and was reclaimed"}); err != nil {
				_ = tx.Rollback(ctx)
				return recovered, fmt.Errorf("record recovered Life task event: %w", err)
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return recovered, fmt.Errorf("commit life cognition recovery transaction: %w", err)
		}
		recovered++
	}
	return recovered, nil
}

// enqueueLifeCognitionTaskAtomically keeps the claimed cognition job, its
// bounded input snapshot, and the queue row in one commit. A process crash
// between any two of those writes otherwise leaves either a running job with
// no task or an unowned queued task that can never be reconciled.
func enqueueLifeCognitionTaskAtomically(
	ctx context.Context,
	queries *db.Queries,
	starter lifeTxStarter,
	job db.LifeCognitionJob,
	agent db.Agent,
	input, payload []byte,
) (db.AgentTaskQueue, error) {
	if starter == nil {
		return db.AgentTaskQueue{}, errors.New("life cognition transaction starter is required")
	}
	tx, err := starter.Begin(ctx)
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("begin life cognition enqueue transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := queries.WithTx(tx)
	updated, err := qtx.UpdateRunningLifeCognitionJobInputFenced(ctx, db.UpdateRunningLifeCognitionJobInputFencedParams{
		ID: job.ID, Input: input, ClaimToken: job.ClaimToken, ContextVersion: job.ContextVersion,
	})
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("prepare life cognition input: %w", err)
	}
	if updated != 1 {
		return db.AgentTaskQueue{}, fmt.Errorf("life cognition lease lost while preparing input")
	}
	task, err := qtx.CreateLifeCognitionAgentTask(ctx, db.CreateLifeCognitionAgentTaskParams{
		AgentID: job.CompanionAgentID, RuntimeID: agent.RuntimeID, Context: payload,
		InitiatorUserID: job.UserID,
		TriggerSummary:  pgtype.Text{String: "人生后台任务：" + job.JobType, Valid: true},
	})
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("create life cognition task: %w", err)
	}
	if _, err := service.RecordTaskQueuedEventTx(ctx, qtx, util.UUIDToString(job.WorkspaceID), task); err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("record life cognition task queued event: %w", err)
	}
	attached, err := qtx.AttachLifeCognitionJobTaskFenced(ctx, db.AttachLifeCognitionJobTaskFencedParams{
		ID: job.ID, TaskID: task.ID, ClaimToken: job.ClaimToken, ContextVersion: job.ContextVersion,
	})
	if err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("attach life cognition task: %w", err)
	}
	if attached != 1 {
		return db.AgentTaskQueue{}, fmt.Errorf("life cognition lease lost while attaching task")
	}
	if err := tx.Commit(ctx); err != nil {
		return db.AgentTaskQueue{}, fmt.Errorf("commit life cognition enqueue transaction: %w", err)
	}
	return task, nil
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
		inputObject["new_materials"] = lifeMaterialItems(materials, false)
		inputObject["evidence_refs"] = lifeMaterialReferences(materials)
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
		inputObject["new_materials"] = lifeMaterialItems(materials, false)
		inputObject["evidence_refs"] = lifeMaterialReferences(materials)
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

func lifeMaterialReferences(materials []db.LifeMaterial) []map[string]string {
	refs := make([]map[string]string, 0, len(materials))
	for _, material := range materials {
		refs = append(refs, map[string]string{
			"source_type":     "material",
			"source_id":       util.UUIDToString(material.ID),
			"source_key":      material.SourceKey,
			"source_revision": material.SourceRevision,
		})
	}
	return refs
}

func lifeTextExcerpt(value string, maxRunes int) string {
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes]) + "…"
}

func scheduleLifeCognitionJobs(ctx context.Context, queries *db.Queries, starter lifeTxStarter, now time.Time) {
	scheduleLifeProactiveChecks(ctx, queries, starter, now)
	scheduleLifeProactiveReviews(ctx, queries, starter, now)
	scheduleLifeMemoryReviews(ctx, queries, starter, now)
	scheduleLifeThoughtDevelopment(ctx, queries, starter, now)
	scheduleLifeCommitmentReviews(ctx, queries, starter, now)
	scheduleLifeRelationshipReviews(ctx, queries, starter, now)
	scheduleLifeExperimentChecks(ctx, queries, starter, now)
	scheduleLifeObserverRuns(ctx, queries, starter, now)
	scheduleLifeChronicles(ctx, queries, starter, now)
}

func scheduleLifeProactiveReviews(ctx context.Context, queries *db.Queries, starter lifeTxStarter, now time.Time) {
	rows, err := queries.ListPendingLifeProactiveReviews(ctx, 100)
	if err != nil {
		slog.Warn("life cognition: list proactive reviews", "error", err)
		return
	}
	for _, row := range rows {
		err = createScheduledLifeJob(ctx, queries, starter, row.WorkspaceID, row.UserID, row.AgentID,
			"proactive_review", "proactive-review:"+util.UUIDToString(row.ID), map[string]any{
				"check_id": util.UUIDToString(row.ID), "reason": row.Reason, "message": row.Message,
				"checked_at": timestampValue(row.CheckedAt), "user_responded_at": timestampValue(row.UserRespondedAt),
			}, now)
		if err != nil {
			slog.Warn("life cognition: schedule proactive review", "error", err)
		}
	}
}

func scheduleLifeThoughtDevelopment(ctx context.Context, queries *db.Queries, starter lifeTxStarter, now time.Time) {
	rows, err := queries.ListDueLifeInternalThoughts(ctx, 100)
	if err != nil {
		slog.Warn("life cognition: list internal thoughts", "error", err)
		return
	}
	for _, row := range rows {
		err = createScheduledLifeJobWithMarker(ctx, queries, starter, row.WorkspaceID, row.UserID, row.CompanionAgentID,
			"develop_thought", "thought:"+util.UUIDToString(row.ID)+":"+row.LastDevelopedAt.Time.UTC().Format(time.RFC3339),
			map[string]any{"thought_id": util.UUIDToString(row.ID), "type": row.ThoughtType, "title": row.Title, "content": row.Content, "metadata": json.RawMessage(row.Metadata)}, now,
			func(ctx context.Context, qtx *db.Queries) error {
				return qtx.MarkLifeInternalThoughtScheduled(ctx, row.ID)
			})
		if err != nil {
			slog.Warn("life cognition: schedule thought development", "error", err)
		}
	}
}

func createScheduledLifeJob(ctx context.Context, queries *db.Queries, starter lifeTxStarter, workspaceID, userID, agentID pgtype.UUID, jobType, dedupeKey string, input any, scheduledAt time.Time) error {
	return createScheduledLifeJobWithMarker(ctx, queries, starter, workspaceID, userID, agentID, jobType, dedupeKey, input, scheduledAt, nil)
}

func createScheduledLifeJobWithMarker(ctx context.Context, queries *db.Queries, starter lifeTxStarter, workspaceID, userID, agentID pgtype.UUID, jobType, dedupeKey string, input any, scheduledAt time.Time, marker func(context.Context, *db.Queries) error) error {
	if starter == nil {
		return errors.New("life cognition transaction starter is required for scheduling")
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return err
	}
	tx, err := starter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin life cognition schedule transaction: %w", err)
	}
	defer tx.Rollback(ctx)
	qtx := queries.WithTx(tx)
	_, err = qtx.CreateLifeCognitionJob(ctx, db.CreateLifeCognitionJobParams{
		WorkspaceID: workspaceID, UserID: userID, CompanionAgentID: agentID,
		JobType: jobType, DedupeKey: dedupeKey, Input: raw,
		ScheduledAt: pgtype.Timestamptz{Time: scheduledAt, Valid: true},
	})
	if err != nil {
		return err
	}
	if marker != nil {
		if err := marker(ctx, qtx); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit life cognition schedule transaction: %w", err)
	}
	return nil
}

func scheduleLifeProactiveChecks(ctx context.Context, queries *db.Queries, starter lifeTxStarter, now time.Time) {
	rows, err := queries.ListDueLifeProactivePolicies(ctx, 100)
	if err != nil {
		slog.Warn("life cognition: list proactive policies", "error", err)
		return
	}
	for _, row := range rows {
		due := row.NextCheckAt.Time
		next := now.Add(pgIntervalDuration(row.MinimumInterval, 12*time.Hour))
		err = createScheduledLifeJobWithMarker(ctx, queries, starter, row.WorkspaceID, row.UserID, row.AgentID,
			"proactive_check", "scheduled:"+due.UTC().Format(time.RFC3339), map[string]any{
				"reason": "定期检查现在是否有值得主动开口的事", "quiet_hours": json.RawMessage(row.QuietHours),
				"timezone": row.Timezone, "unanswered_count": row.UnansweredCount,
			}, now, func(ctx context.Context, qtx *db.Queries) error {
				return qtx.AdvanceLifeProactivePolicy(ctx, db.AdvanceLifeProactivePolicyParams{
					WorkspaceID: row.WorkspaceID, UserID: row.UserID,
					NextCheckAt: pgtype.Timestamptz{Time: next, Valid: true},
				})
			})
		if err != nil {
			slog.Warn("life cognition: schedule proactive check", "error", err)
		}
	}
}

func scheduleLifeMemoryReviews(ctx context.Context, queries *db.Queries, starter lifeTxStarter, now time.Time) {
	rows, err := queries.ListDueLifeMemoryReviews(ctx, 100)
	if err != nil {
		slog.Warn("life cognition: list memory reviews", "error", err)
		return
	}
	for _, row := range rows {
		err = createScheduledLifeJobWithMarker(ctx, queries, starter, row.WorkspaceID, row.UserID, row.AgentID,
			"review_memories", "memory:"+util.UUIDToString(row.ID)+":"+row.ReviewAfter.Time.UTC().Format(time.RFC3339),
			map[string]any{"memory_id": util.UUIDToString(row.ID), "content": row.Content, "status": row.Status}, now,
			func(ctx context.Context, qtx *db.Queries) error {
				return qtx.MarkLifeMemoryReviewScheduled(ctx, db.MarkLifeMemoryReviewScheduledParams{
					ID: row.ID, ReviewAfter: pgtype.Timestamptz{Time: now.Add(30 * 24 * time.Hour), Valid: true},
				})
			})
		if err != nil {
			slog.Warn("life cognition: schedule memory review", "error", err)
		}
	}
}

func scheduleLifeCommitmentReviews(ctx context.Context, queries *db.Queries, starter lifeTxStarter, now time.Time) {
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
		err = createScheduledLifeJobWithMarker(ctx, queries, starter, row.WorkspaceID, row.UserID, profile.AgentID,
			"proactive_check", "commitment:"+util.UUIDToString(row.ID)+":"+due.Time.UTC().Format(time.RFC3339),
			map[string]any{"reason": "已经确认的承诺到了回看时间", "commitment_id": util.UUIDToString(row.ID), "content": row.Content}, now,
			func(ctx context.Context, qtx *db.Queries) error {
				return qtx.AdvanceLifeCommitmentRevisit(ctx, db.AdvanceLifeCommitmentRevisitParams{
					ID: row.ID, RevisitAfter: pgtype.Timestamptz{Time: now.Add(24 * time.Hour), Valid: true},
				})
			})
		if err != nil {
			slog.Warn("life cognition: schedule commitment review", "error", err)
		}
	}
}

func scheduleLifeRelationshipReviews(ctx context.Context, queries *db.Queries, starter lifeTxStarter, now time.Time) {
	rows, err := queries.ListDueLifeRelationshipEvents(ctx, 100)
	if err != nil {
		slog.Warn("life cognition: list relationship reviews", "error", err)
		return
	}
	for _, row := range rows {
		err = createScheduledLifeJobWithMarker(ctx, queries, starter, row.WorkspaceID, row.UserID, row.AgentID,
			"relationship_reunion", "event:"+util.UUIDToString(row.ID)+":"+row.RevisitAfter.Time.UTC().Format(time.RFC3339),
			map[string]any{"relationship_event_id": util.UUIDToString(row.ID), "event_type": row.EventType, "context": row.Context}, now,
			func(ctx context.Context, qtx *db.Queries) error {
				return qtx.AdvanceLifeRelationshipEventRevisit(ctx, db.AdvanceLifeRelationshipEventRevisitParams{
					ID: row.ID, RevisitAfter: pgtype.Timestamptz{Time: now.Add(7 * 24 * time.Hour), Valid: true},
				})
			})
		if err != nil {
			slog.Warn("life cognition: schedule relationship review", "error", err)
		}
	}
}

func scheduleLifeExperimentChecks(ctx context.Context, queries *db.Queries, starter lifeTxStarter, now time.Time) {
	rows, err := queries.ListRunningLifeExperimentRoundsForChecks(ctx, 100)
	if err != nil {
		slog.Warn("life cognition: list experiment rounds", "error", err)
		return
	}
	day := now.UTC().Format("2006-01-02")
	for _, row := range rows {
		if err := createScheduledLifeJob(ctx, queries, starter, row.WorkspaceID, row.UserID, row.AgentID,
			"experiment_check", "round:"+util.UUIDToString(row.ID)+":"+day,
			map[string]any{
				"round_id": util.UUIDToString(row.ID), "status": row.Status,
				"plan": json.RawMessage(row.Plan), "starts_at": timestampValue(row.StartsAt),
				"ends_at": timestampValue(row.EndsAt), "stopped_at": timestampValue(row.StoppedAt),
				"stop_reason": row.StopReason,
			}, now); err != nil {
			slog.Warn("life cognition: schedule experiment check", "error", err)
		}
	}
}

func scheduleLifeObserverRuns(ctx context.Context, queries *db.Queries, starter lifeTxStarter, now time.Time) {
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
		next := now.Add(pgIntervalDuration(row.MinimumInterval, 24*time.Hour))
		err = createScheduledLifeJobWithMarker(ctx, queries, starter, row.WorkspaceID, row.UserID, row.AgentID,
			"observer_run", "observer:"+util.UUIDToString(row.ID)+":"+due.UTC().Format(time.RFC3339),
			map[string]any{"observer_id": util.UUIDToString(row.ID), "period_start": periodStart.Format(time.RFC3339), "period_end": now.Format(time.RFC3339)}, now,
			func(ctx context.Context, qtx *db.Queries) error {
				return qtx.AdvanceLifeObserverSchedule(ctx, db.AdvanceLifeObserverScheduleParams{
					ID: row.ID, NextRunAt: pgtype.Timestamptz{Time: next, Valid: true},
				})
			})
		if err != nil {
			slog.Warn("life cognition: schedule observer run", "error", err)
		}
	}
}

func scheduleLifeChronicles(ctx context.Context, queries *db.Queries, starter lifeTxStarter, now time.Time) {
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
			err := createScheduledLifeJob(ctx, queries, starter, profile.WorkspaceID, profile.UserID, profile.AgentID,
				"chronicle_generate", period.PeriodKind+":"+period.PeriodStart.Time.Format("2006-01-02"), map[string]any{
					"period_kind": period.PeriodKind, "period_start": period.PeriodStart.Time.Format(time.RFC3339), "period_end": period.PeriodEnd.Time.Format(time.RFC3339),
				}, now)
			if err != nil {
				slog.Warn("life cognition: schedule chronicle", "error", err,
					"period_kind", period.PeriodKind, "period_start", period.PeriodStart.Time)
				continue
			}
			// The cursor is an acknowledgement of a successfully generated
			// entry, not a reservation.  Advancing it here would lose a period
			// if the queued task or the process died before producing output.
			// The output transaction advances it after the entry and evidence
			// links have committed; the dedupe key prevents duplicate jobs while
			// that task is pending.
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
			failLifeCognitionJob(ctx, queries, row.ID, fmt.Errorf("agent task completed without submitting governed structured output"), row.ClaimToken, row.ContextVersion)
		case "failed", "cancelled":
			errText := row.TaskStatus
			if row.TaskError.Valid && row.TaskError.String != "" {
				errText += ": " + row.TaskError.String
			}
			failLifeCognitionJob(ctx, queries, row.ID, fmt.Errorf("%s", errText), row.ClaimToken, row.ContextVersion)
		}
	}
}

func failLifeCognitionJob(ctx context.Context, queries *db.Queries, id pgtype.UUID, err error, token pgtype.Text, contextVersion int64) {
	var updateErr error
	if token.Valid && token.String != "" {
		changed, fencedErr := queries.FailLifeCognitionJobFenced(ctx, db.FailLifeCognitionJobFencedParams{ID: id, Error: err.Error(), ClaimToken: token, ContextVersion: contextVersion})
		updateErr = fencedErr
		if updateErr == nil && changed != 1 {
			updateErr = fmt.Errorf("life cognition claim already finalized")
		}
	} else {
		updateErr = queries.FailLifeCognitionJob(ctx, db.FailLifeCognitionJobParams{ID: id, Error: err.Error()})
	}
	if updateErr != nil {
		slog.Warn("life cognition: mark failed", "job_id", util.UUIDToString(id), "error", updateErr)
	}
}
