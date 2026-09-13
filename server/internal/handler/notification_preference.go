// 本文件实现用户的「通知偏好」读写端点：按 (workspace, user) 存一份 JSON 偏好
// map。PUT 是整体替换语义（兼容已安装客户端）；PATCH 只原子合并传来的键——避免
// 过期的标签页或设备把无关的静音设置整份冲掉。
//
// 允许的组（validNotifGroups）里 system_notifications 不是 inbox 事件组，而是「是否
// 弹系统通知横幅」的投递渠道开关——它共享同一份偏好 map，让一个端点覆盖全部通知偏好。
package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/logger"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// validNotifGroups 是 API 接受的偏好组键集合。不在其中的键一律拒绝。
// system_notifications 不是 inbox 事件组——它是控制原生 OS 通知横幅是否弹出的
// 「投递渠道」开关——但它与事件组共享同一份偏好 map，一个端点即可覆盖全部通知偏好。
var validNotifGroups = map[string]bool{
	"assignments":          true,
	"status_changes":       true,
	"comments":             true,
	"mentions":             true,
	"updates":              true,
	"agent_activity":       true,
	"system_notifications": true,
}

// validNotifValues 是每个组允许的偏好值。
var validNotifValues = map[string]bool{
	"all":   true,
	"muted": true,
}

// GetNotificationPreferences 读当前用户在 workspace 的通知偏好；没有记录时返回空偏好
// 而非 404。
func (h *Handler) GetNotificationPreferences(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())

	pref, err := h.Queries.GetNotificationPreference(r.Context(), db.GetNotificationPreferenceParams{
		WorkspaceID: parseUUID(workspaceID),
		UserID:      parseUUID(userID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusOK, map[string]any{
				"workspace_id": workspaceID,
				"preferences":  map[string]any{},
			})
			return
		}
		slog.Warn("GetNotificationPreference failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to get notification preferences")
		return
	}

	var prefs map[string]string
	if err := json.Unmarshal(pref.Preferences, &prefs); err != nil {
		prefs = map[string]string{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"workspace_id": workspaceID,
		"preferences":  prefs,
	})
}

// updateNotifPrefRequest 是更新请求体：一张组键 → 值的偏好 map。
type updateNotifPrefRequest struct {
	Preferences map[string]string `json:"preferences"`
}

// decodeNotificationPreferenceRequest 解析并校验请求体：preferences 必填，键与值都须
// 落在白名单内；通过后把 map 序列化成 JSON 字节供落库。
func decodeNotificationPreferenceRequest(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	var req updateNotifPrefRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return nil, false
	}

	if req.Preferences == nil {
		writeError(w, http.StatusBadRequest, "preferences field is required")
		return nil, false
	}

	for k, v := range req.Preferences {
		if !validNotifGroups[k] {
			writeError(w, http.StatusBadRequest, "invalid preference group: "+k)
			return nil, false
		}
		if !validNotifValues[v] {
			writeError(w, http.StatusBadRequest, "invalid preference value: "+v)
			return nil, false
		}
	}

	prefsJSON, err := json.Marshal(req.Preferences)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to marshal preferences")
		return nil, false
	}
	return prefsJSON, true
}

// writeNotificationPreferenceResponse 把落库后的偏好行统一写回（偏好列解不回 JSON 时
// 按空偏好处理）。
func writeNotificationPreferenceResponse(
	w http.ResponseWriter,
	workspaceID string,
	pref db.NotificationPreference,
) {
	var prefs map[string]string
	if err := json.Unmarshal(pref.Preferences, &prefs); err != nil {
		prefs = map[string]string{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"workspace_id": workspaceID,
		"preferences":  prefs,
	})
}

// UpdateNotificationPreferences 用整份替换语义更新偏好（PUT）——为兼容已安装客户端
// 保留原有契约。
func (h *Handler) UpdateNotificationPreferences(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	prefsJSON, ok := decodeNotificationPreferenceRequest(w, r)
	if !ok {
		return
	}

	pref, err := h.Queries.UpsertNotificationPreference(r.Context(), db.UpsertNotificationPreferenceParams{
		WorkspaceID: parseUUID(workspaceID),
		UserID:      parseUUID(userID),
		Preferences: prefsJSON,
	})
	if err != nil {
		slog.Warn("UpsertNotificationPreference failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to update notification preferences")
		return
	}
	writeNotificationPreferenceResponse(w, workspaceID, pref)
}

// PatchNotificationPreferences 只原子合并传来的键（PATCH）——防止过期的标签页或
// 设备把无关的静音设置整份冲掉。
func (h *Handler) PatchNotificationPreferences(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	prefsJSON, ok := decodeNotificationPreferenceRequest(w, r)
	if !ok {
		return
	}

	pref, err := h.Queries.PatchNotificationPreference(r.Context(), db.PatchNotificationPreferenceParams{
		WorkspaceID: parseUUID(workspaceID),
		UserID:      parseUUID(userID),
		Preferences: prefsJSON,
	})
	if err != nil {
		slog.Warn("PatchNotificationPreference failed", append(logger.RequestAttrs(r), "error", err)...)
		writeError(w, http.StatusInternalServerError, "failed to update notification preferences")
		return
	}
	writeNotificationPreferenceResponse(w, workspaceID, pref)
}
