package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	promptEvaluationAssetDataset    = "数据集"
	promptEvaluationAssetTestSuite  = "测试套件"
	promptEvaluationAssetExperiment = "实验"
	promptEvaluationAssetOptimize   = "优化运行"
)

var promptTemplateVariablePattern = regexp.MustCompile(`\{\{\s*([a-zA-Z0-9_.-]+)\s*\}\}`)

type PromptEvaluationAssetResponse struct {
	ID          string  `json:"id"`
	WorkspaceID string  `json:"workspace_id"`
	PromptID    *string `json:"prompt_id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	AssetType   string  `json:"asset_type"`
	Payload     any     `json:"payload"`
	Status      string  `json:"status"`
	CreatedBy   *string `json:"created_by"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

type CreatePromptEvaluationAssetRequest struct {
	PromptID    json.RawMessage `json:"prompt_id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	AssetType   string          `json:"asset_type"`
	Payload     json.RawMessage `json:"payload"`
	Status      string          `json:"status"`
}

type UpdatePromptEvaluationAssetRequest struct {
	PromptID    json.RawMessage `json:"prompt_id"`
	Name        *string         `json:"name"`
	Description *string         `json:"description"`
	AssetType   *string         `json:"asset_type"`
	Payload     json.RawMessage `json:"payload"`
	Status      *string         `json:"status"`
}

type promptEvaluationRunResult struct {
	RunAt             string                          `json:"运行时间"`
	AssetType         string                          `json:"资产类型"`
	PromptName        string                          `json:"提示词"`
	TotalCases        int                             `json:"总用例数"`
	PassedCases       int                             `json:"通过用例数"`
	FailedCases       int                             `json:"失败用例数"`
	PassRate          float64                         `json:"通过率"`
	TotalDurationMs   int64                           `json:"总耗时毫秒"`
	AverageDurationMs int64                           `json:"平均耗时毫秒"`
	InputTokens       int                             `json:"输入token"`
	OutputTokens      int                             `json:"输出token"`
	EstimatedCost     float64                         `json:"预估成本"`
	AgentName         string                          `json:"执行Agent"`
	Model             string                          `json:"模型"`
	Runtime           string                          `json:"runtime"`
	TraceTaskID       string                          `json:"trace/task id"`
	FailureReason     string                          `json:"失败原因"`
	Conclusion        string                          `json:"评估结论"`
	MissingVarCount   int                             `json:"缺失变量数"`
	CaseResults       []promptEvaluationCaseRunResult `json:"用例结果"`
}

type promptEvaluationCaseRunResult struct {
	Name             string            `json:"名称"`
	Status           string            `json:"状态"`
	Variables        map[string]string `json:"变量"`
	RenderedPrompt   string            `json:"渲染提示词"`
	UsedVariables    []string          `json:"使用变量"`
	MissingVariables []string          `json:"缺失变量"`
	ExpectedContains []string          `json:"期望包含"`
	MatchedContains  []string          `json:"已匹配"`
}

func promptEvaluationAssetToResponse(asset db.PromptEvaluationAsset) PromptEvaluationAssetResponse {
	return PromptEvaluationAssetResponse{
		ID:          uuidToString(asset.ID),
		WorkspaceID: uuidToString(asset.WorkspaceID),
		PromptID:    uuidToPtr(asset.PromptID),
		Name:        asset.Name,
		Description: asset.Description,
		AssetType:   asset.AssetType,
		Payload:     decodeJSONDefault(asset.Payload, map[string]any{}),
		Status:      asset.Status,
		CreatedBy:   uuidToPtr(asset.CreatedBy),
		CreatedAt:   timestampToString(asset.CreatedAt),
		UpdatedAt:   timestampToString(asset.UpdatedAt),
	}
}

func validPromptEvaluationAssetType(assetType string) bool {
	return assetType == promptEvaluationAssetDataset ||
		assetType == promptEvaluationAssetTestSuite ||
		assetType == promptEvaluationAssetExperiment ||
		assetType == promptEvaluationAssetOptimize
}

func jsonObjectField(w http.ResponseWriter, raw json.RawMessage, field string) ([]byte, bool) {
	if len(raw) == 0 {
		return nil, true
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, true
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		writeError(w, http.StatusBadRequest, field+" must be a JSON object")
		return nil, false
	}
	return raw, true
}

func (h *Handler) promptEvaluationPromptID(w http.ResponseWriter, r *http.Request, workspaceID pgtype.UUID, raw json.RawMessage, fallback pgtype.UUID) (pgtype.UUID, bool) {
	if len(raw) == 0 {
		return fallback, true
	}
	trimmed := bytes.TrimSpace(raw)
	if bytes.Equal(trimmed, []byte("null")) {
		return pgtype.UUID{}, true
	}
	var promptID string
	if err := json.Unmarshal(raw, &promptID); err != nil {
		writeError(w, http.StatusBadRequest, "prompt_id must be a string or null")
		return pgtype.UUID{}, false
	}
	if promptID == "" {
		return pgtype.UUID{}, true
	}
	promptUUID, ok := parseUUIDOrBadRequest(w, promptID, "prompt_id")
	if !ok {
		return pgtype.UUID{}, false
	}
	if _, err := h.Queries.GetPromptLibraryItemInWorkspace(r.Context(), db.GetPromptLibraryItemInWorkspaceParams{ID: promptUUID, WorkspaceID: workspaceID}); err != nil {
		writeError(w, http.StatusBadRequest, "prompt_id does not belong to this workspace")
		return pgtype.UUID{}, false
	}
	return promptUUID, true
}

func (h *Handler) ListPromptEvaluationAssets(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	var assetType pgtype.Text
	if value := r.URL.Query().Get("asset_type"); value != "" {
		if !validPromptEvaluationAssetType(value) {
			writeError(w, http.StatusBadRequest, "asset_type must be 数据集, 测试套件, 实验 or 优化运行")
			return
		}
		assetType = pgtype.Text{String: value, Valid: true}
	}
	var status pgtype.Text
	if value := r.URL.Query().Get("status"); value != "" {
		if !validPromptLibraryStatus(value) {
			writeError(w, http.StatusBadRequest, "status must be 启用 or 归档")
			return
		}
		status = pgtype.Text{String: value, Valid: true}
	}
	var promptID pgtype.UUID
	if value := r.URL.Query().Get("prompt_id"); value != "" {
		parsed, ok := parseUUIDOrBadRequest(w, value, "prompt_id")
		if !ok {
			return
		}
		promptID = parsed
	}
	assets, err := h.Queries.ListPromptEvaluationAssets(r.Context(), db.ListPromptEvaluationAssetsParams{
		WorkspaceID: workspaceUUID,
		AssetType:   assetType,
		Status:      status,
		PromptID:    promptID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list prompt evaluation assets")
		return
	}
	resp := make([]PromptEvaluationAssetResponse, len(assets))
	for i, asset := range assets {
		resp[i] = promptEvaluationAssetToResponse(asset)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": resp, "total": len(resp)})
}

func (h *Handler) GetPromptEvaluationAsset(w http.ResponseWriter, r *http.Request) {
	asset, ok := h.loadPromptEvaluationAsset(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, promptEvaluationAssetToResponse(asset))
}

func (h *Handler) CreatePromptEvaluationAsset(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	var req CreatePromptEvaluationAssetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if !validPromptEvaluationAssetType(req.AssetType) {
		writeError(w, http.StatusBadRequest, "asset_type must be 数据集, 测试套件, 实验 or 优化运行")
		return
	}
	status := normalizePromptLibraryStatus(req.Status)
	if !validPromptLibraryStatus(status) {
		writeError(w, http.StatusBadRequest, "status must be 启用 or 归档")
		return
	}
	promptID, ok := h.promptEvaluationPromptID(w, r, workspaceUUID, req.PromptID, pgtype.UUID{})
	if !ok {
		return
	}
	payload, ok := jsonObjectField(w, req.Payload, "payload")
	if !ok {
		return
	}
	asset, err := h.Queries.CreatePromptEvaluationAsset(r.Context(), db.CreatePromptEvaluationAssetParams{
		WorkspaceID: workspaceUUID,
		Name:        req.Name,
		Description: req.Description,
		AssetType:   req.AssetType,
		CreatedBy:   parseUUID(userID),
		PromptID:    promptID,
		Payload:     payload,
		Status:      status,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "an evaluation asset with this type and name already exists")
			return
		}
		if isCheckViolation(err) {
			writeError(w, http.StatusBadRequest, "prompt evaluation asset rejected: a field value failed a database constraint")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create prompt evaluation asset")
		return
	}
	writeJSON(w, http.StatusCreated, promptEvaluationAssetToResponse(asset))
}

func (h *Handler) UpdatePromptEvaluationAsset(w http.ResponseWriter, r *http.Request) {
	existing, ok := h.loadPromptEvaluationAsset(w, r)
	if !ok {
		return
	}
	var req UpdatePromptEvaluationAssetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name != nil && *req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.AssetType != nil && !validPromptEvaluationAssetType(*req.AssetType) {
		writeError(w, http.StatusBadRequest, "asset_type must be 数据集, 测试套件, 实验 or 优化运行")
		return
	}
	if req.Status != nil && !validPromptLibraryStatus(*req.Status) {
		writeError(w, http.StatusBadRequest, "status must be 启用 or 归档")
		return
	}
	promptID, ok := h.promptEvaluationPromptID(w, r, existing.WorkspaceID, req.PromptID, existing.PromptID)
	if !ok {
		return
	}
	payload, ok := jsonObjectField(w, req.Payload, "payload")
	if !ok {
		return
	}
	asset, err := h.Queries.UpdatePromptEvaluationAsset(r.Context(), db.UpdatePromptEvaluationAssetParams{
		ID:          existing.ID,
		WorkspaceID: existing.WorkspaceID,
		PromptID:    promptID,
		Name:        textParam(req.Name),
		Description: textParam(req.Description),
		AssetType:   textParam(req.AssetType),
		Payload:     payload,
		Status:      textParam(req.Status),
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "an evaluation asset with this type and name already exists")
			return
		}
		if isCheckViolation(err) {
			writeError(w, http.StatusBadRequest, "prompt evaluation asset rejected: a field value failed a database constraint")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update prompt evaluation asset")
		return
	}
	writeJSON(w, http.StatusOK, promptEvaluationAssetToResponse(asset))
}

func (h *Handler) DeletePromptEvaluationAsset(w http.ResponseWriter, r *http.Request) {
	asset, ok := h.loadPromptEvaluationAsset(w, r)
	if !ok {
		return
	}
	if err := h.Queries.DeletePromptEvaluationAsset(r.Context(), db.DeletePromptEvaluationAssetParams{ID: asset.ID, WorkspaceID: asset.WorkspaceID}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete prompt evaluation asset")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) RunPromptEvaluationAsset(w http.ResponseWriter, r *http.Request) {
	asset, ok := h.loadPromptEvaluationAsset(w, r)
	if !ok {
		return
	}
	if !asset.PromptID.Valid {
		writeError(w, http.StatusBadRequest, "prompt_id is required to run an evaluation asset")
		return
	}
	prompt, err := h.Queries.GetPromptLibraryItemInWorkspace(r.Context(), db.GetPromptLibraryItemInWorkspaceParams{
		ID:          asset.PromptID,
		WorkspaceID: asset.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "prompt_id does not belong to this workspace")
		return
	}
	payload := decodePayloadObject(asset.Payload)
	result := buildPromptEvaluationRunResult(asset, prompt, payload)
	payload["最近运行"] = result
	payload["运行记录"] = appendPromptEvaluationRunHistory(payload["运行记录"], result)
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode prompt evaluation result")
		return
	}
	updated, err := h.Queries.UpdatePromptEvaluationAsset(r.Context(), db.UpdatePromptEvaluationAssetParams{
		ID:          asset.ID,
		WorkspaceID: asset.WorkspaceID,
		PromptID:    asset.PromptID,
		Payload:     payloadBytes,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save prompt evaluation result")
		return
	}
	writeJSON(w, http.StatusOK, promptEvaluationAssetToResponse(updated))
}

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

func buildPromptEvaluationRunResult(asset db.PromptEvaluationAsset, prompt db.PromptLibraryItem, payload map[string]any) promptEvaluationRunResult {
	started := time.Now()
	cases := promptEvaluationCases(payload)
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
	return promptEvaluationRunResult{
		RunAt:             time.Now().UTC().Format(time.RFC3339),
		AssetType:         asset.AssetType,
		PromptName:        prompt.Name,
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
		Model:             "本地模板渲染",
		Runtime:           "server",
		TraceTaskID:       "未创建 Agent 任务",
		FailureReason:     failureReason,
		Conclusion:        conclusion,
		MissingVarCount:   missingCount,
		CaseResults:       results,
	}
}

func promptEvaluationCases(payload map[string]any) []map[string]any {
	raw := firstValue(payload, "cases", "用例", "测试用例", "数据集", "training_cases", "evaluation_cases", "用例集")
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

func firstValue(m map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := m[key]; ok {
			return value
		}
	}
	return nil
}

func stringFromAny(value any) string {
	switch v := value.(type) {
	case string:
		return v
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
