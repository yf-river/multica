package handler

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (h *Handler) loadPromptEvaluationAsset(w http.ResponseWriter, r *http.Request) (db.PromptEvaluationAsset, bool) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return db.PromptEvaluationAsset{}, false
	}
	assetID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "prompt evaluation asset id")
	if !ok {
		return db.PromptEvaluationAsset{}, false
	}
	asset, err := h.Queries.GetPromptEvaluationAssetInWorkspace(r.Context(), db.GetPromptEvaluationAssetInWorkspaceParams{
		ID:          assetID,
		WorkspaceID: workspaceUUID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "prompt evaluation asset not found")
			return db.PromptEvaluationAsset{}, false
		}
		writeError(w, http.StatusInternalServerError, "failed to load prompt evaluation asset")
		return db.PromptEvaluationAsset{}, false
	}
	return asset, true
}

func (h *Handler) ensurePromptEvaluationAgent(w http.ResponseWriter, r *http.Request, workspaceID pgtype.UUID, ownerID pgtype.UUID, member db.Member) (db.Agent, db.AgentRuntime, bool) {
	runtime, ok := h.selectPromptEvaluationRuntime(w, r, workspaceID, member)
	if !ok {
		return db.Agent{}, db.AgentRuntime{}, false
	}
	agents, err := h.Queries.ListAllAgents(r.Context(), workspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list agents for training evaluation")
		return db.Agent{}, db.AgentRuntime{}, false
	}
	if agentRow, runtimeRow, ok := h.findSOPPromptEvaluationAgent(r.Context(), workspaceID, member, agents); ok {
		return agentRow, runtimeRow, true
	}
	instructions := promptEvaluationAgentInstructions()
	for _, existing := range agents {
		if existing.Name != promptEvaluationAgentName {
			continue
		}
		if uuidToString(existing.RuntimeID) == uuidToString(runtime.ID) &&
			existing.Model.String == promptEvaluationAgentModel() &&
			existing.Instructions == instructions &&
			existing.Name == promptEvaluationAgentName {
			if existing.ArchivedAt.Valid {
				restored, err := h.Queries.RestoreAgent(r.Context(), existing.ID)
				if err != nil {
					writeError(w, http.StatusInternalServerError, "failed to restore training evaluation agent")
					return db.Agent{}, db.AgentRuntime{}, false
				}
				return restored, runtime, true
			}
			return existing, runtime, true
		}
		updated, err := h.Queries.UpdateAgent(r.Context(), db.UpdateAgentParams{
			ID:           existing.ID,
			Name:         pgtype.Text{String: promptEvaluationAgentName, Valid: true},
			RuntimeMode:  pgtype.Text{String: runtime.RuntimeMode, Valid: true},
			RuntimeID:    runtime.ID,
			Instructions: pgtype.Text{String: instructions, Valid: true},
			Model:        pgtype.Text{String: promptEvaluationAgentModel(), Valid: true},
			Status:       pgtype.Text{String: "idle", Valid: true},
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to update training evaluation agent")
			return db.Agent{}, db.AgentRuntime{}, false
		}
		if updated.ArchivedAt.Valid {
			updated, err = h.Queries.RestoreAgent(r.Context(), updated.ID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to restore training evaluation agent")
				return db.Agent{}, db.AgentRuntime{}, false
			}
		}
		return updated, runtime, true
	}

	created, err := h.Queries.CreateAgent(r.Context(), db.CreateAgentParams{
		WorkspaceID:        workspaceID,
		Name:               promptEvaluationAgentName,
		Description:        "训练与评估模块自动创建，用于真实执行提示词、数据集和测试套件。",
		RuntimeMode:        runtime.RuntimeMode,
		RuntimeConfig:      []byte("{}"),
		RuntimeID:          runtime.ID,
		Scope:              "workspace",
		MaxConcurrentTasks: defaultAgentMaxConcurrentTasks,
		OwnerID:            ownerID,
		Instructions:       instructions,
		CustomEnv:          []byte("{}"),
		CustomArgs:         []byte("[]"),
		Model:              pgtype.Text{String: promptEvaluationAgentModel(), Valid: true},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create training evaluation agent")
		return db.Agent{}, db.AgentRuntime{}, false
	}
	return created, runtime, true
}

func (h *Handler) findSOPPromptEvaluationAgent(ctx context.Context, workspaceID pgtype.UUID, member db.Member, agents []db.Agent) (db.Agent, db.AgentRuntime, bool) {
	required := map[string]bool{
		"PM":            false,
		"01-clarify":    false,
		"02-design":     false,
		"03-task-split": false,
		"04-implement":  false,
		"05-verify":     false,
	}
	aliases := map[string]string{
		"PM-项目经理": "PM",
		"pm":      "PM",
		"01-需求澄清": "01-clarify",
		"02-方案设计": "02-design",
		"03-任务拆分": "03-task-split",
		"04-开发":   "04-implement",
		"05-验证测试": "05-verify",
		"05-测试":   "05-verify",
	}
	var verifier *db.Agent
	for i := range agents {
		key := agents[i].Name
		if alias, ok := aliases[key]; ok {
			key = alias
		}
		if _, ok := required[key]; ok {
			required[key] = true
		}
		if key == "05-verify" {
			verifier = &agents[i]
		}
	}
	for _, present := range required {
		if !present {
			return db.Agent{}, db.AgentRuntime{}, false
		}
	}
	if verifier == nil {
		return db.Agent{}, db.AgentRuntime{}, false
	}
	runtime, err := h.Queries.GetAgentRuntimeForWorkspace(ctx, db.GetAgentRuntimeForWorkspaceParams{
		ID:          verifier.RuntimeID,
		WorkspaceID: workspaceID,
	})
	if err != nil || runtime.Status != "online" || !canUseRuntimeForAgent(member, runtime) {
		return db.Agent{}, db.AgentRuntime{}, false
	}
	if !runtime.LastSeenAt.Valid || time.Since(runtime.LastSeenAt.Time) > promptEvaluationRuntimeFreshTTL {
		return db.Agent{}, db.AgentRuntime{}, false
	}
	return *verifier, runtime, true
}

func (h *Handler) selectPromptEvaluationExecutionAgent(w http.ResponseWriter, r *http.Request, workspaceID pgtype.UUID, ownerID pgtype.UUID, member db.Member, payload map[string]any) (db.Agent, db.AgentRuntime, bool) {
	requestedAgentID := promptEvaluationRequestedAgentID(payload)
	if requestedAgentID == "" {
		return h.ensurePromptEvaluationAgent(w, r, workspaceID, ownerID, member)
	}
	agentID, err := util.ParseUUID(requestedAgentID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "执行智能体标识无效")
		return db.Agent{}, db.AgentRuntime{}, false
	}
	agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID:          agentID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "执行智能体不属于当前工作区")
		return db.Agent{}, db.AgentRuntime{}, false
	}
	if agent.ArchivedAt.Valid {
		writeError(w, http.StatusBadRequest, "执行智能体已归档，不能创建真实调试任务")
		return db.Agent{}, db.AgentRuntime{}, false
	}
	runtime, err := h.Queries.GetAgentRuntimeForWorkspace(r.Context(), db.GetAgentRuntimeForWorkspaceParams{
		ID:          agent.RuntimeID,
		WorkspaceID: workspaceID,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "执行智能体绑定的运行时不可用")
		return db.Agent{}, db.AgentRuntime{}, false
	}
	if !canUseRuntimeForAgent(member, runtime) {
		writeError(w, http.StatusForbidden, "当前成员不能使用该执行智能体绑定的运行时")
		return db.Agent{}, db.AgentRuntime{}, false
	}
	if runtime.Status != "online" {
		writeError(w, http.StatusServiceUnavailable, "执行智能体绑定的运行时不在线，请先启动运行时")
		return db.Agent{}, db.AgentRuntime{}, false
	}
	return agent, runtime, true
}

func (h *Handler) selectPromptEvaluationRuntime(w http.ResponseWriter, r *http.Request, workspaceID pgtype.UUID, member db.Member) (db.AgentRuntime, bool) {
	readiness, err := h.promptEvaluationRuntimeReadiness(r.Context(), workspaceID, member)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list runtimes for training evaluation")
		return db.AgentRuntime{}, false
	}
	if readiness.Status == "就绪" && readiness.Runtime != nil {
		runtimeID := parseUUID(readiness.Runtime.ID)
		runtime, err := h.Queries.GetAgentRuntimeForWorkspace(r.Context(), db.GetAgentRuntimeForWorkspaceParams{
			ID:          runtimeID,
			WorkspaceID: workspaceID,
		})
		if err == nil {
			return runtime, true
		}
	}
	writeError(w, http.StatusServiceUnavailable, readiness.Fix)
	return db.AgentRuntime{}, false
}

func promptEvaluationRequestedAgentID(payload map[string]any) string {
	for _, raw := range []any{
		payload["执行智能体"],
		firstValue(payload, "agent_id", "execution_agent_id", "target_agent_id", "执行智能体标识", "目标智能体标识"),
		firstValue(payload, "execution_agent", "target_agent"),
		firstValue(asMap(payload["历史调试载荷"]), "执行智能体", "execution_agent", "agent_id", "execution_agent_id", "target_agent_id", "执行智能体标识", "目标智能体标识"),
		firstValue(asMap(payload["运行环境"]), "执行智能体", "execution_agent", "agent_id", "execution_agent_id", "target_agent_id", "执行智能体标识", "目标智能体标识"),
	} {
		if id := promptEvaluationAgentIDFromAny(raw); id != "" {
			return id
		}
	}
	return ""
}

func promptEvaluationAgentIDFromAny(raw any) string {
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case map[string]any:
		return stringFromAny(firstValue(v, "agent_id", "id", "智能体标识", "执行智能体标识"))
	default:
		return ""
	}
}

func promptEvaluationModelForAgent(agent db.Agent) string {
	if agent.Model.Valid && strings.TrimSpace(agent.Model.String) != "" {
		return strings.TrimSpace(agent.Model.String)
	}
	return promptEvaluationAgentModel()
}

func (h *Handler) promptEvaluationRuntimeReadiness(ctx context.Context, workspaceID pgtype.UUID, member db.Member) (PromptEvaluationRuntimeReadinessResponse, error) {
	checkedAt := time.Now().UTC()
	runtimes, err := h.Queries.ListAgentRuntimes(ctx, workspaceID)
	if err != nil {
		return PromptEvaluationRuntimeReadinessResponse{}, err
	}
	provider := promptEvaluationAgentProvider()
	providerName := strings.ToUpper(provider[:1]) + provider[1:]
	inaccessibleRuntime := 0
	var best *db.AgentRuntime
	for i := range runtimes {
		runtime := runtimes[i]
		if !strings.EqualFold(runtime.Provider, provider) {
			continue
		}
		if !canUseRuntimeForAgent(member, runtime) {
			inaccessibleRuntime++
			continue
		}
		if best == nil || runtimeReadinessRank(runtime, checkedAt) > runtimeReadinessRank(*best, checkedAt) {
			best = &runtime
		}
	}
	if best == nil {
		if inaccessibleRuntime > 0 {
			return promptEvaluationRuntimeReadinessResponse("无权限", providerName+" 无权限", "当前工作区存在 "+providerName+" 运行时，但你没有绑定或使用权限。", "请让运行时所有者将 "+providerName+" 运行时设为公开，或由工作区管理员为训练评估智能体绑定可用运行时。", nil, checkedAt), nil
		}
		return promptEvaluationRuntimeReadinessResponse("缺失", providerName+" 缺失", "当前工作区未发现 "+providerName+" 运行时，评测运行不能执行 "+promptEvaluationAgentModel()+"。", "安装并配置 "+provider+"，启动 multica 守护进程，等待 /api/runtimes 出现 provider="+provider+" 且 status=online 的运行时。", nil, checkedAt), nil
	}
	ageSeconds := promptEvaluationRuntimeAgeSeconds(*best, checkedAt)
	respRuntime := runtimeToResponse(*best)
	if best.Status != "online" {
		return promptEvaluationRuntimeReadinessResponse("离线", providerName+" 离线", "已注册 "+providerName+" runtime「"+best.Name+"」，但当前状态是离线，不能创建真实智能体任务。", "启动 multica daemon，并确认 "+provider+" 可执行文件在 PATH 中，或设置对应 MULTICA_<PROVIDER>_PATH 后重启 daemon。", &respRuntime, checkedAt), nil
	}
	if !best.LastSeenAt.Valid || checkedAt.Sub(best.LastSeenAt.Time) > promptEvaluationRuntimeFreshTTL {
		return promptEvaluationRuntimeReadinessResponse("过期", providerName+" 心跳过期", providerName+" runtime「"+best.Name+"」状态仍是 online，但最近心跳已经超过 2 分钟，不能证明当前可执行。", "检查 multica daemon 是否仍在运行，确认网络和心跳正常后等待 last_seen_at 刷新，再创建真实智能体任务。", &respRuntime, checkedAt), nil
	}
	recentCapacityFailure, err := h.Queries.GetRecentRuntimeCapacityFailure(ctx, db.GetRecentRuntimeCapacityFailureParams{
		WorkspaceID: workspaceID,
		RuntimeID:   best.ID,
		CompletedAt: pgtype.Timestamptz{Time: checkedAt.Add(-promptEvaluationRuntimeLimitTTL), Valid: true},
		Model:       pgtype.Text{String: promptEvaluationAgentModel(), Valid: true},
	})
	if err == nil {
		detail := providerName + " runtime「" + best.Name + "」在线且心跳新鲜，但最近任务 " + uuidToString(recentCapacityFailure.ID) + " 返回模型容量或额度限制，当前不能证明 " + promptEvaluationAgentModel() + " 可执行。"
		if recentCapacityFailure.Error.Valid && strings.TrimSpace(recentCapacityFailure.Error.String) != "" {
			detail += " 最近错误：" + truncatePromptEvaluationEvidence(recentCapacityFailure.Error.String, 180)
		}
		resp := promptEvaluationRuntimeReadinessResponse("容量受限", "模型额度受限", detail, "如果持续出现 429/529，请申请 "+fallbackPromptEvaluationAgentModel+" 模型额度或让管理员调整 Agent 模型配置。", &respRuntime, checkedAt)
		resp.LastSeenAgeSeconds = ageSeconds
		return resp, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return PromptEvaluationRuntimeReadinessResponse{}, err
	}
	resp := promptEvaluationRuntimeReadinessResponse("就绪", providerName+" 在线", "已发现在线且心跳新鲜的 "+providerName+" 运行时「"+best.Name+"」，可以作为 "+promptEvaluationAgentModel()+" 的真实执行目标。", "无需修复；下一步应创建真实智能体任务并采集链路追踪、令牌、成本和输出。", &respRuntime, checkedAt)
	resp.LastSeenAgeSeconds = ageSeconds
	return resp, nil
}

func promptEvaluationRuntimeReadinessResponse(status, label, detail, fix string, runtime *AgentRuntimeResponse, checkedAt time.Time) PromptEvaluationRuntimeReadinessResponse {
	ageSeconds := int64(-1)
	if runtime != nil && runtime.LastSeenAt != nil {
		if lastSeen, err := time.Parse(time.RFC3339, *runtime.LastSeenAt); err == nil {
			ageSeconds = int64(checkedAt.Sub(lastSeen).Seconds())
		}
	}
	return PromptEvaluationRuntimeReadinessResponse{
		Status:             status,
		Label:              label,
		Detail:             detail,
		Fix:                fix,
		Model:              promptEvaluationAgentModel(),
		Runtime:            runtime,
		LastSeenAgeSeconds: ageSeconds,
		CheckedAt:          checkedAt.Format(time.RFC3339),
	}
}

func runtimeReadinessRank(runtime db.AgentRuntime, now time.Time) int {
	if runtime.Status == "online" && runtime.LastSeenAt.Valid && now.Sub(runtime.LastSeenAt.Time) <= promptEvaluationRuntimeFreshTTL {
		return 3
	}
	if runtime.Status == "online" {
		return 2
	}
	return 1
}

func promptEvaluationRuntimeAgeSeconds(runtime db.AgentRuntime, now time.Time) int64 {
	if !runtime.LastSeenAt.Valid {
		return -1
	}
	return int64(now.Sub(runtime.LastSeenAt.Time).Seconds())
}

func promptEvaluationAgentInstructions() string {
	return strings.Join([]string{
		"你是 Multica 训练与评估 Agent。你只负责执行当前提示词评估任务，必须使用中文输出。",
		"输出必须包含执行结论、逐用例结果、失败原因、改进建议、可复盘证据。",
		"最终回复必须包含一个可机读 JSON 代码块，schema 必须是 multica.training_evaluation.agent_verdict.v1，字段为：schema_version、schema、case_results、summary。",
		"不要修改业务代码，不要创建任务，不要泄露密钥。",
	}, "\n")
}

func buildPromptEvaluationAgentMessage(asset db.PromptEvaluationAsset, prompt db.PromptLibraryItem, payload map[string]any) string {
	payloadBytes, _ := json.MarshalIndent(payload, "", "  ")
	return strings.Join([]string{
		"请执行一次 Multica 训练与评估运行。",
		"",
		"【资产】",
		"名称：" + asset.Name,
		"类型：" + asset.AssetType,
		"",
		"【提示词】",
		"名称：" + prompt.Name,
		"内容：",
		prompt.Content,
		"",
		"【评估数据】",
		string(payloadBytes),
		"",
		"【输出要求】",
		"1. 全部使用中文。",
		"2. 逐条说明用例是否通过、失败原因和证据。",
		"3. 给出中文指标：总用例数、通过数、失败数、通过率、总耗时、平均耗时、输入 token、输出 token、预估成本、执行 Agent、模型、运行时、trace/任务标识、失败原因、评估结论。",
		"4. 如果需要优化提示词，只输出候选建议，不要自动替换生产版本。",
		"5. 最终必须附带一个 JSON 代码块，Multica 会用它自动回写运行历史；缺失该结构会被标记为“需人工复核”。",
		"",
		"【必须返回的 JSON schema】",
		"```json",
		`{
	  "schema_version": 1,
	  "schema": "multica.training_evaluation.agent_verdict.v1",
	  "case_results": [
    {
      "case_index": 0,
      "status": "通过 | 未通过 | 需人工复核",
      "output": "该用例的实际输出或摘要",
      "failure_reason": "无 | 失败原因 | 需复核原因",
      "evidence": {
        "命中": ["命中的期望字段"],
        "缺失": ["缺失的期望字段"],
        "trace_task_id": "如已知则填写"
      }
    }
  ],
	  "summary": {
	    "total_cases": 0,
	    "passed_cases": 0,
	    "failed_cases": 0,
	    "failure_reason": "无 | 汇总失败原因",
	    "conclusion": "中文结论",
	    "improvement_suggestions": ["中文建议"],
	    "reproducible_evidence": ["任务标识、日志、关键输出摘要"]
	  }
	}`,
		"```",
	}, "\n")
}

func promptEvaluationPayloadWithCases(payload map[string]any, cases []map[string]any) map[string]any {
	result := make(map[string]any, len(payload)+2)
	for key, value := range payload {
		result[key] = value
	}
	result["cases"] = cases
	result["用例"] = cases
	return result
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

func mustJSONBytes(value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		return []byte("{}")
	}
	return raw
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
		name := stringFromAny(firstValue(c, "name", "名称"))
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
		return containsHanRune(result.RenderedPrompt)
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

func containsHanRune(value string) bool {
	for _, r := range value {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
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
	name := stringFromAny(firstValue(item, "name", "名称", "case_name", "用例名称"))
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
		name := stringFromAny(variable["name"])
		if name == "" {
			continue
		}
		if value, ok := variable["default_value"]; ok {
			defaults[name] = stringFromAny(value)
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
		"运行ID":            stringFromAny(runIndex["run_id"]),
		"来源运行":            sourceRunID,
		"状态":              stringFromAny(runIndex["状态"]),
		"执行Agent":         stringFromAny(runIndex["执行Agent"]),
		"模型":              stringFromAny(runIndex["模型"]),
		"runtime":         stringFromAny(runIndex["runtime"]),
		"runtime_id":      stringFromAny(runIndex["runtime_id"]),
		"trace/task id":   stringFromAny(runIndex["trace/task id"]),
		"chat_session_id": stringFromAny(runIndex["chat_session_id"]),
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
		"状态":    stringFromAny(runIndex["状态"]),
		"轮次":    roundIndex,
		"运行ID":  stringFromAny(runIndex["run_id"]),
		"任务ID":  stringFromAny(runIndex["trace/task id"]),
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

func promptEvaluationDatasetRowsFingerprint(rows []db.PromptEvaluationDatasetRow) string {
	snapshot := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		snapshot = append(snapshot, map[string]any{
			"row_index":         row.RowIndex,
			"row_name":          row.RowName,
			"variables":         decodeJSONDefault(row.Variables, map[string]any{}),
			"expected_contains": decodeJSONDefault(row.ExpectedContains, []any{}),
			"expected":          decodeJSONDefault(row.Expected, map[string]any{}),
			"tags":              decodeJSONDefault(row.Tags, []any{}),
			"source":            row.Source,
		})
	}
	sum := sha256.Sum256(mustJSONBytes(snapshot))
	return fmt.Sprintf("%x", sum[:])
}

func promptEvaluationDatasetVersionRowFingerprint(row db.PromptEvaluationDatasetVersionRow) string {
	snapshot := map[string]any{
		"row_index":         row.RowIndex,
		"row_name":          row.RowName,
		"variables":         decodeJSONDefault(row.Variables, map[string]any{}),
		"expected_contains": decodeJSONDefault(row.ExpectedContains, []any{}),
		"expected":          decodeJSONDefault(row.Expected, map[string]any{}),
		"tags":              decodeJSONDefault(row.Tags, []any{}),
		"source":            row.Source,
	}
	sum := sha256.Sum256(mustJSONBytes(snapshot))
	return fmt.Sprintf("%x", sum[:])
}

func buildPromptEvaluationDatasetVersionDiff(baseRows []db.PromptEvaluationDatasetVersionRow, targetRows []db.PromptEvaluationDatasetVersionRow) PromptEvaluationDatasetVersionDiffResponse {
	baseByIndex := make(map[int32]db.PromptEvaluationDatasetVersionRow, len(baseRows))
	targetByIndex := make(map[int32]db.PromptEvaluationDatasetVersionRow, len(targetRows))
	indexSet := map[int32]bool{}
	for _, row := range baseRows {
		baseByIndex[row.RowIndex] = row
		indexSet[row.RowIndex] = true
	}
	for _, row := range targetRows {
		targetByIndex[row.RowIndex] = row
		indexSet[row.RowIndex] = true
	}
	indexes := make([]int, 0, len(indexSet))
	for index := range indexSet {
		indexes = append(indexes, int(index))
	}
	sort.Ints(indexes)

	resp := PromptEvaluationDatasetVersionDiffResponse{
		Summary: map[string]int{
			"新增":  0,
			"删除":  0,
			"变更":  0,
			"未变更": 0,
		},
		Added:     []PromptEvaluationDatasetVersionRowResponse{},
		Removed:   []PromptEvaluationDatasetVersionRowResponse{},
		Changed:   []PromptEvaluationDatasetVersionChangedRow{},
		Unchanged: []PromptEvaluationDatasetVersionRowResponse{},
	}
	for _, rawIndex := range indexes {
		index := int32(rawIndex)
		base, hasBase := baseByIndex[index]
		target, hasTarget := targetByIndex[index]
		switch {
		case !hasBase && hasTarget:
			resp.Added = append(resp.Added, promptEvaluationDatasetVersionRowToResponse(target))
			resp.Summary["新增"]++
		case hasBase && !hasTarget:
			resp.Removed = append(resp.Removed, promptEvaluationDatasetVersionRowToResponse(base))
			resp.Summary["删除"]++
		case promptEvaluationDatasetVersionRowFingerprint(base) != promptEvaluationDatasetVersionRowFingerprint(target):
			resp.Changed = append(resp.Changed, PromptEvaluationDatasetVersionChangedRow{
				RowIndex: index,
				Base:     promptEvaluationDatasetVersionRowToResponse(base),
				Target:   promptEvaluationDatasetVersionRowToResponse(target),
			})
			resp.Summary["变更"]++
		default:
			resp.Unchanged = append(resp.Unchanged, promptEvaluationDatasetVersionRowToResponse(target))
			resp.Summary["未变更"]++
		}
	}
	return resp
}

func promptEvaluationPayloadCasesFromDatasetVersionRows(rows []db.PromptEvaluationDatasetVersionRow) []map[string]any {
	cases := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		cases = append(cases, map[string]any{
			"name":              row.RowName,
			"case_name":         row.RowName,
			"名称":                row.RowName,
			"variables":         decodeJSONDefault(row.Variables, map[string]any{}),
			"变量":                decodeJSONDefault(row.Variables, map[string]any{}),
			"expected_contains": decodeJSONDefault(row.ExpectedContains, []any{}),
			"期望包含":              decodeJSONDefault(row.ExpectedContains, []any{}),
			"expected":          decodeJSONDefault(row.Expected, map[string]any{}),
			"期望":                decodeJSONDefault(row.Expected, map[string]any{}),
			"tags":              decodeJSONDefault(row.Tags, []any{}),
			"标签":                decodeJSONDefault(row.Tags, []any{}),
		})
	}
	return cases
}

func promptEvaluationDatasetVersionRestoreMetadata(version db.PromptEvaluationDatasetVersion, requestMetadata []byte) []byte {
	metadata := map[string]any{
		"来源":        "数据集版本恢复",
		"恢复来源版本":    version.Version,
		"恢复来源版本标识":  uuidToString(version.ID),
		"恢复来源版本名称":  version.VersionLabel,
		"恢复来源版本行指纹": version.RowFingerprint,
		"恢复时间":      time.Now().Format(time.RFC3339),
	}
	if len(requestMetadata) > 0 {
		var extra map[string]any
		if err := json.Unmarshal(requestMetadata, &extra); err == nil {
			for key, value := range extra {
				metadata[key] = value
			}
		}
	}
	return mustJSONBytes(metadata)
}

func promptEvaluationDatasetVersionSummary(version db.PromptEvaluationDatasetVersion) map[string]any {
	return map[string]any{
		"dataset_version_id": uuidToString(version.ID),
		"version":            version.Version,
		"version_label":      version.VersionLabel,
		"row_count":          version.RowCount,
		"row_fingerprint":    version.RowFingerprint,
		"created_at":         timestampToString(version.CreatedAt),
	}
}

func (h *Handler) promptEvaluationDatasetVersionBindings(w http.ResponseWriter, r *http.Request, workspaceID pgtype.UUID, payload map[string]any) ([]map[string]any, bool) {
	datasetIDs := promptEvaluationLinkedDatasetIDs(payload)
	explicit := promptEvaluationExplicitDatasetVersionRefs(payload)
	if len(datasetIDs) == 0 && len(explicit) == 0 {
		return nil, true
	}
	bindings := make([]map[string]any, 0, len(datasetIDs)+len(explicit))
	seenVersions := map[string]bool{}
	explicitDatasets := map[string]bool{}
	for _, ref := range explicit {
		datasetID, err := util.ParseUUID(ref.DatasetAssetID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "linked dataset version has invalid dataset_asset_id")
			return nil, false
		}
		versionID, err := util.ParseUUID(ref.DatasetVersionID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "linked dataset version has invalid dataset_version_id")
			return nil, false
		}
		datasetKey := uuidToString(datasetID)
		versionKey := uuidToString(versionID)
		explicitDatasets[datasetKey] = true
		if seenVersions[datasetKey+"."+versionKey] {
			continue
		}
		version, err := h.Queries.GetPromptEvaluationDatasetVersionInAsset(r.Context(), db.GetPromptEvaluationDatasetVersionInAssetParams{
			WorkspaceID:    workspaceID,
			DatasetAssetID: datasetID,
			ID:             versionID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusBadRequest, "linked dataset version does not belong to this workspace dataset")
				return nil, false
			}
			writeError(w, http.StatusInternalServerError, "failed to load linked dataset version")
			return nil, false
		}
		summary := promptEvaluationDatasetVersionSummary(version)
		summary["dataset_asset_id"] = datasetKey
		summary["绑定方式"] = "资产声明的明确数据集版本"
		if strings.TrimSpace(ref.DatasetName) != "" {
			summary["dataset_name"] = strings.TrimSpace(ref.DatasetName)
		}
		bindings = append(bindings, summary)
		seenVersions[datasetKey+"."+versionKey] = true
	}
	seen := map[string]bool{}
	for _, rawID := range datasetIDs {
		datasetID, err := util.ParseUUID(rawID)
		if err != nil {
			continue
		}
		key := uuidToString(datasetID)
		if key == "" || seen[key] || explicitDatasets[key] {
			continue
		}
		seen[key] = true
		version, err := h.Queries.GetLatestPromptEvaluationDatasetVersion(r.Context(), db.GetLatestPromptEvaluationDatasetVersionParams{
			WorkspaceID:    workspaceID,
			DatasetAssetID: datasetID,
		})
		if err != nil {
			continue
		}
		summary := promptEvaluationDatasetVersionSummary(version)
		summary["dataset_asset_id"] = key
		summary["绑定方式"] = "运行开始时读取最新数据集版本"
		bindings = append(bindings, summary)
	}
	return bindings, true
}

type promptEvaluationDatasetVersionRef struct {
	DatasetAssetID   string
	DatasetVersionID string
	DatasetName      string
}

func promptEvaluationExplicitDatasetVersionRefs(payload map[string]any) []promptEvaluationDatasetVersionRef {
	raw := firstValue(payload, "linked_dataset_versions", "数据集版本", "关联数据集版本")
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	result := make([]promptEvaluationDatasetVersionRef, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		versionID := strings.TrimSpace(stringFromAny(firstValue(m, "dataset_version_id", "version_id", "数据集版本ID")))
		if versionID == "" {
			continue
		}
		datasetID := strings.TrimSpace(stringFromAny(firstValue(m, "dataset_id", "dataset_asset_id", "数据集ID")))
		if datasetID == "" {
			result = append(result, promptEvaluationDatasetVersionRef{DatasetVersionID: versionID})
			continue
		}
		result = append(result, promptEvaluationDatasetVersionRef{
			DatasetAssetID:   datasetID,
			DatasetVersionID: versionID,
			DatasetName:      strings.TrimSpace(stringFromAny(firstValue(m, "dataset_name", "数据集名称", "name", "名称"))),
		})
	}
	return result
}

func promptEvaluationLinkedDatasetIDs(payload map[string]any) []string {
	values := []any{
		firstValue(payload, "linked_dataset_ids", "dataset_ids", "数据集ID", "关联数据集ID"),
	}
	if nested, ok := firstValue(payload, "linked_dataset_versions", "数据集版本", "关联数据集版本").([]any); ok {
		for _, item := range nested {
			if m, ok := item.(map[string]any); ok {
				values = append(values, firstValue(m, "dataset_id", "dataset_asset_id", "数据集ID"))
			}
		}
	}
	result := []string{}
	for _, value := range values {
		switch v := value.(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				result = append(result, strings.TrimSpace(v))
			}
		case []any:
			for _, item := range v {
				if s := strings.TrimSpace(stringFromAny(item)); s != "" {
					result = append(result, s)
				}
			}
		case []string:
			for _, item := range v {
				if strings.TrimSpace(item) != "" {
					result = append(result, strings.TrimSpace(item))
				}
			}
		}
	}
	return result
}

func firstValue(m map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := m[key]; ok {
			return value
		}
	}
	return nil
}

func asMap(value any) map[string]any {
	if m, ok := value.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func stringFromAny(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case int:
		return strconv.Itoa(v)
	case int32:
		return strconv.Itoa(int(v))
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

func firstNonEmptyPromptEvaluationString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func firstNonEmptyPromptEvaluationField(row map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(stringFromAny(row[key])); value != "" {
			return value
		}
	}
	return ""
}

func stringMapFromAny(value any) map[string]string {
	result := map[string]string{}
	if m, ok := value.(map[string]any); ok {
		for key, raw := range m {
			result[key] = stringFromAny(raw)
		}
	}
	return result
}

func stringListFromAny(value any) []string {
	switch v := value.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s := strings.TrimSpace(stringFromAny(item)); s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func sortedBoolKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
