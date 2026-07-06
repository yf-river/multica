package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	promptLibraryStatusActive   = "启用"
	promptLibraryStatusArchived = "归档"
	defaultPromptLibraryType    = "text"

	promptLibraryVersionSourceCreated      = "手动创建"
	promptLibraryVersionSourceUpdated      = "手动更新"
	promptLibraryVersionSourceOptimization = "优化候选发布"
)

var promptLibraryVariablePattern = regexp.MustCompile(`\{\{\s*([^{}]+?)\s*\}\}`)

type PromptLibraryItemResponse struct {
	ID          string  `json:"id"`
	WorkspaceID string  `json:"workspace_id"`
	ProjectID   *string `json:"project_id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	PromptType  string  `json:"prompt_type"`
	Content     string  `json:"content"`
	Variables   any     `json:"variables"`
	Tags        any     `json:"tags"`
	Status      string  `json:"status"`
	Version     int32   `json:"version"`
	CreatedBy   *string `json:"created_by"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

type PromptLibraryVersionResponse struct {
	ID                string  `json:"id"`
	PromptID          string  `json:"prompt_id"`
	WorkspaceID       string  `json:"workspace_id"`
	ProjectID         *string `json:"project_id"`
	Version           int32   `json:"version"`
	Name              string  `json:"name"`
	Description       string  `json:"description"`
	PromptType        string  `json:"prompt_type"`
	Content           string  `json:"content"`
	Variables         any     `json:"variables"`
	Tags              any     `json:"tags"`
	Source            string  `json:"source"`
	SourceCandidateID *string `json:"source_candidate_id"`
	ChangeNote        string  `json:"change_note"`
	CreatedBy         *string `json:"created_by"`
	CreatedAt         string  `json:"created_at"`
}

type PromptLibraryTrialResponse struct {
	ID              string         `json:"id"`
	WorkspaceID     string         `json:"workspace_id"`
	PromptID        string         `json:"prompt_id"`
	VersionID       string         `json:"version_id"`
	AgentID         string         `json:"agent_id"`
	ChatSessionID   *string        `json:"chat_session_id"`
	TaskID          *string        `json:"task_id"`
	Input           string         `json:"input"`
	RenderedMessage string         `json:"rendered_message"`
	Variables       map[string]any `json:"variables"`
	Status          string         `json:"status"`
	OutputPreview   string         `json:"output_preview"`
	CreatedBy       *string        `json:"created_by"`
	CreatedAt       string         `json:"created_at"`
	UpdatedAt       string         `json:"updated_at"`
}

type CreatePromptLibraryItemRequest struct {
	ProjectID   json.RawMessage `json:"project_id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	PromptType  string          `json:"prompt_type"`
	Content     string          `json:"content"`
	Variables   json.RawMessage `json:"variables"`
	Tags        json.RawMessage `json:"tags"`
	Status      string          `json:"status"`
}

type CreatePromptLibraryVersionRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Content     string  `json:"content"`
	ChangeNote  string  `json:"change_note"`
}

type UpdatePromptLibraryItemRequest struct {
	ProjectID   json.RawMessage `json:"project_id"`
	Name        *string         `json:"name"`
	Description *string         `json:"description"`
	PromptType  *string         `json:"prompt_type"`
	Content     *string         `json:"content"`
	Variables   json.RawMessage `json:"variables"`
	Tags        json.RawMessage `json:"tags"`
	Status      *string         `json:"status"`
}

type CreatePromptLibraryTrialRequest struct {
	AgentID   string            `json:"agent_id"`
	Input     string            `json:"input"`
	Variables map[string]string `json:"variables"`
}

func promptLibraryItemToResponse(item db.PromptLibraryItem) PromptLibraryItemResponse {
	return PromptLibraryItemResponse{
		ID:          uuidToString(item.ID),
		WorkspaceID: uuidToString(item.WorkspaceID),
		ProjectID:   uuidToPtr(item.ProjectID),
		Name:        item.Name,
		Description: item.Description,
		PromptType:  item.PromptType,
		Content:     item.Content,
		Variables:   decodeJSONDefault(item.Variables, []any{}),
		Tags:        decodeJSONDefault(item.Tags, []any{}),
		Status:      item.Status,
		Version:     item.Version,
		CreatedBy:   uuidToPtr(item.CreatedBy),
		CreatedAt:   timestampToString(item.CreatedAt),
		UpdatedAt:   timestampToString(item.UpdatedAt),
	}
}

func promptLibraryVersionToResponse(version db.PromptLibraryVersion) PromptLibraryVersionResponse {
	return PromptLibraryVersionResponse{
		ID:                uuidToString(version.ID),
		PromptID:          uuidToString(version.PromptID),
		WorkspaceID:       uuidToString(version.WorkspaceID),
		ProjectID:         uuidToPtr(version.ProjectID),
		Version:           version.Version,
		Name:              version.Name,
		Description:       version.Description,
		PromptType:        version.PromptType,
		Content:           version.Content,
		Variables:         decodeJSONDefault(version.Variables, []any{}),
		Tags:              decodeJSONDefault(version.Tags, []any{}),
		Source:            version.Source,
		SourceCandidateID: uuidToPtr(version.SourceCandidateID),
		ChangeNote:        version.ChangeNote,
		CreatedBy:         uuidToPtr(version.CreatedBy),
		CreatedAt:         timestampToString(version.CreatedAt),
	}
}

func createPromptLibraryVersion(ctx context.Context, q *db.Queries, item db.PromptLibraryItem, source string, sourceCandidateID pgtype.UUID, changeNote string) (db.PromptLibraryVersion, error) {
	return q.CreatePromptLibraryVersion(ctx, db.CreatePromptLibraryVersionParams{
		PromptID:          item.ID,
		WorkspaceID:       item.WorkspaceID,
		Version:           item.Version,
		Name:              item.Name,
		Description:       item.Description,
		PromptType:        item.PromptType,
		Content:           item.Content,
		Source:            source,
		CreatedBy:         item.CreatedBy,
		ProjectID:         item.ProjectID,
		Variables:         item.Variables,
		Tags:              item.Tags,
		SourceCandidateID: sourceCandidateID,
		ChangeNote:        changeNote,
	})
}

func promptLibraryTrialToResponse(trial db.PromptLibraryTrial) PromptLibraryTrialResponse {
	variables, _ := decodeJSONDefault(trial.Variables, map[string]any{}).(map[string]any)
	if variables == nil {
		variables = map[string]any{}
	}
	return PromptLibraryTrialResponse{
		ID:              uuidToString(trial.ID),
		WorkspaceID:     uuidToString(trial.WorkspaceID),
		PromptID:        uuidToString(trial.PromptID),
		VersionID:       uuidToString(trial.VersionID),
		AgentID:         uuidToString(trial.AgentID),
		ChatSessionID:   uuidToPtr(trial.ChatSessionID),
		TaskID:          uuidToPtr(trial.TaskID),
		Input:           trial.Input,
		RenderedMessage: trial.RenderedMessage,
		Variables:       variables,
		Status:          trial.Status,
		OutputPreview:   trial.OutputPreview,
		CreatedBy:       uuidToPtr(trial.CreatedBy),
		CreatedAt:       timestampToString(trial.CreatedAt),
		UpdatedAt:       timestampToString(trial.UpdatedAt),
	}
}

func promptLibraryTrialRowToResponse(trial db.ListPromptLibraryTrialsRow) PromptLibraryTrialResponse {
	variables, _ := decodeJSONDefault(trial.Variables, map[string]any{}).(map[string]any)
	if variables == nil {
		variables = map[string]any{}
	}
	return PromptLibraryTrialResponse{
		ID:              uuidToString(trial.ID),
		WorkspaceID:     uuidToString(trial.WorkspaceID),
		PromptID:        uuidToString(trial.PromptID),
		VersionID:       uuidToString(trial.VersionID),
		AgentID:         uuidToString(trial.AgentID),
		ChatSessionID:   uuidToPtr(trial.ChatSessionID),
		TaskID:          uuidToPtr(trial.TaskID),
		Input:           trial.Input,
		RenderedMessage: trial.RenderedMessage,
		Variables:       variables,
		Status:          trial.Status,
		OutputPreview:   trial.OutputPreview,
		CreatedBy:       uuidToPtr(trial.CreatedBy),
		CreatedAt:       timestampToString(trial.CreatedAt),
		UpdatedAt:       timestampToString(trial.UpdatedAt),
	}
}

func decodeJSONDefault(raw []byte, fallback any) any {
	if len(raw) == 0 {
		return fallback
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return fallback
	}
	return value
}

func normalizePromptLibraryStatus(status string) string {
	if status == "" {
		return promptLibraryStatusActive
	}
	return status
}

func validPromptLibraryStatus(status string) bool {
	return status == promptLibraryStatusActive || status == promptLibraryStatusArchived
}

func jsonArrayField(w http.ResponseWriter, raw json.RawMessage, field string) ([]byte, bool) {
	if len(raw) == 0 {
		return nil, true
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, true
	}
	var arr []any
	if err := json.Unmarshal(raw, &arr); err != nil {
		writeError(w, http.StatusBadRequest, field+" must be a JSON array")
		return nil, false
	}
	return raw, true
}

func textParam(value *string) pgtype.Text {
	if value == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *value, Valid: true}
}

func (h *Handler) promptLibraryProjectID(w http.ResponseWriter, r *http.Request, workspaceID pgtype.UUID, raw json.RawMessage, fallback pgtype.UUID) (pgtype.UUID, bool) {
	if len(raw) == 0 {
		return fallback, true
	}
	trimmed := bytes.TrimSpace(raw)
	if bytes.Equal(trimmed, []byte("null")) {
		return pgtype.UUID{}, true
	}
	var projectID string
	if err := json.Unmarshal(raw, &projectID); err != nil {
		writeError(w, http.StatusBadRequest, "project_id must be a string or null")
		return pgtype.UUID{}, false
	}
	if projectID == "" {
		return pgtype.UUID{}, true
	}
	projectUUID, ok := parseUUIDOrBadRequest(w, projectID, "project_id")
	if !ok {
		return pgtype.UUID{}, false
	}
	if _, err := h.Queries.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{ID: projectUUID, WorkspaceID: workspaceID}); err != nil {
		writeError(w, http.StatusBadRequest, "project_id does not belong to this workspace")
		return pgtype.UUID{}, false
	}
	return projectUUID, true
}

func (h *Handler) ListPromptLibraryItems(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	var projectID pgtype.UUID
	if rawProjectID := r.URL.Query().Get("project_id"); rawProjectID != "" {
		parsed, ok := parseUUIDOrBadRequest(w, rawProjectID, "project_id")
		if !ok {
			return
		}
		projectID = parsed
	}
	var promptType pgtype.Text
	if value := r.URL.Query().Get("prompt_type"); value != "" {
		promptType = pgtype.Text{String: value, Valid: true}
	}
	var status pgtype.Text
	if value := r.URL.Query().Get("status"); value != "" {
		if !validPromptLibraryStatus(value) {
			writeError(w, http.StatusBadRequest, "status must be 启用 or 归档")
			return
		}
		status = pgtype.Text{String: value, Valid: true}
	}

	items, err := h.Queries.ListPromptLibraryItems(r.Context(), db.ListPromptLibraryItemsParams{
		WorkspaceID: workspaceUUID,
		ProjectID:   projectID,
		PromptType:  promptType,
		Status:      status,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list prompt library items")
		return
	}
	resp := make([]PromptLibraryItemResponse, len(items))
	for i, item := range items {
		resp[i] = promptLibraryItemToResponse(item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": resp, "total": len(resp)})
}

func (h *Handler) GetPromptLibraryItem(w http.ResponseWriter, r *http.Request) {
	item, ok := h.loadPromptLibraryItem(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, promptLibraryItemToResponse(item))
}

func (h *Handler) ListPromptLibraryVersions(w http.ResponseWriter, r *http.Request) {
	item, ok := h.loadPromptLibraryItem(w, r)
	if !ok {
		return
	}
	versions, err := h.Queries.ListPromptLibraryVersions(r.Context(), db.ListPromptLibraryVersionsParams{
		WorkspaceID: item.WorkspaceID,
		PromptID:    item.ID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list prompt library versions")
		return
	}
	resp := make([]PromptLibraryVersionResponse, len(versions))
	for i, version := range versions {
		resp[i] = promptLibraryVersionToResponse(version)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": resp, "total": len(resp)})
}

func (h *Handler) CreatePromptLibraryItem(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}

	var req CreatePromptLibraryItemRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.Content == "" {
		writeError(w, http.StatusBadRequest, "content is required")
		return
	}
	promptType := req.PromptType
	if promptType == "" {
		promptType = defaultPromptLibraryType
	}
	status := normalizePromptLibraryStatus(req.Status)
	if !validPromptLibraryStatus(status) {
		writeError(w, http.StatusBadRequest, "status must be 启用 or 归档")
		return
	}
	projectID, ok := h.promptLibraryProjectID(w, r, workspaceUUID, req.ProjectID, pgtype.UUID{})
	if !ok {
		return
	}
	variables, ok := jsonArrayField(w, req.Variables, "variables")
	if !ok {
		return
	}
	tags, ok := jsonArrayField(w, req.Tags, "tags")
	if !ok {
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start prompt library transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	item, err := qtx.CreatePromptLibraryItem(r.Context(), db.CreatePromptLibraryItemParams{
		WorkspaceID: workspaceUUID,
		Name:        req.Name,
		Description: req.Description,
		PromptType:  promptType,
		Content:     req.Content,
		CreatedBy:   parseUUID(userID),
		ProjectID:   projectID,
		Variables:   variables,
		Tags:        tags,
		Status:      status,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "a prompt with this name already exists")
			return
		}
		if isCheckViolation(err) {
			writeError(w, http.StatusBadRequest, "prompt library item rejected: a field value failed a database constraint")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create prompt library item")
		return
	}
	if _, err := createPromptLibraryVersion(r.Context(), qtx, item, promptLibraryVersionSourceCreated, pgtype.UUID{}, "初始版本"); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create prompt library version")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit prompt library item")
		return
	}
	writeJSON(w, http.StatusCreated, promptLibraryItemToResponse(item))
}

func (h *Handler) UpdatePromptLibraryItem(w http.ResponseWriter, r *http.Request) {
	existing, ok := h.loadPromptLibraryItem(w, r)
	if !ok {
		return
	}
	var req UpdatePromptLibraryItemRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name != nil && *req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.Content != nil && *req.Content == "" {
		writeError(w, http.StatusBadRequest, "content is required")
		return
	}
	if req.Status != nil && !validPromptLibraryStatus(*req.Status) {
		writeError(w, http.StatusBadRequest, "status must be 启用 or 归档")
		return
	}
	projectID, ok := h.promptLibraryProjectID(w, r, existing.WorkspaceID, req.ProjectID, existing.ProjectID)
	if !ok {
		return
	}
	variables, ok := jsonArrayField(w, req.Variables, "variables")
	if !ok {
		return
	}
	tags, ok := jsonArrayField(w, req.Tags, "tags")
	if !ok {
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start prompt library transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	item, err := qtx.UpdatePromptLibraryItem(r.Context(), db.UpdatePromptLibraryItemParams{
		ID:          existing.ID,
		WorkspaceID: existing.WorkspaceID,
		ProjectID:   projectID,
		Name:        textParam(req.Name),
		Description: textParam(req.Description),
		PromptType:  textParam(req.PromptType),
		Content:     textParam(req.Content),
		Variables:   variables,
		Tags:        tags,
		Status:      textParam(req.Status),
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "a prompt with this name already exists")
			return
		}
		if isCheckViolation(err) {
			writeError(w, http.StatusBadRequest, "prompt library item rejected: a field value failed a database constraint")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update prompt library item")
		return
	}
	if _, err := createPromptLibraryVersion(r.Context(), qtx, item, promptLibraryVersionSourceUpdated, pgtype.UUID{}, ""); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create prompt library version")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit prompt library item update")
		return
	}
	writeJSON(w, http.StatusOK, promptLibraryItemToResponse(item))
}

func (h *Handler) CreatePromptLibraryVersion(w http.ResponseWriter, r *http.Request) {
	existing, ok := h.loadPromptLibraryItem(w, r)
	if !ok {
		return
	}
	var req CreatePromptLibraryVersionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Content) == "" {
		writeError(w, http.StatusBadRequest, "content is required")
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start prompt library transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	item, err := qtx.UpdatePromptLibraryItemLatestVersion(r.Context(), db.UpdatePromptLibraryItemLatestVersionParams{
		ID:          existing.ID,
		WorkspaceID: existing.WorkspaceID,
		Content:     req.Content,
		Name:        textParam(req.Name),
		Description: textParam(req.Description),
		PromptType:  pgtype.Text{String: "text", Valid: true},
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "a prompt with this name already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update prompt library item")
		return
	}
	version, err := createPromptLibraryVersion(r.Context(), qtx, item, promptLibraryVersionSourceUpdated, pgtype.UUID{}, strings.TrimSpace(req.ChangeNote))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create prompt library version")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit prompt library version")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"item":    promptLibraryItemToResponse(item),
		"version": promptLibraryVersionToResponse(version),
	})
}

func (h *Handler) DeletePromptLibraryItem(w http.ResponseWriter, r *http.Request) {
	item, ok := h.loadPromptLibraryItem(w, r)
	if !ok {
		return
	}
	if err := h.Queries.DeletePromptLibraryItem(r.Context(), db.DeletePromptLibraryItemParams{ID: item.ID, WorkspaceID: item.WorkspaceID}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete prompt library item")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func renderPromptLibraryTrialMessage(content string, input string, variables map[string]string) string {
	renderedPrompt := promptLibraryVariablePattern.ReplaceAllStringFunc(content, func(match string) string {
		parts := promptLibraryVariablePattern.FindStringSubmatch(match)
		if len(parts) < 2 {
			return match
		}
		name := strings.TrimSpace(parts[1])
		if value, ok := variables[name]; ok {
			return value
		}
		return match
	})
	input = strings.TrimSpace(input)
	if input == "" {
		return fmt.Sprintf("请严格按照下面的提示词执行。\n\n<提示词版本>\n%s\n</提示词版本>", renderedPrompt)
	}
	return fmt.Sprintf("请严格按照下面的提示词执行。\n\n<提示词版本>\n%s\n</提示词版本>\n\n<用户输入>\n%s\n</用户输入>", renderedPrompt, input)
}

func missingPromptLibraryTrialVariables(content string, variables map[string]string) []string {
	seen := make(map[string]bool)
	missing := make([]string, 0)
	for _, parts := range promptLibraryVariablePattern.FindAllStringSubmatch(content, -1) {
		if len(parts) < 2 {
			continue
		}
		name := strings.TrimSpace(parts[1])
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		if strings.TrimSpace(variables[name]) == "" {
			missing = append(missing, name)
		}
	}
	return missing
}

func (h *Handler) ListPromptLibraryTrials(w http.ResponseWriter, r *http.Request) {
	item, ok := h.loadPromptLibraryItem(w, r)
	if !ok {
		return
	}
	trials, err := h.Queries.ListPromptLibraryTrials(r.Context(), db.ListPromptLibraryTrialsParams{
		WorkspaceID: item.WorkspaceID,
		PromptID:    item.ID,
		Limit:       20,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list prompt library trials")
		return
	}
	resp := make([]PromptLibraryTrialResponse, len(trials))
	for i, trial := range trials {
		resp[i] = promptLibraryTrialRowToResponse(trial)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": resp, "total": len(resp)})
}

func (h *Handler) CreatePromptLibraryTrial(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	item, ok := h.loadPromptLibraryItem(w, r)
	if !ok {
		return
	}
	versionID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "versionId"), "prompt version id")
	if !ok {
		return
	}
	version, err := h.Queries.GetPromptLibraryVersionForPrompt(r.Context(), db.GetPromptLibraryVersionForPromptParams{
		ID:          versionID,
		WorkspaceID: item.WorkspaceID,
		PromptID:    item.ID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "prompt version not found")
		return
	}

	var req CreatePromptLibraryTrialRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.AgentID == "" {
		writeError(w, http.StatusBadRequest, "agent_id is required")
		return
	}
	if missingVariables := missingPromptLibraryTrialVariables(version.Content, req.Variables); len(missingVariables) > 0 {
		writeError(w, http.StatusBadRequest, "missing prompt variables: "+strings.Join(missingVariables, ", "))
		return
	}
	agentID, ok := parseUUIDOrBadRequest(w, req.AgentID, "agent_id")
	if !ok {
		return
	}
	agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID:          agentID,
		WorkspaceID: item.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	if agent.ArchivedAt.Valid {
		writeError(w, http.StatusBadRequest, "agent is archived")
		return
	}
	workspaceID := uuidToString(item.WorkspaceID)
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	if !h.canAccessPersonalAgent(r.Context(), agent, actorType, actorID, workspaceID) {
		writeError(w, http.StatusForbidden, "you do not have access to this agent")
		return
	}

	renderedMessage := renderPromptLibraryTrialMessage(version.Content, strings.TrimSpace(req.Input), req.Variables)
	session, err := h.Queries.CreateChatSession(r.Context(), db.CreateChatSessionParams{
		WorkspaceID: item.WorkspaceID,
		AgentID:     agent.ID,
		CreatorID:   parseUUID(userID),
		Title:       fmt.Sprintf("提示词试跑 · %s v%d", item.Name, version.Version),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create prompt trial chat session")
		return
	}
	msg, err := h.Queries.CreateChatMessage(r.Context(), db.CreateChatMessageParams{
		ChatSessionID: session.ID,
		Role:          "user",
		Content:       renderedMessage,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create prompt trial message")
		return
	}
	task, err := h.TaskService.EnqueueChatTask(r.Context(), session, parseUUID(userID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to enqueue prompt trial task: "+err.Error())
		return
	}
	if err := h.Queries.LinkChatMessageToTask(r.Context(), db.LinkChatMessageToTaskParams{ID: msg.ID, TaskID: task.ID}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to link prompt trial message")
		return
	}
	variablesJSON, _ := json.Marshal(req.Variables)
	if len(variablesJSON) == 0 || string(variablesJSON) == "null" {
		variablesJSON = []byte(`{}`)
	}
	trial, err := h.Queries.CreatePromptLibraryTrial(r.Context(), db.CreatePromptLibraryTrialParams{
		WorkspaceID:     item.WorkspaceID,
		PromptID:        item.ID,
		VersionID:       version.ID,
		AgentID:         agent.ID,
		ChatSessionID:   session.ID,
		TaskID:          task.ID,
		Input:           strings.TrimSpace(req.Input),
		RenderedMessage: renderedMessage,
		Variables:       variablesJSON,
		Status:          task.Status,
		CreatedBy:       parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create prompt library trial")
		return
	}
	writeJSON(w, http.StatusAccepted, promptLibraryTrialToResponse(trial))
}

func (h *Handler) loadPromptLibraryItem(w http.ResponseWriter, r *http.Request) (db.PromptLibraryItem, bool) {
	workspaceID := h.resolveWorkspaceID(r)
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return db.PromptLibraryItem{}, false
	}
	itemID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "id"), "prompt id")
	if !ok {
		return db.PromptLibraryItem{}, false
	}
	item, err := h.Queries.GetPromptLibraryItemInWorkspace(r.Context(), db.GetPromptLibraryItemInWorkspaceParams{
		ID:          itemID,
		WorkspaceID: workspaceUUID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "prompt library item not found")
			return db.PromptLibraryItem{}, false
		}
		writeError(w, http.StatusInternalServerError, "failed to load prompt library item")
		return db.PromptLibraryItem{}, false
	}
	return item, true
}
