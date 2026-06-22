package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// SyncPromptEvaluationRunForTask回写绑定到该任务的训练评估运行。
// 返回 synced=false 表示该任务不是训练评估 Agent 运行，不需要处理。
func SyncPromptEvaluationRunForTask(ctx context.Context, q *db.Queries, task db.AgentTaskQueue) (db.PromptEvaluationRun, bool, error) {
	run, err := q.GetPromptEvaluationRunByTask(ctx, task.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return db.PromptEvaluationRun{}, false, nil
		}
		return db.PromptEvaluationRun{}, false, err
	}
	updated, err := SyncPromptEvaluationRunFromTask(ctx, q, run)
	if err != nil {
		return db.PromptEvaluationRun{}, true, err
	}
	return updated, true, nil
}

func (s *TaskService) syncPromptEvaluationRunForTask(ctx context.Context, task db.AgentTaskQueue, source string) {
	if s == nil || s.Queries == nil {
		return
	}
	if _, synced, err := SyncPromptEvaluationRunForTask(ctx, s.Queries, task); err != nil {
		slog.Warn("prompt evaluation auto-sync failed",
			"source", source,
			"task_id", util.UUIDToString(task.ID),
			"status", task.Status,
			"error", err,
		)
	} else if synced {
		slog.Info("prompt evaluation auto-synced",
			"source", source,
			"task_id", util.UUIDToString(task.ID),
			"status", task.Status,
		)
	}
}

func (s *TaskService) reassignPromptEvaluationRunToRetry(ctx context.Context, parent db.AgentTaskQueue, child db.AgentTaskQueue) {
	if s == nil || s.Queries == nil {
		return
	}
	if _, err := s.Queries.ReassignPromptEvaluationRunTask(ctx, db.ReassignPromptEvaluationRunTaskParams{
		TaskID:        parent.ID,
		TaskID_2:      child.ID,
		ChatSessionID: child.ChatSessionID,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return
		}
		slog.Warn("prompt evaluation retry reassign failed",
			"parent_task_id", util.UUIDToString(parent.ID),
			"child_task_id", util.UUIDToString(child.ID),
			"error", err,
		)
		return
	}
	s.syncPromptEvaluationRunForTask(ctx, child, "task_retry")
}

// SyncPromptEvaluationRunFromTask从绑定的 agent_task_queue 重新计算 run、trial 和资产快照。
func SyncPromptEvaluationRunFromTask(ctx context.Context, q *db.Queries, run db.PromptEvaluationRun) (db.PromptEvaluationRun, error) {
	if !run.TaskID.Valid {
		return db.PromptEvaluationRun{}, errors.New("prompt evaluation run is not linked to an agent task")
	}
	task, err := q.GetAgentTaskInWorkspace(ctx, db.GetAgentTaskInWorkspaceParams{
		ID:          run.TaskID,
		WorkspaceID: run.WorkspaceID,
	})
	if err != nil {
		return db.PromptEvaluationRun{}, err
	}
	return syncPromptEvaluationRunWithTask(ctx, q, run, task)
}

func syncPromptEvaluationRunWithTask(ctx context.Context, q *db.Queries, run db.PromptEvaluationRun, task db.AgentTaskQueue) (db.PromptEvaluationRun, error) {
	usages, err := q.GetTaskUsage(ctx, task.ID)
	if err != nil {
		return db.PromptEvaluationRun{}, err
	}
	taskMessages, err := q.ListTaskMessages(ctx, task.ID)
	if err != nil {
		return db.PromptEvaluationRun{}, err
	}
	status, passed, failed, passRate, conclusion, failureReason := promptEvaluationRunStatusFromTask(run, task)
	agentVerdicts, hasStructuredVerdicts := promptEvaluationAgentVerdictsFromTask(run, task, taskMessages)
	if task.Status == "completed" {
		if hasStructuredVerdicts {
			status, passed, failed, passRate, conclusion, failureReason = promptEvaluationRunStatusFromAgentVerdicts(run, agentVerdicts)
		} else {
			total := run.TotalCases
			status = "需人工复核"
			passed = 0
			failed = total
			passRate = 0
			conclusion = "Agent 执行完成，但未返回可机读的逐用例评估结果，需要验收者人工复核"
			failureReason = "缺少结构化逐用例评估结果"
		}
	}
	inputTokens, outputTokens := promptEvaluationUsageTotals(run, usages)
	estimatedCost, unpricedModels := promptEvaluationEstimatedCost(run, usages)
	durationMs := promptEvaluationTaskDurationMs(task, run)
	averageMs := int64(0)
	if run.TotalCases > 0 && durationMs > 0 {
		averageMs = durationMs / int64(run.TotalCases)
	}
	evidence := promptEvaluationTaskEvidence(run, task, usages, taskMessages)
	agentName := util.UUIDToString(task.AgentID)
	if agent, err := q.GetAgent(ctx, task.AgentID); err == nil && strings.TrimSpace(agent.Name) != "" {
		agentName = agent.Name
	}
	metrics := map[string]any{
		"总用例数":          run.TotalCases,
		"通过数":           passed,
		"失败数":           failed,
		"通过率":           passRate,
		"总耗时":           durationMs,
		"平均耗时":          averageMs,
		"输入 token":      inputTokens,
		"输出 token":      outputTokens,
		"输入token":       inputTokens,
		"输出token":       outputTokens,
		"预估成本":          estimatedCost,
		"缺少模型价格":        unpricedModels,
		"执行Agent":       agentName,
		"模型":            run.Model,
		"runtime":       run.RuntimeProvider,
		"trace/task id": util.UUIDToString(task.ID),
		"失败原因":          failureReason,
		"评估结论":          conclusion,
	}
	updated, err := q.UpdatePromptEvaluationRunFromTask(ctx, db.UpdatePromptEvaluationRunFromTaskParams{
		ID:                run.ID,
		WorkspaceID:       run.WorkspaceID,
		Status:            status,
		PassedCases:       passed,
		FailedCases:       failed,
		PassRate:          passRate,
		TotalDurationMs:   durationMs,
		AverageDurationMs: averageMs,
		InputTokens:       inputTokens,
		OutputTokens:      outputTokens,
		EstimatedCost:     estimatedCost,
		FailureReason:     pgtype.Text{String: failureReason, Valid: true},
		Conclusion:        pgtype.Text{String: conclusion, Valid: true},
		Metrics:           mustJSONBytes(metrics),
		Evidence:          mustJSONBytes(evidence),
		StartedAt:         task.StartedAt,
		CompletedAt:       task.CompletedAt,
	})
	if err != nil {
		return db.PromptEvaluationRun{}, err
	}
	trialStatus := promptEvaluationTrialStatusFromRunStatus(status)
	trialFailureReason := failureReason
	if trialStatus == "通过" {
		trialFailureReason = "无"
	}
	perCaseInput, perCaseOutput := promptEvaluationPerCaseUsage(run.TotalCases, inputTokens, outputTokens)
	if err := q.UpdatePromptEvaluationTrialsFromTask(ctx, db.UpdatePromptEvaluationTrialsFromTaskParams{
		RunID:         run.ID,
		WorkspaceID:   run.WorkspaceID,
		Status:        trialStatus,
		InputTokens:   perCaseInput,
		OutputTokens:  perCaseOutput,
		DurationMs:    averageMs,
		FailureReason: trialFailureReason,
		Evidence: mustJSONBytes(map[string]any{
			"run_id":        util.UUIDToString(run.ID),
			"task_id":       util.UUIDToString(task.ID),
			"同步来源":          "Agent task 自动回写",
			"Agent任务状态":     task.Status,
			"评估结论":          conclusion,
			"失败原因":          failureReason,
			"trace/task id": util.UUIDToString(task.ID),
		}),
	}); err != nil {
		return db.PromptEvaluationRun{}, err
	}
	if task.Status == "completed" && hasStructuredVerdicts {
		for _, verdict := range agentVerdicts {
			if err := q.UpdatePromptEvaluationTrialFromAgentVerdict(ctx, db.UpdatePromptEvaluationTrialFromAgentVerdictParams{
				RunID:         run.ID,
				WorkspaceID:   run.WorkspaceID,
				CaseIndex:     verdict.CaseIndex,
				Status:        verdict.Status,
				InputTokens:   perCaseInput,
				OutputTokens:  perCaseOutput,
				DurationMs:    averageMs,
				FailureReason: verdict.FailureReason,
				Output:        mustJSONBytes(verdict.Output),
				Evidence: mustJSONBytes(map[string]any{
					"run_id":        util.UUIDToString(run.ID),
					"task_id":       util.UUIDToString(task.ID),
					"同步来源":          "Agent task 结构化逐用例评估",
					"Agent任务状态":     task.Status,
					"评估结论":          verdict.Conclusion,
					"失败原因":          verdict.FailureReason,
					"trace/task id": util.UUIDToString(task.ID),
					"Agent证据":       verdict.Evidence,
				}),
			}); err != nil {
				return db.PromptEvaluationRun{}, err
			}
		}
	}
	if err := syncPromptEvaluationAssetAgentRunSnapshot(ctx, q, updated, task, agentName, status, conclusion, failureReason); err != nil {
		return db.PromptEvaluationRun{}, err
	}
	return updated, nil
}

func syncPromptEvaluationAssetAgentRunSnapshot(ctx context.Context, q *db.Queries, run db.PromptEvaluationRun, task db.AgentTaskQueue, agentName string, status string, conclusion string, failureReason string) error {
	asset, err := q.GetPromptEvaluationAssetInWorkspace(ctx, db.GetPromptEvaluationAssetInWorkspaceParams{
		ID:          run.AssetID,
		WorkspaceID: run.WorkspaceID,
	})
	if err != nil {
		return err
	}
	payload := decodePayloadObject(asset.Payload)
	record := map[string]any{
		"运行时间":            time.Now().UTC().Format(time.RFC3339),
		"run_id":          util.UUIDToString(run.ID),
		"状态":              status,
		"执行Agent":         agentName,
		"agent_id":        util.UUIDToString(task.AgentID),
		"模型":              run.Model,
		"runtime":         run.RuntimeProvider,
		"runtime_id":      util.UUIDToString(task.RuntimeID),
		"trace/task id":   util.UUIDToString(task.ID),
		"chat_session_id": util.UUIDToString(task.ChatSessionID),
		"总用例数":            run.TotalCases,
		"通过数":             run.PassedCases,
		"失败数":             run.FailedCases,
		"通过率":             run.PassRate,
		"总耗时毫秒":           run.TotalDurationMs,
		"平均耗时毫秒":          run.AverageDurationMs,
		"输入token":         run.InputTokens,
		"输出token":         run.OutputTokens,
		"失败原因":            failureReason,
		"评估结论":            conclusion,
	}
	payload["最近Agent运行"] = record
	payload["Agent运行记录"] = appendPromptEvaluationAgentRunHistory(payload["Agent运行记录"], record)
	_, err = q.UpdatePromptEvaluationAsset(ctx, db.UpdatePromptEvaluationAssetParams{
		ID:          asset.ID,
		WorkspaceID: asset.WorkspaceID,
		PromptID:    asset.PromptID,
		Payload:     mustJSONBytes(payload),
	})
	return err
}

func promptEvaluationRunStatusFromTask(run db.PromptEvaluationRun, task db.AgentTaskQueue) (string, int32, int32, float64, string, string) {
	switch task.Status {
	case "completed":
		total := run.TotalCases
		return "通过", total, 0, promptEvaluationPassRate(total, 0), "Agent 执行完成，等待验收者复核输出质量", "无"
	case "failed":
		total := run.TotalCases
		reason := "Agent 执行失败"
		if task.Error.Valid && strings.TrimSpace(task.Error.String) != "" {
			reason = task.Error.String
		}
		return "失败", 0, total, 0, "Agent 执行失败，需要查看 task 日志和失败原因", reason
	case "cancelled":
		return "已取消", run.PassedCases, run.FailedCases, run.PassRate, "Agent 执行已取消", "任务被取消"
	case "running":
		return "运行中", run.PassedCases, run.FailedCases, run.PassRate, "Agent 正在执行", "无"
	default:
		return "已入队", run.PassedCases, run.FailedCases, run.PassRate, "等待 Agent 执行完成", "无"
	}
}

func promptEvaluationPassRate(passed int32, failed int32) float64 {
	total := passed + failed
	if total == 0 {
		return 0
	}
	return float64(passed) / float64(total)
}

func promptEvaluationUsageTotals(run db.PromptEvaluationRun, usages []db.TaskUsage) (int32, int32) {
	input := int64(0)
	output := int64(0)
	for _, usage := range usages {
		input += usage.InputTokens + usage.CacheReadTokens + usage.CacheWriteTokens
		output += usage.OutputTokens
	}
	if input == 0 {
		input = int64(run.InputTokens)
	}
	if output == 0 {
		output = int64(run.OutputTokens)
	}
	return clampInt32(input), clampInt32(output)
}

func promptEvaluationEstimatedCost(run db.PromptEvaluationRun, usages []db.TaskUsage) (float64, []string) {
	if len(usages) == 0 {
		return run.EstimatedCost, nil
	}
	total := float64(0)
	unpriced := map[string]bool{}
	for _, usage := range usages {
		cost, ok := metrics.EstimateUsageCostUSD(
			usage.Model,
			usage.InputTokens,
			usage.OutputTokens,
			usage.CacheReadTokens,
			usage.CacheWriteTokens,
		)
		if !ok {
			key := strings.TrimSpace(usage.Provider + "/" + usage.Model)
			if key == "/" {
				key = "未记录"
			}
			unpriced[key] = true
			continue
		}
		total += cost
	}
	keys := make([]string, 0, len(unpriced))
	for key := range unpriced {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return metrics.RoundCostUSD(total), keys
}

func promptEvaluationTaskDurationMs(task db.AgentTaskQueue, run db.PromptEvaluationRun) int64 {
	if task.StartedAt.Valid && task.CompletedAt.Valid {
		duration := task.CompletedAt.Time.Sub(task.StartedAt.Time).Milliseconds()
		if duration > 0 {
			return duration
		}
	}
	if task.StartedAt.Valid && task.Status == "running" {
		duration := time.Since(task.StartedAt.Time).Milliseconds()
		if duration > 0 {
			return duration
		}
	}
	return run.TotalDurationMs
}

func promptEvaluationTaskEvidence(run db.PromptEvaluationRun, task db.AgentTaskQueue, usages []db.TaskUsage, messages []db.TaskMessage) map[string]any {
	usageRows := make([]map[string]any, 0, len(usages))
	for _, usage := range usages {
		estimatedCost, priced := metrics.EstimateUsageCostUSD(
			usage.Model,
			usage.InputTokens,
			usage.OutputTokens,
			usage.CacheReadTokens,
			usage.CacheWriteTokens,
		)
		usageRows = append(usageRows, map[string]any{
			"provider":           usage.Provider,
			"model":              usage.Model,
			"input_tokens":       usage.InputTokens,
			"output_tokens":      usage.OutputTokens,
			"cache_read_tokens":  usage.CacheReadTokens,
			"cache_write_tokens": usage.CacheWriteTokens,
			"estimated_cost":     estimatedCost,
			"priced":             priced,
		})
	}
	messageSummary := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		messageSummary = append(messageSummary, map[string]any{
			"seq":     message.Seq,
			"type":    message.Type,
			"tool":    message.Tool,
			"content": truncatePromptEvaluationEvidence(promptEvaluationTextValue(message.Content), 800),
		})
	}
	return map[string]any{
		"run_id":        util.UUIDToString(run.ID),
		"task_id":       util.UUIDToString(task.ID),
		"task_status":   task.Status,
		"task_result":   decodeJSONDefault(task.Result, map[string]any{}),
		"task_error":    promptEvaluationTextValue(task.Error),
		"session_id":    promptEvaluationTextValue(task.SessionID),
		"work_dir":      promptEvaluationTextValue(task.WorkDir),
		"started_at":    timestampToString(task.StartedAt),
		"completed_at":  timestampToString(task.CompletedAt),
		"usage":         usageRows,
		"task_messages": messageSummary,
		"同步时间":          time.Now().UTC().Format(time.RFC3339),
		"同步说明":          "从 agent_task_queue、task_usage 和 task_message 回写结构化训练评估运行记录",
	}
}

func promptEvaluationTrialStatusFromRunStatus(status string) string {
	switch status {
	case "通过":
		return "通过"
	case "失败":
		return "失败"
	case "未通过":
		return "未通过"
	case "需人工复核":
		return "需人工复核"
	case "已取消":
		return "已跳过"
	default:
		return "待执行"
	}
}

type promptEvaluationAgentCaseVerdict struct {
	CaseIndex     int32
	Status        string
	FailureReason string
	Conclusion    string
	Output        any
	Evidence      map[string]any
}

func promptEvaluationAgentVerdictsFromTask(run db.PromptEvaluationRun, task db.AgentTaskQueue, messages []db.TaskMessage) ([]promptEvaluationAgentCaseVerdict, bool) {
	sources := make([]string, 0, len(messages)+2)
	if len(task.Result) > 0 {
		sources = append(sources, string(task.Result))
		if output := promptEvaluationTaskResultOutput(task.Result); output != "" {
			sources = append(sources, output)
		}
	}
	for _, message := range messages {
		if content := promptEvaluationTextValue(message.Content); content != "" {
			sources = append(sources, content)
		}
		if output := promptEvaluationTextValue(message.Output); output != "" {
			sources = append(sources, output)
		}
	}
	for _, source := range sources {
		for _, candidate := range promptEvaluationJSONCandidates(source) {
			verdicts, ok := parsePromptEvaluationAgentVerdicts(candidate, run.TotalCases)
			if ok {
				return verdicts, true
			}
		}
	}
	return nil, false
}

func promptEvaluationRunStatusFromAgentVerdicts(run db.PromptEvaluationRun, verdicts []promptEvaluationAgentCaseVerdict) (string, int32, int32, float64, string, string) {
	total := run.TotalCases
	if total <= 0 {
		total = int32(len(verdicts))
	}
	passed := int32(0)
	review := int32(0)
	reasons := make([]string, 0)
	for _, verdict := range verdicts {
		switch verdict.Status {
		case "通过":
			passed++
		case "需人工复核":
			review++
			if verdict.FailureReason != "" && verdict.FailureReason != "无" {
				reasons = append(reasons, verdict.FailureReason)
			}
		default:
			if verdict.FailureReason != "" && verdict.FailureReason != "无" {
				reasons = append(reasons, verdict.FailureReason)
			}
		}
	}
	failed := total - passed
	if failed < 0 {
		failed = 0
	}
	status := "通过"
	conclusion := "Agent 返回结构化逐用例评估，全部用例通过"
	if failed > 0 {
		if review > 0 && review == failed {
			status = "需人工复核"
			conclusion = "Agent 返回结构化逐用例评估，但部分用例需要验收者人工复核"
		} else {
			status = "未通过"
			conclusion = "Agent 返回结构化逐用例评估，存在未通过用例"
		}
	}
	failureReason := "无"
	if len(reasons) > 0 {
		failureReason = strings.Join(uniquePromptEvaluationStrings(reasons), "；")
	} else if status == "需人工复核" {
		failureReason = "部分用例需要人工复核"
	} else if status == "未通过" {
		failureReason = "存在未通过用例"
	}
	return status, passed, failed, promptEvaluationPassRate(passed, failed), conclusion, failureReason
}

func parsePromptEvaluationAgentVerdicts(raw any, totalCases int32) ([]promptEvaluationAgentCaseVerdict, bool) {
	value, ok := raw.(map[string]any)
	if !ok {
		if list, ok := raw.([]any); ok {
			return parsePromptEvaluationAgentVerdictList(list, totalCases)
		}
		return nil, false
	}
	for _, key := range []string{"用例结果", "逐用例结果", "cases", "case_results", "trials", "results", "用例"} {
		if list, ok := value[key].([]any); ok {
			return parsePromptEvaluationAgentVerdictList(list, totalCases)
		}
	}
	if totalCases == 1 && promptEvaluationMapHasRecognizedVerdict(value) {
		verdict := promptEvaluationAgentVerdictFromMap(value, 0)
		return []promptEvaluationAgentCaseVerdict{verdict}, true
	}
	return nil, false
}

func parsePromptEvaluationAgentVerdictList(list []any, totalCases int32) ([]promptEvaluationAgentCaseVerdict, bool) {
	if len(list) == 0 {
		return nil, false
	}
	byIndex := map[int32]promptEvaluationAgentCaseVerdict{}
	for index, item := range list {
		row, ok := item.(map[string]any)
		if !ok {
			continue
		}
		verdict := promptEvaluationAgentVerdictFromMap(row, int32(index))
		byIndex[verdict.CaseIndex] = verdict
	}
	if len(byIndex) == 0 {
		return nil, false
	}
	total := totalCases
	if total <= 0 {
		total = int32(len(byIndex))
	}
	verdicts := make([]promptEvaluationAgentCaseVerdict, 0, total)
	for index := int32(0); index < total; index++ {
		if verdict, ok := byIndex[index]; ok {
			verdicts = append(verdicts, verdict)
			continue
		}
		verdicts = append(verdicts, promptEvaluationAgentCaseVerdict{
			CaseIndex:     index,
			Status:        "需人工复核",
			FailureReason: "Agent 未返回该用例的结构化评估结果",
			Conclusion:    "缺少逐用例结果",
			Output:        map[string]any{},
			Evidence:      map[string]any{"缺失": true},
		})
	}
	return verdicts, true
}

func promptEvaluationAgentVerdictFromMap(row map[string]any, fallbackIndex int32) promptEvaluationAgentCaseVerdict {
	caseIndex := promptEvaluationCaseIndexFromMap(row, fallbackIndex)
	status := normalizePromptEvaluationAgentStatus(firstNonEmptyString(row, "状态", "status", "结论", "result"))
	if status == "" {
		status = statusFromPromptEvaluationPassedValue(firstValue(row, "passed", "通过", "pass"))
	}
	if status == "" {
		status = "需人工复核"
	}
	failureReason := firstNonEmptyString(row, "失败原因", "failure_reason", "reason", "error", "问题")
	if failureReason == "" {
		if status == "通过" {
			failureReason = "无"
		} else if status == "需人工复核" {
			failureReason = "Agent 未给出明确通过/未通过结论"
		} else {
			failureReason = "Agent 判定未通过"
		}
	}
	conclusion := firstNonEmptyString(row, "评估结论", "conclusion", "summary", "说明")
	if conclusion == "" {
		conclusion = status
	}
	output := firstValue(row, "输出", "output", "actual", "实际输出")
	if output == nil {
		output = map[string]any{}
	}
	return promptEvaluationAgentCaseVerdict{
		CaseIndex:     caseIndex,
		Status:        status,
		FailureReason: failureReason,
		Conclusion:    conclusion,
		Output:        output,
		Evidence:      row,
	}
}

func promptEvaluationMapHasRecognizedVerdict(row map[string]any) bool {
	if _, ok := row["状态"]; ok {
		return normalizePromptEvaluationAgentStatus(stringFromAny(row["状态"])) != ""
	}
	if _, ok := row["status"]; ok {
		return normalizePromptEvaluationAgentStatus(stringFromAny(row["status"])) != ""
	}
	if _, ok := row["结论"]; ok {
		return normalizePromptEvaluationAgentStatus(stringFromAny(row["结论"])) != ""
	}
	if _, ok := row["result"]; ok {
		return normalizePromptEvaluationAgentStatus(stringFromAny(row["result"])) != ""
	}
	if _, ok := row["passed"]; ok {
		return statusFromPromptEvaluationPassedValue(row["passed"]) != ""
	}
	if _, ok := row["通过"]; ok {
		return statusFromPromptEvaluationPassedValue(row["通过"]) != ""
	}
	if _, ok := row["pass"]; ok {
		return statusFromPromptEvaluationPassedValue(row["pass"]) != ""
	}
	return false
}

func promptEvaluationCaseIndexFromMap(row map[string]any, fallback int32) int32 {
	if value, ok := intFromAny(firstValue(row, "case_index", "caseIndex")); ok && value >= 0 {
		return int32(value)
	}
	if value, ok := intFromAny(firstValue(row, "用例序号", "序号", "index", "case_number")); ok && value > 0 {
		return int32(value - 1)
	}
	return fallback
}

func normalizePromptEvaluationAgentStatus(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "通过", "pass", "passed", "ok", "success", "succeeded", "true":
		return "通过"
	case "失败", "未通过", "不通过", "fail", "failed", "failure", "false":
		return "未通过"
	case "需人工复核", "人工复核", "待人工复核", "needs_review", "review", "manual_review":
		return "需人工复核"
	default:
		return ""
	}
}

func statusFromPromptEvaluationPassedValue(value any) string {
	switch v := value.(type) {
	case bool:
		if v {
			return "通过"
		}
		return "未通过"
	case string:
		return normalizePromptEvaluationAgentStatus(v)
	default:
		return ""
	}
}

func promptEvaluationTaskResultOutput(raw []byte) string {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	return firstNonEmptyString(payload, "output", "输出", "result", "结果")
}

func promptEvaluationJSONCandidates(source string) []any {
	source = strings.TrimSpace(source)
	if source == "" {
		return nil
	}
	candidates := []string{source}
	parts := strings.Split(source, "```")
	for index := 1; index < len(parts); index += 2 {
		block := strings.TrimSpace(parts[index])
		if newline := strings.IndexByte(block, '\n'); newline >= 0 {
			header := strings.TrimSpace(strings.ToLower(block[:newline]))
			if header == "json" || header == "javascript" || header == "js" {
				block = strings.TrimSpace(block[newline+1:])
			}
		}
		candidates = append(candidates, block)
	}
	if start, end := strings.Index(source, "{"), strings.LastIndex(source, "}"); start >= 0 && end > start {
		candidates = append(candidates, source[start:end+1])
	}
	if start, end := strings.Index(source, "["), strings.LastIndex(source, "]"); start >= 0 && end > start {
		candidates = append(candidates, source[start:end+1])
	}
	parsed := make([]any, 0, len(candidates))
	for _, candidate := range candidates {
		var value any
		if err := json.Unmarshal([]byte(strings.TrimSpace(candidate)), &value); err == nil {
			parsed = append(parsed, value)
		}
	}
	return parsed
}

func firstValue(row map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := row[key]; ok && value != nil {
			return value
		}
	}
	return nil
}

func firstNonEmptyString(row map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(stringFromAny(row[key])); value != "" {
			return value
		}
	}
	return ""
}

func uniquePromptEvaluationStrings(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func promptEvaluationPerCaseUsage(totalCases int32, inputTokens int32, outputTokens int32) (int32, int32) {
	if totalCases <= 0 {
		return 0, 0
	}
	return inputTokens / totalCases, outputTokens / totalCases
}

func appendPromptEvaluationAgentRunHistory(raw any, result map[string]any) []any {
	history, _ := raw.([]any)
	runID := stringFromAny(result["run_id"])
	next := []any{result}
	for _, item := range history {
		if runID != "" {
			if existing, ok := item.(map[string]any); ok && stringFromAny(existing["run_id"]) == runID {
				continue
			}
		}
		next = append(next, item)
	}
	if len(next) > 20 {
		next = next[:20]
	}
	return next
}

func decodePayloadObject(raw []byte) map[string]any {
	var payload map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &payload)
	}
	if payload == nil {
		return map[string]any{}
	}
	return payload
}

func decodeJSONDefault(raw []byte, fallback any) any {
	if len(raw) == 0 {
		return fallback
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return fallback
	}
	return value
}

func mustJSONBytes(value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		return []byte("{}")
	}
	return raw
}

func truncatePromptEvaluationEvidence(value string, maxRunes int) string {
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes]) + "..."
}

func promptEvaluationTextValue(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func timestampToString(ts pgtype.Timestamptz) string {
	if !ts.Valid {
		return ""
	}
	return ts.Time.UTC().Format(time.RFC3339)
}

func clampInt32(value int64) int32 {
	if value > int64(^uint32(0)>>1) {
		return int32(^uint32(0) >> 1)
	}
	if value < 0 {
		return 0
	}
	return int32(value)
}

func stringFromAny(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case int:
		return strconv.Itoa(v)
	case int32:
		return strconv.FormatInt(int64(v), 10)
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		if v {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}

func intFromAny(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int32:
		return int(v), true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(v))
		return parsed, err == nil
	default:
		return 0, false
	}
}
