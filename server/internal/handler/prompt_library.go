package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const (
	promptLibraryStatusActive   = "启用"
	promptLibraryStatusArchived = "归档"
	defaultPromptLibraryType    = "通用"
)

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

	item, err := h.Queries.CreatePromptLibraryItem(r.Context(), db.CreatePromptLibraryItemParams{
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

	item, err := h.Queries.UpdatePromptLibraryItem(r.Context(), db.UpdatePromptLibraryItemParams{
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
	writeJSON(w, http.StatusOK, promptLibraryItemToResponse(item))
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
