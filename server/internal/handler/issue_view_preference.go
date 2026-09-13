package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// 职责定位：issue 表格视图的个人偏好（隐藏项 + 排序）读写端点。偏好文档归客户端所有，这里
// 除「必须是 JSON 对象」外不做解释；其中条目 id 形如 "builtin:<key>" / "view:<uuid>"。

// resolvePreferenceScope 校验 scope_type 参数并回填 scope_id，让复合键永不带 NULL：workspace
// 作用域用工作区 id、my 作用域用用户 id、project 作用域用（已验证）项目 id。
func (h *Handler) resolvePreferenceScope(
	w http.ResponseWriter, r *http.Request,
	wsUUID pgtype.UUID, userID string, scopeType, rawScopeID string,
) (pgtype.UUID, bool) {
	switch scopeType {
	case "workspace":
		return wsUUID, true
	case "my":
		return parseUUID(userID), true
	case "project":
		if rawScopeID == "" {
			writeError(w, http.StatusBadRequest, "scope_id is required for project scope")
			return pgtype.UUID{}, false
		}
		projUUID, ok := parseUUIDOrBadRequest(w, rawScopeID, "scope_id")
		if !ok {
			return pgtype.UUID{}, false
		}
		if _, err := h.Queries.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{
			ID: projUUID, WorkspaceID: wsUUID,
		}); err != nil {
			writeError(w, http.StatusNotFound, "project not found")
			return pgtype.UUID{}, false
		}
		return projUUID, true
	default:
		writeError(w, http.StatusBadRequest, "invalid scope_type")
		return pgtype.UUID{}, false
	}
}

// IssueViewPreferenceResponse 是偏好读/写响应的统一形态。
type IssueViewPreferenceResponse struct {
	ScopeType string          `json:"scope_type"`
	ScopeID   *string         `json:"scope_id"`
	Prefs     json.RawMessage `json:"prefs"`
	UpdatedAt string          `json:"updated_at"`
}

// GetIssueViewPreference 处理 GET /api/issues/view-preference：取当前用户在 scope_type/scope_id
// 下的视图偏好。尚无记录时返回空对象 {}（不是错误）。
func (h *Handler) GetIssueViewPreference(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace id")
	if !ok {
		return
	}
	scopeType := r.URL.Query().Get("scope_type")
	scopeID, ok := h.resolvePreferenceScope(w, r, wsUUID, userID, scopeType, r.URL.Query().Get("scope_id"))
	if !ok {
		return
	}

	pref, err := h.Queries.GetIssueViewPreference(r.Context(), db.GetIssueViewPreferenceParams{
		WorkspaceID: wsUUID,
		UserID:      parseUUID(userID),
		ScopeType:   scopeType,
		ScopeID:     scopeID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// 尚无记录——是空文档而非错误。
			writeJSON(w, http.StatusOK, IssueViewPreferenceResponse{
				ScopeType: scopeType,
				ScopeID:   uuidToPtr(scopeID),
				Prefs:     json.RawMessage("{}"),
			})
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load preference")
		return
	}
	writeJSON(w, http.StatusOK, IssueViewPreferenceResponse{
		ScopeType: pref.ScopeType,
		ScopeID:   uuidToPtr(pref.ScopeID),
		Prefs:     json.RawMessage(pref.Prefs),
		UpdatedAt: timestampToString(pref.UpdatedAt),
	})
}

// PutIssueViewPreferenceRequest 是 PUT view-preference 的请求体。
type PutIssueViewPreferenceRequest struct {
	ScopeType string          `json:"scope_type"`
	ScopeID   *string         `json:"scope_id"`
	Prefs     json.RawMessage `json:"prefs"`
}

// PutIssueViewPreference 处理 PUT /api/issues/view-preference：整体保存当前用户在某作用域下的
// 视图偏好（upsert），并校验 prefs 必须是 JSON 对象。
func (h *Handler) PutIssueViewPreference(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace id")
	if !ok {
		return
	}
	var req PutIssueViewPreferenceRequest
	r.Body = http.MaxBytesReader(w, r.Body, issueViewBodyMaxBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	rawScopeID := ""
	if req.ScopeID != nil {
		rawScopeID = *req.ScopeID
	}
	scopeID, ok := h.resolvePreferenceScope(w, r, wsUUID, userID, req.ScopeType, rawScopeID)
	if !ok {
		return
	}
	if len(req.Prefs) == 0 {
		req.Prefs = json.RawMessage("{}")
	}
	if !isJSONObject(req.Prefs) {
		writeError(w, http.StatusBadRequest, "prefs must be a JSON object")
		return
	}

	pref, err := h.Queries.UpsertIssueViewPreference(r.Context(), db.UpsertIssueViewPreferenceParams{
		WorkspaceID: wsUUID,
		UserID:      parseUUID(userID),
		ScopeType:   req.ScopeType,
		ScopeID:     scopeID,
		Prefs:       req.Prefs,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save preference")
		return
	}
	writeJSON(w, http.StatusOK, IssueViewPreferenceResponse{
		ScopeType: pref.ScopeType,
		ScopeID:   uuidToPtr(pref.ScopeID),
		Prefs:     json.RawMessage(pref.Prefs),
		UpdatedAt: timestampToString(pref.UpdatedAt),
	})
}
