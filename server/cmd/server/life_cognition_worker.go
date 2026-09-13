// life_cognition_worker.go
//
// 职责：调度并派发"人生认知（Life）"后台任务。
// Life 是陪伴型 AI（companion agent）对用户长期记忆/材料做加工的能力集：
// 主动检查、主动复盘、记忆回顾、想法深化、承诺回访、关系回访、实验检查、
// 观察者运行（observer）与编年史（chronicle）生成等。本 worker 每 15 秒
// 转一圈：先把到点该跑的 Life job 建出来（每个 job 都带去重键，防止重复），
// 再把"过期/完成但没产出"的 job 对账收尾，然后批量认领（fenced）到期 job，
// 把入参（含从库里取回的材料/证据）装成 Agent 任务上下文入队。整套写入都用
// 事务 + 栅栏（claim token/context version）保证并发多实例下不重复执行。
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

// lifeTxStarter 抽象"开事务"，让调度/入队/恢复都能在独立事务里原子完成。
type lifeTxStarter interface {
	Begin(context.Context) (pgx.Tx, error)
}

const (
	// lifeCognitionPollInterval 巡检周期：每 15 秒看一次有没有到点的 job。
	lifeCognitionPollInterval = 15 * time.Second
	// lifeCognitionClaimLimit 每轮最多认领的 job 数（防止一轮压垮数据库/队列）。
	lifeCognitionClaimLimit = 10
	// lifeCognitionContextVersion 是塞给 Agent 的上下文描述串（每次改提示结构
	// 应升版本号，历史任务据此识别自己是哪代语境）。
	lifeCognitionContextVersion = "life-context-v6"
)

// lifeCognitionTaskContext 是塞给 Agent 任务的"上下文信封"：标明这是 life_cognition
// 类型的任务、归属哪个 job、工作区与用户是谁，以及携带认领用的 claim token 和
// 输入负载。Agent 侧用 ClaimToken/ContextVersion 回写结果，保证只有当前持有
// 租约的那次执行能提交。
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

// lifeCognitionJobInput 是 job 原始入参的解析形状：描述这次要"吃"哪些材料
// （按 ID、按某次对话、按来源修订、或按时间段），以及时间段参数等。
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

// runLifeCognitionWorker 启动人生认知调度循环：先立刻跑一轮，再每 15 秒跑一轮，
// ctx 取消即退出。
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

// tickLifeCognitionJobs 执行一轮：过期提案先清掉 → 调度各种到点的 job →
// 把"任务完成却没产出/失败"的 job 对账掉 → 收回过期 job → 认领到期 job 并
// 逐个入队成 Agent 任务。
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
		// 认领应带栅栏数据（claim token 与 context version），缺失说明认领
		// 协议被破坏，直接判失败。
		if !job.ClaimToken.Valid || job.ClaimToken.String == "" || job.ContextVersion <= 0 {
			failLifeCognitionJob(ctx, queries, job.ID, fmt.Errorf("life cognition claim did not receive fencing data"), job.ClaimToken, job.ContextVersion)
			continue
		}
		agent, err := queries.GetAgent(ctx, job.CompanionAgentID)
		if err != nil || agent.ArchivedAt.Valid {
			failLifeCognitionJob(ctx, queries, job.ID, fmt.Errorf("companion agent unavailable: %v", err), job.ClaimToken, job.ContextVersion)
			continue
		}
		// 把"按需解析的输入"扩成完整上下文：先把 job 原始输入拷出来（避免改到
		// 原切片），再按 job 类型把对应的材料/证据/子编年史附加进去。
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

// recoverExpiredLifeCognitionJobs 在一个事务里同时收尾"队列行 + job"：job 的
// 租约过期、其配套 Agent 任务仍挂着时，把它判取消并回写终态事件。取消事件也
// 记在同一事务里，能防止崩溃留下"终态任务但可靠流永远看不见"的不一致。
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
			// 任务行真实存在：给它补一个"取消"终态事件，让可靠事件流也知道。
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

// enqueueLifeCognitionTaskAtomically 把"已认领的 job、它的有界输入快照、队列行"
// 放进同一个事务提交。否则任两步写入之间进程一崩，就会留下"job 在跑却没有任务"
// 或"无人认领、永远无法对账的排队任务"。
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
	// 入队前先带栅栏更新 job 输入：claim token 必须仍是自己的，否则说明租约
	// 已被别人抢走（另一个实例/恢复流程），立即中止。
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
	// 任务建好后把 job 指向它；同样是带栅栏的，防止租约在窗口内易主。
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

// attachLifeJobSources 按 job 类型把"要加工的源材料"附加进送 Agent 的输入：
// understand_materials 按指定材料解析；observer_run 按时间段取材料；编年史
// 生成（chronicle_generate）则走 attachChronicleJobSources（除了材料还要带
// 已有子编年史作参照）。
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

// resolveLifeJobMaterials 解析 understand_materials 到底要读哪些材料：优先按
// 显式 MaterialIDs/MaterialID，其次按"某次对话到某条消息为止"，再次按来源
// 修订（source_type+source_key+source_revision）；都没给就直接报错（精确源
// 缺失，不该猜）。
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

// listLifeMaterialsForPeriod 返回某时间段内的全部人生材料（时间段两端按
// RFC3339 校验）。
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

// attachChronicleJobSources 组装编年史生成任务的输入：日编年史只带"新材料 +
// 证据引用"；月/年编年史还要带上该段内已有的子编年史条目（含各自的事实/感受/
// 领悟/行动与证据）作为输入，让模型在上层概括时有据可依。
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

// lifeMaterialItems 把材料行转成给模型的 map 列表。includeContent=false 时
// 只放"摘录"（控制 token 体积），需要完整内容时（如日编年史）由调用方另行
// 读取。
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

// lifeMaterialReferences 生成一段"证据引用"列表（source_type=material），供
// 下游写编年史时引用材料原始出处。
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

// lifeTextExcerpt 按 rune（而非字节）截取文本前 maxRunes 个字符做摘要，超长
// 加省略号，避免切在 UTF-8 字符中间。
func lifeTextExcerpt(value string, maxRunes int) string {
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes]) + "…"
}

// scheduleLifeCognitionJobs 汇总所有类型的调度入口，挨个把到点的 job 建出来。
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

// scheduleLifeProactiveReviews 把"待办的主动复盘"排成 job。
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

// scheduleLifeThoughtDevelopment 把"到点该深化的内部想法"排成 job，并当场把
// 想法标记为已排期（marker），防止下一轮重复排。
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

// createScheduledLifeJob 是 createScheduledLifeJobWithMarker 的不带 marker
// 便捷版。
func createScheduledLifeJob(ctx context.Context, queries *db.Queries, starter lifeTxStarter, workspaceID, userID, agentID pgtype.UUID, jobType, dedupeKey string, input any, scheduledAt time.Time) error {
	return createScheduledLifeJobWithMarker(ctx, queries, starter, workspaceID, userID, agentID, jobType, dedupeKey, input, scheduledAt, nil)
}

// createScheduledLifeJobWithMarker 在事务里"建 job + 可选地推进源头状态
// （marker）"。job 用 dedupeKey 去重（唯一约束），因此重复调度只会静默命中
// 已有行。marker 与建 job 同事务提交，保证两者不会只成一个。
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

// scheduleLifeProactiveChecks 处理"定期主动检查策略"（用户设了想被定期问候/
// 追问的事）：到点就把该用户的一次 proactive_check 排成 job，并把策略的下次
// 检查时间推进到 next。
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

// scheduleLifeMemoryReviews 把"到点该复看的记忆"排成 job，并把下次复看时间推
// 到 30 天后。
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

// scheduleLifeCommitmentReviews 到点提醒用户回看已确认的承诺，并推进下次回看
// 时间到 24 小时后。若找不到该用户的陪伴档案则跳过（没有可派发的 agent）。
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

// scheduleLifeRelationshipReviews 到点安排"关系事件回访"（比如认识纪念日这类
// 值得主动提起的事），下次回访推进到 7 天后。
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

// scheduleLifeExperimentChecks 给正在运行的"人生实验"轮次排每日检查（按天去重，
// 一天一轮）。
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

// scheduleLifeObserverRuns 到点排"观察者运行"：观察区间从上次运行（没有就往前
// 30 天）到当前，并推进下次运行时间（按最小间隔，缺省兜底 24 小时）。
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

// scheduleLifeChronicles 给每个开了 Life 的用户补生成缺失的编年史时段。注意：
// 它"建 job"但绝不在排期时推进查询游标——游标是"成功产出"的确认，不是预约
// （推进太早会丢掉时段）。推进发生在产出事务里（写入条目与证据后）；排队任务
// 未完成期间靠去重键防重复。
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
			// 见函数头注释：此处刻意不推进游标。
		}
	}
}

// pgIntervalDuration 把 PostgreSQL interval 换算成 time.Duration（月按 30 天
// 粗算）；非法/非正时回退 fallback。
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

// timestampValue 把可空时间戳序列化成适合 JSON 的值（空则 nil，前端好处理）。
func timestampValue(value pgtype.Timestamptz) any {
	if !value.Valid {
		return nil
	}
	return value.Time.Format(time.RFC3339Nano)
}

// reconcileLifeCognitionJobs 对账：找出"已挂 Agent 任务、但任务已终态"的
// running job。任务 completed 却没提交受约束的结构化产出，或者任务 failed/
// cancelled，都意味着 job 白跑了——把它判失败，让对应用户侧可见（AI 需主动
// 告知）。
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

// failLifeCognitionJob 把 job 标记为失败。带栅栏数据时走 Fenced 版本（防止把
// 已被新执行持有的 job 误标失败）；没带则直接按 ID 更新。更新失败只记日志——
// 下轮对账/恢复还会再试。
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
