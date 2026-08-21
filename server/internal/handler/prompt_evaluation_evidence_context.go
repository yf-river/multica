package handler

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util/prompteval"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func buildPromptEvaluationEvidenceContext(
	run db.PromptEvaluationRun,
	task *db.AgentTaskQueue,
	refs promptEvaluationEvidenceRefs,
	trials []PromptEvaluationTrialResponse,
	usages []PromptEvaluationTaskUsageResponse,
	messages []protocol.TaskMessagePayload,
	traceEvents []TaskTraceEventResponse,
) map[string]any {
	context := map[string]any{
		"工作区":     uuidToString(run.WorkspaceID),
		"提示词":     uuidToString(run.PromptID),
		"评测资产":    uuidToString(run.AssetID),
		"运行":      uuidToString(run.ID),
		"任务":      uuidToString(run.TaskID),
		"触发来源":    run.TriggerSource,
		"执行Agent": uuidToString(run.AgentID),
		"模型":      run.Model,
		"运行时":     run.RuntimeProvider,
		"运行时标识":   uuidToString(run.RuntimeID),
		"状态":      run.Status,
		"创建者":     uuidToString(run.CreatedBy),
		"开始时间":    timestampToString(run.StartedAt),
		"结束时间":    timestampToString(run.CompletedAt),
		"总耗时ms":   run.TotalDurationMs,
		"输入token": run.InputTokens,
		"输出token": run.OutputTokens,
		"预估成本":    run.EstimatedCost,
		"失败原因":    run.FailureReason,
		"评估结论":    run.Conclusion,
		"证据完整性": map[string]any{
			"用例数":       len(trials),
			"任务用量条数":    len(usages),
			"任务消息条数":    len(messages),
			"trace事件条数": len(traceEvents),
		},
		"输入输出摘要": buildPromptEvaluationIOContext(trials, messages),
	}
	if refs.Prompt != nil {
		context["提示词名称"] = refs.Prompt.Name
		context["提示词类型"] = refs.Prompt.PromptType
		context["提示词版本"] = refs.Prompt.Version
	}
	if refs.Asset != nil {
		context["评测资产名称"] = refs.Asset.Name
		context["评测资产类型"] = refs.Asset.AssetType
	}
	if refs.Agent != nil {
		context["执行Agent名称"] = refs.Agent.Name
		context["执行Agent状态"] = refs.Agent.Status
	}
	if refs.Runtime != nil {
		context["运行时名称"] = refs.Runtime.Name
		context["运行时状态"] = refs.Runtime.Status
		context["运行时提供方"] = refs.Runtime.Provider
	}
	if refs.Issue != nil {
		context["issue标题"] = refs.Issue.Title
		context["issue状态"] = refs.Issue.Status
		context["issue编号"] = refs.Issue.Number
		context["项目"] = uuidToString(refs.Issue.ProjectID)
		context["承接方类型"] = refs.Issue.AssigneeType.String
		context["承接方"] = uuidToString(refs.Issue.AssigneeID)
	}
	if refs.Project != nil {
		context["项目"] = uuidToString(refs.Project.ID)
		context["项目名称"] = refs.Project.Title
		context["项目状态"] = refs.Project.Status
	}
	if refs.Squad != nil {
		context["小队"] = uuidToString(refs.Squad.ID)
		context["小队名称"] = refs.Squad.Name
	}
	if run.ChatSessionID.Valid {
		context["会话"] = uuidToString(run.ChatSessionID)
	}
	if task != nil {
		context["issue"] = uuidToString(task.IssueID)
		context["任务状态"] = task.Status
		context["触发评论"] = uuidToString(task.TriggerCommentID)
		context["自动化运行"] = uuidToString(task.AutopilotRunID)
		context["发起人"] = uuidToString(task.InitiatorUserID)
		context["任务触发摘要"] = task.TriggerSummary.String
		context["任务尝试次数"] = task.Attempt
		context["任务最大尝试次数"] = task.MaxAttempts
		context["是否队长任务"] = task.IsLeaderTask
	}
	return context
}

func buildPromptEvaluationIOContext(trials []PromptEvaluationTrialResponse, messages []protocol.TaskMessagePayload) map[string]any {
	context := map[string]any{
		"用例输入摘要": "未记录",
		"用例输出摘要": "未记录",
		"消息摘要":   "未记录",
	}
	if len(trials) > 0 {
		context["用例输入摘要"] = prompteval.TruncatePromptEvaluationEvidence(promptEvaluationEvidenceSummaryString(trials[0].Input), 300)
		context["用例输出摘要"] = prompteval.TruncatePromptEvaluationEvidence(promptEvaluationEvidenceSummaryString(trials[0].Output), 300)
	}
	if len(messages) > 0 {
		message := messages[len(messages)-1]
		context["消息摘要"] = prompteval.TruncatePromptEvaluationEvidence(firstNonEmptyPromptEvaluationString(message.Content, message.Output, message.Type), 300)
	}
	return context
}

func validPromptEvaluationEvidenceSnapshotType(value string) bool {
	switch value {
	case "手动归档", "验收归档", "自动归档":
		return true
	default:
		return false
	}
}

func buildPromptEvaluationEvidenceSnapshotSummary(evidence PromptEvaluationRunEvidenceResponse, generatedAt time.Time, insight map[string]any) map[string]any {
	run := evidence.Run
	summary := map[string]any{
		"语义版本":                "multica.prompt_evaluation.evidence_snapshot.summary.v1",
		"生成时间":                generatedAt.Format(time.RFC3339),
		"运行ID":                run.ID,
		"运行类型":                run.RunKind,
		"运行状态":                run.Status,
		"触发来源":                run.TriggerSource,
		"总用例数":                run.TotalCases,
		"通过数":                 run.PassedCases,
		"失败数":                 run.FailedCases,
		"通过率":                 run.PassRate,
		"总耗时毫秒":               run.TotalDurationMs,
		"输入token":             run.InputTokens,
		"输出token":             run.OutputTokens,
		"预估成本":                run.EstimatedCost,
		"执行Agent":             run.AgentID,
		"模型":                  run.Model,
		"runtime":             run.RuntimeID,
		"runtime供应商":          run.RuntimeProvider,
		"trace/task id":       run.TaskID,
		"失败原因":                promptEvaluationEvidenceFailureReason(evidence),
		"评估结论":                run.Conclusion,
		"trial数":              len(evidence.Trials),
		"usage行数":             len(evidence.TaskUsage),
		"任务消息数":               len(evidence.TaskMessages),
		"trace事件数":            len(evidence.TraceEvents),
		"execution span数":     len(evidence.ExecutionSpans),
		"tool call chain数":    len(evidence.ToolCallChains),
		"tool call summary行数": len(evidence.ToolCallSummary),
		"上下文字段数":              len(evidence.Context),
	}
	if insight != nil {
		summary["服务端解释"] = map[string]any{
			"质量判断":   prompteval.StringFromAny(insight["质量判断"]),
			"建议动作":   prompteval.StringFromAny(insight["建议动作"]),
			"失败主因":   prompteval.StringFromAny(insight["失败主因"]),
			"维度摘要数":  len(anySliceFromRecord(insight, "维度评分摘要")),
			"维度趋势数":  len(anySliceFromRecord(insight, "维度评分趋势")),
			"优化候选数":  len(anySliceFromRecord(insight, "优化候选证据")),
			"单位通过成本": insight["单位通过成本"],
		}
	}
	return summary
}

func (h *Handler) buildPromptEvaluationEvidenceSnapshotInsight(ctx context.Context, workspaceID pgtype.UUID, evidence PromptEvaluationRunEvidenceResponse) (map[string]any, error) {
	run := evidence.Run
	assetID := parseUUID(run.AssetID)
	summaries, err := h.Queries.ListPromptEvaluationDimensionScoreSummaries(ctx, db.ListPromptEvaluationDimensionScoreSummariesParams{
		WorkspaceID: workspaceID,
		AssetID:     assetID,
	})
	if err != nil {
		return nil, err
	}
	trends, err := h.Queries.ListPromptEvaluationDimensionScoreTrends(ctx, db.ListPromptEvaluationDimensionScoreTrendsParams{
		WorkspaceID: workspaceID,
		AssetID:     assetID,
	})
	if err != nil {
		return nil, err
	}
	candidates, err := h.Queries.ListPromptEvaluationOptimizationCandidates(ctx, db.ListPromptEvaluationOptimizationCandidatesParams{
		WorkspaceID: workspaceID,
		RunID:       parseUUID(run.ID),
		Limit:       20,
	})
	if err != nil {
		return nil, err
	}
	summaryItems := make([]PromptEvaluationDimensionScoreSummaryResponse, len(summaries))
	for i, item := range summaries {
		summaryItems[i] = promptEvaluationDimensionScoreSummaryToResponse(item)
	}
	trendItems := make([]PromptEvaluationDimensionScoreTrendResponse, len(trends))
	for i, item := range trends {
		trendItems[i] = promptEvaluationDimensionScoreTrendToResponse(item)
	}
	candidateItems := make([]map[string]any, 0, len(candidates))
	for _, item := range candidates {
		resp := promptEvaluationOptimizationCandidateToResponse(item)
		metrics, _ := resp.Metrics.(map[string]any)
		candidateItems = append(candidateItems, map[string]any{
			"id":        resp.ID,
			"run_id":    resp.RunID,
			"prompt_id": resp.PromptID,
			"状态":        resp.Status,
			"失败用例数":     resp.FailedCaseCount,
			"候选优先级":     prompteval.StringFromAny(metrics["候选优先级"]),
			"失败维度":      metrics["失败维度"],
			"优先级依据":     prompteval.StringFromAny(metrics["候选优先级依据"]),
			"修改依据":      resp.Rationale,
		})
	}
	return map[string]any{
		"语义版本":   "multica.prompt_evaluation.evidence_snapshot.insight.v1",
		"运行ID":   run.ID,
		"资产ID":   run.AssetID,
		"质量判断":   promptEvaluationEvidenceQualityLabel(run),
		"单位通过成本": promptEvaluationEvidenceCostPerPassedCase(run),
		"失败主因":   promptEvaluationEvidenceFailureReason(evidence),
		"建议动作":   promptEvaluationEvidenceRecommendation(evidence, len(candidateItems)),
		"维度评分摘要": summaryItems,
		"维度评分趋势": trendItems,
		"优化候选证据": candidateItems,
	}, nil
}

func anySliceFromRecord(record map[string]any, key string) []any {
	if record == nil {
		return nil
	}
	switch value := record[key].(type) {
	case []any:
		return value
	case []PromptEvaluationDimensionScoreSummaryResponse:
		result := make([]any, len(value))
		for i := range value {
			result[i] = value[i]
		}
		return result
	case []PromptEvaluationDimensionScoreTrendResponse:
		result := make([]any, len(value))
		for i := range value {
			result[i] = value[i]
		}
		return result
	case []map[string]any:
		result := make([]any, len(value))
		for i := range value {
			result[i] = value[i]
		}
		return result
	default:
		return nil
	}
}

func promptEvaluationEvidenceQualityLabel(run PromptEvaluationRunResponse) string {
	if run.TotalCases == 0 {
		return "暂无用例"
	}
	if run.Status == "失败" || run.FailedCases > 0 {
		return "质量风险高"
	}
	if run.PassRate >= 0.8 {
		return "质量稳定"
	}
	if run.PassRate >= 0.5 {
		return "质量待优化"
	}
	return "质量风险高"
}

func promptEvaluationEvidenceCostPerPassedCase(run PromptEvaluationRunResponse) float64 {
	if run.PassedCases <= 0 {
		return 0
	}
	return run.EstimatedCost / float64(run.PassedCases)
}

func promptEvaluationEvidenceRecommendation(evidence PromptEvaluationRunEvidenceResponse, candidateCount int) string {
	run := evidence.Run
	if run.Status == "失败" || run.FailedCases > 0 {
		if candidateCount > 0 {
			return "已有优化候选，建议验收者优先确认高优先级候选并回归同一数据集版本。"
		}
		return "优先根据失败主因生成优化候选，再用同一实验数据回归验证。"
	}
	if run.PassedCases > 0 && promptEvaluationEvidenceCostPerPassedCase(run) > 0.2 {
		return "质量可用但单位通过成本偏高，建议压缩提示词上下文或尝试更轻模型复测。"
	}
	if run.PassedCases > 0 {
		return "当前运行可作为质量基线，后续观察维度趋势和提示词版本变化。"
	}
	return "先补充可评分运行，形成质量和成本基线。"
}

func promptEvaluationEvidenceFailureReason(evidence PromptEvaluationRunEvidenceResponse) string {
	if strings.TrimSpace(evidence.Run.FailureReason) != "" {
		return evidence.Run.FailureReason
	}
	for i := len(evidence.TraceEvents) - 1; i >= 0; i-- {
		if strings.TrimSpace(evidence.TraceEvents[i].FailureReason) != "" {
			return evidence.TraceEvents[i].FailureReason
		}
	}
	return "无"
}

func promptEvaluationEvidenceSummaryString(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(raw)
}
