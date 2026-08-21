package handler

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/util/prompteval"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func promptEvaluationPayloadWithCases(payload map[string]any, cases []map[string]any) map[string]any {
	result := make(map[string]any, len(payload)+2)
	for key, value := range payload {
		result[key] = value
	}
	result["cases"] = cases
	result["用例"] = cases
	return result
}

func buildPromptEvaluationRunResult(asset db.PromptEvaluationAsset, prompt db.PromptLibraryItem, payload map[string]any, cases []map[string]any) promptEvaluationRunResult {
	started := time.Now()
	if len(cases) == 0 {
		cases = promptEvaluationCases(payload)
	}
	results := make([]promptEvaluationCaseRunResult, 0, len(cases))
	passed := 0
	missingCount := 0
	inputTokens := 0
	outputTokens := 0
	for idx, c := range cases {
		name := prompteval.StringFromAny(firstValue(c, "name", "名称"))
		if name == "" {
			name = "用例 " + strconv.Itoa(idx+1)
		}
		variables := stringMapFromAny(firstValue(c, "variables", "变量", "输入变量"))
		expected := stringListFromAny(firstValue(c, "expected_contains", "期望包含", "期望"))
		rendered, used, missing := renderPromptContent(prompt.Content, prompt.Variables, variables)
		inputTokens += estimatePromptEvaluationTokens(prompt.Content)
		outputTokens += estimatePromptEvaluationTokens(rendered)
		matched := make([]string, 0, len(expected))
		for _, expectedText := range expected {
			if expectedText != "" && strings.Contains(rendered, expectedText) {
				matched = append(matched, expectedText)
			}
		}
		status := "通过"
		if len(missing) > 0 || len(matched) != len(expected) {
			status = "失败"
		} else {
			passed++
		}
		missingCount += len(missing)
		results = append(results, promptEvaluationCaseRunResult{
			Name:             name,
			Status:           status,
			Variables:        variables,
			RenderedPrompt:   rendered,
			UsedVariables:    used,
			MissingVariables: missing,
			ExpectedContains: expected,
			MatchedContains:  matched,
		})
	}
	durationMs := time.Since(started).Milliseconds()
	if durationMs < 1 {
		durationMs = 1
	}
	totalCases := len(results)
	failed := totalCases - passed
	passRate := 0.0
	averageMs := int64(0)
	if totalCases > 0 {
		passRate = float64(passed) / float64(totalCases)
		averageMs = durationMs / int64(totalCases)
	}
	failureReason := "无"
	conclusion := "通过"
	if failed > 0 {
		failureReason = "存在缺失变量或期望内容未命中"
		conclusion = "未通过"
	}
	dimensionScores := buildPromptEvaluationExperimentDimensionScores(promptEvaluationExperimentDimensionsForAsset(asset.AssetType, payload), results)
	return promptEvaluationRunResult{
		RunAt:             time.Now().UTC().Format(time.RFC3339),
		AssetType:         asset.AssetType,
		PromptName:        prompt.Name,
		PromptVersion:     prompt.Version,
		TotalCases:        totalCases,
		PassedCases:       passed,
		FailedCases:       failed,
		PassRate:          passRate,
		TotalDurationMs:   durationMs,
		AverageDurationMs: averageMs,
		InputTokens:       inputTokens,
		OutputTokens:      outputTokens,
		EstimatedCost:     0,
		AgentName:         "本地提示词渲染器",
		Model:             "本地模板渲染检查",
		Runtime:           "server",
		TraceTaskID:       "未创建智能体任务",
		FailureReason:     failureReason,
		Conclusion:        conclusion,
		MissingVarCount:   missingCount,
		CaseResults:       results,
		DimensionScores:   dimensionScores,
	}
}

func buildPromptEvaluationExperimentDimensionScores(dimensions []normalizedPromptEvaluationExperimentDimension, results []promptEvaluationCaseRunResult) []promptEvaluationExperimentDimensionScore {
	if len(dimensions) == 0 {
		return nil
	}
	scores := make([]promptEvaluationExperimentDimensionScore, 0, len(dimensions))
	for idx, dimension := range dimensions {
		name := strings.TrimSpace(dimension.Name)
		if name == "" {
			name = "维度 " + strconv.Itoa(idx+1)
		}
		passed := 0
		rule := promptEvaluationDimensionRule(name)
		for _, result := range results {
			if promptEvaluationDimensionCasePassed(name, result) {
				passed++
			}
		}
		total := len(results)
		score := 0.0
		status := "无用例"
		if total > 0 {
			score = float64(passed) / float64(total)
			status = "已评分"
		}
		scores = append(scores, promptEvaluationExperimentDimensionScore{
			DimensionIndex: int32(idx),
			DimensionName:  name,
			Score:          score,
			PassedCases:    passed,
			TotalCases:     total,
			Status:         status,
			Rule:           rule,
			Evidence:       fmt.Sprintf("%s：%d/%d", rule, passed, total),
		})
	}
	return scores
}

func pendingPromptEvaluationExperimentDimensionScores(dimensions []normalizedPromptEvaluationExperimentDimension, totalCases int) []promptEvaluationExperimentDimensionScore {
	if len(dimensions) == 0 {
		return nil
	}
	scores := make([]promptEvaluationExperimentDimensionScore, 0, len(dimensions))
	for idx, dimension := range dimensions {
		name := strings.TrimSpace(dimension.Name)
		if name == "" {
			name = "维度 " + strconv.Itoa(idx+1)
		}
		rule := promptEvaluationDimensionRule(name)
		scores = append(scores, promptEvaluationExperimentDimensionScore{
			DimensionIndex: int32(idx),
			DimensionName:  name,
			Score:          0,
			PassedCases:    0,
			TotalCases:     totalCases,
			Status:         "待执行",
			Rule:           rule,
			Evidence:       "等待智能体返回结构化评估结果后再评分",
		})
	}
	return scores
}

func promptEvaluationDimensionCasePassed(dimensionName string, result promptEvaluationCaseRunResult) bool {
	normalized := strings.ToLower(strings.TrimSpace(dimensionName))
	switch {
	case strings.Contains(normalized, "缺失变量") || strings.Contains(normalized, "变量"):
		return len(result.MissingVariables) == 0
	case strings.Contains(normalized, "中文"):
		return prompteval.ContainsHanRune(result.RenderedPrompt)
	case strings.Contains(normalized, "命中") || strings.Contains(normalized, "覆盖") || strings.Contains(normalized, "期望"):
		return len(result.ExpectedContains) == len(result.MatchedContains)
	default:
		return result.Status == "通过"
	}
}

func promptEvaluationDimensionRule(dimensionName string) string {
	normalized := strings.ToLower(strings.TrimSpace(dimensionName))
	switch {
	case strings.Contains(normalized, "缺失变量") || strings.Contains(normalized, "变量"):
		return "逐用例检查缺失变量数为 0"
	case strings.Contains(normalized, "中文"):
		return "逐用例检查渲染提示词包含中文字符"
	case strings.Contains(normalized, "命中") || strings.Contains(normalized, "覆盖") || strings.Contains(normalized, "期望"):
		return "逐用例检查期望内容全部命中"
	default:
		return "逐用例沿用总体通过状态"
	}
}

func promptEvaluationCases(payload map[string]any) []map[string]any {
	raw := payload["cases"]
	if arr, ok := raw.([]map[string]any); ok && len(arr) > 0 {
		cases := make([]map[string]any, len(arr))
		copy(cases, arr)
		return cases
	}
	if arr, ok := raw.([]any); ok {
		cases := make([]map[string]any, 0, len(arr))
		for _, item := range arr {
			if m, ok := item.(map[string]any); ok {
				cases = append(cases, m)
			}
		}
		if len(cases) > 0 {
			return cases
		}
	}
	return []map[string]any{{"名称": "默认用例", "变量": firstValue(payload, "variables", "变量", "输入变量")}}
}

func normalizePromptEvaluationPayloadObject(payload map[string]any) map[string]any {
	normalized := make(map[string]any, len(payload)+4)
	for key, value := range payload {
		normalized[key] = value
	}
	normalized["schema_version"] = 1
	normalized["schema"] = "multica.training_evaluation.payload.v1"
	normalized["语义版本"] = "multica.training_evaluation.v1"
	normalized["payload_contract"] = map[string]any{
		"schema_version": 1,
		"schema":         "multica.training_evaluation.payload.v1",
		"cases":          "cases[].case_name / variables / expected_contains / tags",
		"写入策略":           "新建和更新统一写入规范 cases。",
	}
	cases := promptEvaluationCases(payload)
	normalizedCases := make([]map[string]any, 0, len(cases))
	for index, item := range cases {
		c := normalizePromptEvaluationCase(index, item)
		expectedContains := c.ExpectedContains
		if expectedContains == nil {
			expectedContains = []string{}
		}
		tags := c.Tags
		if tags == nil {
			tags = []string{}
		}
		normalizedCases = append(normalizedCases, map[string]any{
			"case_name":         c.Name,
			"variables":         c.Variables,
			"expected_contains": expectedContains,
			"tags":              tags,
		})
	}
	normalized["cases"] = normalizedCases
	return normalized
}

func normalizePromptEvaluationCase(index int, item map[string]any) normalizedPromptEvaluationCase {
	name := prompteval.StringFromAny(firstValue(item, "name", "名称", "case_name", "用例名称"))
	if name == "" {
		name = "用例 " + strconv.Itoa(index+1)
	}
	variables := stringMapFromAny(firstValue(item, "variables", "变量", "输入变量"))
	expectedContains := stringListFromAny(firstValue(item, "expected_contains", "期望包含", "期望"))
	input := map[string]any{
		"变量":   variables,
		"原始输入": firstValue(item, "input", "输入"),
	}
	expected := map[string]any{
		"期望包含": expectedContains,
		"原始期望": firstValue(item, "expected", "期望"),
	}
	return normalizedPromptEvaluationCase{
		Name:             name,
		Variables:        variables,
		ExpectedContains: expectedContains,
		Input:            input,
		Expected:         expected,
		Tags:             stringListFromAny(firstValue(item, "tags", "标签")),
	}
}

func estimatePromptEvaluationTokens(value string) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	tokens := len([]rune(value)) / 4
	if tokens < 1 {
		return 1
	}
	return tokens
}

func renderPromptContent(content string, variablesRaw []byte, values map[string]string) (string, []string, []string) {
	defaults := promptVariableDefaults(variablesRaw)
	usedSet := map[string]bool{}
	missingSet := map[string]bool{}
	rendered := promptTemplateVariablePattern.ReplaceAllStringFunc(content, func(match string) string {
		parts := promptTemplateVariablePattern.FindStringSubmatch(match)
		if len(parts) < 2 {
			return match
		}
		name := strings.TrimSpace(parts[1])
		usedSet[name] = true
		if value, ok := values[name]; ok {
			return value
		}
		if value, ok := defaults[name]; ok {
			return value
		}
		missingSet[name] = true
		return match
	})
	return rendered, sortedBoolKeys(usedSet), sortedBoolKeys(missingSet)
}

func promptVariableDefaults(raw []byte) map[string]string {
	var variables []map[string]any
	_ = json.Unmarshal(raw, &variables)
	defaults := map[string]string{}
	for _, variable := range variables {
		name := prompteval.StringFromAny(variable["name"])
		if name == "" {
			continue
		}
		if value, ok := variable["default_value"]; ok {
			defaults[name] = prompteval.StringFromAny(value)
		}
	}
	return defaults
}

func appendPromptEvaluationRunHistory(raw any, result promptEvaluationRunResult) []any {
	history, _ := raw.([]any)
	next := append([]any{result}, history...)
	if len(next) > 20 {
		next = next[:20]
	}
	return next
}

func applyPromptEvaluationOptimizationRunContract(payload map[string]any, assetID string, sourceRunID string, runIndex map[string]any, eventName string) {
	if payload == nil {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	rounds := promptEvaluationAnyList(payload["优化轮次"])
	roundIndex := intFromAny(runIndex["轮次"])
	if roundIndex <= 0 {
		roundIndex = len(rounds) + 1
		runIndex["轮次"] = roundIndex
	}
	retryIndex := intFromAny(runIndex["重试序号"])
	round := map[string]any{
		"轮次":              roundIndex,
		"重试序号":            retryIndex,
		"运行ID":            prompteval.StringFromAny(runIndex["run_id"]),
		"来源运行":            sourceRunID,
		"状态":              prompteval.StringFromAny(runIndex["状态"]),
		"执行Agent":         prompteval.StringFromAny(runIndex["执行Agent"]),
		"模型":              prompteval.StringFromAny(runIndex["模型"]),
		"runtime":         prompteval.StringFromAny(runIndex["runtime"]),
		"runtime_id":      prompteval.StringFromAny(runIndex["runtime_id"]),
		"trace/task id":   prompteval.StringFromAny(runIndex["trace/task id"]),
		"chat_session_id": prompteval.StringFromAny(runIndex["chat_session_id"]),
		"创建时间":            now,
		"验收口径": []string{
			"必须保留中文语义",
			"必须给出优化候选正文和逐条修改依据",
			"必须能回读 task、trace、消息、用量和人工确认状态",
		},
	}
	rounds = append([]any{round}, rounds...)
	if len(rounds) > 20 {
		rounds = rounds[:20]
	}
	logs := promptEvaluationAnyList(payload["日志流"])
	logs = append(logs, map[string]any{
		"seq":   len(logs) + 1,
		"事件":    eventName,
		"状态":    prompteval.StringFromAny(runIndex["状态"]),
		"轮次":    roundIndex,
		"运行ID":  prompteval.StringFromAny(runIndex["run_id"]),
		"任务ID":  prompteval.StringFromAny(runIndex["trace/task id"]),
		"消息":    eventName + "已入队，等待智能体输出候选并回写证据",
		"记录时间":  now,
		"可回读证据": []string{"运行历史", "运行证据", "task messages", "trace 事件", "task usage"},
	})
	if len(logs) > 50 {
		logs = logs[len(logs)-50:]
	}
	payload["schema"] = "multica.training_evaluation.optimization_run.v2"
	payload["语义版本"] = "multica.training_evaluation.optimization_run.v2"
	payload["优化运行契约"] = map[string]any{
		"schema": "multica.training_evaluation.optimization_run.v2",
		"资产ID":   assetID,
		"来源运行":   sourceRunID,
		"轮次字段":   "优化轮次",
		"日志字段":   "日志流",
		"重试入口":   "/api/prompt-evaluation-assets/" + assetID + "/agent-run",
		"候选发布入口": "/api/prompt-evaluation-optimization-candidates/{candidate_id}/publish",
		"人工确认要求": "优化运行只产生候选和证据；发布新版本必须经过人工确认。",
		"数据回读要求": []string{"资产详情", "运行历史", "运行证据", "优化候选", "提示词版本历史"},
	}
	payload["优化轮次"] = rounds
	payload["日志流"] = logs
	currentRetryCount := retryIndex
	if currentRetryCount < promptEvaluationOptimizationRetryCount(payload) {
		currentRetryCount = promptEvaluationOptimizationRetryCount(payload)
	}
	payload["重试策略"] = map[string]any{
		"允许重试":   true,
		"当前重试次数": currentRetryCount,
		"重试入口":   "/api/prompt-evaluation-assets/" + assetID + "/agent-run",
		"重试说明":   "重试会创建新的真实智能体任务和运行记录，不覆盖已有轮次。",
	}
}

func promptEvaluationOptimizationRoundCount(payload map[string]any) int {
	return len(promptEvaluationAnyList(payload["优化轮次"]))
}

func promptEvaluationOptimizationRetryCount(payload map[string]any) int {
	count := 0
	for _, item := range promptEvaluationAnyList(payload["优化轮次"]) {
		record, ok := item.(map[string]any)
		if !ok {
			continue
		}
		retry := intFromAny(record["重试序号"])
		if retry > count {
			count = retry
		}
	}
	return count
}

func promptEvaluationAnyList(raw any) []any {
	if values, ok := raw.([]any); ok {
		return values
	}
	return nil
}

func intFromAny(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return int(parsed)
	case string:
		parsed, _ := strconv.Atoi(strings.TrimSpace(typed))
		return parsed
	default:
		return 0
	}
}

func floatFromAny(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int32:
		return float64(typed)
	case int64:
		return float64(typed)
	case json.Number:
		parsed, _ := typed.Float64()
		return parsed
	case string:
		parsed, _ := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return parsed
	default:
		return 0
	}
}

func promptEvaluationAgentRunMessage(assetType string) string {
	if assetType == promptEvaluationAssetOptimize {
		return "优化运行重试已入队；请通过运行历史、日志流和证据面板追踪新轮次。"
	}
	return "真实智能体任务已入队；请通过 task messages、usage 和运行历史追踪结果。"
}
