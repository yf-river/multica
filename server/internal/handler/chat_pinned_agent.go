// 本文件实现聊天「快捷 agent 栏」（pinned agent）的置顶管理：列表 / 置顶 / 取消
// 置顶。置顶按 (workspace, user) 维度、各用户私有；上限 maxChatPinnedAgents 保持
// 快捷栏紧凑。所有端点都先经 resolveChatAgentAccess 求出调用者当前可访问的 agent
// 集合，防止置顶暴露或指向已失去访问权的 agent（与 ListChatSessions 一致）。
package handler

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// maxChatPinnedAgents 封顶快捷 agent 栏，让它保持紧凑、小而精选。
const maxChatPinnedAgents = 5

// ChatPinnedAgentResponse 是用户「快捷 agent」栏中的一项（agent + 排序位置）。
type ChatPinnedAgentResponse struct {
	AgentID  string  `json:"agent_id"`
	Position float64 `json:"position"`
}

// resolveChatAgentAccess 返回调用者当前可用的 agent id 集合；出错时写好响应并
// 返回 ok=false。与 ListChatSessions 镜像，让置顶不能暴露或指向调用者已失去
// 访问权的 agent。
func (h *Handler) resolveChatAgentAccess(w http.ResponseWriter, r *http.Request, userID, workspaceID string) (map[string]struct{}, bool) {
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return nil, false
	}
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	allowed, ok := h.accessibleAgentIDs(r.Context(), workspaceID, actorType, actorID, member.Role)
	if !ok {
		writeError(w, http.StatusInternalServerError, "failed to resolve agent access")
		return nil, false
	}
	return allowed, true
}

// ListChatPinnedAgents 返回调用者置顶的快捷聊天 agent，按位置有序。已失去访问权
// （归档 / 权限变更）的 agent 的置顶会被从响应中丢弃。
func (h *Handler) ListChatPinnedAgents(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	allowed, ok := h.resolveChatAgentAccess(w, r, userID, workspaceID)
	if !ok {
		return
	}

	rows, err := h.Queries.ListChatPinnedAgents(r.Context(), db.ListChatPinnedAgentsParams{
		WorkspaceID: parseUUID(workspaceID),
		UserID:      parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list pinned agents")
		return
	}

	resp := make([]ChatPinnedAgentResponse, 0, len(rows))
	for _, row := range rows {
		agentID := uuidToString(row.AgentID)
		if _, ok := allowed[agentID]; !ok {
			continue
		}
		resp = append(resp, ChatPinnedAgentResponse{AgentID: agentID, Position: row.Position})
	}
	writeJSON(w, http.StatusOK, resp)
}

// PinChatAgent 把某 agent 加进调用者的快捷聊天栏（追加到末尾）。幂等——置顶
// 一个已置顶的 agent 返回 200。
func (h *Handler) PinChatAgent(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())

	var req struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	agentUUID, ok := parseUUIDOrBadRequest(w, req.AgentID, "agent_id")
	if !ok {
		return
	}

	allowed, ok := h.resolveChatAgentAccess(w, r, userID, workspaceID)
	if !ok {
		return
	}
	if _, ok := allowed[req.AgentID]; !ok {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}

	// 快捷栏封顶为一小组精选 agent。重复置顶已置顶的保持幂等，且在达到上限时也
	// 放行。
	existing, err := h.Queries.ListChatPinnedAgents(r.Context(), db.ListChatPinnedAgentsParams{
		WorkspaceID: parseUUID(workspaceID),
		UserID:      parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list pinned agents")
		return
	}
	alreadyPinned := false
	for _, e := range existing {
		if uuidToString(e.AgentID) == req.AgentID {
			alreadyPinned = true
			break
		}
	}
	if !alreadyPinned && len(existing) >= maxChatPinnedAgents {
		writeError(w, http.StatusBadRequest, "pinned agent limit reached")
		return
	}

	maxPos, err := h.Queries.GetMaxChatPinnedAgentPosition(r.Context(), db.GetMaxChatPinnedAgentPositionParams{
		WorkspaceID: parseUUID(workspaceID),
		UserID:      parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get position")
		return
	}

	row, err := h.Queries.CreateChatPinnedAgent(r.Context(), db.CreateChatPinnedAgentParams{
		WorkspaceID: parseUUID(workspaceID),
		UserID:      parseUUID(userID),
		AgentID:     agentUUID,
		Position:    maxPos + 1,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to pin agent")
		return
	}
	writeJSON(w, http.StatusOK, ChatPinnedAgentResponse{
		AgentID:  uuidToString(row.AgentID),
		Position: row.Position,
	})
}

// UnpinChatAgent 把某 agent 从调用者的快捷聊天栏移除。删除不存在行不报错
// （删除即幂等）。
func (h *Handler) UnpinChatAgent(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	agentUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "agentId"), "agentId")
	if !ok {
		return
	}

	if err := h.Queries.DeleteChatPinnedAgent(r.Context(), db.DeleteChatPinnedAgentParams{
		WorkspaceID: parseUUID(workspaceID),
		UserID:      parseUUID(userID),
		AgentID:     agentUUID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to unpin agent")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
