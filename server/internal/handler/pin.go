// 本文件实现侧边栏「置顶」（pin）的 CRUD：把某 issue / project / view 钉到当前用户
// 侧边栏的固定入口，支持列表、创建（钉到末尾）、删除与排序。
//
// 置顶是纯个人化的用户级数据（user_id + workspace_id + item_type + item_id 唯一）。
// 能力开关约定：item_type=view 的置顶只发给声明理解它的客户端（?include=view）——
// 老客户端（桌面版早于分类功能）会把非 issue 置顶都当项目置顶去拉详情、404 后永久
// 自动取消置顶，把 view 置顶也按旧契约返回会毁掉用户的视图置顶。
package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// PinnedItemResponse 只携带 pin 的元数据。刻意不含标题/状态/编号/图标——客户端从
// 它们自己的 issue / project 查询缓存里派生这些，这样 issue:updated 事件能自然流入
// 侧边栏，无需对 pinKeys 做跨实体失效。
type PinnedItemResponse struct {
	ID          string  `json:"id"`
	WorkspaceID string  `json:"workspace_id"`
	UserID      string  `json:"user_id"`
	ItemType    string  `json:"item_type"`
	ItemID      string  `json:"item_id"`
	Position    float64 `json:"position"`
	CreatedAt   string  `json:"created_at"`
}

// pinnedItemToResponse 把置顶行转成对外结构。
func pinnedItemToResponse(p db.PinnedItem) PinnedItemResponse {
	return PinnedItemResponse{
		ID:          uuidToString(p.ID),
		WorkspaceID: uuidToString(p.WorkspaceID),
		UserID:      uuidToString(p.UserID),
		ItemType:    p.ItemType,
		ItemID:      uuidToString(p.ItemID),
		Position:    p.Position,
		CreatedAt:   timestampToString(p.CreatedAt),
	}
}

// CreatePinRequest 是创建置顶的请求体。
type CreatePinRequest struct {
	ItemType string `json:"item_type"`
	ItemID   string `json:"item_id"`
}

// ReorderPinsRequest 是重排序的请求体：客户端提交完整有序列表。
type ReorderPinsRequest struct {
	Items []ReorderItem `json:"items"`
}

// ReorderItem 是重排序列表里的一项（pin id + 新位置）。
type ReorderItem struct {
	ID       string  `json:"id"`
	Position float64 `json:"position"`
}

// ListPins 返回当前用户在 workspace 里的置顶列表。
func (h *Handler) ListPins(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := h.resolveWorkspaceID(r)

	pins, err := h.Queries.ListPinnedItems(r.Context(), db.ListPinnedItemsParams{
		WorkspaceID: parseUUID(workspaceID),
		UserID:      parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list pins")
		return
	}

	// 能力开关：在保存视图功能之前的客户端，把所有非 issue 置顶都当项目置顶拉详情、
	// 拿到 404 就永久自动取消置顶。若把 item_type=view 也按旧契约返回，老 Desktop 只要
	// 打开侧边栏就会毁掉用户的视图置顶——所以 view 行只发给声明理解它们的客户端
	// （?include=view）。
	includeViews := strings.Contains(r.URL.Query().Get("include"), "view")

	resp := make([]PinnedItemResponse, 0, len(pins))
	for _, p := range pins {
		if p.ItemType == "view" && !includeViews {
			continue
		}
		resp = append(resp, pinnedItemToResponse(p))
	}
	writeJSON(w, http.StatusOK, resp)
}

// CreatePin 把某 issue/project/view 钉到末尾：先校验 item 在本 workspace 存在且可读，
// 再在同一事务里取 max position+1 写入并记录事件。
func (h *Handler) CreatePin(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := h.resolveWorkspaceID(r)

	var req CreatePinRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.ItemType != "issue" && req.ItemType != "project" && req.ItemType != "view" {
		writeError(w, http.StatusBadRequest, "item_type must be 'issue', 'project' or 'view'")
		return
	}
	if req.ItemID == "" {
		writeError(w, http.StatusBadRequest, "item_id is required")
		return
	}

	itemUUID, ok := parseUUIDOrBadRequest(w, req.ItemID, "item_id")
	if !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	// 校验该 item 存在于本 workspace
	switch req.ItemType {
	case "issue":
		if _, err := h.Queries.GetIssueInWorkspace(r.Context(), db.GetIssueInWorkspaceParams{
			ID: itemUUID, WorkspaceID: wsUUID,
		}); err != nil {
			writeError(w, http.StatusNotFound, "issue not found")
			return
		}
	case "project":
		if _, err := h.Queries.GetProjectInWorkspace(r.Context(), db.GetProjectInWorkspaceParams{
			ID: itemUUID, WorkspaceID: wsUUID,
		}); err != nil {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
	case "view":
		// 与视图端点同一条读规则：自己的视图，或共享给 workspace 的视图。别人的私有
		// 视图按 404——置顶绝不能用来确认某个视图是否存在。
		view, err := h.Queries.GetIssueView(r.Context(), db.GetIssueViewParams{
			ID: itemUUID, WorkspaceID: wsUUID,
		})
		if err != nil || !canReadIssueView(view, parseUUID(userID)) {
			writeError(w, http.StatusNotFound, "view not found")
			return
		}
	}

	// 取当前最大位置，把新 pin 追加到末尾。
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start pin transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	maxPos, err := qtx.GetMaxPinnedItemPosition(r.Context(), db.GetMaxPinnedItemPositionParams{
		WorkspaceID: wsUUID,
		UserID:      parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get position")
		return
	}

	pin, err := qtx.CreatePinnedItem(r.Context(), db.CreatePinnedItemParams{
		WorkspaceID: wsUUID,
		UserID:      parseUUID(userID),
		ItemType:    req.ItemType,
		ItemID:      itemUUID,
		Position:    maxPos + 1,
	})
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "item already pinned")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create pin")
		return
	}

	resp := pinnedItemToResponse(pin)
	event, err := service.RecordDurableEventTx(r.Context(), qtx, buildPinDomainEvent(
		protocol.EventPinCreated, workspaceID, userID, "member", userID,
		map[string]any{"pin": resp}, uuidToString(pin.ID),
	))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record pin event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit pin")
		return
	}
	h.publishEvent(event)
	writeJSON(w, http.StatusCreated, resp)
}

// DeletePin 移除一个置顶（仅本人）。
func (h *Handler) DeletePin(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	itemType := chi.URLParam(r, "itemType")
	itemID := chi.URLParam(r, "itemId")

	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	itemUUID, ok := parseUUIDOrBadRequest(w, itemID, "item id")
	if !ok {
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start pin transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	err = qtx.DeletePinnedItem(r.Context(), db.DeletePinnedItemParams{
		WorkspaceID: wsUUID,
		UserID:      parseUUID(userID),
		ItemType:    itemType,
		ItemID:      itemUUID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete pin")
		return
	}

	event, err := service.RecordDurableEventTx(r.Context(), qtx, buildPinDomainEvent(protocol.EventPinDeleted, workspaceID, userID, "member", userID, map[string]any{
		"item_type": itemType,
		"item_id":   itemID,
	}, itemType+":"+itemID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record pin event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit pin deletion")
		return
	}
	h.publishEvent(event)
	w.WriteHeader(http.StatusNoContent)
}

// ReorderPins 按客户端提交的完整有序列表更新位置，并在同一事务里广播重排事件。
func (h *Handler) ReorderPins(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := h.resolveWorkspaceID(r)

	var req ReorderPinsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start pin transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	for _, item := range req.Items {
		itemUUID, ok := parseUUIDOrBadRequest(w, item.ID, "items[].id")
		if !ok {
			return
		}
		if err := qtx.UpdatePinnedItemPosition(r.Context(), db.UpdatePinnedItemPositionParams{
			Position:    item.Position,
			ID:          itemUUID,
			WorkspaceID: wsUUID,
			UserID:      parseUUID(userID),
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to reorder pins")
			return
		}
	}

	// 扇出让其它会话（web/desktop、或第二个标签页）重拉 pin 列表拿到新顺序。没有它，
	// 重排只在发起客户端上保持一致，直到硬刷新。
	event, err := service.RecordDurableEventTx(r.Context(), qtx, buildPinDomainEvent(protocol.EventPinReordered, workspaceID, userID, "member", userID, map[string]any{"items": req.Items}, pinReorderKey(req.Items)))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record pin event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit pin reorder")
		return
	}
	h.publishEvent(event)

	w.WriteHeader(http.StatusNoContent)
}

// pinReorderKey 用整个有序列表的 JSON 作幂等键。
func pinReorderKey(items []ReorderItem) string {
	// 客户端发送完整有序列表；JSON 对这个具体切片是稳定的，让同一次重排的重试共享
	// 同一个幂等键。
	b, _ := json.Marshal(items)
	return string(b)
}
