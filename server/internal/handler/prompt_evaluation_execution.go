package handler

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/prompteval"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (h *Handler) persistPromptEvaluationLocalRun(w http.ResponseWriter, r *http.Request, queries *db.Queries, runID pgtype.UUID, asset db.PromptEvaluationAsset, result promptEvaluationRunResult, createdBy pgtype.UUID) (db.PromptEvaluationRun, bool) {
	now := time.Now()
	status := "通过"
	if result.FailedCases > 0 {
		status = "未通过"
	}
	metrics := map[string]any{
		"总用例数":    result.TotalCases,
		"通过数":     result.PassedCases,
		"失败数":     result.FailedCases,
		"通过率":     result.PassRate,
		"总耗时":     result.TotalDurationMs,
		"平均耗时":    result.AverageDurationMs,
		"输入token": result.InputTokens,
		"输出token": result.OutputTokens,
		"预估成本":    result.EstimatedCost,
		"执行Agent": result.AgentName,
		"模型":      result.Model,
		"runtime": result.Runtime,
		"提示词版本":   result.PromptVersion,
		"实验维度评分":  result.DimensionScores,
		"失败原因":    result.FailureReason,
		"评估结论":    result.Conclusion,
	}
	datasetVersionBindings, ok := h.promptEvaluationDatasetVersionBindings(w, r, asset.WorkspaceID, decodePayloadObject(asset.Payload))
	if !ok {
		return db.PromptEvaluationRun{}, false
	}
	if len(datasetVersionBindings) > 0 {
		metrics["数据集版本数"] = len(datasetVersionBindings)
	}
	run, err := queries.CreatePromptEvaluationRunWithID(r.Context(), db.CreatePromptEvaluationRunWithIDParams{
		ID:                runID,
		WorkspaceID:       asset.WorkspaceID,
		AssetID:           asset.ID,
		PromptID:          asset.PromptID,
		RunKind:           "本地渲染",
		Status:            status,
		TriggerSource:     "手动",
		Model:             result.Model,
		RuntimeProvider:   result.Runtime,
		TotalCases:        int32(result.TotalCases),
		PassedCases:       int32(result.PassedCases),
		FailedCases:       int32(result.FailedCases),
		PassRate:          result.PassRate,
		TotalDurationMs:   result.TotalDurationMs,
		AverageDurationMs: result.AverageDurationMs,
		InputTokens:       int32(result.InputTokens),
		OutputTokens:      int32(result.OutputTokens),
		EstimatedCost:     result.EstimatedCost,
		FailureReason:     result.FailureReason,
		Conclusion:        result.Conclusion,
		Metrics:           mustJSONBytes(metrics),
		Evidence:          mustJSONBytes(map[string]any{"资产类型": asset.AssetType, "运行方式": "本地提示词渲染", "提示词版本": result.PromptVersion, "数据集版本": datasetVersionBindings, "实验维度评分": result.DimensionScores}),
		StartedAt:         pgtype.Timestamptz{Time: now, Valid: true},
		CompletedAt:       pgtype.Timestamptz{Time: now, Valid: true},
		CreatedBy:         createdBy,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create prompt evaluation run")
		return db.PromptEvaluationRun{}, false
	}
	if err := h.persistPromptEvaluationDimensionScores(r.Context(), queries, run, result.DimensionScores, "local_run"); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to persist prompt evaluation dimension scores")
		return db.PromptEvaluationRun{}, false
	}
	for idx, caseResult := range result.CaseResults {
		failureReason := ""
		if caseResult.Status != "通过" {
			failureReason = result.FailureReason
		}
		if _, err := queries.CreatePromptEvaluationTrial(r.Context(), db.CreatePromptEvaluationTrialParams{
			RunID:          run.ID,
			WorkspaceID:    asset.WorkspaceID,
			AssetID:        asset.ID,
			CaseIndex:      int32(idx),
			CaseName:       caseResult.Name,
			Status:         caseResult.Status,
			Input:          mustJSONBytes(map[string]any{"变量": caseResult.Variables}),
			Expected:       mustJSONBytes(map[string]any{"期望包含": caseResult.ExpectedContains}),
			Output:         mustJSONBytes(map[string]any{"已匹配": caseResult.MatchedContains, "使用变量": caseResult.UsedVariables, "缺失变量": caseResult.MissingVariables}),
			RenderedPrompt: caseResult.RenderedPrompt,
			InputTokens:    int32(estimatePromptEvaluationTokens(caseResult.RenderedPrompt)),
			OutputTokens:   int32(estimatePromptEvaluationTokens(caseResult.RenderedPrompt)),
			DurationMs:     result.AverageDurationMs,
			FailureReason:  failureReason,
			Evidence:       mustJSONBytes(map[string]any{"run_id": uuidToString(run.ID), "trace/task id": uuidToString(run.ID)}),
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create prompt evaluation trial")
			return db.PromptEvaluationRun{}, false
		}
	}
	return run, true
}

func (h *Handler) persistPromptEvaluationQueuedAgentRun(w http.ResponseWriter, r *http.Request, queries *db.Queries, runID pgtype.UUID, asset db.PromptEvaluationAsset, prompt db.PromptLibraryItem, agent db.Agent, runtime db.AgentRuntime, taskID pgtype.UUID, chatSessionID pgtype.UUID, createdBy pgtype.UUID, triggerSource string, payload map[string]any, cases []map[string]any) (db.PromptEvaluationRun, bool) {
	datasetVersionBindings, ok := h.promptEvaluationDatasetVersionBindings(w, r, asset.WorkspaceID, payload)
	if !ok {
		return db.PromptEvaluationRun{}, false
	}
	dimensionScores := pendingPromptEvaluationExperimentDimensionScores(promptEvaluationExperimentDimensions(payload), len(cases))
	run, err := queries.CreatePromptEvaluationRunWithID(r.Context(), db.CreatePromptEvaluationRunWithIDParams{
		ID:                runID,
		WorkspaceID:       asset.WorkspaceID,
		AssetID:           asset.ID,
		PromptID:          asset.PromptID,
		RunKind:           "Agent执行",
		Status:            "已入队",
		TriggerSource:     triggerSource,
		AgentID:           agent.ID,
		RuntimeID:         runtime.ID,
		TaskID:            taskID,
		ChatSessionID:     chatSessionID,
		Model:             promptEvaluationModelForAgent(agent),
		RuntimeProvider:   runtime.Provider,
		TotalCases:        int32(len(cases)),
		PassedCases:       0,
		FailedCases:       0,
		PassRate:          0,
		TotalDurationMs:   0,
		AverageDurationMs: 0,
		InputTokens:       int32(estimatePromptEvaluationTokens(string(mustJSONBytes(payload)))),
		OutputTokens:      0,
		EstimatedCost:     0,
		FailureReason:     "无",
		Conclusion:        "等待智能体执行完成",
		Metrics: mustJSONBytes(map[string]any{
			"总用例数":          len(cases),
			"通过数":           0,
			"失败数":           0,
			"通过率":           0,
			"执行Agent":       agent.Name,
			"模型":            promptEvaluationModelForAgent(agent),
			"runtime":       runtime.Provider,
			"提示词版本":         prompt.Version,
			"实验维度评分":        dimensionScores,
			"trace/task id": uuidToString(taskID),
			"评估结论":          "等待智能体执行完成",
			"数据集版本数":        len(datasetVersionBindings),
		}),
		Evidence: mustJSONBytes(map[string]any{
			"task_id":         uuidToString(taskID),
			"chat_session_id": uuidToString(chatSessionID),
			"agent_id":        uuidToString(agent.ID),
			"runtime_id":      uuidToString(runtime.ID),
			"提示词版本":           prompt.Version,
			"数据集版本":           datasetVersionBindings,
			"实验维度评分":          dimensionScores,
		}),
		StartedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
		CreatedBy: createdBy,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create queued prompt evaluation run")
		return db.PromptEvaluationRun{}, false
	}
	if err := h.persistPromptEvaluationDimensionScores(r.Context(), queries, run, dimensionScores, "run_metrics"); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to persist queued prompt evaluation dimension scores")
		return db.PromptEvaluationRun{}, false
	}
	for idx, c := range cases {
		name := util.StringFromAny(c["case_name"])
		if name == "" {
			name = "用例 " + strconv.Itoa(idx+1)
		}
		if _, err := queries.CreatePromptEvaluationTrial(r.Context(), db.CreatePromptEvaluationTrialParams{
			RunID:         run.ID,
			WorkspaceID:   asset.WorkspaceID,
			AssetID:       asset.ID,
			CaseIndex:     int32(idx),
			CaseName:      name,
			Status:        "待执行",
			Input:         mustJSONBytes(map[string]any{"变量": c["variables"]}),
			Expected:      mustJSONBytes(map[string]any{"期望包含": c["expected_contains"]}),
			Output:        mustJSONBytes(map[string]any{}),
			FailureReason: "等待智能体执行完成",
			Evidence:      mustJSONBytes(map[string]any{"run_id": uuidToString(run.ID), "task_id": uuidToString(taskID)}),
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create queued prompt evaluation trial")
			return db.PromptEvaluationRun{}, false
		}
	}
	return run, true
}

func (h *Handler) persistPromptEvaluationDimensionScores(ctx context.Context, queries *db.Queries, run db.PromptEvaluationRun, scores []promptEvaluationExperimentDimensionScore, source string) error {
	if len(scores) == 0 {
		return nil
	}
	if err := queries.DeletePromptEvaluationDimensionScoresByRun(ctx, db.DeletePromptEvaluationDimensionScoresByRunParams{
		WorkspaceID: run.WorkspaceID,
		RunID:       run.ID,
	}); err != nil {
		return err
	}
	for _, score := range scores {
		dimensionName := strings.TrimSpace(score.DimensionName)
		if dimensionName == "" {
			dimensionName = "维度 " + strconv.Itoa(int(score.DimensionIndex)+1)
		}
		if _, err := queries.UpsertPromptEvaluationDimensionScore(ctx, db.UpsertPromptEvaluationDimensionScoreParams{
			WorkspaceID:    run.WorkspaceID,
			RunID:          run.ID,
			AssetID:        run.AssetID,
			PromptID:       run.PromptID,
			DimensionIndex: score.DimensionIndex,
			DimensionName:  dimensionName,
			Score:          score.Score,
			PassedCases:    int32(score.PassedCases),
			TotalCases:     int32(score.TotalCases),
			Status:         promptEvaluationDimensionScoreStatus(score.Status),
			Rule:           score.Rule,
			Evidence:       score.Evidence,
			Source:         source,
		}); err != nil {
			return err
		}
	}
	return nil
}

func promptEvaluationDimensionScoreStatus(status string) string {
	switch status {
	case "待执行", "已评分", "无用例":
		return status
	default:
		return "待执行"
	}
}

func (h *Handler) promptEvaluationCasesForAsset(w http.ResponseWriter, r *http.Request, asset db.PromptEvaluationAsset) ([]map[string]any, bool) {
	rows, err := h.Queries.ListPromptEvaluationCases(r.Context(), db.ListPromptEvaluationCasesParams{
		WorkspaceID: asset.WorkspaceID,
		AssetID:     asset.ID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list prompt evaluation cases")
		return nil, false
	}
	executableRows := make([]db.PromptEvaluationCase, 0, len(rows))
	for _, row := range rows {
		if row.Status == "启用" || row.Status == "active" {
			executableRows = append(executableRows, row)
		}
	}
	if len(executableRows) == 0 {
		return promptEvaluationCases(decodePayloadObject(asset.Payload)), true
	}
	cases := make([]map[string]any, 0, len(executableRows))
	for _, row := range executableRows {
		cases = append(cases, map[string]any{
			"case_name":         row.CaseName,
			"variables":         mustDecodePersistedJSONObject(row.Variables, "prompt evaluation case variables"),
			"expected_contains": promptEvaluationExecutableExpectedContains(row.ExpectedContains),
			"tags":              mustDecodePersistedJSONArray(row.Tags, "prompt evaluation case tags"),
		})
	}
	return cases, true
}

func validPromptEvaluationOptimizationCandidateStatus(status string) bool {
	return status == "待确认" || status == "已发布" || status == "已拒绝"
}

func promptEvaluationRunHasFailure(run db.PromptEvaluationRun) bool {
	if run.FailedCases > 0 {
		return true
	}
	if run.Status == "未通过" || run.Status == "失败" {
		return true
	}
	reason := strings.TrimSpace(run.FailureReason)
	return reason != "" && reason != "无"
}

func promptEvaluationRunFailedCaseCount(run db.PromptEvaluationRun, trials []db.PromptEvaluationTrial) int32 {
	if run.FailedCases > 0 {
		return run.FailedCases
	}
	failed := int32(0)
	for _, trial := range trials {
		if trial.Status == "未通过" || trial.Status == "失败" {
			failed++
		}
	}
	if failed > 0 {
		return failed
	}
	if run.TotalCases > 0 && (run.Status == "未通过" || run.Status == "失败") {
		return run.TotalCases
	}
	return 1
}

func promptEvaluationWeakDimensionSummaries(rows []db.ListPromptEvaluationDimensionScoreSummariesRow) []map[string]any {
	result := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		weak := row.LatestStatus != "已评分" || row.TotalCases == 0 || row.Score < 1
		if !weak {
			continue
		}
		priority := "中"
		if row.LatestStatus != "已评分" || row.TotalCases == 0 || row.Score < 0.5 {
			priority = "高"
		}
		result = append(result, map[string]any{
			"维度名称":  row.DimensionName,
			"维度序号":  row.DimensionIndex,
			"得分":    row.Score,
			"通过用例数": row.PassedCases,
			"总用例数":  row.TotalCases,
			"运行次数":  row.RunCount,
			"已评分次数": row.ScoredRunCount,
			"最新状态":  row.LatestStatus,
			"最新证据":  row.LatestEvidence,
			"评分规则":  row.LatestRule,
			"优先级":   priority,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		priorityRank := map[string]int{"高": 0, "中": 1, "低": 2}
		leftPriority := priorityRank[util.StringFromAny(result[i]["优先级"])]
		rightPriority := priorityRank[util.StringFromAny(result[j]["优先级"])]
		if leftPriority != rightPriority {
			return leftPriority < rightPriority
		}
		leftScore := floatFromAny(result[i]["得分"])
		rightScore := floatFromAny(result[j]["得分"])
		if leftScore != rightScore {
			return leftScore < rightScore
		}
		return util.StringFromAny(result[i]["维度名称"]) < util.StringFromAny(result[j]["维度名称"])
	})
	return result
}

func promptEvaluationCandidatePriority(weakDimensions []map[string]any) string {
	for _, item := range weakDimensions {
		if util.StringFromAny(item["优先级"]) == "高" {
			return "高"
		}
	}
	if len(weakDimensions) > 0 {
		return "中"
	}
	return "低"
}

func promptEvaluationDefaultWeakDimensionSummaries(run db.PromptEvaluationRun) []map[string]any {
	if !promptEvaluationRunHasFailure(run) {
		return nil
	}
	totalCases := int(run.TotalCases)
	if totalCases <= 0 {
		totalCases = 1
	}
	defaults := promptEvaluationDefaultExperimentDimensions()
	result := make([]map[string]any, 0, len(defaults))
	for idx, item := range defaults {
		result = append(result, map[string]any{
			"维度名称":  item.Name,
			"维度序号":  int32(idx),
			"得分":    0.0,
			"通过用例数": 0,
			"总用例数":  totalCases,
			"运行次数":  1,
			"已评分次数": 0,
			"最新状态":  "待执行",
			"最新证据":  "运行缺少实验维度评分事实，按默认实验维度标记为失败复盘重点",
			"评分规则":  promptEvaluationDimensionRule(item.Name),
			"优先级":   "高",
		})
	}
	return result
}

func buildPromptEvaluationCandidateFailureSummary(run db.PromptEvaluationRun, trials []db.PromptEvaluationTrial) map[string]any {
	trialSummaries := make([]map[string]any, 0, len(trials))
	for _, trial := range trials {
		if trial.Status == "通过" {
			continue
		}
		trialSummaries = append(trialSummaries, map[string]any{
			"用例序号":  trial.CaseIndex,
			"用例名称":  trial.CaseName,
			"状态":    trial.Status,
			"失败原因":  trial.FailureReason,
			"输入":    mustDecodePersistedJSONObject(trial.Input, "prompt evaluation trial input"),
			"期望":    mustDecodePersistedJSONObject(trial.Expected, "prompt evaluation trial expected"),
			"输出":    mustDecodePersistedJSONValue(trial.Output, "prompt evaluation trial output"),
			"渲染提示词": trial.RenderedPrompt,
		})
	}
	if len(trialSummaries) == 0 && len(trials) > 0 {
		for _, trial := range trials {
			trialSummaries = append(trialSummaries, map[string]any{
				"用例序号": trial.CaseIndex,
				"用例名称": trial.CaseName,
				"状态":   trial.Status,
				"输入":   mustDecodePersistedJSONObject(trial.Input, "prompt evaluation trial input"),
				"期望":   mustDecodePersistedJSONObject(trial.Expected, "prompt evaluation trial expected"),
			})
		}
	}
	return map[string]any{
		"run_id":        uuidToString(run.ID),
		"asset_id":      uuidToString(run.AssetID),
		"run_kind":      run.RunKind,
		"状态":            run.Status,
		"总用例数":          run.TotalCases,
		"通过数":           run.PassedCases,
		"失败数":           promptEvaluationRunFailedCaseCount(run, trials),
		"通过率":           run.PassRate,
		"模型":            run.Model,
		"runtime":       run.RuntimeProvider,
		"trace/task id": uuidToPtr(run.TaskID),
		"失败原因":          run.FailureReason,
		"评估结论":          run.Conclusion,
		"失败用例":          trialSummaries,
		"evidence":      mustDecodePersistedJSONObject(run.Evidence, "prompt evaluation run evidence"),
		"生成说明":          "基于结构化运行记录和失败用例生成优化候选；候选不会自动替换生产提示词。",
	}
}

func buildPromptEvaluationCandidateContent(prompt db.PromptLibraryItem, run db.PromptEvaluationRun, sourceSummary map[string]any) (string, string) {
	failureReason := strings.TrimSpace(run.FailureReason)
	if failureReason == "" {
		failureReason = "结构化运行记录显示存在失败用例，需要补充边界、输出格式和验收约束。"
	}
	failedCases, _ := sourceSummary["失败用例"].([]map[string]any)
	rationale := "基于失败用例补充中文输出约束、失败处理要求、证据字段和验收口径；原提示词不被自动替换，必须人工确认后发布。"
	if _, ok := sourceSummary["真实Agent运行证据"].(map[string]any); ok {
		rationale = "基于失败用例和真实智能体 task 证据补充中文输出约束、失败处理要求、证据字段和验收口径；原提示词不被自动替换，必须人工确认后发布。"
	}
	lines := []string{
		strings.TrimSpace(prompt.Content),
		"",
		"【优化候选：失败用例修复建议】",
		"来源运行：" + uuidToString(run.ID),
		"失败原因：" + failureReason,
		"失败用例数：" + strconv.Itoa(int(promptEvaluationRunFailedCaseCount(run, nil))),
		"",
		"请在后续执行中严格遵守：",
		"1. 全部输出使用中文，避免英文状态词和未解释的内部缩写。",
		"2. 对每个输入先复述目标、边界、影响范围和验收条件。",
		"3. 当信息不足时，先提出需要团队确认的问题，不要直接假设结论。",
		"4. 输出必须包含可观测证据：耗时、执行 Agent、模型、trace/task id、失败原因和评估结论。",
		"5. 如果触发失败场景，明确指出失败用例、缺失变量、未命中期望和下一步修复建议。",
	}
	if len(failedCases) > 0 {
		lines = append(lines, "", "失败用例摘要：")
		for _, item := range failedCases {
			name := util.StringFromAny(item["用例名称"])
			reason := util.StringFromAny(item["失败原因"])
			if name == "" {
				name = "未命名用例"
			}
			if reason == "" {
				reason = failureReason
			}
			lines = append(lines, "- "+name+"："+reason)
		}
	}
	if weakDimensions, ok := sourceSummary["失败维度"].([]map[string]any); ok && len(weakDimensions) > 0 {
		lines = append(lines, "", "维度优先级：")
		for _, item := range weakDimensions {
			name := util.StringFromAny(item["维度名称"])
			if name == "" {
				name = "未命名维度"
			}
			lines = append(lines, "- "+name+"："+util.StringFromAny(item["优先级"])+"优先级，得分 "+util.StringFromAny(item["得分"])+"，证据："+prompteval.TruncateEvidence(util.StringFromAny(item["最新证据"]), 160))
		}
		rationale = "基于失败用例、维度评分弱项和真实运行证据补充中文输出约束、失败处理要求、证据字段和验收口径；原提示词不被自动替换，必须人工确认后发布。"
	}
	if runtimeEvidence, ok := sourceSummary["真实Agent运行证据"].(map[string]any); ok {
		lines = append(lines, "", "真实智能体输出摘要：")
		if messages, ok := runtimeEvidence["task消息"].([]map[string]any); ok && len(messages) > 0 {
			for _, message := range messages {
				content := strings.TrimSpace(util.StringFromAny(message["content"]))
				if content == "" {
					content = strings.TrimSpace(util.StringFromAny(message["output"]))
				}
				if content == "" {
					continue
				}
				lines = append(lines, "- task消息 #"+util.StringFromAny(message["seq"])+"："+prompteval.TruncateEvidence(content, 240))
			}
		}
		if traces, ok := runtimeEvidence["trace事件"].([]map[string]any); ok && len(traces) > 0 {
			for _, trace := range traces {
				name := util.StringFromAny(firstValue(trace, "event_name", "event_type"))
				status := util.StringFromAny(trace["status"])
				reason := util.StringFromAny(trace["failure_reason"])
				if name == "" {
					name = "未命名 trace"
				}
				if reason == "" {
					reason = "无"
				}
				lines = append(lines, "- trace "+name+"："+status+"，失败原因："+reason)
			}
		}
		if usages, ok := runtimeEvidence["task用量"].([]map[string]any); ok && len(usages) > 0 {
			for _, usage := range usages {
				lines = append(lines, "- 用量 "+util.StringFromAny(usage["provider"])+"/"+util.StringFromAny(usage["model"])+"：输入 "+util.StringFromAny(usage["input_tokens"])+"，输出 "+util.StringFromAny(usage["output_tokens"])+"，预估成本 "+util.StringFromAny(usage["estimated_cost"]))
			}
		}
	}
	lines = append(lines, "", "人工发布要求：发布前必须由验收者确认该候选不会降低原有通过用例质量。")
	return strings.Join(lines, "\n"), rationale
}

func buildPromptEvaluationSourcePromptSnapshot(prompt db.PromptLibraryItem) map[string]any {
	return map[string]any{
		"prompt_id": uuidToString(prompt.ID),
		"名称":        prompt.Name,
		"类型":        prompt.PromptType,
		"版本":        prompt.Version,
		"状态":        prompt.Status,
		"变量":        mustDecodePersistedJSONArray(prompt.Variables, "prompt library item variables"),
		"标签":        mustDecodePersistedJSONArray(prompt.Tags, "prompt library item tags"),
		"内容摘要":      prompteval.TruncateEvidence(prompt.Content, 1200),
	}
}

func buildPromptEvaluationPublishedPromptDescription(candidate db.PromptEvaluationOptimizationCandidate, prompt db.PromptLibraryItem) string {
	parts := []string{
		"由训练与评估优化候选人工确认发布。",
		"来源提示词：" + prompt.Name + " v" + strconv.Itoa(int(prompt.Version)) + "。",
		"来源运行：" + uuidToString(candidate.RunID) + "。",
	}
	if candidate.Rationale != "" {
		parts = append(parts, "优化依据："+candidate.Rationale)
	}
	return strings.Join(parts, " ")
}

func buildPromptEvaluationPublishedPromptTags(raw []byte) []byte {
	tags := stringListFromAny(mustDecodePersistedJSONArray(raw, "prompt library item tags"))
	seen := map[string]bool{}
	next := make([]string, 0, len(tags)+3)
	for _, tag := range append(tags, "优化发布", "人工确认", "训练与评估") {
		if tag == "" || seen[tag] {
			continue
		}
		seen[tag] = true
		next = append(next, tag)
	}
	return mustJSONBytes(next)
}
