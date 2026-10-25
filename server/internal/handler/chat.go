// 本文件是「Chat（聊天会话）」资源的 HTTP 处理器集合：会话 CRUD（创建/列表/
// 详情/改名/换项目/置顶/归档/删除）、发送消息与分页读取、快捷操作重生成、
// 待办任务聚合（FAB 指示器）、排队任务的优先/清空、面向用户的取消任务
// （CancelTaskByUser）、以及被取消消息的草稿恢复。
// 贯穿全文件的权限模型：
//   - 会话归创建者私有（creator-only）；归档后只读，不可再入队新任务；
//   - 每次发送/重生成都重跑 INVOKE 门控（MUL-4525），不只是读侧可见性门控，
//     权限一旦被收回就立即停止入队；
//   - 删除/归档会取消该会话的在途任务，并清理各种「无外键」的关联行
//     （频道绑定、外发卡片、草稿恢复、agent builder 草稿）。
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/analytics"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/logger"
	obsmetrics "github.com/multica-ai/multica/server/internal/metrics"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/upstream"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// chatSessionTitleMaxLen 限制重命名输入长度。长得下有意义摘要、短得保持下拉行可扫读。
const chatSessionTitleMaxLen = 200

// ---------------------------------------------------------------------------
// 聊天会话
// ---------------------------------------------------------------------------

// CreateChatSessionRequest 是创建聊天会话的请求体。
type CreateChatSessionRequest struct {
	AgentID       string  `json:"agent_id"`
	Title         string  `json:"title"`
	ProjectID     *string `json:"project_id"`
	ReuseExisting bool    `json:"reuse_existing"`
}

// CreateChatSession 处理 POST /api/chat/sessions。权限要点：须在工作区内能
// INVOKE 目标 agent（MUL-3963 的调用权限而非软性的可见权限；A2A 会话按链顶
// 发起人判定）；agent 存在且未归档。创建前在事务里对 workspace/project 行加
// FOR KEY SHARE 锁，避免与工作区删除中的 FOR UPDATE 竞争（#5219 创建/删除协议）。
func (h *Handler) CreateChatSession(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())

	var req CreateChatSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.AgentID == "" {
		writeError(w, http.StatusBadRequest, "agent_id is required")
		return
	}
	agentID, ok := parseUUIDOrBadRequest(w, req.AgentID, "agent_id")
	if !ok {
		return
	}
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	projectID := pgtype.UUID{Valid: false}
	if req.ProjectID != nil && strings.TrimSpace(*req.ProjectID) != "" {
		projectID, ok = parseUUIDOrBadRequest(w, strings.TrimSpace(*req.ProjectID), "project_id")
		if !ok {
			return
		}
	}

	// 校验 agent 属于该工作区。
	agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
		ID:          agentID,
		WorkspaceID: workspaceUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return
	}
	if agent.ArchivedAt.Valid {
		writeError(w, http.StatusBadRequest, "agent is archived")
		return
	}
	// 调用门控：发起聊天会产生 agent 运行，因此走调用权限（MUL-3963），而不是
	// 软性的可见门控。agent 之间的聊天会话按链顶发起者判定。
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	if !h.canInvokeAgent(r.Context(), agent, actorType, actorID, h.invokeOriginatorFromRequest(r, actorType, actorID), workspaceID) {
		writeError(w, http.StatusForbidden, "you do not have access to this agent")
		return
	}
	if req.ReuseExisting {
		if agent.SystemKey.String != service.MikaSystemKey || projectID.Valid || actorType != "member" {
			writeError(w, http.StatusBadRequest, "conversation reuse is only supported for a member's Mika chat")
			return
		}
		session, err := h.getOrCreateMikaSession(r.Context(), agent, workspaceID, userID, req.Title)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to open the Mika conversation")
			return
		}
		writeJSON(w, http.StatusOK, chatSessionToResponse(session))
		return
	}

	// 在事务里创建，事务先对 workspace 行加 FOR KEY SHARE 锁：它与
	// DeleteWorkspace 的 FOR UPDATE 冲突，因此不会往「删除进行中」的工作区里
	// 创建会话、随后被删除后的终结器清成孤儿（#5219 创建/删除协议；见
	// LockWorkspaceForChatSessionCreate）。
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	if _, err := qtx.LockWorkspaceForChatSessionCreate(r.Context(), workspaceUUID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "workspace not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to lock workspace")
		return
	}
	if projectID.Valid {
		if _, err := qtx.LockProjectForChatSessionCreate(r.Context(), db.LockProjectForChatSessionCreateParams{
			ID:          projectID,
			WorkspaceID: workspaceUUID,
		}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "project not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "failed to lock project")
			return
		}
	}

	session, err := qtx.CreateChatSession(r.Context(), db.CreateChatSessionParams{
		ID:          dbid.NewV7(),
		WorkspaceID: workspaceUUID,
		AgentID:     agentID,
		CreatorID:   parseUUID(userID),
		Title:       req.Title,
		ProjectID:   projectID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create chat session")
		return
	}
	session, err = qtx.MarkChatSessionExplicitlyCreated(r.Context(), session.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to mark chat session explicit")
		return
	}
	createdEvent, err := recordChatEventTx(r.Context(), qtx, buildChatSessionCreatedEvent(session, actorType, actorID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record chat session event")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit chat session create")
		return
	}
	h.publishEvent(createdEvent)

	writeJSON(w, http.StatusCreated, chatSessionToResponse(session))
}

// ListChatSessions 处理 GET /api/chat/sessions。返回当前用户创建的会话列表，
// ?status=all 时含已归档/已删软标记的。先一次性计算可访问 agent 集合，据此
// 丢弃目标 agent 已无权访问的会话。
func (h *Handler) ListChatSessions(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())

	// 只算一次可访问 agent 集合，据此丢弃目标 agent 已无权访问的会话——否则
	// 角色被降级的成员仍能看到（并经由 ListChatMessages 读到）其曾有权限的
	// private agent 的会话列表与转录。角色取自工作区成员上下文。
	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	allowed, ok := h.accessibleAgentIDs(r.Context(), workspaceID, actorType, actorID, member.Role)
	if !ok {
		writeError(w, http.StatusInternalServerError, "failed to resolve agent access")
		return
	}

	status := r.URL.Query().Get("status")

	// 两个调用点 → 两种行类型但形状相同。用小的逐分支循环收集进同一响应切片。
	var resp []ChatSessionResponse
	if status == "all" {
		rows, err := h.Queries.ListAllChatSessionsByCreator(r.Context(), db.ListAllChatSessionsByCreatorParams{
			WorkspaceID: parseUUID(workspaceID),
			CreatorID:   parseUUID(userID),
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list chat sessions")
			return
		}
		resp = make([]ChatSessionResponse, 0, len(rows))
		for _, s := range rows {
			if _, ok := allowed[uuidToString(s.AgentID)]; !ok {
				continue
			}
			resp = append(resp, ChatSessionResponse{
				ID:          uuidToString(s.ID),
				WorkspaceID: uuidToString(s.WorkspaceID),
				AgentID:     uuidToString(s.AgentID),
				CreatorID:   uuidToString(s.CreatorID),
				ProjectID:   uuidToPtr(s.ProjectID),
				Title:       s.Title,
				Status:      s.Status,
				HasUnread:   s.UnreadCount > 0,
				UnreadCount: int(s.UnreadCount),
				LastMessage: buildChatLastMessage(s.LastMessageAt, s.LastMessageContent, s.LastMessageRole, s.LastMessageFailureReason, s.LastMessageKind),
				Pinned:      s.PinnedAt.Valid,
				CreatedAt:   timestampToString(s.CreatedAt),
				UpdatedAt:   timestampToString(s.UpdatedAt),
			})
		}
	} else {
		rows, err := h.Queries.ListChatSessionsByCreator(r.Context(), db.ListChatSessionsByCreatorParams{
			WorkspaceID: parseUUID(workspaceID),
			CreatorID:   parseUUID(userID),
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list chat sessions")
			return
		}
		resp = make([]ChatSessionResponse, 0, len(rows))
		for _, s := range rows {
			if _, ok := allowed[uuidToString(s.AgentID)]; !ok {
				continue
			}
			resp = append(resp, ChatSessionResponse{
				ID:          uuidToString(s.ID),
				WorkspaceID: uuidToString(s.WorkspaceID),
				AgentID:     uuidToString(s.AgentID),
				CreatorID:   uuidToString(s.CreatorID),
				ProjectID:   uuidToPtr(s.ProjectID),
				Title:       s.Title,
				Status:      s.Status,
				HasUnread:   s.UnreadCount > 0,
				UnreadCount: int(s.UnreadCount),
				LastMessage: buildChatLastMessage(s.LastMessageAt, s.LastMessageContent, s.LastMessageRole, s.LastMessageFailureReason, s.LastMessageKind),
				Pinned:      s.PinnedAt.Valid,
				CreatedAt:   timestampToString(s.CreatedAt),
				UpdatedAt:   timestampToString(s.UpdatedAt),
			})
		}
	}
	if err := h.hydrateChatSessionChannelMetadata(r.Context(), resp); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load chat channel metadata")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// loadChatSessionForUser 按会话归属加载会话：仅创建者可访问（403），且必须属
// 当前工作区。失败时已写好错误响应。
func (h *Handler) loadChatSessionForUser(w http.ResponseWriter, r *http.Request, userID, workspaceID, sessionID string) (db.ChatSession, bool) {
	sessionUUID, ok := parseUUIDOrBadRequest(w, sessionID, "chat session id")
	if !ok {
		return db.ChatSession{}, false
	}
	workspaceUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return db.ChatSession{}, false
	}
	session, err := h.Queries.GetChatSessionInWorkspace(r.Context(), db.GetChatSessionInWorkspaceParams{
		ID:          sessionUUID,
		WorkspaceID: workspaceUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "chat session not found")
		return db.ChatSession{}, false
	}
	if uuidToString(session.CreatorID) != userID {
		writeError(w, http.StatusForbidden, "not your chat session")
		return db.ChatSession{}, false
	}
	return session, true
}

// gateChatSessionForUser 在会话归属检查之上叠加 private-agent 访问门控：即便
// 成员仍是会话创建者，一旦失去对目标 agent 的访问（角色降级、所有权转移、
// agent 转 private），就不能继续读该聊天转录。返回 ok=false 前已写好错误响应。
func (h *Handler) gateChatSessionForUser(w http.ResponseWriter, r *http.Request, userID, workspaceID, sessionID string) (db.ChatSession, bool) {
	session, ok := h.loadChatSessionForUser(w, r, userID, workspaceID, sessionID)
	if !ok {
		return db.ChatSession{}, false
	}
	agent, err := h.Queries.GetAgent(r.Context(), session.AgentID)
	if err != nil {
		writeError(w, http.StatusNotFound, "agent not found")
		return db.ChatSession{}, false
	}
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	if !h.canAccessPrivateAgent(r.Context(), agent, actorType, actorID, workspaceID) {
		writeError(w, http.StatusForbidden, "you do not have access to this agent")
		return db.ChatSession{}, false
	}
	return session, true
}

// gatePublicChatSessionForUser 在常规归属与 private-agent 检查之上增加「成员可
// 见」投影边界：内部频道命令类会话保持持久化，但只含这些命令的会话不是公开的
// Chat。清理与恢复操作继续用更底层的归属门控，让空绑定会话仍可归档/删除、成员
// 自己取消的草稿永不搁浅。一切常规 Chat 交互都用本门控，缓存起来的隐藏会话 ID
// 无法复活一个只含命令的 Chat。
func (h *Handler) gatePublicChatSessionForUser(w http.ResponseWriter, r *http.Request, userID, workspaceID, sessionID string) (db.ChatSession, bool) {
	session, ok := h.gateChatSessionForUser(w, r, userID, workspaceID, sessionID)
	if !ok {
		return db.ChatSession{}, false
	}
	if _, err := h.Queries.GetPublicChatSessionInWorkspace(r.Context(), db.GetPublicChatSessionInWorkspaceParams{
		ID:          session.ID,
		WorkspaceID: session.WorkspaceID,
	}); err != nil {
		writeError(w, http.StatusNotFound, "chat session not found")
		return db.ChatSession{}, false
	}
	return session, true
}

// GetChatSession 处理 GET /api/chat/sessions/{sessionId}，走「公开会话」门控，
// 返回单个会话（含频道来源元数据）。
func (h *Handler) GetChatSession(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	sessionID := chi.URLParam(r, "sessionId")

	session, ok := h.gatePublicChatSessionForUser(w, r, userID, workspaceID, sessionID)
	if !ok {
		return
	}

	resp := chatSessionToResponse(session)
	// hydrateChatSessionChannelMetadata 会改动切片元素，因此这里保留一个实体切片
	// 而非把临时值交给 writeJSON。
	responses := []ChatSessionResponse{resp}
	if err := h.hydrateChatSessionChannelMetadata(r.Context(), responses); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load chat channel metadata")
		return
	}
	writeJSON(w, http.StatusOK, responses[0])
}

// UpdateChatSessionRequest 是更新会话的请求体：title（行内改名）与
// project_id（后续轮次使用的项目上下文）必须恰好给一个，靠指针区分「没传」和
// 「显式传 null 清空」。
type UpdateChatSessionRequest struct {
	Title     *string         `json:"title"`
	ProjectID json.RawMessage `json:"project_id"`
}

// UpdateChatSession 更新会话上一个用户可编辑字段。title 供行内改名；project_id
// 控制后续轮次使用的项目上下文。status 与 pinned 各有专用端点，agent/creator/
// workspace 不可变，resume 指针（session_id / work_dir / runtime_id）归 daemon。
func (h *Handler) UpdateChatSession(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	sessionID := chi.URLParam(r, "sessionId")

	var req UpdateChatSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	hasTitle := req.Title != nil
	hasProjectID := req.ProjectID != nil
	if hasTitle == hasProjectID {
		writeError(w, http.StatusBadRequest, "exactly one of title or project_id is required")
		return
	}

	session, ok := h.gatePublicChatSessionForUser(w, r, userID, workspaceID, sessionID)
	if !ok {
		return
	}

	var (
		updated      db.ChatSession
		err          error
		updatedEvent events.Event
	)
	var projectIDChanged bool
	if hasTitle {
		title := strings.TrimSpace(*req.Title)
		if title == "" {
			writeError(w, http.StatusBadRequest, "title is required")
			return
		}
		if len([]rune(title)) > chatSessionTitleMaxLen {
			writeError(w, http.StatusBadRequest, "title is too long")
			return
		}
		tx, txErr := h.TxStarter.Begin(r.Context())
		if txErr != nil {
			writeError(w, http.StatusInternalServerError, "failed to start transaction")
			return
		}
		defer tx.Rollback(r.Context())
		qtx := h.Queries.WithTx(tx)
		if _, lockErr := qtx.LockChatSessionForRuntimeBind(r.Context(), session.ID); lockErr != nil {
			writeError(w, http.StatusInternalServerError, "failed to lock chat session")
			return
		}
		updated, err = qtx.UpdateChatSessionTitle(r.Context(), db.UpdateChatSessionTitleParams{
			ID:    session.ID,
			Title: title,
		})
		if err == nil {
			resolvedSessionID := uuidToString(updated.ID)
			updatedEvent, err = recordChatEventTx(r.Context(), qtx, buildChatSessionUpdatedEvent(updated, "member", userID, protocol.ChatSessionUpdatedPayload{
				ChatSessionID: resolvedSessionID,
				Title:         updated.Title,
				UpdatedAt:     timestampToString(updated.UpdatedAt),
			}, "title"))
		}
		if err == nil {
			err = tx.Commit(r.Context())
		}
	} else {
		projectID := pgtype.UUID{Valid: false}
		if string(req.ProjectID) != "null" {
			var rawProjectID string
			if err := json.Unmarshal(req.ProjectID, &rawProjectID); err != nil {
				writeError(w, http.StatusBadRequest, "project_id must be a UUID or null")
				return
			}
			rawProjectID = strings.TrimSpace(rawProjectID)
			if rawProjectID == "" {
				writeError(w, http.StatusBadRequest, "project_id must be a UUID or null")
				return
			}
			projectID, ok = parseUUIDOrBadRequest(w, rawProjectID, "project_id")
			if !ok {
				return
			}
		}

		tx, txErr := h.TxStarter.Begin(r.Context())
		if txErr != nil {
			writeError(w, http.StatusInternalServerError, "failed to start transaction")
			return
		}
		defer tx.Rollback(r.Context())
		qtx := h.Queries.WithTx(tx)

		if projectID.Valid {
			if _, lockErr := qtx.LockProjectForChatSessionCreate(r.Context(), db.LockProjectForChatSessionCreateParams{
				ID:          projectID,
				WorkspaceID: session.WorkspaceID,
			}); lockErr != nil {
				if errors.Is(lockErr, pgx.ErrNoRows) {
					writeError(w, http.StatusNotFound, "project not found")
					return
				}
				writeError(w, http.StatusInternalServerError, "failed to lock project")
				return
			}
		}

		updated, err = qtx.UpdateChatSessionProject(r.Context(), db.UpdateChatSessionProjectParams{
			ID:          session.ID,
			WorkspaceID: session.WorkspaceID,
			ProjectID:   projectID,
		})
		if err == nil {
			resolvedSessionID := uuidToString(updated.ID)
			projectIDValue := uuidToPtr(updated.ProjectID)
			updatedEvent, err = recordChatEventTx(r.Context(), qtx, buildChatSessionUpdatedEvent(updated, "member", userID, protocol.ChatSessionUpdatedPayload{
				ChatSessionID: resolvedSessionID,
				Title:         updated.Title,
				ProjectID:     &projectIDValue,
				UpdatedAt:     timestampToString(updated.UpdatedAt),
			}, "project"))
		}
		if err == nil {
			err = tx.Commit(r.Context())
		}
		projectIDChanged = true
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update chat session")
		return
	}

	resolvedSessionID := uuidToString(updated.ID)
	payload := protocol.ChatSessionUpdatedPayload{
		ChatSessionID: resolvedSessionID,
		Title:         updated.Title,
		UpdatedAt:     timestampToString(updated.UpdatedAt),
	}
	if projectIDChanged {
		projectID := uuidToPtr(updated.ProjectID)
		payload.ProjectID = &projectID
	}
	if updatedEvent.ID != "" {
		h.publishEvent(updatedEvent)
	} else {
		h.publishChat(protocol.EventChatSessionUpdated, workspaceID, "member", userID, resolvedSessionID, payload)
	}

	writeJSON(w, http.StatusOK, chatSessionToResponse(updated))
}

// SetChatSessionPinnedRequest 是置顶/取消置顶的请求体。
type SetChatSessionPinnedRequest struct {
	Pinned bool `json:"pinned"`
}

// SetChatSessionPinned 置顶/取消置顶会话，让它钉在调用方会话列表顶部。置顶状态
// 按会话、由于会话按创建者，天然按用户。它永不 bump updated_at（见 SQL），
// 取消置顶不会让会话跳到活动排序列表顶部。
func (h *Handler) SetChatSessionPinned(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	sessionID := chi.URLParam(r, "sessionId")

	var req SetChatSessionPinnedRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	session, ok := h.gatePublicChatSessionForUser(w, r, userID, workspaceID, sessionID)
	if !ok {
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	if _, err := qtx.LockChatSessionForRuntimeBind(r.Context(), session.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to lock chat session")
		return
	}
	updated, err := qtx.SetChatSessionPinned(r.Context(), db.SetChatSessionPinnedParams{
		ID:     session.ID,
		Pinned: req.Pinned,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update chat session")
		return
	}

	resolvedSessionID := uuidToString(updated.ID)
	pinned := updated.PinnedAt.Valid
	event, err := recordChatEventTx(r.Context(), qtx, buildChatSessionUpdatedEvent(updated, "member", userID, protocol.ChatSessionUpdatedPayload{
		ChatSessionID: resolvedSessionID,
		Title:         updated.Title,
		Pinned:        &pinned,
		UpdatedAt:     timestampToString(updated.UpdatedAt),
	}, "pin"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record chat session event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit chat session update")
		return
	}
	h.publishEvent(event)

	writeJSON(w, http.StatusOK, chatSessionToResponse(updated))
}

// SetChatSessionArchivedRequest 是归档/取消归档会话的请求体。
type SetChatSessionArchivedRequest struct {
	Archived bool `json:"archived"`
}

// SetChatSessionArchived 归档/取消归档调用方拥有的会话。归档是可逆的删除孪生：
// 行仍留在用户历史（「已归档」入口后），会话变为只读——SendChatMessage 拒绝
// status='archived'。硬删除只在归档列表里提供，悬停一次点击不会销毁任何东西。
//
// 归档还会切断任何外部频道绑定。Web 发送路径已把 status='archived' 当只读，但
// 频道引擎（飞书/Slack）不经检查会话状态、直接经 channel_chat_session_binding
// 解析入站流量——于是用户归档后，绑定的会话仍持续积累 agent 回复和卡住的未读
// 徽标（MUL-4372）。在同一事务里丢弃绑定，让该外部聊天的下一条入站消息在
// 新绑定下创建全新 chat_session（见 EnsureSession），而不是复活这个已归档的。
// 取消归档有意不重建绑定：若后续流量已分叉出新会话，现在频道归那个新会话所有，
// 恢复旧绑定等于把它抢回来。
//
// 切断绑定正是让「归档绑定会话会取消其在途任务」成立的原因：适配器仍握着
// chat id，因此一次在归档后幸存的轮次会向一个该会话已不属于的群房间作答。
// 因此这个取消仅作用于「曾有绑定」的会话。归档纯 Web 聊天则不动其任务——它是
// UI 呈现的可逆整理动作（无确认、取消归档即撤销），其破坏性孪生是 Stop 按钮，
// 走 CancelTaskByUser 取消并把用户输入的提示词还回去。
func (h *Handler) SetChatSessionArchived(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	sessionID := chi.URLParam(r, "sessionId")

	var req SetChatSessionArchivedRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	session, ok := h.gateChatSessionForUser(w, r, userID, workspaceID, sessionID)
	if !ok {
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	updated, err := qtx.SetChatSessionArchived(r.Context(), db.SetChatSessionArchivedParams{
		ID:       session.ID,
		Archived: req.Archived,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update chat session")
		return
	}

	// 事务内取消的行，提交后再广播。取消归档时与纯 Web 聊天时为 nil；
	// BroadcastCancelledTasks 对空切片是 no-op。
	var cancelled []db.AgentTaskQueue
	var archivedEvent events.Event

	if req.Archived {
		// 在下方 DELETE 之前读绑定——DELETE 会抹掉证据：跑完后无法分辨「曾在
		// 频道上」与「从不在频道上」的会话。
		_, bindingErr := qtx.GetChannelChatSessionBindingBySessionAny(r.Context(), session.ID)
		switch {
		case bindingErr == nil:
			// 有绑定，且下方删除即将丢弃该绑定。ClaimAgentTask 不读
			// chat_session.status，因此归档前入队的任务在归档后仍可被 claim：
			// daemon 会在用户已关闭的会话上跑一轮，花掉 runtime 与配额并把
			// assistant 消息写进已归档聊天。届时绑定已消失，brief 会把运行描述
			// 成私密 web 聊天——这正是下方历史注记要防的错位——而出站发送方
			// 经同一被删行解析目的地，已无路可投递。与 DeleteChatSession 相同，
			// 在丢弃绑定的同一事务里取消。
			//
			// 查询不限于从未开始的工作：它也匹配 dispatched/running/
			// waiting_local_directory/deferred，因此返回的行必须到达下方提交后
			// 的广播，而不是被丢弃。
			cancelled, err = qtx.CancelAgentTasksByChatSession(r.Context(), session.ID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to cancel queued tasks for the archived session")
				return
			}
			if err = service.SettleDeliveredDelegatedFailureRecoveries(r.Context(), qtx, cancelled...); err != nil {
				writeError(w, http.StatusInternalServerError, "failed to settle delegated failure recoveries")
				return
			}
			for _, task := range cancelled {
				if _, err = service.RecordTaskTerminalEventTx(r.Context(), qtx, protocol.EventTaskCancelled, task,
					map[string]any{"reason": "chat_session_archived"}); err != nil {
					writeError(w, http.StatusInternalServerError, "failed to record cancelled chat tasks")
					return
				}
			}
		case errors.Is(bindingErr, pgx.ErrNoRows):
			// 纯 Web 聊天没有房间、适配器与迟到的回答落脚处，因此没有要保护谁
			// 的问题，归档正如 UI 所说：可逆的「收起来」动作，无确认、可取消归档，
			// 硬删除只在其后提供。取消还会毁掉用户输入中的提示词——Stop 按钮经
			// CancelTaskByUser 取消，会把提示词还回来（cancelled_chat_message
			// .restore_to_input，或一条持久的 chat_draft_restore 行）——而
			// CancelAgentTasksByChatSession 两者都不做。
		default:
			// 无法判定该会话是否曾绑定。继续会静默跳过取消、重开上面那个 bug，
			// 因此失败归档而非猜测；调用方可重试。
			slog.Warn("read chat session channel binding failed", "session_id", sessionID, "error", bindingErr)
			writeError(w, http.StatusInternalServerError, "failed to read chat session channel binding")
			return
		}
		if err := qtx.DeleteChannelChatSessionBindingBySession(r.Context(), session.ID); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to clear chat session channel binding")
			return
		}
	}
	resolvedSessionID := uuidToString(updated.ID)
	status := updated.Status
	archivedEvent, err = recordChatEventTx(r.Context(), qtx, buildChatSessionUpdatedEvent(updated, "member", userID, protocol.ChatSessionUpdatedPayload{
		ChatSessionID: resolvedSessionID,
		Title:         updated.Title,
		Status:        &status,
		UpdatedAt:     timestampToString(updated.UpdatedAt),
	}, "status"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record chat session event")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		slog.Warn("commit chat session archive failed", "session_id", sessionID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to commit chat session update")
		return
	}

	// 提交后广播，同 DeleteChatSession：订阅方绝不该观察到未真正持久化事务的
	// 事件。下面这个调用会采集取消结果、吊销任务 mat_ token、让各 agent 退出
	// 'working'、发出 task:cancelled 让其他客户端丢弃该行而非排队展示到下次刷新，
	// 并唤醒 runtime 让排队的后继任务此刻即被 claim，而不是等 daemon 下次轮询。
	h.TaskService.BroadcastCancelledTasks(r.Context(), workspaceID, cancelled)

	h.publishEvent(archivedEvent)

	writeJSON(w, http.StatusOK, chatSessionToResponse(updated))
}

// DeleteChatSession 硬删除调用方拥有的会话。行锁 + 取消 + 删除在单个事务内，
// 这样并发的 SendChatMessage 无法入队一个之后会被 agent_task_queue 的
// chat_session_id 外键 ON DELETE SET NULL 变成孤儿任务的任务。取消失败会中止
// 删除；事件只在提交后发出。
func (h *Handler) DeleteChatSession(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	sessionID := chi.URLParam(r, "sessionId")

	session, ok := h.loadChatSessionForUser(w, r, userID, workspaceID, sessionID)
	if !ok {
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	// 对 chat_session 行加 FOR UPDATE，挡住任何引用它的并发 agent_task_queue
	// INSERT（外键校验需要 KEY SHARE 锁）。提交删除后，被挡的 INSERT 因外键检查
	// 失败，无法落下孤儿任务。
	if _, err := qtx.LockChatSessionForDelete(r.Context(), session.ID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// 已不存在——按幂等成功处理。
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to lock chat session")
		return
	}

	// claim/clear/prioritize/direct send 都会在任务行前锁 agent。删除也保持
	// 会话锁之后的同序 agent → task 后缀，否则 builder-agent 删除会与并发 claim
	// 死锁。
	if _, err := qtx.GetAgentForClaimUpdate(r.Context(), session.AgentID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to lock chat agent")
		return
	}

	cancelled, err := qtx.CancelAgentTasksByChatSession(r.Context(), session.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to cancel chat session tasks")
		return
	}
	if err := service.SettleDeliveredDelegatedFailureRecoveries(r.Context(), qtx, cancelled...); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to settle delegated failure recoveries")
		return
	}
	for _, task := range cancelled {
		if _, err = service.RecordTaskTerminalEventTx(r.Context(), qtx, protocol.EventTaskCancelled, task,
			map[string]any{"reason": "chat_session_deleted"}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to record cancelled chat tasks")
			return
		}
	}

	// channel_chat_session_binding 曾经带 ON DELETE CASCADE 的 chat_session 外键；
	// MUL-3515 §4 移除了所有 channel_* 外键，因此要在删除 chat_session 的同一事务
	// 里手动剪掉绑定。
	if err := qtx.DeleteChannelChatSessionBindingBySession(r.Context(), session.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete chat session binding")
		return
	}
	// channel_outbound_card_message 同样以 chat_session_id 为键且无外键、无回收器，
	// 因此要在同一事务里清理，否则删除的会话会留下永久的孤儿卡片行（#4810 后续）。
	if err := qtx.DeleteChannelOutboundCardMessagesBySession(r.Context(), session.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete chat session outbound cards")
		return
	}
	// chat_draft_restore 同样以 chat_session_id 为键且无外键，因此待恢复草稿在此
	// 清理而非靠 DB 级联（#5219）。上方的 LockChatSessionForDelete 兼作草稿恢复
	// 协议的删除方：FinalizeDeferredCancelledChat 插入恢复前会取同一 chat_session
	// 锁，无法绕过这次清扫（见 chat.sql 的 LockChatSessionForTask）。
	if err := qtx.DeleteChatDraftRestoresBySession(r.Context(), session.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete chat session draft restores")
		return
	}

	// 同一个无外键清理：builder 会话正在编辑的 agent 配置。普通聊天没有，为 no-op。
	if err := qtx.DeleteAgentBuilderDraft(r.Context(), session.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete agent builder draft")
		return
	}

	if err := qtx.DeleteChatSession(r.Context(), db.DeleteChatSessionParams{
		ID:          session.ID,
		WorkspaceID: session.WorkspaceID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete chat session")
		return
	}
	if err := qtx.DeleteAgentLabelAssignmentsByAgent(r.Context(), session.AgentID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to remove chat session agent label assignments")
		return
	}
	if err := qtx.DeleteSystemAgentByID(r.Context(), session.AgentID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to clean up chat session agent")
		return
	}
	deletedEvent, err := recordChatEventTx(r.Context(), qtx, buildChatSessionDeletedEvent(session, "member", userID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record chat session event")
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		slog.Warn("commit chat session delete failed", "session_id", sessionID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to commit chat session delete")
		return
	}

	// 发送后提交的广播。订阅方绝不该观察到未真正持久化事务的事件。
	//
	// workspace 必须取自已删掉的会话：任务在删除前被取消返回，仍带着它的 id，但它
	// 所经以解析的行此刻已不存在。
	h.TaskService.BroadcastCancelledTasks(r.Context(), workspaceID, cancelled)

	h.publishEvent(deletedEvent)

	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// 聊天消息
// ---------------------------------------------------------------------------

// SendChatMessageRequest 是发送聊天消息的请求体：纯文本内容 + 要绑定的附件 id。
type SendChatMessageRequest struct {
	HealthReportIDs []string `json:"health_report_ids"`
	Content         string   `json:"content"`
	AttachmentIDs   []string `json:"attachment_ids"`
}

// SendChatMessageResponse 是发送消息的响应：MessageID/TaskID 供客户端挂载，
// SupportsQueue/Queued 告知任务走了排队（agent 忙碌时）还是立即派发。
type SendChatMessageResponse struct {
	MessageID     string `json:"message_id"`
	TaskID        string `json:"task_id"`
	SupportsQueue bool   `json:"supports_queue"`
	Queued        bool   `json:"queued"`
	// AttachmentIDs 是服务端实际绑定到本条消息的附件行。客户端拿它与自己请求的
	// id 比对，附件静默绑定失败时可提醒用户——无需额外往返。不用 `omitempty`：
	// 请求了附件但一条都没绑上的发送必须序列化为 `[]`（而非省略），否则客户端
	// 分不清「全部绑定失败」与「旧服务器没有该字段」，会悄悄跳过本应出现的警告。
	// 未请求附件时值为 nil → `null`，客户端守卫在请求 id 检查处短路。
	AttachmentIDs []string `json:"attachment_ids"`
	// CreatedAt 在用户点发送的瞬间为聊天 StatusPill 计时器提供锚点。没有它前端
	// 回退到本地时钟，之后 WS 事件送达真实 created_at 时计时器会「倒跳」。在此
	// 返回它，让 pill 从一开始就以稳定锚点显示 0 秒。
	CreatedAt string `json:"created_at"`
}

// SendChatMessage 处理 POST /api/chat/sessions/{sessionId}/messages。核心要点：
// 门控=归属+private-agent+INVOKE 三重（见下方各注记），会话必须 active（已归档
// 只读）、agent 必须可入队。整个轮次（任务+消息+附件）由 SendDirectChatMessage
// 原子提交，首条消息触发尽力而为的 LLM 自动起标题（MUL-4295）。
func (h *Handler) SendChatMessage(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	sessionID := chi.URLParam(r, "sessionId")

	var req SendChatMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Content == "" {
		writeError(w, http.StatusBadRequest, "content is required")
		return
	}

	// 提前校验附件 id，非法输入在 400 即返回、不产生任何状态变更。真正的绑定在
	// CreateChatMessage 之后进行，这样才能拿到 message_id 回填进附件行。
	attachmentIDs, ok := parseUUIDSliceOrBadRequest(w, req.AttachmentIDs, "attachment_ids")
	if !ok {
		return
	}

	// 每次发送都加载会话并重跑 private-agent 门控。会话创建者在创建时过了该
	// 门控，但其工作区角色（或 agent 的所有者）此后可能已变——别让陈旧会话
	// 成为用户已无法到达的 private agent 的后门。agent 发送方绕过以保留 A2A
	// 协作。
	session, ok := h.gatePublicChatSessionForUser(w, r, userID, workspaceID, sessionID)
	if !ok {
		return
	}
	// 已归档会话只读：拒绝为它入队新 agent 工作（见 SetChatSessionArchived）。
	// 前端对 status='archived' 禁用输入框，只在那里提供取消归档/删除。功能推出
	// 前的旧软归档行也落入同一检查。
	if session.Status != "active" {
		writeError(w, http.StatusBadRequest, "chat session is archived")
		return
	}

	// 在持久化用户消息前预检 agent 的入队前置条件。EnqueueChatTask（下方）会
	// 对已归档/无 runtime 的 agent 报错；没有该检查，陈旧客户端（另一标签页里
	// agent 已被归档）会先落下用户消息再吃一个 500，留下无任务无回复的孤儿消息。
	// 快速以 4xx 失败、不做任何变更。见 EnqueueChatTask 的
	// ErrChatTaskAgentArchived / NoRuntime。
	agent, err := h.Queries.GetAgent(r.Context(), session.AgentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load chat agent")
		return
	}
	if agent.ArchivedAt.Valid {
		writeError(w, http.StatusConflict, "chat agent is archived")
		return
	}
	// 共同判定：未绑定 agent 与「其 CLI 无法运行」的机器在这里都是拒绝，各有
	// 自己的码。单纯离线的 runtime 根本不查——聊天消息会照旧排队等它。
	if verdict, err := service.AgentReadiness(r.Context(), h.runtimeLookup(obsmetrics.RuntimeLookupSourceChat), agent); err == nil && verdict.Blocked() {
		h.writeDispatchBlocked(w, http.StatusConflict, verdict.Reason)
		return
	}

	// 每次发送都重跑 INVOKE 门控，而不只是 gatePublicChatSessionForUser 里较软
	// 的可见门控（MUL-4525）。canAccessPrivateAgent 允许工作区 admin 继续读转录，
	// 但发送消息会入队运行，必须满足 canInvokeAgent——它没有 admin 绕过。创建
	// 会话时用户能 invoke 该 agent，一旦该权限被收回（agent 转 private、所有权
	// 转移、目标被移出白名单），就必须立刻停止入队，而且要在持久化用户消息/
	// 附件/任务之前失败。被挡时返回结构化的、可枚举的安全原因，输入框可解释而
	// 不泄漏 private-agent 细节。
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	if !h.canInvokeAgent(r.Context(), agent, actorType, actorID, h.invokeOriginatorFromRequest(r, actorType, actorID), workspaceID) {
		h.writeDispatchBlocked(w, http.StatusForbidden, ReasonInvocationNotAllowed)
		return
	}

	// 在插入新行之前探测这是否为会话的首条人类消息。这把 LLM 自动起标题
	// （MUL-4295）限定在开场轮：只在第一条用户消息时把默认/原始标题升级一次，
	// 之后每次发送都不再重跑。此处的查询错误按「非首条」处理，直接跳过生成
	// （尽力而为——绝不阻塞发送）。
	hadUserMessage := true
	if existed, err := h.Queries.ChatSessionHasPublicUserMessage(r.Context(), session.ID); err == nil {
		hadUserMessage = existed
	}
	channelBacked := false
	channelSourceKnown := true
	if !hadUserMessage {
		if _, err := h.Queries.GetChannelChatSessionBindingBySessionAny(r.Context(), session.ID); err == nil {
			channelBacked = true
		} else if !errors.Is(err, pgx.ErrNoRows) {
			// 标题生成是可选的。来源权威不可用时跳过，而不是冒险把频道的
			// 手工改名当成自动生成的兜底标题覆盖掉。
			channelSourceKnown = false
		}
	}

	// 整个轮次原子持久化（MUL-4351）：所属任务、绑定到该任务（一存在即属任务
	// 不可变输入批次）的用户消息、附件绑定与会话触摸一起提交，daemon 只在提交
	// 后被告知。Web 聊天中发送方即已鉴权请求用户（会话只归创建者），因此他们是
	// 任务发起者——以 `## Task Initiator` 暴露给 agent。actorType/actorID 已在上方
	// 为 invoke 门控解析。
	healthReportIDs, valid := parseUUIDSliceOrBadRequest(w, req.HealthReportIDs, "health_report_ids")
	if !valid {
		return
	}
	for _, id := range healthReportIDs {
		var owned bool
		if err := h.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM health_report WHERE id=$1 AND workspace_id=$2 AND user_id=$3)`, id, workspaceID, userID).Scan(&owned); err != nil || !owned {
			writeError(w, 404, "体检报告不存在")
			return
		}
	}
	sent, err := h.TaskService.SendDirectChatMessage(r.Context(), session, agent, parseUUID(userID), req.Content, attachmentIDs, actorType, parseUUID(actorID), healthReportIDs)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrChatSessionArchived):
			writeError(w, http.StatusConflict, "chat session is archived")
		case errors.Is(err, service.ErrChatTaskAgentArchived):
			writeError(w, http.StatusConflict, "chat agent is archived")
		case errors.Is(err, service.ErrChatTaskAgentNoRuntime):
			writeError(w, http.StatusConflict, "chat agent has no runtime")
		default:
			slog.Warn("send chat message failed", append(logger.RequestAttrs(r), "error", err)...)
			writeError(w, http.StatusInternalServerError, "failed to send chat message")
		}
		return
	}
	msg := sent.Message
	task := sent.Task
	currentTitle := session.Title
	if sent.InitialTitle != "" {
		currentTitle = sent.InitialTitle
		h.ChannelChatTitleInitialized(session.WorkspaceID, session.CreatorID, session.ID, sent.InitialTitle)
	}

	// 服务端实际绑定的附件 id。请求了却未绑定的 id 会暴露给客户端，让它提醒
	// 用户（见 SendChatMessageResponse）。
	var boundAttachmentIDs []string
	if len(attachmentIDs) > 0 {
		boundAttachmentIDs = make([]string, 0, len(sent.BoundAttachmentIDs))
		for _, id := range sent.BoundAttachmentIDs {
			boundAttachmentIDs = append(boundAttachmentIDs, uuidToString(id))
		}
	}

	taskContext := h.TaskService.AnalyticsContextForTask(r.Context(), task)
	platform, _, _ := middleware.ClientMetadataFromContext(r.Context())
	obsmetrics.RecordEvent(h.Analytics, h.Metrics, analytics.ChatMessageSent(
		userID,
		workspaceID,
		uuidToString(session.ID),
		uuidToString(task.ID),
		uuidToString(session.AgentID),
		taskContext.RuntimeMode,
		taskContext.Provider,
		platform,
	))

	// 只广播其事务已提交的信封。旧测试夹具可能不建 outbox 行，因此保留内存兜底。
	resolvedSessionID := uuidToString(session.ID)
	if sent.MessageEvent.ID != "" {
		h.publishEvent(sent.MessageEvent)
	} else {
		h.publishChat(protocol.EventChatMessage, workspaceID, "member", userID, resolvedSessionID, protocol.ChatMessagePayload{
			ChatSessionID: resolvedSessionID,
			MessageID:     uuidToString(msg.ID),
			Role:          "user",
			Content:       req.Content,
			TaskID:        uuidToString(task.ID),
			CreatedAt:     timestampToString(msg.CreatedAt),
		})
	}

	// 首条用户消息 → 触发尽力而为的 LLM 自动起标题（MUL-4295）。即发即忘、非阻塞：
	// 无论标题是否生成都会写出下方响应；LLM 层被禁用/出错时静默保留由首条消息
	// 推导出的原标题。session.Title 是在此观察到的默认/原标题，驱动 CAS，确保生成
	// 中途的手工改名永不被覆盖。
	shouldGenerateTitle := shouldGenerateFirstMessageTitle(hadUserMessage, currentTitle, sent.InitialTitle, channelBacked, channelSourceKnown)
	if shouldGenerateTitle {
		h.maybeGenerateChatTitleAsync(workspaceID, userID, session.ID, currentTitle, req.Content)
	}

	writeJSON(w, http.StatusCreated, SendChatMessageResponse{
		MessageID:     uuidToString(msg.ID),
		TaskID:        uuidToString(task.ID),
		SupportsQueue: true,
		Queued:        sent.Queued,
		CreatedAt:     timestampToString(task.CreatedAt),
		AttachmentIDs: boundAttachmentIDs,
	})
}

func shouldGenerateFirstMessageTitle(hadUserMessage bool, currentTitle, initializedTitle string, channelBacked, channelSourceKnown bool) bool {
	if hadUserMessage || currentTitle == "" || !channelSourceKnown {
		return false
	}
	if channelBacked {
		// 对显式空的 /new Chat，事先存在的非空标题是手工改名。只有本次发送初始
		// 化的标题才配被尽力而为的 LLM 替换；否则手工命名永远赢。
		return initializedTitle != ""
	}
	return true
}

// ChatMessagesCursorResponse 是消息翻页游标：按 (created_at, id) 组合定位
// 下一页的起点，避免同一时间戳下的歧义。
type ChatMessagesCursorResponse struct {
	CreatedAt string `json:"created_at"`
	ID        string `json:"id"`
}

// ChatMessagesPageResponse 是一页消息记录及续翻信息（has_more/next_cursor）。
type ChatMessagesPageResponse struct {
	Messages   []ChatMessageResponse       `json:"messages"`
	Limit      int                         `json:"limit"`
	HasMore    bool                        `json:"has_more"`
	NextCursor *ChatMessagesCursorResponse `json:"next_cursor,omitempty"`
}

func parseChatMessagesPageParams(r *http.Request) (int, pgtype.Timestamptz, pgtype.UUID, error) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			return 0, pgtype.Timestamptz{}, pgtype.UUID{}, errors.New("invalid limit")
		}
		limit = parsed
	}

	rawBeforeCreatedAt := r.URL.Query().Get("before_created_at")
	rawBeforeID := r.URL.Query().Get("before_id")
	if rawBeforeCreatedAt == "" && rawBeforeID == "" {
		return limit, pgtype.Timestamptz{}, pgtype.UUID{}, nil
	}
	if rawBeforeCreatedAt == "" || rawBeforeID == "" {
		return 0, pgtype.Timestamptz{}, pgtype.UUID{}, errors.New("invalid cursor")
	}
	beforeTime, err := time.Parse(time.RFC3339Nano, rawBeforeCreatedAt)
	if err != nil {
		return 0, pgtype.Timestamptz{}, pgtype.UUID{}, errors.New("invalid cursor")
	}
	beforeID, err := util.ParseUUID(rawBeforeID)
	if err != nil {
		return 0, pgtype.Timestamptz{}, pgtype.UUID{}, errors.New("invalid cursor")
	}
	return limit, pgtype.Timestamptz{Time: beforeTime, Valid: true}, beforeID, nil
}

// RegenerateChatQuickActionsResponse 确认刷新请求已被接受。message_id 是被刷新
// 药丸所挂的 assistant 轮次——客户端把待处理占位符锚在它上面，等 chat:quick_actions
// 补充事件到达时解析。
// RegenerateChatQuickActionsRequest 指定客户端要刷新的 assistant 轮次。服务端在
// 入队前确认它仍是会话最新轮次（否则 409），让客户端待处理标记与 chat:quick_actions
// 将解析的轮次对齐——无需 ack 对账（MUL-5149）。
type RegenerateChatQuickActionsRequest struct {
	MessageID string `json:"message_id"`
}

// RegenerateChatQuickActionsResponse 确认刷新已被接受：message_id 是被刷新药丸
// 所挂的 assistant 轮次，客户端把待处理占位符锚在它上面等补充事件到达。
type RegenerateChatQuickActionsResponse struct {
	MessageID string `json:"message_id"`
}

// RegenerateChatQuickActions 在用户显式请求时（快捷操作行上的「刷新」按钮，
// MUL-5149）重跑会话最新 assistant 轮次的建议生成。生成在服务端，因此不产生
// agent 运行；刷新后的药丸走与自动轮次相同的 chat:quick_actions 实时路径送达。
func (h *Handler) RegenerateChatQuickActions(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	sessionID := chi.URLParam(r, "sessionId")

	session, ok := h.gatePublicChatSessionForUser(w, r, userID, workspaceID, sessionID)
	if !ok {
		return
	}
	if session.Status != "active" {
		writeError(w, http.StatusBadRequest, "chat session is archived")
		return
	}
	agent, err := h.Queries.GetAgent(r.Context(), session.AgentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load chat agent")
		return
	}
	if agent.ArchivedAt.Valid {
		writeError(w, http.StatusConflict, "chat agent is archived")
		return
	}
	// 刷新不再运行 agent，但它仍是对该 agent 会话的一次用户触发消费，因此它
	// 保持与发送相同的 INVOKE 门控（MUL-4525），而非 gatePublicChatSessionForUser
	// 里较软的可见门控。刻意不因生成移到服务端而放宽。
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	if !h.canInvokeAgent(r.Context(), agent, actorType, actorID, h.invokeOriginatorFromRequest(r, actorType, actorID), workspaceID) {
		h.writeDispatchBlocked(w, http.StatusForbidden, ReasonInvocationNotAllowed)
		return
	}

	var req RegenerateChatQuickActionsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	expectedMessageID, ok := parseUUIDOrBadRequest(w, req.MessageID, "message_id")
	if !ok {
		return
	}

	messageID, targetTask, err := h.TaskService.RegenerateChatQuickActions(r.Context(), session, expectedMessageID)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrChatQuickActionsStale):
			// 客户端要刷新的轮次不再是最新——它的视图过期了（有新回复到达）。
			// 409 → 客户端回滚乐观标记，并邀请用户在新轮次上刷新。
			writeError(w, http.StatusConflict, "a newer reply arrived — refresh it instead")
		case errors.Is(err, service.ErrChatQuickActionsBusy):
			// 该会话已有一轮在跑；它将替换这些药丸所挂的回复。409 → 客户端回滚，
			// 待会话稳定后可再刷新。
			writeError(w, http.StatusConflict, "still working — try refreshing in a moment")
		case errors.Is(err, service.ErrChatQuickActionsNoTurn):
			writeError(w, http.StatusConflict, "no assistant reply to refresh yet")
		case errors.Is(err, service.ErrChatQuickActionsUnavailable):
			writeFeatureDisabled(w, "suggestions_not_available", "suggestions are not available on this deployment")
		default:
			writeError(w, http.StatusInternalServerError, "failed to regenerate quick actions")
		}
		return
	}
	// 与自动轮次一样「分离」：先回 202，刷新后的药丸经 chat:quick_actions 送达。
	h.TaskService.GenerateChatQuickActionsAsync(targetTask, service.ChatQuickActionsRefresh)
	writeJSON(w, http.StatusAccepted, RegenerateChatQuickActionsResponse{
		MessageID: uuidToString(messageID),
	})
}

// ListChatMessages 处理 GET /api/chat/sessions/{sessionId}/messages——一次性返回
// 全部可见消息（过滤掉隐藏的 onboarding kickoff 行），并带上分组附件。旧客户端用；
// 新客户端用分页版。
func (h *Handler) ListChatMessages(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	sessionID := chi.URLParam(r, "sessionId")

	session, ok := h.gatePublicChatSessionForUser(w, r, userID, workspaceID, sessionID)
	if !ok {
		return
	}

	messages, err := h.Queries.ListChatMessages(r.Context(), session.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list chat messages")
		return
	}
	messages = visibleChatMessages(messages)

	messageIDs := make([]pgtype.UUID, len(messages))
	for i, m := range messages {
		messageIDs[i] = m.ID
	}
	groupedAtt := h.groupChatMessageAttachments(r.Context(), workspaceID, messageIDs)

	resp := make([]ChatMessageResponse, len(messages))
	for i, m := range messages {
		resp[i] = chatMessageToResponse(m, groupedAtt[uuidToString(m.ID)])
	}
	writeJSON(w, http.StatusOK, resp)
}

// ListChatMessagesPage 处理 GET /api/chat/sessions/{sessionId}/messages/page——
// 键集游标分页（before_created_at + before_id），每页 limit 条。
func (h *Handler) ListChatMessagesPage(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	sessionID := chi.URLParam(r, "sessionId")

	session, ok := h.gatePublicChatSessionForUser(w, r, userID, workspaceID, sessionID)
	if !ok {
		return
	}

	limit, beforeCreatedAt, beforeID, err := parseChatMessagesPageParams(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// 会话可含一条服务端撰写的 onboarding kickoff。在常规 lookahead 之外多取一
	// 行，隐藏它不会让可见页变短或丢下一个游标。
	messages, err := h.Queries.ListChatMessagesPage(r.Context(), db.ListChatMessagesPageParams{
		ChatSessionID:   session.ID,
		Limit:           int32(limit + 2),
		BeforeCreatedAt: beforeCreatedAt,
		BeforeID:        beforeID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list chat messages")
		return
	}
	messages = visibleChatMessages(messages)
	hasMore := len(messages) > limit
	if hasMore {
		messages = messages[:limit]
	}
	var nextCursor *ChatMessagesCursorResponse
	if hasMore && len(messages) > 0 {
		oldest := messages[len(messages)-1]
		nextCursor = &ChatMessagesCursorResponse{
			CreatedAt: oldest.CreatedAt.Time.Format(time.RFC3339Nano),
			ID:        uuidToString(oldest.ID),
		}
	}
	// SQL 先取最新窗口，空游标落在最近尾部。序列化前反转每一页游标，让视口内
	// 消息保持时间顺序。
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}

	messageIDs := make([]pgtype.UUID, len(messages))
	for i, m := range messages {
		messageIDs[i] = m.ID
	}
	groupedAtt := h.groupChatMessageAttachments(r.Context(), workspaceID, messageIDs)

	resp := make([]ChatMessageResponse, len(messages))
	for i, m := range messages {
		resp[i] = chatMessageToResponse(m, groupedAtt[uuidToString(m.ID)])
	}
	writeJSON(w, http.StatusOK, ChatMessagesPageResponse{
		Messages:   resp,
		Limit:      limit,
		HasMore:    hasMore,
		NextCursor: nextCursor,
	})
}

// PendingChatTaskResponse 由 GetPendingChatTask 返回——当前在途任务的 id/状态，
// 或没有任何活动任务时的空对象。CreatedAt 是前端给聊天 StatusPill 计时的锚点
// （已过秒数 = now - CreatedAt）。它必须来自服务端，因为乐观种子没有真实的任务
// created_at，计时器还要在刷新/重开后存活。
type PendingChatTaskResponse struct {
	TaskID    string `json:"task_id,omitempty"`
	Status    string `json:"status,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	// WaitReason 解释 waiting_local_directory 停驻：任务停在哪目录、已知时持有者
	// 任务的短 id。只在该状态下发出——其他状态下该列仍带着上次停驻的文本，若照
	// 发会被读成对运行正常任务的实时解释。旧服务器上缺席，客户端渲染为现在的
	// 裸 "Waiting for local directory"。
	WaitReason    string                   `json:"wait_reason,omitempty"`
	SupportsQueue bool                     `json:"supports_queue"`
	QueuedTasks   []QueuedChatTaskResponse `json:"queued_tasks,omitempty"`
}

// waitReasonForStatus 用状态门控存储的停驻文本。wait_reason 在任务恢复时从不
// 清除——daemon 在进入停驻时写一次——无条件返回会把「被任务 abc12345 停驻」挂到
// 已运行十分钟的任务上。
func waitReasonForStatus(status string, reason pgtype.Text) string {
	if status != "waiting_local_directory" || !reason.Valid {
		return ""
	}
	return strings.TrimSpace(reason.String)
}

// QueuedChatTaskResponse 是聊天任务入队时的响应（agent 忙碌、任务排队等待）。
type QueuedChatTaskResponse struct {
	TaskID    string `json:"task_id"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	MessageID string `json:"message_id,omitempty"`
	Content   string `json:"content,omitempty"`
}

// PrioritizeQueuedChatTaskResponse 是把某排队任务提到队首后的确认。
type PrioritizeQueuedChatTaskResponse struct {
	TaskID       string `json:"task_id"`
	ActiveTaskID string `json:"active_task_id,omitempty"`
}

// MarkChatSessionRead 清除会话的 unread_since（→has_unread=false），并广播
// chat:session_read，让同一用户的其他设备也去掉徽标。
func (h *Handler) MarkChatSessionRead(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	sessionID := chi.URLParam(r, "sessionId")

	session, ok := h.gatePublicChatSessionForUser(w, r, userID, workspaceID, sessionID)
	if !ok {
		return
	}

	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)
	if _, err := qtx.LockChatSessionForRuntimeBind(r.Context(), session.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to lock chat session")
		return
	}
	if err := qtx.MarkChatSessionRead(r.Context(), session.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to mark session read")
		return
	}
	updated, err := qtx.GetChatSession(r.Context(), session.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to reload chat session")
		return
	}

	event, err := recordChatEventTx(r.Context(), qtx, buildChatSessionReadEvent(updated, "member", userID))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record chat session event")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit session read")
		return
	}
	h.publishEvent(event)

	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// 延迟取消的草稿恢复 (#5219)
// ---------------------------------------------------------------------------

// ChatDraftRestoreResponse 是一条可恢复的输入框草稿：一次延迟取消在取消 HTTP
// 响应已返回后以空转录结算，被删的提示词因此留在服务端，直到创建者的客户端
// 应用并消费它。附件在读取时按存储 id 解析，携带常规的 handler URL 策略。
type ChatDraftRestoreResponse struct {
	ID            string               `json:"id"`
	ChatSessionID string               `json:"chat_session_id"`
	TaskID        string               `json:"task_id"`
	Content       string               `json:"content"`
	Attachments   []AttachmentResponse `json:"attachments,omitempty"`
	CreatedAt     string               `json:"created_at"`
}

// ChatDraftRestoresResponse 是一次查询返回的、会话待恢复草稿的列表容器。
type ChatDraftRestoresResponse struct {
	Restores []ChatDraftRestoreResponse `json:"restores"`
}

// ListChatDraftRestores 返回会话待处理的草稿恢复。经 loadChatSessionForUser
// 仅限创建者——刻意不用 private-agent 门控：内容是调用者自己被删的提示词，失去
// agent 访问权不该让它永远搁在服务端。
func (h *Handler) ListChatDraftRestores(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	sessionID := chi.URLParam(r, "sessionId")

	session, ok := h.loadChatSessionForUser(w, r, userID, workspaceID, sessionID)
	if !ok {
		return
	}

	rows, err := h.Queries.ListChatDraftRestoresBySession(r.Context(), session.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list draft restores")
		return
	}

	var attachmentIDs []pgtype.UUID
	for _, row := range rows {
		attachmentIDs = append(attachmentIDs, row.AttachmentIds...)
	}
	attachmentsByID := map[string]AttachmentResponse{}
	if len(attachmentIDs) > 0 {
		attRows, err := h.Queries.ListAttachmentsByIDs(r.Context(), db.ListAttachmentsByIDsParams{
			AttachmentIds: attachmentIDs,
			WorkspaceID:   session.WorkspaceID,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load draft restore attachments")
			return
		}
		attachmentMode := attachmentURLModeFromRequest(r)
		for _, a := range attRows {
			attachmentsByID[uuidToString(a.ID)] = h.attachmentToResponse(a, attachmentMode)
		}
	}

	resp := ChatDraftRestoresResponse{Restores: make([]ChatDraftRestoreResponse, 0, len(rows))}
	for _, row := range rows {
		item := ChatDraftRestoreResponse{
			ID:            uuidToString(row.ID),
			ChatSessionID: uuidToString(row.ChatSessionID),
			TaskID:        uuidToString(row.TaskID),
			Content:       row.Content,
			CreatedAt:     timestampToString(row.CreatedAt),
		}
		for _, attID := range row.AttachmentIds {
			if a, ok := attachmentsByID[uuidToString(attID)]; ok {
				item.Attachments = append(item.Attachments, a)
			}
		}
		resp.Restores = append(resp.Restores, item)
	}
	writeJSON(w, http.StatusOK, resp)
}

// ConsumeChatDraftRestore 在创建者客户端应用一条草稿恢复后删除它。幂等：消费
// 已消费（或从不存在的）恢复仍返回 204，响应丢失后的重试不会难倒客户端。
func (h *Handler) ConsumeChatDraftRestore(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	sessionID := chi.URLParam(r, "sessionId")

	session, ok := h.loadChatSessionForUser(w, r, userID, workspaceID, sessionID)
	if !ok {
		return
	}
	restoreUUID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "restoreId"), "restore id")
	if !ok {
		return
	}

	if _, err := h.Queries.DeleteChatDraftRestore(r.Context(), db.DeleteChatDraftRestoreParams{
		ID:            restoreUUID,
		ChatSessionID: session.ID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to consume draft restore")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// PendingChatTasksResponse 是 FAB 消费的聚合视图。
type PendingChatTasksResponse struct {
	Tasks []PendingChatTaskItem `json:"tasks"`
}

// PendingChatTaskItem 是一条在途聊天任务的摘要（任务/会话维度）。
type PendingChatTaskItem struct {
	TaskID        string `json:"task_id"`
	Status        string `json:"status"`
	ChatSessionID string `json:"chat_session_id"`
}

// CancelledChatMessageResponse 描述用户主动取消时被一并作废的聊天消息，可带回
// 输入框（restore_to_input=true，延迟取消的草稿恢复）。
type CancelledChatMessageResponse struct {
	ChatSessionID  string               `json:"chat_session_id"`
	MessageID      string               `json:"message_id"`
	Content        string               `json:"content"`
	RestoreToInput bool                 `json:"restore_to_input"`
	Attachments    []AttachmentResponse `json:"attachments,omitempty"`
}

// CancelTaskByUserResponse 是用户取消任务的响应：内嵌 AgentTaskResponse 表示
// 任务终态；当该任务带着一条被取消的聊天消息时附带 CancelledChatMessage。
type CancelTaskByUserResponse struct {
	AgentTaskResponse
	CancelledChatMessage *CancelledChatMessageResponse `json:"cancelled_chat_message,omitempty"`
}

// ListPendingChatTasks 返回当前用户在本工作区拥有的每个在途聊天任务。驱动 FAB
// 在聊天窗口关闭时的「运行中」指示（没有订阅任何逐会话查询）。属于已失去访问权
// 的 private agent 的任务被从响应中丢弃。
func (h *Handler) ListPendingChatTasks(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())

	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	allowed, ok := h.accessibleAgentIDs(r.Context(), workspaceID, actorType, actorID, member.Role)
	if !ok {
		writeError(w, http.StatusInternalServerError, "failed to resolve agent access")
		return
	}

	// 没有可访问的 agent → 反正每行都会被滤掉。跳过 DB 往返直接返回空列表
	// （镜像 HasPendingChatTasks）。
	if len(allowed) == 0 {
		writeJSON(w, http.StatusOK, PendingChatTasksResponse{Tasks: []PendingChatTaskItem{}})
		return
	}

	rows, err := h.Queries.ListPendingChatTasksByCreator(r.Context(), db.ListPendingChatTasksByCreatorParams{
		WorkspaceID: parseUUID(workspaceID),
		CreatorID:   parseUUID(userID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list pending chat tasks")
		return
	}

	// 待处理查询现在逐行返回 cs.agent_id，因此可直接拿已加载的 `allowed` 集合滤
	// 掉已失去访问权的 private agent 的任务——这条热路径上不再有第二次
	// ListAllChatSessionsByCreator 扫描（MUL-4159）。
	items := make([]PendingChatTaskItem, 0, len(rows))
	for _, row := range rows {
		agentID := uuidToString(row.AgentID)
		if _, ok := allowed[agentID]; !ok {
			continue
		}
		items = append(items, PendingChatTaskItem{
			TaskID:        uuidToString(row.TaskID),
			Status:        row.Status,
			ChatSessionID: uuidToString(row.ChatSessionID),
		})
	}
	writeJSON(w, http.StatusOK, PendingChatTasksResponse{Tasks: items})
}

// HasPendingChatTasksResponse 是 FAB 消费的布尔快路径负载——它只需知道是否存在
// 任何在途聊天任务，不需要完整列表。
type HasPendingChatTasksResponse struct {
	HasPending bool `json:"has_pending"`
}

// HasPendingChatTasks 用单条 EXISTS 查询回答「当前用户在本工作区是否有在途聊天
// 任务」，供 FAB 的运行指示器用。它是 ListPendingChatTasks 的布尔兄弟：详细列表
// 留给 ChatWindow 历史/停止任务流程。
//
// 权限过滤端到端保留：调用者当前可见的 agent 集合按与 ListPendingChatTasks 相同
// 的方式解析并作为 agent_ids 推进查询，因此失去 private agent 访问权的成员绝不
// 会因那台 agent 上的任务看到 true（MUL-4159）。可访问 agent 集合为空时短路为
// false，不打 DB。
func (h *Handler) HasPendingChatTasks(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())

	member, ok := h.workspaceMember(w, r, workspaceID)
	if !ok {
		return
	}
	actorType, actorID := h.resolveActor(r, userID, workspaceID)
	allowed, ok := h.accessibleAgentIDs(r.Context(), workspaceID, actorType, actorID, member.Role)
	if !ok {
		writeError(w, http.StatusInternalServerError, "failed to resolve agent access")
		return
	}

	// 没有可访问的 agent → 调用者可看到的任何东西都不可能在途。跳过往返返回
	// false。
	if len(allowed) == 0 {
		writeJSON(w, http.StatusOK, HasPendingChatTasksResponse{HasPending: false})
		return
	}

	agentIDs := make([]pgtype.UUID, 0, len(allowed))
	for id := range allowed {
		agentIDs = append(agentIDs, parseUUID(id))
	}

	hasPending, err := h.Queries.HasPendingChatTasksByCreator(r.Context(), db.HasPendingChatTasksByCreatorParams{
		WorkspaceID: parseUUID(workspaceID),
		CreatorID:   parseUUID(userID),
		AgentIds:    agentIDs,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check pending chat tasks")
		return
	}

	writeJSON(w, http.StatusOK, HasPendingChatTasksResponse{HasPending: hasPending})
}

// GetPendingChatTask 返回当前任务加其有序后继。根字段保留原响应形状，旧客户端
// 刷新/重开后仍能恢复单个待处理任务。
func (h *Handler) GetPendingChatTask(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	sessionID := chi.URLParam(r, "sessionId")

	session, ok := h.gatePublicChatSessionForUser(w, r, userID, workspaceID, sessionID)
	if !ok {
		return
	}

	tasks, err := h.Queries.ListPendingChatTasksForSession(r.Context(), session.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list pending chat tasks")
		return
	}
	if len(tasks) == 0 {
		writeJSON(w, http.StatusOK, PendingChatTaskResponse{SupportsQueue: true})
		return
	}

	head := tasks[0]
	queued := make([]QueuedChatTaskResponse, 0, len(tasks)-1)
	for _, task := range tasks[1:] {
		if task.Status != "queued" {
			continue
		}
		queued = append(queued, QueuedChatTaskResponse{
			TaskID:    uuidToString(task.ID),
			Status:    task.Status,
			CreatedAt: timestampToString(task.CreatedAt),
			MessageID: uuidToString(task.MessageID),
			Content:   task.Content,
		})
	}

	writeJSON(w, http.StatusOK, PendingChatTaskResponse{
		TaskID:        uuidToString(head.ID),
		Status:        head.Status,
		CreatedAt:     timestampToString(head.CreatedAt),
		WaitReason:    waitReasonForStatus(head.Status, head.WaitReason),
		SupportsQueue: true,
		QueuedTasks:   queued,
	})
}

// PrioritizeQueuedChatTask 把一条排队后继提到其 FIFO 同伴之前。客户端随后经既有
// 取消端点取消当前任务，保留该路径的转录与草稿恢复语义。
func (h *Handler) PrioritizeQueuedChatTask(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	sessionID := chi.URLParam(r, "sessionId")
	taskID, ok := parseUUIDOrBadRequest(w, chi.URLParam(r, "taskId"), "task id")
	if !ok {
		return
	}

	session, ok := h.gatePublicChatSessionForUser(w, r, userID, workspaceID, sessionID)
	if !ok {
		return
	}
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to start prioritize transaction")
		return
	}
	defer tx.Rollback(r.Context())
	qtx := h.Queries.WithTx(tx)

	// ClaimTask 在挑选工作前也取同一行锁。在这里持有它，让「优先 + 上报活动任务」
	// 成为一次服务端权威决策，而不是两个客户端请求与 daemon 竞速。
	if _, err := qtx.GetAgentForClaimUpdate(r.Context(), session.AgentID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to lock chat agent")
		return
	}
	prioritized, err := qtx.PrioritizeQueuedChatTask(
		r.Context(),
		db.PrioritizeQueuedChatTaskParams{ID: taskID, ChatSessionID: session.ID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		// 该比较-交换既拒绝过期的队列行，也拒绝没有可替换的已认领回复的有效后继。
		// 对当前客户端区分两者，同时保持相同的 409 兼容契约。
		queuedTask, loadErr := qtx.GetAgentTask(r.Context(), taskID)
		if loadErr != nil && !errors.Is(loadErr, pgx.ErrNoRows) {
			writeError(w, http.StatusInternalServerError, "failed to load queued task")
			return
		}
		if loadErr == nil &&
			queuedTask.Status == "queued" &&
			queuedTask.ChatSessionID.Valid &&
			uuidToString(queuedTask.ChatSessionID) == uuidToString(session.ID) {
			writeError(w, http.StatusConflict, "there is no active reply to replace")
			return
		}
		writeError(w, http.StatusConflict, "task is no longer queued")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to prioritize queued task")
		return
	}
	queuedTask, err := qtx.GetAgentTask(r.Context(), taskID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load prioritized task")
		return
	}
	queueEvent, err := service.RecordDurableEventTx(r.Context(), qtx, events.Event{
		Type:           protocol.EventTaskQueued,
		IdempotencyKey: "task:queued:" + uuidToString(queuedTask.ID) + ":prioritized",
		StreamKey:      "task:" + uuidToString(queuedTask.ID),
		WorkspaceID:    workspaceID,
		ActorType:      "member",
		ActorID:        userID,
		TaskID:         uuidToString(queuedTask.ID),
		ChatSessionID:  uuidToString(session.ID),
		Payload: map[string]any{
			"task_id":         uuidToString(queuedTask.ID),
			"agent_id":        uuidToString(queuedTask.AgentID),
			"status":          queuedTask.Status,
			"chat_session_id": uuidToString(session.ID),
			"queue_action":    "prioritized",
		},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record queued task priority")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to commit queued task priority")
		return
	}
	h.TaskService.PublishCommittedEvent(queueEvent)

	writeJSON(w, http.StatusOK, PrioritizeQueuedChatTaskResponse{
		TaskID:       uuidToString(prioritized.TaskID),
		ActiveTaskID: uuidToString(prioritized.ActiveTaskID),
	})
}

// ClearQueuedChatTasks 取消所有排队后继，不碰会话当前的「位置头」任务，即便该头
// 尚未被认领。
func (h *Handler) ClearQueuedChatTasks(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	sessionID := chi.URLParam(r, "sessionId")
	session, ok := h.gatePublicChatSessionForUser(w, r, userID, workspaceID, sessionID)
	if !ok {
		return
	}
	if err := h.TaskService.CancelQueuedChatTasks(r.Context(), session.ID, session.AgentID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to clear queued tasks")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// 任务取消（面向用户，带归属检查）
// ---------------------------------------------------------------------------

// CancelTaskByUser 取消调用者有权在当前工作区操作的任务。
//
// 租户隔离统一经任务所属 agent 强制：每条 agent_task_queue 行都带 NOT NULL 的
// agent_id（ON DELETE CASCADE，agent 恒存在），而 agent 是工作区作用域的。
// GetAgentTaskInWorkspace 因此是唯一租户守卫，无论设置了哪个可选源外键
// （issue / chat_session / autopilot_run）都适用——这正是 run_only autopilot 任务
// 与 quick_create 任务（其 issue 尚不存在）能取消的原因。只按 issue_id /
// chat_session_id 取消正是之前让这些任务 404 的做法（MUL-2827）。
//
// 在租户之上叠加两层隐私模型：
//   - 聊天任务对发起会话的成员私有，只有该创建者可取消；
//   - 其余任务暴露在 agent Activity 页与工作区任务快照上，两者都向无权限成员
//     隐藏 private agent。取消经 canAccessPrivateAgent 镜像该门控，让这个仅
//     id 的端点永不比暴露任务的界面更宽松。
func (h *Handler) CancelTaskByUser(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	workspaceID := ctxWorkspaceID(r.Context())
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	taskID := chi.URLParam(r, "taskId")
	taskUUID, ok := parseUUIDOrBadRequest(w, taskID, "task id")
	if !ok {
		return
	}

	task, err := h.Queries.GetAgentTaskInWorkspace(r.Context(), db.GetAgentTaskInWorkspaceParams{
		ID:          taskUUID,
		WorkspaceID: wsUUID,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}

	var (
		queuedOnly      bool
		expectedSession pgtype.UUID
		queueAction     string
	)
	if expectedStatus := r.URL.Query().Get("expected_status"); expectedStatus != "" {
		if expectedStatus != "queued" {
			writeError(w, http.StatusBadRequest, "expected_status must be queued")
			return
		}
		sessionID := r.URL.Query().Get("chat_session_id")
		var ok bool
		expectedSession, ok = parseUUIDOrBadRequest(w, sessionID, "chat_session_id")
		if !ok {
			return
		}
		if !task.ChatSessionID.Valid || task.ChatSessionID != expectedSession {
			writeError(w, http.StatusConflict, "task does not belong to the expected chat session")
			return
		}
		queueAction = r.URL.Query().Get("queue_action")
		if queueAction != "edit" && queueAction != "remove" {
			writeError(w, http.StatusBadRequest, "queue_action must be edit or remove")
			return
		}
		queuedOnly = true
	}

	if task.ChatSessionID.Valid {
		// 聊天隐私：即使工作区是共享的，也只有打开该会话的成员能取消其任务。
		cs, err := h.Queries.GetChatSessionInWorkspace(r.Context(), db.GetChatSessionInWorkspaceParams{
			ID:          task.ChatSessionID,
			WorkspaceID: wsUUID,
		})
		if err != nil {
			writeError(w, http.StatusNotFound, "task not found")
			return
		}
		if uuidToString(cs.CreatorID) != userID {
			writeError(w, http.StatusForbidden, "not your task")
			return
		}
	} else {
		// issue / autopilot / quick_create 任务都暴露在 agent Activity 页与工作区
		// 快照上，它们门控 private agent。这里镜像该门控。
		agent, err := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
			ID:          task.AgentID,
			WorkspaceID: wsUUID,
		})
		if isNotFound(err) {
			// 上游任务在模型调用 start 前尚无同步批次；用持久化自动化派发来源
			// 识别预启动任务，取消权限仍限定在同工作区管理员。
			managed, lookupErr := h.Queries.GetAgent(r.Context(), task.AgentID)
			if lookupErr == nil && managed.WorkspaceID == wsUUID && managed.Kind == "system" && upstream.IsExecutorSystemKey(managed.SystemKey.String) {
				var syncTask bool
				if lookupErr = h.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM upstream_sync_run WHERE task_id=$1)
 OR EXISTS(
 SELECT 1 FROM agent_task_queue t
 JOIN autopilot_run ar ON ar.id=t.autopilot_run_id AND ar.task_id=t.id
 JOIN autopilot ap ON ap.id=ar.autopilot_id
 JOIN upstream_sync_config c ON c.singleton=1 AND c.autopilot_id=ap.id
 WHERE t.id=$1 AND c.agent_id=t.agent_id AND c.runtime_id=t.runtime_id
 AND ap.workspace_id=$2 AND ap.execution_mode='run_only')`, task.ID, wsUUID).Scan(&syncTask); lookupErr != nil {
					writeError(w, http.StatusInternalServerError, "failed to load upstream task")
					return
				}
				if syncTask {
					if _, _, ok := h.upstreamScope(w, r, true); !ok {
						return
					}
					agent, err = managed, nil
				}
			}
		}
		if err != nil {
			writeError(w, http.StatusNotFound, "task not found")
			return
		}
		actorType, actorID := h.resolveActor(r, userID, workspaceID)
		if !h.canAccessPrivateAgent(r.Context(), agent, actorType, actorID, workspaceID) {
			writeError(w, http.StatusForbidden, "you do not have access to this agent")
			return
		}
	}

	cancelled, err := h.TaskService.CancelTaskWithResult(r.Context(), taskUUID, service.CancelTaskOptions{
		ClientSupportsDraftRestore: requestHasClientCapability(r, protocol.AppCapabilityChatDraftRestoreV1),
		QueuedOnly:                 queuedOnly,
		ExpectedChatSession:        expectedSession,
		QueueAction:                queueAction,
		UserInitiated:              true,
	})
	if errors.Is(err, service.ErrTaskNoLongerQueued) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	resp := CancelTaskByUserResponse{
		AgentTaskResponse: taskToResponse(cancelled.Task, workspaceID),
	}
	h.hydrateTaskAttributions(r.Context(), []*TaskAttribution{resp.AgentTaskResponse.Attribution})
	if cancelled.CancelledChatMessage != nil {
		attachments := make([]AttachmentResponse, 0, len(cancelled.CancelledChatMessage.Attachments))
		attachmentMode := attachmentURLModeFromRequest(r)
		for _, a := range cancelled.CancelledChatMessage.Attachments {
			attachments = append(attachments, h.attachmentToResponse(a, attachmentMode))
		}
		resp.CancelledChatMessage = &CancelledChatMessageResponse{
			ChatSessionID:  cancelled.CancelledChatMessage.ChatSessionID,
			MessageID:      cancelled.CancelledChatMessage.MessageID,
			Content:        cancelled.CancelledChatMessage.Content,
			RestoreToInput: cancelled.CancelledChatMessage.RestoreToInput,
			Attachments:    attachments,
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// ---------------------------------------------------------------------------
// 响应类型与辅助函数
// ---------------------------------------------------------------------------

// ChatSessionResponse 是会话的线格式（列表/详情通用）。
type ChatSessionResponse struct {
	ID          string  `json:"id"`
	WorkspaceID string  `json:"workspace_id"`
	AgentID     string  `json:"agent_id"`
	CreatorID   string  `json:"creator_id"`
	ProjectID   *string `json:"project_id"`
	Title       string  `json:"title"`
	Status      string  `json:"status"`
	// 仅列表端点填充——单会话取回返回 0/false/nil。
	// HasUnread 是为现有消费者保留的便捷字段（== UnreadCount > 0）。
	HasUnread   bool             `json:"has_unread"`
	UnreadCount int              `json:"unread_count"`
	LastMessage *ChatLastMessage `json:"last_message"`
	// Pinned 标记用户钉到列表顶部的聊天。由列表端点与置顶/取消置顶及单会话响应
	// 填充。
	Pinned bool `json:"pinned"`
	// ChannelSource 仅对从外部频道创建的 Chat 存在。
	// IsCurrentChannelRoute 区分当前路由世代与一个在 Multica 内仍可读写的旧 Chat。
	ChannelSource         *ChatSessionChannelSourceResponse `json:"channel_source,omitempty"`
	IsCurrentChannelRoute *bool                             `json:"is_current_channel_route,omitempty"`
	CreatedAt             string                            `json:"created_at"`
	UpdatedAt             string                            `json:"updated_at"`
}

// ChatSessionChannelSourceResponse 描述会话的外部频道来源。
type ChatSessionChannelSourceResponse struct {
	ChannelType    string `json:"channel_type"`
	InstallationID string `json:"installation_id"`
	RouteRevision  int64  `json:"route_revision"`
}

// hydrateChatSessionChannelMetadata 批量给会话补外部频道来源元数据：有绑定会话填
// ChannelSource + IsCurrentChannelRoute，无绑定的跳过。
func (h *Handler) hydrateChatSessionChannelMetadata(ctx context.Context, sessions []ChatSessionResponse) error {
	if len(sessions) == 0 {
		return nil
	}
	ids := make([]pgtype.UUID, 0, len(sessions))
	for _, session := range sessions {
		ids = append(ids, parseUUID(session.ID))
	}
	bindings, err := h.Queries.ListChannelChatSessionBindingsBySessions(ctx, ids)
	if err != nil {
		return err
	}
	bySession := make(map[string]db.ChannelChatSessionBinding, len(bindings))
	for _, binding := range bindings {
		bySession[uuidToString(binding.ChatSessionID)] = binding
	}
	for i := range sessions {
		binding, ok := bySession[sessions[i].ID]
		if !ok {
			continue
		}
		current := !binding.RetiredAt.Valid
		sessions[i].ChannelSource = &ChatSessionChannelSourceResponse{
			ChannelType: binding.ChannelType, InstallationID: uuidToString(binding.InstallationID), RouteRevision: binding.RouteRevision,
		}
		sessions[i].IsCurrentChannelRoute = &current
	}
	return nil
}

// ChatLastMessage 是会话最近一条消息的预览，用于渲染 IM 风会话列表（名字+摘要+
// 时间）。会话尚无消息时为 nil。
type ChatLastMessage struct {
	Content       string  `json:"content"`
	Role          string  `json:"role"`
	CreatedAt     string  `json:"created_at"`
	FailureReason *string `json:"failure_reason"`
	// MessageKind 是 'message'（默认）或 'no_response'。隐藏的 onboarding kickoff
	// 行让 buildChatLastMessage 返回 nil，从不暴露。
	MessageKind string `json:"message_kind"`
}

// buildChatLastMessage 由列表行列拼出预览；没有最后一条消息时返回 nil（LEFT JOIN
// 产生 NULL 时间戳）。
func buildChatLastMessage(at pgtype.Timestamptz, content, role string, failure pgtype.Text, kind string) *ChatLastMessage {
	if !at.Valid || kind == protocol.ChatMessageKindOnboardingKickoff {
		return nil
	}
	return &ChatLastMessage{
		Content:       content,
		Role:          role,
		CreatedAt:     timestampToString(at),
		FailureReason: textToPtr(failure),
		MessageKind:   normalizeMessageKind(kind),
	}
}

// ChatMessageResponse 是单条聊天消息的线格式。
type ChatMessageResponse struct {
	ID            string  `json:"id"`
	ChatSessionID string  `json:"chat_session_id"`
	Role          string  `json:"role"`
	Content       string  `json:"content"`
	TaskID        *string `json:"task_id"`
	CreatedAt     string  `json:"created_at"`
	// FailureReason 标记由 FailTask 的聊天兜底合成的 assistant 行。前端据此切到
	// 破坏性气泡。
	FailureReason *string `json:"failure_reason"`
	// ElapsedMs 是从任务创建到终态的实际墙钟时长。驱动「38 秒回复」/「12 秒后
	// 失败」字幕。
	ElapsedMs *int64 `json:"elapsed_ms"`
	// MessageKind 是增量字段。面向用户的列表 handler 过滤 onboarding kickoff 行；
	// 客户端仍理解该 kind 作为兼容守卫。
	MessageKind string `json:"message_kind"`
	// QuickActions 是随本 assistant 轮次生成的净化后继建议。旧行与用户消息恒为
	// 空数组。
	QuickActions []protocol.ChatQuickAction `json:"quick_actions"`
	// Attachments 经 chat_message_id 关联到本条消息。聊天气泡据此渲染文件卡片，
	// daemon claim 路径（daemon.go）也从同一来源拉结构化元数据，让 agent 执行
	// `multica attachment download <id>` 而非去猜可能过期的 markdown URL。
	Attachments []AttachmentResponse `json:"attachments,omitempty"`
}

func chatSessionToResponse(s db.ChatSession) ChatSessionResponse {
	return ChatSessionResponse{
		ID:          uuidToString(s.ID),
		WorkspaceID: uuidToString(s.WorkspaceID),
		AgentID:     uuidToString(s.AgentID),
		CreatorID:   uuidToString(s.CreatorID),
		ProjectID:   uuidToPtr(s.ProjectID),
		Title:       s.Title,
		Status:      s.Status,
		Pinned:      s.PinnedAt.Valid,
		CreatedAt:   timestampToString(s.CreatedAt),
		UpdatedAt:   timestampToString(s.UpdatedAt),
	}
}

func chatMessageToResponse(m db.ChatMessage, attachments []AttachmentResponse) ChatMessageResponse {
	return ChatMessageResponse{
		ID:            uuidToString(m.ID),
		ChatSessionID: uuidToString(m.ChatSessionID),
		Role:          m.Role,
		Content:       m.Content,
		TaskID:        uuidToPtr(m.TaskID),
		CreatedAt:     timestampToString(m.CreatedAt),
		FailureReason: textToPtr(m.FailureReason),
		ElapsedMs:     int8ToPtr(m.ElapsedMs),
		MessageKind:   normalizeMessageKind(m.MessageKind),
		QuickActions:  decodeChatQuickActions(m.QuickActions),
		Attachments:   attachments,
	}
}

// visibleChatMessages 移除发给 agent runtime 但永不属于成员可见对话的产品撰写
// 上下文。daemon 经 ListChatInputMessages 读取其任务作用域输入，因此该面向用户
// 的过滤不会把 kickoff 从 Mika 的执行里移除。
func visibleChatMessages(messages []db.ChatMessage) []db.ChatMessage {
	visible := make([]db.ChatMessage, 0, len(messages))
	for _, message := range messages {
		if message.MessageKind == protocol.ChatMessageKindOnboardingKickoff {
			continue
		}
		visible = append(visible, message)
	}
	return visible
}

// decodeChatQuickActions 解码存储的快捷操作 JSON，超限截断；缺失/损坏返回空数组。
func decodeChatQuickActions(raw []byte) []protocol.ChatQuickAction {
	actions := make([]protocol.ChatQuickAction, 0)
	if len(raw) == 0 {
		return actions
	}
	if err := json.Unmarshal(raw, &actions); err != nil {
		return []protocol.ChatQuickAction{}
	}
	if actions == nil {
		return []protocol.ChatQuickAction{}
	}
	if len(actions) > chatQuickActionResponseLimit {
		actions = actions[:chatQuickActionResponseLimit]
	}
	return actions
}

const chatQuickActionResponseLimit = 3

// normalizeMessageKind 把存储的 chat_message.message_kind 映射为 API 暴露的值。
// 未知/空 kind 降级为 'message'，未来的 kind 不会让旧客户端渲染崩坏（MUL-4351）。
func normalizeMessageKind(kind string) string {
	switch kind {
	case protocol.ChatMessageKindNoResponse:
		return protocol.ChatMessageKindNoResponse
	case protocol.ChatMessageKindOnboardingKickoff:
		return protocol.ChatMessageKindOnboardingKickoff
	case protocol.ChatMessageKindOnboardingOpening:
		return protocol.ChatMessageKindOnboardingOpening
	default:
		return protocol.ChatMessageKindMessage
	}
}
